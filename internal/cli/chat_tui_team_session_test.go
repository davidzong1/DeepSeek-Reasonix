package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/team"
)

// leaderTeam returns one team with a leader and a coder for session tests.
func leaderTeam() team.Team {
	return team.Team{Name: "alpha", Template: []team.MemberSlot{
		{MemberID: "lead", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive},
		{MemberID: "alice", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}}
}

// TestTeamSessionLeaderGate pins the t gate (§5): t from a non-leader member
// is refused with a message and never opens the session; t from the leader
// opens the session window on the leader.
func TestTeamSessionLeaderGate(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t) // the roster is id-sorted: alice first, lead second
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	if m.teamPick.session.active {
		t.Fatal("t from a non-leader must not open the session")
	}
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "Only the leader can start a team session") {
		t.Fatalf("non-leader t should render the refusal, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // focus lead
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	if !m.teamPick.session.active {
		t.Fatal("t from the leader should open the session")
	}
	if got := m.renderTeamPicker(); got != "" {
		t.Fatalf("a freshly opened session renders no panel, got:\n%s", got)
	}
	m.setSessionPanel(true)
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "lead") || !strings.Contains(got, "session") {
		t.Fatalf("the session window should show the leader and the session title, got:\n%s", got)
	}
}

// TestTeamSessionSwitchPersistsAndEsc pins the session window: the full roster
// renders beside the current member, down switches the displayed member and
// persists the selection, and esc returns to the roster.
func TestTeamSessionSwitchPersistsAndEsc(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // focus lead
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})         // open on the leader
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl})
	if got := m.teamPick.session.current; got != "alice" {
		t.Fatalf("ctrl+down should switch to alice, got %q", got)
	}
	sel, err := m.teamPick.sessions.ReadSelection("alpha")
	if err != nil || sel.MemberID != "alice" {
		t.Fatalf("down should persist the selection, got %+v, %v", sel, err)
	}
	// j is a printable character now — the composer owns letters. Wrapping uses
	// the reserved switch key.
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl})
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("ctrl+down should wrap to the leader, got %q", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.teamPick.session.active {
		t.Fatal("esc should close the session window")
	}
	if got := m.teamPick.model.Mode(); got != "list" {
		t.Fatalf("esc should return to the roster, got %q", got)
	}
}

// TestTeamSessionReopenLandsOnLeader pins the [TEAM] entry (§11.4): clicking
// the button again after a switch reopens the session on the team's leader —
// the entry state is leader-first, never the last selected member.
func TestTeamSessionReopenLandsOnLeader(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // focus lead
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // switch to alice
	m.onTeamButtonClick()                              // simulate a fresh [TEAM] click
	if !m.teamPick.session.active {
		t.Fatal("the click must reopen the session window")
	}
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("the click must reopen on the leader, got %q", got)
	}
}

// TestTeamRosterLeaderToggleAndSessionGate pins the roster's leader shortcut
// (§5): l flips the focused member's standalone leader through the store's CAS
// setter, and the t gate reads the same registry field — a just-assigned
// leader opens the session, a just-cleared one is refused.
func TestTeamRosterLeaderToggleAndSessionGate(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)                         // id-sorted: alice first, lead second
	m = teamKey(m, tea.KeyPressMsg{Code: 'l'}) // assign alice as leader
	doc := readStoredTeamDoc(t)
	if !doc.Teams[0].Template[1].Leader {
		t.Fatal("l should assign the focused member as leader")
	}
	if !doc.Teams[0].Template[0].Leader {
		t.Fatal("l must not touch other members")
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 't'}) // a leader can open the session
	if !m.teamPick.session.active {
		t.Fatal("t from a just-assigned leader should open the session")
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m = teamKey(m, tea.KeyPressMsg{Code: 'l'}) // clear it again
	doc = readStoredTeamDoc(t)
	if doc.Teams[0].Template[1].Leader {
		t.Fatal("l should clear the leader marker")
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	if m.teamPick.session.active {
		t.Fatal("t from a cleared member must not open the session")
	}
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "Only the leader can start a team session") {
		t.Fatalf("cleared leader t should render the refusal, got:\n%s", got)
	}
}

