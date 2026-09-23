package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	"reasonix/desktop/internal/legacycleanup"
	"reasonix/desktop/internal/workspacestate"
	filelock "reasonix/internal/identitylock"
	"reasonix/internal/session"
)

func migrationCheckpointPath(cp desktopMigrationCheckpoint) (string, string) {
	path := cp.files[0]
	if filepath.Base(path) == "manifest.json" {
		return filepath.Dir(path), "canonical"
	}
	return path, "legacy"
}

// Reconcile adoption independently of content comparison: the destination may
// have been continued, archived or purged since this receipt was written.
func (a *App) completeRegisteredMigration(ctx context.Context, source desktopMigrationSource, cp desktopMigrationCheckpoint, id, digest string) error {
	path, format := migrationCheckpointPath(cp)
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return err
	}
	if state.SessionStates[id].Lifecycle == workspacestate.Deleted {
		return cp.complete(id, digest)
	}
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: id}
	if _, err := a.desktopSessionService("").Query().Stat(ctx, ref); err != nil {
		if errors.Is(err, session.ErrSessionNotFound) {
			return a.sourceRecovery(ctx, path, format, "adopted_target_missing", source.scope, source.workspaceRoot, source.headID)
		}
		return err
	}
	workspace, err := a.ensureDesktopMigrationWorkspace(ctx, source)
	if err != nil {
		return err
	}
	fingerprint, err := desktopSourceFingerprint(path)
	if err != nil {
		return err
	}
	key := source.mappingKey(path)
	mapping, exists, err := state.ResolveSource(key)
	if err != nil {
		return err
	}
	if exists && source.operationID == "" {
		if mapping.SessionID != id {
			return workspacestate.ErrMutationConflict
		}
		if _, err := a.canonicalSessionWorkspace(ctx, ref); err == nil {
			return cp.complete(id, digest)
		}
	}
	if hook := a.desktopSessions.beforeMigrationRegistryCommit; hook != nil {
		if err := hook(); err != nil {
			return errors.Join(err, updateDesktopMigrationLedger(cp.key, id, "failed", "registry", digest))
		}
	}
	if err := a.commitDesktopImport(ctx, source, path, format, fingerprint, id, workspace); err != nil {
		return err
	}
	if format == "legacy" {
		if err := a.bindLegacyCleanupMigration(ctx, path, source.headID, id, workspace); err != nil &&
			!errors.Is(err, legacycleanup.ErrNotInitialized) && !errors.Is(err, errLegacyCleanupStateChanged) {
			slog.Warn("desktop: legacy cleanup migration binding unavailable")
		}
	}
	return cp.complete(id, digest)
}

func (a *App) prepareRegisteredMigration(ctx context.Context, source desktopMigrationSource, cp desktopMigrationCheckpoint, id, workspace string) error {
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return err
	}
	if _, exists := state.Workspaces[workspace]; !exists {
		return errors.Join(workspacestate.ErrWorkspaceNotFound, updateDesktopMigrationLedger(cp.key, id, "failed", "registry"))
	}
	path, _ := migrationCheckpointPath(cp)
	fingerprint, err := desktopSourceFingerprint(path)
	if err != nil {
		return err
	}
	_, err = a.prepareDesktopImport(ctx, source, path, fingerprint, id, workspace)
	return err
}

func (a *App) resolveRegisteredMigrationTarget(ctx context.Context, source desktopMigrationSource, cp desktopMigrationCheckpoint, preferredID, digest string) (string, bool, error) {
	path, _ := migrationCheckpointPath(cp)
	fingerprint, err := desktopSourceFingerprint(path)
	if err != nil {
		return "", false, err
	}
	return a.resolveDesktopImportTarget(ctx, a.desktopSessionService("").Query(), preferredID, cp.key, source.mappingKey(path), digest, path, fingerprint, source.headID)
}

func (a *App) quarantineChangedMigration(ctx context.Context, source desktopMigrationSource, cp desktopMigrationCheckpoint, digest string) (bool, error) {
	if source.operationID != "" || !cp.completed() || cp.matchesCompletedContent(digest) {
		return false, nil
	}
	path, format := migrationCheckpointPath(cp)
	// The old path receipt adopted the selected head, not every head. Adding
	// an independently identified head is discovery, not a changed adoption.
	if source.headID != "" && source.legacyAdoption != nil && cp.record.TargetSessionID == source.legacyAdoption.TargetSessionID {
		state, err := a.workspaceRegistry().Load(ctx)
		if err != nil {
			return true, err
		}
		if _, exists, err := state.ResolveSource(desktopSourceKey(path, source.headID)); err != nil {
			return true, err
		} else if !exists {
			return false, nil
		}
	}
	return true, a.sourceRecovery(ctx, path, format, "source_changed_after_adoption", source.scope, source.workspaceRoot, source.headID)
}

func lockDesktopMigrationLedger() (func(), error) {
	path := desktopMigrationLedgerPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	return filelock.TryAcquire(path + ".lock")
}

// Registry fingerprints distinguish a metadata-only stat change from a new
// historical version without converting or publishing the source again.
func (a *App) checkAdoptedMigrationSource(ctx context.Context, source desktopMigrationSource, cp desktopMigrationCheckpoint) (bool, error) {
	if !cp.completed() || source.operationID != "" {
		return false, nil
	}
	path, format := migrationCheckpointPath(cp)
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return true, err
	}
	if state.SessionStates[cp.record.TargetSessionID].Lifecycle == workspacestate.Deleted {
		return true, nil
	}
	mapping, exists, err := state.ResolveSource(source.mappingKey(path))
	if err != nil {
		return true, err
	}
	if !exists {
		return false, nil
	}
	fingerprint, err := desktopSourceFingerprint(path)
	if err != nil {
		return true, err
	}
	if mapping.Fingerprint == fingerprint {
		return true, a.completeRegisteredMigration(ctx, source, cp, mapping.SessionID, cp.record.ContentDigest)
	}
	return true, a.sourceRecovery(ctx, path, format, "source_changed_after_adoption", source.scope, source.workspaceRoot, source.headID)
}
