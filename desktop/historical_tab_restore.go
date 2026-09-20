package main

import (
	"os"
	"path/filepath"

	"reasonix/internal/agent"
)

// A source proven by background discovery is a pending user choice, not a
// damaged canonical session. Never read its content or acquire its writer lock
// while restoring presentation. Durable recovery/mapping evidence wins.
func (a *App) savedTabHistoricalSource(entry desktopTabEntry, evidence savedTabReconcileEvidence) *SessionSourceRef {
	if entry.SessionID != "" || entry.SessionPath == "" || evidence.registryErr != nil || evidence.draftErr != nil {
		return nil
	}
	if _, found, _ := savedTabPendingSessionIdentity(entry, evidence); found {
		return nil
	}
	if savedTabHasRecoveryOwner(entry, evidence) {
		return nil
	}
	path := agent.CanonicalSessionPath(entry.SessionPath)
	c := &a.historicalImports
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, source := range c.sources {
		if !sameDesktopPath(source.path, path) || source.scope != entry.Scope ||
			source.scope == "project" && !sameDesktopPath(source.root, entry.WorkspaceRoot) {
			continue
		}
		if _, adopted := historicalMappingForSource(evidence.registry, key); adopted {
			return nil
		}
		// A path-only saved tab selects the source's current head, not whichever
		// indexed branch happens to appear first in this map iteration.
		return &SessionSourceRef{HostID: localDesktopHostID, Path: path}
	}
	return nil
}

func hasHistoricalSessionArtifacts(path string) bool {
	for _, name := range []string{"manifest.json", "events.frames", "events.jsonl"} {
		if info, err := os.Lstat(filepath.Join(path, name)); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}
