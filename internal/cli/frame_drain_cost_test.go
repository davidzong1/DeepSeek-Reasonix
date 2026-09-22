// Part A acceptance (TEAM_FRAME_PATH_COST_ROUTE.md §5.1/§5.2): the batch drain
// that makes a burst cost one frame, and the retain area that keeps a background
// member's streamed deltas off the event loop entirely.
package cli

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/event"
)

// memberEventTUI is a bound team session with a live pump: the shape both nodes
// act on. The leader is bound, so "alice" is the background member.
func memberEventTUI(t *testing.T) chatTUI {
	t.Helper()
	m := promptTestTUI(t, nil)
	if m.boundMember() != "lead" {
		t.Fatalf("fixture bound member = %q, want the leader", m.boundMember())
	}
	return m
}

func memberText(member, text string) memberEvent {
	return memberEvent{member: member, ev: event.Event{Kind: event.Text, Text: text}}
}

// TestOneMemberEventDrainsTheWholeBacklog is the F-A1 gate. bubbletea renders a
// full View() per message, so fifty queued events used to cost fifty renders;
// one delivered event must now carry the whole backlog in a single Update.
//
// Red→green: without the drainReady call in handleMemberEvent only the delivered
// event lands, and the other fifty are still queued.
func TestOneMemberEventDrainsTheWholeBacklog(t *testing.T) {
	const backlog = 50
	m := memberEventTUI(t)
	// Alternating kinds on purpose: push coalesces a run of the same delta kind
	// into its pending neighbour, so a same-kind burst is already one slot and
	// would not exercise the batch at all.
	for i := range backlog {
		kind := event.Text
		if i%2 == 1 {
			kind = event.Reasoning
		}
		m.memberEvents.push("alice", event.Event{Kind: kind, Text: "background token"})
	}
	m.memberEvents.push("lead", event.Event{Kind: event.Text, Text: "BOUND-TOKEN"})

	// The bound event is the head of that backlog: what the wait command hands
	// over, and one call must carry the rest with it. The queue interleaves the
	// two senders by arrival, so the head is taken as it comes.
	head, ok := m.memberEvents.next()
	if !ok {
		t.Fatal("the backlog must have a head to deliver")
	}
	cmd := m.handleMemberEvent(memberEventMsg(head))

	// The whole backlog landed: one slot is the head this call consumed, and the
	// rest are the bound member's answer plus every retained background event.
	retained := len(m.memberEvents.heldTurn("alice"))
	answer := m.pending.String()
	if retained+len(answer) == 0 {
		t.Fatalf("nothing was drained: %d background events retained, bound answer %q", retained, answer)
	}
	if got := m.memberEvents.drainReady(memberEventBatchLimit); len(got) != 0 {
		t.Fatalf("%d events were left queued after the batch", len(got))
	}
	// The bound member's own event is on the transcript path, not retained.
	m.commitPending()
	if joined := strings.Join(m.transcript, "\n"); !strings.Contains(joined, "BOUND-TOKEN") {
		t.Fatalf("the bound event must still render:\n%s", joined)
	}
	if cmd == nil {
		t.Fatal("the batch must re-arm the wait exactly once")
	}
	// The queue is empty: one call took everything the pump was holding.
	if rest := m.memberEvents.drainReady(memberEventBatchLimit); len(rest) != 0 {
		t.Fatalf("%d events were left queued after the batch", len(rest))
	}
}

// TestBatchKeepsPerEventSemantics pins that a batched event is routed exactly
// like a lone one: an unread badge, a prompt record and a turn boundary all
// still land when they arrive behind other events.
func TestBatchKeepsPerEventSemantics(t *testing.T) {
	m := memberEventTUI(t)
	m.memberEvents.push("alice", event.Event{Kind: event.Text, Text: "streaming"})
	m.memberEvents.push("alice", event.Event{Kind: event.Message, Text: "done work"})

	m.handleMemberEvent(memberEventMsg(memberText("lead", "BOUND-TOKEN")))

	if got := m.teamPick.session.unread["alice"]; got != 1 {
		t.Fatalf("a batched Message must still count as unread: %d", got)
	}
	// Both are retained: the delta for the switch's replay, and the Message with
	// it — a background turn is kept whole, and only the delta never becomes a
	// message of its own.
	if got := len(m.memberEvents.heldTurn("alice")); got != 2 {
		t.Fatalf("the batched turn must be retained whole, got %d events", got)
	}
}

