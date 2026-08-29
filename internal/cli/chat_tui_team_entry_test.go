package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// TestTeamButtonEntryReturnsRestoreCmd pins the entry seam: clicking [ TEAM ]
// opens straight on the team's leader window (§11.4) — the overlay is never
// "just opened" — and reports the leader as the bound member. The roster
// management page stays reachable via esc.
func TestTeamButtonEntryReturnsRestoreCmd(t *testing.T) {
	leaderTeamFixture(t)

	ctrl := control.New(control.Options{})
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)

	// The entry path assembles the leader backend and arms its event pump.
	if cmd := m.onTeamButtonClick(); cmd == nil {
		t.Fatal("the leader entry must arm the member event pump")
	}
	if m.teamPick == nil || !m.teamPick.session.active {
		t.Fatal("the click must open the overlay with the session active")
	}
	if got := m.teamPick.session.current; got != "alpha" {
		t.Fatalf("session must open on the leader, got %q", got)
	}
	if got := m.boundMember(); got != "alpha" {
		t.Fatalf("the window must report the leader as bound, got %q", got)
	}
}

// TestTeamButtonEntryBindsLeaderBackend pins R2.2: the [TEAM] click binds the
// leader's own Agent backend, so the window's transcript is that member's
// history rather than the chat's. A pre-installed registry is kept across
// reopens — its backends hold session leases.
func TestTeamButtonEntryBindsLeaderBackend(t *testing.T) {
	leaderTeamFixture(t)
	m := openTeamOverlay(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)

	m.memberEvents = make(chan memberEvent, 4)
	installed := newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return stubBackend{label: b.MemberID, history: []provider.Message{
			{Role: provider.RoleUser, Content: "LEADER-OWN-HISTORY"},
		}}, nil
	}, 4)
	m.teamBackends = installed

	if cmd := m.onTeamButtonClick(); cmd == nil {
		t.Fatal("the click must bind the leader and arm the pump")
	}
	if m.teamBackends != installed {
		t.Error("reopening must keep the existing registry, not orphan its backends")
	}
	if got := m.ctrl.Label(); got != "alpha" {
		t.Errorf("the window must be bound to the leader's backend, label = %q", got)
	}
	if joined := strings.Join(m.transcript, "\n"); !strings.Contains(joined, "LEADER-OWN-HISTORY") {
		t.Errorf("the transcript must show the leader's own history:\n%s", joined)
	}
}

// TestTeamOverlayCloseReturnsNoCmd pins the close seam: closing the overlay
// drops it from the model and returns no asynchronous command — the registry
// Close() completes synchronously (every instance stopped), so there is
// nothing left to await once the keypress handler returns.
func TestTeamOverlayCloseReturnsNoCmd(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "T", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive},
	}})
	m := openTeamOverlay(t)

	next, cmd := m.handleTeamPickerKey(tea.KeyPressMsg{Code: tea.KeyEsc}) // close the session window
	if cmd != nil {
		t.Fatal("closing the session must not return a pending command")
	}
	next, cmd = next.(chatTUI).handleTeamPickerKey(tea.KeyPressMsg{Code: tea.KeyEsc}) // close the overlay
	if cmd != nil {
		t.Fatal("closing the overlay must not return a pending command")
	}
	if next.(chatTUI).teamPick != nil {
		t.Fatal("esc on the team list should close the overlay")
	}
}
