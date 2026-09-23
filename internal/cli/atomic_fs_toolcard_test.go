package cli

import (
	"strings"
	"testing"

	"reasonix/internal/tool"

	_ "reasonix/internal/tool/builtin"
)

// The team atomic-FS surface mounts atomic_read/atomic_write in place of the
// legacy trio. Several host tables are keyed by tool name — the transcript card,
// the approval prompt, the ACP tool kind — and a tool that is mounted but absent
// from them does not fail: it degrades silently, showing a raw name where every
// other tool shows a verb. This pins the pair's membership so a future rename or
// a new surface cannot lose it quietly.
func TestAtomicPairIsLabelledInEveryNameKeyedTable(t *testing.T) {
	pair := []string{"atomic_read", "atomic_write"}
	for _, name := range pair {
		if _, ok := tool.LookupBuiltin(name); !ok {
			t.Fatalf("%s is not registered; the tables below cannot be checked", name)
		}
		if toolVerb[name] == "" {
			t.Errorf("toolVerb has no label for %s; its card would show the raw name", name)
		}
		if toolArgKey[name] == "" {
			t.Errorf("toolArgKey has no argument for %s; its card would show no target", name)
		}
		if toolCategory[name] == "" {
			t.Errorf("toolCategory has no category for %s; its status dot would lose the read/write colour", name)
		}
	}
	// The reader must read as a read and the writer as a write: the dot colour
	// and the card verb are how a user tells them apart at a glance.
	if got := toolCategory["atomic_read"]; got != "read" {
		t.Errorf("atomic_read category = %q, want read", got)
	}
	if got := toolCategory["atomic_write"]; got != "write" {
		t.Errorf("atomic_write category = %q, want write", got)
	}
	if got := toolArg("atomic_read", `{"path":"internal/a.go","mode":"window"}`); got != "internal/a.go" {
		t.Errorf("atomic_read card arg = %q, want the path", got)
	}
	if got := toolArg("atomic_write", `{"path":"internal/a.go","mode":"patch"}`); got != "internal/a.go" {
		t.Errorf("atomic_write card arg = %q, want the path", got)
	}
}

// A tool the provider surface mounts must never render as an unlabelled raw
// name: every name on the team surface gets a verb.
func TestTeamSurfaceToolsAllRenderWithAVerb(t *testing.T) {
	for _, name := range []string{
		"atomic_read", "atomic_write", "bash", "read_file", "write_file", "edit_file",
	} {
		if got := toolDisplayName(name); got == "" || strings.Contains(got, "_") {
			t.Errorf("toolDisplayName(%q) = %q; a mounted tool must not fall through to its raw name", name, got)
		}
	}
}
