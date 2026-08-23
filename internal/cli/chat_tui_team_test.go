package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/team"
	"reasonix/internal/team/tui"
)

// writeTeamDoc saves a registry document at rel through team.FileStore and
// chdirs into the fixture root, so the [ TEAM ] overlay loads real data.
func writeTeamDoc(t *testing.T, rel string, teams ...team.Team) {
	t.Helper()
	root := t.TempDir()
	store, err := team.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	doc := team.TeamDoc{
		Document: team.Document{SchemaVersion: team.SchemaVersion},
		Teams:    teams,
	}
	path := filepath.Join(".reasonix", "team", rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(path, &doc); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
}

// writeTeamFixture seeds the primary team.json.
func writeTeamFixture(t *testing.T, teams ...team.Team) {
	t.Helper()
	writeTeamDoc(t, team.TeamFile, teams...)
}

// writeLegacyTeamFixture seeds the legacy teams.json, leaving the primary
// team.json absent — the pre-rename layout the overlay migrates on first write.
func writeLegacyTeamFixture(t *testing.T, teams ...team.Team) {
	t.Helper()
	writeTeamDoc(t, team.TeamsLegacyFile, teams...)
}

// openTeamOverlayW builds a sized chat TUI and clicks [ TEAM ] open.
func openTeamOverlayW(t *testing.T, width int) chatTUI {
	t.Helper()
	ctrl := control.New(control.Options{})
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), width)
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
	m = next.(chatTUI)
	m.onTeamButtonClick()
	return m
}

// openTeamOverlay opens the overlay at the default width.
func openTeamOverlay(t *testing.T) chatTUI {
	t.Helper()
	return openTeamOverlayW(t, 80)
}

// openRosterW opens the overlay at the given width and descends into the
// first team's roster. Since the [TEAM] click lands on the leader's session
// window (§11.4), a leader-backed fixture first escs the session back to the
// team list, then enters the roster.
func openRosterW(t *testing.T, width int) chatTUI {
	t.Helper()
	m := openTeamOverlayW(t, width)
	if m.teamPick.session.active {
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc}) // close the session back to the team list
	}
	return teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
}

// openRoster opens the overlay and descends into the first team's roster.
func openRoster(t *testing.T) chatTUI {
	t.Helper()
	return openRosterW(t, 80)
}

// teamKey routes one keypress through the team overlay handler.
func teamKey(m chatTUI, msg tea.KeyPressMsg) chatTUI {
	next, _ := m.handleTeamPickerKey(msg)
	return next.(chatTUI)
}

// typeTeamName feeds one keypress per rune of s into the add-team or
// add-member input.
func typeTeamName(m chatTUI, s string) chatTUI {
	for _, r := range s {
		m = teamKey(m, tea.KeyPressMsg{Code: r})
	}
	return m
}

// primaryTeamPath is the primary registry path the overlay reads and writes.
func primaryTeamPath() string {
	return filepath.Join(".reasonix", "team", team.TeamFile)
}

