package builtin

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"reasonix/internal/tool"
)

// The symbol locator for mode=patch: when a model already knows which outline
// symbol it wants to change, the host resolves that symbol's span on the bytes
// this same call read, so no numbered window has to travel back to the model
// first. It is the only patch locator that does not require an earlier
// atomic_read.
//
// Nothing here does I/O. The span is a pure function of (path, content), so the
// bytes the splice is cut from are the bytes the caller read, and the matching
// rules can be exercised without a filesystem.

// atomicSymbolCandidateMax bounds the candidate list a non-unique-symbol
// rejection carries. The list is there to let the model disambiguate on its next
// call; it is not a second copy of the outline, so it stays short and never
// carries a function body.
const atomicSymbolCandidateMax = 8

// atomicWriteSymbols is the outline's collector set WITHOUT its indent
// degradation. A read may honestly say "no symbols, here is the head"; a write
// must not, because a wrong span would replace a block the caller never named.
// So a file with no real symbol table yields nothing here and the patch is
// refused rather than cut along an indentation guess.
func atomicWriteSymbols(path, content string) []codeSymbol {
	ext := filepath.Ext(path)
	switch ext {
	case ".md", ".markdown":
		if symbols := atomicMarkdownSymbols(content); len(symbols) > 0 {
			return symbols
		}
	case ".go":
		if symbols := atomicGoSymbols(path, content); len(symbols) > 0 {
			return symbols
		}
	}
	return atomicMatcherSymbols(path, ext, content)
}

// atomicSymbolDisplayName is the name a model both reads in the outline and
// writes back as `symbol`: a method is "Parent.Name", everything else its own
// name. The kind is deliberately excluded — it labels the map, it does not name
// the symbol.
func atomicSymbolDisplayName(symbol codeSymbol) string {
	if symbol.Parent != "" {
		return symbol.Parent + "." + symbol.Name
	}
	return symbol.Name
}

// atomicSymbolCandidates renders the disambiguation list for a name that matched
// more than one symbol. Entries look exactly like outline lines (which is where
// the model got the name), and the list is bounded.
func atomicSymbolCandidates(hits []codeSymbol) string {
	var b strings.Builder
	kept := min(len(hits), atomicSymbolCandidateMax)
	for _, hit := range hits[:kept] {
		fmt.Fprintf(&b, "%d→%s\n", hit.Line, atomicOutlineLabel(hit))
	}
	if remaining := len(hits) - kept; remaining > 0 {
		fmt.Fprintf(&b, "…[%d more candidate(s) withheld]…\n", remaining)
	}
	return strings.TrimRight(b.String(), "\n")
}

// atomicResolveSymbolSpan turns one outline name into the 1-based inclusive line
// span a patch will replace, plus the display name to report in the receipt.
//
// The span ends where the NEXT symbol begins, minus one line. For a Go top-level
// func that is exactly the function; for a Markdown heading it is the whole
// section, which is why the span is also capped (§3.3): a heading that swallows
// half the document is refused rather than written.
func atomicResolveSymbolSpan(content, path, symbol string) (start, end int, label string, err error) {
	name := strings.TrimSpace(symbol)
	if name == "" {
		return 0, 0, "", fmt.Errorf("symbol is required for a symbol patch")
	}
	symbols := atomicWriteSymbols(path, content)
	if len(symbols) == 0 {
		return 0, 0, "", &tool.OperationError{
			Diagnostic: tool.OperationDiagnostic{
				Code:     tool.FSNotObserved,
				Path:     path,
				Recovery: "this file has no writable symbol map; read a window with atomic_read, then patch with range or edits",
			},
			Cause: fmt.Errorf("no outline symbols in %s", path),
		}
	}
	// A symbol answers both to the bare name it is declared under and to the
	// qualified name the outline prints, so `Target` reaches the method too — and
	// then loses to the ambiguity rule rather than silently picking the function.
	var hits []codeSymbol
	for _, candidate := range symbols {
		if candidate.Name == name || atomicSymbolDisplayName(candidate) == name {
			hits = append(hits, candidate)
		}
	}
	if len(hits) == 0 {
		return 0, 0, "", &tool.OperationError{
			Diagnostic: tool.OperationDiagnostic{
				Code:     tool.FSNotObserved,
				Path:     path,
				Recovery: "read the symbol map with atomic_read mode=outline, then retry, or patch with range or edits",
			},
			Cause: fmt.Errorf("symbol %q is not one of %s's %d writable symbol(s)", name, path, len(symbols)),
		}
	}
	if len(hits) > 1 {
		// A short name that resolves to several symbols is refused even when the
		// caller could have meant one: picking silently is how the wrong function
		// gets replaced.
		return 0, 0, "", &tool.OperationError{
			Diagnostic: tool.OperationDiagnostic{
				Code:     tool.WriteTargetAmbiguous,
				Path:     path,
				Recovery: "name the method as Type.Name, or read a window with atomic_read and patch with range",
			},
			Cause: fmt.Errorf("symbol %q is not unique in %s (%d matches):\n%s", name, path, len(hits), atomicSymbolCandidates(hits)),
		}
	}

	sorted := append([]codeSymbol(nil), symbols...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Line < sorted[j].Line })
	hit := hits[0]
	lines := atomicSplitLines(content)
	start, label = hit.Line, atomicSymbolDisplayName(hit)
	if start < 1 || start > len(lines) {
		return 0, 0, "", fmt.Errorf("symbol %q was reported at line %d of a %d-line file", label, start, len(lines))
	}
	end = len(lines)
	for _, candidate := range sorted {
		if candidate.Line > start {
			end = candidate.Line - 1
			break
		}
	}
	// A line carrying more than one symbol (a `var a, b = ...` spec) cannot give
	// either one a span that does not cross the other, so the span collapses to
	// that line instead of growing on to the next distinct symbol. The same clamp
	// covers the next symbol landing on this line, which would otherwise produce an
	// end before the start.
	if end < start || atomicSymbolsOnLine(sorted, start) > 1 {
		end = start
	}
	spanLines := end - start + 1
	spanBytes := len(strings.Join(lines[start-1:end], "\n"))
	if spanLines > atomicAutoWholeMaxLines || spanBytes > atomicReadBudgetBytes {
		return 0, 0, "", &tool.OperationError{
			Diagnostic: tool.OperationDiagnostic{
				Code:     tool.FSTooLarge,
				Path:     path,
				Recovery: "read a window with atomic_read and patch with range, or use edits:[{old,new}]",
			},
			Cause: fmt.Errorf("symbol %q spans %d line(s) / %d bytes, over the %d-line / %d-byte symbol-patch cap",
				label, spanLines, spanBytes, atomicAutoWholeMaxLines, atomicReadBudgetBytes),
		}
	}
	return start, end, label, nil
}

// atomicSymbolsOnLine counts the symbols reported at one line, which is what the
// same-line collapse rule is decided on.
func atomicSymbolsOnLine(symbols []codeSymbol, line int) int {
	count := 0
	for _, symbol := range symbols {
		if symbol.Line == line {
			count++
		}
	}
	return count
}

// atomicSymbolSpanLabel renders the receipt fragment that names the resolved
// symbol and the span it replaced, so a caller can check what it hit without the
// old body being echoed back.
func atomicSymbolSpanLabel(label string, start, end int) string {
	return fmt.Sprintf("symbol %s %d-%d", label, start, end)
}
