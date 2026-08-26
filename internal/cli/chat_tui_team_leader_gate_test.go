package cli

// Leader-gate implementation tests (§11.4): the refusal is a banner over a
// live management page — assign clears it, and the auto-corrected entry moves
// the roster focus onto the leader it binds.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/team"
)

// TestTeamLeaderRefusalClearsOnAssign pins the refusal lifecycle: the [TEAM]
// click on a leaderless team renders the banner over the live page — the
// roster stays reachable and writable — and l appointing a leader clears it.
func TestTeamLeaderRefusalClearsOnAssign(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "alpha", Template: []team.MemberSlot{
		{MemberID: "alice", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	m := openTeamOverlay(t)
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Only the leader can start a team session") {
		t.Fatalf("the leaderless click should refuse, got:\n%s", got)
	}
	// The refusal is a banner, not the errMsg dead end: the team list and its
	// keys stay up, so a new member can still be added before a leader exists.
	if !strings.Contains(got, "a add team") {
		t.Fatalf("the refusal must not replace the management page, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // into the roster
	m = teamKey(m, tea.KeyPressMsg{Code: 'l'})          // appoint the leader
	if m.teamPick.refusal != "" {
		t.Fatalf("assigning a leader must clear the refusal, got %q", m.teamPick.refusal)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	if !m.teamPick.session.active {
		t.Fatal("a just-appointed leader opens the session")
	}
}

// TestTeamLeaderEntryMovesRosterFocus pins the auto-corrected entry: t from a
// non-leader binds the leader and moves the roster highlight onto it, so the
// next esc lands on the member the session was bound to.
func TestTeamLeaderEntryMovesRosterFocus(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)                                 // leaders first: lead pinned, alice after
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // focus alice
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("the session must bind the leader, got %q", got)
	}
	if got, _ := m.teamPick.model.Focused(); got.ID != "lead" {
		t.Fatalf("the roster highlight must follow the entry, got %q", got.ID)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc}) // back to the roster
	if got, _ := m.teamPick.model.Focused(); got.ID != "lead" {
		t.Fatalf("esc must land on the leader the session bound, got %q", got.ID)
	}
}