// readStoredTeamDoc loads the document the overlay writes, from the cwd the
// fixture chdir'ed into (write-then-read-back, §8.3).
func readStoredTeamDoc(t *testing.T) team.TeamDoc {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	store, err := team.NewTeamStore(cwd)
	if err != nil {
		t.Fatal(err)
	}
	doc, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// storedTeamBytes is the raw team.json content, for cancel-does-not-write
// assertions (an unchanged file proves no publish happened).
func storedTeamBytes(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(primaryTeamPath())
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestTeamButtonRendersAndHasMouseHitBox(t *testing.T) {
	ctrl := control.New(control.Options{})
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)

	lines := strings.Split(m.View().Content, "\n")
	buttonX, buttonY := -1, -1
	for y, styled := range lines {
		line := ansi.Strip(styled)
		if before, _, found := strings.Cut(line, teamButtonText); found {
			buttonX = visibleWidth(before)
			buttonY = y
			break
		}
	}
	if buttonX < 0 || buttonY < 0 {
		t.Fatalf("team button %q is missing from the TUI frame", teamButtonText)
	}
	if !m.teamButtonHit(buttonX, buttonY) {
		t.Fatal("left edge of the rendered team button should be clickable")
	}
	if m.teamButtonHit(buttonX-1, buttonY) {
		t.Fatal("cell before the rendered team button should not be clickable")
	}
	if m.teamButtonHit(buttonX+visibleWidth(teamButtonText), buttonY) {
		t.Fatal("cell after the rendered team button should not be clickable")
	}

	updated, cmd := m.Update(tea.MouseClickMsg{X: buttonX, Y: buttonY, Button: tea.MouseLeft})
	clicked := updated.(chatTUI)
	if cmd != nil {
		t.Fatal("team callback should not schedule a command")
	}
	if clicked.teamPick == nil || clicked.teamPick.model == nil {
		t.Fatal("team button click should open the team view model")
	}
	if clicked.sel.active || clicked.validComposerSelection() {
		t.Fatal("team button click should not start transcript or composer selection")
	}
}

// TestTeamPickerOpensOnTeamList pins the entry screen: the click lands on the
// team list showing every registered team, not inside one team's roster.
func TestTeamPickerOpensOnTeamList(t *testing.T) {
	writeTeamFixture(t,
		team.Team{Name: "Alpha", Template: []team.MemberSlot{
			{MemberID: "a1", Role: team.RoleCoder, Status: team.MemberStatusActive},
		}},
		team.Team{Name: "Beta"},
	)
	m := openTeamOverlay(t)
	if got := m.teamPick.model.Mode(); got != tui.ModeTeams {
		t.Fatalf("overlay should open on the team list, got %q", got)
	}
	got := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"Teams", "Alpha", "Beta", "1 member", "0 members", "a add team", "d delete team"} {
		if !strings.Contains(got, want) {
			t.Fatalf("team list should show %q, got:\n%s", want, got)
		}
	}
	// Member rows belong to the roster screen, one level down.
	if strings.Contains(got, "a1") {
		t.Fatalf("team list must not list members, got:\n%s", got)
	}
}

