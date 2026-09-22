package cli

// Dispatcher acceptance tests for TEAM_LEADER_WAIT_ROUTE.md §8.3/§8.7: the
// leader's board wakeup cursor has one owner, serving the window's notices and a
// waiting leader from the one advance.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/team"
)

// newWakeWire opens a board wire with no registry behind it — the shape a plain
// host or a test owns directly.
func newWakeWire(t *testing.T) *teamInboxWire {
	t.Helper()
	w := openTeamInbox(t.TempDir())
	if w == nil {
		t.Fatal("the board must open")
	}
	t.Cleanup(w.close)
	return w
}

// appendWakeup writes one durable leader wakeup: the board row the wake path
// appends when a task reaches reported, canceled or failed.
func appendWakeup(t *testing.T, w *teamInboxWire, id, leader, summary string) {
	t.Helper()
	if _, err := w.board.Append(context.Background(), team.AppendInput{
		BoardID: team.BoardShared, EventID: id, ClientMsgID: id, Kind: team.EventWakeup,
		Summary: summary, Stamped: team.Identity{MemberID: leader},
	}); err != nil {
		t.Fatal(err)
	}
}

// quiet establishes the leader's cursor the way an overlay open does: the first
// read of a leader that has none is quiet, so history before it never replays.
func quiet(t *testing.T, w *teamInboxWire) {
	t.Helper()
	if got := w.wakes.drain("alpha", "lead"); len(got) != 0 {
		t.Fatalf("the first read must establish the cursor quietly, got %+v", got)
	}
}

// TestWakeDispatcherAdvancesTheLeaderCursorOnce pins the one-owner contract: a
// wakeup surfaces from the drain exactly once, and the advanced cursor keeps a
// second drain from replaying it.
func TestWakeDispatcherAdvancesTheLeaderCursorOnce(t *testing.T) {
	w := newWakeWire(t)
	quiet(t, w)
	appendWakeup(t, w, "w1", "lead", "task t1 reported")

	got := w.wakes.drain("alpha", "lead")
	if len(got) != 1 || got[0].Summary != "task t1 reported" || got[0].Kind != waitKindWakeup {
		t.Fatalf("drain = %+v, want the leader's reported wakeup", got)
	}
	if again := w.wakes.drain("alpha", "lead"); len(again) != 0 {
		t.Fatalf("a drained wakeup must not surface again, got %+v", again)
	}
}

// TestWakeDispatcherServesEveryConsumerFromOneAdvance pins §8.7's shared-cursor
// gate: concurrent drains — the roster tick and a leader's wait among them —
// produce exactly one batch, and the waiting party receives that same batch.
func TestWakeDispatcherServesEveryConsumerFromOneAdvance(t *testing.T) {
	w := newWakeWire(t)
	quiet(t, w)
	bus := newWaitBus()
	defer bus.close()
	w.attachSignals(bus)
	waiter := bus.Subscribe("alpha")
	defer waiter.Close()
	appendWakeup(t, w, "w1", "lead", "task t1 reported")

	var mu sync.Mutex
	var drained []WaitEvent
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			batch := w.wakes.drain("alpha", "lead")
			mu.Lock()
			drained = append(drained, batch...)
			mu.Unlock()
		})
	}
	wg.Wait()
	if len(drained) != 1 {
		t.Fatalf("concurrent drains returned %d events, want one advance to find it", len(drained))
	}
	got := receiveWaitEvent(t, waiter)
	if got.Summary != "task t1 reported" || got.Seq == 0 {
		t.Fatalf("waiter = %+v, want the same event the window's drain reported", got)
	}
	select {
	case ev := <-waiter.C():
		t.Fatalf("the waiter saw one occurrence twice: %+v", ev)
	default:
	}
}

