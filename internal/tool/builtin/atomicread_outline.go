package builtin

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	fileenc "reasonix/internal/fileutil/encoding"
	"reasonix/internal/tool"
)

// The non-window modes: the symbol map, the since-last-read delta, and the tail.
// All three are a bounded body under a header carrying the read id.

// atomicReadPlan is one rendered result: the numbered lines to deliver, where
// they start, and whether anything was left over.
type atomicReadPlan struct {
	body      string
	firstLine int
	delivered int
	hasMore   bool
	// section is the mode-specific label that appears in the header.
	section string
	// nextOffset is the offset that continues this window, or -1 when the mode
	// has no continuation. It becomes the reader's own paging trailer, which is
	// what the host parses back out to prove what was delivered.
	nextOffset int
	// degraded marks an outline that had no symbols to map and fell back to a
	// head window. auto must not stack a second head on top of it.
	degraded bool
}

// plan decides what this call delivers. mode=auto needs the file's size before
// it can choose between a whole read and an outline, so the size comes from the
// anchor rather than a second stat.
func (r atomicRead) plan(ctx context.Context, p atomicReadParams, rp ResolvedPath, anchor atomicAnchor, content string) (atomicReadPlan, error) {
	switch p.Mode {
	case atomicModeWindow:
		return atomicWindowPlan(p, content), nil
	case atomicModeTail:
		return atomicTailPlan(p, content), nil
	case atomicModeOutline:
		return atomicOutlinePlan(rp, content), nil
	case atomicModeDelta:
		return r.deltaPlan(ctx, rp, anchor, p, content)
	default:
		return r.autoPlan(rp, anchor, content), nil
	}
}

// autoPlan is the default shape: a small file is cheap enough to return whole,
// and a large one gets its map plus a head window, which is what a member
// almost always wants before deciding where to work.
func (r atomicRead) autoPlan(rp ResolvedPath, anchor atomicAnchor, content string) atomicReadPlan {
	if anchor.Lines <= atomicAutoWholeMaxLines {
		plan := atomicWindowPlan(atomicReadParams{Offset: 0, Limit: atomicWindowDefaultLimit}, content)
		plan.section = fmt.Sprintf("auto→window 1-%d/%d", plan.delivered, anchor.Lines)
		return plan
	}
	outline := atomicOutlinePlan(rp, content)
	if outline.degraded {
		// A file with no symbols already fell back to a head window; stacking a
		// second head on top would just repeat it.
		outline.section = strings.Replace(outline.section, "outline (no symbols)", "auto→head (no symbols)", 1)
		return outline
	}
	// The head shares the result budget with the outline that precedes it.
	remaining := atomicReadBudgetBytes - atomicHeaderReserveBytes - len(outline.body) - 2
	head := atomicWindowPlanWithin(atomicReadParams{Offset: 0, Limit: atomicAutoHeadLines}, content, max(remaining, 0))
	outline.section = fmt.Sprintf("auto→outline+head 1-%d/%d", head.delivered, anchor.Lines)
	outline.body = strings.TrimRight(outline.body+"\n\n"+head.body, "\n")
	outline.nextOffset = head.nextOffset
	// The head is a preview of the top of the file, not the whole of it, so the
	// result is never complete even when the outline happened to fit.
	outline.hasMore = true
	return outline
}

// atomicWindowPlan renders numbered lines offset/limit, clipped to the read
// budget. Clipping happens on whole lines: half a line is not a line the model
// can cite, and the host's window parser would reject it anyway.
func atomicWindowPlan(p atomicReadParams, content string) atomicReadPlan {
	return atomicWindowPlanWithin(p, content, atomicReadBudgetBytes-atomicHeaderReserveBytes)
}

// atomicWindowPlanWithin is atomicWindowPlan with an explicit byte ceiling. auto
// uses it so an outline plus a head still fits inside one result: the outline
// spends part of the budget, and the head must be sized against what is left
// rather than against the whole of it.
func atomicWindowPlanWithin(p atomicReadParams, content string, ceiling int) atomicReadPlan {
	lines := atomicSplitLines(content)
	if len(lines) == 0 {
		return atomicReadPlan{body: "(empty file)", section: "window 0-0/0", nextOffset: -1}
	}
	if p.Offset >= len(lines) {
		return atomicReadPlan{
			body:       fmt.Sprintf("(offset %d is past EOF — file has %d lines)", p.Offset, len(lines)),
			firstLine:  len(lines) + 1,
			section:    fmt.Sprintf("window %d-%d/%d", p.Offset+1, p.Offset+1, len(lines)),
			nextOffset: -1,
		}
	}
	limit := p.Limit
	if limit <= 0 {
		limit = atomicWindowDefaultLimit
	}
	end := min(p.Offset+limit, len(lines))
	width := len(strconv.Itoa(end))
	var b strings.Builder
	used, delivered := 0, 0
	for i := p.Offset; i < end; i++ {
		numbered := fmt.Sprintf("%*d→%s\n", width, i+1, lines[i])
		if used+len(numbered) > ceiling {
			break
		}
		b.WriteString(numbered)
		used += len(numbered)
		delivered++
	}
	plan := atomicReadPlan{
		body:       strings.TrimRight(b.String(), "\n"),
		firstLine:  p.Offset + 1,
		delivered:  delivered,
		hasMore:    p.Offset+delivered < len(lines),
		nextOffset: -1,
	}
	last := plan.firstLine + max(delivered-1, 0)
	plan.section = fmt.Sprintf("window %d-%d/%d", plan.firstLine, last, len(lines))
	if plan.hasMore {
		plan.nextOffset = p.Offset + delivered
		plan.section += fmt.Sprintf(" (%d more; pass offset=%d)", len(lines)-plan.nextOffset, plan.nextOffset)
	}
	return plan
}

