package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/team"
)

// writeAgentUsersFixture seeds agent_users.json with pool entries and chdirs
// into the fixture root, so the pool screen loads real data.
func writeAgentUsersFixture(t *testing.T, users ...team.AgentUser) {
	t.Helper()
	root := t.TempDir()
	au, err := team.NewAgentUsersStore(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if err := au.AddAgentUser(u); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
}

// poolUsersPath is the pool document path the screen reads and writes.
func poolUsersPath() string {
	return filepath.Join(".reasonix", "team", team.AgentUsersFile)
}

// readStoredPool loads the pool the screen writes, from the cwd the fixture
// chdir'ed into (write-then-read-back, §8.3).
func readStoredPool(t *testing.T) []team.AgentUser {
	t.Helper()
	au, err := team.NewAgentUsersStore(mustGetwd(t))
	if err != nil {
		t.Fatal(err)
	}
	users, err := au.ListAgentUsers()
	if err != nil {
		t.Fatal(err)
	}
	return users
}

// openPool opens the team overlay and enters the pool screen with u.
func openPool(t *testing.T) chatTUI {
	t.Helper()
	m := openTeamOverlay(t)
	return teamKey(m, tea.KeyPressMsg{Code: 'u'})
}

// fillAddEditor drives the add editor through every field — the id row, the
// five configuration fields, the api key — and saves with s. Add tests reuse
// it instead of re-spelling the form; tests that vary one aspect (duplicate
// id, blank id) still hit the full-fill path before their assertion. The
// provider row is a picker, not a text field: it opens on "unconfigured" and
// one down steps onto Claude (Anthropic), matching the anthropic the other
// fields once typed.
func fillAddEditor(m chatTUI, id string) chatTUI {
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	m = teamKey(m, enter) // open the id row
	m = typeTeamName(m, id)
	m = teamKey(m, enter)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // id → identity
	m = teamKey(m, enter)
	m = typeTeamName(m, "acct@corp")
	m = teamKey(m, enter)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // identity → provider
	m = teamKey(m, enter)                              // open the picker on unconfigured
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // → Claude (Anthropic)
	m = teamKey(m, enter)
	for _, v := range []string{"https://api.anthropic.com", "sk-ant-fill-add", "claude-opus-5", "high"} {
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
		m = teamKey(m, enter)
		m = typeTeamName(m, v)
		m = teamKey(m, enter)
	}
	return teamKey(m, tea.KeyPressMsg{Code: 's'})
}

// TestTeamPoolOpensFromTeamListAndEscBack pins the entry/exit chain: u from
// the team list opens the pool screen showing every entry with its provider
// and model, and esc returns to the team list.
func TestTeamPoolOpensFromTeamListAndEscBack(t *testing.T) {
	writeAgentUsersFixture(t,
		team.AgentUser{UserID: "au-1", Provider: "anthropic", Model: "claude-opus-5"},
		team.AgentUser{UserID: "au-2", Provider: "openai", Model: "gpt-5"},
	)
	m := openTeamOverlay(t)
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "a add team") || !strings.Contains(got, "u agent users") {
		t.Fatalf("team list should render its keys before u, got:\n%s", got)
	}
	m = openPool(t)
	got := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"Agent users", "au-1", "anthropic / claude-opus-5", "au-2", "openai / gpt-5", "a add user", "d delete user"} {
		if !strings.Contains(got, want) {
			t.Fatalf("pool should show %q, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "a add team") {
		t.Fatalf("pool must not render team-list keys, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	got = ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Teams") || !strings.Contains(got, "a add team") {
		t.Fatalf("esc should return to the team list, got:\n%s", got)
	}
}

// TestTeamPoolFocusMovesWithJKPins up/down focus navigation and the focused
// row marker.
func TestTeamPoolFocusMovesWithJK(t *testing.T) {
	writeAgentUsersFixture(t,
		team.AgentUser{UserID: "au-1", Provider: "anthropic"},
		team.AgentUser{UserID: "au-2", Provider: "openai"},
		team.AgentUser{UserID: "au-3", Provider: "deepseek"},
	)
	m := openPool(t)
	first := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(first, "❯ 1. au-1") {
		t.Fatalf("pool should open focused on the first row, got:\n%s", first)
	}
	for range 2 {
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "au-3") {
		t.Fatalf("down twice should focus the third row, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})
	got = ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "au-2") {
		t.Fatalf("k should focus the second row, got:\n%s", got)
	}
}

// TestTeamPoolAddsUser pins the write path: a opens the field-list editor on
// an empty draft with the cursor on the missing id row; filling every field
// and pressing s publishes the entry to agent_users.json (write-then-read-back,
// §8.3), and the pool renders the new row.
func TestTeamPoolAddsUser(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{UserID: "au-1"})
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Adding agent user") || !strings.Contains(got, "> id:") {
		t.Fatalf("a must open the field-list editor on the missing id row, got:\n%s", got)
	}
	m = fillAddEditor(m, "au-2")
	got = ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "au-2") {
		t.Fatalf("after add the pool should show the new entry, got:\n%s", got)
	}
	users := readStoredPool(t)
	if len(users) != 2 || users[1].UserID != "au-2" {
		t.Fatalf("add should persist exactly the new entry, got %+v", users)
	}
	info, err := os.Stat(poolUsersPath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("agent_users.json perm = %o, want 600", perm)
	}
}