// TestWakeDispatcherWakesOnTheDurableRowAlone pins the fallback: a wakeup that no
// producer reports in-process — a cross-process write, or a producer that never
// learned about the bus — still reaches a waiting party, because the drain
// publishes what it reads rather than relying on the edge trigger.
func TestWakeDispatcherWakesOnTheDurableRowAlone(t *testing.T) {
	w := newWakeWire(t)
	quiet(t, w)
	bus := newWaitBus()
	defer bus.close()
	w.attachSignals(bus)
	waiter := bus.Subscribe("alpha")
	defer waiter.Close()
	appendWakeup(t, w, "w1", "lead", "task t1 reported")
	appendWakeup(t, w, "w2", "lead", "task t2 canceled")

	if got := w.wakes.drain("alpha", "lead"); len(got) != 2 {
		t.Fatalf("drain = %+v, want the two saved wakeups", got)
	}
	for _, want := range []string{"task t1 reported", "task t2 canceled"} {
		if ev := receiveWaitEvent(t, waiter); ev.Summary != want {
			t.Fatalf("waiter = %+v, want %q", ev, want)
		}
	}
}

// TestWakeDispatcherKeepsTheLeaderCursorPerLeader pins that the cursor belongs to
// the leader it was established for: a second leader's first read is quiet too,
// and the first leader's wakeups are not replayed to it.
func TestWakeDispatcherKeepsTheLeaderCursorPerLeader(t *testing.T) {
	w := newWakeWire(t)
	quiet(t, w)
	appendWakeup(t, w, "w1", "lead", "task t1 reported")
	if got := w.wakes.drain("alpha", "lead2"); len(got) != 0 {
		t.Fatalf("a new leader's first read must be quiet, got %+v", got)
	}
	if got := w.wakes.drain("alpha", "lead"); len(got) != 1 {
		t.Fatalf("the first leader's cursor must still hold its wakeup, got %+v", got)
	}
}

// TestWakeRegistryCloseReleasesWaiters pins the teardown gate: the bus lives on
// the registry, and closeAll closes it, so a leader blocked in its wait is
// released instead of stranding a goroutine past the registry that owned it.
func TestWakeRegistryCloseReleasesWaiters(t *testing.T) {
	r := newTeamBackends(nil, 0)
	waiter := r.signals().Subscribe("alpha")
	r.closeAll()
	select {
	case _, ok := <-waiter.C():
		if ok {
			t.Fatal("closeAll must release the waiter's subscription")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closeAll left the waiter blocked")
	}
}

// scrollbackHas reports whether any committed transcript block carries want. The
// window's notices are committed lines, so this is the boundary the notice is
// asserted at rather than the state behind it.
func scrollbackHas(m chatTUI, want string) bool {
	for _, block := range m.transcript {
		if strings.Contains(block, want) {
			return true
		}
	}
	return false
}

// TestTeamTickConsumesWakeupsWhileTheOverlayIsOpen pins §3.1's gap: wakeups used
// to be consumed only when the overlay opened, so a report that arrived while it
// was up showed nothing until a reopen. The roster tick now schedules the same
// read off the frame path and routes the batch back through the message the
// window already handles.
func TestTeamTickConsumesWakeupsWhileTheOverlayIsOpen(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	closeBoardOn(t, m)
	appendWakeup(t, m.teamPick.board, "w1", "lead", "task t1 reported")

	next, cmd := m.update(teamRosterRefreshMsg{tick: true, gen: m.rosterTickGen})
	batch := rosterResult(t, cmd, func(msg teamRosterRefreshMsg) bool { return msg.wake != nil })
	if len(batch.wake) != 1 || batch.wake[0].Summary != "task t1 reported" {
		t.Fatalf("tick batch = %+v, want the leader's wakeup", batch.wake)
	}
	after, _ := next.(chatTUI).update(batch)
	if !scrollbackHas(after.(chatTUI), "wakeup: task t1 reported") {
		t.Fatal("the tick's wakeup must surface as a notice without reopening the overlay")
	}
}
