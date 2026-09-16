package main

import (
	"errors"
	"reasonix/internal/session"
)

// DeleteSession is the legacy archive RPC. Canonical content and legacy
// originals remain in place; the lifecycle owner rejects active runtimes.
func (a *App) DeleteSession(path string) error {
	if id, ok := parseSessionRoute(path); ok {
		return friendlySessionFileError(a.ArchiveCanonicalSession(session.SessionRef{HostID: localDesktopHostID, SessionID: id}))
	}
	_, valid, err := a.sessionDirForPath(path)
	if err != nil {
		return friendlySessionFileError(err)
	}
	ref, adopted, err := a.legacyCanonicalRef(a.bootContext(), valid)
	if err != nil {
		return friendlySessionFileError(err)
	}
	if adopted {
		return friendlySessionFileError(a.ArchiveCanonicalSession(ref))
	}
	release, ok := a.tryLockRuntimeMutation("archive historical session")
	if !ok {
		return errTopicArchiveBusy
	}
	ref, dependency, err := a.stageArchiveSource(a.bootContext(), valid)
	var fallback fallbackRuntimeTarget
	if err == nil {
		dependencies := []string{}
		if dependency != "" {
			dependencies = append(dependencies, dependency)
		}
		fallback, err = a.archiveSessionRefsLocked([]session.SessionRef{ref}, dependencies...)
	}
	release()
	if err == nil {
		if fallback.needs {
			_ = a.openFallbackRuntime(fallback)
		}
		a.emitProjectTreeChanged()
	}
	return friendlySessionFileError(err)
}

// PurgeTrashedSession resolves a legacy trash identity and removes only its
// canonical application session, retaining upgrade originals.
func (a *App) PurgeTrashedSession(path string) error {
	if _, err := a.trashedSessionDir(path); err != nil {
		return err
	}
	if !explicitlyDeletedLegacyEntry(path) {
		return errors.New("historical recovery entries cannot be permanently cleared")
	}
	// Legacy public RPCs retain upgrade originals too. Resolve/import the
	// archived application identity before delegating to canonical purge.
	if err := a.discoverHistoricalTrash(a.bootContext()); err != nil {
		return err
	}
	state, err := a.workspaceRegistry().Load(a.bootContext())
	if err != nil {
		return err
	}
	if mapping, adopted := state.SourceMappings[desktopSourceKey(path, "")]; adopted {
		return a.PurgeCanonicalSession(session.SessionRef{HostID: localDesktopHostID, SessionID: mapping.SessionID})
	}
	return errors.New("historical session has no verified canonical identity")
}