// TestTeamPickerNavigatesTeamsAndDescends pins the three-level chain: pick a
// team, open its roster, open a member, and step back out the same way.
func TestTeamPickerNavigatesTeamsAndDescends(t *testing.T) {
	writeTeamFixture(t,
		team.Team{Name: "Alpha", Template: []team.MemberSlot{
			{MemberID: "a1", Role: team.RoleCoder, Status: team.MemberStatusActive},
		}},
		team.Team{Name: "Beta", Template: []team.MemberSlot{
			{MemberID: "b1", Role: team.RoleTester, Status: team.MemberStatusActive},
		}},
	)
	m := openTeamOverlay(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.teamPick.model.Name(); got != "Beta" {
		t.Fatalf("down should focus Beta, got %q", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.teamPick.model.Mode(); got != tui.ModeList {
		t.Fatalf("enter should open Beta's roster, got %q", got)
	}
	roster := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"Beta", "b1", "a add member", "d delete member"} {
		if !strings.Contains(roster, want) {
			t.Fatalf("roster should show %q, got:\n%s", want, roster)
		}
	}
	if strings.Contains(roster, "a1") {
		t.Fatalf("Beta's roster must not show Alpha's member, got:\n%s", roster)
	}

	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.teamPick.model.Mode(); got != tui.ModeContext {
		t.Fatalf("enter should open the member context view, got %q", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := m.teamPick.model.Mode(); got != tui.ModeList {
		t.Fatalf("esc should return to the roster, got %q", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := m.teamPick.model.Mode(); got != tui.ModeTeams {
		t.Fatalf("esc should return to the team list, got %q", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.teamPick != nil {
		t.Fatal("esc from the team list should close the overlay")
	}
}

// TestTeamPickerQuitChain pins q/ctrl+c exit semantics from the team list.
func TestTeamPickerQuitChain(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Alpha"})
	m := openTeamOverlay(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'q'})
	if m.teamPick == nil || m.teamPick.model.Mode() != tui.ModeQuit {
		t.Fatal("q should enter the quit confirmation")
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := m.teamPick.model.Mode(); got != tui.ModeTeams {
		t.Fatalf("esc should cancel the quit confirmation, got %q", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 'q'})
	m = teamKey(m, tea.KeyPressMsg{Code: 'q'})
	if m.teamPick != nil {
		t.Fatal("confirming the quit should close the team view")
	}

	m.onTeamButtonClick()
	m = teamKey(m, tea.KeyPressMsg{Code: 'q'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.teamPick != nil {
		t.Fatal("Enter should confirm the quit and close the team view")
	}

	m.onTeamButtonClick()
	m = teamKey(m, tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'c'})
	if m.teamPick == nil || m.teamPick.model.Mode() != tui.ModeQuit {
		t.Fatal("ctrl+c should enter the quit confirmation inside the team view")
	}
}

// TestTeamPickerRendersRealTeamDocMembers pins that the roster shows every
// template slot with its lifecycle status, and the context view its details —
// all from the document, never from invented example members.
func TestTeamPickerRendersRealTeamDocMembers(t *testing.T) {
	writeTeamFixture(t, team.Team{
		Name: "Fixture Team",
		Template: []team.MemberSlot{
			{MemberID: "alpha", Role: team.RoleCoder, AgentUserRef: "u-alice", Status: team.MemberStatusActive},
			{MemberID: "omega", Role: team.RoleTester, Status: team.MemberStatusActive},
			{MemberID: "retired", Role: team.RoleReviewer, Status: team.MemberStatusArchived},
		},
	})
	m := openRoster(t)
	list := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"Fixture Team", "alpha", "omega", "retired", "(coder · active)", "(tester · active)", "(reviewer · archived)"} {
		if !strings.Contains(list, want) {
			t.Fatalf("roster should show %q, got:\n%s", want, list)
		}
	}
	// The roster comes from the document: the old hardcoded example members
	// must not appear, and no row may claim the legacy role "leader" — the
	// marker renders only through the standalone leader property.
	for _, gone := range []string{"P4-cli-wiring", "internal/cli", "(leader"} {
		if strings.Contains(list, gone) {
			t.Fatalf("roster must not show %q, got:\n%s", gone, list)
		}
	}
	// The compact roster shows only id/role/leader/status — never the launch
	// configuration, which the detail screen owns.
	if strings.Contains(list, "Agent") || strings.Contains(list, "Proxy") {
		t.Fatalf("compact roster must not show launch configuration, got:\n%s", list)
	}

	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	ctx := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"alpha", "Role: coder", "Leader: off", "State: idle", "Status: active"} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("context view should show %q, got:\n%s", want, ctx)
		}
	}
	// The agent fields stay backend-only: the detail view shows no "Agent user"
	// label line (the binding still surfaces as an editor field).
	if strings.Contains(ctx, "Agent user") {
		t.Fatalf("context view must not show an agent-user label, got:\n%s", ctx)
	}
	if strings.Contains(ctx, "Task:") || strings.Contains(ctx, "Context:") {
		t.Fatalf("context view must not invent task/context pointers, got:\n%s", ctx)
	}

	m = teamKey(m, tea.KeyPressMsg{Code: 'q'})
	quit := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(quit, "Leave team view") {
		t.Fatalf("quit mode should render the confirmation prompt, got:\n%s", quit)
	}
}

// TestTeamCompactRosterHelpLine pins the compact roster's help surface: it
// advertises only its own keys (a/d/l/p/t/e) — Enter/Space and the editor-only
// keys (s/b) stay off it, and the session hint reads "🌟 t Enter_session"
// while the member editor line names the full surface.
func TestTeamCompactRosterHelpLine(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	list := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"a add member", "d delete member", "🌟 t Enter_session", "p proxy", "e edit", "l leader on/off"} {
		if !strings.Contains(list, want) {
			t.Fatalf("roster help should show %q, got:\n%s", want, list)
		}
	}
	for _, gone := range []string{"t session", "Enter/Space", "s save", "s status", "b bind", "l leader-mode"} {
		if strings.Contains(list, gone) {
			t.Fatalf("roster help must not show %q, got:\n%s", gone, list)
		}
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	ctx := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"s save", "🌟 t Enter_session", "b bind", "l leader-mode"} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("editor help should show %q, got:\n%s", want, ctx)
		}
	}
	for _, gone := range []string{"t session"} {
		if strings.Contains(ctx, gone) {
			t.Fatalf("editor help must not show %q, got:\n%s", gone, ctx)
		}
	}
}

// TestTeamRosterHelpWrapsAtEdge pins the adaptive help block (§5): wide enough
// the whole block renders on one line, narrow it word-wraps at the panel edge
// — the same keys either way, no hard-coded line split.
func TestTeamRosterHelpWrapsAtEdge(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	const help = "↑/↓ navigate · a add member · d delete member · 🌟 t Enter_session · p proxy · e edit · l leader on/off · Esc back · q quit"
	m := openRosterW(t, 160)
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, help) {
		t.Fatalf("a wide panel should keep the help on one line, got:\n%s", got)
	}
	m = openRosterW(t, 40)
	if got := ansi.Strip(m.renderTeamPicker()); strings.Contains(got, help) {
		t.Fatalf("a narrow panel should wrap the help at the edge, got:\n%s", got)
	}
}

// TestTeamPickerBootstrapsFirstTeam pins the fresh-project path end to end: no
// .reasonix/team at all still opens an empty team list whose a key creates the
// first team and lands it on disk. This is the dead end the old overlay had —
// an absent registry blocked every write behind an error message.
func TestTeamPickerBootstrapsFirstTeam(t *testing.T) {
	t.Chdir(t.TempDir()) // no .reasonix/team/team.json
	m := openTeamOverlay(t)
	if m.teamPick == nil || m.teamPick.model == nil {
		t.Fatal("team button click must still open the overlay")
	}
	if got := m.teamPick.errMsg; got != "" {
		t.Fatalf("an absent registry is empty, not an error, got %q", got)
	}
	got := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"No team yet", "a add team", "Esc close"} {
		if !strings.Contains(got, want) {
			t.Fatalf("empty registry should render %q, got:\n%s", want, got)
		}
	}

	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	if !strings.Contains(ansi.Strip(m.renderTeamPicker()), "New team name:") {
		t.Fatal("a must open the add-team input on an empty registry")
	}
	m = typeTeamName(m, "first")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	got = ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "first") {
		t.Fatalf("after bootstrap, the team list should show the new team, got:\n%s", got)
	}
	doc := readStoredTeamDoc(t)
	if len(doc.Teams) != 1 || doc.Teams[0].Name != "first" {
		t.Fatalf("bootstrap should persist exactly the new team, got %+v", doc.Teams)
	}
}

