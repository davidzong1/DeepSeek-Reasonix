// Composer-to-wait acceptance tests for the leader wait route: input a bound
// leader's window accepts while its turn runs must reach the wait bus, or a
// leader blocked in leader_wait sleeps through input that is already queued.
package cli

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/sessioninbox"
	"reasonix/internal/team"
)

// composerStub is a bound leader backend that accepts durable input and reports
// itself busy: the state a leader is in while leader_wait holds its only step.
type composerStub struct {
	stubBackend
	// running is what Running reports, so the window's own running flag and the
	// backend's can be driven independently — the split this suite is about.
	running bool
	queued  []string
	steers  []string
}

func (s *composerStub) Running() bool { return s.running }

func (s *composerStub) TryEnqueueFollowup(req control.InboxRequest) (sessioninbox.InboxReceipt, error) {
	s.queued = append(s.queued, req.Submit)
	return sessioninbox.InboxReceipt{ItemID: "q1", Disposition: sessioninbox.DispositionQueuedFollowup}, nil
}

func (s *composerStub) TryEnqueueAndSteer(req control.InboxRequest) (sessioninbox.InboxReceipt, error) {
	s.steers = append(s.steers, req.Submit)
	return sessioninbox.InboxReceipt{ItemID: "s1", Disposition: sessioninbox.DispositionSteerAccepted}, nil
}

func (s *composerStub) InboxSnapshot() sessioninbox.InboxSnapshot {
	items := make([]sessioninbox.InboxItemMeta, 0, len(s.queued))
	for range s.queued {
		items = append(items, sessioninbox.InboxItemMeta{ID: "q1", State: sessioninbox.StateQueued})
	}
	return sessioninbox.InboxSnapshot{Items: items}
}

// composerRig binds the leader to a stub standing in for the busy backend
// leader_wait blocks inside. A real controller is embedded for everything the
// frame path reads that this suite does not drive.
func composerRig(t *testing.T) (chatTUI, *composerStub) {
	t.Helper()
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	m.memberEvents = newMemberEventPump()
	stub := &composerStub{
		stubBackend: stubBackend{SessionAPI: control.New(control.Options{}), label: "lead"},
		running:     true,
	}
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return stub, nil
	}, 4)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	if cmd := m.switchTeamMember("lead"); cmd == nil {
		t.Fatal("binding the leader must arm the member event pump")
	}
	return m, stub
}

