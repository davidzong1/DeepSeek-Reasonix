package agent

// Acceptance tests for L2 (TEAM_WRITE_LEASE_OPTIMIZATION_ROUTE.md): a tool-call
// hook only widens a call's workspace lease as far as its writes are proven or
// declared, and an unproven hook still keeps the whole workspace.

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestWriteLeaseScopeNarrowsOnlyOnEvidence(t *testing.T) {
	root := t.TempDir()
	toolPaths := []string{filepath.Join(root, "internal", "a.go")}
	cases := []struct {
		name        string
		surface     ToolHookWriteSurface
		hookPaths   []string
		toolBounded bool
		wantScope   []string
		wantWhole   bool
	}{
		{
			name:        "no hooks keeps the tool's own scope",
			toolBounded: true,
			wantScope:   toolPaths,
		},
		{
			name:        "no hooks and no tool paths needs the whole workspace",
			toolBounded: false,
			wantWhole:   true,
		},
		{
			name:        "proven reader hook leaves the tool's own scope untouched",
			surface:     ToolHookWriteSurface{Fires: true},
			toolBounded: true,
			wantScope:   toolPaths,
		},
		{
			name:        "unproven hook keeps the whole workspace",
			surface:     ToolHookWriteSurface{Fires: true, WholeWorkspace: true},
			toolBounded: true,
			wantWhole:   true,
		},
		{
			name:      "proven literal hook path becomes the scope",
			surface:   ToolHookWriteSurface{Fires: true, Paths: []string{filepath.Join(root, "hook.log")}},
			hookPaths: []string{filepath.Join(root, "hook.log")},
			wantScope: []string{filepath.Join(root, "hook.log")},
		},
		{
			name:        "declared formatter hook joins the call's own paths",
			surface:     ToolHookWriteSurface{Fires: true, ToolPathsScoped: true},
			toolBounded: true,
			wantScope:   toolPaths,
		},
		{
			name:        "declared formatter hook without nameable paths fails closed",
			surface:     ToolHookWriteSurface{Fires: true, ToolPathsScoped: true},
			toolBounded: false,
			wantWhole:   true,
		},
		{
			name:        "literal and declared hook scopes combine",
			surface:     ToolHookWriteSurface{Fires: true, ToolPathsScoped: true, Paths: []string{filepath.Join(root, "hook.log")}},
			hookPaths:   []string{filepath.Join(root, "hook.log")},
			toolBounded: true,
			wantScope:   []string{filepath.Join(root, "hook.log"), filepath.Join(root, "internal", "a.go")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scope, whole := writeLeaseScope(tc.surface, tc.hookPaths, toolPaths, tc.toolBounded)
			if whole != tc.wantWhole {
				t.Fatalf("whole = %v, want %v (scope %v)", whole, tc.wantWhole, scope)
			}
			if !slices.Equal(scope, tc.wantScope) {
				t.Fatalf("scope = %v, want %v", scope, tc.wantScope)
			}
		})
	}
}

func TestWorkspaceWritePathsDropsTargetsOutsideTheWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "hook.log")
	got := workspaceWritePaths(root, []string{
		filepath.Join(root, "hook.log"),
		"relative/hook.log",
		outside,
		"$TARGET",
		"~/hook.log",
		"",
	})
	want := []string{filepath.Join(root, "hook.log"), filepath.Join(root, "relative", "hook.log")}
	if !slices.Equal(got, want) {
		t.Fatalf("workspaceWritePaths = %v, want %v", got, want)
	}
}

func TestToolHookWriteSurfacePrefersTheReporter(t *testing.T) {
	called := ""
	report := func(toolName string) ToolHookWriteSurface {
		called = toolName
		return ToolHookWriteSurface{Fires: true, ToolPathsScoped: true}
	}
	got := toolHookWriteSurface(&stubHooks{}, report, "write_file")
	if called != "write_file" {
		t.Fatalf("reporter saw tool %q, want write_file", called)
	}
	if !got.Fires || !got.ToolPathsScoped {
		t.Fatalf("surface = %+v, want the reporter's value", got)
	}

	// Without a reporter, hooks that cannot describe their writes keep the
	// conservative whole-workspace coverage they have always had.
	if fallback := toolHookWriteSurface(&stubHooks{}, nil, "write_file"); !fallback.WholeWorkspace || !fallback.Fires {
		t.Fatalf("fallback surface = %+v, want a whole-workspace hold", fallback)
	}
	if empty := toolHookWriteSurface(nil, nil, "write_file"); empty.Fires || empty.WholeWorkspace {
		t.Fatalf("hookless surface = %+v, want the zero value", empty)
	}
}
