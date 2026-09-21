package cli

// Deliverable read projections: outline by default, body on request. A body read
// once is carried in every later request prefix — ~356K tokens over two reads in
// a measured leader session.

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// deliverableSummaryHeadings bounds how many section headings an outline
// lists. A document with more sections than this is summarized by its first
// sections plus a count, so the outline itself stays bounded.
const deliverableSummaryHeadings = 40

// deliverableSummaryChars caps the outline's opening excerpt. The excerpt is
// there to identify the document, not to stand in for it.
const deliverableSummaryChars = 400

// deliverablePageDefault is the byte window one paged read returns when the
// caller names no limit.
const deliverablePageDefault = 8 * 1024

// deliverablePageMax bounds one paged read so a caller cannot defeat paging by
// asking for the whole document in a single page.
const deliverablePageMax = 32 * 1024

// deliverableReadArgs is the read tool's argument shape. Mode selects the
// projection; offset and limit page the body and are only meaningful in full
// mode.
type deliverableReadArgs struct {
	ID     string `json:"id"`
	Mode   string `json:"mode"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// deliverableOutline is the default projection: the document's identity, its
// size, its section headings, and a short opening excerpt. It answers "what is
// this document and is it worth reading whole" without paying for the body.
func deliverableOutline(id string, body []byte) string {
	text := string(body)
	lines := strings.Split(text, "\n")
	var b strings.Builder
	fmt.Fprintf(&b, "deliverable %s — outline only (%d bytes, %d lines)\n", id, len(body), len(lines))
	fmt.Fprintf(&b, "Read it whole with mode=\"full\" (page it with offset/limit) when the body matters.\n")

	headings := outlineHeadings(lines)
	if len(headings) == 0 {
		b.WriteString("no markdown headings; the opening excerpt below is the whole structure\n")
	} else {
		fmt.Fprintf(&b, "\nsections (%d):\n", len(headings))
		shown := headings
		if len(shown) > deliverableSummaryHeadings {
			shown = shown[:deliverableSummaryHeadings]
		}
		for _, h := range shown {
			b.WriteString(h)
			b.WriteByte('\n')
		}
		if rest := len(headings) - len(shown); rest > 0 {
			fmt.Fprintf(&b, "… and %d more section(s)\n", rest)
		}
	}
	if excerpt := outlineExcerpt(text); excerpt != "" {
		fmt.Fprintf(&b, "\nopening excerpt:\n%s\n", excerpt)
	}
	return b.String()
}

// outlineHeadings returns the document's markdown headings, indented by depth
// so the outline reads as a tree without repeating the '#' marks.
func outlineHeadings(lines []string) []string {
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		depth := 0
		for depth < len(trimmed) && trimmed[depth] == '#' {
			depth++
		}
		if depth > 6 || depth >= len(trimmed) || (trimmed[depth] != ' ' && trimmed[depth] != '\t') {
			continue // '#' alone, or a code comment like '#include'
		}
		out = append(out, strings.Repeat("  ", depth-1)+strings.TrimSpace(trimmed[depth:]))
	}
	return out
}

// outlineExcerpt is the document's opening prose with its blank-line runs
// collapsed, cut to a rune boundary so the excerpt never splits a multi-byte
// character.
func outlineExcerpt(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	joined := strings.Join(fields, " ")
	if len(joined) <= deliverableSummaryChars {
		return joined
	}
	cut := deliverableSummaryChars
	for cut > 0 && !utf8.RuneStart(joined[cut]) {
		cut--
	}
	return joined[:cut] + "…"
}

// runeBoundaryAtOrBefore trims a byte offset back to a character start: a page
// boundary chosen by byte count can land inside a multi-byte rune, and emitting
// that slice would put invalid UTF-8 in the transcript. It never trims below
// start, so a page always advances and a walking caller terminates.
func runeBoundaryAtOrBefore(body []byte, end, start int) int {
	for end > start && end < len(body) && !utf8.RuneStart(body[end]) {
		end--
	}
	if end == start && start < len(body) {
		_, size := utf8.DecodeRune(body[start:])
		end = start + size
	}
	return end
}

// deliverablePage is the full-mode projection. A page is a byte window reported
// with the exact offset and limit to continue from, so a caller walks the
// document deliberately instead of pulling it in one call.
//
// A read with no paging arguments on a document that fits one page returns the
// body verbatim: mode="full" means the bytes, and a header there would be noise
// the outline already covered. The window header appears only when the caller
// actually got a window.
func deliverablePage(id string, body []byte, offset, limit int) string {
	total := len(body)
	requestedPage := offset > 0 || limit > 0
	offset = min(max(offset, 0), total)
	if limit <= 0 {
		limit = deliverablePageDefault
	}
	limit = min(limit, deliverablePageMax)
	end := min(offset+limit, total)
	// A page must not end mid-character: team documents are frequently CJK, and
	// a raw byte cut would emit invalid UTF-8 into the transcript.
	end = runeBoundaryAtOrBefore(body, end, offset)
	page := body[offset:end]

	if offset == 0 && end == total && (!requestedPage || limit >= total) {
		return string(page)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "deliverable %s — bytes %d-%d of %d\n", id, offset, end, total)
	if end < total {
		fmt.Fprintf(&b, "continue with offset=%d limit=%d\n", end, limit)
	} else {
		b.WriteString("this page reaches the end of the document\n")
	}
	b.WriteString("\n")
	b.Write(page)
	return b.String()
}
