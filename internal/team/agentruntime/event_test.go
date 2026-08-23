package agentruntime

import (
	"testing"
	"time"
)

func testKey() InstanceKey { return InstanceKey{Team: "t", MemberID: "m"} }

func drainEvents(t *testing.T, sub Subscription, kinds ...RuntimeEventKind) []RuntimeEvent {
	t.Helper()
	var out []RuntimeEvent
	deadline := time.After(3 * time.Second)
	for _, want := range kinds {
		select {
		case ev, ok := <-sub.C:
			if !ok {
				t.Fatalf("stream closed before %q (got %v)", want, out)
			}
			out = append(out, ev)
		case <-deadline:
			t.Fatalf("timed out waiting for %q (got %v)", want, out)
		}
	}
	return out
}

func TestEventSourceSequencesMonotonicallyPerInstance(t *testing.T) {
	s := newEventSource()
	sub := s.Subscribe()
	defer sub.Cancel()
	s.Publish(testKey(), EventDelta, "a")
	s.Publish(testKey(), EventDelta, "b")
	evs := drainEvents(t, sub, EventDelta, EventDelta)
	if evs[0].Sequence != 1 || evs[1].Sequence != 2 {
		t.Fatalf("sequences = %d, %d; want 1, 2", evs[0].Sequence, evs[1].Sequence)
	}
	for _, ev := range evs {
		if ev.Team != "t" || ev.MemberID != "m" {
			t.Fatalf("event identity = %s/%s, want t/m", ev.Team, ev.MemberID)
		}
	}
}

// TestEventSourceSlowConsumerDropsDeltasKeepsTerminals pins route §11.3: a
// full channel evicts older deltas so terminal events always land — the
// consumer may read stale deltas first (FIFO), but the terminal message and
// done event are never dropped.
func TestEventSourceSlowConsumerDropsDeltasKeepsTerminals(t *testing.T) {
	s := newEventSource()
	sub := s.Subscribe()
	// Do not read: flood deltas until the channel is full, then terminal
	// events must still be delivered (evicting deltas to make room).
	for range 200 {
		s.Publish(testKey(), EventDelta, "d")
	}
	s.Publish(testKey(), EventMessage, "final")
	s.Publish(testKey(), EventDone, "")
	// Keep reading until both terminal events arrive.
	var got []RuntimeEvent
	deadline := time.After(3 * time.Second)
	for len(got) < 2 || got[len(got)-1].Kind != EventDone {
		select {
		case ev, ok := <-sub.C:
			if !ok {
				t.Fatalf("stream closed before terminals (got %v)", got)
			}
			got = append(got, ev)
		case <-deadline:
			t.Fatalf("terminal events never delivered: %v", got)
		}
	}
	lastTwo := got[len(got)-2:]
	if lastTwo[0].Kind != EventMessage || lastTwo[0].Text != "final" {
		t.Fatalf("terminal events = %v, want message(final) then done", lastTwo)
	}
	if lastTwo[1].Kind != EventDone {
		t.Fatalf("final event = %v, want done", lastTwo[1].Kind)
	}
	// Deltas were actually dropped: the consumer never saw the full 200.
	if len(got) > 100 {
		t.Fatalf("no delta eviction happened: %d events received", len(got))
	}
}

// TestEventSourceSubscribeReplaysTerminals pins the late-subscriber rule: a
// new subscriber sees recent terminal events, so a consumer that starts after
// a failure still learns about it.
func TestEventSourceSubscribeReplaysTerminals(t *testing.T) {
	s := newEventSource()
	s.Publish(testKey(), EventError, "boom")
	s.Publish(testKey(), EventDelta, "late delta")
	sub := s.Subscribe()
	defer sub.Cancel()
	evs := drainEvents(t, sub, EventError)
	if evs[0].Text != "boom" {
		t.Fatalf("replayed error = %q, want boom", evs[0].Text)
	}
}

func TestEventSourceCancelClosesOnlyItsChannel(t *testing.T) {
	s := newEventSource()
	subA := s.Subscribe()
	subB := s.Subscribe()
	subA.Cancel()
	select {
	case _, ok := <-subA.C:
		if ok {
			t.Fatal("cancelled channel still open")
		}
	default:
		t.Fatal("cancelled channel not closed")
	}
	// B keeps receiving.
	s.Publish(testKey(), EventDelta, "b")
	if evs := drainEvents(t, subB, EventDelta); evs[0].Text != "b" {
		t.Fatalf("B event = %q, want b", evs[0].Text)
	}
	subB.Cancel()
}

func TestEventSourceCloseClosesAllAndPublishAfterCloseIsNoop(t *testing.T) {
	s := newEventSource()
	sub := s.Subscribe()
	s.Close()
	select {
	case _, ok := <-sub.C:
		if ok {
			t.Fatal("channel open after Close")
		}
	default:
		t.Fatal("channel not closed after Close")
	}
	s.Publish(testKey(), EventDelta, "after close") // must not panic
}