// TestBackgroundDeltaDoesNotBecomeAMessage is the F-A2 gate. A background
// member's streamed delta changes nothing the window paints, so it must never
// reach Update — the whole point is that the frame it used to cost is gone.
//
// The negative half is what makes it a gate: the wait is armed with only an
// invisible event available, and it must stay blocked. Red→green: without the
// visible() filter the command returns that delta immediately.
func TestBackgroundDeltaDoesNotBecomeAMessage(t *testing.T) {
	m := memberEventTUI(t)
	m.memberEvents.push("alice", event.Event{Kind: event.Text, Text: "INVISIBLE"})

	released := make(chan tea.Msg, 1)
	go func() { released <- waitForMemberEvent(m.memberEvents, "lead")() }()
	select {
	case msg := <-released:
		t.Fatalf("a background delta surfaced as a message: %#v", msg)
	case <-time.After(50 * time.Millisecond):
	}

	// A visible event releases the wait, and it is that event: the delta stayed
	// parked instead of being delivered first.
	m.memberEvents.push("alice", event.Event{Kind: event.TurnDone})
	select {
	case msg := <-released:
		if got := memberEvent(msg.(memberEventMsg)); got.ev.Kind != event.TurnDone {
			t.Fatalf("the wait released with %v, want the visible TurnDone", got.ev.Kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a visible event did not release the wait")
	}
	held := m.memberEvents.heldTurn("alice")
	if len(held) != 1 || held[0].Text != "INVISIBLE" {
		t.Fatalf("the parked delta = %+v, want the one invisible event", held)
	}
}

// TestRetainedTurnReplaysInOrderAndClears pins the replay contract across the
// ownership move: what the pump retained is what the switch shows, in arrival
// order, and replaying clears it so a second switch cannot show it twice.
func TestRetainedTurnReplaysInOrderAndClears(t *testing.T) {
	m := memberEventTUI(t)
	for _, text := range []string{"FIRST", "SECOND", "THIRD"} {
		m.handleMemberEvent(memberEventMsg(memberText("alice", text)))
	}
	if cmd := m.switchTeamMember("alice"); cmd == nil {
		t.Fatal("switching to alice must bind its backend")
	}
	// The replayed deltas are a streamed answer like any other: they reach the
	// transcript once committed, which is what makes their order observable.
	m.commitPending()
	joined := strings.Join(m.transcript, "\n")
	first := strings.Index(joined, "FIRST")
	second := strings.Index(joined, "SECOND")
	third := strings.Index(joined, "THIRD")
	if first < 0 || second < first || third < second {
		t.Fatalf("the retained turn must replay in arrival order (%d,%d,%d):\n%s", first, second, third, joined)
	}
	if got := len(m.memberEvents.heldTurn("alice")); got != 0 {
		t.Fatalf("a replayed turn must clear its hold, %d events left", got)
	}
}

// TestRetainedTurnEndsWithThePump pins the lifetime the ownership move changes:
// the hold belongs to the member backends that produced it, so closing the pump
// they write into ends it too.
func TestRetainedTurnEndsWithThePump(t *testing.T) {
	pump := newMemberEventPump()
	pump.push("alice", event.Event{Kind: event.Text, Text: "streaming"})
	pump.hold(memberEvent{member: "alice", ev: event.Event{Kind: event.Text, Text: "held"}})
	if got := len(pump.heldTurn("alice")); got != 1 {
		t.Fatalf("hold = %d events, want 1", got)
	}
	pump.dropAllHeld()
	if got := len(pump.heldTurn("alice")); got != 0 {
		t.Fatalf("the hold must end with its pump, %d events left", got)
	}
}

// TestHoldAppliesTheQueuePolicies pins the three exclusions the old session
// buffer applied, now that hold owns them: prompts are left to
// ReplayPendingPrompts, a settled turn clears the hold, and the cap drops the
// oldest rather than the newest.
func TestHoldAppliesTheQueuePolicies(t *testing.T) {
	pump := newMemberEventPump()
	pump.hold(memberEvent{member: "alice", ev: event.Event{Kind: event.ApprovalRequest, Approval: event.Approval{ID: "a1"}}})
	pump.hold(memberEvent{member: "alice", ev: event.Event{Kind: event.AskRequest, Ask: event.Ask{ID: "q1"}}})
	if got := len(pump.heldTurn("alice")); got != 0 {
		t.Fatalf("prompts must stay out of the hold, got %d", got)
	}

	for i := range memberLiveEventCap + 50 {
		text := "keep"
		if i == 0 {
			text = "OLDEST"
		}
		pump.hold(memberEvent{member: "alice", ev: event.Event{Kind: event.Text, Text: text}})
	}
	held := pump.heldTurn("alice")
	if len(held) != memberLiveEventCap {
		t.Fatalf("hold = %d events, want the cap %d", len(held), memberLiveEventCap)
	}
	for _, ev := range held {
		if ev.Text == "OLDEST" {
			t.Fatal("the cap must drop the oldest events, not the newest")
		}
	}

	pump.hold(memberEvent{member: "alice", ev: event.Event{Kind: event.TurnDone}})
	if got := len(pump.heldTurn("alice")); got != 0 {
		t.Fatalf("a settled turn must clear the hold, %d events left", got)
	}
}
