package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/fileops"
	"reasonix/internal/tool"
)

func atomicTestContext() context.Context {
	return fileops.WithStore(context.Background(), fileops.NewStore())
}

func atomicWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A read id must be reproducible from the bytes and version it names, and must
// not depend on anything else the host happens to be holding.
func TestAtomicReadIDIsContentAddressed(t *testing.T) {
	content := []byte("alpha\nbeta\n")
	first := atomicReadID(atomicRouteDisk, "/w/a.go", "disk-v1:1", content)
	second := atomicReadID(atomicRouteDisk, "/w/a.go", "disk-v1:1", content)
	if first != second {
		t.Fatalf("same content and version produced different ids: %q vs %q", first, second)
	}
	if !atomicReadIDShape(first) {
		t.Fatalf("id %q does not have the documented r-<hex> shape", first)
	}
	for _, other := range []struct {
		name    string
		route   string
		path    string
		version fileops.Version
		content []byte
	}{
		{"different content", atomicRouteDisk, "/w/a.go", "disk-v1:1", []byte("alpha\nBETA\n")},
		{"different version", atomicRouteDisk, "/w/a.go", "disk-v1:2", content},
		{"different path", atomicRouteDisk, "/w/b.go", "disk-v1:1", content},
		{"different route", atomicRouteOverlay, "/w/a.go", "disk-v1:1", content},
	} {
		if got := atomicReadID(other.route, other.path, other.version, other.content); got == first {
			t.Errorf("%s produced the same read id", other.name)
		}
	}
}

// A fabricated id must not resolve: the model can only cite a read the host
// actually performed.
func TestAtomicAnchorForRejectsUnknownReadID(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	atomicWriteFile(t, path, "alpha\n")
	ctx := atomicTestContext()

	_, err := atomicAnchorFor(ctx, nil, path, "r-deadbeef")
	var opErr *tool.OperationError
	if !errors.As(err, &opErr) || opErr.Diagnostic.Code != tool.FSNotObserved {
		t.Fatalf("unknown since error = %v", err)
	}
	if _, err := atomicAnchorFor(ctx, nil, path, "not-an-id"); !errors.As(err, &opErr) || opErr.Diagnostic.Code != tool.FSNotObserved {
		t.Fatalf("malformed since error = %v", err)
	}
}