// TestTeamPoolAddDuplicateShowsError pins the store refusal rendered on the
// pool after the s save, with the file unchanged.
func TestTeamPoolAddDuplicateShowsError(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{UserID: "au-1"})
	before, err := os.ReadFile(poolUsersPath())
	if err != nil {
		t.Fatal(err)
	}
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	m = fillAddEditor(m, "au-1") // every field filled; the store refuses the id
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "already exists") {
		t.Fatalf("duplicate add should render the refusal, got:\n%s", got)
	}
	after, err := os.ReadFile(poolUsersPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a refused add must not touch agent_users.json")
	}
}

// TestTeamPoolDeletesUser pins the delete path: d confirms on the focused
// entry and the store re-read shows the removal.
func TestTeamPoolDeletesUser(t *testing.T) {
	writeAgentUsersFixture(t,
		team.AgentUser{UserID: "au-1"},
		team.AgentUser{UserID: "au-2"},
	)
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'd'})
	if !strings.Contains(ansi.Strip(m.renderTeamPicker()), `Delete agent user "au-1"`) {
		t.Fatal("d must arm the delete confirmation on the focused entry")
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	got := ansi.Strip(m.renderTeamPicker())
	if strings.Contains(got, "au-1") {
		t.Fatalf("after delete the pool should drop the entry, got:\n%s", got)
	}
	users := readStoredPool(t)
	if len(users) != 1 || users[0].UserID != "au-2" {
		t.Fatalf("delete should persist the removal, got %+v", users)
	}
}

// TestTeamPoolRefusesDeletingLastUser pins the ErrLastAgentUser refusal
// rendered on the pool, with the file unchanged, and that esc cancels the
// confirmation without writing.
func TestTeamPoolRefusesDeletingLastUser(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{UserID: "au-1"})
	before, err := os.ReadFile(poolUsersPath())
	if err != nil {
		t.Fatal(err)
	}
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'd'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Cannot delete the last agent user") {
		t.Fatalf("last-entry delete should render the refusal, got:\n%s", got)
	}
	after, err := os.ReadFile(poolUsersPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a refused delete must not touch agent_users.json")
	}
	// The error screen's esc closes the pool back onto the team list.
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "Teams") {
		t.Fatalf("esc from the refusal should close the pool, got:\n%s", got)
	}
}

// TestTeamPoolEscCancelsWriteStates pins that esc cancels the add editor and
// the delete confirmation without writing, and that q cancels a delete (it
// never accelerates one).
func TestTeamPoolEscCancelsWriteStates(t *testing.T) {
	writeAgentUsersFixture(t,
		team.AgentUser{UserID: "au-1"},
		team.AgentUser{UserID: "au-2"},
	)
	before, err := os.ReadFile(poolUsersPath())
	if err != nil {
		t.Fatal(err)
	}
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	m = typeTeamName(m, "au-3") // letters in the editor's list phase are inert
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "a add user") {
		t.Fatalf("esc should cancel the add editor back to the list, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 'd'})
	m = teamKey(m, tea.KeyPressMsg{Code: 'q'})
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "au-1") || strings.Contains(got, "Delete agent user") {
		t.Fatalf("q should cancel the delete confirmation, got:\n%s", got)
	}
	after, err := os.ReadFile(poolUsersPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("cancelled writes must not touch agent_users.json")
	}
}

