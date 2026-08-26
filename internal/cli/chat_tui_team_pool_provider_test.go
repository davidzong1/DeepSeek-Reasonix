package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/team"
)

// poolEditOpenProvider walks the editor onto the provider picker: e opens the
// field list, the cursor lands on the first missing field, and up/down move it
// onto the provider row (the id row of an existing entry is immutable).
func poolEditOpenProvider(m chatTUI, u team.AgentUser) chatTUI {
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'})
	for i, f := range poolEditFields {
		if f == team.AgentUserFieldProvider {
			// step from wherever the cursor landed onto the provider row
			first := firstMissingField(u, false)
			for range (first - i + len(poolEditFields)) % len(poolEditFields) {
				m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyUp})
			}
			break
		}
	}
	return teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the picker
}

// TestTeamPoolProviderPickerPreselectsAndCycles pins the picker contract on an
// existing entry: opening the field preselects the stored canonical value,
// up/down cycle the options (unconfigured wraps around), printable keys are
// inert, and enter confirms the highlighted choice into the draft, which s
// persists.
func TestTeamPoolProviderPickerPreselectsAndCycles(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{
		UserID: "au-1", Identity: "alice", Provider: "openai", Model: "claude-opus-5",
	})
	m := openPool(t)
	m = poolEditOpenProvider(m, team.AgentUser{UserID: "au-1", Identity: "alice", Provider: "openai", Model: "claude-opus-5"})
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "GPT (OpenAI)") {
		t.Fatalf("the picker should preselect the stored provider, got:\n%s", got)
	}
	for _, r := range "claude" { // printable keys never touch the picker
		m = teamKey(m, tea.KeyPressMsg{Code: r})
	}
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "GPT (OpenAI)") {
		t.Fatalf("typed letters must not move the picker, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // → DeepSeek
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})         // → openai (wrapped)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyUp})   // → anthropic
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	teamKey(m, tea.KeyPressMsg{Code: 's'})
	users := readStoredPool(t)
	if users[0].Provider != "anthropic" {
		t.Fatalf("s should persist the highlighted option, got %q", users[0].Provider)
	}
}

// TestTeamPoolProviderPickerClearsToUnconfigured pins the explicit clear path:
// stepping up from the first canonical option lands on "unconfigured", and
// confirming it writes a blank provider — an entry is legal unconfigured.
func TestTeamPoolProviderPickerClearsToUnconfigured(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{
		UserID: "au-1", Identity: "alice", Provider: "anthropic",
	})
	m := openPool(t)
	m = poolEditOpenProvider(m, team.AgentUser{UserID: "au-1", Identity: "alice", Provider: "anthropic"})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyUp}) // anthropic → unconfigured
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	teamKey(m, tea.KeyPressMsg{Code: 's'})
	users := readStoredPool(t)
	if users[0].Provider != "" {
		t.Fatalf("confirming unconfigured should clear the provider, got %q", users[0].Provider)
	}
}

// TestTeamPoolProviderPickerEscKeepsValue pins the cancel path: esc from the
// picker discards the field edit, and a later save keeps the stored provider.
func TestTeamPoolProviderPickerEscKeepsValue(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{
		UserID: "au-1", Identity: "alice", Provider: "deepseek",
	})
	m := openPool(t)
	m = poolEditOpenProvider(m, team.AgentUser{UserID: "au-1", Identity: "alice", Provider: "deepseek"})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyUp}) // → openai
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	teamKey(m, tea.KeyPressMsg{Code: 's'})
	users := readStoredPool(t)
	if users[0].Provider != "deepseek" {
		t.Fatalf("esc should discard the picker move, got %q", users[0].Provider)
	}
}

// TestTeamPoolProviderLegacyShowsAndSurvives pins the legacy contract: a
// provider value an older version wrote renders marked as legacy in the
// picker, printable keys stay inert, and editing another field saves without
// rewriting or refusing it — the value is only replaced when the user picks a
// legal option.
func TestTeamPoolProviderLegacyShowsAndSurvives(t *testing.T) {
	writeTeamPoolFixture(t, nil, []team.AgentUser{
		{UserID: "au-1", Identity: "alice", Provider: "legacy-x", Model: "m1"},
	})
	m := openPool(t)
	m = poolEditOpenProvider(m, team.AgentUser{UserID: "au-1", Identity: "alice", Provider: "legacy-x", Model: "m1"})
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "legacy: legacy-x") {
		t.Fatalf("a legacy provider should render marked, got:\n%s", got)
	}
	for _, r := range "claude" { // still a picker: letters are inert
		m = teamKey(m, tea.KeyPressMsg{Code: r})
	}
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "legacy: legacy-x") {
		t.Fatalf("letters must not touch a legacy provider, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})   // leave the picker
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // provider → base_url
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // base_url → api_key
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // api_key → model
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // edit model
	for range "m1" {
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	m = typeTeamName(m, "m2")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	teamKey(m, tea.KeyPressMsg{Code: 's'})
	users := readStoredPool(t)
	if users[0].Provider != "legacy-x" || users[0].Model != "m2" {
		t.Fatalf("editing another field must preserve the legacy provider, got %q/%q", users[0].Provider, users[0].Model)
	}
}

// TestTeamPoolProviderLegacyReplacedByPick pins the replace path: a legacy
// provider is only rewritten once the user highlights a legal option.
func TestTeamPoolProviderLegacyReplacedByPick(t *testing.T) {
	writeTeamPoolFixture(t, nil, []team.AgentUser{
		{UserID: "au-1", Identity: "alice", Provider: "legacy-x", Model: "m1"},
	})
	m := openPool(t)
	m = poolEditOpenProvider(m, team.AgentUser{UserID: "au-1", Identity: "alice", Provider: "legacy-x", Model: "m1"})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // legacy → unconfigured
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // → anthropic
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	teamKey(m, tea.KeyPressMsg{Code: 's'})
	users := readStoredPool(t)
	if users[0].Provider != "anthropic" {
		t.Fatalf("picking a legal option should replace the legacy value, got %q", users[0].Provider)
	}
}
