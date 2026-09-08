package cli

import (
	"os"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/team"
)

// poolRosterUsers returns three registry entries for the roster pool tests.
func poolRosterUsers() []team.AgentUser {
	return []team.AgentUser{
		{UserID: "au-1", Identity: "one", Provider: "anthropic"},
		{UserID: "au-2", Identity: "two", Provider: "openai"},
		{UserID: "au-3", Identity: "three", Provider: "deepseek"},
	}
}

// TestTeamPoolEditorSavePreservesClickOrder pins the ordered multi-select's core
// contract: the saved pool order is the order the user toggled entries on, never
// the registry order. Toggling au-2 before au-1 persists [au-2 au-1].
func TestTeamPoolEditorSavePreservesClickOrder(t *testing.T) {
	teamFixture := fixtureTeam()
	teamFixture.Template[0].Leader = true
	writeTeamPoolFixture(t, []team.Team{teamFixture}, poolRosterUsers())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'u'})
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "Team agent pool: alpha") {
		t.Fatalf("roster u should open the team-pool editor, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // au-1 → au-2
	m = teamKey(m, tea.KeyPressMsg{Code: ' '})         // first click: au-2
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyUp})   // au-2 → au-1
	m = teamKey(m, tea.KeyPressMsg{Code: ' '})         // second click: au-1
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})         // save
	doc := readStoredTeamDoc(t)
	if got := doc.Teams[0].AgentUserPool; !reflect.DeepEqual(got, []string{"au-2", "au-1"}) {
		t.Fatalf("saved pool = %v, want [au-2 au-1] (click order, not list order)", got)
	}
}

// TestTeamPoolEditorFoldsLegacyDefaultOnWrite pins the read-compat migration:
// opening the overlay and editor never rewrites team.json (the legacy default
// stays until an explicit pool write), and saving folds the legacy default into
// the pool field — the head is preserved, the legacy field clears.
func TestTeamPoolEditorFoldsLegacyDefaultOnWrite(t *testing.T) {
	teamFixture := fixtureTeam()
	teamFixture.Template[0].Leader = true
	teamFixture.DefaultAgentUserRef = "au-1" // legacy: a pool was never written
	writeTeamPoolFixture(t, []team.Team{teamFixture}, poolRosterUsers())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'u'})
	// A read of the roster and editor must not have rewritten team.json.
	raw, err := os.ReadFile(primaryTeamPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "agent_user_pool") {
		t.Fatalf("opening the editor must not write a pool field:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"DefaultAgentUserRef":"au-1"`) {
		t.Fatalf("the legacy default must survive a read untouched:\n%s", raw)
	}
	// The editor seeds the legacy default as the selection; add au-2 behind it.
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // au-1 → au-2
	m = teamKey(m, tea.KeyPressMsg{Code: ' '})         // select au-2 (click order tail)
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})         // save → fold
	doc := readStoredTeamDoc(t)
	team0 := doc.Teams[0]
	if !reflect.DeepEqual(team0.AgentUserPool, []string{"au-1", "au-2"}) {
		t.Fatalf("pool after fold = %v, want [au-1 au-2] (legacy default stays head)", team0.AgentUserPool)
	}
	if team0.DefaultAgentUserRef != "" {
		t.Fatalf("legacy default not folded away on the explicit pool write, got %q", team0.DefaultAgentUserRef)
	}
}

// TestTeamPoolEditorEmptySelectionGatesSession pins the empty-pool gate: saving
// an empty selection clears the team default, and starting a session is then
// refused with the press-u hint until a pool is configured again.
func TestTeamPoolEditorEmptySelectionGatesSession(t *testing.T) {
	teamFixture := fixtureTeam()
	teamFixture.Template[0].Leader = true
	teamFixture.AgentUserPool = []string{"au-1"}
	writeTeamPoolFixture(t, []team.Team{teamFixture}, poolRosterUsers())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'u'})
	m = teamKey(m, tea.KeyPressMsg{Code: ' '}) // au-1 selected → toggle off
	m = teamKey(m, tea.KeyPressMsg{Code: 's'}) // save an empty pool
	if doc := readStoredTeamDoc(t); len(doc.Teams[0].AgentUserPool) != 0 {
		t.Fatalf("saving an empty selection must clear the pool, got %v", doc.Teams[0].AgentUserPool)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	if m.teamPick.session.active {
		t.Fatal("a team with no pool must not start a session")
	}
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "press u") {
		t.Fatalf("the empty-pool session gate should point at the u editor, got:\n%s", got)
	}
}

