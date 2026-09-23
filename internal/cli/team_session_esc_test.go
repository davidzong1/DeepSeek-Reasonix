package cli

// Regression tests for the bound team session's esc contract: esc stops the
// selected member's turn, and only a second esc leaves. m.state cannot gate this
// — every member bind resets it, and a dispatched member never sets it.

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// escRig binds the overlay's session to a member whose backend reports the given
// live turn, and counts the interrupts that reach it. status feeds the switch
// gate; running/canceled feed the esc gate.
func escRig(t *testing.T, status control.RuntimeStatus, running, canceled bool) (chatTUI, *int) {
	t.Helper()
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	m.memberEvents = newMemberEventPump()
	cancels := 0
	requested := false
	live := running
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return stubBackend{
			label: b.MemberID, status: status, live: &live,
			canceled: canceled, cancels: &cancels, requested: &requested,
		}, nil
	}, 4)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	if cmd := m.switchTeamMember("lead"); cmd == nil {
		t.Fatal("binding the leader must succeed")
	}
	return m, &cancels
}

// TestCtrlCStopsTheBoundMemberAndNeverQuits pins Ctrl+C's half: in a bound
// session it stops the member, and neither press quits the app. The chat
// handler's quit branch belongs to this window's own turn — a member slow to
// stop must not take the session down with it.
func TestCtrlCStopsTheBoundMemberAndNeverQuits(t *testing.T) {
	m, cancels := escRig(t, control.RuntimeStatus{Running: true}, true, false)
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

	next, first := m.Update(ctrlC)
	m = next.(chatTUI)
	if *cancels != 1 {
		t.Fatalf("Ctrl+C must interrupt the member, got %d cancels", *cancels)
	}
	if first != nil {
		t.Fatal("the first Ctrl+C must interrupt, not quit")
	}

	// The cancel is in flight now: the plain chat quits on this second press, a
	// team session must not.
	next, second := m.Update(ctrlC)
	m = next.(chatTUI)
	if second != nil {
		t.Fatalf("the second Ctrl+C must not quit from a team session, got %T", second())
	}
	if *cancels != 1 {
		t.Fatalf("an in-flight cancel must not be re-requested, got %d cancels", *cancels)
	}
	if !m.teamPick.session.active {
		t.Fatal("Ctrl+C must leave the session open")
	}
}

// TestCtrlCOnAnIdleWindowWithALiveMemberStillStops pins the gate again: the
// window's own flag reads idle, so the chat handler would treat this as the
// composer-clear / double-press-quit gesture. The member's live turn wins.
func TestCtrlCOnAnIdleWindowWithALiveMemberStillStops(t *testing.T) {
	m, cancels := escRig(t, control.RuntimeStatus{Running: true}, true, false)
	m.state = tuiIdle

	next, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = next.(chatTUI)

	if *cancels != 1 {
		t.Fatalf("a live member turn must be interruptible from an idle window, got %d", *cancels)
	}
	if cmd != nil {
		t.Fatalf("an interrupt must not return a quit command, got %T", cmd())
	}
}

