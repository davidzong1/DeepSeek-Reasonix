//go:build windows

package builtin

import (
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/sandbox"
	"reasonix/internal/tool"
)

func TestWindowsRestrictedBashRejectedBeforeLaunch(t *testing.T) {
	previous := bashSandboxCommand
	bashSandboxCommand = func(sandbox.Spec, sandbox.Shell, string) ([]string, bool) {
		t.Fatal("disabled Bash reached process preparation")
		return nil, false
	}
	t.Cleanup(func() { bashSandboxCommand = previous })
	for _, preset := range []string{"read-only", "workspace-write"} {
		for _, background := range []bool{false, true} {
			b := bash{shell: sandbox.Shell{Kind: sandbox.ShellBash, Path: `C:\Git\bin\bash.exe`}, workDir: t.TempDir()}
			args, _ := json.Marshal(bashParams{Command: "echo hello", RunInBackground: background})
			res, err := b.ExecuteDetailed(sandbox.WithPermissionPreset(t.Context(), preset), args)
			if err == nil || !strings.Contains(err.Error(), "disabled") || res.Execution.State != tool.ShellStateNotRun || res.Execution.MutationRisk != tool.ShellMutationNotStarted {
				t.Fatalf("%s background=%v: %+v err=%v", preset, background, res, err)
			}
		}
	}
}
