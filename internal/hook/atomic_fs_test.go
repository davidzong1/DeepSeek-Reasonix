package hook

// The atomic pair keeps the Claude-facing hook contract: an imported hook
// matches on Claude's tool names, so a swapped surface would stop firing it.

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
)

// TestAtomicFSPairKeepsClaudeHookContract pins the atomic pair into the
// Claude-facing bridge. An imported hook matches on Claude's tool names, so a
// team build that swaps the surface would otherwise stop firing: a hook written
// to guard "Write" would never see atomic_write, and one guarding "Read" would
// never see atomic_read.
func TestAtomicFSPairKeepsClaudeHookContract(t *testing.T) {
	for _, tc := range []struct{ reasonix, claude string }{
		{"atomic_read", "Read"},
		{"atomic_write", "Write"},
	} {
		if got := claudeFacingToolName(tc.reasonix); got != tc.claude {
			t.Errorf("claudeFacingToolName(%q) = %q, want %q", tc.reasonix, got, tc.claude)
		}
		hook := ResolvedHook{HookConfig: HookConfig{Match: tc.claude, PayloadFormat: "claude"}, Event: PreToolUse}
		if !MatchesTool(hook, tc.reasonix) {
			t.Errorf("a Claude matcher %q must match Reasonix tool %q", tc.claude, tc.reasonix)
		}
	}
}

// TestAtomicWriteInputRenameCoversOnlyTheTopLevelPath documents what the key
// rename can and cannot do: a single-path atomic_write reaches a hook with
// Claude's file_path, while an ops transaction keeps its array — there is no
// single file_path to synthesize, and inventing one would hand a guard a path
// the call never named.
func TestAtomicWriteInputRenameCoversOnlyTheTopLevelPath(t *testing.T) {
	single := claudeFacingToolInput("atomic_write", json.RawMessage(`{"path":"a.go","mode":"replace","content":"x"}`), t.TempDir())
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(single, &obj); err != nil {
		t.Fatalf("single-path input is not an object: %v", err)
	}
	if _, ok := obj["file_path"]; !ok {
		t.Fatalf("single-path atomic_write must expose file_path, got %s", single)
	}
	if _, ok := obj["path"]; ok {
		t.Fatalf("the original path key must be renamed away, got %s", single)
	}

	ops := claudeFacingToolInput("atomic_write", json.RawMessage(`{"ops":[{"path":"a.go","mode":"replace","content":"x"}]}`), t.TempDir())
	obj = nil // Unmarshal merges into a non-nil map; start clean for the second case.
	if err := json.Unmarshal(ops, &obj); err != nil {
		t.Fatalf("ops input is not an object: %v", err)
	}
	if _, ok := obj["file_path"]; ok {
		t.Fatalf("an ops transaction has no single file_path; got %s", ops)
	}
	if _, ok := obj["ops"]; !ok {
		t.Fatalf("an ops transaction must keep its array, got %s", ops)
	}
}

// TestAtomicWriteOpsExposesFilePaths pins the fail-closed half of the payload
// contract: an ops transaction has no single file_path, so a Write-shaped guard
// reading .tool_input.file_path would get "null" and pass. Exposing every target
// as file_paths lets such a guard inspect all of them instead.
func TestAtomicWriteOpsExposesFilePaths(t *testing.T) {
	dir := t.TempDir()
	raw := json.RawMessage(`{"ops":[{"path":"a.go","mode":"replace","content":"x"},{"path":"sub/b.go","mode":"create","content":"y"}]}`)
	out := claudeFacingToolInput("atomic_write", raw, dir)

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("ops input is not an object: %v", err)
	}
	if _, ok := obj["file_path"]; ok {
		t.Fatalf("an ops transaction has no single file_path; got %s", out)
	}
	var paths []string
	if err := json.Unmarshal(obj["file_paths"], &paths); err != nil {
		t.Fatalf("file_paths missing or not an array: %s", out)
	}
	want := []string{filepath.Join(dir, "a.go"), filepath.Join(dir, "sub/b.go")}
	if !slices.Equal(paths, want) {
		t.Fatalf("file_paths = %v, want %v", paths, want)
	}
	// The original array stays, so a hook can still see modes and contents.
	if _, ok := obj["ops"]; !ok {
		t.Fatalf("the ops array must survive the adaptation: %s", out)
	}
}

// TestAtomicWriteSinglePathExposesBothForms keeps the single-path case working
// for both guard shapes: file_path for a Write-shaped guard, file_paths for one
// that reads the list uniformly.
func TestAtomicWriteSinglePathExposesBothForms(t *testing.T) {
	dir := t.TempDir()
	out := claudeFacingToolInput("atomic_write", json.RawMessage(`{"path":"a.go","mode":"replace","content":"x"}`), dir)
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatal(err)
	}
	var single string
	if err := json.Unmarshal(obj["file_path"], &single); err != nil {
		t.Fatalf("file_path missing: %s", out)
	}
	if want := filepath.Join(dir, "a.go"); single != want {
		t.Fatalf("file_path = %q, want %q", single, want)
	}
	var paths []string
	if err := json.Unmarshal(obj["file_paths"], &paths); err != nil {
		t.Fatalf("file_paths missing: %s", out)
	}
	if len(paths) != 1 || paths[0] != single {
		t.Fatalf("file_paths = %v, want [%s]", paths, single)
	}
}
