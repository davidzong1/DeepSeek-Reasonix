package cli

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// option is one choice of an optionList: id is the committed value, label the
// rendered text (callers may pre-style it; empty renders as the id), disabled
// rows are skipped by the cursor and never selectable. Ids must be unique
// within one list — the multi-selection set keys on them.
type option struct {
	id       string
	label    string
	disabled bool
}

// optionListKind distinguishes single-choice (enter commits the cursor row)
// from multi-choice (space toggles rows, enter commits the selected set).
type optionListKind uint8

const (
	optionSingle optionListKind = iota
	optionMulti
)

// optionListAction is what a handled keypress did to the list, so the host can
// publish or discard the pick in its own Update without function callbacks.
const (
	optionListNone   optionListAction = iota
	optionListCommit                  // enter: read choice()/choices()
	optionListCancel                  // esc: host discards the pick
)

type optionListAction uint8

// optionList is the reusable popup option list: a cursor over a value set with
// offset scrolling, adaptive height, wheel input, and single/multi selection.
// The host forwards keys while the list is open and renders view() inside its
// own panel; letters the list does not consume stay the host's business.
type optionList struct {
	kind       optionListKind
	options    []option
	cursor     int
	offset     int
	maxVisible int                 // window rows for followCursor; set by resize
	selected   map[string]struct{} // single: the committed row; multi: toggled ids
}

// setOptions seeds the list for a pick: cursor on initial (or 0 when absent),
// selection cleared, offset reset.
func (l *optionList) setOptions(kind optionListKind, opts []option, initial string) {
	l.kind = kind
	l.options = opts
	l.cursor, l.offset = 0, 0
	l.selected = make(map[string]struct{})
	for i, o := range opts {
		if o.id == initial {
			l.cursor = i
			return
		}
	}
}

// resize records the visible window rows the host derives from its window
// height, then pulls the offset back onto the cursor. Idempotent for the same
// value, so it can run on every render.
func (l *optionList) resize(rows int) {
	if rows == l.maxVisible {
		return
	}
	l.maxVisible = max(rows, 1)
	l.followCursor()
}

// handleKey routes one keypress: navigation consumes and moves the cursor,
// enter commits, esc cancels, and anything else reports unconsumed. A commit
// on an empty list is a no-op that still reports consumed.
func (l *optionList) handleKey(msg tea.KeyPressMsg) (bool, optionListAction) {
	switch msg.String() {
	case "up", "k", "left":
		l.moveCursor(-1)
		return true, optionListNone
	case "down", "j", "right":
		l.moveCursor(1)
		return true, optionListNone
	case "home":
		l.moveCursor(-len(l.options))
		return true, optionListNone
	case "end":
		l.moveCursor(len(l.options))
		return true, optionListNone
	case "pgup":
		l.moveCursor(-max(l.maxVisible, 1))
		return true, optionListNone
	case "pgdown":
		l.moveCursor(max(l.maxVisible, 1))
		return true, optionListNone
	case "enter":
		if len(l.options) == 0 {
			return true, optionListNone
		}
		if l.kind == optionSingle {
			l.selectCursor()
		}
		return true, optionListCommit
	case "space":
		if l.kind == optionMulti && len(l.options) > 0 {
			l.toggleCursor()
		}
		return true, optionListNone
	case "esc", "ctrl+c":
		return true, optionListCancel
	}
	return false, optionListNone
}

// wheel scrolls the cursor one row per wheel tick; the host forwards wheel
// events only while the list is the active picker.
func (l *optionList) wheel(up bool) bool {
	if len(l.options) == 0 {
		return false
	}
	if up {
		l.moveCursor(-1)
	} else {
		l.moveCursor(1)
	}
	return true
}

// choice is the committed id of a single pick; ok is false when nothing was
// selected (an empty list or a never-committed multi list).
func (l *optionList) choice() (string, bool) {
	for _, o := range l.options {
		if _, ok := l.selected[o.id]; ok {
			return o.id, true
		}
	}
	return "", false
}

// choices lists the committed ids of a multi pick in option order.
func (l *optionList) choices() []string {
	var out []string
	for _, o := range l.options {
		if _, ok := l.selected[o.id]; ok {
			out = append(out, o.id)
		}
	}
	return out
}

