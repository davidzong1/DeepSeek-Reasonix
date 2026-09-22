package cli

import (
	"sync"
	"sync/atomic"
)

// teamWakeDispatcher is the single owner of the leader's board wakeup cursor.
// Both consumers of a leader's wakeups — the window's notices and the leader's
// interruptible wait — are served from here, because a cursor read twice is a
// cursor advanced twice, and the second reader then sees an empty page while the
// first holds the event. One drain reads the board once, advances the cursor
// once, hands the batch to the wait bus, and returns it for the window's notices.
type teamWakeDispatcher struct {
	wire *teamInboxWire
	// mu serializes drains. The cursor has one owner, so two drains racing — the
	// roster tick and the overlay open, or two goroutines of a test — must
	// sequence, and the loser of the race then reads an empty page.
	mu sync.Mutex
	// signals is the registry's wait bus, absent in hosts and tests with no
	// registry. Atomic rather than mu-guarded: the registry installs it from the
	// frame path, and mu is held across a board read.
	signals atomic.Pointer[waitBus]
}

// setSignals attaches the registry's bus. It is the registry's seam, installed
// once when the board is installed.
func (d *teamWakeDispatcher) setSignals(b *waitBus) {
	if d == nil {
		return
	}
	d.signals.Store(b)
}

// drain reads the leader's wakeup rows, advances the cursor, and reports them.
// The board write is bounded by teamBoardTimeout, already inside the read, so the
// caller is never left waiting on a stalled board; every returned event also went
// to the bus unless the producer already signalled the same occurrence.
func (d *teamWakeDispatcher) drain(teamName, leader string) []WaitEvent {
	if d == nil || d.wire == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	reasons := d.wire.consumeWakeups(leader)
	if len(reasons) == 0 {
		return nil
	}
	events := make([]WaitEvent, 0, len(reasons))
	bus := d.signals.Load()
	for _, reason := range reasons {
		ev := WaitEvent{Kind: waitKindWakeup, Team: teamName, Summary: reason}
		bus.publish(ev)
		events = append(events, ev)
	}
	return events
}
