package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// exitKey is the chord as a terminal sends it: no Text, since ctrl+t carries no
// character. Filling Text in would make String() report "t" and the assertion
// would pass against the wrong key.
var exitKey = tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}

// TestTeamExitKeyLeavesFromEveryScreen pins the exit shortcut's reach: it is
// reserved on every overlay screen, including the transient states that
// otherwise own every key — an armed delete, an open text field, the pool. Those
// are exactly where a user gets stuck, so the way out cannot be a key one of
// them may swallow first.
func TestTeamExitKeyLeavesFromEveryScreen(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(*testing.T) chatTUI
	}{
		{"team list", func(t *testing.T) chatTUI {
			m := openTeamOverlay(t)
			return teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc}) // session -> team list
		}},
		{"roster", openRoster},
		{"member editor", func(t *testing.T) chatTUI {
			return teamKey(openRoster(t), tea.KeyPressMsg{Code: tea.KeyEnter})
		}},
		{"open role field", func(t *testing.T) chatTUI {
			m := teamKey(openRoster(t), tea.KeyPressMsg{Code: tea.KeyEnter})
			return teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		}},
		{"armed member delete", func(t *testing.T) chatTUI {
			return teamKey(openRoster(t), tea.KeyPressMsg{Code: 'd'})
		}},
		{"add-team input", func(t *testing.T) chatTUI {
			m := openTeamOverlay(t)
			m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
			return teamKey(m, tea.KeyPressMsg{Code: 'a'})
		}},
		{"agent-user pool", func(t *testing.T) chatTUI {
			m := openTeamOverlay(t)
			m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
			return teamKey(m, tea.KeyPressMsg{Code: 'u'})
		}},
		{"armed step-down", func(t *testing.T) chatTUI {
			m := openRoster(t)
			m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // focus the leader
			return teamKey(m, tea.KeyPressMsg{Code: 'k'})
		}},
		{"bound session", openTeamOverlay},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeTeamFixture(t, leaderTeam())
			m := tc.open(t)
			if m.teamPick == nil {
				t.Fatal("the fixture must leave the overlay open")
			}
			m = teamKey(m, exitKey)
			if m.teamPick != nil {
				t.Fatal("the exit key must close the whole overlay, not one layer")
			}
			if m.hideComposer() {
				t.Error("leaving the team must hand the composer back to the chat")
			}
			if got := m.renderTeamPicker(); got != "" {
				t.Errorf("no team surface may survive the exit, got:\n%s", got)
			}
		})
	}
}

// TestTeamExitUnbindsMemberBackend pins what the exit is for: it puts the window
// back on the chat's own backend and its history in one step, from a bound
// session that esc would only unwind one layer of. The member's backend survives
// — leaving is not retiring, exactly as closing the overlay never was.
func TestTeamExitUnbindsMemberBackend(t *testing.T) {
	m := overlayWithBackends(t, map[string][]provider.Message{
		"lead": {userMessage("LEAD-HISTORY")},
	})
	m.teamPick.backends = m.teamBackends
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = next.(chatTUI)
	ambient := m.ctrl.Label()
	m.switchTeamMember("lead")
	m.setSessionPanel(true)
	if m.ctrl.Label() != "lead" {
		t.Fatalf("expected the leader bound, got %q", m.ctrl.Label())
	}

	m = teamKey(m, exitKey)

	if m.teamPick != nil {
		t.Fatal("the exit key must close the overlay from a bound session")
	}
	if got := m.ctrl.Label(); got != ambient {
		t.Errorf("leaving the team must restore the chat backend, got %q want %q", got, ambient)
	}
	if m.ambient != nil {
		t.Error("the saved ambient backend must be released after restoring")
	}
	if joined := strings.Join(m.transcript, "\n"); strings.Contains(joined, "LEAD-HISTORY") {
		t.Error("the member's transcript must not linger in the chat window")
	}
	if _, ok := m.teamBackends.bound("alpha", "lead"); !ok {
		t.Error("leaving the team must not retire the member's backend")
	}
	if got := m.statusMemberIDs(); got != nil {
		t.Errorf("the status line must drop the member buttons, got %v", got)
	}
}

// TestTeamExitClosesTheMemberModelPicker pins the one overlay that outlives the
// team by construction: /model in a bound session opens a picker over the team,
// and its choices are that member's models. Left open, its next confirm would
// address a member no longer bound.
func TestTeamExitClosesTheMemberModelPicker(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openTeamOverlay(t)
	if err := m.teamPick.store.AddAgentUser(team.AgentUser{
		UserID: "pool-a", Provider: "openai", Model: "gpt", BaseURL: "https://x/v1",
	}); err != nil {
		t.Fatal(err)
	}
	m.runModelSubcommand("/model")
	if m.quickPick == nil {
		t.Fatal("/model in a bound session must open the member picker")
	}

	m = teamKey(m, exitKey)
	if m.quickPick != nil {
		t.Fatal("the member's model picker must leave with the team")
	}
	// Even if one survived, confirming it must not act on a member that is gone.
	m.rebindMemberAgentUser("pool-a")
}

// TestTeamExitKeyIsAdvertisedAndInert pins the two halves of discoverability: the
// help lines name the exact key that leaves, and outside the overlay the chord is
// the composer's own — the team never claims it globally.
func TestTeamExitKeyIsAdvertisedAndInert(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, teamExitHint) {
		t.Errorf("the roster help must name the exit key, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // the member editor
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, teamExitHint) {
		t.Errorf("the member editor help must name the exit key, got:\n%s", got)
	}
	m = teamKey(m, exitKey)
	m.setSessionPanel(true) // no overlay: the setter must be inert, not panic

	// Outside the overlay the chord belongs to the composer again.
	if _, _, consumed := m.handleTeamKey(exitKey); consumed {
		t.Error("with no overlay open the exit key must not be consumed")
	}
}