// TestEnterOnABusyLeaderHoldsUntilTheWait pins the mid-turn composer path when
// the leader is working rather than waiting: Enter must not steer into the
// current step and must not wake a wait that is not running. The line stays
// held until the next leader_wait.
func TestEnterOnABusyLeaderHoldsUntilTheWait(t *testing.T) {
	m, stub := composerRig(t)
	m.state = tuiRunning
	m.input.SetValue("please also update the changelog")

	waiter := m.teamBackends.signals().Subscribe("alpha")
	defer waiter.Close()

	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(chatTUI)

	if len(stub.queued) != 0 || len(stub.steers) != 0 {
		t.Fatalf("Enter outside leader_wait must not dispatch, queued=%v steers=%v", stub.queued, stub.steers)
	}
	if got := m.teamBackends.signals().heldCount("alpha", "lead"); got != 1 {
		t.Fatalf("held lines = %d, want the composer line kept for the next wait", got)
	}
	select {
	case ev := <-waiter.C():
		t.Fatalf("a line sent outside leader_wait woke the bus: %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestEnterWhileTheLeaderWaitsSteers pins the other half: a line sent while
// leader_wait is blocked is a mid-turn steer and wakes that wait, the same
// admission Ctrl+Enter uses.
func TestEnterWhileTheLeaderWaitsSteers(t *testing.T) {
	m, stub := composerRig(t)
	m.state = tuiRunning
	m.input.SetValue("please also update the changelog")
	m.teamBackends.signals().enterWait("alpha", "lead")

	waiter := m.teamBackends.signals().Subscribe("alpha")
	defer waiter.Close()

	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(chatTUI)

	if len(stub.steers) != 1 || len(stub.queued) != 0 {
		t.Fatalf("Enter during leader_wait must steer, steers=%v queued=%v", stub.steers, stub.queued)
	}
	if m.teamBackends.signals().heldCount("alpha", "lead") != 0 {
		t.Fatal("a line sent during leader_wait must not be held")
	}
	select {
	case ev := <-waiter.C():
		if ev.Kind != waitKindInput || ev.Team != "alpha" || ev.ID != "lead" {
			t.Fatalf("event = %+v, want the leader's own input", ev)
		}
		if !strings.Contains(ev.Summary, "please also update the changelog") {
			t.Fatalf("summary = %q, want the queued line", ev.Summary)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the line never reached the leader's wait")
	}
}

// TestEnterOnAnIdleWindowWithABusyLeaderHoldsUntilTheWait pins the split the
// defect turned on: a member's turn survives a switch, and Enter must hand the
// line to the running leader rather than race it. The window's own flag now
// follows the backend, so the test drives the idle half explicitly: outside
// leader_wait the already-running branch holds the line for the next wait — it
// starts no follow-up turn and does not wake the bus.
func TestEnterOnAnIdleWindowWithABusyLeaderHoldsUntilTheWait(t *testing.T) {
	m, stub := composerRig(t)
	m.state = tuiIdle
	if !m.ctrl.Running() {
		t.Fatal("precondition: the bound leader's backend is still running its turn")
	}
	m.input.SetValue("one more thing before you finish")

	waiter := m.teamBackends.signals().Subscribe("alpha")
	defer waiter.Close()

	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(chatTUI)

	if len(stub.queued) != 0 || len(stub.steers) != 0 {
		t.Fatalf("the already-running branch must hold the line, queued=%v steers=%v", stub.queued, stub.steers)
	}
	if got := m.teamBackends.signals().heldCount("alpha", "lead"); got != 1 {
		t.Fatalf("held lines = %d, want the composer line", got)
	}
	select {
	case ev := <-waiter.C():
		t.Fatalf("the already-running branch woke the bus outside leader_wait: %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestSlashSteerWakesItsWait pins the /steer entry point. It shares enqueueSteer
// with Ctrl+Enter, so it must release the leader's wait exactly as the key does
// — including when mid-turn admission is refused and the text stays a durable
// follow-up, which is the case that otherwise waits forever.
func TestSlashSteerWakesItsWait(t *testing.T) {
	m, stub := composerRig(t)
	m.state = tuiRunning
	m.input.SetValue("/steer switch to approach B")

	waiter := m.teamBackends.signals().Subscribe("alpha")
	defer waiter.Close()

	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(chatTUI)

	if len(stub.steers) != 1 {
		t.Fatalf("/steer must attempt a mid-turn steer, steers = %v", stub.steers)
	}
	select {
	case ev := <-waiter.C():
		if ev.Kind != waitKindInput || ev.ID != "lead" {
			t.Fatalf("event = %+v, want the leader's own input", ev)
		}
		if !strings.Contains(ev.Summary, "switch to approach B") {
			t.Fatalf("summary = %q, want the steer body", ev.Summary)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("/steer did not wake the leader's wait")
	}
}

// TestCtrlEnterOnABusyLeaderWakesItsWait pins the third composer entry point —
// the durable mid-turn steer key — so all of them carry the same assertion.
func TestCtrlEnterOnABusyLeaderWakesItsWait(t *testing.T) {
	m, stub := composerRig(t)
	m.state = tuiRunning
	m.input.SetValue("hold off on the rename")

	waiter := m.teamBackends.signals().Subscribe("alpha")
	defer waiter.Close()

	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl})
	m = next.(chatTUI)

	if len(stub.steers) != 1 {
		t.Fatalf("Ctrl+Enter must attempt a mid-turn steer, steers = %v", stub.steers)
	}
	select {
	case ev := <-waiter.C():
		if ev.Kind != waitKindInput || ev.ID != "lead" {
			t.Fatalf("event = %+v, want the leader's own input", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ctrl+Enter did not wake the leader's wait")
	}
}

// TestQueuedInputOfANonLeaderStaysOffTheBus pins the scope end to end: the same
// entry point on a window bound to a plain member must not wake the leader,
// whose wait is about the members it dispatched, not about its own inbox.
func TestQueuedInputOfANonLeaderStaysOffTheBus(t *testing.T) {
	m, stub := composerRig(t)
	if cmd := m.switchTeamMember("alice"); cmd == nil {
		t.Fatal("binding alice must arm the member event pump")
	}
	m.state = tuiRunning
	m.input.SetValue("hello")

	waiter := m.teamBackends.signals().Subscribe("alpha")
	defer waiter.Close()

	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(chatTUI)

	if len(stub.queued) != 1 {
		t.Fatalf("the line must still be queued durably, queued = %v", stub.queued)
	}
	select {
	case ev := <-waiter.C():
		t.Fatalf("a member's queued input woke the leader's wait: %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestQueuedInputOnAFailedWriteStaysOffTheBus pins the ordering the signal has
// to keep: a failed durable write leaves nothing to run, so waking the leader
// would only spend a turn on an empty queue.
func TestQueuedInputOnAFailedWriteStaysOffTheBus(t *testing.T) {
	m, _ := composerRig(t)
	m.ctrl = failingEnqueueBackend{SessionAPI: m.ctrl}
	m.state = tuiRunning
	m.teamBackends.signals().enterWait("alpha", "lead")
	m.input.SetValue("this write will fail")

	waiter := m.teamBackends.signals().Subscribe("alpha")
	defer waiter.Close()

	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(chatTUI)

	select {
	case ev := <-waiter.C():
		t.Fatalf("a failed durable write woke the leader's wait: %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

// failingEnqueueBackend refuses every durable write, standing in for a closed
// or unwritable inbox.
type failingEnqueueBackend struct {
	control.SessionAPI
}

func (failingEnqueueBackend) TryEnqueueFollowup(control.InboxRequest) (sessioninbox.InboxReceipt, error) {
	return sessioninbox.InboxReceipt{}, sessioninbox.ErrClosed
}

func (failingEnqueueBackend) TryEnqueueAndSteer(control.InboxRequest) (sessioninbox.InboxReceipt, error) {
	return sessioninbox.InboxReceipt{}, sessioninbox.ErrClosed
}

// TestMemberTurnStartedLeavesTheWindowIdle documented the state split the
// composer paths had to work around: a member's TurnStarted used to reach the
// window's ingest only as a todo reset, so m.state stayed idle for a turn the
// window did not submit. The window now follows the backend's live turn, so the
// assertion is inverted — the composer paths still must not gate their signal on
// m.state (they gate on controllerRunning), but a thinking member is no longer
// invisible to the footer.
func TestMemberTurnStartedLeavesTheWindowIdle(t *testing.T) {
	m, _ := composerRig(t)
	m.handleMemberEvent(memberEventMsg{member: "lead", ev: event.Event{Kind: event.TurnStarted}})
	m.handleMemberEvent(memberEventMsg{
		member: "lead",
		ev:     event.Event{Kind: event.TurnPhase, PhaseName: "working"},
	})
	if m.state != tuiRunning {
		t.Fatalf("a bound member's TurnStarted must show as running, state = %v", m.state)
	}
	if m.turnPhase != "working" {
		t.Fatalf("turnPhase = %q, want the member's own phase", m.turnPhase)
	}
}
