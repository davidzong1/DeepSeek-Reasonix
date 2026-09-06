package boot

import (
	"os"
	"path/filepath"
	"testing"
)

func makeTeamProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "team", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestResolveTeamProjectRootUsesExplicitProject(t *testing.T) {
	root := makeTeamProject(t)
	other := t.TempDir()
	oldExe, oldBuilt := osExecutable, builtProjectRoot
	t.Cleanup(func() { osExecutable, builtProjectRoot = oldExe, oldBuilt })
	osExecutable = func() (string, error) { return filepath.Join(other, "reasonix"), nil }
	builtProjectRoot = ""

	if got := ResolveTeamProjectRoot(root); got != root {
		t.Fatalf("ResolveTeamProjectRoot = %q, want explicit project %q", got, root)
	}
}

func TestResolveTeamProjectRootFindsProjectOwningBinary(t *testing.T) {
	root := makeTeamProject(t)
	other := t.TempDir()
	oldExe, oldBuilt := osExecutable, builtProjectRoot
	t.Cleanup(func() { osExecutable, builtProjectRoot = oldExe, oldBuilt })
	osExecutable = func() (string, error) { return filepath.Join(root, "bin", "reasonix"), nil }
	builtProjectRoot = ""

	if got := ResolveTeamProjectRoot(other); got != root {
		t.Fatalf("ResolveTeamProjectRoot from unrelated cwd = %q, want binary project %q", got, root)
	}
}

func TestResolveTeamProjectRootUsesBuildRootForCopiedBinary(t *testing.T) {
	root := makeTeamProject(t)
	launchProject := makeTeamProject(t)
	installDir := t.TempDir()
	oldExe, oldBuilt := osExecutable, builtProjectRoot
	t.Cleanup(func() { osExecutable, builtProjectRoot = oldExe, oldBuilt })
	osExecutable = func() (string, error) { return filepath.Join(installDir, "bin", "reasonix"), nil }
	builtProjectRoot = root

	if got := ResolveTeamProjectRoot(launchProject); got != root {
		t.Fatalf("ResolveTeamProjectRoot from copied binary = %q, want build project %q instead of launch project", got, root)
	}
}
