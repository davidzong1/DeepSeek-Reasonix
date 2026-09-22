package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"reasonix/desktop/internal/sessionui"
	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/identitylock"
	"reasonix/internal/session"
)

type ManualSessionCreationRequest struct {
	OperationID   string `json:"operationId"`
	WorkspaceID   string `json:"workspaceId"`
	Scope         string `json:"scope,omitempty"`
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
}

type ManualSessionCreationView struct {
	OperationID   string               `json:"operationId"`
	WorkspaceID   string               `json:"workspaceId"`
	Scope         string               `json:"scope"`
	WorkspaceRoot string               `json:"workspaceRoot"`
	Ref           session.SessionRef   `json:"ref"`
	TopicID       string               `json:"topicId"`
	Phase         string               `json:"phase"`
	Error         string               `json:"error,omitempty"`
	Settings      SessionDraftSettings `json:"settings"`
}

func (a *App) sessionUIStore() *sessionui.Store {
	a.sessionServicesMu.Lock()
	defer a.sessionServicesMu.Unlock()
	if a.sessionUI == nil {
		a.sessionUI = sessionui.New(config.DesktopSessionUIStatePath())
	}
	return a.sessionUI
}

func (a *App) BeginManualSessionCreation(req ManualSessionCreationRequest) (result ManualSessionCreationView, err error) {
	defer func() { err = sessionUIError(err, req.WorkspaceID, req.OperationID) }()
	if a.shuttingDown.Load() {
		return ManualSessionCreationView{}, errors.New("application is shutting down")
	}
	id := strings.TrimSpace(req.OperationID)
	if len(id) < 8 || len(id) > 128 {
		return ManualSessionCreationView{}, errors.New("invalid creation operation identity")
	}
	workspaceID := req.WorkspaceID
	if workspaceID == "" {
		var err error
		workspaceID, err = a.ensureDesktopWorkspace(a.bootContext(), req.Scope, req.WorkspaceRoot)
		if err != nil {
			return ManualSessionCreationView{}, err
		}
	}
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return ManualSessionCreationView{}, err
	}
	w, ok := state.Workspaces[workspaceID]
	if !ok {
		return ManualSessionCreationView{}, workspacestate.ErrWorkspaceNotFound
	}
	store := a.sessionUIStore()
	existing, err := store.Get(a.bootContext(), "creation", id)
	if err != nil {
		return ManualSessionCreationView{}, err
	}
	if existing.Revision != "0" {
		var view ManualSessionCreationView
		if err := json.Unmarshal(existing.Payload, &view); err != nil {
			return view, err
		}
		if view.WorkspaceID != workspaceID {
			return view, workspacestate.ErrMutationConflict
		}
		return view, nil
	}
	scope, root := canonicalWorkspaceScope(w), w.Root
	if scope == "global" {
		root = ""
	}
	sum := sha256.Sum256([]byte(id))
	view := ManualSessionCreationView{OperationID: id, WorkspaceID: workspaceID, Scope: scope, WorkspaceRoot: root,
		Ref:     session.SessionRef{HostID: localDesktopHostID, SessionID: fmt.Sprintf("desktop-manual-%x", sum[:16])},
		TopicID: fmt.Sprintf("manual-%x", sum[:16]), Phase: "reserved", Settings: a.defaultDraftSettings(scope, root)}
	view.Settings.ModelSource = draftModelSourceExplicit
	payload, err := json.Marshal(view)
	if err != nil {
		return view, err
	}
	record, err := store.Save(a.bootContext(), "creation", id, "0", payload)
	if errors.Is(err, sessionui.ErrConflict) {
		err = json.Unmarshal(record.Payload, &view)
		if err == nil && view.WorkspaceID != workspaceID {
			err = workspacestate.ErrMutationConflict
		}
		return view, err
	}
	if err != nil {
		return view, err
	}
	a.startManualCreationWorker(record)
	return view, nil
}

func (a *App) GetManualSessionCreation(operationID string) (result ManualSessionCreationView, err error) {
	defer func() { err = sessionUIError(err, "", operationID) }()
	r, err := a.sessionUIStore().Get(a.bootContext(), "creation", operationID)
	var view ManualSessionCreationView
	if err != nil {
		return view, err
	}
	if r.Revision == "0" {
		return view, errors.New("creation operation not found")
	}
	err = json.Unmarshal(r.Payload, &view)
	return view, err
}

func (a *App) RetryManualSessionCreation(operationID string) (result ManualSessionCreationView, err error) {
	defer func() { err = sessionUIError(err, "", operationID) }()
	r, err := a.sessionUIStore().Get(a.bootContext(), "creation", operationID)
	var view ManualSessionCreationView
	if err != nil {
		return view, err
	}
	if r.Revision == "0" {
		return view, errors.New("creation operation not found")
	}
	if err = json.Unmarshal(r.Payload, &view); err != nil {
		return view, err
	}
	if view.Phase != "failed" && view.Phase != "reserved" && view.Phase != "starting" {
		return view, nil
	}
	a.startManualCreationWorker(r)
	return view, nil
}

