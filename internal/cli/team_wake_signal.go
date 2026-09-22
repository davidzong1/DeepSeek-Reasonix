package cli

import (
	"sync"
	"time"
)

// Event kinds the cli side produces. A member's report, cancel and refused
// dispatch carry agentruntime's Attention* labels instead; these three are the
// occurrences only the host knows about.
const (
	// waitKindWakeup labels an event recovered from the board's durable wakeup
	// rows rather than reported in-process. Its summary is the board's own reason
	// text, which is what the durable copy and the producer's edge trigger share.
	waitKindWakeup = "wakeup"
	// waitKindEscalation labels a member's write-access request that needs the
	// leader's decision; its id is the request id the decision names.
	waitKindEscalation = "escalation"
	// waitKindInput labels input the composer handed to a busy leader's context
	// window, which is the leader's cue to finish its turn and take it.
	waitKindInput = "input"
)

// WaitEvent is one occurrence a leader-side waiter cares about: a member's
// report, cancel or refused dispatch, an authorization request that needs the
// leader's decision, or input handed to the leader's context window. Kind names
// which of those it is; ID names the subject (the task or request id) when the
// producer has one; Summary is the human-readable reason, and it is byte-equal
// to the durable wakeup summary the same occurrence writes on the board — that
// equality is what lets the bus drop the durable copy of an event it already
// delivered in-process. Seq orders the events a subscription receives.
type WaitEvent struct {
	Kind    string
	Team    string
	ID      string
	Summary string
	Seq     uint64
}

// WaitSubscription is one waiter's view of the bus: a channel of events for the
// team it subscribed to, and the release that gives the slot back. The channel
// closes when the subscription is released or the bus is torn down, which is how
// a waiter learns to stop instead of blocking forever.
type WaitSubscription interface {
	C() <-chan WaitEvent
	Close()
}

// WaitSignal is the frozen coupling between the two halves of the leader-wait
// route: producers (the task runtime, the escalation queue, the composer) call
// Signal, waiters subscribe. Signal is an edge trigger and never durable — the
// board's wakeup rows are — so an occurrence that arrives with no waiter around
// is held for the next subscription instead of dropped, and the durable drain
// covers the producers that never signal at all.
type WaitSignal interface {
	Signal(WaitEvent)
	Subscribe(team string) WaitSubscription
}

const (
	// waitEventBacklog bounds one subscription's queued events. A waiter returns
	// as soon as its first event lands, so the backlog only has to hold the burst
	// a single slice produces.
	waitEventBacklog = 32
	// waitRecentLimit and waitRecentWindow bound the in-process duplicate
	// suppression window: how many summaries are remembered, and for how long.
	// The window has to outlive the gap between an in-process signal and the
	// dispatcher draining the same occurrence off the board (one tick, plus a
	// board write), which seconds cover and minutes make generous.
	waitRecentLimit  = 256
	waitRecentWindow = 5 * time.Minute
	// waitPendingLimit bounds the events held for a waiter that has not arrived
	// yet. An occurrence nobody was subscribed for is kept, not dropped: a leader
	// that starts waiting after its member already reported must see the report
	// rather than sleep through it. The oldest are the ones to lose, because the
	// board's durable row for them is already consumed.
	waitPendingLimit = 32
)

// waitBus broadcasts WaitEvents to the waiters of one registry. It is
// deliberately tiny and free of I/O: Signal runs on a member's completion
// goroutine and on the composer's frame path, so it may not read the board, take
// a lock anyone else holds, or wait for a subscriber.
type waitBus struct {
	mu     sync.Mutex
	seq    uint64
	closed bool
	subs   map[*waitSub]struct{}
	// recent remembers the (team, summary) pairs already fanned out, so the
	// dispatcher's durable copy of an occurrence is not delivered twice.
	recent map[string]time.Time
	// pending holds the occurrences that arrived with nobody subscribed for
	// them. Every subscription for their team is served from it when it is
	// created, so a second consumer cannot take a wakeup away from the first.
	pending []WaitEvent
}

// waitSub is one waiter's slot: the team it wants events for ("" for every
// team), its buffered channel, and whether the slot is still live.
type waitSub struct {
	bus    *waitBus
	team   string
	ch     chan WaitEvent
	closed bool
}

func newWaitBus() *waitBus {
	return &waitBus{subs: map[*waitSub]struct{}{}, recent: map[string]time.Time{}}
}

// Signal reports one occurrence to every waiter of the event's team. It is the
// producer-side entry point and must not block: a waiter whose backlog is full
// is already runnable, so the event is dropped for it rather than waiting, and
// an occurrence with no waiter at all is held for the next one.
func (b *waitBus) Signal(ev WaitEvent) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.rememberLocked(ev)
	b.fanOutLocked(ev)
}

