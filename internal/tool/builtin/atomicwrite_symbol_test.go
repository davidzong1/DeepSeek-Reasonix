package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/tool"
)

// atomicResolveSymbolSpan is a pure function of (path, content), so its matching
// rules are pinned here without a filesystem: the span arithmetic is the part a
// wrong answer silently destroys, and it must be checkable on its own.

// goSymbolFixture has one func, one type, one method and one top-level func:
// enough to exhibit the qualified-name rule and the next-symbol rule.
const goSymbolFixture = `package p

// doc
func Helper() {
}

type Server struct{}

func (s *Server) Target() int {
	return 1
}

func Target() int {
	return 2
}
`

func TestAtomicResolveSymbolSpanUsesTheNextSymbolAsTheEnd(t *testing.T) {
	// Helper starts at 4; the next symbol (type Server, line 7) ends it at 6.
	start, end, label, err := atomicResolveSymbolSpan(goSymbolFixture, "p.go", "Helper")
	if err != nil {
		t.Fatalf("Helper: %v", err)
	}
	if start != 4 || end != 6 || label != "Helper" {
		t.Fatalf("Helper span = %d-%d %q, want 4-6 Helper", start, end, label)
	}
	// Server starts at 7; the method at 9 is the next symbol.
	start, end, label, err = atomicResolveSymbolSpan(goSymbolFixture, "p.go", "Server")
	if err != nil {
		t.Fatalf("Server: %v", err)
	}
	if start != 7 || end != 8 || label != "Server" {
		t.Fatalf("Server span = %d-%d %q, want 7-8 Server", start, end, label)
	}
}

func TestAtomicResolveSymbolSpanQualifiedNamePicksTheMethod(t *testing.T) {
	start, end, label, err := atomicResolveSymbolSpan(goSymbolFixture, "p.go", "Server.Target")
	if err != nil {
		t.Fatalf("Server.Target: %v", err)
	}
	if start != 9 || end != 12 || label != "Server.Target" {
		t.Fatalf("Server.Target span = %d-%d %q, want 9-12 Server.Target", start, end, label)
	}
}

func TestAtomicResolveSymbolSpanShortNameOverTwoSymbolsIsRefused(t *testing.T) {
	// `Target` is the method's own name AND the top-level func's name: two
	// symbols answer, so the caller must qualify — never "pick the one that is
	// alone in the file".
	_, _, _, err := atomicResolveSymbolSpan(goSymbolFixture, "p.go", "Target")
	assertOperationCode(t, err, tool.WriteTargetAmbiguous)
	if !strings.Contains(err.Error(), "not unique") {
		t.Fatalf("ambiguity error = %v", err)
	}
	if !strings.Contains(err.Error(), "9→method Server.Target") || !strings.Contains(err.Error(), "13→func Target") {
		t.Fatalf("ambiguity error must list the candidates as outline lines, got: %v", err)
	}
	// The candidate list must not carry the function body.
	if strings.Contains(err.Error(), "return 1") {
		t.Fatalf("the ambiguity error echoed a symbol's body: %v", err)
	}
}

func TestAtomicResolveSymbolSpanUnknownNamePointsAtTheOutline(t *testing.T) {
	_, _, _, err := atomicResolveSymbolSpan(goSymbolFixture, "p.go", "Nowhere")
	assertOperationCode(t, err, tool.FSNotObserved)
	if !strings.Contains(err.Error(), "mode=outline") {
		t.Fatalf("the zero-hit recovery must route to the outline: %v", err)
	}
}

func TestAtomicResolveSymbolSpanRefusesAFileWithNoSymbolTable(t *testing.T) {
	// Prose has no symbols, and an indented block is not a span a write may cut:
	// the read path may degrade to an indentation guess, the write path must not.
	prose := "Some prose about the work.\n\nMore prose, with an indented look-alike:\n\n    def fake_thing(self):\n        pass\n"
	if _, _, _, err := atomicResolveSymbolSpan(prose, "notes.txt", "fake_thing"); err == nil {
		t.Fatal("a prose file must not resolve a symbol")
	} else {
		assertOperationCode(t, err, tool.FSNotObserved)
		if !strings.Contains(err.Error(), "no writable symbol map") {
			t.Fatalf("recovery = %v, want it to say there is no symbol map", err)
		}
	}
}

