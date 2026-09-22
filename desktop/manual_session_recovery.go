package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"reasonix/desktop/internal/sessionui"
)

func (a *App) startManualSessionTab(tab *WorkspaceTab) error {
	// Register cancellation before shutdown can freeze new worker admission.
	a.manualCreationMu.Lock()
	if a.shuttingDown.Load() {
		a.manualCreationMu.Unlock()
		return errors.New("application is shutting down")
	}
	a.mu.RLock()
	done, ready := tab.buildDone, tab.Ctrl != nil
	a.mu.RUnlock()
	if !ready && done == nil {
		a.startTabControllerBuild(tab)
		a.mu.RLock()
		done = tab.buildDone
		a.mu.RUnlock()
	}
	a.manualCreationMu.Unlock()
	if done != nil {
		<-done
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if tab.Ctrl == nil || tab.removed {
		return errors.New("session runtime did not start: " + tab.StartupErr)
	}
	return nil
}

func (a *App) failManualCreation(record sessionui.Record, view ManualSessionCreationView, cause error) {
	view.Phase, view.Error = "failed", sessionOperationErrorForTarget(cause, view.Ref.SessionID, view.OperationID).Error()
	payload, _ := json.Marshal(view)
	if _, err := a.sessionUIStore().Save(a.bootContext(), "creation", view.OperationID, record.Revision, payload); err != nil {
		slog.Warn("desktop: persist failed manual creation", "operation", view.OperationID, "err", err)
	}
}

func (a *App) startManualCreationWorker(record sessionui.Record) {
	a.manualCreationMu.Lock()
	defer a.manualCreationMu.Unlock()
	if a.shuttingDown.Load() {
		return
	}
	a.manualCreationTasks.Add(1)
	a.goSafe("manualSessionCreation", func() { defer a.manualCreationTasks.Done(); a.runManualSessionCreation(record) })
}

func (a *App) ListManualSessionCreations() ([]ManualSessionCreationView, error) {
	rows, err := a.sessionUIStore().List(a.bootContext(), "creation")
	if err != nil {
		return []ManualSessionCreationView{}, err
	}
	views := []ManualSessionCreationView{}
	for _, row := range rows {
		var view ManualSessionCreationView
		if err := json.Unmarshal(row.Payload, &view); err != nil {
			return views, err
		}
		if view.Phase != "ready" {
			views = append(views, view)
		}
	}
	return views, nil
}

func (a *App) reconcileManualSessionCreations() {
	select {
	case <-a.tabsRestoredSignal():
	case <-a.bootContext().Done():
		return
	}
	rows, err := a.sessionUIStore().List(a.bootContext(), "creation")
	if err != nil {
		slog.Warn("desktop: read pending manual creations", "err", err)
		return
	}
	for _, row := range rows {
		var view ManualSessionCreationView
		if json.Unmarshal(row.Payload, &view) != nil {
			continue
		}
		if view.Phase == "reserved" || view.Phase == "starting" {
			a.startManualCreationWorker(row)
		}
	}
}
