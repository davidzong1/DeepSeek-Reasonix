package main

import (
	"context"
	"os"
	"reflect"

	"reasonix/desktop/internal/workspacestate"
)

// Source fences track identity/lifecycle, not file length, mtime, title or index
// revision. Appending content keeps a result readable; replacing/deleting its
// source or adopting it into another owner revokes it.
type readSourceFence struct {
	app          *App
	files        map[string]os.FileInfo
	bindings     map[string]string
	initial      workspacestate.State
	store        *readSnapshotStore
	snapshot     *readSnapshot
	metadataOnly bool
	indexed      map[string]string
}

func (a *App) newReadSourceFence(store *readSnapshotStore, snapshot *readSnapshot) (*readSourceFence, error) {
	state, err := a.workspaceRegistry().LoadProjection(a.bootContext())
	if err != nil {
		return nil, err
	}
	return &readSourceFence{app: a, files: map[string]os.FileInfo{}, bindings: map[string]string{}, initial: state, store: store, snapshot: snapshot}, nil
}

func sourceLifecycleBinding(state workspacestate.State, path string) string {
	matched := map[string]any{}
	for key, mapping := range state.SourceMappings {
		if sessionRuntimeKey(mapping.Path) == sessionRuntimeKey(path) {
			matched[key] = []any{mapping, state.SessionStates[mapping.SessionID]}
		}
	}
	return snapshotBinding("source-lifecycle", matched)
}

func (f *readSourceFence) add(ctx context.Context, path string) error {
	if path == "" {
		return nil
	}
	if _, ok := f.files[path]; ok {
		return nil
	}
	var info os.FileInfo
	if !f.metadataOnly {
		var err error
		info, err = os.Stat(path)
		if err != nil {
			return err
		}
	} else if catalog := f.app.sessionCatalog.Load(); catalog != nil {
		record, ok, err := catalog.GetSession(ctx, path)
		if err != nil {
			return err
		}
		if ok {
			if f.indexed == nil {
				f.indexed = map[string]string{}
			}
			f.indexed[path] = snapshotBinding("catalog-source", []any{record.Scope, record.WorkspaceRoot, record.TopicID})
		}
	}
	if err := f.store.reserve(f.snapshot, int64(512+len(path)*2)); err != nil {
		return err
	}
	f.files[path] = info
	f.bindings[path] = sourceLifecycleBinding(f.initial, path)
	return nil
}

func (f *readSourceFence) freeze() func() error {
	f.initial = workspacestate.State{}
	return func() error {
		current, err := f.app.workspaceRegistry().LoadProjection(f.app.bootContext())
		if err != nil {
			return err
		}
		for path, original := range f.files {
			if !f.metadataOnly {
				info, err := os.Stat(path)
				if os.IsNotExist(err) {
					return snapshotStale("lifecycle_changed")
				}
				if err != nil {
					return err
				}
				if !os.SameFile(original, info) {
					return snapshotStale("lifecycle_changed")
				}
			} else if expected, ok := f.indexed[path]; ok {
				catalog := f.app.sessionCatalog.Load()
				if catalog == nil {
					return snapshotStale("lifecycle_changed")
				}
				record, found, err := catalog.GetSession(f.app.bootContext(), path)
				if err != nil {
					return err
				}
				if !found || record.MissingSince != 0 || record.Health == "missing" || snapshotBinding("catalog-source", []any{record.Scope, record.WorkspaceRoot, record.TopicID}) != expected {
					return snapshotStale("lifecycle_changed")
				}
			}
			if sourceLifecycleBinding(current, path) != f.bindings[path] {
				return snapshotStale("lifecycle_changed")
			}
		}
		return nil
	}
}

func (a *App) workspaceReadFence(state workspacestate.State, workspace workspacestate.Workspace, nodes []ProjectNode) func() error {
	states := map[string]workspacestate.SessionState{}
	for _, node := range nodes {
		if node.Session != nil {
			states[node.Session.SessionID] = state.SessionStates[node.Session.SessionID]
		}
	}
	mappings := func(s workspacestate.State) map[string]workspacestate.SourceMapping {
		out := map[string]workspacestate.SourceMapping{}
		for key, m := range s.SourceMappings {
			if _, ok := states[m.SessionID]; ok && m.WorkspaceID == workspace.ID {
				out[key] = m
			}
		}
		return out
	}
	expected := snapshotBinding("mappings", mappings(state))
	workspace.Organization = nil
	workspace.SessionIDs = nil
	return func() error {
		current, err := a.workspaceRegistry().LoadProjection(a.bootContext())
		if err != nil {
			return err
		}
		owner, ok := current.Workspaces[workspace.ID]
		if !ok || owner.Root != workspace.Root || owner.Visible != workspace.Visible {
			return snapshotStale("lifecycle_changed")
		}
		members := map[string]bool{}
		for _, id := range owner.SessionIDs {
			members[id] = true
		}
		for id, expected := range states {
			if !members[id] || !reflect.DeepEqual(current.SessionStates[id], expected) {
				return snapshotStale("lifecycle_changed")
			}
		}
		if snapshotBinding("mappings", mappings(current)) != expected {
			return snapshotStale("lifecycle_changed")
		}
		return nil
	}
}