// A read leaves an anchor a later write can cite by id, and re-reading the
// unchanged file yields the same id.
func TestAtomicCaptureAnchorRoundTripsThroughSince(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	atomicWriteFile(t, path, "alpha\nbeta\n")
	ctx := atomicTestContext()

	anchor, err := atomicCapture(ctx, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	if anchor.Missing || anchor.ReadID == "" || anchor.Lines != 2 || anchor.Bytes != len("alpha\nbeta\n") {
		t.Fatalf("anchor = %+v", anchor)
	}
	byID, err := atomicAnchorFor(ctx, nil, path, anchor.ReadID)
	if err != nil {
		t.Fatal(err)
	}
	if byID.ContentHash != anchor.ContentHash || byID.Version != anchor.Version {
		t.Fatalf("since=%s resolved to %+v, want %+v", anchor.ReadID, byID, anchor)
	}
	// since="" resolves through the session observation instead.
	byObservation, err := atomicAnchorFor(ctx, nil, path, "")
	if err != nil {
		t.Fatal(err)
	}
	if byObservation.ContentHash != anchor.ContentHash {
		t.Fatalf("observation anchor = %+v, want %+v", byObservation, anchor)
	}
}

// A read id is bound to its own path: citing it for another file is refused
// rather than diffed against unrelated bytes.
func TestAtomicAnchorForIsBoundToItsPath(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	first, second := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	atomicWriteFile(t, first, "alpha\n")
	atomicWriteFile(t, second, "alpha\n")
	ctx := atomicTestContext()

	anchor, err := atomicCapture(ctx, nil, first)
	if err != nil {
		t.Fatal(err)
	}
	_, err = atomicAnchorFor(ctx, nil, second, anchor.ReadID)
	var opErr *tool.OperationError
	if !errors.As(err, &opErr) || opErr.Diagnostic.Code != tool.FSNotObserved {
		t.Fatalf("cross-path since error = %v", err)
	}
}

// The recheck is the CAS gate: matching content passes through untouched, a
// concurrent write produces FSStaleVersion carrying the changed hunks, and a
// deleted target is a conflict too — never a silent recreate.
func TestAtomicRecheckDetectsLostUpdate(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	atomicWriteFile(t, path, "alpha\nbeta\ngamma\n")
	ctx := atomicTestContext()

	anchor, err := atomicCapture(ctx, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	current, err := atomicRecheck(ctx, nil, path, anchor)
	if err != nil || string(current) != "alpha\nbeta\ngamma\n" {
		t.Fatalf("unchanged recheck = %q, %v", current, err)
	}

	atomicWriteFile(t, path, "alpha\nBETA\ngamma\n")
	_, err = atomicRecheck(ctx, nil, path, anchor)
	var opErr *tool.OperationError
	if !errors.As(err, &opErr) || opErr.Diagnostic.Code != tool.FSStaleVersion {
		t.Fatalf("stale recheck error = %v", err)
	}
	if opErr.Diagnostic.ExpectedSnapshot != anchor.ReadID {
		t.Fatalf("diagnostic expected snapshot = %q, want the read id", opErr.Diagnostic.ExpectedSnapshot)
	}
	if cause := opErr.Cause.Error(); !strings.Contains(cause, "-beta") || !strings.Contains(cause, "+BETA") {
		t.Fatalf("conflict cause carries no hunk: %q", cause)
	}
	if len(opErr.Cause.Error()) > atomicReadConflictBudgetBytes+256 {
		t.Fatalf("conflict cause is %d bytes, unbounded", len(opErr.Cause.Error()))
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := atomicRecheck(ctx, nil, path, anchor); !errors.As(err, &opErr) || opErr.Diagnostic.Code != tool.FSStaleVersion {
		t.Fatalf("deleted-target recheck error = %v", err)
	}
}

// The recheck also hands back the source it compared, so a writer re-encodes
// and re-publishes to the same store it read from.
func TestAtomicRecheckSourceReturnsReadRoute(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	atomicWriteFile(t, path, "alpha\n")
	ctx := atomicTestContext()

	anchor, err := atomicCapture(ctx, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	src, current, err := atomicRecheckSource(ctx, nil, path, anchor)
	if err != nil {
		t.Fatal(err)
	}
	if src.overlay || string(current) != "alpha\n" || src.content != "alpha\n" {
		t.Fatalf("source = %+v, bytes = %q", src, current)
	}
}

// An anchor for a target that does not exist authorizes a create and nothing
// else.
func TestAtomicCaptureAbsentTarget(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "missing.txt")
	ctx := atomicTestContext()

	anchor, err := atomicCapture(ctx, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	if !anchor.Missing || anchor.ReadID != "" {
		t.Fatalf("absent anchor = %+v", anchor)
	}
	if observed := fileops.FromContext(ctx).Get(fileops.DiskTarget(path, nil)); observed.Kind != fileops.Absent {
		t.Fatalf("observation kind = %v, want Absent", observed.Kind)
	}
	if _, err := atomicRecheck(ctx, nil, path, anchor); err != nil {
		t.Fatalf("absent recheck = %v", err)
	}
	atomicWriteFile(t, path, "raced\n")
	if _, err := atomicRecheck(ctx, nil, path, anchor); err == nil {
		t.Fatal("a target created by someone else must conflict with an absent anchor")
	}
}

// The hunk renderer reports only what changed, keeps three lines of context,
// and stays inside its budget.
func TestAtomicDeltaHunks(t *testing.T) {
	if got := atomicDeltaHunks([]byte("same\n"), []byte("same\n"), atomicReadConflictBudgetBytes); got != nil {
		t.Fatalf("unchanged content produced hunks: %v", got)
	}
	oldText := strings.Repeat("context line\n", 40) + "target\n" + strings.Repeat("tail line\n", 40)
	newText := strings.Replace(oldText, "target\n", "TARGET\n", 1)
	hunks := atomicDeltaHunks([]byte(oldText), []byte(newText), atomicReadConflictBudgetBytes)
	if len(hunks) == 0 {
		t.Fatal("no hunks for a changed line")
	}
	joined := strings.Join(hunks, "\n")
	for _, want := range []string{"@@", "-target", "+TARGET", " context line"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("hunk output missing %q:\n%s", want, joined)
		}
	}
	if len(joined) > atomicReadConflictBudgetBytes {
		t.Fatalf("hunk output is %d bytes, over the %d budget", len(joined), atomicReadConflictBudgetBytes)
	}
	// A whole-file change stays bounded and says what it withheld.
	rewritten := atomicDeltaHunks([]byte(strings.Repeat("old\n", 500)), []byte(strings.Repeat("new\n", 500)), atomicReadConflictBudgetBytes)
	if total := len(strings.Join(rewritten, "\n")); total > atomicReadConflictBudgetBytes {
		t.Fatalf("clipped hunk output is %d bytes, over the %d budget", total, atomicReadConflictBudgetBytes)
	}
	if !strings.Contains(strings.Join(rewritten, "\n"), "withheld") {
		t.Fatalf("clipped hunk output does not say what it withheld: %v", rewritten)
	}
}

func TestAtomicReceiptLineIsBounded(t *testing.T) {
	got := atomicReceiptLine("patch", "internal/a.go", 0, 0, "(+12 -3)", "2140→2149 lines")
	if got != "patch internal/a.go (+12 -3) 2140→2149 lines" {
		t.Fatalf("receipt = %q", got)
	}
	long := atomicReceiptLine("replace", strings.Repeat("p", 4096), 0, 0)
	if len(long) > atomicReceiptBudgetBytes {
		t.Fatalf("long receipt is %d bytes, over the %d budget", len(long), atomicReceiptBudgetBytes)
	}
}

// Clipping must never tear a rune in half.
func TestAtomicClipUTF8KeepsRuneBoundaries(t *testing.T) {
	text := strings.Repeat("中文内容", 200)
	for _, budget := range []int{1, 2, 3, 5, 17, 64, 129, len(text) - 1} {
		clipped := atomicClipUTF8(text, budget)
		if len(clipped) > budget {
			t.Fatalf("budget %d: clipped to %d bytes", budget, len(clipped))
		}
		if !utf8Valid(clipped) {
			t.Fatalf("budget %d: clip split a rune: %q", budget, clipped)
		}
	}
	if got := atomicClipUTF8(text, 0); got != text {
		t.Fatalf("a zero budget must mean unbounded, got %q", got)
	}
	if got := atomicClipUTF8("short", 64); got != "short" {
		t.Fatalf("an in-budget clip changed the text: %q", got)
	}
}

func utf8Valid(text string) bool {
	for _, r := range text {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

func TestAtomicLineCountMatchesReaderNumbering(t *testing.T) {
	for _, tc := range []struct {
		content string
		want    int
	}{
		{"", 0},
		{"one", 1},
		{"one\n", 1},
		{"one\ntwo", 2},
		{"one\ntwo\n", 2},
		{"\n", 1},
	} {
		if got := atomicLineCount([]byte(tc.content)); got != tc.want {
			t.Errorf("atomicLineCount(%q) = %d, want %d", tc.content, got, tc.want)
		}
	}
}

// The snapshot cache is what makes `since` resolvable after a teammate's write.
// It must stay bounded, evict oldest-first, and stop answering for a snapshot
// it has dropped.
func TestAtomicSnapshotCacheStaysBounded(t *testing.T) {
	atomicResetSnapshotCache()
	t.Cleanup(atomicResetSnapshotCache)
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	ctx := atomicTestContext()

	var firstID string
	for i := range atomicSnapshotCacheEntries + 4 {
		// A distinct size per iteration guarantees a distinct content hash and
		// therefore a distinct read id; the cache collapses identical content
		// onto one entry by design.
		atomicWriteFile(t, path, strings.Repeat("x", 32+i)+"\n")
		anchor, err := atomicCapture(ctx, nil, path)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstID = anchor.ReadID
		}
	}
	atomicSnapshots.mu.Lock()
	entries, total := len(atomicSnapshots.byID), atomicSnapshots.total
	atomicSnapshots.mu.Unlock()
	if entries > atomicSnapshotCacheEntries {
		t.Fatalf("cache holds %d entries, over the %d cap", entries, atomicSnapshotCacheEntries)
	}
	if total > atomicSnapshotCacheBytes {
		t.Fatalf("cache holds %d bytes, over the %d cap", total, atomicSnapshotCacheBytes)
	}
	if entries == 0 {
		t.Fatal("cache evicted everything; `since` would never resolve")
	}
	if _, ok := atomicCachedAnchor(firstID, path); ok {
		t.Fatal("the oldest snapshot survived eviction")
	}
}

// A refused write must leave the disk byte-identical.
func TestAtomicConflictLeavesDiskUntouched(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	atomicWriteFile(t, path, "alpha\n")
	ctx := atomicTestContext()

	anchor, err := atomicCapture(ctx, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	atomicWriteFile(t, path, "alpha\nbeta\n")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := atomicRecheck(ctx, nil, path, anchor); err == nil {
		t.Fatal("expected a conflict")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("a refused write changed the disk: %q -> %q", before, after)
	}
}

// The overlay route is the host's unsaved buffer: an anchor must describe the
// buffer, and a conflict must diff against the buffer, not the stale disk copy.
func TestAtomicAnchorFollowsOverlayRoute(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	atomicWriteFile(t, path, "saved\n")
	overlay := &fakeOverlay{files: map[string]string{path: "unsaved\n"}, writes: map[string]string{}}
	ctx := atomicTestContext()

	anchor, err := atomicCapture(ctx, overlay, path)
	if err != nil {
		t.Fatal(err)
	}
	if anchor.Route != atomicRouteOverlay || anchor.ContentHash != atomicContentHash([]byte("unsaved\n")) {
		t.Fatalf("overlay anchor = %+v", anchor)
	}
	overlay.files[path] = "unsaved again\n"
	_, err = atomicRecheck(ctx, overlay, path, anchor)
	var opErr *tool.OperationError
	if !errors.As(err, &opErr) || opErr.Diagnostic.Code != tool.FSStaleVersion {
		t.Fatalf("overlay conflict error = %v", err)
	}
	if cause := opErr.Cause.Error(); !strings.Contains(cause, "-unsaved") || !strings.Contains(cause, "+unsaved again") {
		t.Fatalf("overlay conflict diffed against the wrong store: %q", cause)
	}
}

// The read contract's own tests live with the reader; this one only pins that
// the shared capture observes the file the read_file route observes.
func TestAtomicCaptureRecordsObservationForLaterWriters(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	atomicWriteFile(t, path, "alpha\n")
	ctx := atomicTestContext()

	if _, err := atomicCapture(ctx, nil, path); err != nil {
		t.Fatal(err)
	}
	// The existing writer must accept a file whose only prior read was an
	// atomic_read: the pair shares one observation store.
	edit := editFile{workDir: dir}
	if _, err := edit.Execute(ctx, json.RawMessage(`{"path":"a.txt","old_string":"alpha","new_string":"ALPHA"}`)); err != nil {
		t.Fatalf("edit_file after atomic_read: %v", err)
	}
}
