package cli

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestTeamSessionComposerTypesAndSends pins the P4.1 composer (§11.4): enter
// in the browsing state focuses it, printable keys type into the draft —
// never the hidden chat composer — and enter sends the draft as one user
// message into the current member's context, clearing the draft and keeping
// the window and composer focused.
func TestTeamSessionComposerTypesAndSends(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // focus lead
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // focus the composer
	if !m.teamPick.session.input {
		t.Fatal("enter should focus the session composer")
	}
	m.input.SetValue("chat draft") // the hidden composer must stay untouched
	for _, r := range "ping" {
		m = teamKey(m, tea.KeyPressMsg{Code: r})
	}
	if got := m.teamPick.session.buf; got != "ping" {
		t.Fatalf("draft = %q, want %q", got, "ping")
	}
	if got := m.input.Value(); got != "chat draft" {
		t.Fatalf("typing must not touch the hidden composer: %q", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // send
	if got := m.teamPick.session.buf; got != "" {
		t.Fatalf("send should clear the draft, got %q", got)
	}
	msgs, err := m.teamPick.sessions.Messages("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Kind != "user" || msgs[0].Text != "ping" {
		t.Fatalf("send should write one user message, got %+v", msgs)
	}
	if got := m.input.Value(); got != "chat draft" {
		t.Fatalf("send must not touch the hidden composer: %q", got)
	}
	if !m.teamPick.session.input {
		t.Fatal("the composer should stay focused after a send")
	}
}

// TestTeamSessionSendEmptyIgnored pins that an empty draft is dropped — no
// zero-length user message ever lands in a member's context.
func TestTeamSessionSendEmptyIgnored(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // empty send
	msgs, err := m.teamPick.sessions.Messages("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("empty send must not write a message, got %+v", msgs)
	}
}

// TestTeamSessionComposerEscKeepsDraft pins the two-level escape: esc in the
// composer returns to the browsing state keeping the draft, and only a second
// esc — from the browsing state — closes the session window.
func TestTeamSessionComposerEscKeepsDraft(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeTeamName(m, "drafttext")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.teamPick.session.input {
		t.Fatal("esc should leave the composer")
	}
	if got := m.teamPick.session.buf; got != "drafttext" {
		t.Fatalf("esc must keep the draft, got %q", got)
	}
	if !m.teamPick.session.active {
		t.Fatal("esc in the composer must not close the session")
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc}) // browsing: close
	if m.teamPick.session.active {
		t.Fatal("a second esc should close the session window")
	}
}

// TestTeamSessionShiftEnterInsertsNewline pins the multiline draft (§11.4):
// shift+enter and alt+enter append a newline, and send persists the full
// multiline text as one user message.
func TestTeamSessionShiftEnterInsertsNewline(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	for _, r := range "one" {
		m = teamKey(m, tea.KeyPressMsg{Code: r})
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	for _, r := range "two" {
		m = teamKey(m, tea.KeyPressMsg{Code: r})
	}
	if got := m.teamPick.session.buf; got != "one\n\ntwo" {
		t.Fatalf("draft = %q, want %q", got, "one\n\ntwo")
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // send the multiline draft
	msgs, err := m.teamPick.sessions.Messages("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Text != "one\n\ntwo" {
		t.Fatalf("send should persist the full multiline text, got %+v", msgs)
	}
}

// TestTeamSessionComposerSwitchTargetsCurrentMember pins the send target
// (§11.4): ctrl+down inside the composer switches the member window — the
// composer stays focused — and the next send lands in that member's own
// context, never the previous member's.
func TestTeamSessionComposerSwitchTargetsCurrentMember(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // lead
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	for _, r := range "forlead" {
		m = teamKey(m, tea.KeyPressMsg{Code: r})
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // send to lead

	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl}) // switch to alice
	if got := m.teamPick.session.current; got != "alice" {
		t.Fatalf("ctrl+down should switch to alice, got %q", got)
	}
	if !m.teamPick.session.input {
		t.Fatal("switching must keep the composer focused")
	}
	for _, r := range "foralice" {
		m = teamKey(m, tea.KeyPressMsg{Code: r})
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	lead, err := m.teamPick.sessions.Messages("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := m.teamPick.sessions.Messages("alpha", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(lead) != 1 || lead[0].Text != "forlead" {
		t.Fatalf("lead history = %+v, want only forlead", lead)
	}
	if len(alice) != 1 || alice[0].Text != "foralice" {
		t.Fatalf("alice history = %+v, want only foralice", alice)
	}
}

// TestTeamSessionComposerArrowsDoNotSwitchMember pins the draft protection: a
// plain arrow inside the composer is inert — it can never yank the window
// onto another member and carry the draft with it.
func TestTeamSessionComposerArrowsDoNotSwitchMember(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeTeamName(m, "draft")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("arrows must not switch members in the composer, got %q", got)
	}
	if got := m.teamPick.session.buf; got != "draft" {
		t.Fatalf("arrows must not touch the draft, got %q", got)
	}
}

// TestTeamSessionPasteLandsInComposer pins the P4.1 paste seam (§11.7): a
// bracketed paste while the composer is focused appends to the draft and the
// hidden chat composer stays untouched; the browsing-state drop is pinned by
// TestTeamPasteInertInSessionWindow.
func TestTeamSessionPasteLandsInComposer(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // composer focused

	next, _ := m.Update(tea.PasteMsg{Content: "pasted text"})
	m = next.(chatTUI)
	if got := m.teamPick.session.buf; got != "pasted text" {
		t.Fatalf("paste should land in the composer draft, got %q", got)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("paste leaked into the hidden composer: %q", got)
	}
}

// TestTeamSessionPasteKeyCtrlVInComposer pins the key path on the session
// composer: ctrl+v reads the native clipboard and the result lands in the
// draft — not the hidden composer.
func TestTeamSessionPasteKeyCtrlVInComposer(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	setLocalClipboardSession(t)
	prev := readNativeClipboardText
	readNativeClipboardText = func() (string, error) { return "clip paste", nil }
	t.Cleanup(func() { readNativeClipboardText = prev })

	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // composer focused
	_, cmd := m.handleTeamPickerKey(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+v in the composer must arm a clipboard read")
	}
	next, _ := m.Update(cmd())
	m = next.(chatTUI)
	if got := m.teamPick.session.buf; got != "clip paste" {
		t.Fatalf("clipboard text = %q, want %q", got, "clip paste")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("clipboard text leaked into the hidden composer: %q", got)
	}
}

// TestTeamSessionSendFailureKeepsDraft pins §11.3: a failed send keeps the
// draft in the composer and surfaces the error in the session window — the
// text is never dropped.
func TestTeamSessionSendFailureKeepsDraft(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	m.teamPick.runtime = nil // a broken seam must not lose the draft
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeTeamName(m, "precious")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.teamPick.session.buf; got != "precious" {
		t.Fatalf("a failed send must keep the draft, got %q", got)
	}
	if got := m.teamPick.session.errMsg; got == "" {
		t.Fatal("a failed send should surface the error")
	}
	msgs, err := m.teamPick.sessions.Messages("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("a failed send must not write a message, got %+v", msgs)
	}
}
