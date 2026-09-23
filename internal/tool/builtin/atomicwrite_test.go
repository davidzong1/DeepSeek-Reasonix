package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fileenc "reasonix/internal/fileutil/encoding"
	"reasonix/internal/tool"
)

// atomicTestWriter is the tool under test, bound to one temp workspace with no
// overlay unless a case supplies one.
func atomicTestWriter(dir string, overlay FileOverlay) atomicWrite {
	return atomicWrite{workDir: dir, roots: realRoots([]string{dir}), overlay: overlay}
}

// atomicObserve is what an atomic_read leaves behind: an anchor in the session
// store plus the snapshot the CAS compares against. Every "read before you
// write" case starts here, so the writer is exercised against the real shared
// base rather than a stub.
func atomicObserve(t *testing.T, ctx context.Context, overlay FileOverlay, path string) atomicAnchor {
	t.Helper()
	anchor, err := atomicCapture(ctx, overlay, path)
	if err != nil {
		t.Fatalf("atomicCapture(%s): %v", path, err)
	}
	return anchor
}

func atomicCall(t *testing.T, w atomicWrite, ctx context.Context, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return w.Execute(ctx, raw)
}

func TestAtomicWriteCreateAndRefuseExisting(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "new.txt")

	out, err := atomicCall(t, w, ctx, map[string]any{"path": "new.txt", "mode": "create", "content": "hello\n"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(out, "create ") || strings.Contains(out, "hello") {
		t.Fatalf("receipt = %q, want a create receipt with no content", out)
	}
	if b, _ := os.ReadFile(path); string(b) != "hello\n" {
		t.Fatalf("disk = %q", b)
	}

	// A second create must be refused, and must not echo the existing content.
	_, err = atomicCall(t, w, ctx, map[string]any{"path": "new.txt", "mode": "create", "content": "other\n"})
	assertOperationCode(t, err, tool.FSStaleVersion)
	if strings.Contains(err.Error(), "hello") {
		t.Fatalf("the refusal echoed the file's content: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "hello\n" {
		t.Fatalf("a refused create changed the file: %q", b)
	}
}

func TestAtomicWriteReplaceNeedsAReadThenHoldsItsAnchor(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// No observation yet: the write is refused, and the file is untouched.
	_, err := atomicCall(t, w, ctx, map[string]any{"path": "a.go", "mode": "replace", "content": "x\n"})
	assertOperationCode(t, err, tool.FSNotObserved)
	if b, _ := os.ReadFile(path); string(b) != "one\ntwo\n" {
		t.Fatalf("a refused replace changed the file: %q", b)
	}

	atomicObserve(t, ctx, nil, path)
	if _, err := atomicCall(t, w, ctx, map[string]any{"path": "a.go", "mode": "replace", "content": "three\n"}); err != nil {
		t.Fatalf("replace after read: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "three\n" {
		t.Fatalf("disk = %q", b)
	}

	// A teammate's write after the read turns the next replace into a loud
	// rejection carrying the changed hunk, never a silent overwrite.
	atomicObserve(t, ctx, nil, path)
	if err := os.WriteFile(path, []byte("teammate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = atomicCall(t, w, ctx, map[string]any{"path": "a.go", "mode": "replace", "content": "mine\n"})
	assertOperationCode(t, err, tool.FSStaleVersion)
	if !strings.Contains(err.Error(), "teammate") {
		t.Fatalf("the conflict must show the changed hunk, got: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "teammate\n" {
		t.Fatalf("a rejected replace still wrote: %q", b)
	}
}

func TestAtomicWriteAppendNeedsNoRead(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "log.txt")
	if err := os.WriteFile(path, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := atomicCall(t, w, ctx, map[string]any{"path": "log.txt", "mode": "append", "content": "second\n"})
	if err != nil {
		t.Fatalf("append without a read: %v", err)
	}
	if !strings.HasPrefix(out, "append ") {
		t.Fatalf("receipt = %q", out)
	}
	if b, _ := os.ReadFile(path); string(b) != "first\nsecond\n" {
		t.Fatalf("disk = %q", b)
	}

	// Appending to a file that does not exist yet creates it: >> would, and the
	// mode exists to replace >>.
	if _, err := atomicCall(t, w, ctx, map[string]any{"path": "fresh.log", "mode": "append", "content": "line\n"}); err != nil {
		t.Fatalf("append to a missing file: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "fresh.log")); string(b) != "line\n" {
		t.Fatalf("fresh.log = %q", b)
	}

	// An oversized payload is refused with a route out, never chunked.
	big := strings.Repeat("x", atomicAppendMaxBytes+1)
	_, err = atomicCall(t, w, ctx, map[string]any{"path": "log.txt", "mode": "append", "content": big})
	assertOperationCode(t, err, tool.FSTooLarge)
	if !strings.Contains(err.Error(), "replace") {
		t.Fatalf("the size refusal must offer a route: %v", err)
	}
}

func TestAtomicWritePatchEditShapesMatchEditFile(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("alpha\nbeta\nalpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	atomicObserve(t, ctx, nil, path)

	// Not found: the same error shape edit_file produces, so the model's learned
	// recovery ("re-read, retry") applies unchanged.
	_, err := atomicCall(t, w, ctx, map[string]any{
		"path": "a.go", "mode": "patch",
		"edits": []map[string]string{{"old": "nowhere", "new": "x"}},
	})
	if err == nil || !strings.Contains(err.Error(), "old_string not found") {
		t.Fatalf("not-found error = %v, want edit_file's shape", err)
	}

	// Ambiguous: refused with the uniqueness error, and the file is untouched.
	_, err = atomicCall(t, w, ctx, map[string]any{
		"path": "a.go", "mode": "patch",
		"edits": []map[string]string{{"old": "alpha", "new": "x"}},
	})
	if err == nil || !strings.Contains(err.Error(), "not unique") {
		t.Fatalf("ambiguous error = %v, want edit_file's shape", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "alpha\nbeta\nalpha\n" {
		t.Fatalf("a refused patch changed the file: %q", b)
	}

	// A unique edit applies, and every byte outside it is carried over.
	if _, err := atomicCall(t, w, ctx, map[string]any{
		"path": "a.go", "mode": "patch",
		"edits": []map[string]string{{"old": "beta", "new": "BETA"}},
	}); err != nil {
		t.Fatalf("patch: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "alpha\nBETA\nalpha\n" {
		t.Fatalf("disk = %q", b)
	}
}

func TestAtomicWritePatchRangeSplicesOnlyTheRange(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("1\n2\n3\n4\n5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	atomicObserve(t, ctx, nil, path)

	if _, err := atomicCall(t, w, ctx, map[string]any{
		"path": "a.txt", "mode": "patch", "content": "two\nthree",
		"range": map[string]int{"start": 2, "end": 3},
	}); err != nil {
		t.Fatalf("range patch: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "1\ntwo\nthree\n4\n5\n" {
		t.Fatalf("disk = %q", b)
	}

	// An out-of-range request is refused rather than clamped: writing a
	// different range than the one asked for is how a range edit destroys a file.
	before, _ := os.ReadFile(path)
	_, err := atomicCall(t, w, ctx, map[string]any{
		"path": "a.txt", "mode": "patch", "content": "x",
		"range": map[string]int{"start": 4, "end": 99},
	})
	if err == nil || !strings.Contains(err.Error(), "outside the file") {
		t.Fatalf("out-of-range error = %v", err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatalf("an out-of-range patch changed the file: %q", after)
	}
}

func TestAtomicWriteDeleteIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "gone.txt")
	if err := os.WriteFile(path, []byte("bye\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	atomicObserve(t, ctx, nil, path)

	if _, err := atomicCall(t, w, ctx, map[string]any{"path": "gone.txt", "mode": "delete"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("delete left the file: %v", err)
	}

	// Already gone is a success that says so, not a failure: the caller's intent
	// is satisfied either way.
	out, err := atomicCall(t, w, ctx, map[string]any{"path": "gone.txt", "mode": "delete"})
	if err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if !strings.Contains(out, "already absent") {
		t.Fatalf("second delete receipt = %q", out)
	}
}

// TestAtomicWritePreservesEncoding pins the same contract write_file has: an
// overwrite must not silently rewrite a GBK file as UTF-8.
func TestAtomicWritePreservesEncoding(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "gbk.txt")
	original := fileenc.Encode("中文内容\n", fileenc.GB18030)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	atomicObserve(t, ctx, nil, path)

	if _, err := atomicCall(t, w, ctx, map[string]any{"path": "gbk.txt", "mode": "replace", "content": "新内容\n"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := fileenc.Detect(raw)
	if enc != fileenc.GB18030 {
		t.Fatalf("encoding = %v, want GBK — the write re-encoded the file", enc)
	}
	if got := string(fileenc.Decode(raw, enc)); got != "新内容\n" {
		t.Fatalf("decoded = %q", got)
	}
}

// TestAtomicWriteOverlayRouteWritesTheBufferNotDisk is the same contract
// edit_file has: content read from the host's unsaved buffer must go back there.
func TestAtomicWriteOverlayRouteWritesTheBufferNotDisk(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("saved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay := &fakeOverlay{files: map[string]string{path: "unsaved\n"}, writes: map[string]string{}}
	w := atomicTestWriter(dir, overlay)
	atomicObserve(t, ctx, overlay, path)

	if _, err := atomicCall(t, w, ctx, map[string]any{
		"path": "a.go", "mode": "patch",
		"edits": []map[string]string{{"old": "unsaved", "new": "agent"}},
	}); err != nil {
		t.Fatalf("overlay patch: %v", err)
	}
	if got := overlay.writes[path]; got != "agent\n" {
		t.Fatalf("overlay write = %q, want the buffer edited", got)
	}
	if b, _ := os.ReadFile(path); string(b) != "saved\n" {
		t.Fatalf("disk = %q, want it untouched — the host owns persisting the buffer", b)
	}
}

// TestAtomicWriteSafetyParityWithWriteFile is §6.2 case 9: every refusal
// write_file produces must be produced here too, from the same sequence.
func TestAtomicWriteSafetyParityWithWriteFile(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	w := atomicTestWriter(dir, nil)

	// Outside the write roots.
	_, err := atomicCall(t, w, ctx, map[string]any{"path": outside, "mode": "create", "content": "x"})
	if err == nil || !strings.Contains(err.Error(), "outside the writable roots") {
		t.Fatalf("outside-roots error = %v", err)
	}

	// The same target through write_file, for a byte-comparable refusal.
	wf := writeFile{workDir: dir, roots: realRoots([]string{dir})}
	raw, _ := json.Marshal(map[string]string{"path": outside, "content": "x"})
	_, wfErr := wf.Execute(ctx, raw)
	if wfErr == nil {
		t.Fatal("write_file accepted the same out-of-roots target; the fixtures disagree")
	}
	if !strings.Contains(wfErr.Error(), "outside the writable roots") {
		t.Fatalf("write_file error = %v, want the same shape", wfErr)
	}

	// Session data is denied even when a root covers it.
	stateRoot := t.TempDir()
	guard := NewSessionDataGuard(stateRoot, nil)
	guarded := atomicWrite{workDir: stateRoot, roots: realRoots([]string{stateRoot}), guard: guard}
	sessionFile := filepath.Join(stateRoot, "sessions", "s.json")
	_, err = atomicCall(t, guarded, ctx, map[string]any{"path": sessionFile, "mode": "create", "content": "x"})
	if err == nil || !strings.Contains(err.Error(), "session/state data") {
		t.Fatalf("session-data error = %v", err)
	}
	if _, statErr := os.Stat(sessionFile); !os.IsNotExist(statErr) {
		t.Fatalf("a refused session-data write created the file: %v", statErr)
	}
}

// TestAtomicWriteRefusalsLeaveBytesUntouched is §6.1's invariant 5 across every
// mode: a rejected call must be a no-op on disk.
func TestAtomicWriteRefusalsLeaveBytesUntouched(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	path := filepath.Join(dir, "a.txt")
	seed := "seed\n"
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	w := atomicTestWriter(dir, nil)

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "replace without a read", args: map[string]any{"path": "a.txt", "mode": "replace", "content": "x\n"}},
		{name: "patch without a read", args: map[string]any{"path": "a.txt", "mode": "patch", "edits": []map[string]string{{"old": "seed", "new": "x"}}}},
		{name: "patch with no edits", args: map[string]any{"path": "a.txt", "mode": "patch"}},
		{name: "patch with both edits and range", args: map[string]any{"path": "a.txt", "mode": "patch", "content": "x", "range": map[string]int{"start": 1, "end": 1}, "edits": []map[string]string{{"old": "seed", "new": "x"}}}},
		{name: "create over an existing file", args: map[string]any{"path": "a.txt", "mode": "create", "content": "x\n"}},
		{name: "unknown mode", args: map[string]any{"path": "a.txt", "mode": "rewrite", "content": "x\n"}},
		{name: "append with no content", args: map[string]any{"path": "a.txt", "mode": "append"}},
		{name: "missing path", args: map[string]any{"mode": "replace", "content": "x\n"}},
		{name: "missing mode", args: map[string]any{"path": "a.txt", "content": "x\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := atomicCall(t, w, ctx, tc.args); err == nil {
				t.Fatal("call succeeded, want a refusal")
			}
			if b, _ := os.ReadFile(path); string(b) != seed {
				t.Fatalf("disk = %q, want the seed unchanged", b)
			}
		})
	}
}

// atomicSymbolGoFixture is the symbol fixture the write-path cases share: one
// method and one top-level func with the SAME short name, which is what makes the
// ambiguity rule and the qualified-name rule observable on a real file.
const atomicSymbolGoFixture = `package p

type Server struct{}

func (s *Server) Target() int {
	return 1
}

func Target() int {
	return 2
}
`

// TestAtomicWriteSymbolPatchNeedsNoWindowRead is §6.1 case 1: the model already
// knows the symbol, so one call locates, replaces and reports — and the receipt
// never carries the old body or the new content back into the context.
func TestAtomicWriteSymbolPatchNeedsNoWindowRead(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "a.go")
	unique := "package p\n\nfunc Other() int {\n\treturn 0\n}\n\nfunc Target() int {\n\treturn 1\n}\n"
	if err := os.WriteFile(path, []byte(unique), 0o644); err != nil {
		t.Fatal(err)
	}

	// No atomic_read and no atomicObserve here: the symbol locator carries its
	// own anchor. If this ever starts requiring a prior observation, it fails.
	out, err := atomicCall(t, w, ctx, map[string]any{
		"path": "a.go", "mode": "patch", "symbol": "Target",
		"content": "func Target() int {\n\treturn 2\n}",
	})
	if err != nil {
		t.Fatalf("symbol patch without a prior read: %v", err)
	}
	want := "package p\n\nfunc Other() int {\n\treturn 0\n}\n\nfunc Target() int {\n\treturn 2\n}\n"
	if b, _ := os.ReadFile(path); string(b) != want {
		t.Fatalf("disk = %q, want only the Target span replaced", b)
	}
	if !strings.Contains(out, "symbol Target 7-9") {
		t.Fatalf("receipt = %q, want the resolved symbol and span", out)
	}
	if strings.Contains(out, "return 1") || strings.Contains(out, "return 2") {
		t.Fatalf("the receipt echoed the replaced body or the new content: %q", out)
	}
	if len(out) > atomicReceiptBudgetBytes {
		t.Fatalf("symbol receipt is %dB, over the bounded budget", len(out))
	}
}

func TestAtomicWriteSymbolPatchRefusesAmbiguousShortName(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte(atomicSymbolGoFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := atomicCall(t, w, ctx, map[string]any{
		"path": "a.go", "mode": "patch", "symbol": "Target",
		"content": "func Target() int {\n\treturn 9\n}",
	})
	assertOperationCode(t, err, tool.WriteTargetAmbiguous)
	for _, want := range []string{"5→method Server.Target", "9→func Target"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the rejection must list %q as a candidate, got: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "return 1") || strings.Contains(err.Error(), "return 2") {
		t.Fatalf("the ambiguity rejection echoed a symbol body: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != atomicSymbolGoFixture {
		t.Fatalf("a refused symbol patch changed the file: %q", b)
	}
}

func TestAtomicWriteSymbolPatchQualifiedNameTouchesOnlyTheMethod(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte(atomicSymbolGoFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := atomicCall(t, w, ctx, map[string]any{
		"path": "a.go", "mode": "patch", "symbol": "Server.Target",
		"content": "func (s *Server) Target() int {\n\treturn 11\n}",
	})
	if err != nil {
		t.Fatalf("qualified symbol patch: %v", err)
	}
	got, _ := os.ReadFile(path)
	// The span runs to the line before the NEXT symbol, so the blank separator
	// line belongs to it — the replacement content decides the spacing. What must
	// not change is the other symbol's bytes.
	if !strings.Contains(string(got), "return 11") || strings.Contains(string(got), "return 1\n") {
		t.Fatalf("the method span was not the one replaced: %q", got)
	}
	if !strings.HasSuffix(string(got), "func Target() int {\n\treturn 2\n}\n") {
		t.Fatalf("the top-level func's bytes changed: %q", got)
	}
	if !strings.Contains(out, "symbol Server.Target 5-8") {
		t.Fatalf("receipt = %q", out)
	}
}

func TestAtomicWriteSymbolPatchRefusesMixedLocatorsAndEmptyContent(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte(atomicSymbolGoFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "symbol + edits", args: map[string]any{
			"path": "a.go", "mode": "patch", "symbol": "Target", "content": "x",
			"edits": []map[string]string{{"old": "return 2", "new": "return 3"}},
		}},
		{name: "symbol + range", args: map[string]any{
			"path": "a.go", "mode": "patch", "symbol": "Target", "content": "x",
			"range": map[string]int{"start": 9, "end": 11},
		}},
		{name: "symbol with empty content", args: map[string]any{
			"path": "a.go", "mode": "patch", "symbol": "Target", "content": "",
		}},
		{name: "symbol with whitespace content", args: map[string]any{
			"path": "a.go", "mode": "patch", "symbol": "Target", "content": "  \n",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := atomicCall(t, w, ctx, tc.args); err == nil {
				t.Fatal("call succeeded, want a refusal")
			}
			if b, _ := os.ReadFile(path); string(b) != atomicSymbolGoFixture {
				t.Fatalf("a refusal changed the file: %q", b)
			}
		})
	}
}

func TestAtomicWriteSymbolPatchRefusesAFileWithNoSymbolTable(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "notes.txt")
	// The indented block is the shape the read path degrades to; a write must not
	// cut a file along it.
	prose := "Some prose.\n\n    func not_really_a_symbol() {\n        // indented look-alike\n    }\n"
	if err := os.WriteFile(path, []byte(prose), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := atomicCall(t, w, ctx, map[string]any{
		"path": "notes.txt", "mode": "patch", "symbol": "not_really_a_symbol", "content": "x",
	})
	assertOperationCode(t, err, tool.FSNotObserved)
	if !strings.Contains(err.Error(), "no writable symbol map") {
		t.Fatalf("recovery = %v, want it to name the missing symbol map", err)
	}
	if b, _ := os.ReadFile(path); string(b) != prose {
		t.Fatalf("a refused symbol patch changed the file: %q", b)
	}
}

func TestAtomicWriteSymbolPatchMarkdownSectionLeavesTheNextOneAlone(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "doc.md")
	original := "# Title\n\nintro\n\n## One\n\nfirst body\n\n## Two\n\nsecond body\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := atomicCall(t, w, ctx, map[string]any{
		"path": "doc.md", "mode": "patch", "symbol": "One",
		"content": "## One\n\nfirst body, revised",
	}); err != nil {
		t.Fatalf("markdown symbol patch: %v", err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "first body, revised") {
		t.Fatalf("the replaced section is missing: %q", b)
	}
	if !strings.HasSuffix(string(b), "## Two\n\nsecond body\n") || !strings.Contains(string(b), "# Title\n\nintro\n") {
		t.Fatalf("bytes outside the section changed: %q", b)
	}
}

func TestAtomicWriteSymbolPatchRefusesAnOversizedSpan(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "big.go")

	var b strings.Builder
	b.WriteString("package p\n\nfunc Big() {\n")
	for i := 0; i < atomicAutoWholeMaxLines+20; i++ {
		b.WriteString("\t_ = 1\n")
	}
	b.WriteString("}\n")
	original := b.String()
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := atomicCall(t, w, ctx, map[string]any{
		"path": "big.go", "mode": "patch", "symbol": "Big", "content": "func Big() {}",
	})
	assertOperationCode(t, err, tool.FSTooLarge)
	if b, _ := os.ReadFile(path); string(b) != original {
		t.Fatalf("a refused oversized symbol patch changed the file")
	}
}

func TestAtomicWriteSymbolPatchHonoursSince(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte(atomicSymbolGoFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	anchor := atomicObserve(t, ctx, nil, path)

	// A teammate moves the file on after the read the caller cites.
	teammate := "package p\n\nfunc Target() int {\n\treturn 3\n}\n"
	if err := os.WriteFile(path, []byte(teammate), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := atomicCall(t, w, ctx, map[string]any{
		"path": "a.go", "mode": "patch", "symbol": "Target", "since": anchor.ReadID,
		"content": "func Target() int {\n\treturn 9\n}",
	})
	assertOperationCode(t, err, tool.FSStaleVersion)
	if b, _ := os.ReadFile(path); string(b) != teammate {
		t.Fatalf("a stale symbol patch still wrote: %q", b)
	}
}

func TestAtomicWriteSymbolExemptionDoesNotReachEditsOrRange(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte(atomicSymbolGoFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	// Regression: only the symbol locator may skip the read-before-write rule.
	_, err := atomicCall(t, w, ctx, map[string]any{
		"path": "a.go", "mode": "patch",
		"edits": []map[string]string{{"old": "return 2", "new": "return 3"}},
	})
	assertOperationCode(t, err, tool.FSNotObserved)
	_, err = atomicCall(t, w, ctx, map[string]any{
		"path": "a.go", "mode": "patch", "range": map[string]int{"start": 9, "end": 9}, "content": "func Target() int {",
	})
	assertOperationCode(t, err, tool.FSNotObserved)
	if b, _ := os.ReadFile(path); string(b) != atomicSymbolGoFixture {
		t.Fatalf("an unobserved edits/range patch changed the file: %q", b)
	}

	// And the symbol locator on the same untouched target still works, so the
	// exemption is scoped rather than a general read-before-write bypass.
	if _, err := atomicCall(t, w, ctx, map[string]any{
		"path": "a.go", "mode": "patch", "symbol": "Server.Target",
		"content": "func (s *Server) Target() int {\n\treturn 1\n}",
	}); err != nil {
		t.Fatalf("symbol patch after the refusals: %v", err)
	}
}

func TestAtomicWriteSymbolPatchResolvesFromTheHostBuffer(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("disk line one\ndisk line two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buffered := "package p\n\nfunc Target() int {\n\treturn 1\n}\n"
	overlay := &fakeOverlay{files: map[string]string{path: buffered}, writes: map[string]string{}}
	w := atomicTestWriter(dir, overlay)

	if _, err := atomicCall(t, w, ctx, map[string]any{
		"path": "a.go", "mode": "patch", "symbol": "Target",
		"content": "func Target() int {\n\treturn 2\n}",
	}); err != nil {
		t.Fatalf("overlay symbol patch: %v", err)
	}
	want := "package p\n\nfunc Target() int {\n\treturn 2\n}\n"
	if got := overlay.writes[path]; got != want {
		t.Fatalf("overlay write = %q, want the buffer spliced", got)
	}
	if b, _ := os.ReadFile(path); string(b) != "disk line one\ndisk line two\n" {
		t.Fatalf("disk = %q, want it untouched — the host owns persisting the buffer", b)
	}
}

func TestAtomicWriteSymbolPreviewShowsTheSameSplice(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte(atomicSymbolGoFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	// The approval card must describe exactly the bytes Execute would publish,
	// including on the symbol locator's unread path.
	change, err := w.Preview(ctx, argsJSON(t, map[string]any{
		"path": "a.go", "mode": "patch", "symbol": "Server.Target",
		"content": "func (s *Server) Target() int {\n\treturn 11\n}",
	}))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !strings.Contains(change.NewText, "return 11") || !strings.Contains(change.NewText, "func Target() int {\n\treturn 2") {
		t.Fatalf("preview new text = %q", change.NewText)
	}
	if change.OldText != atomicSymbolGoFixture {
		t.Fatalf("preview old text = %q", change.OldText)
	}
}

func TestAtomicWriteOpsSymbolFailureLeavesEveryFileAlone(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	a := "package p\n\nfunc Target() int {\n\treturn 1\n}\n"
	bContent := "package p\n\nfunc Other() int {\n\treturn 2\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(a), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte(bContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Two symbol patches in one transaction, no prior read for either; the second
	// names a symbol that does not exist, so prepare must abort the whole plan.
	_, err := atomicCall(t, w, ctx, map[string]any{"ops": []map[string]any{
		{"path": "a.go", "mode": "patch", "symbol": "Target", "content": "func Target() int {\n\treturn 11\n}"},
		{"path": "b.go", "mode": "patch", "symbol": "Nowhere", "content": "func Nowhere() {}"},
	}})
	assertOperationCode(t, err, tool.FSNotObserved)
	if got, _ := os.ReadFile(filepath.Join(dir, "a.go")); string(got) != a {
		t.Fatalf("a.go changed despite the failed transaction: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "b.go")); string(got) != bContent {
		t.Fatalf("b.go changed despite the failed transaction: %q", got)
	}
}

func assertOperationCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want an operation error with code %s, got nil", code)
	}
	var opErr *tool.OperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("error %v is not an OperationError", err)
	}
	if opErr.Diagnostic.Code != code {
		t.Fatalf("code = %s, want %s (%v)", opErr.Diagnostic.Code, code, err)
	}
	if strings.TrimSpace(opErr.Diagnostic.Recovery) == "" {
		t.Fatalf("code %s carries no recovery text", code)
	}
}

// TestAtomicWriteReceiptNeverReturnsTheFile pins the tool's central promise: the
// provider-visible result names what changed and never carries the content, so a
// successful write costs the same as a failed one.
func TestAtomicWriteReceiptNeverReturnsTheFile(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "big.txt")
	secret := strings.Repeat("SECRET-PAYLOAD ", 400) + "UNIQUE-TAIL\n"
	if err := os.WriteFile(path, []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	atomicObserve(t, ctx, nil, path)

	out, err := atomicCall(t, w, ctx, map[string]any{
		"path": "big.txt", "mode": "patch",
		"edits": []map[string]string{{"old": "UNIQUE-TAIL", "new": "redacted"}},
	})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if strings.Contains(out, "SECRET-PAYLOAD") {
		t.Fatalf("the receipt echoed file content:\n%s", out)
	}
	if len(out) > maxPostWriteReceiptBytes+atomicReceiptBudgetBytes {
		t.Fatalf("receipt is unbounded: %d bytes", len(out))
	}
}

// TestAtomicWriteArgumentGateAdmitsTheTransactionForm pins the one rule the
// provider schema cannot state. The frozen schema requires `path`, but an
// ops-only call has no single path, so argument validation compiles a contract
// variant and the tool enforces "path or ops" itself. A call naming neither must
// still be refused before any hook, lease or execute step.
func TestAtomicWriteArgumentGateAdmitsTheTransactionForm(t *testing.T) {
	w := atomicWrite{}
	for _, tc := range []struct {
		name string
		args string
		want int
	}{
		{name: "ops only", args: `{"ops":[{"path":"a.txt","mode":"replace","content":"x"}]}`, want: 0},
		{name: "path only", args: `{"path":"a.txt","mode":"replace","content":"x"}`, want: 0},
		{name: "both", args: `{"path":"a.txt","ops":[{"path":"b.txt","mode":"replace","content":"x"}]}`, want: 0},
		{name: "neither", args: `{"mode":"replace","content":"x"}`, want: 1},
		{name: "empty ops", args: `{"ops":[]}`, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := tool.ValidateArguments(w, json.RawMessage(tc.args))
			if result.CompileErr != nil {
				t.Fatalf("contract schema did not compile: %v", result.CompileErr)
			}
			if len(result.Violations) != tc.want {
				t.Fatalf("violations = %v, want %d", result.Violations, tc.want)
			}
			if tc.want > 0 && strings.TrimSpace(result.Violations[0].Expected) == "" {
				t.Fatal("a violation must state what the caller should supply")
			}
		})
	}
	// The provider-visible schema must stay exactly as the route froze it: the
	// variant is host-side only.
	if got := string(w.Schema()); !strings.Contains(got, `"required":["path"]`) {
		t.Fatalf("provider schema drifted from the frozen contract: %s", got)
	}
	// The variant keeps the nested required lists (edits/range) and drops only
	// the root one, so "path or ops" is decided by ValidateArguments.
	var variant map[string]any
	if err := json.Unmarshal(w.ArgumentContractSchema(), &variant); err != nil {
		t.Fatalf("the contract variant is not valid JSON: %v", err)
	}
	if _, stillRequired := variant["required"]; stillRequired {
		t.Fatalf("the contract variant must drop the root required list: %s", w.ArgumentContractSchema())
	}
}
