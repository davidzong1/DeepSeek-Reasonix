package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/team"
	"reasonix/internal/team/tui"
)

func TestTeamPickerAddTeamPersistsAndFocusesIt(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	m := openTeamOverlay(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "New team name:") {
		t.Fatalf("a should open the add-team input, got:\n%s", got)
	}
	m = typeTeamName(m, "beta")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if name := m.teamPick.model.Name(); name != "beta" {
		t.Fatalf("focus should move onto the new team, got %q", name)
	}
	got = ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"Fixture Team", "beta"} {
		if !strings.Contains(got, want) {
			t.Fatalf("after add, the team list should show %q, got:\n%s", want, got)
		}
	}
	doc := readStoredTeamDoc(t)
	if len(doc.Teams) != 2 {
		t.Fatalf("document should hold 2 teams, got %d", len(doc.Teams))
	}
	if doc.Teams[0].Name != "Fixture Team" || doc.Teams[1].Name != "beta" {
		t.Fatalf("new team should be appended, got %+v", doc.Teams)
	}
}

// TestTeamPickerAddDuplicateTeamRefused pins the readable refusal instead of a
// raw error string.
func TestTeamPickerAddDuplicateTeamRefused(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Alpha"})
	before := storedTeamBytes(t)
	m := openTeamOverlay(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	m = typeTeamName(m, "Alpha")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "already exists") {
		t.Fatalf("duplicate team should explain the refusal, got:\n%s", got)
	}
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("refused duplicate add must not write team.json")
	}
}

func TestTeamPickerAddTeamEscCancelDoesNotWrite(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	before := storedTeamBytes(t)
	m := openTeamOverlay(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	m = typeTeamName(m, "beta")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})

	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Fixture Team") || strings.Contains(got, "New team name") {
		t.Fatalf("esc should cancel back to the team list, got:\n%s", got)
	}
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("cancelled add must not write team.json")
	}
}

func TestTeamPickerAddTeamEmptyNameIgnored(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team"})
	before := storedTeamBytes(t)
	m := openTeamOverlay(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "New team name:") {
		t.Fatalf("empty-name enter should stay in the input, got:\n%s", got)
	}
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("empty-name confirm must not write team.json")
	}
}

func TestTeamPickerDeleteTeamPersistsAndShowsNext(t *testing.T) {
	writeTeamFixture(t,
		team.Team{Name: "Alpha", Template: []team.MemberSlot{
			{MemberID: "a1", Role: team.RoleCoder, Status: team.MemberStatusActive},
		}},
		team.Team{Name: "Beta", Template: []team.MemberSlot{
			{MemberID: "b1", Role: team.RoleTester, Status: team.MemberStatusActive},
		}},
	)
	m := openTeamOverlay(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'd'})
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, `Delete team "Alpha"`) || !strings.Contains(got, "Enter delete") {
		t.Fatalf("d should confirm deleting the focused team, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	got = ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Beta") || strings.Contains(got, "Alpha") {
		t.Fatalf("after delete, the team list should show only Beta, got:\n%s", got)
	}
	doc := readStoredTeamDoc(t)
	if len(doc.Teams) != 1 || doc.Teams[0].Name != "Beta" {
		t.Fatalf("document should hold only Beta, got %+v", doc.Teams)
	}
}

// TestTeamPickerDeleteLastTeamRefused pins ErrLastTeam: deleting the final team
// is refused with a readable message and the registry stays untouched.
func TestTeamPickerDeleteLastTeamRefused(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Solo", Template: []team.MemberSlot{
		{MemberID: "a1", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	before := storedTeamBytes(t)
	m := openTeamOverlay(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'd'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Cannot delete the last team") {
		t.Fatalf("deleting the last team should explain the refusal, got:\n%s", got)
	}
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("refused last-team delete must not write team.json")
	}
	if doc := readStoredTeamDoc(t); len(doc.Teams) != 1 || doc.Teams[0].Name != "Solo" {
		t.Fatalf("registry should still hold Solo, got %+v", doc.Teams)
	}
}

func TestTeamPickerDeleteEscCancelDoesNotWrite(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	before := storedTeamBytes(t)
	m := openTeamOverlay(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'd'})
	m = teamKey(m, tea.KeyPressMsg{Code: 'q'})
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Fixture Team") || strings.Contains(got, "Delete team") {
		t.Fatalf("q should cancel the delete back to the team list, got:\n%s", got)
	}
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("cancelled delete must not write team.json")
	}
}

