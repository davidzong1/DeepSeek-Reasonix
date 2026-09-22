// Part A (TEAM_FRAME_PATH_COST_ROUTE.md F-A1/F-A2): the member-event batch
// drain and the retain area that keeps a background member's streamed deltas
// off the event loop. Same package as the pump so the policies stay testable
// without an Update loop.
package cli

import (
	"reasonix/internal/event"
)

// memberEventBatchLimit caps how many already-queued events one Update
// coalesces. It is a bound, not a target: the point of draining is that one
// View() covers a whole burst instead of one per event, and the cap is what
// keeps an unbounded backlog from delaying the keystroke behind it.
const memberEventBatchLimit = 64

// memberLiveEventCap bounds one background member's retained turn; it is
// declared next to the hold policy it belongs to (see hold).

// memberEventHeld is one event taken off the queue for a window that should not
// see it yet. It keeps the pump's arrival stamp so a replay preserves the order
// the member produced, and the events of one member's turn are not interleaved
// with another's by the hold.
type memberEventHeld struct {
	member string
	ev     event.Event
	seq    int64
}

// drainReady takes up to limit already-queued events in global arrival order,
// without waiting. An empty result is the common case: a burst is what this
// exists for, and a lone event is simply a batch of one.
func (p *memberEventPump) drainReady(limit int) []memberEvent {
	if p == nil || limit <= 0 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	var out []memberEvent
	for len(out) < limit {
		_, ready, ok := p.nextLocked()
		if !ok {
			break
		}
		out = append(out, ready)
	}
	return out
}

// hold keeps one event for a later replay, under the same bound and drop-oldest
// policy the queue uses. It is the second half of the retain area: the command
// side pulls an invisible event off the queue and parks it here, so it is never
// dropped for being old and never costs a frame.
//
// TurnDone clears the member's hold instead: the settled turn is in the member's
// own History() from then on, and replaying both would show it twice.
func (p *memberEventPump) hold(ev memberEvent) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	if ev.ev.Kind == event.TurnDone {
		delete(p.held, ev.member)
		return
	}
	if ev.ev.Kind == event.ApprovalRequest || ev.ev.Kind == event.AskRequest {
		// ReplayPendingPrompts re-raises a prompt on bind; holding it too would
		// raise the same decision card twice on one switch.
		return
	}
	held := append(p.held[ev.member], memberEventHeld(ev))
	if over := len(held) - memberLiveEventCap; over > 0 {
		held = held[over:]
	}
	if p.held == nil {
		p.held = map[string][]memberEventHeld{}
	}
	p.held[ev.member] = held
}

// heldTurn returns one member's retained turn in arrival order, without
// clearing it. A switch replays it on top of the member's committed history.
func (p *memberEventPump) heldTurn(member string) []event.Event {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	held := p.held[member]
	out := make([]event.Event, 0, len(held))
	for _, item := range held {
		out = append(out, item.ev)
	}
	return out
}

// dropHeld forgets one member's retained turn. Replaying is what clears it, so
// a second switch cannot show the same turn twice.
func (p *memberEventPump) dropHeld(member string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.held, member)
}

// dropAllHeld forgets every retained turn. A hold belongs to the member
// backends that produced it, so it ends with the pump those backends write into.
func (p *memberEventPump) dropAllHeld() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.held = nil
}
