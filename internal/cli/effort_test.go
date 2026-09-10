package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
