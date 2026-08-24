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

// TestTeamButtonOpensOnLeaderAfterMemberRemoved pins the [TEAM] entry
// (§11.4): the click opens the session on the first leader even when the
// roster shrank since the last visit — the entry state is leader-first, never
// the last selected member, so a deleted member cannot strand the session.
func TestTeamButtonOpensOnLeaderAfterMemberRemoved(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "coder-1", Role: team.RoleCoder, Status: team.MemberStatusActive},
		{MemberID: "leader-1", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})                   // focus leader-1
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})                           // enter the session
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl}) // switch to coder-1, persisting it
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

	m.onTeamButtonClick() // simulate a fresh [TEAM] click
	if !m.teamPick.session.active || m.teamPick.session.current != "leader-1" {
		t.Fatalf("the click must open on the leader, got active=%v current=%q",
			m.teamPick.session.active, m.teamPick.session.current)
	}
}

// TestTeamButtonNoLeaderStaysOnManagementPage pins the [TEAM] entry dead-end
// (§11.4): a leaderless team does not open the session — the management page
// stays up, quiet, since the leader marker is the gate and the roster screen
// still offers l to assign one.
func TestTeamButtonNoLeaderStaysOnManagementPage(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "coder-1", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})

	m := openTeamOverlay(t)
	if m.teamPick.session.active {
		t.Fatal("a leaderless team must not open the session window")
	}
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "1 member") {
		t.Fatalf("the team list should stay up, got:\n%s", got)
	}
}
