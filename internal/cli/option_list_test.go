package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func testOptions(n int) []option {
	opts := make([]option, 0, n)
	for i := range n {
		opts = append(opts, option{id: string(rune('a' + i))})
	}
	return opts
}

// TestOptionListSinglePinsCursorCommitCancel walks a single pick: the cursor
// opens on the initial id, moves with up/down, enter commits the cursor row,
// and esc cancels.
func TestOptionListSinglePinsCursorCommitCancel(t *testing.T) {
	var l optionList
	l.setOptions(optionSingle, testOptions(3), "b")
	if l.cursor != 1 {
		t.Fatalf("initial should land on b, got %d", l.cursor)
	}
	consumed, action := l.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if !consumed || action != optionListNone {
		t.Fatalf("down should consume and move, got %v/%v", consumed, action)
	}
	if l.cursor != 2 {
		t.Fatalf("down should move to c, got %d", l.cursor)
	}
	_, action = l.handleKey(tea.KeyPressMsg{Code: 'x'})
	if action != optionListNone {
		t.Fatalf("a letter must not act on the list")
	}
	_, action = l.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if action != optionListCommit {
		t.Fatalf("enter should commit, got %v", action)
	}
	if id, ok := l.choice(); !ok || id != "c" {
		t.Fatalf("choice should be the committed c, got %q/%v", id, ok)
	}
	l.setOptions(optionSingle, testOptions(3), "a")
	if _, action := l.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc}); action != optionListCancel {
		t.Fatalf("esc should cancel, got %v", action)
	}
	if _, ok := l.choice(); ok {
		t.Fatal("a cancelled pick must not report a choice")
	}
}

// TestOptionListInitialAbsentLandsZero pins setOptions: an initial id not in
// the list (a deleted pool entry) opens on row zero.
func TestOptionListInitialAbsentLandsZero(t *testing.T) {
	var l optionList
	l.setOptions(optionSingle, testOptions(2), "zz")
	if l.cursor != 0 {
		t.Fatalf("an absent initial should land on 0, got %d", l.cursor)
	}
}

// TestOptionListMultiToggleCommitsSet pins the multi pick: space toggles rows
// idempotently, enter commits the selected set in option order, and the help
// advertises the toggle.
func TestOptionListMultiToggleCommitsSet(t *testing.T) {
	var l optionList
	l.setOptions(optionMulti, testOptions(4), "")
	if _, action := l.handleKey(tea.KeyPressMsg{Code: ' '}); action != optionListNone {
		t.Fatalf("space should toggle without committing, got %v", action)
	}
	l.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	l.handleKey(tea.KeyPressMsg{Code: ' '})
	l.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	l.handleKey(tea.KeyPressMsg{Code: ' '})
	if got := l.choices(); len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("toggles should select a/b/c, got %v", got)
	}
	_, action := l.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if action != optionListCommit {
		t.Fatalf("enter should commit the set, got %v", action)
	}
	if got := l.choices(); len(got) != 3 {
		t.Fatalf("commit keeps the toggled set, got %v", got)
	}
	if got := l.view(40, 10); !strings.Contains(ansi.Strip(got), "Space toggle") {
		t.Fatalf("multi help should advertise the toggle, got:\n%s", got)
	}
}

// TestOptionListDisabledSkipped pins disabled rows: the cursor jumps over
// them, home/end land on the nearest enabled row, and a fully disabled list
// never moves.
func TestOptionListDisabledSkipped(t *testing.T) {
	var l optionList
	opts := []option{{id: "a"}, {id: "b", disabled: true}, {id: "c"}}
	l.setOptions(optionSingle, opts, "a")
	l.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if l.cursor != 2 {
		t.Fatalf("down should skip the disabled row to c, got %d", l.cursor)
	}
	l.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if l.cursor != 0 {
		t.Fatalf("up should skip the disabled row to a, got %d", l.cursor)
	}
	opts = []option{{id: "a", disabled: true}, {id: "b"}, {id: "c", disabled: true}}
	l.setOptions(optionSingle, opts, "b")
	l.handleKey(tea.KeyPressMsg{Code: tea.KeyHome})
	if l.cursor != 1 {
		t.Fatalf("home should land on the nearest enabled row, got %d", l.cursor)
	}
	l.handleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	if l.cursor != 1 {
		t.Fatalf("end should land on the nearest enabled row, got %d", l.cursor)
	}
	opts = []option{{id: "a", disabled: true}, {id: "b", disabled: true}}
	l.setOptions(optionSingle, opts, "a")
	l.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if l.cursor != 0 {
		t.Fatalf("a fully disabled list must not move, got %d", l.cursor)
	}
}

