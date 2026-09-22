package hook

// Acceptance tests for L2 (TEAM_WRITE_LEASE_OPTIMIZATION_ROUTE.md): a tool-call
// hook only narrows the workspace write lease when its writes can be bounded.

import (
	"slices"
	"testing"
)

func runnerWith(t *testing.T, hooks ...ResolvedHook) *Runner {
	t.Helper()
	return NewRunner(hooks, t.TempDir(), nil, nil)
}

func toolHook(event Event, match, command string, scope string) ResolvedHook {
	return ResolvedHook{
		HookConfig: HookConfig{Match: match, Command: command, WriteScope: scope},
		Event:      event,
		Scope:      ScopeProject,
	}
}

func TestToolCallWriteSurfaceWithoutHooks(t *testing.T) {
	surface := runnerWith(t).ToolCallWriteSurface("write_file")
	if surface.Fires || surface.WholeWorkspace || surface.ToolPathsScoped || len(surface.Paths) != 0 {
		t.Fatalf("hookless surface = %+v, want the zero value", surface)
	}
}

func TestToolCallWriteSurfaceBoundsEachHookShape(t *testing.T) {
	cases := []struct {
		name           string
		hooks          []ResolvedHook
		wantFires      bool
		wantPaths      []string
		wantToolScoped bool
		wantWhole      bool
	}{
		{
			name:      "proven reader does not widen",
			hooks:     []ResolvedHook{toolHook(PreToolUse, "write_file", `grep -q package f.go`, "")},
			wantFires: true,
		},
		{
			name:      "env-prefixed reader does not widen",
			hooks:     []ResolvedHook{toolHook(PostToolUse, ".*", `NO_COLOR=1 cat f.go`, "")},
			wantFires: true,
		},
		{
			name:      "literal redirect is a proven path",
			hooks:     []ResolvedHook{toolHook(PostToolUse, "write_file", `echo done > hook.log`, "")},
			wantFires: true,
			wantPaths: []string{"hook.log"},
		},
		{
			name: "literal paths are sorted and deduplicated",
			hooks: []ResolvedHook{
				toolHook(PostToolUse, "write_file", `echo b > z.log`, ""),
				toolHook(PostToolUse, "write_file", `echo a > a.log`, ""),
				toolHook(PostToolUse, "write_file", `echo b2 > z.log`, ""),
			},
			wantFires: true,
			wantPaths: []string{"a.log", "z.log"},
		},
		{
			name:      "declared none bounds an unproven hook",
			hooks:     []ResolvedHook{toolHook(PreToolUse, "write_file", `prettier --write "$FILE"`, WriteScopeNone)},
			wantFires: true,
		},
		{
			name:           "declared tool bounds an unproven hook to the call's paths",
			hooks:          []ResolvedHook{toolHook(PreToolUse, "write_file", `gofmt -w "$FILE"`, WriteScopeToolCall)},
			wantFires:      true,
			wantToolScoped: true,
		},
		{
			name:      "undeclared unproven hook keeps the whole workspace",
			hooks:     []ResolvedHook{toolHook(PreToolUse, "write_file", `gofmt -w "$FILE"`, "")},
			wantFires: true,
			wantWhole: true,
		},
		{
			name:      "unknown declaration value stays fail-closed",
			hooks:     []ResolvedHook{toolHook(PreToolUse, "write_file", `prettier --write "$FILE"`, "toolPaths")},
			wantFires: true,
			wantWhole: true,
		},
		{
			name:      "proof outranks a narrower declaration",
			hooks:     []ResolvedHook{toolHook(PostToolUse, "write_file", `printf x >> out/f.txt`, WriteScopeNone)},
			wantFires: true,
			wantPaths: []string{"out/f.txt"},
		},
		{
			name:      "plugin contextFile writes nothing",
			hooks:     []ResolvedHook{{HookConfig: HookConfig{Match: "write_file", Command: "cat $1", ContextFile: "ctx.json"}, Event: PreToolUse, Scope: ScopePlugin}},
			wantFires: true,
		},
		{
			name:      "matcher scoped to another tool does not fire",
			hooks:     []ResolvedHook{toolHook(PreToolUse, "bash", `gofmt -w "$FILE"`, "")},
			wantFires: false,
		},
		{
			name:      "malformed matcher never fires",
			hooks:     []ResolvedHook{toolHook(PreToolUse, "write_file(", `gofmt -w "$FILE"`, "")},
			wantFires: false,
		},
		{
			name: "one unbounded hook wins over a bounded sibling",
			hooks: []ResolvedHook{
				toolHook(PreToolUse, "write_file", `grep -q x f.go`, ""),
				toolHook(PostToolUse, "write_file", `python3 scripts/touch.py`, ""),
			},
			wantFires: true,
			wantWhole: true,
		},
		{
			name: "non-tool events are ignored",
			hooks: []ResolvedHook{{
				HookConfig: HookConfig{Command: `gofmt -w "$FILE"`},
				Event:      SessionStart,
				Scope:      ScopeProject,
			}},
			wantFires: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			surface := runnerWith(t, tc.hooks...).ToolCallWriteSurface("write_file")
			if surface.Fires != tc.wantFires {
				t.Fatalf("Fires = %v, want %v (%+v)", surface.Fires, tc.wantFires, surface)
			}
			if !slices.Equal(surface.Paths, tc.wantPaths) {
				t.Fatalf("Paths = %v, want %v", surface.Paths, tc.wantPaths)
			}
			if surface.ToolPathsScoped != tc.wantToolScoped {
				t.Fatalf("ToolPathsScoped = %v, want %v", surface.ToolPathsScoped, tc.wantToolScoped)
			}
			if surface.WholeWorkspace != tc.wantWhole {
				t.Fatalf("WholeWorkspace = %v, want %v", surface.WholeWorkspace, tc.wantWhole)
			}
		})
	}
}
