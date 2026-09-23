package builtin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/fileops"
	"reasonix/internal/tool"
)

func atomicReadTool(t *testing.T, dir string) atomicRead {
	t.Helper()
	return atomicRead{workDir: dir, forbidRoots: realRoots(nil)}
}

func atomicReadArgs(t *testing.T, m map[string]any) json.RawMessage {
	t.Helper()
	return argsJSON(t, m)
}

// The header is the only place a read id is emitted, and it is what a later
// write cites as `since`. Without it the whole anchor protocol is unreachable.
func TestAtomicReadHeaderCarriesReadIDAndShape(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	atomicWriteFile(t, filepath.Join(dir, "a.txt"), "alpha\nbeta\n")
	ctx := atomicTestContext()
	r := atomicReadTool(t, dir)

	out, env, err := r.ExecuteRead(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	header := strings.SplitN(out, "\n", 2)[0]
	id := atomicHeaderReadID(t, header)
	if !atomicReadIDShape(id) {
		t.Fatalf("header carries no usable read id: %q", header)
	}
	if !strings.Contains(header, "a.txt") || !strings.Contains(header, "2 lines") {
		t.Fatalf("header = %q", header)
	}
	if !strings.Contains(out, "1→alpha") || !strings.Contains(out, "2→beta") {
		t.Fatalf("window body = %q", out)
	}
	if env.Source.Snapshot == "" || env.Source.CanonicalPath == "" {
		t.Fatalf("envelope = %+v", env)
	}
	if env.ReadID != "" && env.ReadID == id {
		t.Fatalf("the tool's own id and the host's read id must be different namespaces")
	}
	// The header id must resolve as an anchor, or `since` is unusable.
	if _, err := atomicAnchorFor(ctx, nil, filepath.Join(dir, "a.txt"), id); err != nil {
		t.Fatalf("the header's read id does not resolve: %v", err)
	}
}

func atomicHeaderReadID(t *testing.T, header string) string {
	t.Helper()
	fields := strings.Fields(header)
	if len(fields) < 2 || fields[0] != "read" {
		t.Fatalf("header does not start with `read <id>`: %q", header)
	}
	return fields[1]
}

// mode=auto returns a small file whole and a large one as an outline plus a
// head window — the two shapes a member actually needs, chosen without asking.
func TestAtomicReadAutoThreshold(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	ctx := atomicTestContext()
	r := atomicReadTool(t, dir)

	small := filepath.Join(dir, "small.txt")
	atomicWriteFile(t, small, strings.Repeat("small line\n", atomicAutoWholeMaxLines))
	out, err := r.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "small.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "auto→window") || !strings.Contains(out, "small line") {
		t.Fatalf("small file auto read = %q", firstLines(out, 2))
	}

	large := filepath.Join(dir, "large.go")
	var big strings.Builder
	big.WriteString("package main\n\n")
	for i := 0; i < atomicAutoWholeMaxLines; i++ {
		big.WriteString("func Handler" + itoa(i) + "(ctx context.Context) error { return nil }\n")
	}
	atomicWriteFile(t, large, big.String())
	out, err = r.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "large.go"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "auto→outline+head") {
		t.Fatalf("large file auto read header = %q", firstLines(out, 1))
	}
	if !strings.Contains(out, "60→func Handler") {
		t.Fatalf("auto did not deliver the head window:\n%s", out[:min(400, len(out))])
	}
	if strings.Contains(out, "func Handler"+itoa(atomicAutoWholeMaxLines-1)) {
		t.Fatalf("auto delivered the whole file instead of a bounded head")
	}
	if len(out) > atomicReadBudgetBytes {
		t.Fatalf("auto returned %d bytes, over the %d budget", len(out), atomicReadBudgetBytes)
	}
}

// mode=window honours offset/limit and always numbers lines absolutely, so a
// window read is citable without any host metadata.
func TestAtomicReadWindowOffsetAndLimit(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	atomicWriteFile(t, filepath.Join(dir, "a.txt"), "one\ntwo\nthree\nfour\nfive\n")
	ctx := atomicTestContext()
	r := atomicReadTool(t, dir)

	out, err := r.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt", "mode": "window", "offset": 2, "limit": 2}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "3→three") || !strings.Contains(out, "4→four") {
		t.Fatalf("window = %q", out)
	}
	if strings.Contains(out, "5→five") {
		t.Fatalf("window ignored limit: %q", out)
	}
	if !strings.Contains(out, "pass offset=4") {
		t.Fatalf("window does not say how to continue: %q", out)
	}

	past := atomicReadArgs(t, map[string]any{"path": "a.txt", "mode": "window", "offset": 99, "limit": 5})
	out, err = r.Execute(ctx, past)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "past EOF") {
		t.Fatalf("past-EOF read = %q", out)
	}

	if _, err := r.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt", "mode": "sideways"})); err == nil {
		t.Fatal("an unknown mode must be rejected, not silently defaulted")
	}
	if _, err := r.Execute(ctx, atomicReadArgs(t, map[string]any{"path": ""})); err == nil {
		t.Fatal("an empty path must be rejected")
	}
}