// currentLabel is the cursor row's display text, for the host's inline
// preview column while the pick is open.
func (l *optionList) currentLabel() string {
	if len(l.options) == 0 {
		return ""
	}
	return l.labelOf(l.options[l.cursor])
}

func (l *optionList) labelOf(o option) string {
	if o.label != "" {
		return o.label
	}
	return o.id
}

// moveCursor shifts the cursor by delta rows, skipping disabled options and
// clamping at the ends. A page jump lands on the nearest enabled row in the
// jump direction; a wall of disabled rows leaves the cursor put.
func (l *optionList) moveCursor(delta int) {
	n := len(l.options)
	if n == 0 {
		return
	}
	start := l.cursor
	dir := 1
	if delta < 0 {
		dir = -1
	}
	step := delta
	if step == 0 {
		step = dir
	}
	for range n {
		prev := l.cursor
		l.cursor = min(max(l.cursor+step, 0), n-1)
		if !l.options[l.cursor].disabled {
			break
		}
		if l.cursor == prev {
			step = -step // wall: probe the other way
			continue
		}
		step = dir
	}
	if l.options[l.cursor].disabled {
		l.cursor = start // a wall on both sides leaves the cursor put
	}
	l.followCursor()
}

// followCursor pulls the offset so the cursor stays inside the visible window.
func (l *optionList) followCursor() {
	if l.maxVisible <= 0 {
		return
	}
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+l.maxVisible {
		l.offset = l.cursor - l.maxVisible + 1
	}
}

// view renders the popup: a bordered window of rows — cursor and selected
// marks, disabled rows dimmed — over an adaptive row count, then the help
// line. height is the host-visible rows; the cursor is always inside the
// window even when the host forgot to resize.
func (l *optionList) view(width, height int) string {
	rows := min(max(height-3, 1), len(l.options))
	if len(l.options) == 0 {
		rows = 1
	}
	// The window clamps to the list: an offset that outlived a shrunken window
	// would otherwise leave a blank head and a fake "no options" tail.
	start := min(l.offset, max(len(l.options)-rows, 0))
	if l.cursor < start || l.cursor >= start+rows {
		start = min(max(l.cursor-rows+1, 0), max(len(l.options)-rows, 0))
	}
	var b strings.Builder
	b.WriteString("┌" + strings.Repeat("─", max(width-2, 0)) + "┐\n")
	for i := start; i < start+rows; i++ {
		var line string
		if i >= len(l.options) {
			line = dim("no options")
		} else {
			o := l.options[i]
			mark := "  "
			if i == l.cursor {
				mark = "› "
			}
			line = mark + l.labelOf(o)
			if o.disabled {
				line = mark + dim(l.labelOf(o))
			}
			if l.kind == optionMulti {
				if _, ok := l.selected[o.id]; ok {
					line += " ✓"
				}
			}
		}
		// The row pads to exactly width-4 cells between its fixed corners —
		// padColumn's at-least-one-space guarantee would widen a full row.
		cell := max(width-4, 0)
		line = truncateCells(line, cell)
		line += strings.Repeat(" ", cell-visibleWidth(line))
		b.WriteString("│ " + line + " │\n")
	}
	b.WriteString("└" + strings.Repeat("─", max(width-2, 0)) + "┘\n")
	help := "↑/↓ move · Enter confirm · Esc cancel"
	if l.kind == optionMulti {
		help = "↑/↓ move · Space toggle · Enter confirm · Esc cancel"
	}
	help = truncateCells(dim(help), max(width, 0))
	help += strings.Repeat(" ", max(width-visibleWidth(help), 0))
	b.WriteString(help)
	return b.String()
}

func (l *optionList) selectCursor() {
	l.selected = map[string]struct{}{l.options[l.cursor].id: {}}
}

func (l *optionList) toggleCursor() {
	id := l.options[l.cursor].id
	if _, ok := l.selected[id]; ok {
		delete(l.selected, id)
	} else {
		l.selected[id] = struct{}{}
	}
}

// optionListHeight is the popup's total height budget — rows plus chrome —
// derived from the window height: a quarter, bounded, so the list adapts to
// the window without ever dominating the overlay.
func optionListHeight(h int) int {
	return min(max(h/4, 3), 10) + 3
}
