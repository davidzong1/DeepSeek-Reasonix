package boot

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveWorkspaceRootExplicitAndGitFallback proves the --dir contract:
// an explicit workspace root is honored even inside a git repository, while an
// empty root still falls back to the nearest git root from the working directory.
func TestResolveWorkspaceRootExplicitAndGitFallback(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// Explicit --dir pins the workspace root, not the git root of the repo.
	if got := resolveWorkspaceRoot(sub); got != sub {
		t.Fatalf("resolveWorkspaceRoot(%q) = %q, want the explicit dir", sub, got)
	}

	// No explicit root: fall back to the nearest git root from the CWD.
	t.Chdir(sub)
	got := resolveWorkspaceRoot("")
	wantRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("resolveWorkspaceRoot(%q) returned unusable path %q: %v", "", got, err)
	}
	if gotResolved != wantRoot {
		t.Fatalf("resolveWorkspaceRoot(\"\") = %q, want git root %q", got, repo)
	}
}

// TestNearestGitRootHomeCeiling proves the workspace root never climbs to the
// user's home directory: a dotfiles repo at $HOME is user-global state, not a
// project, while a real repo strictly below home is still found.
func TestNearestGitRootHomeCeiling(t *testing.T) {
	home := t.TempDir()
	oldHome := osUserHomeDir
	osUserHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { osUserHomeDir = oldHome })

	mkdir := func(p string) {
		t.Helper()
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// $HOME itself is a git repo (dotfiles): never a project root.
	mkdir(filepath.Join(home, ".git"))

	// A real repo strictly below home resolves from a deep subdir.
	repo := filepath.Join(home, "proj")
	mkdir(filepath.Join(repo, ".git"))
	deep := filepath.Join(repo, "a", "b")
	mkdir(deep)
	if got, ok := nearestGitRoot(deep); !ok || got != repo {
		t.Fatalf("nearestGitRoot(%q) = (%q,%v), want repo %q below home", deep, got, ok, repo)
	}

	// A home subdir with no nested repo must not resolve upward to $HOME's .git.
	bare := filepath.Join(home, "elsewhere")
	mkdir(bare)
	if got, ok := nearestGitRoot(bare); ok {
		t.Fatalf("nearestGitRoot(%q) = %q, want no git root above $HOME", bare, got)
	}
}
