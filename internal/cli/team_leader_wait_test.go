package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"reasonix/internal/evidence"
	"reasonix/internal/planmode"
	"reasonix/internal/team/agentruntime"
	"reasonix/internal/tool"
)

// waitForWaitSubscriber blocks until the bus has a subscriber, so a wait-side
// assertion measures the wait loop rather than a goroutine's start-up. It waits
// on a condition instead of sleeping for a duration, so a slow machine costs
// wall clock and never a false pass.
func waitForWaitSubscriber(t *testing.T, bus *waitBus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		bus.mu.Lock()
		live := len(bus.subs)
		bus.mu.Unlock()
		if live > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the wait loop never subscribed to the bus")
}

// leaderWaitFixture wires the tool to a live bus the way the registry does:
// through the task service's late-bound signal slot.
func leaderWaitFixture(t *testing.T) (*teamTaskService, *waitBus) {
	t.Helper()
	bus := newWaitBus()
	service := &teamTaskService{}
	service.signals.Store(bus)
	return service, bus
}

func leaderWaitToolFrom(t *testing.T, service *teamTaskService) tool.Tool {
	t.Helper()
	for _, candidate := range newLeaderTaskTools(service, "alpha", "lead") {
		if candidate.Name() == "leader_wait" {
			return candidate
		}
	}
	t.Fatal("leader_wait is absent from the leader task surface")
	return nil
}

func leaderReportEvent(id string) WaitEvent {
	return WaitEvent{Kind: agentruntime.AttentionReport, Team: "alpha", ID: id, Summary: "task " + id + " reported"}
}

// TestLeaderWaitEndsOnCancelPromptly pins I6: Esc / Ctrl+C cancels the turn's
// context, and the wait must return on that cancellation rather than at some
// boundary of its own. A wait that only re-checked the context on a timer would
// hold the turn until the next one.
func TestLeaderWaitEndsOnCancelPromptly(t *testing.T) {
	bus := newWaitBus()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := awaitLeaderWait(ctx, bus, "alpha", 0)
		done <- err
	}()
	waitForWaitSubscriber(t, bus)
	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled wait returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled wait never returned")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("cancel took %s: the wait did not observe ctx.Done directly", elapsed)
	}
}

// TestLeaderWaitReportsTimeoutAsAnOrdinaryResult pins the other half of the
// context contract: a deadline is not an error and not a member event. The
// leader has to read "nothing happened yet" as a normal answer, or every quiet
// wait would surface as a broken turn.
func TestLeaderWaitReportsTimeoutAsAnOrdinaryResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	events, err := awaitLeaderWait(ctx, newWaitBus(), "alpha", 0)
	if err != nil {
		t.Fatalf("a deadline must not be an error: %v", err)
	}
	if len(events) != 1 || events[0].Kind != waitKindTimeout {
		t.Fatalf("timeout wait returned %+v, want one %q event", events, waitKindTimeout)
	}
}