func (a *App) runManualSessionCreation(record sessionui.Record) {
	var view ManualSessionCreationView
	if json.Unmarshal(record.Payload, &view) != nil {
		return
	}
	lockRoot := filepath.Join(filepath.Dir(a.sessionUIStore().Path()), "manual-creation-locks")
	if err := os.MkdirAll(lockRoot, 0700); err != nil {
		a.failManualCreation(record, view, err)
		return
	}
	release, err := identitylock.TryAcquire(filepath.Join(lockRoot, view.Ref.SessionID+".lock"))
	if err != nil {
		if !errors.Is(err, identitylock.ErrHeld) {
			a.failManualCreation(record, view, err)
		}
		return
	}
	defer release()
	current, err := a.sessionUIStore().Get(a.bootContext(), "creation", view.OperationID)
	if err != nil || current.Revision != record.Revision {
		return
	}
	view.Phase, view.Error = "starting", ""
	payload, _ := json.Marshal(view)
	record, err = a.sessionUIStore().Save(a.bootContext(), "creation", view.OperationID, record.Revision, payload)
	if err != nil {
		return
	}
	err = a.createManualSessionRuntime(view)
	view.Phase = "ready"
	if err != nil {
		view.Phase, view.Error = "failed", sessionOperationErrorForTarget(err, view.Ref.SessionID, view.OperationID).Error()
	}
	payload, _ = json.Marshal(view)
	if _, saveErr := a.sessionUIStore().Save(a.bootContext(), "creation", view.OperationID, record.Revision, payload); saveErr != nil {
		slog.Warn("desktop: persist manual creation result", "operation", view.OperationID, "err", saveErr)
	}
	a.emitProjectTreeChanged()
}

func (a *App) createManualSessionRuntime(view ManualSessionCreationView) error {
	tab, err := a.reserveManualSessionTab(view)
	if err != nil {
		return err
	}
	err = a.startManualSessionTab(tab)
	if err == nil {
		a.mu.Lock()
		tab.PendingCreateOperationID = ""
		a.saveTabsLocked()
		a.mu.Unlock()
	}
	return err
}

func (a *App) reserveManualSessionTab(view ManualSessionCreationView) (*WorkspaceTab, error) {
	defer a.lockRuntimeMutation("reserve manual session")()
	if a.shuttingDown.Load() {
		return nil, errors.New("application is shutting down")
	}
	ctx := a.bootContext()
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return nil, err
	}
	if lifecycle := state.SessionStates[view.Ref.SessionID].Lifecycle; lifecycle == workspacestate.Archived || lifecycle == workspacestate.Deleted {
		return nil, workspacestate.ErrMutationConflict
	}
	w, ok := state.Workspaces[view.WorkspaceID]
	if !ok || !sameProjectRoot(w.Root, desktopWorkspaceRoot(view.Scope, view.WorkspaceRoot)) {
		return nil, workspacestate.ErrMutationConflict
	}
	if err := a.workspaceRegistry().BeginCreate(ctx, workspacestate.PendingCreate{OperationID: view.OperationID, WorkspaceID: view.WorkspaceID, SessionID: view.Ref.SessionID}); err != nil {
		return nil, err
	}
	service := a.desktopSessionService("")
	if _, err := service.Query().Stat(ctx, view.Ref); errors.Is(err, session.ErrSessionNotFound) {
		runtime, createErr := service.Create(ctx, session.CreateOptions{SessionID: view.Ref.SessionID, CWD: desktopWorkspaceRoot(view.Scope, view.WorkspaceRoot), Origin: session.SessionOriginNew})
		if createErr != nil {
			return nil, createErr
		}
		if err := service.SetModel(ctx, view.Ref, view.Settings.Model, ""); err != nil {
			return nil, err
		}
		if _, err := runtime.Session().Flush(ctx); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if err := a.validateDesktopWorkspaceMembership(ctx, view.WorkspaceID, view.Ref); err != nil {
		return nil, err
	}
	if err := a.workspaceRegistry().AttachSession(ctx, view.OperationID, view.WorkspaceID, view.Ref.SessionID, ""); err != nil {
		return nil, err
	}
	if err := a.workspaceRegistry().EnsureSessionTopic(ctx, view.Ref.SessionID, view.TopicID, ""); err != nil {
		return nil, err
	}
	a.emitProjectTreeChanged()
	if meta := a.metaForDraftSession(view.Ref.SessionID); meta != nil {
		tab, _ := a.tabAndCtrlByID(meta.ID)
		if tab != nil {
			return tab, nil
		}
	}
	settings := view.Settings
	tab := &WorkspaceTab{Scope: view.Scope, WorkspaceRoot: desktopWorkspaceRoot(view.Scope, view.WorkspaceRoot),
		SessionID: view.Ref.SessionID, TopicID: view.TopicID, TopicTitle: defaultTopicTitle, topicTitleSource: topicTitleSourceAuto,
		PendingCreateOperationID: view.OperationID, model: settings.Model, qualityFloor: settings.QualityFloor,
		mode:             tabModeFromAxes(tabModeHasPlan(settings.Mode), settings.ToolApprovalMode == control.ToolApprovalDangerFullAccess),
		toolApprovalMode: settings.ToolApprovalMode, disabledMCP: cloneServerViewMap(settings.DisabledMCP), mcpOrder: append([]string(nil), settings.MCPOrder...)}
	if settings.Effort != "" {
		effort := settings.Effort
		tab.effort = &effort
	}
	if err := createTopicState(view.WorkspaceRoot, view.TopicID, defaultTopicTitle, topicTitleSourceAuto, time.Now().UnixMilli()); err != nil {
		return nil, err
	}
	a.mu.Lock()
	tab.ID = a.newUniqueTabIDLocked()
	tab.sink = &tabEventSink{tabID: tab.ID, app: a}
	a.tabs[tab.ID] = tab
	a.tabOrder = append(a.tabOrder, tab.ID)
	a.saveTabsLocked()
	a.mu.Unlock()
	return tab, nil
}
