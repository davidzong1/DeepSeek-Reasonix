// F-B1 acceptance for TEAM_FRAME_PATH_COST_ROUTE.md §5.3: renderTranscript must
// render the byte-identical frame after the per-frame horizontal join was
// replaced by appending the scrollbar cell to each row.
package cli

import (
	"bytes"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// transcriptFixture is one sized model with `lines` committed blocks, its wrap
// cache built at the model's own content width.
func transcriptFixture(t *testing.T, width, height, lines int) chatTUI {
	t.Helper()
	m := frameCostModel(t, frameCostMembers, lines)
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = next.(chatTUI)
	m.syncWrappedLines(transcriptContentWidth(m.width, m.nativeScrollback), true)
	m.feedViewportContent()
	return m
}

// referenceRenderTranscript is the implementation F-B1 replaced: every visible
// row joined to the scrollbar column with lipgloss's ANSI-aware horizontal join.
// It is the byte gate's oracle, so it is written the long way on purpose.
func referenceRenderTranscript(m chatTUI) string {
	h := m.viewport.Height()
	if h <= 0 {
		return ""
	}
	cw := m.viewport.Width()
	lines := m.wrappedLines
	total := len(lines)
	yoff := m.viewport.YOffset()
	start, end := m.sel.ordered()
	thumbStart, thumbSize := scrollbarThumb(h, yoff, total)
	blank := strings.Repeat(" ", cw)

	rows := make([]string, h)
	bar := make([]string, h)
	for r := range h {
		idx := yoff + r
		line := blank
		if idx >= 0 && idx < total {
			line = lines[idx]
		}
		if m.sel.active && !m.sel.empty() {
			if lo, hi, ok := selSpan(idx, start, end, cw); ok {
				line = lipgloss.StyleRanges(line, lipgloss.NewRange(lo, hi, selStyle))
			}
		}
		rows[r] = line
		bar[r] = scrollbarCell(r, total, h, thumbStart, thumbSize)
	}
	joined := make([]string, h)
	for r := range h {
		joined[r] = renderTranscriptRow(rows[r], bar[r])
	}
	return strings.Join(joined, "\n")
}

// TestRenderTranscriptAppendMatchesTheHorizontalJoin is the node's strong gate:
// for every fixture shape — scrolled to top, middle and bottom, with and without
// a selection, an empty transcript, an odd content width, and no overflow — the
// appended row and the joined row are byte-equal. A mismatch fails here rather
// than in a terminal, where it would read as a stray colour.
func TestRenderTranscriptAppendMatchesTheHorizontalJoin(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
		lines         int
		scrollTo      float64 // 0 = top, 1 = bottom
		selectRows    bool
	}{
		{name: "top", width: 120, height: 40, lines: 200},
		{name: "middle", width: 120, height: 40, lines: 200, scrollTo: 0.5},
		{name: "bottom", width: 120, height: 40, lines: 200, scrollTo: 1},
		{name: "selected", width: 120, height: 40, lines: 200, scrollTo: 0.3, selectRows: true},
		{name: "empty transcript", width: 120, height: 40},
		{name: "odd content width", width: 81, height: 25, lines: 120, scrollTo: 0.4},
		{name: "no overflow", width: 120, height: 60, lines: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := transcriptFixture(t, tc.width, tc.height, tc.lines)
			if tc.lines > 0 {
				span := max(len(m.wrappedLines)-m.viewport.Height(), 0)
				m.viewport.SetYOffset(int(tc.scrollTo * float64(span)))
			}
			if tc.selectRows {
				m.sel = selection{active: true, anchor: selPos{line: 2, col: 1}, head: selPos{line: 5, col: 7}}
			}
			got := m.renderTranscript()
			want := referenceRenderTranscript(m)
			if !bytes.Equal([]byte(got), []byte(want)) {
				t.Fatalf("appended frame differs from the horizontal join\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

// TestRenderTranscriptAppendMatchesOnWideRunes pins the width accounting the join
// used to do: a row holding CJK and emoji is wider in cells than in bytes, and the
// appended bar must still land in the last cell of the frame.
func TestRenderTranscriptAppendMatchesOnWideRunes(t *testing.T) {
	m := transcriptFixture(t, 60, 12, 0)
	m.viewport.SetHeight(4)
	m.wrappedLines = []string{
		wrapTranscript("成员报告：构建完成 🎉 done", m.viewport.Width()),
		wrapTranscript("混合 mixed 内容 with emoji ✅ and text", m.viewport.Width()),
		wrapTranscript("short", m.viewport.Width()),
	}
	m.feedViewportContent()

	got := m.renderTranscript()
	if want := referenceRenderTranscript(m); got != want {
		t.Fatalf("wide-rune rows differ\n got: %q\nwant: %q", got, want)
	}
	for i, row := range strings.Split(got, "\n") {
		if w := visibleWidth(row); w != m.viewport.Width()+1 {
			t.Fatalf("row %d is %d cells wide, want content width + the 1-cell bar: %q", i, w, row)
		}
	}
}

// TestRenderTranscriptAppendMatchesOnStyledRows pins the case the join was really
// protecting: a row that ends inside an ANSI style. wrapTranscript pads each line
// to the content width outside its styles, and the selection highlight is what
// makes a row end inside one — so this fixture keeps the padding the append
// relies on and styles the last cells of a row, which is where a naive append
// would let the bar cell inherit the style or the style leak into it.
func TestRenderTranscriptAppendMatchesOnStyledRows(t *testing.T) {
	m := transcriptFixture(t, 100, 20, 0)
	m.viewport.SetHeight(4)
	cw := m.viewport.Width()
	m.wrappedLines = []string{
		wrapTranscript("\x1b[31mred content that stays open for a while\x1b[0m", cw),
		wrapTranscript("\x1b[1;32mbold green\x1b[0m closed", cw),
		wrapTranscript("plain row", cw),
		wrapTranscript("", cw), // an empty block still wraps to one padded row
		wrapTranscript("tail", cw),
	}
	m.feedViewportContent()
	m.viewport.SetYOffset(0)
	// A selection whose end lands on the row's last column: the row then ends
	// inside the reverse style, which is the shape that used to need the join.
	m.sel = selection{active: true, anchor: selPos{line: 1, col: 2}, head: selPos{line: 3, col: cw}}

	got := m.renderTranscript()
	if want := referenceRenderTranscript(m); got != want {
		t.Fatalf("styled rows differ\n got: %q\nwant: %q", got, want)
	}
	for i, row := range strings.Split(got, "\n") {
		if w := visibleWidth(row); w != cw+1 {
			t.Fatalf("row %d is %d cells wide, want content width + the 1-cell bar: %q", i, w, row)
		}
	}
}

// TestWrapPadsEveryLineToTheContentWidth pins the invariant the append relies on:
// wrapTranscript pads every line it produces to the content width, so appending
// the 1-cell bar lands in the last column without the join's width pad. It guards
// the assumption rather than testing an unreachable case — a line shorter than cw
// would be the one input where the two renderings differ.
func TestWrapPadsEveryLineToTheContentWidth(t *testing.T) {
	m := transcriptFixture(t, 100, 20, 40)
	cw := m.viewport.Width()
	if len(m.wrappedLines) == 0 {
		t.Fatal("precondition: the fixture must have wrapped lines")
	}
	for i, line := range m.wrappedLines {
		if got := visibleWidth(line); got != cw {
			t.Fatalf("line %d is %d cells wide, want the content width %d: %q", i, got, cw, line)
		}
	}
	// And the rendered frame is the content width plus the bar column, row for
	// row — the property the equality gate above asserts byte-wise.
	for i, row := range strings.Split(m.renderTranscript(), "\n") {
		if got := visibleWidth(row); got != cw+1 {
			t.Fatalf("frame row %d is %d cells wide, want %d", i, got, cw+1)
		}
	}
}

// TestFrameViewCostRecordsTheDrop is the F-B1 benchmark: one full-screen View()
// per iteration, the fixed cost bubbletea pays on the goroutine that also
// handles input. It asserts nothing — the number is recorded next to §2.2's
// baseline so the drop is visible in the test log.
func TestFrameViewCostRecordsTheDrop(t *testing.T) {
	m := transcriptFixture(t, frameCostWidth, frameCostHeight, frameCostLines)
	frameCostReport(t, "View() after F-B1", frameCostView(m))
}
