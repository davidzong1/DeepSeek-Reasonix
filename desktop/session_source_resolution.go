package main

import (
	"os"
	"path/filepath"
	"reasonix/internal/agent"
	"reasonix/internal/session"
	"strings"
)

func (a *App) resolveSourceSessionTarget(selector SessionSelector, allowArchived bool) (SessionTarget, error) {
	source := selector.Source
	if source.HostID != "" && source.HostID != localDesktopHostID {
		return SessionTarget{}, newSessionOperationError("unsupported", "This source belongs to another host.")
	}
	if strings.TrimSpace(source.Path) == "" {
		return SessionTarget{}, newSessionOperationError("target_not_found", "The source no longer exists.")
	}
	key := desktopSourceKey(source.Path, source.HeadID)
	if source.SourceKey != "" && source.SourceKey != key {
		return SessionTarget{}, newSessionOperationError("target_changed", "The source identity changed.")
	}
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return SessionTarget{}, err
	}
	if mapping, ok := state.SourceMappings[key]; ok {
		return a.resolveCanonicalSessionTargetState(session.SessionRef{HostID: localDesktopHostID, SessionID: mapping.SessionID}, selector.TopicID, allowArchived)
	}
	if source.HeadID == "" {
		canonical, _ := a.desktopHistoricalRoots()
		for _, root := range canonical {
			if !sameDesktopPath(filepath.Dir(source.Path), root.root) {
				continue
			}
			info, statErr := os.Lstat(source.Path)
			pending := pendingHistoricalOperation(state, key)
			if statErr != nil && !(os.IsNotExist(statErr) && pending != nil) {
				return SessionTarget{}, newSessionOperationError("target_not_found", "The historical source is unavailable.")
			}
			if info != nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
				return SessionTarget{}, newSessionOperationError("target_not_found", "The historical source is not a session directory.")
			}
			copy := *source
			copy.HostID, copy.SourceKey = localDesktopHostID, key
			return SessionTarget{Source: &copy, SessionPath: source.Path, TopicID: selector.TopicID,
				Scope: root.scope, WorkspaceRoot: root.workspaceRoot}, nil
		}
	}
	if source.HeadID != "" {
		dir, validated, err := a.sessionDirForPath(source.Path)
		if err != nil {
			return SessionTarget{}, err
		}
		if _, _, err = validateSessionPath(dir, validated); err != nil {
			return SessionTarget{}, err
		}
		heads, err := agent.ListSessionHeads(validated)
		if err != nil {
			return SessionTarget{}, err
		}
		found := false
		for _, head := range heads {
			if head.ID == source.HeadID && !head.Retired {
				found = true
			}
		}
		if !found {
			return SessionTarget{}, newSessionOperationError("target_not_found", "This historical head is no longer available.")
		}
		copy := *source
		copy.Path, copy.HostID, copy.SourceKey = validated, localDesktopHostID, key
		target := SessionTarget{Source: &copy, SessionPath: validated, Scope: "global"}
		for _, project := range loadProjectsFile().Projects {
			if canonicalRuntimeRoot(dir) == canonicalRuntimeRoot(desktopSessionDir(project.Root)) {
				target.Scope, target.WorkspaceRoot = "project", project.Root
				break
			}
		}
		if meta, ok, err := agent.LoadBranchMeta(validated); err == nil && ok {
			target.TopicID = meta.TopicID
			if meta.Scope != "" {
				target.Scope, target.WorkspaceRoot = meta.Scope, meta.WorkspaceRoot
			}
		}
		return target, nil
	}
	return a.resolveLegacySessionTarget(source.Path, selector.TopicID, allowArchived)
}