// TestTeamPoolEmptyShowsCreateHint pins the fresh-project path: an absent
// agent_users.json opens an empty pool whose a key creates the first entry.
func TestTeamPoolEmptyShowsCreateHint(t *testing.T) {
	t.Chdir(t.TempDir()) // no .reasonix/team/agent_users.json
	m := openPool(t)
	got := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"No agent users yet", "a add user", "Esc back"} {
		if !strings.Contains(got, want) {
			t.Fatalf("empty pool should render %q, got:\n%s", want, got)
		}
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	m = fillAddEditor(m, "au-1")
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "au-1") {
		t.Fatalf("after bootstrap the pool should show the new entry, got:\n%s", got)
	}
	users := readStoredPool(t)
	if len(users) != 1 || users[0].UserID != "au-1" {
		t.Fatalf("bootstrap should persist exactly the new entry, got %+v", users)
	}
}

// TestTeamPoolCorruptFileShowsMessage pins that an unreadable pool is an
// error state, distinct from an absent one, and blocks writes: a cannot arm
// the editor over it, and esc closes the pool onto the team list.
func TestTeamPoolCorruptFileShowsMessage(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".reasonix", "team", team.AgentUsersFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version": "not-an-int"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	m := openPool(t)
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Agent data unavailable") {
		t.Fatalf("corrupt agent_users.json should render the error message, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	if strings.Contains(ansi.Strip(m.renderTeamPicker()), "Adding agent user") {
		t.Fatal("a must not arm a write over a corrupt pool")
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc}) // close the pool
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "Teams") {
		t.Fatalf("esc should return from the error pool to the team list, got:\n%s", got)
	}
}

// TestTeamPoolUnconfiguredRowLabels pins the display of an entry with neither
// provider nor model — the minimal a-create path renders "unconfigured".
func TestTeamPoolUnconfiguredRowLabels(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{UserID: "au-1"})
	m := openPool(t)
	got := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"au-1", "(unconfigured)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("pool should show %q, got:\n%s", want, got)
		}
	}
}

// TestTeamPoolAddPublishesAllFields pins the a→list→field→s flow: every field
// typed into the editor lands in the entry the pool reads back, the api key
// renders in plaintext while typing and persists raw at 0600 (§4.2, user
// contract), and s is the single write.
func TestTeamPoolAddPublishesAllFields(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{UserID: "au-1"})
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	// id row (first missing, focused on entry)
	m = teamKey(m, enter)
	m = typeTeamName(m, "au-2")
	m = teamKey(m, enter)
	// identity
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, enter)
	m = typeTeamName(m, "acct@corp")
	m = teamKey(m, enter)
	// provider — a picker, never a text field: unconfigured → Claude (Anthropic)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, enter)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // unconfigured → anthropic
	m = teamKey(m, enter)
	// base url
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, enter)
	m = typeTeamName(m, "https://api.anthropic.com")
	m = teamKey(m, enter)
	// api key — plaintext while typing (user contract)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // base url → api key
	m = teamKey(m, enter)
	m = typeTeamName(m, "sk-ant-editor-test")
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "sk-ant-editor-test") {
		t.Fatalf("the api key row must render in plaintext, got:\n%s", got)
	}
	if strings.Contains(got, "●") {
		t.Fatalf("the editor must not mask the key, got:\n%s", got)
	}
	m = teamKey(m, enter) // confirm into the draft
	// model
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // api key → model
	m = teamKey(m, enter)
	m = typeTeamName(m, "claude-opus-5")
	m = teamKey(m, enter)
	// effort
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // model → effort
	m = teamKey(m, enter)
	m = typeTeamName(m, "high")
	m = teamKey(m, enter)
	teamKey(m, tea.KeyPressMsg{Code: 's'})
	users := readStoredPool(t)
	if len(users) != 2 {
		t.Fatalf("s should persist exactly the new entry, got %+v", users)
	}
	u := users[1]
	if u.UserID != "au-2" || u.Identity != "acct@corp" || u.Provider != "anthropic" || u.BaseURL != "https://api.anthropic.com" ||
		u.Model != "claude-opus-5" || u.Effort != "high" || u.APIKey != "sk-ant-editor-test" {
		t.Fatalf("s should persist every editor field, got %+v", u)
	}
}

