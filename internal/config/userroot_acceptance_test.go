package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// requireOutsideProject fails when path escapes under the project dir — the
// mirror of requireTestPathWithin above. User-data roots must never resolve
// into whatever project happens to contain the cwd, even one carrying its own
// .reasonix tree or reasonix.toml.
func requireOutsideProject(t *testing.T, project, path string) {
	t.Helper()
	if path == "" {
		return
	}
	rel, err := filepath.Rel(project, path)
	if err != nil || (!strings.HasPrefix(rel, "..") && rel != "." && rel != "..") {
		t.Fatalf("user-data path %q leaked into project dir %q", path, project)
	}
}

// withCwd runs fn with the process working directory set to dir, restoring the
// original afterwards. Sequential-only: a parallel sibling could observe the
// moved cwd, so callers must not use t.Parallel.
func withCwd(t *testing.T, dir string, fn func()) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	defer func() {
		if err := os.Chdir(orig); err != nil {
			t.Errorf("restore cwd %s: %v", orig, err)
		}
	}()
	fn()
}

// TestUserDataDirsCwdIndependent pins the "run from anywhere" contract that
// the team-data user-root work builds on: session history, stats, archive,
// memory, credentials and user config all resolve under the user state root
// regardless of cwd — including a nested cwd inside a project that itself
// carries a reasonix.toml and a .reasonix/team tree. None of them may fall
// back into that project.
func TestUserDataDirsCwdIndependent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	t.Setenv("REASONIX_STATE_HOME", "")
	t.Setenv("REASONIX_CACHE_HOME", filepath.Join(home, "cache"))

	project := t.TempDir()
	// A project-shaped cwd: reasonix.toml plus a prior .reasonix/team tree that
	// must not become the resolved roots.
	mustWriteFile(t, filepath.Join(project, "reasonix.toml"), `model = "unused"`)
	mustMkdir(t, filepath.Join(project, ".reasonix", "team"))
	mustWriteFile(t, filepath.Join(project, ".reasonix", "team", "team.json"), `{"teams":[]}`)
	mustMkdir(t, filepath.Join(project, "nested", "deep"))

	want := map[string]string{
		"SessionDir":                   filepath.Join(home, "sessions"),
		"StatsDir":                     filepath.Join(home, "stats"),
		"ArchiveDir":                   filepath.Join(home, "archive"),
		"MemoryUserDir":                home,
		"UserConfigPath":               filepath.Join(home, "config.toml"),
		"UserCredentialsPath":          filepath.Join(home, ".env"),
		"MissingReasoningWarnStateDir": filepath.Join(home, "state"),
	}
	got := map[string]string{
		"SessionDir": SessionDir(), "StatsDir": StatsDir(), "ArchiveDir": ArchiveDir(),
		"MemoryUserDir": MemoryUserDir(), "UserConfigPath": UserConfigPath(),
		"UserCredentialsPath": UserCredentialsPath(), "MissingReasoningWarnStateDir": MissingReasoningWarnStateDir(),
	}

	withCwd(t, filepath.Join(project, "nested", "deep"), func() {
		for name, wantPath := range want {
			g := got[name]
			if g != wantPath {
				t.Errorf("%s under nested project cwd: want %q, got %q", name, wantPath, g)
			}
			requireOutsideProject(t, project, g)
		}
	})
	// Also from the project root itself — the most tempting leak point.
	withCwd(t, project, func() {
		for name, wantPath := range want {
			if g := got[name]; g != wantPath {
				t.Errorf("%s under project-root cwd: want %q, got %q", name, wantPath, g)
			}
		}
	})
}

// TestDefaultHomeBranchFollowsHomeDir pins the unix default: with no override,
// the state root is ~/.reasonix, tracked through HOME itself so a per-user
// profile redirect (or a fake HOME in tests) relocates everything together.
func TestDefaultHomeBranchFollowsHomeDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("default branch on windows is %APPDATA%\\reasonix, covered elsewhere")
	}
	t.Setenv("REASONIX_HOME", "")
	t.Setenv("REASONIX_STATE_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got := SessionDir(); got != filepath.Join(home, ".reasonix", "sessions") {
		t.Errorf("SessionDir default: want %q, got %q", filepath.Join(home, ".reasonix", "sessions"), got)
	}
	if got := UserConfigPath(); got != filepath.Join(home, ".reasonix", "config.toml") {
		t.Errorf("UserConfigPath default: want %q, got %q", filepath.Join(home, ".reasonix", "config.toml"), got)
	}
	if got := UserCredentialsPath(); got != filepath.Join(home, ".reasonix", ".env") {
		t.Errorf("UserCredentialsPath default: want %q, got %q", filepath.Join(home, ".reasonix", ".env"), got)
	}
}

// TestTwoReasonixHomesAreDisjoint is the multi-user isolation acceptance: two
// distinct REASONIX_HOME roots never share session history, stats, config, or
// credentials — a second user's state is invisible to the first.
func TestTwoReasonixHomesAreDisjoint(t *testing.T) {
	homeA, homeB := t.TempDir(), t.TempDir()
	dirs := func(home string) []string {
		t.Helper()
		t.Setenv("REASONIX_HOME", home)
		return []string{
			SessionDir(), StatsDir(), ArchiveDir(), MemoryUserDir(),
			UserConfigPath(), UserCredentialsPath(), MissingReasoningWarnStateDir(),
		}
	}
	setA := dirs(homeA)
	setB := dirs(homeB)
	for i, a := range setA {
		if b := setB[i]; a == b {
			t.Errorf("dir index %d identical across homes: %q", i, a)
		}
		rel, err := filepath.Rel(homeA, a)
		if err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("home A path %q escaped its home root", a)
		}
	}
	for _, b := range setB {
		if strings.HasPrefix(b, homeA) {
			t.Errorf("home B path %q resolved under home A", b)
		}
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
