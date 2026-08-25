package cli

// Option-list host integration tests (route R9): the member editor and pool
// provider pickers routed through optionList — letters and wheel events stay
// in the active list, esc cancels zero-write, commits land in the draft.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/team"
)

// TestMemberPickerLettersInertAndComposerClean pins the host routing boundary:
// while a field's option list is open, letters — including the save key s and
// the session key t — are consumed by the picker and never reach the composer,
// and the persisted document stays byte-identical.
func TestMemberPickerLettersInertAndComposerClean(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	before := storedTeamBytes(t)
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // member editor
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the status picker
	if got := m.teamPick.memberEdit.list.cursor; got != 0 {
		t.Fatalf("the status picker should open on the persisted value, got %d", got)
	}
	for _, r := range "xst" {
		m = teamKey(m, tea.KeyPressMsg{Code: r})
	}
	if got := m.teamPick.memberEdit.list.cursor; got != 0 {
		t.Fatalf("letters must not move the picker, got %d", got)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("letters leaked into the composer: %q", got)
	}
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("letters in the picker must not write team.json")
	}
}

// TestMemberPickerWheelMovesCursorAndCommits pins the wheel seam: a wheel
// event while the status picker is open moves the cursor exactly like the
// down key, and the committed row lands in the draft on s.
func TestMemberPickerWheelMovesCursorAndCommits(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // member editor
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the status picker

	next, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = next.(chatTUI)
	if got := m.teamPick.memberEdit.list.cursor; got != 1 {
		t.Fatalf("wheel down should move the status picker, got %d", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // commit the row
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})          // publish the draft
	doc := readStoredTeamDoc(t)
	if st := doc.Teams[0].Template[0].Status; st != team.MemberStatusDisabled {
		t.Fatalf("s should persist the wheel-chosen status, got %q", st)
	}
}

// TestMemberPickerEscCancelsZeroWrite pins the integrated cancel path: moving
// the picker and cancelling with esc leaves the editor on the field list and
// the document byte-identical — a cancelled wheel or key move never writes.
func TestMemberPickerEscCancelsZeroWrite(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	before := storedTeamBytes(t)
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // member editor
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the status picker
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // active → disabled
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})   // cancel back to the field list
	if got := m.teamPick.memberEdit.kind; got != memberEditFieldList {
		t.Fatalf("esc should return to the field list, got %v", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 's'}) // save the untouched draft
	doc := readStoredTeamDoc(t)
	if st := doc.Teams[0].Template[0].Status; st != team.MemberStatusActive {
		t.Fatalf("a cancelled pick must not change status, got %q", st)
	}
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("a cancelled pick must not write team.json")
	}
}

// TestMemberPickerCommitPersistsArchived pins the committed pick reaching the
// document: the picker walks to the archived row, enter confirms it, s
// persists — the full field path from pick to publish.
func TestMemberPickerCommitPersistsArchived(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // member editor
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the status picker
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // active → disabled
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // disabled → archived
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // commit
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})
	doc := readStoredTeamDoc(t)
	if st := doc.Teams[0].Template[0].Status; st != team.MemberStatusArchived {
		t.Fatalf("s should persist the archived status, got %q", st)
	}
}

// TestPoolProviderWheelMovesCursorAndCommits pins the pool side of the same
// seam: a wheel event inside the provider picker moves the cursor, and the
// committed provider replaces the stored one on s.
func TestPoolProviderWheelMovesCursorAndCommits(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{
		UserID: "au-1", Identity: "alice", Provider: "openai", Model: "gpt-5",
	})
	m := openPool(t)
	m = poolEditOpenProvider(m, team.AgentUser{UserID: "au-1", Identity: "alice", Provider: "openai", Model: "gpt-5"})

	next, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = next.(chatTUI)
	if got := m.teamPick.pool.list.cursor; got != 3 {
		t.Fatalf("wheel down from openai should land on deepseek, got %d", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	teamKey(m, tea.KeyPressMsg{Code: 's'})
	users := readStoredPool(t)
	if users[0].Provider != "deepseek" {
		t.Fatalf("s should persist the wheel-chosen provider, got %q", users[0].Provider)
	}
}

// TestMemberPickerRendersBorderedPopup pins the hosted render: the open field
// draws the popup's border and help line inside the overlay, so the picker is
// visible as a popup, not a bare row list.
func TestMemberPickerRendersBorderedPopup(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // member editor
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the status picker
	got := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"┌", "┐", "active", "disabled", "Enter confirm"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the picker should render its popup chrome, missing %q in:\n%s", want, got)
		}
	}
}