// TestTeamPoolAddEscCancels pins that esc from the add editor — after a field
// was typed into the draft — aborts without writing.
func TestTeamPoolAddEscCancels(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{UserID: "au-1"})
	before, err := os.ReadFile(poolUsersPath())
	if err != nil {
		t.Fatal(err)
	}
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the id row
	m = typeTeamName(m, "au-2")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // into the draft
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})   // abort the whole add
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "a add user") {
		t.Fatalf("esc should abort the add editor back to the pool list, got:\n%s", got)
	}
	after, err := os.ReadFile(poolUsersPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("an aborted add must not touch agent_users.json")
	}
}

// TestTeamPoolAddInvalidFieldStays pins the field-level gate in the add flow:
// a bad base URL refuses at field confirm, names the field without its value,
// and leaves the file untouched.
func TestTeamPoolAddInvalidFieldStays(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{UserID: "au-1"})
	before, err := os.ReadFile(poolUsersPath())
	if err != nil {
		t.Fatal(err)
	}
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open id
	m = typeTeamName(m, "au-2")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	for range 3 { // identity → provider → base url
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeTeamName(m, "not a url")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // field refuses
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "agent user base_url: must be an http(s) URL with a host") {
		t.Fatalf("bad base url should refuse at field confirm, got:\n%s", got)
	}
	if !strings.Contains(got, "> base_url:") {
		t.Fatalf("the cursor should stay on base_url, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc}) // back to the list phase
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc}) // abort the whole add
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "a add user") {
		t.Fatalf("esc should abort the add editor back to the pool list, got:\n%s", got)
	}
	after, err := os.ReadFile(poolUsersPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a refused field must not touch agent_users.json")
	}
}

// TestTeamPoolAddBlankIDSaveRefuses pins the s route's integrity check in the
// add flow: saving an entry without an id refuses on the id row — the one
// required field — and writes nothing, not even the pool file.
func TestTeamPoolAddBlankIDSaveRefuses(t *testing.T) {
	t.Chdir(t.TempDir())
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Agent user id must not be empty") {
		t.Fatalf("s with a blank id should refuse, got:\n%s", got)
	}
	if !strings.Contains(got, "> id:") {
		t.Fatalf("s should locate the cursor on the id row, got:\n%s", got)
	}
	if !strings.Contains(got, "Adding agent user") {
		t.Fatalf("the editor should stay open after the refusal, got:\n%s", got)
	}
	if _, err := os.Stat(poolUsersPath()); !os.IsNotExist(err) {
		t.Fatalf("no pool file should be created by a blank-id save, err=%v", err)
	}
}