// TestOptionListScrollPinsOffsetFollow pins scrolling: pgdn/pgup move by the
// window, home/end jump the ends, and followCursor keeps the cursor inside
// the window (offset follows, never out of range).
func TestOptionListScrollPinsOffsetFollow(t *testing.T) {
	var l optionList
	l.setOptions(optionSingle, testOptions(12), "a")
	l.resize(5)
	l.handleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if l.cursor != 5 || l.offset != 1 {
		t.Fatalf("pgdn should move to 5 with offset 1, got %d/%d", l.cursor, l.offset)
	}
	l.handleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if l.cursor != 10 || l.offset != 6 {
		t.Fatalf("pgdn should move to 10 with offset 6, got %d/%d", l.cursor, l.offset)
	}
	l.handleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	if l.cursor != 11 || l.offset != 7 {
		t.Fatalf("end should move to 11 with offset 7, got %d/%d", l.cursor, l.offset)
	}
	l.handleKey(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if l.cursor != 6 || l.offset != 6 {
		t.Fatalf("pgup should move to 6 with the window pulled back to 6, got %d/%d", l.cursor, l.offset)
	}
	l.handleKey(tea.KeyPressMsg{Code: tea.KeyHome})
	if l.cursor != 0 || l.offset != 0 {
		t.Fatalf("home should reset both, got %d/%d", l.cursor, l.offset)
	}
}

// TestOptionListViewAdaptsHeight pins the popup: the window shows at most
// height-3 rows, shrinks to the list, renders the empty state, and truncates
// long labels to the width.
func TestOptionListViewAdaptsHeight(t *testing.T) {
	var l optionList
	l.setOptions(optionSingle, testOptions(12), "a")
	got := ansi.Strip(l.view(40, 7))
	lines := strings.Split(got, "\n")
	if len(lines) != 7 {
		t.Fatalf("height 7 should render 7 lines, got %d:\n%s", len(lines), got)
	}
	if rows := strings.Count(got, "│ "); rows != 4 {
		t.Fatalf("height 7 should show 4 option rows, got %d:\n%s", rows, got)
	}
	got = ansi.Strip(l.view(40, 40))
	if rows := strings.Count(got, "│ "); rows != 12 {
		t.Fatalf("a tall view should show all 12 rows, got %d:\n%s", rows, got)
	}
	var empty optionList
	empty.setOptions(optionSingle, nil, "")
	if got := ansi.Strip(empty.view(40, 10)); !strings.Contains(got, "no options") {
		t.Fatalf("an empty list should render its empty state, got:\n%s", got)
	}
	l.setOptions(optionSingle, []option{{id: "averylongoptionlabelthatmustbetruncated"}}, "a")
	got = ansi.Strip(l.view(20, 10))
	for _, line := range strings.Split(got, "\n") {
		if w := visibleWidth(line); w > 20 {
			t.Fatalf("a view line must not exceed the width, got %d: %q", w, line)
		}
	}
}

// TestOptionListViewKeepsCursorInside pins the render fallback: even without a
// resize, a cursor moved past the window renders with the cursor visible.
func TestOptionListViewKeepsCursorInside(t *testing.T) {
	var l optionList
	l.setOptions(optionSingle, testOptions(12), "a")
	for range 10 {
		l.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	got := ansi.Strip(l.view(40, 7))
	if !strings.Contains(got, "› k") {
		t.Fatalf("the cursor row should be visible without resize, got:\n%s", got)
	}
}

// TestOptionListWheelMovesAndInerts pins wheel input: up/down move one row,
// an empty list reports unconsumed.
func TestOptionListWheelMovesAndInerts(t *testing.T) {
	var l optionList
	l.setOptions(optionSingle, testOptions(3), "a")
	if !l.wheel(false) || l.cursor != 1 {
		t.Fatalf("wheel down should move to b, got %d", l.cursor)
	}
	if !l.wheel(true) || l.cursor != 0 {
		t.Fatalf("wheel up should move back to a, got %d", l.cursor)
	}
	var empty optionList
	if empty.wheel(false) {
		t.Fatal("wheel on an empty list must report unconsumed")
	}
}

// TestOptionListHeightBudget pins the window-derived budget: a quarter of the
// height, bounded, plus chrome.
func TestOptionListHeightBudget(t *testing.T) {
	for _, tt := range []struct{ h, want int }{
		{8, 6}, {20, 8}, {40, 13}, {200, 13},
	} {
		if got := optionListHeight(tt.h); got != tt.want {
			t.Fatalf("optionListHeight(%d) = %d, want %d", tt.h, got, tt.want)
		}
	}
}