// atomicTailPlan renders the last `limit` lines. Log files and long command
// outputs are the reason this exists: the end is where the news is.
func atomicTailPlan(p atomicReadParams, content string) atomicReadPlan {
	lines := atomicSplitLines(content)
	if len(lines) == 0 {
		return atomicReadPlan{body: "(empty file)", section: "tail 0-0/0", nextOffset: -1}
	}
	limit := p.Limit
	if !p.WindowGiven || limit == atomicWindowDefaultLimit {
		limit = atomicTailDefaultLines
	}
	start := max(len(lines)-limit, 0)
	plan := atomicWindowPlan(atomicReadParams{Offset: start, Limit: len(lines) - start}, content)
	plan.section = fmt.Sprintf("tail %d-%d/%d", plan.firstLine, len(lines), len(lines))
	plan.hasMore = false
	plan.nextOffset = -1
	return plan
}

// atomicOutlinePlan renders the symbol/section map. It reuses code_index's
// collectors, and when a file yields no symbols at all it degrades to a
// recognizable head plus the line count rather than pretending the file is
// empty.
func atomicOutlinePlan(rp ResolvedPath, content string) atomicReadPlan {
	symbols := atomicOutlineSymbols(rp.Path, content)
	lines := atomicSplitLines(content)
	if len(symbols) == 0 {
		head := atomicWindowPlan(atomicReadParams{Offset: 0, Limit: min(atomicOutlineFallbackLines, len(lines))}, content)
		head.section = fmt.Sprintf("outline (no symbols) 1-%d/%d", head.delivered, len(lines))
		head.hasMore = len(lines) > head.delivered
		head.degraded = true
		return head
	}
	var b strings.Builder
	used, kept := 0, 0
	for _, symbol := range symbols {
		entry := fmt.Sprintf("%d→%s\n", symbol.Line, atomicOutlineLabel(symbol))
		if used+len(entry) > atomicOutlineBudgetBytes {
			break
		}
		b.WriteString(entry)
		used += len(entry)
		kept++
	}
	plan := atomicReadPlan{
		body:       strings.TrimRight(b.String(), "\n"),
		firstLine:  1,
		section:    fmt.Sprintf("outline %d symbols/%d lines", kept, len(lines)),
		hasMore:    kept < len(symbols),
		nextOffset: -1,
	}
	if plan.hasMore {
		plan.section = fmt.Sprintf("outline %d of %d symbols/%d lines", kept, len(symbols), len(lines))
	}
	return plan
}

// deltaPlan answers "what changed since the read id I already have". It needs a
// `since`: without one there is nothing to diff against, and guessing (say, the
// last read of any file) would report changes the model never saw.
func (r atomicRead) deltaPlan(ctx context.Context, rp ResolvedPath, anchor atomicAnchor, p atomicReadParams, content string) (atomicReadPlan, error) {
	if p.Since == "" {
		return atomicReadPlan{}, &tool.OperationError{
			Diagnostic: tool.OperationDiagnostic{
				Code:     tool.FSNotObserved,
				Path:     rp.DisplayPath,
				Recovery: "mode=delta needs since=<read_id from an earlier atomic_read header>; read the file first, or use mode=window",
			},
			Cause: fmt.Errorf("mode=delta requires since"),
		}
	}
	previous, err := atomicAnchorFor(ctx, r.overlay, rp.Path, p.Since)
	if err != nil {
		return atomicReadPlan{}, err
	}
	before, ok := atomicCachedContent(previous.ReadID)
	if !ok {
		return atomicReadPlan{}, &tool.OperationError{
			Diagnostic: tool.OperationDiagnostic{
				Code:             tool.FSStaleVersion,
				Path:             rp.DisplayPath,
				ExpectedSnapshot: p.Since,
				Recovery:         "that read is no longer available to diff against; read the file again with atomic_read",
			},
			Cause: fmt.Errorf("%w: %s", ErrFileChanged, rp.Path),
		}
	}
	hunks := atomicDeltaHunks(before, []byte(content), atomicDeltaBudgetBytes)
	if len(hunks) == 0 {
		return atomicReadPlan{body: "unchanged", firstLine: 1, nextOffset: -1, section: fmt.Sprintf("delta %s (unchanged)", p.Since)}, nil
	}
	return atomicReadPlan{
		body:       strings.Join(hunks, "\n"),
		firstLine:  1,
		section:    fmt.Sprintf("delta %s→%s", p.Since, anchor.ReadID),
		hasMore:    true,
		nextOffset: -1,
	}, nil
}