// mode=outline maps Go symbols, markdown headings, and falls back to a
// recognizable head for a file with no symbols at all.
func TestAtomicReadOutline(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	ctx := atomicTestContext()
	r := atomicReadTool(t, dir)

	atomicWriteFile(t, filepath.Join(dir, "a.go"), "package main\n\nimport \"fmt\"\n\nfunc Alpha() {}\n\ntype Beta struct{}\n\nfunc (b Beta) Gamma() {}\n\nfunc main() { fmt.Println(Alpha) }\n")
	out, err := r.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.go", "mode": "outline"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"5→func Alpha", "7→struct Beta", "9→method Beta.Gamma", "11→func main"} {
		if !strings.Contains(out, want) {
			t.Fatalf("outline missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "fmt.Println") {
		t.Fatalf("outline leaked body lines:\n%s", out)
	}

	atomicWriteFile(t, filepath.Join(dir, "doc.md"), "# Title\n\nbody\n\n## Section one\n\nmore\n\n### Deep\n")
	out, err = r.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "doc.md", "mode": "outline"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1→h1 Title", "5→h2 Section one", "9→h3 Deep"} {
		if !strings.Contains(out, want) {
			t.Fatalf("markdown outline missing %q:\n%s", want, out)
		}
	}

	atomicWriteFile(t, filepath.Join(dir, "notes.txt"), strings.Repeat("just prose\n", 60))
	out, err = r.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "notes.txt", "mode": "outline"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "outline (no symbols)") || !strings.Contains(out, "1→just prose") {
		t.Fatalf("symbol-free outline did not degrade to a head: %q", firstLines(out, 2))
	}
}

// mode=delta reports only what changed since the cited read, and says
// "unchanged" rather than returning an empty body.
func TestAtomicReadDeltaReportsOnlyChanges(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	atomicWriteFile(t, path, "alpha\nbeta\ngamma\n")
	ctx := atomicTestContext()
	r := atomicReadTool(t, dir)

	first, err := r.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	id := atomicHeaderReadID(t, first)

	out, err := r.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt", "mode": "delta", "since": id}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "unchanged") {
		t.Fatalf("delta over an untouched file = %q", out)
	}

	atomicWriteFile(t, path, "alpha\nBETA\ngamma\n")
	out, err = r.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt", "mode": "delta", "since": id}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "-beta") || !strings.Contains(out, "+BETA") {
		t.Fatalf("delta did not report the change:\n%s", out)
	}
	if strings.Contains(out, "+gamma") || strings.Contains(out, "-alpha") {
		t.Fatalf("delta reported unchanged context as a change:\n%s", out)
	}
	if !strings.Contains(out, "[delta "+id+"→") {
		t.Fatalf("delta header does not name both anchors: %q", firstLines(out, 1))
	}
}

// delta without a since, or with an id the host never issued, is refused: there
// is nothing honest to diff against.
func TestAtomicReadDeltaRequiresAKnownSince(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	atomicWriteFile(t, filepath.Join(dir, "a.txt"), "alpha\n")
	ctx := atomicTestContext()
	r := atomicReadTool(t, dir)

	_, err := r.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt", "mode": "delta"}))
	var opErr *tool.OperationError
	if !errors.As(err, &opErr) || opErr.Diagnostic.Code != tool.FSNotObserved {
		t.Fatalf("delta without since = %v", err)
	}
	_, err = r.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt", "mode": "delta", "since": "r-deadbeef"}))
	if !errors.As(err, &opErr) || opErr.Diagnostic.Code != tool.FSNotObserved {
		t.Fatalf("delta with an unknown since = %v", err)
	}
}

// mode=tail returns the end of the file — the part a log reader wants.
func TestAtomicReadTail(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 200; i++ {
		b.WriteString("line " + itoa(i) + "\n")
	}
	atomicWriteFile(t, filepath.Join(dir, "log.txt"), b.String())
	ctx := atomicTestContext()
	r := atomicReadTool(t, dir)

	out, err := r.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "log.txt", "mode": "tail"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "200→line 200") || !strings.Contains(out, "[tail 121-200/200]") {
		t.Fatalf("tail = %q", firstLines(out, 1))
	}
	if strings.Contains(out, "1→line 1\n") {
		t.Fatalf("tail returned the head too:\n%s", out)
	}
}