// TestTeamRosterGRestoresFocusedMemberToPoolHead pins the roster g key: the
// focused member's explicit override folds away (the pin clears, falling back
// to the pool head — the write-side retirement P3 chose over pinning the head
// again, same terminal binding) and only that member changes — a second
// member's override survives untouched.
func TestTeamRosterGRestoresFocusedMemberToPoolHead(t *testing.T) {
	teamFixture := team.Team{Name: "alpha", AgentUserPool: []string{"au-1", "au-2", "au-3"}, Template: []team.MemberSlot{
		{MemberID: "lead", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive, AgentUserRef: "au-2"},
		{MemberID: "bob", Role: team.RoleTester, Status: team.MemberStatusActive, AgentUserRef: "au-3"},
	}}
	writeTeamPoolFixture(t, []team.Team{teamFixture}, poolRosterUsers())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'g'})
	doc := readStoredTeamDoc(t)
	byID := map[string]string{}
	for _, slot := range doc.Teams[0].Template {
		byID[slot.MemberID] = slot.AgentUserRef
	}
	if byID["lead"] != "" {
		t.Fatalf("g should fold the pinned override away (cleared = pool head au-1), got %q", byID["lead"])
	}
	if byID["bob"] != "au-3" {
		t.Fatalf("g must not touch other members, bob = %q, want au-3", byID["bob"])
	}
}

// TestTeamRosterGEmptyPoolRefused pins the g refusal: with no pool configured,
// restore is refused and the focused member's override stays put — the u editor
// is the only way to give the team a default.
func TestTeamRosterGEmptyPoolRefused(t *testing.T) {
	teamFixture := team.Team{Name: "alpha", Template: []team.MemberSlot{
		{MemberID: "lead", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive, AgentUserRef: "au-1"},
	}}
	writeTeamPoolFixture(t, []team.Team{teamFixture}, poolRosterUsers())
	// Entry on a pool-less team is refused even for a pinned member — the g
	// refusal lives on the management page the [TEAM] click lands on.
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'g'})
	if doc := readStoredTeamDoc(t); doc.Teams[0].Template[0].AgentUserRef != "au-1" {
		t.Fatalf("g on an empty pool must not rewrite the override, got %q", doc.Teams[0].Template[0].AgentUserRef)
	}
	if !strings.Contains(ansi.Strip(m.renderTeamPicker()), "press u") {
		t.Fatalf("g on an empty pool should point at the u editor, got:\n%s", ansi.Strip(m.renderTeamPicker()))
	}
}

// memberEditPoolFixture returns a pool-backed leader team with three registry
// entries, ready for the member editor's pool row tests.
func memberEditPoolFixture() team.Team {
	return team.Team{Name: "alpha", AgentUserPool: []string{"au-1", "au-2", "au-3"}, Template: []team.MemberSlot{
		{MemberID: "lead", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive},
	}}
}

// openMemberPoolRow descends from the roster into the member editor's pool row:
// e opens the editor, four downs land on the pool field (the last row), Enter
// opens its mode picker.
func openMemberPoolRow(t *testing.T, fixture team.Team) chatTUI {
	t.Helper()
	writeTeamPoolFixture(t, []team.Team{fixture}, poolRosterUsers())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'}) // member editor
	for range 4 {
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the pool row's mode picker
	return m
}

// TestMemberEditPoolModeDefaultsToInherit pins the pool row's default state: an
// untouched member opens on inherit, the mode picker labels what inherit
// resolves to (the team pool head), and a plain save writes no pool fields.
func TestMemberEditPoolModeDefaultsToInherit(t *testing.T) {
	m := openMemberPoolRow(t, memberEditPoolFixture())
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Agent pool: inherit") {
		t.Fatalf("the pool row should read inherit by default, got:\n%s", got)
	}
	if !strings.Contains(got, "inherit — team pool head au-1") {
		t.Fatalf("the inherit option should label the inherited team head, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // confirm inherit
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})          // save
	raw, err := os.ReadFile(primaryTeamPath())
	if err != nil {
		t.Fatal(err)
	}
	// The slot fields marshal without tags, so only PoolMode/AgentUserPool are
	// member-level — the team pool field ("agent_user_pool") is legitimately there.
	if strings.Contains(string(raw), `"PoolMode"`) || strings.Contains(string(raw), `"AgentUserPool"`) {
		t.Fatalf("an inherit member's save must never write pool fields:\n%s", raw)
	}
}

// TestMemberEditPoolCustomPersistsClickOrder drives the two-stage pool editor:
// the custom commit descends into the ordered multi-select, the toggle order
// becomes the persisted pool order, and the pool row then reads custom.
func TestMemberEditPoolCustomPersistsClickOrder(t *testing.T) {
	m := openMemberPoolRow(t, memberEditPoolFixture())
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // inherit → custom
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // commit custom → ordered select
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "Space toggle in click order") {
		t.Fatalf("the custom commit should open the ordered multi-select, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // au-1 → au-2
	m = teamKey(m, tea.KeyPressMsg{Code: ' '})          // first click: au-2
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyUp})    // au-2 → au-1
	m = teamKey(m, tea.KeyPressMsg{Code: ' '})          // second click: au-1
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // merge into the draft
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})          // save the editor
	doc := readStoredTeamDoc(t)
	slot := doc.Teams[0].Template[0]
	if !slot.IsCustomPool() {
		t.Fatalf("mode after save = %q, want custom", slot.PoolMode)
	}
	if !reflect.DeepEqual(slot.AgentUserPool, []string{"au-2", "au-1"}) {
		t.Fatalf("custom pool = %v, want [au-2 au-1] (click order, not list order)", slot.AgentUserPool)
	}
}

