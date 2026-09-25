package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

// Explicit source archives stage content as archived from the beginning. They
// must not use PrepareSession, which publishes an active/openable conversation.
func (a *App) archiveHistoricalSource(selector SessionSelector) (SessionMutationResult, error) {
	if selector.Source != nil && selector.Source.HostID != "" && selector.Source.HostID != localDesktopHostID {
		return SessionMutationResult{}, newSessionOperationError("unsupported", "This source belongs to another host.")
	}
	runtimeRelease, ok := a.tryLockRuntimeMutation("archive historical source")
	if !ok {
		return SessionMutationResult{}, sessionOperationErrorForTarget(errTopicArchiveBusy, "", "")
	}
	defer runtimeRelease()
	ctx, done, err := a.beginHistoricalRecovery()
	if err != nil {
		return SessionMutationResult{}, err
	}
	defer done()
	id, source, err := a.historicalSourceForSelector(selector)
	if err != nil {
		return SessionMutationResult{}, err
	}
	operationID := "archive-source-" + strings.TrimPrefix(newTabID(), "tab_")
	result, err := a.archiveHistoricalSourceWithOperation(ctx, id, source, operationID)
	if err != nil {
		slog.Warn("desktop: historical archive failed", "source_key", id, "operation", operationID, "err", err)
		return SessionMutationResult{}, sessionOperationErrorForTarget(err, id, operationID)
	}
	c := &a.historicalImports
	c.mu.Lock()
	for i := range c.catalog {
		if c.catalog[i].node.Source != nil && sameDesktopPath(c.catalog[i].node.Source.Path, source.path) {
			c.catalog[i].sourceChanged = false
		}
	}
	c.mu.Unlock()
	a.emitProjectTreeChanged()
	return result, nil
}

func (a *App) archiveHistoricalSourceWithOperation(ctx context.Context, id string, source historicalSource, operationID string) (SessionMutationResult, error) {
	release, err := acquireHistoricalSource(ctx, id, source)
	if err != nil {
		return SessionMutationResult{}, err
	}
	defer release()
	fingerprint, err := desktopSourceFingerprint(source.path)
	if err != nil {
		return SessionMutationResult{}, err
	}
	mapping, dependencies, err := a.stageHistoricalArchive(ctx, id, source, fingerprint)
	if err != nil {
		return SessionMutationResult{}, err
	}
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return SessionMutationResult{}, err
	}
	ref := session.SessionRef{HostID: localDesktopHostID, SessionID: mapping.SessionID}
	if len(dependencies) != 0 {
		operationID = "archive-" + dependencies[0]
	}
	lifecycle := state.SessionStates[ref.SessionID]
	outcome := "archived"
	if lifecycle.Lifecycle == workspacestate.Deleted {
		outcome = "already_removed"
	} else if lifecycle.Lifecycle == workspacestate.Archived {
		if _, err := a.desktopSessionService("").Query().Snapshot(ctx, ref); err != nil {
			return SessionMutationResult{}, err
		}
	} else if lifecycle.Lifecycle != workspacestate.Archived {
		verify := func(ctx context.Context, current workspacestate.State) error {
			fp, err := desktopSourceFingerprint(source.path)
			if err != nil || fp != fingerprint {
				return errors.Join(err, workspacestate.ErrMutationConflict)
			}
			for _, dependency := range dependencies {
				if err := a.validateHistoricalArchiveContent(ctx, source, current.PendingOperations[dependency]); err != nil {
					return err
				}
			}
			return nil
		}
		if err := a.archiveSessionRefsWithOperationConditional([]session.SessionRef{ref}, operationID, verify, dependencies...); err != nil {
			return SessionMutationResult{}, fmt.Errorf("commit historical archive: %w", err)
		}
		state, err = a.workspaceRegistry().Load(ctx)
		if err != nil {
			return SessionMutationResult{}, err
		}
		lifecycle = state.SessionStates[ref.SessionID]
	}
	if outcome != "already_removed" && source.format == "canonical" && ref.SessionID != filepath.Base(source.path) {
		outcome = "archived_copy"
	}
	projectionPending := false
	if outcome != "already_removed" {
		if err := a.applyHistoricalSourcePresentation(desktopSourceKey(source.path, source.head), ref); err != nil {
			// Archive is durable, just as import can succeed with presentation
			// pending. Retrying the source action reapplies its saved overlay.
			projectionPending = true
			slog.Warn("desktop: archived source presentation pending", "source_key", id, "operation", operationID, "err", err)
		}
	}
	target := SessionTarget{SessionRef: ref, Scope: source.scope, WorkspaceRoot: source.root}
	aliases := a.sessionTargetIdentityAliases(target)
	aliases = append(aliases, "source\x00local\x00"+id)
	return SessionMutationResult{TargetKey: target.key(), OperationID: operationID, Committed: true,
		Outcome: outcome, LifecycleGeneration: lifecycle.Generation, IdentityAliases: aliases, ProjectionPending: projectionPending}, nil
}