// Every mode's result stays inside the read budget. This is the property the
// whole tool exists for; an unbounded mode would silently reintroduce the
// whole-file read it replaces.
func TestAtomicReadResultsStayWithinBudget(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	ctx := atomicTestContext()
	r := atomicReadTool(t, dir)

	var b strings.Builder
	for i := 0; i < 4000; i++ {
		b.WriteString("func Handler" + itoa(i) + "(ctx context.Context) error { return nil } // padding padding padding\n")
	}
	atomicWriteFile(t, filepath.Join(dir, "big.go"), b.String())

	for _, mode := range []string{"auto", "window", "outline", "tail"} {
		out, err := r.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "big.go", "mode": mode}))
		if err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
		if len(out) > atomicReadBudgetBytes {
			t.Errorf("mode %s returned %d bytes, over the %d budget", mode, len(out), atomicReadBudgetBytes)
		}
	}
}

// A non-UTF-8 file is decoded for display while the disk copy stays in its own
// encoding — the reader must never rewrite bytes as a side effect of reading.
func TestAtomicReadDecodesNonUTF8WithoutRewriting(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "gbk.txt")
	raw := gbkBytes(t, "你好世界\n这是第二行\n")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := atomicTestContext()
	r := atomicReadTool(t, dir)

	out, err := r.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "gbk.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "你好世界") || !strings.Contains(out, "这是第二行") {
		t.Fatalf("GBK content was not decoded:\n%s", out)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(raw) {
		t.Fatal("a read rewrote the file's encoding")
	}
}

// A UTF-16 file is text, not binary: its NUL bytes must not trip the binary
// rejection.
func TestAtomicReadAcceptsUTF16(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "u16.txt")
	content := "first line\nsecond line\n"
	if err := os.WriteFile(path, utf16LEBytes(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := atomicTestContext()
	r := atomicReadTool(t, dir)

	out, err := r.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "u16.txt"}))
	if err != nil {
		t.Fatalf("UTF-16 read: %v", err)
	}
	if !strings.Contains(out, "first line") || !strings.Contains(out, "second line") {
		t.Fatalf("UTF-16 content was not decoded:\n%s", out)
	}
}

// A binary target is refused before anything is anchored.
func TestAtomicReadRejectsBinary(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "blob.bin")
	if err := os.WriteFile(path, []byte{0x7f, 'E', 'L', 'F', 0x00, 0x01, 0x02, 0x00, 0x03}, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := atomicTestContext()
	r := atomicReadTool(t, dir)

	_, _, err := r.ExecuteRead(ctx, atomicReadArgs(t, map[string]any{"path": "blob.bin"}))
	if err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("binary read error = %v", err)
	}
	if observed := fileops.FromContext(ctx).Get(fileops.DiskTarget(path, nil)); observed.Kind == fileops.Present {
		t.Fatal("a refused binary read must not leave a present observation")
	}
}

// A missing target is FSNotFound with the create-only-if-required recovery, and
// the observation it leaves is Absent — which is what authorizes a later create.
func TestAtomicReadMissingFileObservesAbsent(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "missing.txt")
	ctx := atomicTestContext()
	r := atomicReadTool(t, dir)

	_, _, err := r.ExecuteRead(ctx, atomicReadArgs(t, map[string]any{"path": "missing.txt"}))
	var opErr *tool.OperationError
	if !errors.As(err, &opErr) || opErr.Diagnostic.Code != tool.FSNotFound {
		t.Fatalf("missing read error = %v", err)
	}
	if observed := fileops.FromContext(ctx).Get(fileops.DiskTarget(path, nil)); observed.Kind != fileops.Absent {
		t.Fatalf("observation kind = %v, want Absent", observed.Kind)
	}
}

// A forbidden root must look absent, not forbidden: a distinguishable refusal
// is itself a disclosure.
func TestAtomicReadForbiddenRootLooksAbsent(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.MkdirAll(secret, 0o755); err != nil {
		t.Fatal(err)
	}
	atomicWriteFile(t, filepath.Join(secret, "a.txt"), "classified\n")
	ctx := atomicTestContext()
	r := atomicRead{workDir: dir, forbidRoots: realRoots([]string{secret})}

	_, _, err := r.ExecuteRead(ctx, atomicReadArgs(t, map[string]any{"path": "secret/a.txt"}))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forbidden read error = %v, want a not-exist shape", err)
	}
}

// A directory is not a missing file: the model's next move differs, so the
// error must too.
func TestAtomicReadDirectoryIsNamedAsSuch(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := atomicTestContext()
	r := atomicReadTool(t, dir)

	_, _, err := r.ExecuteRead(ctx, atomicReadArgs(t, map[string]any{"path": "sub"}))
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("directory read error = %v", err)
	}
}