// render writes the header and body. The header is the only place a read id is
// ever emitted, and it always states the mode, the delivered window and the
// file's size, so a model can tell a partial view from a complete one without
// guessing.
func (p atomicReadPlan) render(anchor atomicAnchor, rp ResolvedPath) string {
	header := fmt.Sprintf("read %s %s [%s] (%d lines, %s)", anchor.ReadID, rp.DisplayPath, p.section, anchor.Lines, atomicHumanBytes(anchor.Bytes))
	if p.body == "" {
		return header
	}
	body := header + "\n" + p.body
	if p.hasMore && p.nextOffset >= 0 {
		// The trailer is the reader's own paging state; the host parses it back
		// out to prove exactly which lines were delivered.
		body += fmt.Sprintf("\n\n[more lines below; pass offset=%d]\n", p.nextOffset)
	}
	return body
}

// atomicOutlineLabel renders one symbol map entry. The line number is already
// the prefix, so the label stays short.
func atomicOutlineLabel(symbol codeSymbol) string {
	name := symbol.Name
	if symbol.Parent != "" {
		name = symbol.Parent + "." + name
	}
	if symbol.Kind != "" {
		return symbol.Kind + " " + name
	}
	return name
}

// atomicOutlineSymbols collects a file's symbols for the outline. It works from
// the content the read anchored rather than re-reading the path, so the map and
// the bytes it describes always come from the same source — which matters for
// an unsaved editor buffer, where the two differ.
func atomicOutlineSymbols(path, content string) []codeSymbol {
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
	if symbols := atomicMatcherSymbols(path, ext, content); len(symbols) > 0 {
		return symbols
	}
	return atomicIndentSymbols(content)
}

// atomicGoSymbols parses Go source in memory. Parsing the buffer rather than the
// path is deliberate: the outline must describe the bytes the read anchored.
func atomicGoSymbols(path, content string) []codeSymbol {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, content, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	return goSymbols(fset, file, path)
}

// atomicMatcherSymbols runs code_index's per-language line matchers over the
// content, so the outline a member sees here matches what code_index would
// report for the same file.
func atomicMatcherSymbols(path, ext, content string) []codeSymbol {
	matchers := codeIndexMatchers(ext)
	if len(matchers) == 0 {
		return nil
	}
	var out []codeSymbol
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), readFileMaxLineBytes)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		for _, matcher := range matchers {
			if symbol, ok := matcher.match(text); ok {
				symbol.File, symbol.Line = path, line
				out = append(out, symbol)
				break
			}
		}
	}
	return out
}

var (
	reMarkdownHeading = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)
	reIndentBlock     = regexp.MustCompile(`^(\t+| {2,})(?:func|def|class|type|interface|struct|enum|const|var|let|function|public|private|protected|static)\s`)
)

// atomicMarkdownSymbols maps a document's headings. Markdown has no symbols,
// but its heading tree is the section map a reader actually navigates by.
func atomicMarkdownSymbols(content string) []codeSymbol {
	var out []codeSymbol
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), readFileMaxLineBytes)
	line := 0
	for scanner.Scan() {
		line++
		match := reMarkdownHeading.FindStringSubmatch(scanner.Text())
		if match == nil {
			continue
		}
		out = append(out, codeSymbol{
			Name: strings.TrimSpace(match[2]),
			Kind: "h" + strconv.Itoa(len(match[1])),
			Line: line,
		})
	}
	return out
}

// atomicIndentSymbols is the last resort for a language with no matcher: an
// indentation-shaped block. It is deliberately conservative — a wrong entry in
// the map costs more than a missing one, because the model trusts the line
// numbers it is given.
func atomicIndentSymbols(content string) []codeSymbol {
	var out []codeSymbol
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), readFileMaxLineBytes)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if !reIndentBlock.MatchString(text) {
			continue
		}
		out = append(out, codeSymbol{Name: strings.TrimSpace(text), Kind: "block", Line: line})
	}
	return out
}

// atomicLooksBinary reports whether a byte sample looks like a non-text file.
// The encoding checks come first: UTF-16 files carry a NUL for every ASCII
// character, so a naive NUL scan would call every Windows source file binary.
func atomicLooksBinary(peek []byte) bool {
	if len(peek) == 0 {
		return false
	}
	if _, ok := fileenc.DetectUTF16NoBOM(peek); ok {
		return false
	}
	if fileenc.DetectQuick(peek) != fileenc.UTF8 {
		return false
	}
	return bytes.IndexByte(peek, 0) >= 0
}

// atomicHumanBytes renders a byte count the way a header should read: exact
// below a kilobyte, rounded above it.
func atomicHumanBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	}
}