// publish hands one durable drain result to the waiters. It is the dispatcher's
// entry point, and unlike Signal it drops an occurrence the bus already carried
// in-process — the same report reaches the bus twice on purpose (once as the
// member's edge trigger, once from the board's durable row), and the waiter must
// see it once.
func (b *waitBus) publish(ev WaitEvent) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	if key := waitEventKey(ev); b.seenLocked(key) {
		return
	}
	b.rememberLocked(ev)
	b.fanOutLocked(ev)
}

// Subscribe returns one waiter's channel. The subscription delivers only its
// team's events (every team's when team is empty), and it starts with whatever
// already happened for that team: an occurrence that arrived with no waiter
// around is held for the next one, so a leader that starts waiting after its
// member reported sees the report instead of sleeping through it. A torn-down
// bus hands back an already-closed subscription: a waiter that subscribes after
// teardown must stop, never block.
//
// The held events are handed to *every* subscription for their team rather than
// consumed by the first, so one consumer cannot take a wakeup away from another.
// A waiter that subscribes repeatedly therefore sees the held window again, and
// drops what it has already reported by sequence (see leaderWaitTool.afterSeq).
func (b *waitBus) Subscribe(team string) WaitSubscription {
	sub := &waitSub{team: team}
	if b == nil {
		close(sub.ch)
		sub.closed = true
		return sub
	}
	sub.bus = b
	sub.ch = make(chan WaitEvent, waitEventBacklog)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		close(sub.ch)
		sub.closed = true
		return sub
	}
	for _, ev := range b.retainedForLocked(team) {
		select {
		case sub.ch <- ev:
		default: // a fresh channel of waitEventBacklog cannot overflow here
		}
	}
	b.subs[sub] = struct{}{}
	return sub
}

// close releases every subscription: each waiter's channel closes, so a blocked
// wait returns instead of leaking a goroutine past the registry that owned it.
func (b *waitBus) close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for sub := range b.subs {
		sub.closed = true
		close(sub.ch)
	}
	b.subs = map[*waitSub]struct{}{}
}

// Close releases one subscription. It is idempotent, and safe to call from the
// waiter's own goroutine while producers keep signalling.
func (s *waitSub) Close() {
	if s == nil || s.bus == nil {
		return
	}
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	delete(s.bus.subs, s)
	close(s.ch)
}

// C returns the subscription's event channel.
func (s *waitSub) C() <-chan WaitEvent {
	if s == nil {
		return nil
	}
	return s.ch
}

// fanOutLocked delivers one event to every live subscription of its team and
// stamps it with the bus sequence. An event nobody was subscribed for is kept
// for the next subscription rather than dropped (see pending). Caller holds the
// bus lock.
func (b *waitBus) fanOutLocked(ev WaitEvent) {
	b.seq++
	ev.Seq = b.seq
	matched := false
	for sub := range b.subs {
		if sub.team != "" && sub.team != ev.Team {
			continue
		}
		matched = true
		select {
		case sub.ch <- ev:
		default: // full backlog: the waiter is already runnable, drop this copy
		}
	}
	if !matched {
		b.retainLocked(ev)
	}
}

// retainLocked keeps one undispatched event for the next subscriptions of its
// team, dropping the oldest once the window is full. Caller holds the bus lock.
func (b *waitBus) retainLocked(ev WaitEvent) {
	b.pending = append(b.pending, ev)
	if len(b.pending) > waitPendingLimit {
		b.pending = b.pending[len(b.pending)-waitPendingLimit:]
	}
}

// retainedForLocked returns a copy of the held events a subscription for team
// would want, leaving the held window intact for any other consumer. Caller
// holds the bus lock.
func (b *waitBus) retainedForLocked(team string) []WaitEvent {
	if len(b.pending) == 0 {
		return nil
	}
	var out []WaitEvent
	for _, ev := range b.pending {
		if team == "" || ev.Team == team {
			out = append(out, ev)
		}
	}
	return out
}

// rememberLocked records one occurrence as delivered, pruning the window once it
// outgrows its cap. Caller holds the bus lock.
func (b *waitBus) rememberLocked(ev WaitEvent) {
	if b.recent == nil {
		b.recent = map[string]time.Time{}
	}
	now := time.Now()
	if len(b.recent) >= waitRecentLimit {
		for key, at := range b.recent {
			if now.Sub(at) > waitRecentWindow {
				delete(b.recent, key)
			}
		}
	}
	b.recent[waitEventKey(ev)] = now
}

// seenLocked reports whether this occurrence was already delivered inside the
// window. Caller holds the bus lock.
func (b *waitBus) seenLocked(key string) bool {
	at, ok := b.recent[key]
	if !ok {
		return false
	}
	if time.Since(at) > waitRecentWindow {
		delete(b.recent, key)
		return false
	}
	return true
}

// waitEventKey identifies one occurrence for duplicate suppression. It is the
// team and the summary, never the kind: the producer's edge trigger and the
// board's durable row describe the same occurrence with different kinds but the
// same reason text, and that text carries the task id the occurrence is about.
func waitEventKey(ev WaitEvent) string { return ev.Team + "\x00" + ev.Summary }
