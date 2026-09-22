package cli

// Bus acceptance tests for TEAM_LEADER_WAIT_ROUTE.md §8.2/§8.7: an event that
// preceded the subscription is held rather than lost, one occurrence is
// delivered once, and a teardown releases every waiter.

import (
	"strings"
	"testing"
	"time"
)

// waitEvent is a distinguishable occurrence for the bus tests.
func waitEvent(kind, team, summary string) WaitEvent {
	return WaitEvent{Kind: kind, Team: team, ID: summary, Summary: summary}
}

// receiveWaitEvent takes one event, failing the test if none arrives.
func receiveWaitEvent(t *testing.T, sub WaitSubscription) WaitEvent {
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

// TestWaitBusHoldsAnEventThatPrecededTheSubscription pins the check-then-wait
// window from the waiter's side: an occurrence that arrives with no waiter
// around waits for the next subscription, so a leader that starts waiting after
// its member already reported sees the report instead of sleeping through it.
func TestWaitBusHoldsAnEventThatPrecededTheSubscription(t *testing.T) {
	bus := newWaitBus()
	defer bus.close()
	bus.Signal(waitEvent("report", "alpha", "task t1 reported"))

	sub := bus.Subscribe("alpha")
	defer sub.Close()
	if got := receiveWaitEvent(t, sub); got.Summary != "task t1 reported" || got.Seq == 0 {
		t.Fatalf("held event = %+v, want the pre-subscription report with a sequence", got)
	}
	select {
	case ev := <-sub.C():
		t.Fatalf("the held event must be delivered once, got a second %+v", ev)
	default:
	}
}

// TestWaitBusDeliversOneOccurrenceOnce pins the duplicate suppression: the
// producer reports an occurrence in-process and the dispatcher later publishes
// the same occurrence off the board, and the waiter — which is subscribed the
// whole time — sees it exactly once.
func TestWaitBusDeliversOneOccurrenceOnce(t *testing.T) {
	bus := newWaitBus()
	defer bus.close()
	sub := bus.Subscribe("alpha")
	defer sub.Close()

	bus.Signal(waitEvent("report", "alpha", "task t1 reported"))
	bus.publish(waitEvent(waitKindWakeup, "alpha", "task t1 reported"))
	if got := receiveWaitEvent(t, sub); got.Kind != "report" {
		t.Fatalf("first delivery = %+v, want the producer's edge report", got)
	}
	select {
	case ev := <-sub.C():
		t.Fatalf("the board's copy of the same occurrence must be suppressed, got %+v", ev)
	default:
	}

	// A different occurrence is not suppressed by the first one's key.
	bus.publish(waitEvent(waitKindWakeup, "alpha", "task t2 reported"))
	if got := receiveWaitEvent(t, sub); got.Summary != "task t2 reported" {
		t.Fatalf("second occurrence = %+v, want the board-only report", got)
	}
}

// TestWaitBusScopesEventsToTheirTeam pins the filter: a registry serves several
// teams, and a waiter for one of them must not be woken by another's work — the
// other team's event stays held for its own waiter.
func TestWaitBusScopesEventsToTheirTeam(t *testing.T) {
	bus := newWaitBus()
	defer bus.close()
	bus.Signal(waitEvent("report", "beta", "task b1 reported"))

	alpha := bus.Subscribe("alpha")
	defer alpha.Close()
	select {
	case ev := <-alpha.C():
		t.Fatalf("alpha's waiter received beta's event: %+v", ev)
	default:
	}
	beta := bus.Subscribe("beta")
	defer beta.Close()
	if got := receiveWaitEvent(t, beta); got.Summary != "task b1 reported" {
		t.Fatalf("beta's waiter = %+v, want its own held event", got)
	}
}

// TestWaitBusKeepsTheArrivalOrder pins the audit order: the sequence a
// subscription sees is monotonic and matches the order the occurrences were
// signalled in, which is what lets a waiter report a burst in a stable order.
func TestWaitBusKeepsTheArrivalOrder(t *testing.T) {
	bus := newWaitBus()
	defer bus.close()
	sub := bus.Subscribe("alpha")
	defer sub.Close()

	for _, summary := range []string{"task t1 reported", "task t2 reported", "task t3 canceled"} {
		bus.Signal(waitEvent(waitKindWakeup, "alpha", summary))
	}
	var got []WaitEvent
	for range 3 {
		got = append(got, receiveWaitEvent(t, sub))
	}
	for i, ev := range got {
		if ev.Summary != []string{"task t1 reported", "task t2 reported", "task t3 canceled"}[i] {
			t.Fatalf("event %d = %+v, want the signalled order", i, ev)
		}
		if i > 0 && ev.Seq <= got[i-1].Seq {
			t.Fatalf("sequence went backwards: %d after %d", ev.Seq, got[i-1].Seq)
		}
	}
}

// TestWaitBusSignalsWithoutBlockingOnAFullWaiter pins the producer contract: a
// waiter that stopped reading must never stall the member goroutine reporting
// into the bus — the waiter is runnable again as soon as it reads, and the
// durable row covers whatever its full backlog dropped.
func TestWaitBusSignalsWithoutBlockingOnAFullWaiter(t *testing.T) {
	bus := newWaitBus()
	defer bus.close()
	sub := bus.Subscribe("alpha")
	defer sub.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range waitEventBacklog * 4 {
			bus.Signal(waitEvent("report", "alpha", "task t1 reported"))
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("signalling blocked on a waiter that was not reading")
	}
}

// TestWaitBusCloseReleasesEveryWaiter pins the teardown half of §8.7: the
// registry closes the bus with the backends, and every blocked waiter's channel
// closes with it, so no goroutine outlives the registry it was waiting on.
func TestWaitBusCloseReleasesEveryWaiter(t *testing.T) {
	bus := newWaitBus()
	sub := bus.Subscribe("alpha")
	bus.close()

	select {
	case _, ok := <-sub.C():
		if ok {
			t.Fatal("a released subscription must observe a closed channel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing the bus left the waiter blocked")
	}
	// A waiter that subscribes after teardown gets the same end signal rather
	// than a channel nothing will ever write to.
	late := bus.Subscribe("alpha")
	defer late.Close()
	if _, ok := <-late.C(); ok {
		t.Fatal("a subscription opened after teardown must be closed too")
	}
}

// TestWaitBusSummaryCarriesTheTaskID pins the shape a waiter switches on: the
// event names its kind and id, and its summary is the reason text the durable
// board row carries, because that text is what the two delivery paths are
// matched by.
func TestWaitBusSummaryCarriesTheTaskID(t *testing.T) {
	bus := newWaitBus()
	defer bus.close()
	sub := bus.Subscribe("alpha")
	defer sub.Close()
	bus.Signal(WaitEvent{Kind: "report", Team: "alpha", ID: "t1", Summary: "task t1 reported"})

	got := receiveWaitEvent(t, sub)
	if got.Kind != "report" || got.ID != "t1" || !strings.Contains(got.Summary, "t1") {
		t.Fatalf("event = %+v, want the kind, the task id and the reason text", got)
	}
}
