//go:build windows

package shellrun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/sandbox"
)

func TestMain(m *testing.M) {
	sandbox.RegisterHelperDispatch()
	if len(os.Args) > 1 && os.Args[1] == sandbox.WindowsHelperCommand {
		os.Exit(sandbox.RunWindowsSandboxHelper(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// Exercise the real helper, restricted token, inherited child and filesystem
// boundary together. Cross-compilation does not execute this acceptance test.
func TestWindowsNativeShellPreflightRetainsWriteBoundary(t *testing.T) {
	path, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell 7 required for native Windows acceptance")
	}
	workspace, outside, temp := t.TempDir(), t.TempDir(), t.TempDir()
	sh := sandbox.Shell{Kind: sandbox.ShellPowerShell, Path: path}
	spec := sandbox.Spec{Mode: "enforce", Network: true, WriteRoots: []string{workspace}}
	insideFile, outsideFile := filepath.Join(workspace, "inside.txt"), filepath.Join(outside, "outside.txt")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	command := "$ErrorActionPreference='Stop'; Set-Content -LiteralPath " + quote(insideFile) +
		" -Value ok; try { Set-Content -LiteralPath " + quote(outsideFile) + " -Value forbidden; exit 9 } catch { exit 0 }"
	prepared := sandbox.PrepareShell(spec, sh, command, temp)
	if !prepared.Wrapped {
		t.Fatal("sandbox helper unavailable")
	}
	res := RunForeground(context.Background(), Request{
		Argv: prepared.Argv, ProbeArgv: WindowsProbeArgv(spec, sh, temp),
		Dir: workspace, Env: os.Environ(), Timeout: 30 * time.Second,
		ShellKind: sh.Kind.String(), ShellPath: sh.Path,
	})
	if res.Err != nil {
		t.Fatalf("native preflight/execution: %v\n%s", res.Err, res.Combined)
	}
	if _, err := os.Stat(insideFile); err != nil {
		t.Fatalf("workspace write failed: %v", err)
	}
	if _, err := os.Stat(outsideFile); !os.IsNotExist(err) {
		t.Fatalf("outside write boundary failed: %v", err)
	}
}