// TestExitKeysWorkAgainOnceTheInterruptSettles pins the flow the interrupt must
// not break: a member is thinking, it is interrupted, the turn settles and the
// member is idle — and from there the exit keys behave exactly as they did
// before: Ctrl+C twice quits the TUI, Ctrl+T leaves the team for the plain chat
// window. An interrupt that kept consuming Ctrl+C would strand both.
func TestExitKeysWorkAgainOnceTheInterruptSettles(t *testing.T) {
	m, cancels := escRig(t, control.RuntimeStatus{Running: true}, true, false)
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

	next, _ := m.Update(ctrlC)
	m = next.(chatTUI)
	if *cancels != 1 {
		t.Fatalf("precondition: Ctrl+C must interrupt the thinking member, got %d", *cancels)
	}

	// The turn settles: Running() goes false, and the TurnDone ingest (not driven
	// here — it also publishes the member's owner history off-loop) idles the
	// window's own flag.
	backend, ok := m.teamBackends.bound("alpha", "lead")
	if !ok {
		t.Fatal("the bound member must still be assembled")
	}
	stub, ok := backend.(stubBackend)
	if !ok || stub.live == nil {
		t.Fatal("the fixture backend must expose its live flag")
	}
	*stub.live = false
	m.state = tuiIdle

	next, first := m.Update(ctrlC)
	m = next.(chatTUI)
	if first != nil {
		t.Fatal("the first idle Ctrl+C must arm the quit, not quit outright")
	}
	if m.lastCtrlCAt.IsZero() {
		t.Fatal("the first idle Ctrl+C must arm the double-press quit")
	}

	next, second := m.Update(ctrlC)
	m = next.(chatTUI)
	if second == nil {
		t.Fatal("a double Ctrl+C on an idle member must quit the TUI")
	}
	if msg := second(); msg != (tuiShutdownMsg{userInitiated: true}) {
		t.Fatalf("the double Ctrl+C command = %T, want tuiShutdownMsg", msg)
	}

	// Ctrl+T is the way out of the team, back to the chat's own window.
	next, _ = m.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	m = next.(chatTUI)
	if m.teamPick != nil || m.teamSessionBound() {
		t.Fatal("Ctrl+T must leave the team session and hand the window back")
	}
	if m.ambient != nil {
		t.Fatal("the ambient backend must be adopted again on the way out")
	}
}

// TestCtrlCOutsideATeamSessionKeepsItsGestures is the boundary: the interrupt
// only replaces Ctrl+C inside a bound session. Everywhere else the old
// semantics stand, including the idle press that arms the double-press quit.
func TestCtrlCOutsideATeamSessionKeepsItsGestures(t *testing.T) {
	m := newTestChatTUI()
	next, _ := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = next.(chatTUI)

	if m.lastCtrlCAt.IsZero() {
		t.Fatal("an idle Ctrl+C outside a team must arm the double-press quit")
	}
}

// TestEscStopsTheBoundMemberWithoutLeaving is the core contract: the member is
// thinking, esc interrupts it, and the session stays open so the operator can
// watch it stop and then decide what to do.
func TestEscStopsTheBoundMemberWithoutLeaving(t *testing.T) {
	m, cancels := escRig(t, control.RuntimeStatus{Running: true}, true, false)
	if !m.teamPick.session.active {
		t.Fatal("precondition: the session must be bound")
	}

	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})

	if *cancels != 1 {
		t.Fatalf("esc must interrupt the member exactly once, got %d", *cancels)
	}
	if !m.teamPick.session.active {
		t.Fatal("interrupting a turn must not close the session")
	}
}

// TestEscLeavesOnceTheCancelIsInFlight pins the second half: a cancel that is
// already requested must not swallow esc, or a member that takes a moment to
// stop would block every attempt to leave in the meantime.
func TestEscLeavesOnceTheCancelIsInFlight(t *testing.T) {
	m, cancels := escRig(t, control.RuntimeStatus{Running: true, CancelRequested: true}, true, true)
	if !m.teamPick.session.active {
		t.Fatal("precondition: the session must be bound")
	}

	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})

	if *cancels != 0 {
		t.Fatalf("a turn already being cancelled must not be cancelled again, got %d", *cancels)
	}
	if m.teamPick.session.active {
		t.Fatal("the second esc must close the session")
	}
}

// TestEscStillClosesAnIdleSession keeps the pre-existing navigation contract: a
// member with nothing running has nothing to interrupt, so esc is the layer
// walk it always was — panel first, then the session.
func TestEscStillClosesAnIdleSession(t *testing.T) {
	m, cancels := escRig(t, control.RuntimeStatus{}, false, false)
	m.setSessionPanel(true)

	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.teamPick.session.panel {
		t.Fatal("esc must dismiss the panel before it closes the session")
	}
	if !m.teamPick.session.active {
		t.Fatal("hiding the panel must not close the session")
	}

	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.teamPick.session.active {
		t.Fatal("a second esc must close the session")
	}
	if *cancels != 0 {
		t.Fatalf("an idle member must never be cancelled, got %d", *cancels)
	}
}

