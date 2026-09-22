package cli

import (
	"testing"
	"time"

	"reasonix/internal/event"
)

// TestMemberEventPumpCoalescesStreamingDeltas pins the burst-halving half of the
// pump contract: consecutive deltas on one member's stream collapse into the
// pending neighbour, so a token burst costs one queue slot, while a state event
// between two deltas keeps them apart (they belong to different messages).
func TestMemberEventPumpCoalescesStreamingDeltas(t *testing.T) {
	pump := newMemberEventPump()
	sink := pump.sink("alice")
	sink.Emit(event.Event{Kind: event.Text, Text: "hel"})
	sink.Emit(event.Event{Kind: event.Text, Text: "lo "})
	sink.Emit(event.Event{Kind: event.ToolProgress, Tool: event.Tool{ID: "t1", Output: "chunk"}})
	sink.Emit(event.Event{Kind: event.ToolProgress, Tool: event.Tool{ID: "t1", Output: "2"}})
	sink.Emit(event.Event{Kind: event.ToolProgress, Tool: event.Tool{ID: "t2", Output: "other"}})
	sink.Emit(event.Event{Kind: event.Message, Text: "hello chunk2"})
	sink.Emit(event.Event{Kind: event.Text, Text: "tail"})

	var got []memberEvent
	for {
		ev, ok := pump.next()
		if !ok {
			t.Fatal("pump closed while draining")
		}
		got = append(got, ev)
		if len(got) == 5 {
			break
		}
	}
	if len(got) != 5 {
		t.Fatalf("drained %d events, want 5 coalesced groups", len(got))
	}
	if got[0].ev.Kind != event.Text || got[0].ev.Text != "hello " {
		t.Errorf("text group = %+v, want the two deltas merged", got[0].ev)
	}
	if got[1].ev.Kind != event.ToolProgress || got[1].ev.Tool.ID != "t1" || got[1].ev.Tool.Output != "chunk2" {
		t.Errorf("tool progress group = %+v, want t1's two chunks merged", got[1].ev)
	}
	if got[2].ev.Tool.ID != "t2" || got[2].ev.Tool.Output != "other" {
		t.Errorf("second tool's progress must not merge into the first's: %+v", got[2].ev)
	}
	if got[3].ev.Kind != event.Message {
		t.Errorf("state event order = %+v, want the Message before the tail text", got[3].ev)
	}
	if got[4].ev.Kind != event.Text || got[4].ev.Text != "tail" {
		t.Errorf("tail text = %+v, want a fresh group after the Message", got[4].ev)
	}
}

// TestMemberEventPumpNeverBlocksANonDrainingConsumer pins the reason the pump
// exists: a member whose queue overflows is never backpressured. Pushing far
// past the cap returns promptly, the queue stays bounded, and protected turn
// state survives the eviction of everything around it.
func TestMemberEventPumpNeverBlocksANonDrainingConsumer(t *testing.T) {
	pump := newMemberEventPump()
	sink := pump.sink("alice")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range memberEventQueueCap + 200 {
			// Alternating kinds defeat coalescing, so the queue really fills.
			if i%2 == 0 {
				sink.Emit(event.Event{Kind: event.Notice, Text: "notice"})
				continue
			}
			sink.Emit(event.Event{Kind: event.Text, Text: "delta"})
		}
		sink.Emit(event.Event{Kind: event.TurnDone})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Emit blocked on a consumer that never drained")
	}
	if got := len(pump.queues["alice"]); got > memberEventQueueCap+1 {
		t.Fatalf("queue length = %d, want it bounded near the cap", got)
	}
	if pump.dropped == 0 {
		t.Fatal("overflow must evict (dropped counter stayed zero)")
	}
	var sawDone bool
	for {
		ev, ok := pump.next()
		if !ok {
			t.Fatal("pump closed while draining")
		}
		if ev.ev.Kind == event.TurnDone {
			sawDone = true
		}
		if len(pump.queues["alice"]) == 0 {
			break
		}
	}
	if !sawDone {
		t.Fatal("TurnDone was evicted; protected turn state must survive overflow")
	}
}

// TestMemberEventPumpServesLongestWaitingMemberFirst pins the fairness rule: a
// member's backlog cannot starve another member's single event, because the
// drain follows arrival order across members, not whichever queue is longest.
func TestMemberEventPumpServesLongestWaitingMemberFirst(t *testing.T) {
	pump := newMemberEventPump()
	pump.sink("alice").Emit(event.Event{Kind: event.Message, Text: "a1"})
	pump.sink("bob").Emit(event.Event{Kind: event.Message, Text: "b1"})
	pump.sink("alice").Emit(event.Event{Kind: event.Message, Text: "a2"})
	var order []string
	for range 3 {
		ev, ok := pump.next()
		if !ok {
			t.Fatal("pump closed while draining")
		}
		order = append(order, ev.ev.Text)
	}
	want := []string{"a1", "b1", "a2"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("drain order = %v, want arrival order %v", order, want)
		}
	}
}

// TestMemberEventPumpCloseUnblocksNext pins teardown: a pump waiting for work
// must release its consumer when the seam is torn down, and later pushes must be
// dropped instead of resurrecting a queue behind a closed window.
func TestMemberEventPumpCloseUnblocksNext(t *testing.T) {
	pump := newMemberEventPump()
	got := make(chan bool, 1)
	go func() {
		_, ok := pump.next()
		got <- ok
	}()
	// Let the consumer reach its wait before closing, so the test measures the
	// close itself rather than a race with the goroutine's start.
	deadline := time.After(2 * time.Second)
	for {
		pump.mu.Lock()
		waiting := len(pump.queues) == 0
		pump.mu.Unlock()
		if waiting {
			break
		}
		select {
		case <-deadline:
			t.Fatal("consumer never reached its wait")
		default:
		}
	}
	pump.close()
	select {
	case ok := <-got:
		if ok {
			t.Fatal("next must report the pump closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close did not wake the blocked consumer")
	}
	pump.sink("alice").Emit(event.Event{Kind: event.Message, Text: "late"})
	if got := len(pump.queues["alice"]); got != 0 {
		t.Fatalf("a closed pump queued %d events", got)
	}
}
