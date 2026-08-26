package cli

// Regression tests for the leader-first entry contract (§11.4): entering from
// a regular member auto-selects the leader, a leaderless team refuses, and the
// roster keeps leaders pinned on top.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/team"
)

// focusMember steps the roster focus onto the member with the given id,
// wherever the current ordering put it — the entry contract must not depend
// on which member the roster happens to open on.
func focusMember(t *testing.T, m chatTUI, id string) chatTUI {
	t.Helper()
	for i := 0; i < 8; i++ {
		if got, ok := m.teamPick.model.Focused(); ok && got.ID == id {
			return m
		}
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	t.Fatalf("member %q not found in the roster", id)
	return m
}

// TestTeamSessionEntryFromMemberAutoSelectsLeader pins t from a regular
// member: the session opens on the team's leader, never the focused member,
// and no refusal is rendered.
func TestTeamSessionEntryFromMemberAutoSelectsLeader(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := focusMember(t, openRoster(t), "alice")
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	if !m.teamPick.session.active {
		t.Fatal("t from a regular member should auto-enter the session")
	}
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("session should bind the team leader, got %q", got)
	}
	if m.teamPick.errMsg != "" {
		t.Fatalf("auto-entry must not refuse, got %q", m.teamPick.errMsg)
	}
}

// TestTeamSessionEntryMultiLeaderPicksFirstLeader pins multi-leader teams:
// entering from a regular member binds the registry's first leader slot,
// deterministically (the same first-leader order the [TEAM] click uses).
func TestTeamSessionEntryMultiLeaderPicksFirstLeader(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "alpha", Template: []team.MemberSlot{
		{MemberID: "lead1", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive},
		{MemberID: "alice", Role: team.RoleCoder, Status: team.MemberStatusActive},
		{MemberID: "lead2", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive},
	}})
	m := focusMember(t, openRoster(t), "alice")
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	if !m.teamPick.session.active {
		t.Fatal("t from a regular member should auto-enter the session")
	}
	if got := m.teamPick.session.current; got != "lead1" {
		t.Fatalf("the registry's first leader slot must win, got %q", got)
	}
}

// TestTeamSessionEntryNoLeaderRefused pins the leaderless team: entering a
// session refuses with the existing message and never opens a window.
func TestTeamSessionEntryNoLeaderRefused(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "alpha", Template: []team.MemberSlot{
		{MemberID: "alice", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	if m.teamPick.session.active {
		t.Fatal("a leaderless team must not open a session")
	}
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "Only the leader can start a team session") {
		t.Fatalf("the refusal should keep its message, got:\n%s", got)
	}
}

// TestTeamButtonClickNoLeaderRefused pins the [TEAM] click on a leaderless
// team: the overlay stays on the management page with a refusal instead of a
// silent dead end.
func TestTeamButtonClickNoLeaderRefused(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "alpha", Template: []team.MemberSlot{
		{MemberID: "alice", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	m := openTeamOverlay(t)
	if m.teamPick.session.active {
		t.Fatal("a leaderless team must not auto-open a session")
	}
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Only the leader can start a team session") {
		t.Fatalf("the refusal should keep its message, got:\n%s", got)
	}
	// The refusal is a banner, not an error page: the management list stays
	// visible so l can appoint a leader without leaving the overlay.
	if !strings.Contains(got, "alpha") {
		t.Fatalf("the management page must stay visible under the banner, got:\n%s", got)
	}
}

// TestTeamButtonClickNoLeaderSuspendedSilent pins the suspend boundary: a
// deliberately left (Ctrl+T) leaderless team reopens quietly on the
// management page — the user's preference wins over the leader gate.
func TestTeamButtonClickNoLeaderSuspendedSilent(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "alpha", Template: []team.MemberSlot{
		{MemberID: "alice", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	m := openTeamOverlay(t)
	if err := m.teamPick.sessions.WriteSelection("alpha", team.SessionSelection{Team: "alpha", Suspended: true}); err != nil {
		t.Fatal(err)
	}
	m.onTeamButtonClick() // reopen: the suspend preference governs
	if m.teamPick.session.active {
		t.Fatal("a suspended team must not auto-open a session")
	}
	if got := ansi.Strip(m.renderTeamPicker()); strings.Contains(got, "Only the leader can start a team session") {
		t.Fatalf("a suspended leaderless team must reopen silently, got:\n%s", got)
	}
}

// TestTeamRosterPinsLeaderOnTop pins the display order: after the roster
// opens, the leader is focused first — the pinned head of the list.
func TestTeamRosterPinsLeaderOnTop(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	if got, ok := m.teamPick.model.Focused(); !ok || got.ID != "lead" {
		t.Fatalf("the roster should focus the pinned leader, got %q", got.ID)
	}
}
