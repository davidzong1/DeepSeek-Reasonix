package cli

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/team"
)

// writeTeamPoolFixture seeds both documents — team.json and agent_users.json —
// under one temp root and chdirs into it, so the overlay reads and writes the
// pair as one project.
func writeTeamPoolFixture(t *testing.T, teams []team.Team, users []team.AgentUser) {
	t.Helper()
	root := t.TempDir()
	store, err := team.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	teamDoc := team.TeamDoc{
		Document: team.Document{SchemaVersion: team.SchemaVersion},
		Teams:    teams,
	}
	if err := store.Save(filepath.Join(".reasonix", "team", team.TeamFile), &teamDoc); err != nil {
		t.Fatal(err)
	}
	poolDoc := team.AgentUsersDoc{
		Document:   team.Document{SchemaVersion: team.SchemaVersion},
		AgentUsers: users,
	}
	if err := store.Save(filepath.Join(".reasonix", "team", team.AgentUsersFile), &poolDoc); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
}

// fixtureTeam returns one team with a single coder member for config tests.
func fixtureTeam() team.Team {
	return team.Team{Name: "alpha", Template: []team.MemberSlot{
		{MemberID: "alice", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}}
}

// fixtureUser returns one pool entry that passes the domain validation
// (identity is required) for config tests.
func fixtureUser() team.AgentUser {
	return team.AgentUser{UserID: "au-1", Identity: "alice", Provider: "anthropic"}
}

// TestTeamBindCycleBindsAndUnbinds walks the bind cycle: b opens the candidate
// list, up/down cycle it, enter binds and persists, esc unbinds back to the
// team default.
func TestTeamBindCycleBindsAndUnbinds(t *testing.T) {
	writeTeamPoolFixture(t, []team.Team{fixtureTeam()}, []team.AgentUser{
		fixtureUser(),
		{UserID: "au-2", Identity: "bob", Provider: "openai"},
	})
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // b is a detail-screen key
	m = teamKey(m, tea.KeyPressMsg{Code: 'b'})
	got := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{`Bind "alice" to:`, "au-1", "au-2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("bind cycle should show %q, got:\n%s", want, got)
		}
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	doc := readStoredTeamDoc(t)
	if ref := doc.Teams[0].Template[0].AgentUserRef; ref != "au-1" {
		t.Fatalf("enter should bind alice to au-1, got %q", ref)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 'b'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	doc = readStoredTeamDoc(t)
	if ref := doc.Teams[0].Template[0].AgentUserRef; ref != "au-2" {
		t.Fatalf("down then enter should bind au-2, got %q", ref)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 'b'})
	teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	doc = readStoredTeamDoc(t)
	if ref := doc.Teams[0].Template[0].AgentUserRef; ref != "" {
		t.Fatalf("esc while bound should unbind, got %q", ref)
	}
}

// TestTeamBindEmptyPoolShowsHint pins the empty-pool path inside the cycle.
func TestTeamBindEmptyPoolShowsHint(t *testing.T) {
	writeTeamPoolFixture(t, []team.Team{fixtureTeam()}, nil)
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // b is a detail-screen key
	m = teamKey(m, tea.KeyPressMsg{Code: 'b'})
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "no agent users yet") {
		t.Fatalf("empty pool should render its hint, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.teamPick == nil {
		t.Fatal("esc with nothing bound should cancel, not close")
	}
}

func TestTeamDefaultAgentPickerAndSessionGate(t *testing.T) {
	teamFixture := fixtureTeam()
	teamFixture.Template[0].Leader = true
	writeTeamPoolFixture(t, []team.Team{teamFixture}, []team.AgentUser{
		{UserID: "au-1", Identity: "one", Provider: "anthropic", APIKey: "key"},
	})
	m := openTeamOverlay(t)
	if m.teamPick.session.active {
		t.Fatal("session must not start without a team default agent user")
	}
	if !strings.Contains(ansi.Strip(m.renderTeamPicker()), "default agent user") {
		t.Fatalf("missing default-agent refusal: %s", ansi.Strip(m.renderTeamPicker()))
	}
	// Descend into the roster and choose the team default with g.
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = teamKey(m, tea.KeyPressMsg{Code: 'g'})
	if !strings.Contains(ansi.Strip(m.renderTeamPicker()), "Team default agent user") {
		t.Fatalf("g should open the default picker: %s", ansi.Strip(m.renderTeamPicker()))
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := readStoredTeamDoc(t).Teams[0].DefaultAgentUserRef; got != "au-1" {
		t.Fatalf("default agent ref = %q, want au-1", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	if !m.teamPick.session.active {
		t.Fatalf("session should start after configuring the team default (mode=%q refusal=%q err=%q)", m.teamPick.model.Mode(), m.teamPick.refusal, m.teamPick.errMsg)
	}
}

// TestTeamCycleProxyOverride walks the proxy override cycle and pins every
// state on disk: inherit → force-on → force-off → inherit.
func TestTeamCycleProxyOverride(t *testing.T) {
	writeTeamPoolFixture(t, []team.Team{fixtureTeam()}, nil)
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'p'})
	if got := readStoredTeamDoc(t).Teams[0].Template[0].ProxyEnabled; got == nil || !*got {
		t.Fatalf("first p should force the proxy on, got %v", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 'p'})
	if got := readStoredTeamDoc(t).Teams[0].Template[0].ProxyEnabled; got == nil || *got {
		t.Fatalf("second p should force the proxy off, got %v", got)
	}
	teamKey(m, tea.KeyPressMsg{Code: 'p'})
	if got := readStoredTeamDoc(t).Teams[0].Template[0].ProxyEnabled; got != nil {
		t.Fatalf("third p should restore inheritance, got %v", got)
	}
}

// TestTeamLeaderModeGatesMemberAdd pins the policy gate: with leader mode on,
// adding a member is refused with the readable message; toggled off, the same
// add persists.
func TestTeamLeaderModeGatesMemberAdd(t *testing.T) {
	writeTeamPoolFixture(t, []team.Team{fixtureTeam()}, nil)
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // l is a detail-screen key
	m = teamKey(m, tea.KeyPressMsg{Code: 'l'})
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	for _, r := range "bob" {
		m = teamKey(m, tea.KeyPressMsg{Code: r})
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Leader-only operation") {
		t.Fatalf("leader mode should refuse member add, got:\n%s", got)
	}
	if doc := readStoredTeamDoc(t); len(doc.Teams[0].Template) != 1 {
		t.Fatalf("refused add must not persist, got %d members", len(doc.Teams[0].Template))
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 'l'})
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	for _, r := range "bob" {
		m = teamKey(m, tea.KeyPressMsg{Code: r})
	}
	teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	doc := readStoredTeamDoc(t)
	if len(doc.Teams[0].Template) != 2 {
		t.Fatalf("leader mode off should allow the add, got %d members", len(doc.Teams[0].Template))
	}
}

// TestTeamPoolDetailShowsBoundMembers pins the pool detail: enter opens it,
// the bound team/member pair renders, and the api key shows in plaintext per
// the user contract (editor and detail both show it; K2/K3 cover logs only).
func TestTeamPoolDetailShowsBoundMembers(t *testing.T) {
	u := fixtureUser()
	u.APIKey = "sk-ant-1234567890"
	bound := fixtureTeam()
	bound.Template[0].AgentUserRef = "au-1"
	writeTeamPoolFixture(t, []team.Team{bound}, []team.AgentUser{u})
	m := openTeamOverlay(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'u'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	got := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"au-1", "anthropic", "Bound by:", "alpha/alice", "sk-ant-1234567890"} {
		if !strings.Contains(got, want) {
			t.Fatalf("detail should show %q, got:\n%s", want, got)
		}
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "Agent users") {
		t.Fatalf("esc from detail should return to the pool list, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := ansi.Strip(m.renderTeamPicker()); strings.Contains(got, "Agent users") {
		t.Fatalf("esc from the pool list should return to the team list, got:\n%s", got)
	}
}

// TestTeamPoolEditFieldPersists walks the entry field editor: e opens the
// field list on the first missing field (base URL here), up moves onto
// provider, enter opens the picker preselected on the current canonical value,
// down steps onto the next option, enter confirms back to the list, s saves
// and persists; untouched fields survive.
func TestTeamPoolEditFieldPersists(t *testing.T) {
	u := fixtureUser()
	u.Model = "claude-opus-5"
	writeTeamPoolFixture(t, nil, []team.AgentUser{u})
	m := openTeamOverlay(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'u'})
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyUp})    // base URL → provider
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the picker, preselected on anthropic
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // anthropic → openai
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // confirm back to the list
	teamKey(m, tea.KeyPressMsg{Code: 's'})              // save the draft
	doc := readStoredPool(t)
	if doc[0].Provider != "openai" {
		t.Fatalf("s should persist the edited provider, got %q", doc[0].Provider)
	}
	if doc[0].Model != "claude-opus-5" {
		t.Fatalf("untouched fields must survive the edit, got %+v", doc[0])
	}
}
