package cli

import (
	"sync"

	"reasonix/internal/event"
)

// memberEventPump is the fan-in hub every member backend emits into: one bounded
// queue per member, drained by the TUI's single event pump in global arrival
// order. N members streaming at once therefore cost N queues instead of N blocked
// goroutines — the blocking shared channel this replaces coupled every member's
// agent loop to the window's update rate, so one stalled window froze the fleet.
//
// Two policies keep a queue bounded without losing turn state: streaming deltas
// (Text/Reasoning/ToolProgress) coalesce into their pending neighbour on the same
// stream, so a token burst costs one slot; and overflow evicts the oldest delta
// first, then the oldest non-protected state event, and never a protected one
// (TurnStarted/Message/TurnDone/ApprovalRequest/AskRequest) — a dropped delta
// loses pixels, a dropped TurnDone loses a turn.
type memberEventPump struct {
	mu     sync.Mutex
	queues map[string][]memberEvent
	// seq stamps every queued event with its arrival order: the drain serves the
	// globally oldest pending event, exactly the shared channel's old order, so
	// one chatty member's backlog can never jump another member's event.
	seq int64
	// wake has capacity 1: it is a nudge, not a count. A nudge dropped because
	// one is already pending is harmless — the consumer re-checks its queues on
	// every wakeup, and a later push re-creates it.
	wake   chan struct{}
	closed bool
	// dropped counts the events the policy discarded under overflow, whether it
	// refused the incoming one or evicted a queued one. The UI path never reads
	// it; tests use it to pin the policy, and diagnostics can surface it.
	dropped int
	// held is the retain area for events the bound window should not see yet:
	// background members' streamed deltas, parked so they cost no frame. See
	// team_member_event_batch.go.
	held map[string][]memberEventHeld
}

// memberEventQueueCap bounds one member's pending queue before eviction starts.
// With delta coalescing a healthy member stays far below it, so crossing the cap
// means the consumer stopped draining and the eviction policy below decides what
// degrades.
const memberEventQueueCap = 4096

// newMemberEventPump returns an empty pump.
func newMemberEventPump() *memberEventPump {
	return &memberEventPump{
		queues: map[string][]memberEvent{},
		held:   map[string][]memberEventHeld{},
		wake:   make(chan struct{}, 1),
	}
}

// sink adapts one member's backend onto the pump. The returned sink never
// blocks, so the agent's run goroutine is never backpressured by the window.
func (p *memberEventPump) sink(member string) event.Sink {
	if p == nil {
		return event.Discard
	}
	return event.FuncSink(func(e event.Event) { p.push(member, e) })
}

// push enqueues one event under the coalescing and eviction policies. It is
// safe for concurrent callers: every member backend emits from its own run
// goroutine.
func (p *memberEventPump) push(member string, ev event.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	q := p.queues[member]
	if n := len(q); n > 0 && mergeMemberDelta(&q[n-1].ev, ev) {
		p.queues[member] = q
		return
	}
	if len(q) >= memberEventQueueCap {
		// Evicting frees a slot at the cost of one queued event; refusing the
		// arrival costs this one. Only an all-protected queue grows past the cap
		// — the consumer is gone, and turn state is worth more than memory.
		switch evicted := evictMemberOverflow(&q); {
		case evicted:
			p.dropped++
		case !memberEventProtected(ev.Kind):
			p.dropped++
			return
		}
	}
	p.seq++
	p.queues[member] = append(q, memberEvent{member: member, ev: ev, seq: p.seq})
	p.nudgeLocked()
}

// next blocks until one event is ready, then returns it with ok=true. ok is
// false once the pump is closed, so the caller stops pumping instead of
// ingesting a phantom zero event into the bound member's transcript.
func (p *memberEventPump) next() (memberEvent, bool) {
	if p == nil {
		return memberEvent{}, false
	}
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return memberEvent{}, false
		}
		if _, ready, ok := p.nextLocked(); ok {
			p.mu.Unlock()
			return ready, true
		}
		p.mu.Unlock()
		<-p.wake
	}
}

// close stops the pump: later pushes are dropped and a blocked next returns.
// Idempotent, so a teardown racing another teardown is harmless.
func (p *memberEventPump) close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	p.nudgeLocked()
}

// nextLocked pops the globally oldest pending event. Caller holds the lock;
// the scan is over members, not events, so it stays O(members) per delivery.
func (p *memberEventPump) nextLocked() (string, memberEvent, bool) {
	member, best := "", memberEvent{}
	for candidate, q := range p.queues {
		if len(q) == 0 {
			continue
		}
		if member == "" || q[0].seq < best.seq {
			member, best = candidate, q[0]
		}
	}
	if member == "" {
		return "", memberEvent{}, false
	}
	if len(p.queues[member]) == 1 {
		delete(p.queues, member)
	} else {
		p.queues[member] = p.queues[member][1:]
	}
	return member, best, true
}

// nudgeLocked wakes a blocking next. Caller holds the lock.
func (p *memberEventPump) nudgeLocked() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// memberEventDelta reports whether one kind is a streaming delta: high-frequency
// content that coalesces and is safe to drop, because its merged neighbour (or
// the turn's Message event) still carries the text.
func memberEventDelta(kind event.Kind) bool {
	switch kind {
	case event.Text, event.Reasoning, event.ToolProgress:
		return true
	}
	return false
}

// memberEventProtected reports whether one kind is turn state the window derives
// user-visible state from — an unread badge, a prompt card, a transcript commit
// — and which the queue therefore never evicts to make room.
func memberEventProtected(kind event.Kind) bool {
	switch kind {
	case event.TurnStarted, event.Message, event.TurnDone, event.ApprovalRequest, event.AskRequest:
		return true
	}
	return false
}

// mergeMemberDelta folds src into dst when both belong to the same delta stream,
// appending text (or a tool's progress chunk) so the consumer sees the same
// bytes in one event instead of thousands. It reports whether it merged; a kind
// or stream mismatch leaves dst untouched and the caller queues separately.
func mergeMemberDelta(dst *event.Event, src event.Event) bool {
	if dst == nil || dst.Kind != src.Kind {
		return false
	}
	switch src.Kind {
	case event.Text, event.Reasoning:
		dst.Text += src.Text
	case event.ToolProgress:
		if dst.Tool.ID != src.Tool.ID {
			return false
		}
		dst.Tool.Output += src.Tool.Output
	default:
		return false
	}
	return true
}

// evictMemberOverflow frees one queue slot for the oldest event the policy is
// willing to lose: a delta first, then the oldest non-protected state event. It
// reports false when every queued event is protected, leaving the queue intact.
func evictMemberOverflow(q *[]memberEvent) bool {
	if q == nil || len(*q) == 0 {
		return false
	}
	events := *q
	for i := range events {
		if memberEventDelta(events[i].ev.Kind) {
			*q = append(events[:i:i], events[i+1:]...)
			return true
		}
	}
	for i := range events {
		if !memberEventProtected(events[i].ev.Kind) {
			*q = append(events[:i:i], events[i+1:]...)
			return true
		}
	}
	return false
}