// TestTeamPoolEditOpensWithE pins the editor entry: e from the list opens the
// field-list editor showing every editable field — the immutable id row
// first — with the cursor on the first missing field and the save key, and the
// draft prefilled from the focused entry.
func TestTeamPoolEditOpensWithE(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{
		UserID: "au-1", Identity: "acct@corp", Provider: "anthropic",
		BaseURL: "https://api.anthropic.com", Model: "claude-opus-5", Effort: "high",
	})
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'})
	got := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"editing au-1", "> api_key:", "id:", "provider:", "base_url:", "model:", "effort:", "api_key:", "s save"} {
		if !strings.Contains(got, want) {
			t.Fatalf("editor should show %q, got:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "acct@corp") || !strings.Contains(got, "claude-opus-5") {
		t.Fatalf("editor should prefill the draft fields, got:\n%s", got)
	}
}

// TestTeamPoolEditNavWraps pins field-cursor navigation: up/down move the
// cursor through the seven rows (the immutable id row included), wrapping at
// both ends.
func TestTeamPoolEditNavWraps(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{UserID: "au-1", Provider: "anthropic"})
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'})
	for range 5 {
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "> effort:") {
		t.Fatalf("down x5 should cursor the effort field, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "> id:") {
		t.Fatalf("down past the last field should wrap to the id row, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "> effort:") {
		t.Fatalf("up from the id row should wrap to effort, got:\n%s", got)
	}
}

// TestTeamPoolEditShowsPlaintextKey pins the user contract: the editor renders
// the api key in plaintext (the user's display requirement overrides the
// default mask-everything policy on this screen).
func TestTeamPoolEditShowsPlaintextKey(t *testing.T) {
	key := "sk-ant-plaintext-42"
	writeAgentUsersFixture(t, team.AgentUser{UserID: "au-1", APIKey: key})
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'})
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, key) {
		t.Fatalf("editor must render the api key in plaintext (user contract), got:\n%s", got)
	}
}

// TestTeamPoolEditFieldKeyDomainIsolation pins that the field edit owns every
// key: "a" inside the edit is an ordinary letter, never the add key, and the
// edit shows the buffer with the cursor marker.
func TestTeamPoolEditFieldKeyDomainIsolation(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{UserID: "au-1", Provider: "anthropic"})
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open identity field
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "a▏") {
		t.Fatalf("a should type into the field, got:\n%s", got)
	}
}

// TestTeamPoolEditFieldEscReverts pins that esc inside a field edit discards
// that field's edit and returns to the field list without writing.
func TestTeamPoolEditFieldEscReverts(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{UserID: "au-1", Provider: "anthropic"})
	before, err := os.ReadFile(poolUsersPath())
	if err != nil {
		t.Fatal(err)
	}
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open identity
	m = typeTeamName(m, "oops")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "s save") {
		t.Fatalf("esc should return to the field list, got:\n%s", got)
	}
	if got := ansi.Strip(m.renderTeamPicker()); strings.Contains(got, "oops") {
		t.Fatalf("the discarded field edit must not render, got:\n%s", got)
	}
	after, err := os.ReadFile(poolUsersPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a reverted field edit must not touch agent_users.json")
	}
}

// TestTeamPoolEditSavePublishesField pins the s route: typing one field and
// pressing s publishes the whole draft through the CAS path, and every field
// the user did not edit — the api key included — survives (merge semantics).
func TestTeamPoolEditSavePublishesField(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{
		UserID: "au-1", Identity: "acct@corp", Provider: "anthropic",
		BaseURL: "https://api.anthropic.com", Model: "claude-opus-5", Effort: "high",
		APIKey: "sk-ant-keep-me",
	})
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open identity, prefilled
	for range len("acct@corp") {
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyBackspace}) // clear the prefill
	}
	m = typeTeamName(m, "newacct@corp")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // confirm back to list
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})          // save
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "a add user") {
		t.Fatalf("s should return to the pool list, got:\n%s", got)
	}
	users := readStoredPool(t)
	if len(users) != 1 {
		t.Fatalf("s must not add or remove entries, got %+v", users)
	}
	u := users[0]
	if u.Identity != "newacct@corp" {
		t.Fatalf("identity = %q, want the edited value", u.Identity)
	}
	if u.Provider != "anthropic" || u.BaseURL != "https://api.anthropic.com" ||
		u.Model != "claude-opus-5" || u.Effort != "high" {
		t.Fatalf("unedited fields must survive the save, got %+v", u)
	}
	if u.APIKey != "sk-ant-keep-me" {
		t.Fatalf("an untouched api key must survive the save, got %q", u.APIKey)
	}
}

