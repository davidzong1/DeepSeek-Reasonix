package cli

// Read-ahead tests for the durable command chain: the board read a submit used
// to do inline is served from a batch fetched ahead of time, and a batch never
// outlives the bindings it was read under.

import (
	"context"
	"strings"
	"testing"
	"time"
)

// hasInboxPrefetch reports whether a read-ahead batch is waiting for member.
func hasInboxPrefetch(w *teamInboxWire, member string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.prefetched[member]
	return ok
}

// waitForInboxPrefetch waits until the member's read-ahead settles: a batch is
// waiting, or the read finished without one.
func waitForInboxPrefetch(t *testing.T, w *teamInboxWire, member string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		w.mu.Lock()
		_, ready := w.prefetched[member]
		inflight := w.fetching[member]
		w.mu.Unlock()
		if ready || !inflight {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("inbox prefetch did not settle")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestTeamInboxPrefetchServesTheSubmitPath pins the read-ahead contract: once a
// batch is prefetched, the submit consumes it — ack included — without reading
// the board, which is what keeps the keystroke path free of board I/O. The proof
// that the prefetch was the source is that it is gone afterwards: the inline
// fallback would have left it sitting there, and a second submit would inject it.
func TestTeamInboxPrefetchServesTheSubmitPath(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	closeBoardOn(t, m)
	seedCommand(t, m, "lead", 2, "t9", "prefetched work")
	w := m.teamPick.board
	w.prefetch("lead")
	waitForInboxPrefetch(t, w, "lead")
	if !hasInboxPrefetch(w, "lead") {
		t.Fatal("prefetch stored no batch for the bound member")
	}

	got := m.injectTeamTurn("hi")
	if !strings.Contains(got, "[task: t9] prefetched work") {
		t.Fatalf("the prefetched batch must ride the turn, got:\n%s", got)
	}
	if hasInboxPrefetch(w, "lead") {
		t.Fatal("the submit must consume the prefetched batch, not leave it queued")
	}
	if again := m.injectTeamTurn("hi"); again != "hi" {
		t.Fatalf("an acknowledged batch must not inject twice, got:\n%s", again)
	}
}

// TestTeamInboxPrefetchDropsOnReset pins the generation gate across a reopen:
// resetInboxes is what an overlay open runs, and a batch read under the previous
// bindings must not ride a turn bound to a new generation.
func TestTeamInboxPrefetchDropsOnReset(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	closeBoardOn(t, m)
	seedCommand(t, m, "lead", 2, "t10", "old bindings work")
	w := m.teamPick.board
	w.prefetch("lead")
	waitForInboxPrefetch(t, w, "lead")
	if !hasInboxPrefetch(w, "lead") {
		t.Fatal("prefetch stored no batch to reset")
	}

	w.resetInboxes()
	if hasInboxPrefetch(w, "lead") {
		t.Fatal("a reopened overlay must not ride a batch fetched under the old bindings")
	}
}

// TestTeamInboxStalePrefetchNeverRidesTheNextTurn pins the race an ordered
// read-ahead can otherwise lose: a batch queued just before an acknowledgement
// describes the very commands that acknowledgement consumed, so the next submit
// must drop it and read the board instead of injecting them a second time.
func TestTeamInboxStalePrefetchNeverRidesTheNextTurn(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	closeBoardOn(t, m)
	seedCommand(t, m, "lead", 2, "t11", "already delivered work")
	w := m.teamPick.board
	inbox := w.inboxFor("lead")
	// The racing order: the read queues its batch first, and the acknowledgement
	// that consumes the same commands lands after it.
	batch, ok := fetchTeamInbox(inbox)
	if !ok {
		t.Fatal("the seeded command must be fetchable")
	}
	w.storePrefetched("lead", inbox, batch)
	ctx, cancel := context.WithTimeout(context.Background(), teamBoardTimeout)
	defer cancel()
	if err := inbox.Ack(ctx, batch.next); err != nil {
		t.Fatal(err)
	}
	w.noteAck("lead")

	if got := m.injectTeamTurn("hi"); got != "hi" {
		t.Fatalf("a batch overtaken by an acknowledgement must not inject, got:\n%s", got)
	}
}

// TestTeamInboxPrefetchQueuedBeforeAckNeverRides pins the OTHER ordering of the
// same race, which is the one a real run hits: the read starts first and the
// acknowledgement lands while it is still in flight, so the batch is filed
// AFTER the acknowledgement that consumed its commands. Stamping the epoch at
// filing time would make that stale batch look current and inject the same
// commands twice; the epoch therefore has to be the one captured when the read
// was queued.
func TestTeamInboxPrefetchQueuedBeforeAckNeverRides(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	closeBoardOn(t, m)
	seedCommand(t, m, "lead", 2, "t12", "raced work")
	w := m.teamPick.board
	inbox := w.inboxFor("lead")

	// The in-flight read: it read the board before the acknowledgement.
	batch, ok := fetchTeamInbox(inbox)
	if !ok {
		t.Fatal("the seeded command must be fetchable")
	}
	// The epoch prefetch captures when it queues the read — read here the same
	// way, directly, rather than through a production accessor that would exist
	// only for this test.
	w.mu.Lock()
	epoch := w.acks["lead"]
	w.mu.Unlock()
	// A turn's inline path acknowledges those very commands while the read runs.
	ctx, cancel := context.WithTimeout(context.Background(), teamBoardTimeout)
	defer cancel()
	if err := inbox.Ack(ctx, batch.next); err != nil {
		t.Fatal(err)
	}
	w.noteAck("lead")
	// Now the in-flight read finishes and files its batch, stamped with the epoch
	// it was queued under rather than the one in force now.
	batch.ackEpoch = epoch
	w.storePrefetched("lead", inbox, batch)

	if got := m.injectTeamTurn("hi"); got != "hi" {
		t.Fatalf("a batch read before the acknowledgement must not inject after it, got:\n%s", got)
	}
}

// TestTeamInboxPrefetchWithoutBindingStoresNothing pins the unbound gate: a
// member with no persisted binding has no generation to answer for, so the
// read-ahead settles with nothing queued instead of fabricating an inbox.
func TestTeamInboxPrefetchWithoutBindingStoresNothing(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	closeBoardOn(t, m)
	w := m.teamPick.board
	w.prefetch("ghost")
	waitForInboxPrefetch(t, w, "ghost")
	if hasInboxPrefetch(w, "ghost") {
		t.Fatal("an unbound member must have no read-ahead batch")
	}
}
