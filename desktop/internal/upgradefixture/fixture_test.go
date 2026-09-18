package upgradefixture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncodeLegacyHistoryEscapesJSONContent(t *testing.T) {
	want := []legacyMessage{
		{Role: "user", Content: "quote \" slash \\ newline\n中文 %20 #"},
		{Role: "assistant", Content: "second line"},
	}
	body, err := encodeLegacyHistory(want...)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("encoded lines = %d, want %d: %q", len(lines), len(want), body)
	}
	for i, line := range lines {
		var got legacyMessage
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("decode line %d: %v", i, err)
		}
		if got != want[i] {
			t.Fatalf("line %d = %+v, want %+v", i, got, want[i])
		}
	}
}

func TestRunRestoresEnvironmentOnSuccessAndFailure(t *testing.T) {
	for _, key := range []string{"REASONIX_HOME", "REASONIX_STATE_HOME", "REASONIX_CACHE_HOME"} {
		t.Setenv(key, "untouched-"+key)
	}
	home := filepath.Join(t.TempDir(), "isolated")
	report := filepath.Join(t.TempDir(), "fixture.json")
	for _, mode := range []string{"create", "verify", "invalid"} {
		err := Run(mode, home, report, "first")
		if mode == "create" && err != nil {
			t.Fatal(err)
		}
		if mode != "create" && err == nil {
			t.Fatalf("%s must fail before migration", mode)
		}
		for _, key := range []string{"REASONIX_HOME", "REASONIX_STATE_HOME", "REASONIX_CACHE_HOME"} {
			if got := os.Getenv(key); got != "untouched-"+key {
				t.Fatalf("%s leaked %s=%q", mode, key, got)
			}
		}
	}
}
