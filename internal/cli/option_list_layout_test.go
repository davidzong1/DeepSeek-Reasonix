package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestOptionListViewBordersAligned pins the popup geometry: every rendered
// line spans exactly the requested width, and each border corner sits in its
// column — a row padded by padColumn's at-least-one-space guarantee used to
// fall one cell short of the frame.
func TestOptionListViewBordersAligned(t *testing.T) {
	var l optionList
	l.setOptions(optionSingle, []option{
		{id: "a", label: "Alpha"},
		{id: "b", label: "Beta", disabled: true},
		{id: "c", label: "Gamma"},
		{id: "d", label: "Delta"},
	}, "a")
	l.resize(4)
	out := l.view(40, 7)
	lines := strings.Split(out, "\n")
	if len(lines) != 7 {
		t.Fatalf("height 7 should render 7 lines, got %d:\n%s", len(lines), out)
	}
	for i, ln := range lines {
		if w := visibleWidth(ln); w != 40 {
			t.Errorf("line %d spans %d cells, want 40: %q", i, w, ln)
		}
	}
	if s := ansi.Strip(lines[0]); !strings.HasPrefix(s, "┌") || !strings.HasSuffix(s, "┐") {
		t.Errorf("top border misplaced: %q", s)
	}
	for i, ln := range lines[1:5] {
		s := ansi.Strip(ln)
		if !strings.HasPrefix(s, "│ ") || !strings.HasSuffix(s, " │") {
			t.Errorf("row %d border misplaced: %q", i, s)
		}
	}
	if s := ansi.Strip(lines[5]); !strings.HasPrefix(s, "└") || !strings.HasSuffix(s, "┘") {
		t.Errorf("bottom border misplaced: %q", s)
	}
	if h := ansi.Strip(lines[6]); visibleWidth(h) != 40 {
		t.Errorf("help line spans %d cells, want 40", visibleWidth(h))
	}
}

// TestOptionListViewWindowGrowthKeepsFullRows pins the offset clamp: a cursor
// scrolled down in a small window leaves a stale offset behind, and a window
// that grows past the list must render every row — no blank head, no fake
// "no options" tail.
func TestOptionListViewWindowGrowthKeepsFullRows(t *testing.T) {
	var l optionList
	l.setOptions(optionSingle, testOptions(5), "e") // cursor on the last row
	l.resize(3)
	if l.offset != 2 {
		t.Fatalf("a small window should scroll to offset 2, got %d", l.offset)
	}
	l.resize(10) // the window outgrows the list again
	out := l.view(30, 13)
	if strings.Contains(out, "no options") {
		t.Fatalf("a grown window must not render a fake no-options tail:\n%s", out)
	}
	lines := strings.Split(out, "\n")
	for i, ln := range lines[1:6] {
		s := ansi.Strip(ln)
		body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(s, "│ "), " │"), "› "))
		if len(body) != 1 || body[0] < 'a' || body[0] > 'e' {
			t.Fatalf("row %d renders no real option: %q", i, s)
		}
	}
}

// TestOptionListViewEmptyList pins the empty state's geometry: one "no
// options" row between the frame lines, every line at the requested width.
func TestOptionListViewEmptyList(t *testing.T) {
	var l optionList
	l.setOptions(optionSingle, nil, "")
	out := l.view(24, 8)
	lines := strings.Split(out, "\n")
	if len(lines) != 4 {
		t.Fatalf("an empty list should render 4 lines, got %d:\n%s", len(lines), out)
	}
	if s := ansi.Strip(lines[1]); !strings.Contains(s, "no options") {
		t.Fatalf("the empty state row is missing: %q", s)
	}
	for i, ln := range lines {
		if w := visibleWidth(ln); w != 24 {
			t.Errorf("line %d spans %d cells, want 24: %q", i, w, ln)
		}
	}
}

// TestOptionListViewTinyWindowKeepsCursor pins the one-row window: whatever
// the scroll state, the single visible row is the cursor row.
func TestOptionListViewTinyWindowKeepsCursor(t *testing.T) {
	var l optionList
	l.setOptions(optionSingle, testOptions(8), "a")
	l.resize(1)
	for range 7 {
		l.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	out := l.view(24, 4)
	lines := strings.Split(out, "\n")
	if len(lines) != 4 {
		t.Fatalf("height 4 should render 4 lines, got %d", len(lines))
	}
	if s := ansi.Strip(lines[1]); !strings.Contains(s, "› h") {
		t.Fatalf("the single window row must be the cursor row on the last option: %q", s)
	}
}

// TestOptionListViewMultiCheckedStaysAligned pins the multi-selection marks:
// "✓" appended to a label must not widen the row past the frame.
func TestOptionListViewMultiCheckedStaysAligned(t *testing.T) {
	var l optionList
	l.setOptions(optionMulti, testOptions(5), "")
	l.toggleCursor() // a
	l.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	l.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	l.handleKey(tea.KeyPressMsg{Code: ' '}) // c
	l.resize(5)
	out := l.view(30, 8)
	if n := strings.Count(ansi.Strip(out), "✓"); n != 2 {
		t.Fatalf("two toggles should mark two rows, got %d:\n%s", n, out)
	}
	for i, ln := range strings.Split(out, "\n") {
		if w := visibleWidth(ln); w != 30 {
			t.Errorf("line %d spans %d cells, want 30: %q", i, w, ln)
		}
	}
}

// TestOptionListViewHelpFitsWidth pins the help line: truncated to the width
// even in a narrow window, where a frame row is only a few cells wide.
func TestOptionListViewHelpFitsWidth(t *testing.T) {
	var l optionList
	l.setOptions(optionSingle, testOptions(2), "a")
	for i, ln := range strings.Split(l.view(10, 6), "\n") {
		if w := visibleWidth(ln); w != 10 {
			t.Errorf("line %d spans %d cells, want 10: %q", i, w, ln)
		}
	}
}

// TestOptionListViewScrolledWindowShowsContiguousRows pins the scrolled
// window: rows are a contiguous slice of the option list, cursor row inside.
func TestOptionListViewScrolledWindowShowsContiguousRows(t *testing.T) {
	var l optionList
	l.setOptions(optionSingle, testOptions(12), "a")
	l.resize(5)
	l.handleKey(tea.KeyPressMsg{Code: tea.KeyEnd}) // cursor 11, offset 7
	out := l.view(24, 8)                           // 5 row window
	lines := strings.Split(out, "\n")
	for i, ln := range lines[1:6] {
		s := ansi.Strip(ln)
		body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(s, "│ "), " │"), "› "))
		if len(body) != 1 || body[0] != 'h'+byte(i) {
			t.Fatalf("window should show h..l at row %d, got %q", i, s)
		}
	}
}