// TestTeamLeaderResetFlow walks the full k step-down: warn → exact id →
// directory list → clear. The leader flag lands false, the session selection
// drops, and the done stage renders before closing.
func TestTeamLeaderResetFlow(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // focus lead
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})         // enter the session first
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'}) // step down on the leader
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "step down") || !strings.Contains(got, "clears every member") {
		t.Fatalf("k should arm the warning stage, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // exact-id gate
	m = teamKey(m, tea.KeyPressMsg{Code: 'x'})          // wrong first try
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "does not match") {
		t.Fatalf("a wrong id should refuse stage 2, got:\n%s", got)
	}
	m = typeTeamName(m, "lead")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // directory-list confirm
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "directories under") {
		t.Fatalf("stage 3 should show the directory list, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // clear
	doc := readStoredTeamDoc(t)
	if doc.Teams[0].Template[0].Leader {
		t.Fatal("the clear should publish Leader=false")
	}
	sel, err := m.teamPick.sessions.ReadSelection("alpha")
	if err != nil || sel.MemberID != "" {
		t.Fatalf("the clear should drop the session selection, got %+v, %v", sel, err)
	}
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "stepped down") {
		t.Fatalf("the done stage should render, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // close the done stage
	if m.teamPick.reset.kind != leaderResetNone {
		t.Fatal("enter should close the done stage")
	}
}

// TestTeamLeaderResetEscCancelsZeroWrite pins the cancel path: esc at any
// stage returns to the idle overlay and team.json stays byte-identical.
func TestTeamLeaderResetEscCancelsZeroWrite(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	before := storedTeamBytes(t)
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // focus lead
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeTeamName(m, "lead")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.teamPick.reset.kind != leaderResetNone {
		t.Fatalf("esc should return to idle, got %v", m.teamPick.reset.kind)
	}
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("cancelled step-down must not write team.json")
	}
	sel, err := m.teamPick.sessions.ReadSelection("alpha")
	if err != nil || sel.MemberID != "" {
		t.Fatalf("cancelled step-down must not write a selection, got %+v, %v", sel, err)
	}
}

// TestTeamLeaderResetNonLeaderRefused pins the k gate: k from a non-leader
// member is refused and never arms a stage.
func TestTeamLeaderResetNonLeaderRefused(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t) // alice, the coder, is focused first (id-sorted)
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})
	if m.teamPick.reset.kind != leaderResetNone {
		t.Fatalf("k from a non-leader must not arm a stage, got %v", m.teamPick.reset.kind)
	}
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "Only the leader can step down") {
		t.Fatalf("non-leader k should render the refusal, got:\n%s", got)
	}
}

// TestTeamMemberEditRolePersistsAndClears pins the role field: free text
// types into the role row, s persists it, and an empty role clears back to
// unconfigured.
func TestTeamMemberEditRolePersistsAndClears(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the editor on alice
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the role field (prefilled "coder")
	for range "coder" {
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	for _, r := range "architect" {
		m = teamKey(m, tea.KeyPressMsg{Code: r})
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // confirm back to the list
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})          // save
	doc := readStoredTeamDoc(t)
	if got := doc.Teams[0].Template[1].Role; got != "architect" {
		t.Fatalf("s should persist the edited role, got %q", got)
	}
	// Clear it: open the role field, empty the prefilled value, save.
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	for range "architect" {
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})
	if got := m.teamPick.memberEdit.kind; got != memberEditFieldList {
		t.Fatalf("s should return to the field list, kind = %v", got)
	}
	doc = readStoredTeamDoc(t)
	if got := doc.Teams[0].Template[1].Role; got != "" {
		t.Fatalf("an empty role should clear, got %q", got)
	}
}

// TestTeamMemberEditEscZeroWrite pins the cancel path: esc from the editor
// discards the draft and team.json stays byte-identical.
func TestTeamMemberEditEscZeroWrite(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	before := storedTeamBytes(t)
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the editor
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the role field
	m = typeTeamName(m, "architect")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc}) // discard the field edit
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc}) // leave the editor
	if got := m.teamPick.model.Mode(); got != "list" {
		t.Fatalf("esc should return to the roster, got %q", got)
	}
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("cancelled member edit must not write team.json")
	}
}
