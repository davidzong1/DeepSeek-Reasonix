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
// pair as one project. The user-global default team root is pinned to the
// fixture root's .reasonix (see writeTeamDoc).
func writeTeamPoolFixture(t *testing.T, teams []team.Team, users []team.AgentUser) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("REASONIX_STATE_HOME", filepath.Join(root, ".reasonix"))
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

func TestTeamSessionGateAndRosterPoolConfig(t *testing.T) {
	teamFixture := fixtureTeam()
	teamFixture.Template[0].Leader = true
	writeTeamPoolFixture(t, []team.Team{teamFixture}, []team.AgentUser{
		{UserID: "au-1", Identity: "one", Provider: "anthropic", APIKey: "key"},
	})
	m := openTeamOverlay(t)
	if m.teamPick.session.active {
		t.Fatal("session must not start without a team agent pool")
	}
	if !strings.Contains(ansi.Strip(m.renderTeamPicker()), "press u") {
		t.Fatalf("empty pool should gate with the press-u refusal: %s", ansi.Strip(m.renderTeamPicker()))
	}
	// Descend into the roster and configure the team pool with the roster u key:
	// toggle the single candidate on, save, and the session gate opens.
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = teamKey(m, tea.KeyPressMsg{Code: 'u'})
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "Team agent pool: alpha") {
		t.Fatalf("u should open the team-pool editor, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: ' '}) // select au-1
	m = teamKey(m, tea.KeyPressMsg{Code: 's'}) // save the pool
	doc := readStoredTeamDoc(t)
	if got := doc.Teams[0].AgentUserPool; len(got) != 1 || got[0] != "au-1" {
		t.Fatalf("pool after save = %v, want [au-1]", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	if !m.teamPick.session.active {
		t.Fatalf("session should start after configuring the pool (mode=%q refusal=%q err=%q)", m.teamPick.model.Mode(), m.teamPick.refusal, m.teamPick.errMsg)
	}
}

// TestTeamProxyEditorOpensFromRoster pins that the roster p key opens the team
// proxy settings editor seeded with the team default (off, default address),
// that s saves the enabled flag plus address through SetTeamProxy, and that
// Esc cancels with zero writes. The member-level override cycle is gone: a
// member's inherit/on/off lives in the member editor's proxy field.
func TestTeamProxyEditorOpensFromRoster(t *testing.T) {
	writeTeamPoolFixture(t, []team.Team{fixtureTeam()}, nil)
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'p'})
	got := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"Members use proxy", "Address", "off", team.DefaultProxyAddress} {
		if !strings.Contains(got, want) {
			t.Fatalf("p should open the team proxy editor showing %q, got:\n%s", want, got)
		}
	}
	// Esc cancels with zero writes.
	teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if p := readStoredTeamDoc(t).Teams[0].Proxy; p != nil {
		t.Fatalf("Esc must not write a team proxy, got %+v", p)
	}
}

// TestTeamProxyEditorSavesEnabled pins the editor's save path: open the enabled
// field, pick on, confirm, and s publishes the team default proxy through
// SetTeamProxy, persisting the normalized address.
func TestTeamProxyEditorSavesEnabled(t *testing.T) {
	writeTeamPoolFixture(t, []team.Team{fixtureTeam()}, nil)
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'p'})
	// The enabled field is the default cursor row; enter opens its on/off picker
	// seeded on the current value (off), so up moves to on.
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyUp})    // off -> on
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // confirm the pick
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})          // save
	p := readStoredTeamDoc(t).Teams[0].Proxy
	if p == nil || !p.Enabled || p.Address != team.DefaultProxyAddress {
		t.Fatalf("s should enable the team default proxy, got %+v", p)
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