// TestMemberEditPoolEscCancelsZeroWrite pins the editor's cancel discipline at
// every stage: esc on the mode picker and esc on the ordered select both leave
// the document byte-identical — nothing persists until s.
func TestMemberEditPoolEscCancelsZeroWrite(t *testing.T) {
	m := openMemberPoolRow(t, memberEditPoolFixture())
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc}) // cancel the mode picker
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})
	raw := func() string {
		b, _ := os.ReadFile(primaryTeamPath())
		return string(b)
	}
	before := raw()
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // reopen the mode picker
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // → custom
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // enter the ordered select
	m = teamKey(m, tea.KeyPressMsg{Code: ' '})          // toggle au-1 on
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})   // cancel the select → restore inherit
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "Agent pool: inherit") {
		t.Fatalf("cancel must restore the inherited mode, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 's'}) // save nothing
	if after := raw(); after != before {
		t.Fatalf("esc cancels must be zero writes:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestMemberEditPoolInheritBackKeepsEntries pins the lossless round trip: a
// custom member switching back to inherit clears the mode but keeps the entries
// field, so switching to custom again restores the same order.
func TestMemberEditPoolInheritBackKeepsEntries(t *testing.T) {
	fixture := memberEditPoolFixture()
	fixture.Template[0].PoolMode = team.MemberPoolCustom
	fixture.Template[0].AgentUserPool = []string{"au-2", "au-1"}
	m := openMemberPoolRow(t, fixture)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyUp})    // custom → inherit
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // confirm inherit
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})
	slot := readStoredTeamDoc(t).Teams[0].Template[0]
	if slot.IsCustomPool() {
		t.Fatalf("mode after switching back = %q, want empty (inherit)", slot.PoolMode)
	}
	if !reflect.DeepEqual(slot.AgentUserPool, []string{"au-2", "au-1"}) {
		t.Fatalf("inherit back must keep the entries for a lossless round trip, got %v", slot.AgentUserPool)
	}
}

// TestMemberEditPoolCustomEmptyRefused pins the empty-custom guard inside the
// editor: confirming an empty ordered select is refused with a hint (custom
// never silently degrades to inherit), and esc recovers the inherited default.
func TestMemberEditPoolCustomEmptyRefused(t *testing.T) {
	m := openMemberPoolRow(t, memberEditPoolFixture())
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // → custom
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // enter the ordered select
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // confirm with nothing toggled
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "at least one entry") {
		t.Fatalf("an empty custom pool must be refused in the select, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc}) // cancel back to inherit
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})
	raw, _ := os.ReadFile(primaryTeamPath())
	if strings.Contains(string(raw), `"PoolMode"`) {
		t.Fatalf("the refused custom edit must leave no pool field:\n%s", raw)
	}
}

// TestMemberEditPoolPinRefusedBeforeCustom pins the pin/custom conflict in the
// editor: a member with a legacy pin cannot switch to custom until the pin is
// folded (g or the Agent row), and the refusal leaves the document untouched.
func TestMemberEditPoolPinRefusedBeforeCustom(t *testing.T) {
	fixture := memberEditPoolFixture()
	fixture.Template[0].AgentUserRef = "au-3" // legacy pin
	m := openMemberPoolRow(t, fixture)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // → custom
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // ordered select
	m = teamKey(m, tea.KeyPressMsg{Code: ' '})          // toggle au-1
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // merge
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})          // save → store refusal
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "Unbind this member's pinned agent user first") {
		t.Fatalf("the pin refusal should name the unbind step, got:\n%s", got)
	}
	slot := readStoredTeamDoc(t).Teams[0].Template[0]
	if slot.IsCustomPool() || slot.AgentUserRef != "au-3" {
		t.Fatalf("the refusal must leave pin and mode untouched, slot %+v", slot)
	}
}

// TestMemberAgentRowBindRefusedOnCustomMember pins the roster b / agent-row
// refusal for a custom member: its pool head is already the binding, so a
// legacy pin write is refused in the editor (mirror of the pool-row pin
// refusal) and the custom pool stays untouched on disk.
func TestMemberAgentRowBindRefusedOnCustomMember(t *testing.T) {
	fixture := memberEditPoolFixture()
	fixture.Template[0].PoolMode = team.MemberPoolCustom
	fixture.Template[0].AgentUserPool = []string{"au-1", "au-2"}
	writeTeamPoolFixture(t, []team.Team{fixture}, poolRosterUsers())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'})          // member editor
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // status → proxy
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // proxy → agent
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the agent options
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // team default → au-1
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // draft: bind au-1
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})          // save → store refusal
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "Set this member's pool row to inherit first") {
		t.Fatalf("the agent-row bind on a custom member should refuse with the pool-row hint, got:\n%s", got)
	}
	slot := readStoredTeamDoc(t).Teams[0].Template[0]
	if !slot.IsCustomPool() || slot.AgentUserRef != "" || !reflect.DeepEqual(slot.AgentUserPool, []string{"au-1", "au-2"}) {
		t.Fatalf("the refusal must leave the custom pool untouched, slot %+v", slot)
	}
}