// TestEscStopsAMemberTheWindowDidNotStart is the trap this fix turns on: the
// window's own flag reads idle because the member was dispatched by the leader,
// yet its turn is live and esc must still stop it.
func TestEscStopsAMemberTheWindowDidNotStart(t *testing.T) {
	m, cancels := escRig(t, control.RuntimeStatus{Running: true}, true, false)
	m.state = tuiIdle // the flag bindBackend leaves behind

	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})

	if *cancels != 1 {
		t.Fatalf("the backend's live turn must be interruptible, got %d cancels", *cancels)
	}
	if !m.teamPick.session.active {
		t.Fatal("the session must stay open")
	}
}

// TestBindToARunningMemberShowsItThinking pins the other half of the same
// defect: a member already working must render as working the moment the window
// binds it, or the operator sees a busy member as idle and esc as a no-op.
func TestBindToARunningMemberShowsItThinking(t *testing.T) {
	m, _ := escRig(t, control.RuntimeStatus{Running: true}, true, false)

	if m.state != tuiRunning {
		t.Fatalf("binding a running member must enter the running state, got %v", m.state)
	}
	if !m.boundMemberRunning() {
		t.Fatal("boundMemberRunning must read the backend's live turn")
	}
	if line := m.runningWorkingLine(false, false); line == "" {
		t.Fatal("the working line must render while the bound member thinks")
	}
}

// TestMemberTurnBoundariesDriveTheRunningState pins the event half: a member's
// own TurnStarted enters the running state even though the window submitted
// nothing, and its TurnDone leaves it.
//
// Only TurnStarted is driven. A TurnDone also publishes the member's owner
// history, which spawns the cockpit worker mid-test and leaves the temp state
// root still being written when t.TempDir tears it down; the leaving half is
// pinned against the backend instead, in TestEscLeavesOnceTheCancelIsInFlight.
func TestMemberTurnBoundariesDriveTheRunningState(t *testing.T) {
	m, _ := escRig(t, control.RuntimeStatus{}, false, false)
	if m.state != tuiIdle {
		t.Fatalf("precondition: an idle bind starts idle, got %v", m.state)
	}
	m.memberEvents = nil

	next, _ := m.Update(memberEventMsg{member: "lead", ev: event.Event{Kind: event.TurnStarted}})
	m = next.(chatTUI)
	if m.state != tuiRunning {
		t.Fatalf("a member's TurnStarted must enter the running state, got %v", m.state)
	}
}

// TestEscOnAReadOnlyMemberStillLeaves pins the follower degradation: a read-only
// member has no turn of its own to cancel (Running and Cancel are both inert),
// so esc must fall through to the layer walk instead of consuming itself.
func TestEscOnAReadOnlyMemberStillLeaves(t *testing.T) {
	follower, err := newMemberFollowerBackend(
		stubFollowerSource{stamp: "stem:1"}, event.Discard,
		"lead", "ref", "", "", "", "", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := escRig(t, control.RuntimeStatus{}, false, false)
	m.ctrl = follower

	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})

	if m.teamPick.session.active {
		t.Fatal("esc on a read-only member must close the session, not be consumed")
	}
}

// TestSessionPanelHintNamesEscsRealAction pins the key/label pair: the panel
// must not promise "hide panel" while esc stops a turn, nor "stop" when there is
// nothing to stop.
func TestSessionPanelHintNamesEscsRealAction(t *testing.T) {
	m, _ := escRig(t, control.RuntimeStatus{Running: true}, true, false)
	m.setSessionPanel(true)
	if got := m.renderTeamPicker(); !strings.Contains(got, "Esc stop") {
		t.Fatalf("a running member's panel must name esc's stop action:\n%s", got)
	}

	idle, _ := escRig(t, control.RuntimeStatus{}, false, false)
	idle.setSessionPanel(true)
	if got := idle.renderTeamPicker(); !strings.Contains(got, "Esc hide panel") {
		t.Fatalf("an idle member's panel must keep the hide-panel hint:\n%s", got)
	}
}

// stubFollowerSource is the minimal read source a follower needs to exist; the
// esc path never reads history, it only asks whether a turn is running.
type stubFollowerSource struct{ stamp string }

func (s stubFollowerSource) History(_ context.Context) ([]provider.Message, error) {
	return nil, nil
}

func (s stubFollowerSource) Stamp() string { return s.stamp }
