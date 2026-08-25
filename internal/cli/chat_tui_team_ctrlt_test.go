package cli

// Ctrl+T exit-chain regression tests (route R10): the exit key drops the
// persisted selection and arms the auto-session suppression like x, so a
// re-click parks on the management page instead of resuming the old session.

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/team"
	"reasonix/internal/team/tui"
)

// TestCtrlTExitsSessionClearsSelectionAndSuppresses pins the P0 exit-chain
// contract: Ctrl+T from a bound session closes the whole overlay, drops the
// persisted member selection, and arms the suppression so the next [TEAM]
// click parks on the management page.
func TestCtrlTExitsSessionClearsSelectionAndSuppresses(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openTeamOverlay(t)                                              // auto-opens the leader session
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl}) // switch to alice, persisting her
	sessions := m.teamPick.sessions
	m = teamKey(m, exitKey)
	if m.teamPick != nil {
		t.Fatal("Ctrl+T must close the whole overlay")
	}
	sel, err := sessions.ReadSelection("alpha")
	if err != nil || sel.MemberID != "" {
		t.Fatalf("Ctrl+T must clear the persisted selection, got %+v, %v", sel, err)
	}
	if !m.teamSuppressAutoSession {
		t.Fatal("Ctrl+T must arm the auto-session suppression")
	}
}

// TestCtrlTReentryParksOnTeamManagement pins the re-entry shape: after Ctrl+T,
// a [TEAM] click lands on the team list, space descends into the roster, and
// no session window is resurrected on the way.
func TestCtrlTReentryParksOnTeamManagement(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openTeamOverlay(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl})
	m = teamKey(m, exitKey)
	m.onTeamButtonClick() // a fresh [TEAM] click
	if m.teamPick == nil || m.teamPick.session.active {
		t.Fatalf("re-entry after Ctrl+T must park on the management page, got active=%v", m.teamPick.session.active)
	}
	if got := m.teamPick.model.Mode(); got != tui.ModeTeams {
		t.Fatalf("re-entry should land on the team list, got %q", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeySpace})
	if got := m.teamPick.model.Mode(); got != tui.ModeList {
		t.Fatalf("space should enter the member roster, got %q", got)
	}
	if m.teamPick.session.active {
		t.Fatal("the roster must not resume the old session")
	}
}

// TestCtrlTFromRosterClearsStaleSelection pins the roster depth: esc leaves a
// stale selection behind (esc is navigation, not exit), and Ctrl+T from the
// roster must drop it — the same clear as from a bound session.
func TestCtrlTFromRosterClearsStaleSelection(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openTeamOverlay(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl}) // alice, persisted
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})                    // session -> roster
	sessions := m.teamPick.sessions
	sel, err := sessions.ReadSelection("alpha")
	if err != nil || sel.MemberID != "alice" {
		t.Fatalf("esc must keep the selection (navigation, not exit), got %+v, %v", sel, err)
	}
	m = teamKey(m, exitKey)
	if m.teamPick != nil {
		t.Fatal("Ctrl+T from the roster must close the overlay")
	}
	sel, err = sessions.ReadSelection("alpha")
	if err != nil || sel.MemberID != "" {
		t.Fatalf("Ctrl+T must drop a stale session selection, got %+v, %v", sel, err)
	}
}

// TestCtrlTDiscardsComposerDraftAndUnbinds pins the composer depth: a draft
// addressed to the bound member is discarded on exit, the chat's own backend
// comes back, and the composer is not hidden.
func TestCtrlTDiscardsComposerDraftAndUnbinds(t *testing.T) {
	m := overlayWithBackends(t, map[string][]provider.Message{
		"lead": {userMessage("LEAD-HISTORY")},
	})
	m.teamPick.backends = m.teamBackends
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = next.(chatTUI)
	ambient := m.ctrl.Label() // the chat's own backend, before any bind
	m.switchTeamMember("lead")
	m.input.SetValue("draft to alice")
	m = teamKey(m, exitKey)
	if m.teamPick != nil {
		t.Fatal("Ctrl+T must close the overlay from a bound session")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("a draft addressed to the member must not survive the exit: %q", got)
	}
	if m.ctrl.Label() != ambient {
		t.Fatalf("the chat backend must be restored, got %q want %q", m.ctrl.Label(), ambient)
	}
	if m.hideComposer() {
		t.Error("leaving the team must hand the composer back")
	}
}

// TestCtrlTRepeatedExitIdempotent pins the second Ctrl+T: with no overlay
// there is nothing to exit, and the key is not consumed — never a panic, never
// a resurrected overlay.
func TestCtrlTRepeatedExitIdempotent(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, exitKey)
	m = teamKey(m, exitKey) // second Ctrl+T: no overlay left
	if m.teamPick != nil {
		t.Fatal("the second Ctrl+T must not resurrect the overlay")
	}
}

// TestCtrlTNoLeaderAndBrokenStoreNoPanic pins the degenerate depths: a team
// with no leader and an overlay whose session seam failed to open both exit
// cleanly.
func TestCtrlTNoLeaderAndBrokenStoreNoPanic(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "coder-1", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	m = teamKey(m, exitKey)
	if m.teamPick != nil {
		t.Fatal("Ctrl+T without a leader must close the overlay")
	}

	m = newChatTUI(control.New(control.Options{}), "", make(chan event.Event, 1), 80)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.teamPick = &teamPicker{model: tui.New(nil), errMsg: "Team data unavailable: x"}
	m = teamKey(m, exitKey) // must not panic on a nil session seam
	if m.teamPick != nil {
		t.Fatal("Ctrl+T must close a broken overlay too")
	}
}

// TestCtrlTReentryThenTResumesExplicitly pins the deliberate-entry path: after
// Ctrl+T, only t reopens a session, and it opens on the leader — the old
// member selection is never restored.
func TestCtrlTReentryThenTResumesExplicitly(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openTeamOverlay(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl}) // alice
	m = teamKey(m, exitKey)
	m.onTeamButtonClick() // parks on the management page
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeySpace})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // focus lead
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})         // deliberate entry
	if !m.teamPick.session.active || m.teamPick.session.current != "lead" {
		t.Fatalf("t should resume on the leader, not the old member, got active=%v current=%q",
			m.teamPick.session.active, m.teamPick.session.current)
	}
}