func TestTeamPickerAddMemberPersists(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "New member id:") {
		t.Fatalf("a in the roster should open the add-member input, got:\n%s", got)
	}
	m = typeTeamName(m, "beta")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	// The write reloads from disk; the roster must stay on screen.
	if mode := m.teamPick.model.Mode(); mode != tui.ModeList {
		t.Fatalf("adding a member must keep the roster on screen, got %q", mode)
	}
	got = ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"beta", "(active)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("after add, roster should show %q, got:\n%s", want, got)
		}
	}
	doc := readStoredTeamDoc(t)
	tpl := doc.Teams[0].Template
	if len(tpl) != 2 || tpl[1].MemberID != "beta" || tpl[1].Status != team.MemberStatusActive {
		t.Fatalf("template should hold alpha plus an active beta, got %+v", tpl)
	}
}

// TestTeamPickerAddMemberTargetsFocusedTeam pins that member writes land in the
// team the user opened, not always the first one in the registry.
func TestTeamPickerAddMemberTargetsFocusedTeam(t *testing.T) {
	writeTeamFixture(t,
		team.Team{Name: "Alpha", Template: []team.MemberSlot{
			{MemberID: "a1", Role: team.RoleCoder, Status: team.MemberStatusActive},
		}},
		team.Team{Name: "Beta", Template: []team.MemberSlot{
			{MemberID: "b1", Role: team.RoleTester, Status: team.MemberStatusActive},
		}},
	)
	m := openTeamOverlay(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // focus Beta
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open its roster
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	m = typeTeamName(m, "b2")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.teamPick.model.Name(); got != "Beta" {
		t.Fatalf("the write must keep Beta focused, got %q", got)
	}

	doc := readStoredTeamDoc(t)
	if len(doc.Teams[0].Template) != 1 {
		t.Fatalf("Alpha's roster must be untouched, got %+v", doc.Teams[0].Template)
	}
	tpl := doc.Teams[1].Template
	if len(tpl) != 2 || tpl[1].MemberID != "b2" {
		t.Fatalf("Beta's roster should hold b1 plus b2, got %+v", tpl)
	}
}

func TestTeamPickerAddMemberEscCancelDoesNotWrite(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	before := storedTeamBytes(t)
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	m = typeTeamName(m, "beta")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})

	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "alpha") || strings.Contains(got, "New member id") {
		t.Fatalf("esc should cancel back to the roster, got:\n%s", got)
	}
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("cancelled member add must not write team.json")
	}
}

func TestTeamPickerDeleteMemberPersists(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
		{MemberID: "omega", Role: team.RoleTester, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'd'})
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, `Delete member "alpha"`) || !strings.Contains(got, "Enter delete") {
		t.Fatalf("d in the roster should confirm deleting the focused member, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	got = ansi.Strip(m.renderTeamPicker())
	if strings.Contains(got, "alpha") || !strings.Contains(got, "omega") {
		t.Fatalf("after delete, roster should show only omega, got:\n%s", got)
	}
	doc := readStoredTeamDoc(t)
	tpl := doc.Teams[0].Template
	if len(tpl) != 1 || tpl[0].MemberID != "omega" {
		t.Fatalf("template should hold only omega, got %+v", tpl)
	}
}

// TestTeamPickerDeleteLastMemberFallsBackToRoster pins the graceful step-out:
// deleting the member whose context view is open lands on the empty roster,
// not on a dangling detail screen.
func TestTeamPickerDeleteLastMemberFallsBackToRoster(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // context view
	m = teamKey(m, tea.KeyPressMsg{Code: 'd'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if mode := m.teamPick.model.Mode(); mode != tui.ModeList {
		t.Fatalf("deleting the viewed member should drop to the roster, got %q", mode)
	}
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "No team members yet") {
		t.Fatalf("empty roster should render its message, got:\n%s", got)
	}
	if doc := readStoredTeamDoc(t); len(doc.Teams[0].Template) != 0 {
		t.Fatalf("template should be empty, got %+v", doc.Teams[0].Template)
	}
}

func TestTeamPickerDeleteMemberEscCancelDoesNotWrite(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	before := storedTeamBytes(t)
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'd'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})

	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "alpha") || strings.Contains(got, "Delete member") {
		t.Fatalf("esc should cancel the member delete, got:\n%s", got)
	}
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("cancelled member delete must not write team.json")
	}
}

