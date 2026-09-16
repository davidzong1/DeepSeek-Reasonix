package main

import (
	"context"
	"errors"
	"fmt"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

// PurgeCanonicalSession permanently removes only archived sessions. Legacy
// migration originals are retained as upgrade evidence, with mappings acting
// as tombstones so background discovery cannot import them again.
func (a *App) PurgeCanonicalSession(ref session.SessionRef) error {
	if err := validateLocalSessionRef(ref); err != nil {
		return err
	}
	release := a.lockRuntimeMutation("purge archived session")
	defer release()
	if err := a.purgeCanonicalSession(a.bootContext(), ref); err != nil {
		return err
	}
	a.emitProjectTreeChanged()
	return nil
}

func (a *App) purgeCanonicalSession(ctx context.Context, ref session.SessionRef, expected ...uint64) error {
	store := a.workspaceRegistry()
	state, err := store.Load(ctx)
	if err != nil {
		return err
	}
	op, retry := state.PendingOperations["purge-"+ref.SessionID]
	if retry && op.Phase == "committed" {
		return nil
	}
	if !retry && state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Archived {
		return errors.New("only archived sessions can be permanently deleted")
	}
	if err := a.retireArchivedSessionRuntime(ctx, ref); err != nil {
		return fmt.Errorf("session runtime is still in use: %w", err)
	}
	filesystem := session.NewFilesystemPersistence(a.desktopSessions.root)
	if err := filesystem.PurgeWithTombstone(ctx, ref.SessionID, func() error {
		if err := store.BeginPurge(ctx, ref.SessionID, expected...); err != nil {
			return err
		}
		return store.AdvancePurge(ctx, ref.SessionID, "tombstoned")
	}); err != nil {
		return err
	}
	if err := store.AdvancePurge(ctx, ref.SessionID, "content_removed"); err != nil {
		return err
	}
	return store.CompletePurge(ctx, ref.SessionID)
}

// Client release may retain an idle runtime for fast navigation. Archive and
// purge must retire that cache entry; Service.Close still refuses bound clients
// and executing turns, so this cannot close a live user's session underneath it.
func (a *App) retireArchivedSessionRuntime(ctx context.Context, ref session.SessionRef) error {
	service := a.desktopSessionService("")
	if _, live := service.Runtime(ref); !live {
		return nil
	}
	err := service.Close(ctx, ref)
	if errors.Is(err, session.ErrSessionNotRunning) {
		return nil
	}
	return err
}
