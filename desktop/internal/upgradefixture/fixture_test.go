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

func TestVerifyLegacyHistoryChecksContentAfterByteRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	body, err := encodeLegacyHistory(
		legacyMessage{Role: "user", Content: fixtureQuestion},
		legacyMessage{Role: "assistant", Content: fixtureText},
	)
	if err != nil {
		t.Fatal(err)
	}
	// A normal shutdown may rewrite the active legacy checkpoint. Byte changes
	// are acceptable only while the authored conversation stays intact.
	rewritten := strings.ReplaceAll(string(body), "\n", " \n")
	if rewritten == string(body) {
		t.Fatal("fixture did not change legacy bytes")
	}
	if err := os.WriteFile(path, []byte(rewritten), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyLegacyHistory(path, fixtureQuestion, fixtureText); err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(rewritten, fixtureText, "different answer", 1)
	if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyLegacyHistory(path, fixtureQuestion, fixtureText); err == nil {
		t.Fatal("changed authored content passed verification")
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
			t.Fatalf("%s must fail before app startup", mode)
		}
		for _, key := range []string{"REASONIX_HOME", "REASONIX_STATE_HOME", "REASONIX_CACHE_HOME"} {
			if got := os.Getenv(key); got != "untouched-"+key {
				t.Fatalf("%s leaked %s=%q", mode, key, got)
			}
		}
	}
}