// An empty file is a valid read, not an error.
func TestAtomicReadEmptyFile(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	atomicWriteFile(t, filepath.Join(dir, "empty.txt"), "")
	ctx := atomicTestContext()
	r := atomicReadTool(t, dir)

	out, env, err := r.ExecuteRead(ctx, atomicReadArgs(t, map[string]any{"path": "empty.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "(empty file)") || !strings.Contains(out, "0 lines") {
		t.Fatalf("empty read = %q", out)
	}
	if env.SourceEnd == nil || *env.SourceEnd != 0 {
		t.Fatalf("empty read envelope = %+v", env)
	}
}

// The reader resolves through the host's unsaved buffer when one is available,
// and anchors what the buffer holds — not the stale disk copy.
func TestAtomicReadFollowsOverlay(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	atomicWriteFile(t, path, "saved line\n")
	overlay := &fakeOverlay{files: map[string]string{path: "buffer line one\nbuffer line two\n"}}
	ctx := atomicTestContext()
	r := atomicRead{workDir: dir, overlay: overlay}

	out, env, err := r.ExecuteRead(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "buffer line one") || strings.Contains(out, "saved line") {
		t.Fatalf("overlay read = %q", out)
	}
	if env.Source.Kind != tool.ReadSourceOverlay {
		t.Fatalf("envelope source kind = %q, want overlay", env.Source.Kind)
	}
	id := atomicHeaderReadID(t, out)
	anchor, err := atomicAnchorFor(ctx, overlay, path, id)
	if err != nil {
		t.Fatal(err)
	}
	if anchor.Route != atomicRouteOverlay || anchor.ContentHash != atomicContentHash([]byte("buffer line one\nbuffer line two\n")) {
		t.Fatalf("anchor = %+v", anchor)
	}
}

// The tool declares its own classification rather than inheriting a default:
// reads are parallel-safe, and the snip geometry is front-loaded like read_file.
func TestAtomicReadDeclarations(t *testing.T) {
	r := atomicRead{}
	class := r.ClassifyCall(nil)
	if !class.Known || !class.ReadOnly || !class.ParallelSafe {
		t.Fatalf("ClassifyCall = %+v", class)
	}
	if !r.ReadOnly() || !r.PlanModeSafe() {
		t.Fatal("atomic_read must be read-only and plan-mode safe")
	}
	hint := r.SnipHint()
	if hint.Head <= 0 || hint.Tail <= 0 || hint.HeadChars <= 0 || hint.TailChars <= 0 {
		t.Fatalf("SnipHint = %+v", hint)
	}
	resolved, err := r.ResolveReadPath(atomicReadArgs(t, map[string]any{"path": "a.txt"}))
	if err != nil || !strings.HasSuffix(resolved, "a.txt") {
		t.Fatalf("ResolveReadPath = %q, %v", resolved, err)
	}
	if _, err := r.ResolveReadPath(json.RawMessage(`{}`)); err == nil {
		t.Fatal("ResolveReadPath must reject a missing path")
	}
}

// A read that gets clipped by the transport must not claim to have delivered
// the whole window: the envelope is what later evidence is built on.
func TestAtomicReadEnvelopeMarksIncomplete(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	atomicWriteFile(t, filepath.Join(dir, "a.txt"), "one\ntwo\nthree\n")
	ctx := atomicTestContext()
	r := atomicReadTool(t, dir)

	out, err := r.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt", "mode": "window", "offset": 0, "limit": 2}))
	if err != nil {
		t.Fatal(err)
	}
	env, ok := r.ReadEnvelope(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt", "mode": "window", "offset": 0, "limit": 2}), out)
	if !ok {
		t.Fatal("ReadEnvelope reported no envelope")
	}
	if !env.HasMore || env.EOF {
		t.Fatalf("partial window envelope = %+v", env)
	}
	if env.Source.Snapshot == "" {
		t.Fatal("envelope carries no source snapshot")
	}

	// A whole-file read is the same call with the window covering everything.
	full, err := r.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	env, ok = r.ReadEnvelope(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt"}), full)
	if !ok || !env.EOF || env.SourceEnd == nil || *env.SourceEnd != 3 {
		t.Fatalf("complete read envelope = %+v (ok=%v)", env, ok)
	}
	// ReadEnvelope is the host's own re-derivation: it must agree with the
	// in-band envelope ExecuteRead reported.
	_, inband, err := r.ExecuteRead(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	if inband.EOF != env.EOF || inband.Source.Snapshot != env.Source.Snapshot {
		t.Fatalf("in-band envelope %+v disagrees with ReadEnvelope %+v", inband, env)
	}
}

func firstLines(text string, n int) string {
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func utf16LEBytes(s string) []byte {
	out := []byte{0xFF, 0xFE}
	for _, r := range s {
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}