// TestTeamPickerEmptyRegistryShowsCreateHint pins that a team.json registering
// no team is the same recoverable empty state, not a dead end.
func TestTeamPickerEmptyRegistryShowsCreateHint(t *testing.T) {
	writeTeamFixture(t) // team.json registers no team
	m := openTeamOverlay(t)
	got := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"No team yet", "a add team"} {
		if !strings.Contains(got, want) {
			t.Fatalf("empty registry should render %q, got:\n%s", want, got)
		}
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	m = typeTeamName(m, "first")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "first") {
		t.Fatalf("after add, the team list should show the new team, got:\n%s", got)
	}
	if doc := readStoredTeamDoc(t); len(doc.Teams) != 1 || doc.Teams[0].Name != "first" {
		t.Fatalf("a on an empty registry should create the first team, got %+v", doc.Teams)
	}
}

// TestTeamPickerEmptyRosterShowsMessage pins a registered team with no members.
func TestTeamPickerEmptyRosterShowsMessage(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team"})
	m := openRoster(t)
	got := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"Fixture Team", "No team members yet", "a add member"} {
		if !strings.Contains(got, want) {
			t.Fatalf("empty roster should render %q, got:\n%s", want, got)
		}
	}
}

// TestTeamPickerCorruptFileShowsMessage pins that an unreadable registry is
// still an error state, distinct from an absent one, and blocks writes.
func TestTeamPickerCorruptFileShowsMessage(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".reasonix", "team", "team.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version": "not-an-int"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	m := openTeamOverlay(t)
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Team data unavailable") {
		t.Fatalf("corrupt team.json should render the error message, got:\n%s", got)
	}

	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	if strings.Contains(ansi.Strip(m.renderTeamPicker()), "New team name:") {
		t.Fatal("a must not arm a write over a corrupt registry")
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.teamPick != nil {
		t.Fatal("esc should close the error overlay")
	}
}

func TestTeamPickerSpaceEntersRosterThenContext(t *testing.T) {
	writeTeamFixture(t, team.Team{
		Name: "Fixture Team",
		Template: []team.MemberSlot{
			{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
		},
	})
	m := openTeamOverlay(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeySpace})
	if got := m.teamPick.model.Mode(); got != tui.ModeList {
		t.Fatalf("space should open the focused team's roster, got %q", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeySpace})
	if got := m.teamPick.model.Mode(); got != tui.ModeContext {
		t.Fatalf("space should open the focused member's context view, got %q", got)
	}
}
