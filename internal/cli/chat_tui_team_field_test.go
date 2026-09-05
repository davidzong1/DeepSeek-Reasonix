package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/team"
)

// TestFieldCursorMovesByRune pins rune-granular movement and clamping on
// multibyte content: a CJK rune and an emoji each occupy one cursor step, and
// movement never leaves the buffer.
func TestFieldCursorMovesByRune(t *testing.T) {
	s := "ab你🚀" // 4 runes across 11 UTF-8 bytes
	if got := fieldRuneCount(s); got != 4 {
		t.Fatalf("rune count = %d, want 4", got)
	}
	if got := fieldMove(s, 4, -1); got != 3 {
		t.Fatalf("left over the emoji should land after 你, got %d", got)
	}
	if got := fieldMove(s, 3, -1); got != 2 {
		t.Fatalf("left over 你 should land between b and 你, got %d", got)
	}
	if got := fieldMove(s, 0, -1); got != 0 {
		t.Fatalf("left at the head must clamp to 0, got %d", got)
	}
	if got := fieldMove(s, 4, 1); got != 4 {
		t.Fatalf("right at the tail must clamp to 4, got %d", got)
	}
	if got := fieldMove(s, 99, 0); got != 4 {
		t.Fatalf("a stale cursor must clamp to the end, got %d", got)
	}
}

// TestFieldInsertDeleteAtCursor pins that insertion happens at the rune cursor
// and backspace/delete remove around it, never the tail.
func TestFieldInsertDeleteAtCursor(t *testing.T) {
	got, cur := fieldInsert("ab你d", 2, "X")
	if got != "abX你d" || cur != 3 {
		t.Fatalf("insert at cursor = %q cur %d, want abX你d / 3", got, cur)
	}
	got, cur = fieldBackspace(got, cur)
	if got != "ab你d" || cur != 2 {
		t.Fatalf("backspace before the cursor = %q cur %d, want ab你d / 2", got, cur)
	}
	got, cur = fieldDelete(got, 2)
	if got != "abd" || cur != 2 {
		t.Fatalf("delete at the cursor = %q cur %d, want abd / 2", got, cur)
	}
	got, cur = fieldBackspace("", 0)
	if got != "" || cur != 0 {
		t.Fatalf("backspace on an empty buffer must be a no-op, got %q cur %d", got, cur)
	}
}

// TestFieldCursorViewPinsRenders pins the block-cursor splice at the rune
// cursor, clamped into range.
func TestFieldCursorViewRendersAtRuneCursor(t *testing.T) {
	if got := fieldCursorView("coder", 3); got != "cod▏er" {
		t.Fatalf("cursor at 3 = %q, want cod▏er", got)
	}
	if got := fieldCursorView("coder", 5); got != "coder▏" {
		t.Fatalf("cursor at the end = %q, want coder▏", got)
	}
	if got := fieldCursorView("ab你d", 2); got != "ab▏你d" {
		t.Fatalf("cursor between multibyte runes = %q, want ab▏你d", got)
	}
	if got := fieldCursorView("ab", 99); got != "ab▏" {
		t.Fatalf("a stale cursor must render at the end, got %q", got)
	}
}

