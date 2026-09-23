package worktree

// `merge-tree --write-tree` arrived in Git 2.38; older Git reads those flags as
// the legacy three-argument form and exits 129, which used to surface as a
// misleading "preflight merge conflicts". These tests pin that classification.

import (
	"context"
	"strings"
	"testing"
)

// requireMergeTreePreflight skips a merge-back test on a Git too old to run the
// preflight. The merge path cannot be exercised at all without it, so skipping
// is the honest outcome; mergeTree's own classification is covered by
// TestMergeTreeReportsAnOldGitAsUnsupported.
func requireMergeTreePreflight(t *testing.T) {
	t.Helper()
	requireGit(t)
	version, ok := gitVersion(context.Background(), t.TempDir())
	if !ok {
		t.Skip("git version could not be read; skipping a merge-preflight test")
	}
	if versionOlderThan(version, mergeTreePreflightMinVersion) {
		t.Skipf("git %s is older than %s; merge-tree --write-tree is unavailable", version, mergeTreePreflightMinVersion)
	}
}

func TestParseGitVersion(t *testing.T) {
	for _, tc := range []struct {
		out    string
		want   string
		wantOK bool
	}{
		{out: "git version 2.34.1\n", want: "2.34.1", wantOK: true},
		{out: "git version 2.39.2 (Apple Git-145)\n", want: "2.39.2", wantOK: true},
		{out: "git version 2.34.1.windows.1\n", want: "2.34.1.windows.1", wantOK: true},
		{out: "git version 2.38.0\n", want: "2.38.0", wantOK: true},
		{out: "", wantOK: false},
		{out: "git version\n", wantOK: false},
		{out: "something else 1.2.3", wantOK: false},
		{out: "git version not-a-version", wantOK: false},
	} {
		got, ok := parseGitVersion(tc.out)
		if ok != tc.wantOK || (ok && got != tc.want) {
			t.Errorf("parseGitVersion(%q) = (%q, %v), want (%q, %v)", tc.out, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestVersionOlderThan(t *testing.T) {
	for _, tc := range []struct {
		version string
		min     string
		want    bool
	}{
		{version: "2.34.1", min: "2.38", want: true},
		{version: "2.37.9", min: "2.38", want: true},
		{version: "2.38.0", min: "2.38", want: false},
		{version: "2.38.1", min: "2.38", want: false},
		{version: "2.39.2", min: "2.38", want: false},
		{version: "3.0.0", min: "2.38", want: false},
		{version: "2.34.1.windows.1", min: "2.38", want: true},
		{version: "2.39.2 (Apple Git-145)", min: "2.38", want: false},
		// An unreadable version fails closed: the caller must not treat
		// "unknown" as "new enough".
		{version: "not-a-version", min: "2.38", want: true},
		{version: "", min: "2.38", want: true},
	} {
		if got := versionOlderThan(tc.version, tc.min); got != tc.want {
			t.Errorf("versionOlderThan(%q, %q) = %v, want %v", tc.version, tc.min, got, tc.want)
		}
	}
}

// TestMergeTreeReportsAnOldGitAsUnsupported is the classification the whole
// gate rests on: a non-conflict exit from the preflight on an old Git must be
// reported as an unsupported host, not as a merge conflict.
func TestMergeTreeReportsAnOldGitAsUnsupported(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
	head := ""
	if out, _, err := runGit(context.Background(), repo, "rev-parse", "HEAD"); err == nil {
		head = strings.TrimSpace(out)
	}
	if head == "" {
		t.Fatal("could not read the fixture repository HEAD")
	}

	// A missing revision fails with a usage/exit error on every Git — the same
	// class the old-Git path takes, so it exercises the reclassification.
	_, _, _, err := mergeTree(context.Background(), repo, head, "refs/heads/definitely-missing")
	if err == nil {
		t.Fatal("a missing revision must fail the preflight")
	}
	version, _ := gitVersion(context.Background(), repo)
	if versionOlderThan(version, mergeTreePreflightMinVersion) {
		var unsupported *mergeTreeUnsupportedError
		if !asMergeTreeUnsupported(err, &unsupported) {
			t.Fatalf("old git (%s) error = %v, want mergeTreeUnsupportedError", version, err)
		}
		if !strings.Contains(err.Error(), mergeTreePreflightMinVersion) {
			t.Fatalf("the unsupported error must name the required version: %v", err)
		}
		return
	}
	// Supported Git: the failure must NOT be misreported as an unsupported host.
	var unsupported *mergeTreeUnsupportedError
	if asMergeTreeUnsupported(err, &unsupported) {
		t.Fatalf("git %s is new enough; error = %v, want an ordinary preflight failure", version, err)
	}
}

func asMergeTreeUnsupported(err error, target **mergeTreeUnsupportedError) bool {
	for err != nil {
		if typed, ok := err.(*mergeTreeUnsupportedError); ok {
			*target = typed
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}