// TestTeamPoolEditInvalidFieldStays pins field-level integrity: a bad base_url
// refuses at confirm time, the refusal names the field without carrying its
// value, the cursor stays on the field, and the file is untouched.
func TestTeamPoolEditInvalidFieldStays(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{UserID: "au-1", Provider: "anthropic"})
	before, err := os.ReadFile(poolUsersPath())
	if err != nil {
		t.Fatal(err)
	}
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // cursor on base_url
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeTeamName(m, "not a url")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // field refuses
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "agent user base_url: must be an http(s) URL with a host") {
		t.Fatalf("bad base_url should refuse at field confirm, got:\n%s", got)
	}
	if !strings.Contains(got, "> base_url:") {
		t.Fatalf("the cursor should stay on base_url, got:\n%s", got)
	}
	// The refusal message matches the fixed field+reason text — if it carried
	// the typed value, this exact string would not appear (K2).
	after, err := os.ReadFile(poolUsersPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a refused field must not touch agent_users.json")
	}
}

// TestTeamPoolEditSaveValidatesWholeDraft pins the s route's integrity
// backstop: even when every field is filled, a draft that carries an illegal
// value refuses at save, names the field without its value, moves the cursor
// onto it, and leaves the file untouched.
func TestTeamPoolEditSaveValidatesWholeDraft(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{
		UserID: "au-1", Identity: "acct@corp", Provider: "anthropic",
		BaseURL: "https://api.anthropic.com", Model: "claude-opus-5", Effort: "high",
		APIKey: "sk-ant-original",
	})
	before, err := os.ReadFile(poolUsersPath())
	if err != nil {
		t.Fatal(err)
	}
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'})
	p := m.teamPick
	p.pool.draft.BaseURL = "ftp://not-a-dialable-endpoint" // bypass the field gate
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "agent user base_url: must be an http(s) URL with a host") {
		t.Fatalf("s should refuse the bad draft, got:\n%s", got)
	}
	if !strings.Contains(got, "> base_url:") {
		t.Fatalf("s should locate the cursor on base_url, got:\n%s", got)
	}
	// The refusal message matches the fixed field+reason text — if it carried
	// the draft value, this exact string would not appear (K2).
	after, err := os.ReadFile(poolUsersPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a refused save must not touch agent_users.json")
	}
}

// TestTeamPoolEditEscCancelsNoWrite pins that esc from the field list aborts
// the whole edit with no write, returning to the pool list — even after the
// provider picker confirmed its preselection into the draft.
func TestTeamPoolEditEscCancelsNoWrite(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{UserID: "au-1", Provider: "anthropic"})
	before, err := os.ReadFile(poolUsersPath())
	if err != nil {
		t.Fatal(err)
	}
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // identity → provider
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeTeamName(m, "openai")                       // inert: the picker never types
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // confirm keeps the preselection
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})   // abort the whole edit
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "a add user") {
		t.Fatalf("esc should abort the edit back to the pool list, got:\n%s", got)
	}
	after, err := os.ReadFile(poolUsersPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("an aborted edit must not touch agent_users.json")
	}
}

// TestTeamPoolEditKeySavedRaw pins that the api key is stored exactly as typed:
// s after editing the key field persists it raw, with no trimming. The fixture
// is fully configured except the key, so the editor cursor lands on the key
// row and s publishes through whole-entry validation.
func TestTeamPoolEditKeySavedRaw(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{
		UserID: "au-1", Identity: "acct@corp", Provider: "anthropic",
		BaseURL: "https://api.anthropic.com", Model: "claude-opus-5", Effort: "high",
	})
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open api_key
	m = typeTeamName(m, "sk-ant- raw -key")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	teamKey(m, tea.KeyPressMsg{Code: 's'})
	users := readStoredPool(t)
	if users[0].APIKey != "sk-ant- raw -key" {
		t.Fatalf("api key should be stored raw (no trim), got %q", users[0].APIKey)
	}
}

// TestTeamPoolDetailShowsPlaintextKey pins the detail screen contract: enter
// shows the entry's fields with the key in plaintext — the user's display
// requirement covers the whole management screen, editor and detail alike.
func TestTeamPoolDetailShowsPlaintextKey(t *testing.T) {
	key := "sk-ant-1234567890"
	writeAgentUsersFixture(t, team.AgentUser{UserID: "au-1", Provider: "anthropic", APIKey: key})
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Key: "+key) {
		t.Fatalf("detail should render the key in plaintext, got:\n%s", got)
	}
	if strings.Contains(got, "••••") {
		t.Fatalf("detail must not mask the key, got:\n%s", got)
	}
}