func TestAtomicResolveSymbolSpanRefusesAnOversizedSpan(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\n\nfunc Big() {\n")
	for i := 0; i < atomicAutoWholeMaxLines+50; i++ {
		fmt.Fprintf(&b, "\t_ = %d\n", i)
	}
	b.WriteString("}\n")
	_, _, _, err := atomicResolveSymbolSpan(b.String(), "big.go", "Big")
	assertOperationCode(t, err, tool.FSTooLarge)
	if !strings.Contains(err.Error(), "range") {
		t.Fatalf("the size refusal must offer a route: %v", err)
	}

	// Few lines, but past the byte cap: the line cap alone is not the guard.
	var wide strings.Builder
	wide.WriteString("package p\n\nfunc Wide() {\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&wide, "\t_ = %q\n", strings.Repeat("x", 200))
	}
	wide.WriteString("}\n")
	if _, _, _, err := atomicResolveSymbolSpan(wide.String(), "wide.go", "Wide"); err == nil {
		t.Fatal("a span over the byte cap must be refused even when it fits the line cap")
	} else {
		assertOperationCode(t, err, tool.FSTooLarge)
	}
}

func TestAtomicResolveSymbolSpanMarkdownSectionEndsAtTheNextHeading(t *testing.T) {
	doc := "# Title\n\nintro\n\n## One\n\nfirst body\n\n## Two\n\nsecond body\n"
	start, end, label, err := atomicResolveSymbolSpan(doc, "doc.md", "One")
	if err != nil {
		t.Fatalf("One: %v", err)
	}
	if start != 5 || end != 8 || label != "One" {
		t.Fatalf("One span = %d-%d %q, want 5-8", start, end, label)
	}
	if _, _, _, err := atomicResolveSymbolSpan(doc, "doc.md", "Missing"); err == nil {
		t.Fatal("an unknown heading must be refused")
	}
}

func TestAtomicResolveSymbolSpanCollapsesASharedLine(t *testing.T) {
	// `var A, B = ...` is two symbols on one line: neither may get a span that
	// grows past the line and swallows the other's declaration.
	shared := "package p\n\nvar Alpha, Beta = 1, 2\n\nfunc Later() {\n}\n"
	start, end, label, err := atomicResolveSymbolSpan(shared, "p.go", "Alpha")
	if err != nil {
		t.Fatalf("Alpha: %v", err)
	}
	if start != 3 || end != 3 || label != "Alpha" {
		t.Fatalf("Alpha span = %d-%d %q, want the single line 3-3", start, end, label)
	}
	if _, _, _, err := atomicResolveSymbolSpan(shared, "p.go", "Beta"); err != nil {
		t.Fatalf("Beta: %v", err)
	}
	// The unrelated symbol below still gets its own span.
	start, end, _, err = atomicResolveSymbolSpan(shared, "p.go", "Later")
	if err != nil {
		t.Fatalf("Later: %v", err)
	}
	if start != 5 || end != 6 {
		t.Fatalf("Later span = %d-%d, want 5-6", start, end)
	}
}

func TestAtomicResolveSymbolSpanLastSymbolRunsToEOF(t *testing.T) {
	start, end, label, err := atomicResolveSymbolSpan(goSymbolFixture, "p.go", "Target")
	if err == nil {
		t.Fatalf("Target must be ambiguous here, got %d-%d %q", start, end, label)
	}
	// Drop the method so the top-level func is unique and last.
	only := strings.Replace(goSymbolFixture, "func (s *Server) Target() int {\n\treturn 1\n}\n\n", "", 1)
	start, end, label, err = atomicResolveSymbolSpan(only, "p.go", "Target")
	if err != nil {
		t.Fatalf("Target: %v", err)
	}
	if label != "Target" || start != 9 {
		t.Fatalf("Target span = %d-%d %q, want it to start at 9", start, end, label)
	}
	if end != atomicLineCount([]byte(only)) {
		t.Fatalf("end = %d, want the file's last line %d", end, atomicLineCount([]byte(only)))
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