// TestLeaderWaitDeliversAnEventThatPrecededIt is the check-then-wait gate. A
// member that finished while the leader was between turns must be reported to
// the wait that starts afterwards, not slept through because no subscription
// existed at the moment it was produced.
func TestLeaderWaitDeliversAnEventThatPrecededIt(t *testing.T) {
	bus := newWaitBus()
	bus.Signal(leaderReportEvent("alpha-1"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events, err := awaitLeaderWait(ctx, bus, "alpha", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != "alpha-1" {
		t.Fatalf("an occurrence predating the wait was not reported: %+v", events)
	}
}

// TestLeaderWaitWakesOnASignalDeliveredWhileWaiting pins the live arm, and that
// one wait returns the whole burst: two members finishing together are one
// tool result, not two turns.
func TestLeaderWaitWakesOnASignalDeliveredWhileWaiting(t *testing.T) {
	bus := newWaitBus()
	done := make(chan []WaitEvent, 1)
	go func() {
		events, _ := awaitLeaderWait(context.Background(), bus, "alpha", 0)
		done <- events
	}()
	waitForWaitSubscriber(t, bus)
	bus.Signal(leaderReportEvent("alpha-1"))
	select {
	case events := <-done:
		if len(events) != 1 || events[0].ID != "alpha-1" {
			t.Fatalf("live signal not reported: %+v", events)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a signal delivered to a live subscription never woke the wait")
	}
}

// TestLeaderWaitReturnsABurstAsOneBatch pins the batch contract on the path a
// burst actually takes: the occurrences are already held when the wait starts,
// and the whole held set comes back in one result.
func TestLeaderWaitReturnsABurstAsOneBatch(t *testing.T) {
	bus := newWaitBus()
	for _, id := range []string{"alpha-1", "alpha-2", "alpha-3"} {
		bus.Signal(leaderReportEvent(id))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events, err := awaitLeaderWait(ctx, bus, "alpha", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("burst returned %d events, want 3: %+v", len(events), events)
	}
	for i, want := range []string{"alpha-1", "alpha-2", "alpha-3"} {
		if events[i].ID != want {
			t.Errorf("event %d = %q, want %q (arrival order must survive)", i, events[i].ID, want)
		}
	}
}

// TestLeaderWaitIgnoresAnotherTeamsEvents pins the subscription filter: the bus
// is registry-wide, so a wait for one team must not be woken by another's.
func TestLeaderWaitIgnoresAnotherTeamsEvents(t *testing.T) {
	bus := newWaitBus()
	bus.Signal(WaitEvent{Kind: agentruntime.AttentionReport, Team: "beta", ID: "beta-1", Summary: "task beta-1 reported"})
	bus.Signal(leaderReportEvent("alpha-1"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events, err := awaitLeaderWait(ctx, bus, "alpha", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != "alpha-1" {
		t.Fatalf("wait saw another team's events: %+v", events)
	}
}

// TestLeaderWaitEndsWhenTheRegistryCloses pins the teardown gate: closeAll
// closes the bus, and every waiter has to return instead of holding a goroutine
// past the registry it belongs to.
func TestLeaderWaitEndsWhenTheRegistryCloses(t *testing.T) {
	bus := newWaitBus()
	done := make(chan error, 1)
	go func() {
		_, err := awaitLeaderWait(context.Background(), bus, "alpha", 0)
		done <- err
	}()
	waitForWaitSubscriber(t, bus)
	bus.close()
	select {
	case err := <-done:
		if !errors.Is(err, errLeaderWaitClosed) {
			t.Fatalf("teardown returned %v, want errLeaderWaitClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing the registry did not release the waiter")
	}
}

// TestLeaderWaitWithoutABusIsBoundedByItsContext pins the no-registry shape: a
// host that wired no bus must still get a bounded wait that reports its timeout,
// never a turn that blocks on nothing.
func TestLeaderWaitWithoutABusIsBoundedByItsContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	events, err := awaitLeaderWait(ctx, nil, "alpha", 0)
	if err != nil {
		t.Fatalf("no bus must still be an ordinary timeout: %v", err)
	}
	if len(events) != 1 || events[0].Kind != waitKindTimeout {
		t.Fatalf("no-bus wait returned %+v, want one %q event", events, waitKindTimeout)
	}
}

// TestLeaderWaitNeverTakesAWorkspaceWriteLease pins I1. The write lease is taken
// on plan.effects.WorkspaceMutation (tool_write_coordination.go), which the
// classification only reports for a writer or an unknown tool — so the wait has
// to be a known reader, or a leader would sit blocked on a lease while another
// member holds the very workspace it is waiting on.
func TestLeaderWaitNeverTakesAWorkspaceWriteLease(t *testing.T) {
	service, _ := leaderWaitFixture(t)
	wait := leaderWaitToolFrom(t, service)
	if !wait.ReadOnly() {
		t.Fatal("leader_wait must be read-only: a writer classification takes the workspace write lease")
	}
	effects := evidence.ClassifyToolCall("leader_wait", json.RawMessage(`{"timeout_seconds":1200}`), wait.ReadOnly())
	if !effects.Known || effects.WorkspaceMutation {
		t.Fatalf("leader_wait effects = %+v, want a known call with no workspace mutation", effects)
	}
}

// TestLeaderWaitIsNeverBatchedInParallel pins I2. ReadOnly alone is not the
// guarantee it looks like: the batch partitioner falls back to the target's
// ReadOnly when a tool has no classifier, so a neighbouring reader would run
// beside the wait and the step would continue past it.
func TestLeaderWaitIsNeverBatchedInParallel(t *testing.T) {
	service, _ := leaderWaitFixture(t)
	wait := leaderWaitToolFrom(t, service)
	classifier, ok := wait.(tool.BatchClassifier)
	if !ok {
		t.Fatal("leader_wait must implement BatchClassifier or the partitioner falls back to ReadOnly")
	}
	class := classifier.ClassifyCall(json.RawMessage(`{"timeout_seconds":1200}`))
	if !class.Known || !class.ReadOnly || class.ParallelSafe {
		t.Fatalf("leader_wait class = %+v, want known+read-only+not-parallel-safe", class)
	}
}

// TestLeaderWaitToolBlocksUntilTheBusSignals runs the tool end to end: it
// subscribes, a producer reports, and the reason is in the tool result — the
// whole point being that the leader's next sample already carries it instead of
// the leader paying a round-trip to ask for status.
func TestLeaderWaitToolBlocksUntilTheBusSignals(t *testing.T) {
	service, bus := leaderWaitFixture(t)
	wait := leaderWaitToolFrom(t, service)
	done := make(chan string, 1)
	go func() {
		out, err := wait.Execute(context.Background(), json.RawMessage(`{"timeout_seconds":1200}`))
		if err != nil {
			out = "error: " + err.Error()
		}
		done <- out
	}()
	waitForWaitSubscriber(t, bus)
	bus.Signal(leaderReportEvent("alpha-7"))
	select {
	case out := <-done:
		if !strings.Contains(out, agentruntime.AttentionReport) || !strings.Contains(out, "alpha-7") {
			t.Fatalf("tool result does not name the wakeup:\n%s", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the wait tool did not return after a signal")
	}
}

// TestLeaderWaitToolReportsTimeoutAndValidatesItsBound pins the argument
// contract: unset takes the default, an out-of-range value is refused by the
// host as well as by the schema, and a wait that sees nothing says so instead of
// inventing a reason.
func TestLeaderWaitToolReportsTimeoutAndValidatesItsBound(t *testing.T) {
	for _, tc := range []struct {
		seconds int
		want    time.Duration
		wantErr bool
	}{
		{0, leaderWaitDefaultTimeout, false},
		{int(leaderWaitMinTimeout.Seconds()), leaderWaitMinTimeout, false},
		{int(leaderWaitMaxTimeout.Seconds()), leaderWaitMaxTimeout, false},
		{int(leaderWaitMinTimeout.Seconds()) - 1, 0, true},
		{int(leaderWaitMaxTimeout.Seconds()) + 1, 0, true},
		{-1, 0, true},
	} {
		got, err := leaderWaitTimeout(tc.seconds)
		if tc.wantErr {
			if err == nil {
				t.Errorf("leaderWaitTimeout(%d) accepted an out-of-range value", tc.seconds)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("leaderWaitTimeout(%d) = %s, %v; want %s", tc.seconds, got, err, tc.want)
		}
	}
	service, _ := leaderWaitFixture(t)
	wait := leaderWaitToolFrom(t, service)
	var dec struct {
		Properties map[string]struct {
			Minimum int `json:"minimum"`
			Maximum int `json:"maximum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(wait.Schema(), &dec); err != nil {
		t.Fatal(err)
	}
	// The schema is the provider-visible half of the same bound, so it is read
	// back rather than trusted to have been written from the constants.
	bound, ok := dec.Properties["timeout_seconds"]
	if !ok || bound.Minimum != int(leaderWaitMinTimeout.Seconds()) || bound.Maximum != int(leaderWaitMaxTimeout.Seconds()) {
		t.Fatalf("schema bound = %+v, want %d..%d", bound,
			int(leaderWaitMinTimeout.Seconds()), int(leaderWaitMaxTimeout.Seconds()))
	}
	// The parent deadline is what fires here, so the tool's own floor never costs
	// this test twenty minutes of wall clock.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	out, err := wait.Execute(ctx, json.RawMessage(`{"timeout_seconds":1200}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, waitKindTimeout) {
		t.Fatalf("a quiet wait must report its timeout:\n%s", out)
	}
}

// TestLeaderWaitIsLeaderOnly pins I8's surface question: waiting is the leader's
// job, so the member face must not carry it.
func TestLeaderWaitIsLeaderOnly(t *testing.T) {
	service, _ := leaderWaitFixture(t)
	for _, candidate := range newMemberTaskTools(service, "alpha", "m1") {
		if candidate.Name() == "leader_wait" {
			t.Fatal("leader_wait must never reach a member backend")
		}
	}
}

// receiveLeaderWait takes one event within a bound, without depending on another
// file's helpers.
func receiveLeaderWait(t *testing.T, sub WaitSubscription) WaitEvent {
	t.Helper()
	select {
	case ev, ok := <-sub.C():
		if !ok {
			t.Fatal("the subscription closed instead of delivering an event")
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no event arrived")
		return WaitEvent{}
	}
}

// TestWaitBusHeldWindowReachesEveryWaiter pins the ownership rule that keeps one
// consumer from starving another: the events held for a team are handed to every
// subscription, never consumed by whichever one arrives first. A window's notice
// path and a leader's wait both read the same registry bus, so a take-it-once
// window would let the first of them swallow a wakeup the second needed.
func TestWaitBusHeldWindowReachesEveryWaiter(t *testing.T) {
	bus := newWaitBus()
	defer bus.close()
	bus.Signal(leaderReportEvent("alpha-1"))

	first := bus.Subscribe("alpha")
	defer first.Close()
	if got := receiveLeaderWait(t, first); got.ID != "alpha-1" {
		t.Fatalf("first waiter = %+v, want the held event", got)
	}
	second := bus.Subscribe("alpha")
	defer second.Close()
	if got := receiveLeaderWait(t, second); got.ID != "alpha-1" {
		t.Fatalf("second waiter = %+v, want the same held event, not an emptied window", got)
	}
}

// TestLeaderWaitToolDoesNotRepeatAnEventItAlreadyReported pins the cost of that
// shared window: a later wait sees the held events again, and the waiter's own
// high-water sequence is what stops them being reported a second time. Without
// it every leader_wait would be woken instantly by the same stale batch, which
// is worse than the polling it replaced.
func TestLeaderWaitToolDoesNotRepeatAnEventItAlreadyReported(t *testing.T) {
	service, bus := leaderWaitFixture(t)
	wait := leaderWaitToolFrom(t, service)
	bus.Signal(leaderReportEvent("alpha-1"))

	first, err := wait.Execute(context.Background(), json.RawMessage(`{"timeout_seconds":1200}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "alpha-1") {
		t.Fatalf("the first wait missed the held event:\n%s", first)
	}
	// The parent deadline is what ends this one, so a wait that wrongly reported
	// the replay would say so instead of timing out.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	second, err := wait.Execute(ctx, json.RawMessage(`{"timeout_seconds":1200}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(second, "alpha-1") {
		t.Fatalf("the held event was reported to the same leader twice:\n%s", second)
	}
	if !strings.Contains(second, waitKindTimeout) {
		t.Fatalf("second wait = %q, want it to wait rather than answer with a replay", second)
	}
	// A genuinely new occurrence still wakes it.
	bus.Signal(leaderReportEvent("alpha-2"))
	third, err := wait.Execute(context.Background(), json.RawMessage(`{"timeout_seconds":1200}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(third, "alpha-2") {
		t.Fatalf("a new occurrence did not wake the wait:\n%s", third)
	}
}

// TestLeaderWaitBypassesThePlanBoundary pins the deliberate bypass. The wait
// only observes, so it reports itself plan-safe; internal/agent translates that
// report into the policy's tri-state before consulting it, and an Unsafe report
// is what the planning boundary blocks on. This asserts the decision the agent
// actually reaches, not just the one-word report.
func TestLeaderWaitBypassesThePlanBoundary(t *testing.T) {
	service, _ := leaderWaitFixture(t)
	wait := leaderWaitToolFrom(t, service)
	classifier, ok := wait.(tool.PlanModeClassifier)
	if !ok {
		t.Fatal("leader_wait must declare a plan-mode stance, or its safety is never reported")
	}
	safety := planmode.PlanSafetyUnsafe
	if classifier.PlanModeSafe() {
		safety = planmode.PlanSafetySafe
	}
	decision := (planmode.Policy{}).Decide(planmode.Call{
		Name:     "leader_wait",
		ReadOnly: wait.ReadOnly(),
		Safety:   safety,
		Args:     json.RawMessage(`{"timeout_seconds":1200}`),
	})
	if decision.Blocked {
		t.Fatalf("the wait must stay reachable while planning: %s", decision.Message)
	}
}

// TestLeaderWaitHoldsNoLockTheFramePathNeeds pins I3's half of the liveness
// contract. The wait parks on its own goroutine, and the frame thread's bus
// touches — a notice consumer subscribing, a teardown closing — must keep
// working while it is parked. A wait that held the bus lock across its park
// would stall the frame thread and let the watchdog escalate, which is exactly
// what I3 says a long wait must not do.
func TestLeaderWaitHoldsNoLockTheFramePathNeeds(t *testing.T) {
	bus := newWaitBus()
	done := make(chan error, 1)
	go func() {
		_, err := awaitLeaderWait(context.Background(), bus, "alpha", 0)
		done <- err
	}()
	waitForWaitSubscriber(t, bus)

	framePath := make(chan struct{})
	go func() {
		defer close(framePath)
		notice := bus.Subscribe("alpha")
		notice.Close()
		bus.close()
	}()
	select {
	case <-framePath:
	case <-time.After(2 * time.Second):
		t.Fatal("a parked wait held the bus lock: the frame path's own bus touches could not proceed")
	}
	select {
	case err := <-done:
		if !errors.Is(err, errLeaderWaitClosed) {
			t.Fatalf("teardown returned %v, want errLeaderWaitClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the parked wait was not released by the teardown")
	}
}
