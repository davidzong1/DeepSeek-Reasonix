package main

import (
	"context"
	"errors"
	"strings"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/control"
	"reasonix/internal/session"
)

var errSessionWorkspaceConflict = errors.New("session workspace identity is inconsistent; the session files were left unchanged")

func controllerSessionDirectoryMatches(desiredDir, ctrlDir, path string) bool {
	if desiredDir == "" || sameDesktopPath(ctrlDir, desiredDir) {
		return true
	}
	if path == "" {
		return false
	}
	validPath, _, err := validateSessionPath(ctrlDir, path)
	return err == nil && sessionRuntimeKey(validPath) == sessionRuntimeKey(path)
}

// Resolve navigation from durable membership and the immutable header, never
// from the current surface. Both authorities must agree before execution.
func (a *App) canonicalSessionWorkspace(ctx context.Context, ref session.SessionRef) (workspacestate.Workspace, error) {
	if err := validateLocalSessionRef(ref); err != nil {
		return workspacestate.Workspace{}, err
	}
	info, err := a.desktopSessionService("").Query().Stat(ctx, ref)
	if err != nil {
		return workspacestate.Workspace{}, err
	}
	state, err := a.workspaceRegistry().Load(ctx)
	if err != nil {
		return workspacestate.Workspace{}, err
	}
	if state.SessionStates[ref.SessionID].Lifecycle == workspacestate.Deleted {
		return workspacestate.Workspace{}, session.ErrSessionNotFound
	}
	var owner workspacestate.Workspace
	for _, workspace := range state.Workspaces {
		for _, id := range workspace.SessionIDs {
			if id != ref.SessionID {
				continue
			}
			if owner.ID != "" {
				return owner, errSessionWorkspaceConflict
			}
			owner = workspace
		}
	}
	if owner.ID == "" || info.Origin == "" || strings.TrimSpace(info.CWD) == "" || !sameDesktopPath(info.CWD, owner.Root) {
		return owner, errSessionWorkspaceConflict
	}
	return owner, nil
}

func canonicalWorkspaceScope(workspace workspacestate.Workspace) string {
	if workspace.ID == workspacestate.GlobalWorkspaceID {
		return "global"
	}
	return "project"
}

func canonicalWorkspaceChanged(snap tabRuntimeSnapshot, workspace workspacestate.Workspace) bool {
	return snap.scope != canonicalWorkspaceScope(workspace) || !sameDesktopPath(desktopWorkspaceRoot(snap.scope, snap.workspaceRoot), workspace.Root)
}

// The caller holds App.mu and publishes the session/controller in this same
// critical section. Project-derived tab state cannot survive a project move.
func applyCanonicalWorkspaceLocked(tab *WorkspaceTab, workspace workspacestate.Workspace) {
	if canonicalWorkspaceChanged(snapshotTabRuntimeLocked(tab), workspace) {
		tab.TopicID, tab.TopicTitle, tab.topicTitleSource = "", "", ""
		tab.setPinnedFilesState(nil, nil)
	}
	tab.Scope, tab.WorkspaceRoot = canonicalWorkspaceScope(workspace), workspace.Root
	tab.SessionWorkspace.ID = workspace.ID
}

func canonicalSessionTopicIdentity(state workspacestate.State, sessionID string) (string, string) {
	presentation := state.Presentation[sessionID]
	topicID := strings.TrimSpace(presentation.TopicID)
	if topicID == "" {
		topicID = "canonical-" + sessionID
	}
	return topicID, presentation.Title
}

func (a *App) commitCanonicalSessionBinding(tab *WorkspaceTab, ctrl control.SessionAPI, ref session.SessionRef, workspace workspacestate.Workspace, navigation uint64) error {
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return err
	}
	topicID, topicTitle := canonicalSessionTopicIdentity(state, ref.SessionID)
	// Presentation supplies migration defaults; the session log owns titles
	// after a manual or AI rename, including an explicitly cleared title.
	if info, err := a.desktopSessionService("").Query().Stat(a.bootContext(), ref); err == nil && info.MetadataStatus == session.MetadataReady && (info.TitleSequence > 0 || info.Title != "") {
		topicTitle = info.Title
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if tab.removed || a.tabs[tab.ID] != tab || tab.Ctrl != ctrl || (navigation != 0 && a.desktopSessions.navigationSeq.Load() != navigation) {
		return errSessionNavigationSuperseded
	}
	applyCanonicalWorkspaceLocked(tab, workspace)
	tab.SessionID, tab.SessionPath = ref.SessionID, ""
	tab.TopicID, tab.TopicTitle = topicID, topicTitle
	tab.topicTitleSource = ""
	if topicTitle != "" {
		tab.topicTitleSource = topicTitleSourceManual
		if isDefaultTopicTitle(topicTitle) {
			tab.topicTitleSource = topicTitleSourceAuto
		}
	}
	a.bindSessionRuntimeKeyLocked(tab, tab.currentSessionIdentity())
	a.saveTabsLocked()
	return nil
}

// Old versions could persist A's workspace with B's SessionID. Only repair a
// cold surface, and only when the header and registry independently name B.
func (a *App) reconcileCanonicalTabWorkspace(ctx context.Context, tab *WorkspaceTab, generation uint64) error {
	a.mu.RLock()
	id, ctrl := tab.SessionID, tab.Ctrl
	a.mu.RUnlock()
	if id == "" || ctrl != nil {
		return nil
	}
	workspace, err := a.canonicalSessionWorkspace(ctx, session.SessionRef{HostID: localDesktopHostID, SessionID: id})
	if errors.Is(err, session.ErrSessionNotFound) {
		return nil
	} // Old-store migration still owns absent v5 identities.
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tabBuildSupersededLocked(tab, generation) || tab.SessionID != id || tab.Ctrl != nil {
		return errSessionNavigationSuperseded
	}
	applyCanonicalWorkspaceLocked(tab, workspace)
	tab.SessionPath = ""
	a.saveTabsLocked()
	return nil
}

func (a *App) prepareTabControllerWorkspace(tab *WorkspaceTab, ctx context.Context, generation uint64, appCtx context.Context) bool {
	a.mu.Lock()
	// Keep a lease-blocked banner steady across background retries. Ordinary
	// builds reset readiness before resolving their persisted workspace.
	if !tab.removed && tab.Ctrl == nil && !tab.StartupErrLeaseHeld {
		tab.Ready = false
		clearTabStartupError(tab)
		a.setSessionRuntimePhaseLocked(tab, sessionRuntimeStarting, nil)
	}
	a.mu.Unlock()
	if err := a.reconcileCanonicalTabWorkspace(ctx, tab, generation); err != nil {
		a.recordTabStartupFailure(tab, generation, appCtx, friendlySessionLoadError(err))
		return false
	}
	a.reconcileTabWithPinnedSessionMeta(tab)
	return true
}
