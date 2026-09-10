package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/provider"
)

// A level is matched against the vocabulary exactly, so the retired `off`
// spelling and a mixed-case level are refused like any other undeclared alias
// rather than folded into a stored level.
func TestEffortCommandRefusesUndeclaredAliasesWithoutStoringThem(t *testing.T) {
	isolateUserConfig(t)
	for _, alias := range []string{"off", "HIGH"} {
		t.Run(alias, func(t *testing.T) {
			m := newTestChatTUI()
			m.ctrl = control.New(control.Options{Label: "deepseek-flash"})
			m.modelRef = "deepseek-flash/deepseek-v4-flash"
			m.buildController = func(_ controllerBuildSpec, _ []provider.Message, _ string, _ control.SessionAPI) (*control.Controller, error) {
				return control.New(control.Options{Label: "deepseek-flash"}), nil
			}

			if cmd := m.runEffortCommand("/effort " + alias); cmd != nil {
				t.Fatalf("/effort %s rebuilt the session", alias)
			}
			joined := strings.Join(*m.pendingCommit, "\n")
			if !strings.Contains(joined, "UNSUPPORTED_REASONING_EFFORT") {
				t.Fatalf("transcript %q lacks the typed refusal", joined)
			}
			if _, err := os.Stat(config.UserConfigPath()); !os.IsNotExist(err) {
				t.Fatalf("a refused selection reached the store, stat err=%v", err)
			}
		})
	}
}

// /effort must settle a level the adapter cannot send before writing it. The
// stored value used to surface only as the provider's construction error, which
// took the whole entry down instead of reporting the selection.
func TestEffortCommandRefusesPinnedDisabledWithoutStoringIt(t *testing.T) {
	for _, tc := range []struct {
		name, ref, configBody string
		refused               bool
	}{
		{
			name: "generic endpoint cannot send disabled",
			ref:  "gateway/custom",
			configBody: `default_model="gateway/custom"
[[providers]]
name="gateway"
kind="openai"
base_url="https://gateway.example.invalid/v1"
model="custom"
thinking="disabled"
`,
			refused: true,
		},
		{
			name: "deepseek endpoint sends disabled itself",
			ref:  "ds/deepseek-v4-flash",
			configBody: `default_model="ds/deepseek-v4-flash"
[[providers]]
name="ds"
kind="openai"
base_url="https://api.deepseek.com"
model="deepseek-v4-flash"
thinking="disabled"
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("REASONIX_HOME", home)
			t.Chdir(t.TempDir())
			path := filepath.Join(home, "config.toml")
			writeFileForTest(t, path, tc.configBody)

			m := newTestChatTUI()
			m.modelRef = tc.ref
			m.runEffortCommand("/effort disabled")
			joined := strings.Join(*m.pendingCommit, "\n")

			if !tc.refused {
				if strings.Contains(joined, "UNSUPPORTED_REASONING_EFFORT") {
					t.Fatalf("the adapter's own level was refused: %q", joined)
				}
				if !strings.Contains(joined, "model switching is unavailable") {
					t.Fatalf("the selection did not reach the switch step: %q", joined)
				}
				return
			}
			for _, want := range []string{"UNSUPPORTED_REASONING_EFFORT", "thinking is disabled by configuration"} {
				if !strings.Contains(joined, want) {
					t.Fatalf("transcript %q lacks %q", joined, want)
				}
			}
			stored, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(stored), "effort") {
				t.Fatalf("a refused selection reached the store:\n%s", stored)
			}
		})
	}
}