// TestTeamMemberEditStatusPersists pins the member editor's status field: e
// opens the editor, the picker cycles active → disabled, s publishes the draft
// to team.json. The s key no longer cycles status — it saves the editor.
func TestTeamMemberEditStatusPersists(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'})          // compact roster → member editor
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the status picker
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // active → disabled
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // confirm back to the list
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})          // save the draft
	if got := m.teamPick.memberEdit.kind; got != memberEditFieldList {
		t.Fatalf("s should return to the field list, kind = %v", got)
	}
	doc := readStoredTeamDoc(t)
	if st := doc.Teams[0].Template[0].Status; st != team.MemberStatusDisabled {
		t.Fatalf("s should persist the edited status, got %q", st)
	}
}

// TestTeamPickerStatusKeyIsMemberOnly keeps s from acting on the team list,
// where there is no lifecycle status to cycle.
func TestTeamPickerStatusKeyIsMemberOnly(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	before := storedTeamBytes(t)
	m := openTeamOverlay(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("s on the team list must not write team.json")
	}
	if got := m.teamPick.model.Mode(); got != tui.ModeTeams {
		t.Fatalf("s on the team list must not navigate, got %q", got)
	}
}

// TestTeamPickerFirstWriteMigratesLegacyTeamsJSON pins the one-way migration:
// the overlay reads a legacy teams.json, and its first mutation publishes the
// primary team.json (legacy content plus the change) while the legacy file
// stays as a read-only fallback.
func TestTeamPickerFirstWriteMigratesLegacyTeamsJSON(t *testing.T) {
	writeLegacyTeamFixture(t, team.Team{
		Name: "Legacy Team",
		Template: []team.MemberSlot{
			{MemberID: "old", Role: team.RoleCoder, Status: team.MemberStatusActive},
		},
	})
	m := openTeamOverlay(t)
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Legacy Team") {
		t.Fatalf("legacy registry should render its team, got:\n%s", got)
	}

	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	m = typeTeamName(m, "newbie")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "newbie") {
		t.Fatalf("the migrated roster should show the new member, got:\n%s", got)
	}

	doc := readStoredTeamDoc(t)
	if len(doc.Teams) != 1 || doc.Teams[0].Name != "Legacy Team" {
		t.Fatalf("primary should hold the migrated team, got %+v", doc.Teams)
	}
	tpl := doc.Teams[0].Template
	if len(tpl) != 2 || tpl[0].MemberID != "old" || tpl[1].MemberID != "newbie" {
		t.Fatalf("primary template should be old + newbie, got %+v", tpl)
	}
	if tpl[1].Status != team.MemberStatusActive {
		t.Fatalf("new member should join active, got %q", tpl[1].Status)
	}
	if _, err := os.Stat(primaryTeamPath()); err != nil {
		t.Fatalf("primary team.json should exist after the first write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(".reasonix", "team", team.TeamsLegacyFile)); err != nil {
		t.Fatalf("legacy teams.json should remain as fallback: %v", err)
	}
}

// TestTeamCompactRosterKeysRouteToDetail pins the compact-roster key surface:
// b (bind) and s (status) are inert on the roster — its keys are a/d/p/t/e —
// while e descends into the member detail, where b arms the bind cycle.
func TestTeamCompactRosterKeysRouteToDetail(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'b'}) // inert on the compact roster
	if got := m.teamPick.kind; got != teamInputNone {
		t.Fatalf("b on the compact roster must not arm the bind cycle, got %v", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 's'}) // inert on the compact roster
	if doc := readStoredTeamDoc(t); doc.Teams[0].Template[0].Status != team.MemberStatusActive {
		t.Fatal("s on the compact roster must not cycle status")
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'})
	if mode := m.teamPick.model.Mode(); mode != tui.ModeContext {
		t.Fatalf("e should open the member detail, got %q", mode)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 'b'}) // detail-owned key
	if got := m.teamPick.kind; got != teamInputBind {
		t.Fatalf("b on the detail screen should arm the bind cycle, got %v", got)
	}
}
