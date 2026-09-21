package main

import (
	"log/slog"
	"sync/atomic"

	"reasonix/desktop/internal/draftstate"
	"reasonix/desktop/internal/legacycleanup"
	"reasonix/internal/config"
)

// desktopPersistenceState groups process-lifetime stores and their recovery
// coordinator so App does not expose each lifecycle field independently.
type desktopPersistenceState struct {
	desktopSessions             desktopSessionState
	desktopDrafts               *draftstate.Store
	legacyCleanup               *legacycleanup.Store
	desktopMigrationDone        chan struct{}
	desktopMigrationFailed      atomic.Bool
	beforeSavedTabMigrationWait func()
	legacyCleanupWorker         legacyCleanupWorkerState
	historicalImports           historicalImportCoordinator
}

func newDesktopPersistenceState() desktopPersistenceState {
	return desktopPersistenceState{
		desktopSessions:      newDesktopSessionState(),
		desktopDrafts:        draftstate.New(config.DesktopDraftStatePath()),
		legacyCleanup:        legacycleanup.New(config.DesktopLegacyEmptySessionCleanupPath()),
		desktopMigrationDone: make(chan struct{}),
	}
}

func (a *App) registerLegacyCleanupUpgradeBatch() {
	if err := a.initializeLegacyEmptySessionCleanupBatch(); err != nil {
		slog.Warn("desktop: legacy empty session cleanup registration unavailable", "err", err)
	}
}

func (a *App) startDesktopPersistenceReconciliation() {
	a.goSafe("reconcileDraftSubmissions", func() {
		a.reconcileDraftSubmissionOperations()
	})
}
