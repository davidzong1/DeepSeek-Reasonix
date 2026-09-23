package builtin

import (
	"fmt"
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
