package boot

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"reasonix/internal/event"
)

// TestBuildEmitsLegacyMigrationNoticesInOrder pins the sequence of the
// legacy-config notices at the sink Build hands the frontend, so the migrations
// keep reporting in the order they ran.
func TestBuildEmitsLegacyMigrationNoticesInOrder(t *testing.T) {
	home := isolateConfigHome(t)
	t.Setenv("REASONIX_HOME", filepath.Join(home, "reasonix-home"))
	project := robustTempDir(t)
	writeFile(t, project, "reasonix.toml", `
default_model = "test-model"

[agent]
max_steps = 3
memory_compiler = "v5"
soft_compact_ratio = 0.5

[secrets]
redact_tool_output = true

[[providers]]
name = "test-model"
kind = "openai"
base_url = "https://example.invalid"
model = "x"
api_key_env = "REASONIX_TEST_KEY_UNSET"
`)

	want := []string{
		"Deprecated agent step limits were removed.",
		"Deprecated redact_tool_output setting was removed.",
		"Deprecated memory_compiler setting was removed.",
		"上下文维护已简化为单一自动压缩阈值。",
	}
	var got []string
	sink := event.FuncSink(func(e event.Event) {
		if e.Kind == event.Notice && slices.Contains(want, e.Text) {
			if e.Level != event.LevelInfo {
				t.Errorf("notice %q level = %v, want info", e.Text, e.Level)
			}
			got = append(got, e.Text)
		}
	})
	ctrl, err := Build(context.Background(), Options{Sink: sink, WorkspaceRoot: project})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ctrl.Close()

	if !slices.Equal(got, want) {
		t.Fatalf("migration notices = %q, want %q", got, want)
	}
}
