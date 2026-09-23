package builtin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/fileops"
	"reasonix/internal/tool"
)

// The read/write pair shares one anchor protocol. This is the loop a team
// member actually runs: read, someone else writes, read again with delta, then
// write against the anchor the first read left.
func TestAtomicReadCASRoundTrip(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	atomicWriteFile(t, path, "alpha\nbeta\ngamma\n")
	ctx := atomicTestContext()
	reader := atomicReadTool(t, dir)
	writer := atomicWrite{workDir: dir, roots: realRoots([]string{dir})}

	first, err := reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	id := atomicHeaderReadID(t, first)

	// A teammate writes the file behind the reader's back.
	atomicWriteFile(t, path, "alpha\nBETA\ngamma\n")

	// delta against the original read reports exactly the change.
	out, err := reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt", "mode": "delta", "since": id}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "-beta") || !strings.Contains(out, "+BETA") {
		t.Fatalf("delta after a foreign write = %q", out)
	}

	// A write citing the stale anchor is refused with the same hunks.
	_, err = writer.Execute(ctx, atomicReadArgs(t, map[string]any{
		"path": "a.txt", "mode": "patch", "since": id,
		"edits": []map[string]string{{"old": "gamma", "new": "GAMMA"}},
	}))
	var opErr *tool.OperationError
	if !errors.As(err, &opErr) || opErr.Diagnostic.Code != tool.FSStaleVersion {
		t.Fatalf("stale write error = %v", err)
	}
	if cause := opErr.Cause.Error(); !strings.Contains(cause, "+BETA") {
		t.Fatalf("stale write did not carry the hunk: %q", cause)
	}
	if got, _ := os.ReadFile(path); string(got) != "alpha\nBETA\ngamma\n" {
		t.Fatalf("a refused write changed the file: %q", got)
	}

	// Re-reading recovers: the same patch now succeeds on the fresh anchor.
	second, err := reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	fresh := atomicHeaderReadID(t, second)
	if fresh == id {
		t.Fatal("a changed file produced the same read id")
	}
	if _, err := writer.Execute(ctx, atomicReadArgs(t, map[string]any{
		"path": "a.txt", "mode": "patch", "since": fresh,
		"edits": []map[string]string{{"old": "gamma", "new": "GAMMA"}},
	})); err != nil {
		t.Fatalf("patch after re-read: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "alpha\nBETA\nGAMMA\n" {
		t.Fatalf("file after recovery = %q", got)
	}
}

// The metadata version is size+mode+mtime+ctime. On a filesystem whose ctime
// granularity is coarser than a write, a same-size rewrite inside one clock tick
// changes none of it, so a CAS that trusted the version alone would compare the
// file against itself and pass. The bytes the read observed are the authority.
//
// The case drives that exact state: a real read anchors the file, the file is
// rewritten with the same size, and the observation is re-asserted at the
// collided version — which is precisely what the filesystem does on its own.
func TestAtomicWriteRefusesSameSizeRewriteAtCollidingVersion(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	atomicWriteFile(t, path, "AAAA\n")
	ctx := atomicTestContext()
	reader := atomicReadTool(t, dir)

	if _, err := reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt"})); err != nil {
		t.Fatal(err)
	}
	target := fileops.DiskTarget(path, nil)
	observed := fileops.FromContext(ctx).Get(target)
	if observed.Kind != fileops.Present {
		t.Fatalf("the read recorded no observation: %+v", observed)
	}

	// The teammate's edit: same byte count, same metadata version.
	atomicWriteFile(t, path, "BBBB\n")
	fileops.FromContext(ctx).ObservePresent(target, observed.Version)

	writer := atomicWrite{workDir: dir, roots: realRoots([]string{dir})}
	_, err := writer.Execute(ctx, atomicReadArgs(t, map[string]any{
		"path": "a.txt", "mode": "patch",
		"edits": []map[string]string{{"old": "BBBB", "new": "CCCC"}},
	}))
	var opErr *tool.OperationError
	if !errors.As(err, &opErr) || opErr.Diagnostic.Code != tool.FSStaleVersion {
		t.Fatalf("write over a collided version = %v, want a stale-version refusal", err)
	}
	if cause := opErr.Cause.Error(); !strings.Contains(cause, "-AAAA") || !strings.Contains(cause, "+BBBB") {
		t.Fatalf("refusal did not carry the hunk against the read's bytes: %q", cause)
	}
	if got, _ := os.ReadFile(path); string(got) != "BBBB\n" {
		t.Fatalf("a refused write changed the file: %q", got)
	}

	// A fresh read recovers, and the same patch then applies.
	if _, err := reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt"})); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Execute(ctx, atomicReadArgs(t, map[string]any{
		"path": "a.txt", "mode": "patch",
		"edits": []map[string]string{{"old": "BBBB", "new": "CCCC"}},
	})); err != nil {
		t.Fatalf("patch after re-read: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "CCCC\n" {
		t.Fatalf("file after recovery = %q", got)
	}
}

