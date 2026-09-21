package cli

// Deliverable read projections (R2): the default read is an outline and the
// body arrives only on request. Measured on a real leader session, two full
// reads cost ~356K tokens of request prefix over the rounds that followed them.

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"reasonix/internal/tool"
)

// publishDeliverableFixture publishes one document and returns its id.
func publishDeliverableFixture(t *testing.T, body string) string {
	t.Helper()
	t.Setenv("REASONIX_STATE_HOME", t.TempDir())
	tools := deliverableToolSet(t, newMemberDeliverableTools("alpha", "m1", &bytes.Buffer{}))
	out, err := executeTool(t, tools[publishDeliverableName], `{"slug":"report","body":`+jsonString(body)+`}`)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	for field := range strings.FieldsSeq(out) {
		if strings.HasSuffix(field, ".md") {
			return field
		}
	}
	t.Fatalf("publish output names no id: %q", out)
	return ""
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func leaderReadTool(t *testing.T) tool.Tool {
	t.Helper()
	return deliverableToolSet(t, newLeaderDeliverableTools("alpha", "lead", &bytes.Buffer{}))[readDeliverableName]
}

// outlineSections returns just the outline's section list, so an assertion about
// what counts as a heading is not confused by the opening excerpt, which quotes
// the document's own opening bytes.
func outlineSections(t *testing.T, outline string) string {
	t.Helper()
	_, after, ok := strings.Cut(outline, "sections (")
	if !ok {
		return ""
	}
	_, block, ok := strings.Cut(after, ":\n")
	if !ok {
		return ""
	}
	sections, _, _ := strings.Cut(block, "\nopening excerpt:")
	return sections
}

// TestDeliverableReadDefaultsToOutline is the token contract: the default read
// identifies the document without carrying its body, and the body arrives only
// when the caller asks for it.
func TestDeliverableReadDefaultsToOutline(t *testing.T) {
	body := "# Report\n\nintro line\n\n## Findings\n\n" + strings.Repeat("finding detail line\n", 200)
	id := publishDeliverableFixture(t, body)
	read := leaderReadTool(t)

	outline, err := executeTool(t, read, `{"id":"`+id+`"}`)
	if err != nil {
		t.Fatalf("outline read: %v", err)
	}
	if !strings.Contains(outline, "outline only") {
		t.Fatalf("the default read must be an outline, got %q", outline)
	}
	if len(outline) >= len(body)/4 {
		t.Fatalf("the outline must be a fraction of the body: %d of %d bytes", len(outline), len(body))
	}
	sections := outlineSections(t, outline)
	if !strings.Contains(sections, "Report") || !strings.Contains(sections, "Findings") {
		t.Fatalf("the outline must list the document's sections, got %q", sections)
	}
	if strings.Contains(sections, "finding detail line") {
		t.Fatalf("the section list must carry headings only, got %q", sections)
	}
	// The excerpt identifies the document; it is bounded and is not the body.
	if !strings.Contains(outline, "intro line") {
		t.Errorf("the outline must carry an opening excerpt, got:\n%s", outline)
	}
	if len(outline) > 4*1024 {
		t.Fatalf("the outline must stay bounded, got %d bytes", len(outline))
	}

	full, err := executeTool(t, read, `{"id":"`+id+`","mode":"full"}`)
	if err != nil {
		t.Fatalf("full read: %v", err)
	}
	if full != body {
		t.Fatalf("mode=full must return the document verbatim:\n got %d bytes\nwant %d", len(full), len(body))
	}
}

// TestDeliverableReadPagesTheBody pins paging: a windowed read reports the exact
// offset to continue from, and walking the pages reconstructs the document.
func TestDeliverableReadPagesTheBody(t *testing.T) {
	body := strings.Repeat("abcdefghij", 1000) // 10,000 bytes
	id := publishDeliverableFixture(t, body)
	read := leaderReadTool(t)

	var got strings.Builder
	offset := 0
	for pages := 0; ; pages++ {
		if pages > 20 {
			t.Fatal("paging did not terminate")
		}
		out, err := executeTool(t, read, `{"id":"`+id+`","mode":"full","offset":`+itoa(offset)+`,"limit":3000}`)
		if err != nil {
			t.Fatalf("page at %d: %v", offset, err)
		}
		head, payload, _ := strings.Cut(out, "\n\n")
		if !strings.Contains(head, "bytes "+itoa(offset)+"-") {
			t.Fatalf("page header must name the window, got %q", head)
		}
		got.WriteString(payload)
		if strings.Contains(head, "reaches the end") {
			break
		}
		if !strings.Contains(head, "continue with offset=") {
			t.Fatalf("a mid-document page must name the next offset, got %q", head)
		}
		offset += 3000
	}
	if got.String() != body {
		t.Fatalf("paged reads reconstructed %d bytes, want %d", got.Len(), len(body))
	}
}

// TestDeliverableReadRefusesUnknownMode pins the refusal: a typo must not be
// silently downgraded to an outline, or a caller that meant to read the body
// would believe it had.
func TestDeliverableReadRefusesUnknownMode(t *testing.T) {
	id := publishDeliverableFixture(t, "# Report\n\nbody\n")
	if _, err := executeTool(t, leaderReadTool(t), `{"id":"`+id+`","mode":"everything"}`); err == nil {
		t.Fatal("an unknown mode must be refused")
	}
}

// TestDeliverableOutlineBoundsItself pins the outline's own ceiling: a document
// with hundreds of sections must not produce an outline proportional to them.
func TestDeliverableOutlineBoundsItself(t *testing.T) {
	var b strings.Builder
	for i := range 500 {
		b.WriteString("## section ")
		b.WriteString(itoa(i))
		b.WriteString("\nbody\n")
	}
	id := publishDeliverableFixture(t, b.String())
	out, err := executeTool(t, leaderReadTool(t), `{"id":"`+id+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "more section(s)") {
		t.Fatalf("a 500-section document must report the truncated remainder:\n%s", out)
	}
	if len(out) > 4*1024 {
		t.Fatalf("the outline must stay bounded, got %d bytes", len(out))
	}
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

// TestDeliverableOutlineIgnoresCodeComments pins the heading scan: a '#' that
// starts a code comment is not a section.
func TestDeliverableOutlineIgnoresCodeComments(t *testing.T) {
	id := publishDeliverableFixture(t, "# Title\n\n```c\n#include <stdio.h>\n```\n\n## Real Section\n\nbody\n")
	out, err := executeTool(t, leaderReadTool(t), `{"id":"`+id+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	sections := outlineSections(t, out)
	if strings.Contains(sections, "include") {
		t.Fatalf("a code comment must not read as a section:\n%s", sections)
	}
	if !strings.Contains(sections, "Real Section") {
		t.Fatalf("the real heading must appear:\n%s", sections)
	}
}

// TestDeliverablePageNeverSplitsACharacter pins the page boundary: team
// documents are frequently CJK, where a byte-count cut lands mid-rune. A page
// must carry valid UTF-8 and the walk must still cover every byte.
func TestDeliverablePageNeverSplitsACharacter(t *testing.T) {
	body := strings.Repeat("中文内容测试", 500) // 3 bytes per rune, no ASCII
	id := publishDeliverableFixture(t, body)
	read := leaderReadTool(t)

	var got strings.Builder
	offset := 0
	for pages := 0; ; pages++ {
		if pages > 200 {
			t.Fatal("paging did not terminate on a CJK document")
		}
		out, err := executeTool(t, read, `{"id":"`+id+`","mode":"full","offset":`+itoa(offset)+`,"limit":1000}`)
		if err != nil {
			t.Fatalf("page at %d: %v", offset, err)
		}
		head, payload, _ := strings.Cut(out, "\n\n")
		if !utf8.ValidString(payload) {
			t.Fatalf("page at %d is not valid UTF-8", offset)
		}
		got.WriteString(payload)
		if strings.Contains(head, "reaches the end") {
			break
		}
		next := head[strings.LastIndex(head, "offset=")+len("offset="):]
		next = strings.TrimSpace(strings.SplitN(next, " ", 2)[0])
		parsed := 0
		for _, r := range next {
			parsed = parsed*10 + int(r-'0')
		}
		if parsed <= offset {
			t.Fatalf("page at %d reported a non-advancing offset %d", offset, parsed)
		}
		offset = parsed
	}
	if got.String() != body {
		t.Fatalf("paged CJK reads reconstructed %d bytes, want %d", got.Len(), len(body))
	}
}
