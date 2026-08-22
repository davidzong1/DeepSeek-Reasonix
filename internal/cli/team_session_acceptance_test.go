package cli

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/team"
)

// TestTeamEnterSessionRefusedWithoutLeader pins the B2 third branch of the
// t gate (route §1.2, §5): a team with no leader at all refuses t on every
// member — the session window must never open — and renders the same
// explanation as the non-leader refusal.
func TestTeamEnterSessionRefusedWithoutLeader(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "coder-1", Role: team.RoleCoder, Status: team.MemberStatusActive},
		{MemberID: "coder-2", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	for range 2 { // every member is a non-leader; t must refuse on each
		m = teamKey(m, tea.KeyPressMsg{Code: 't'})
		if m.teamPick.session.active {
			t.Fatal("t without any leader must not open the session window")
		}
		got := ansi.Strip(m.renderTeamPicker())
		if !strings.Contains(got, "Only the leader can start a team session") {
			t.Fatalf("t without a leader should render the refusal, got:\n%s", got)
		}
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
}

// TestSessionSelectionFallsBackToLeaderAfterMemberRemoved pins the A7
// fallback (§4.2): a persisted selection naming a member that was deleted
// resumes the session on the first leader instead.
func TestSessionSelectionFallsBackToLeaderAfterMemberRemoved(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "coder-1", Role: team.RoleCoder, Status: team.MemberStatusActive},
		{MemberID: "leader-1", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // focus leader-1
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})         // enter the session
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // select coder-1, persisting it
	sel, err := m.teamPick.sessions.ReadSelection("Fixture Team")
	if err != nil || sel.MemberID != "coder-1" {
		t.Fatalf("selection = %+v, %v; want coder-1", sel, err)
	}

	// Delete coder-1 from the registry, as a member deletion would.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	st, err := team.NewTeamStore(cwd)
	if err != nil {
		t.Fatal(err)
	}
	doc, _, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	doc.Teams[0].Template = doc.Teams[0].Template[1:]
	if err := st.Save(doc); err != nil {
		t.Fatal(err)
	}

	m.onTeamButtonClick() // simulate a restart after the deletion
	if !m.teamPick.session.active || m.teamPick.session.current != "leader-1" {
		t.Fatalf("vanished selection must fall back to the leader, got active=%v current=%q",
			m.teamPick.session.active, m.teamPick.session.current)
	}
}

// TestSessionSelectionNoLeaderStaysOnRosterWithReason pins the A7 dead-end
// (§4.2): a selection naming a vanished member in a leaderless team must not
// reopen the session — the management page stays up and explains why.
func TestSessionSelectionNoLeaderStaysOnRosterWithReason(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "coder-1", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	ss, err := team.NewTeamSessionStore(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := ss.WriteSelection("Fixture Team", team.SessionSelection{MemberID: "ghost-1"}); err != nil {
		t.Fatal(err)
	}

	m := openTeamOverlay(t)
	if m.teamPick.session.active {
		t.Fatal("a selection with no resolvable leader must not reopen the session")
	}
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "No leader to resume the session on") {
		t.Fatalf("the roster should explain the blocked resume, got:\n%s", got)
	}
}
