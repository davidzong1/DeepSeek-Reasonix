package agentruntime

import (
	"sync"
	"time"
)

// RuntimeEventKind classifies one member-runtime event (route §11.3).
type RuntimeEventKind string

const (
	EventStarted RuntimeEventKind = "started" // completion started; Text empty
	EventDelta   RuntimeEventKind = "delta"   // streaming increment; may be dropped for slow consumers
	EventMessage RuntimeEventKind = "message" // complete assistant message, persisted atomically
	EventDone    RuntimeEventKind = "done"    // completion finished; the final terminal event
	EventError   RuntimeEventKind = "error"   // completion failed; Text is the user-facing error
	EventStopped RuntimeEventKind = "stopped" // loop cancelled or stopped
)

// RuntimeEvent is one event of a member's runtime stream. It carries the
// instance identity and a monotonic per-instance sequence so consumers can
// discard stale events after a member switch. It never carries key material
// (K1): error text is provider.UserError or an assembled message, never the
// raw error string that could embed credential fragments.
type RuntimeEvent struct {
	Team      string
	MemberID  string
	Sequence  uint64
	Kind      RuntimeEventKind
	Text      string
	Timestamp string // RFC3339
}

// isTerminal reports whether the event is a terminal-class event: must reach
// consumers (message/done/error/stopped), unlike streaming deltas that a
// slow consumer may drop.
func (k RuntimeEventKind) isTerminal() bool {
	switch k {
	case EventMessage, EventDone, EventError, EventStopped:
		return true
	}
	return false
}

// Subscription is one subscriber handle on a member's runtime event stream:
// C carries the bounded channel, Cancel unsubscribes it (closing the channel)
// so the consumer can stop reading without waiting on the source's lifetime.
type Subscription struct {
	C      <-chan RuntimeEvent
	Cancel func()
}

// sub is one event subscriber: a bounded channel plus a snapshot ring of the
// terminal events it may have missed when its channel was full.
type sub struct {
	ch   chan RuntimeEvent
	last []RuntimeEvent // terminal events that failed to deliver; replayed before later events
}

// EventSource is the bounded broadcaster of one member's runtime events
// (route §11.3): the channel has a fixed capacity, streaming deltas are
// dropped for slow consumers, and terminal events (final message, done,
// error, stopped) are never lost — a full channel evicts an older delta to
// make room, and a still-blocked terminal event is buffered in the
// subscriber's snapshot ring for replay. Late subscribers replay the ring.
type EventSource struct {
	mu     sync.Mutex
	seq    uint64
	subs   map[*sub]struct{}
	last   []RuntimeEvent // per-instance terminal-event ring, replayed to new subscribers
	closed bool
}

// newEventSource returns an empty broadcaster.
func newEventSource() *EventSource {
	return &EventSource{subs: make(map[*sub]struct{})}
}

// Publish emits one event to every subscriber. Terminal events evict older
// deltas from a full channel and, when even that is impossible, land in the
// subscriber's replay ring; deltas are dropped when the channel is full.
func (s *EventSource) Publish(key InstanceKey, kind RuntimeEventKind, text string) {
	ev := RuntimeEvent{
		Team:      key.Team,
		MemberID:  key.MemberID,
		Sequence:  s.nextSeq(),
		Kind:      kind,
		Text:      text,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if kind.isTerminal() {
		s.last = append(s.last, ev)
		if len(s.last) > 4 {
			s.last = s.last[len(s.last)-4:]
		}
	}
	for sub := range s.subs {
		if kind.isTerminal() {
			sub.evictDelta()
		}
		select {
		case sub.ch <- ev:
		default:
			if kind.isTerminal() {
				sub.last = append(sub.last, ev)
			}
		}
	}
}

// Subscribe registers a bounded channel for the event stream. The channel
// carries the instance's recent terminal events first, so a late or slow
// subscriber never misses the final message or error (route §11.3). Cancel
// unsubscribes and closes the channel.
func (s *EventSource) Subscribe() Subscription {
	s.mu.Lock()
	sub := &sub{ch: make(chan RuntimeEvent, 64)}
	if len(s.last) > 0 {
		sub.last = append(sub.last, s.last...)
		s.flushLocked(sub)
	}
	s.subs[sub] = struct{}{}
	s.mu.Unlock()
	return Subscription{C: sub.ch, Cancel: func() { s.unsubscribe(sub) }}
}

// unsubscribe removes one subscriber and closes its channel; unknown
// subscribers are ignored.
func (s *EventSource) unsubscribe(sub *sub) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[sub]; !ok {
		return
	}
	close(sub.ch)
	delete(s.subs, sub)
}

// Close unsubscribes every subscriber and closes their channels. Publishing
// after Close is a no-op.
func (s *EventSource) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for sub := range s.subs {
		close(sub.ch)
		delete(s.subs, sub)
	}
	s.subs = nil
}

// nextSeq returns the instance's next event sequence (monotonic per source).
func (s *EventSource) nextSeq() uint64 {
	s.seq++
	return s.seq
}

// CurrentSeq returns the last published sequence; it seeds the persisted
// cursor value on member exit paths.
func (s *EventSource) CurrentSeq() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq
}

// evictDelta removes the oldest streaming delta from a full channel so a
// terminal event always has room. Only EventDelta is evictable — started
// events and other terminals are pushed back (reordered to the tail, never
// dropped).
func (sub *sub) evictDelta() {
	// Non-blocking: a concurrent consumer may drain the channel between the
	// length check and the receive, and a blocking receive here would hold the
	// source lock that close() needs — deadlocking the whole broadcaster.
	for range cap(sub.ch) {
		select {
		case ev := <-sub.ch:
			if ev.Kind == EventDelta {
				return
			}
			sub.ch <- ev
		default:
			return // already drained; the terminal event has room
		}
	}
}

// flushLocked drains the subscriber's replay ring into its channel,
// evicting deltas as needed; callers hold the source lock. Ring entries
// that still cannot be delivered are kept for a later flush.
func (s *EventSource) flushLocked(sub *sub) {
	kept := sub.last[:0]
	for _, ev := range sub.last {
		if ev.Kind.isTerminal() {
			sub.evictDelta()
		}
		select {
		case sub.ch <- ev:
		default:
			if ev.Kind.isTerminal() {
				kept = append(kept, ev)
			}
		}
	}
	sub.last = kept
}