// openRoleEditor drives the TUI onto alice's property editor with the role
// field open, mirroring TestTeamMemberEditRolePersistsAndClearsContext.
func openRoleEditor(t *testing.T) chatTUI {
	t.Helper()
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // focus alice, the non-leader
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open her property editor
	for memberEditFields[m.teamPick.memberEdit.edit] != "role" {
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the role field, prefilled
	return m
}

// TestTeamRoleCursorStaysInFieldMovesAndEditsAtCursor pins that left/right
// inside the role field move the rune cursor and stay inside the field (never
// reaching global navigation), that a typed rune inserts at the cursor, and
// that backspace removes the rune before it.
func TestTeamRoleCursorStaysInFieldMovesAndEditsAtCursor(t *testing.T) {
	m := openRoleEditor(t)
	me := &m.teamPick.memberEdit
	if me.kind != memberEditFieldEdit {
		t.Fatal("the role field must be open for typing")
	}
	if got, want := me.buf, "coder"; got != want {
		t.Fatalf("role prefill = %q, want %q", got, want)
	}
	if got := me.cur; got != 5 {
		t.Fatalf("cursor opens at the end of %q, got %d", me.buf, got)
	}
	edit := me.edit
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyLeft})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyLeft})
	if got := me.cur; got != 3 {
		t.Fatalf("two lefts from the end of %q = %d, want 3", me.buf, got)
	}
	if me.kind != memberEditFieldEdit || me.edit != edit {
		t.Fatalf("left must stay in the role field, not navigate (kind %v edit %d)", me.kind, me.edit)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 'x'})
	if got, want := me.buf, "codxer"; got != want {
		t.Fatalf("typing at the cursor = %q, want %q (insert, not append)", got, want)
	}
	if got := me.cur; got != 4 {
		t.Fatalf("cursor after the insert = %d, want 4", got)
	}
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "codx▏er") {
		t.Fatalf("the block cursor should sit right after the inserted rune, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	if got, want := me.buf, "coder"; got != want {
		t.Fatalf("backspace at the cursor = %q, want %q", got, want)
	}
	// home/end jump the cursor; enter commits the edited value into the draft.
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyHome})
	if got := me.cur; got != 0 {
		t.Fatalf("home should put the cursor at 0, got %d", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if got := me.cur; got != 5 {
		t.Fatalf("end should put the cursor at 5, got %d", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got, want := me.draft.Role, team.RoleID("coder"); got != want {
		t.Fatalf("committed role = %q, want %q", got, want)
	}
}

// TestTeamRoleCursorMovesOverMultibyteByRune pins UTF-8 rune movement on the
// role field: one left from the end crosses a single multibyte rune, and an
// insert at that cursor stays on rune boundaries.
func TestTeamRoleCursorMovesOverMultibyteByRune(t *testing.T) {
	m := openRoleEditor(t)
	me := &m.teamPick.memberEdit
	for range len("coder") {
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	for _, r := range "编𠜎" { // one CJK and one rare CJK-ext rune
		m = teamKey(m, tea.KeyPressMsg{Code: r})
	}
	if got := me.cur; got != 2 {
		t.Fatalf("typing two CJK runes should end at cursor 2, got %d", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyLeft})
	if got := me.cur; got != 1 {
		t.Fatalf("one left should cross a single rune, got %d", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 'X'})
	if got, want := me.buf, "编X𠜎"; got != want {
		t.Fatalf("insert between CJK runes = %q, want %q", got, want)
	}
}

// TestTeamPoolFieldCursorInsertsAtCursor pins the pool field editor's cursor:
// e on an entry opens its identity field prefilled, left moves the rune cursor
// inside the field, and a typed rune inserts there — merged into the draft on
// enter.
func TestTeamPoolFieldCursorInsertsAtCursor(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{
		UserID: "au-1", Identity: "acct@corp", Provider: "anthropic",
		BaseURL: "https://api.anthropic.com", Model: "claude-opus-5", Effort: "high", APIKey: "sk",
	})
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open identity, prefilled
	pool := &m.teamPick.pool
	if got, want := pool.buf, "acct@corp"; got != want {
		t.Fatalf("identity prefill = %q, want %q", got, want)
	}
	if got := pool.cur; got != 9 {
		t.Fatalf("cursor opens at the end of %q, got %d", pool.buf, got)
	}
	for range 3 {
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyLeft})
	}
	if got := pool.cur; got != 6 {
		t.Fatalf("three lefts from the end = %d, want 6", got)
	}
	if pool.kind != poolInputEditField {
		t.Fatalf("left must stay in the field edit, kind=%v", pool.kind)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 'x'})
	if got, want := pool.buf, "acct@cxorp"; got != want {
		t.Fatalf("typing at the cursor = %q, want %q", got, want)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // merge into the draft
	if got, want := pool.draft.Identity, "acct@cxorp"; got != want {
		t.Fatalf("committed identity = %q, want %q", got, want)
	}
}
