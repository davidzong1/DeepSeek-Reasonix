package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/team"
)

// TestTeamButtonOpensLeaderSession pins the [TEAM] click target state
// (§11.4): the overlay opens straight on the focused team's leader — session
// active, leader current, the chat composer hidden, the leader's own context
// history loading in the window, and the member bar beside it for switching.
func TestTeamButtonOpensLeaderSession(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openTeamOverlay(t)

	if !m.teamPick.session.active {
		t.Fatal("the click must open the session window")
	}
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("the session must open on the leader, got %q", got)
	}
	if !m.hideComposer() {
		t.Fatal("the chat composer must be hidden while the session is up")
	}
	if err := m.teamPick.sessions.AppendMessage("alpha", "lead", team.SessionMessage{
		Kind: "agent", Text: "leader reply", TS: "2026-08-22T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	got := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"lead", "alice", "leader reply"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the session window should render %q, got:\n%s", want, got)
		}
	}
}

// TestTeamButtonSessionKeepsKeysFromChatComposer pins §11.4 modal ownership:
// a key pressed while the session window is up is consumed by the session and
// never reaches the hidden chat composer, and the window never falls back to
// the roster.
func TestTeamButtonSessionKeepsKeysFromChatComposer(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openTeamOverlay(t)
	m.input.SetValue("existing chat draft")

	m = teamKey(m, tea.KeyPressMsg{Code: 'x'})
	if !m.teamPick.session.active {
		t.Fatal("the session window must stay active")
	}
	if got := m.input.Value(); got != "existing chat draft" {
		t.Fatalf("a session key must never reach the chat composer, got %q", got)
	}
}

// TestTeamButtonEscapeReturnsToTeamList pins the session exit state: Esc
// closes the session window back to the team list, where the roster
// management page is one Enter away — the overlay stays up, the chat
// composer stays hidden.
func TestTeamButtonEscapeReturnsToTeamList(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openTeamOverlay(t)

	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.teamPick.session.active {
		t.Fatal("esc must close the session window")
	}
	if m.teamPick == nil {
		t.Fatal("the overlay must stay up after the session closes")
	}
	if !m.hideComposer() {
		t.Fatal("the chat composer stays hidden while the overlay is up")
	}
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Teams") {
		t.Fatalf("esc should land on the team list, got:\n%s", got)
	}
}