// The same collision, reached through the filesystem rather than by asserting
// the version: back-to-back same-size writes on a coarse-ctime host really do
// produce an identical version. The case runs until it observes one and then
// requires the write to be refused; it skips on a host where no collision
// occurs, so it never asserts something the platform cannot produce.
func TestAtomicWriteRefusesCollidedVersionFromTheFilesystem(t *testing.T) {
	caught := 0
	for i := 0; i < 200 && caught < 8; i++ {
		atomicResetSnapshotCache()
		dir := t.TempDir()
		path := filepath.Join(dir, "a.txt")
		atomicWriteFile(t, path, "AAAA\n")
		ctx := atomicTestContext()
		reader := atomicReadTool(t, dir)
		if _, err := reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt"})); err != nil {
			t.Fatal(err)
		}
		observed := fileops.FromContext(ctx).Get(fileops.DiskTarget(path, nil))
		atomicWriteFile(t, path, "BBBB\n")
		src, err := readEditSource(ctx, nil, path)
		if err != nil {
			t.Fatal(err)
		}
		_, version, err := src.observation(nil, path)
		if err != nil {
			t.Fatal(err)
		}
		if string(version) != string(observed.Version) {
			continue // no collision this round
		}
		writer := atomicWrite{workDir: dir, roots: realRoots([]string{dir})}
		if _, err := writer.Execute(ctx, atomicReadArgs(t, map[string]any{
			"path": "a.txt", "mode": "patch",
			"edits": []map[string]string{{"old": "BBBB", "new": "CCCC"}},
		})); err == nil {
			got, _ := os.ReadFile(path)
			t.Fatalf("iteration %d published over a teammate's edit; file=%q", i, got)
		}
		caught++
	}
	if caught == 0 {
		t.Skip("this filesystem produced no metadata-version collision")
	}
	t.Logf("refused %d collided-version writes", caught)
}

// A successful read records the observation that authorizes a later write, and
// a write that goes through another tool refreshes it — the pair and the
// existing writers share one observation store.
func TestAtomicReadObservationInteroperatesWithExistingWriters(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	atomicWriteFile(t, path, "alpha\n")
	ctx := atomicTestContext()
	reader := atomicReadTool(t, dir)
	writer := atomicWrite{workDir: dir, roots: realRoots([]string{dir})}

	if _, err := reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt"})); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Execute(ctx, atomicReadArgs(t, map[string]any{
		"path": "a.txt", "mode": "patch",
		"edits": []map[string]string{{"old": "alpha", "new": "ALPHA"}},
	})); err != nil {
		t.Fatalf("patch after atomic_read: %v", err)
	}
	// The write refreshed the observation, so a second write with no explicit
	// since builds on the first.
	if _, err := writer.Execute(ctx, atomicReadArgs(t, map[string]any{
		"path": "a.txt", "mode": "append", "content": "second\n",
	})); err != nil {
		t.Fatalf("append after a successful patch: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ALPHA\nsecond\n" {
		t.Fatalf("file = %q", got)
	}
}

// The delta path must diff against the bytes the cited read actually delivered,
// not against whatever the path happens to hold now.
func TestAtomicReadDeltaDiffsAgainstTheCitedRead(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	atomicWriteFile(t, path, "one\ntwo\nthree\n")
	ctx := atomicTestContext()
	reader := atomicReadTool(t, dir)

	first, err := reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	firstID := atomicHeaderReadID(t, first)

	atomicWriteFile(t, path, "one\nTWO\nthree\n")
	second, err := reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	secondID := atomicHeaderReadID(t, second)
	if secondID == firstID {
		t.Fatal("two different contents produced the same read id")
	}

	// Diffing the *second* read against the *first* read must show two→TWO, not
	// the current file against itself.
	out, err := reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt", "mode": "delta", "since": firstID}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "-two") || !strings.Contains(out, "+TWO") {
		t.Fatalf("delta did not diff against the cited read: %q", out)
	}
	// The reverse direction is unchanged, because the file has not moved since.
	out, err = reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt", "mode": "delta", "since": secondID}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "unchanged") {
		t.Fatalf("delta against the current read = %q", out)
	}
}

// An evicted read id is refused rather than silently diffed against something
// else: a wrong diff is worse than no diff.
func TestAtomicReadDeltaRefusesEvictedRead(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	ctx := atomicTestContext()
	reader := atomicReadTool(t, dir)

	var firstID string
	for i := range atomicSnapshotCacheEntries + 4 {
		atomicWriteFile(t, path, strings.Repeat("x", 32+i)+"\n")
		out, err := reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt"}))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstID = atomicHeaderReadID(t, out)
		}
	}
	_, err := reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt", "mode": "delta", "since": firstID}))
	var opErr *tool.OperationError
	if !errors.As(err, &opErr) || opErr.Diagnostic.Code != tool.FSNotObserved {
		t.Fatalf("delta on an evicted read = %v", err)
	}
}

// A read never writes: neither the file's bytes nor its mode may change.
func TestAtomicReadIsSideEffectFree(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	atomicWriteFile(t, path, "alpha\nbeta\n")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := atomicTestContext()
	reader := atomicReadTool(t, dir)

	for _, mode := range []string{"auto", "window", "outline", "tail"} {
		if _, err := reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt", "mode": mode})); err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("a read changed the file: %+v -> %+v", before, after)
	}
	// The observation it leaves is Present, which is what a later write needs.
	if observed := fileops.FromContext(ctx).Get(fileops.DiskTarget(path, nil)); observed.Kind != fileops.Present {
		t.Fatalf("observation kind = %v, want Present", observed.Kind)
	}
}
