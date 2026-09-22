package agent

import (
	"context"
	"strings"
	"sync"
)

// WriteIntentToken is the in-process queue the writer-capable agents of one
// workspace share, so peers that would race the same cross-process lease queue
// among themselves instead. It is a performance token, never a safety boundary:
// it can only delay an acquisition, and every holder still takes the workspace
// lease. Overlap is decided by ScheduleOverlaps — the same semantics the write
// scheduler uses — so peers it admits concurrently are the ones the scheduler
// would also run concurrently.
type WriteIntentToken struct {
	// root is the workspace root the intent paths are normalized against.
	root string
	mu   sync.Mutex
	// seq gives every intent an identity, so a release removes exactly the
	// intent it was returned for even when two peers hold equal scopes.
	seq    uint64
	active []tokenIntent
	queue  []*tokenWaiter
}

// tokenIntent is one admitted or requested intent: the peer label it belongs
// to, the scope ScheduleOverlaps compares, and the label a wait report renders.
type tokenIntent struct {
	seq    uint64
	peer   string
	label  string
	claims WritePathSet
}

// tokenWaiter is one queued intent; ready closes when it is admitted.
type tokenWaiter struct {
	intent tokenIntent
	ready  chan struct{}
}

// NewWriteIntentToken builds the token for one workspace root. The root is
// resolved once, here, so the whole-workspace claim and the path claims of the
// same workspace compare against the same root — a raw root beside resolved
// paths would make a whole-workspace intent miss the files it must block.
func NewWriteIntentToken(workspaceRoot string) *WriteIntentToken {
	root := strings.TrimSpace(workspaceRoot)
	if resolved, err := normalizeExistingRoot(root); err == nil {
		root = resolved
	}
	return &WriteIntentToken{root: root}
}

// Acquire queues one intent until no in-process peer holds an overlapping one,
// then returns its release. A peer only queues behind peers it actually
// conflicts with, so disjoint writes keep running concurrently. onWait is called
// at most once, synchronously at enqueue — the caller reports a wait while it is
// happening, not after it ends. Cancellation removes the waiter and may admit
// peers queued behind it.
func (t *WriteIntentToken) Acquire(ctx context.Context, peer string, intent WriteIntent, onWait func(WriteIntentWait)) (func(), error) {
	if t == nil {
		return func() {}, nil
	}
	me := tokenIntent{peer: strings.TrimSpace(peer), label: strings.TrimSpace(intent.Label), claims: t.claimsFor(intent)}
	t.mu.Lock()
	t.seq++
	me.seq = t.seq
	blocker, blocked := t.firstBlockerLocked(me, queuedIntents(t.queue))
	var waiter *tokenWaiter
	if blocked {
		waiter = &tokenWaiter{intent: me, ready: make(chan struct{})}
		t.queue = append(t.queue, waiter)
	} else {
		t.active = append(t.active, me)
	}
	t.mu.Unlock()
	if !blocked {
		return t.release(me.seq), nil
	}
	// Reported outside the mutex: a slow reporter must not stall the queue.
	if onWait != nil {
		onWait(WriteIntentWait{Holder: blocker.peer, Scope: blocker.label})
	}
	select {
	case <-waiter.ready:
		return t.release(me.seq), nil
	case <-ctx.Done():
		t.mu.Lock()
		t.dropLocked(waiter)
		t.mu.Unlock()
		return nil, ctx.Err()
	}
}

// claimsFor normalizes one intent into the comparison form. Anything the
// normalizer refuses — a path outside the workspace, a glob, an intent that
// names nothing — conservatively claims the whole workspace: over-queueing is
// the safe direction for a token whose only job is to delay acquisitions.
func (t *WriteIntentToken) claimsFor(intent WriteIntent) WritePathSet {
	if intent.Whole || len(intent.Paths) == 0 {
		return WritePathSet{WholeWorkspace: true, WorkspaceRoot: t.root}
	}
	claims, err := NormalizeWritePaths(t.root, intent.Paths)
	if err != nil || claims.Empty() {
		return WritePathSet{WholeWorkspace: true, WorkspaceRoot: t.root}
	}
	return claims
}

// firstBlockerLocked reports the peer one intent must wait for among the intents
// already admitted plus those queued before it. Peers it does not overlap are
// deliberately skipped over, so disjoint writes never queue behind each other,
// while overlapping peers still admit in arrival order.
func (t *WriteIntentToken) firstBlockerLocked(me tokenIntent, earlier []tokenIntent) (tokenIntent, bool) {
	if blocker, ok := blockerIn(t.active, me); ok {
		return blocker, true
	}
	return blockerIn(earlier, me)
}

// blockerIn returns the first intent whose scope conflicts with me. An intent
// held by the same peer is skipped: a peer queueing behind itself could only
// deadlock — the holder returns after the waiter, and the waiter waits for the
// holder. Skipping is safe because the token never excludes anyone: the claim it
// guards is still taken, so two same-peer writes stay mutually exclusive there.
func blockerIn(intents []tokenIntent, me tokenIntent) (tokenIntent, bool) {
	for _, held := range intents {
		if held.peer != "" && held.peer == me.peer {
			continue
		}
		if ScheduleOverlaps(held.claims, me.claims) {
			return held, true
		}
	}
	return tokenIntent{}, false
}

func queuedIntents(queue []*tokenWaiter) []tokenIntent {
	if len(queue) == 0 {
		return nil
	}
	out := make([]tokenIntent, 0, len(queue))
	for _, waiter := range queue {
		out = append(out, waiter.intent)
	}
	return out
}

// release returns the idempotent release for one admitted intent.
func (t *WriteIntentToken) release(seq uint64) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			t.removeActiveLocked(seq)
			t.sweepLocked()
			t.mu.Unlock()
		})
	}
}

func (t *WriteIntentToken) removeActiveLocked(seq uint64) {
	for i, held := range t.active {
		if held.seq == seq {
			t.active = append(t.active[:i], t.active[i+1:]...)
			return
		}
	}
}

// dropLocked removes a canceled waiter and re-runs the sweep, so a peer queued
// behind it is admitted rather than stranded until the next release.
func (t *WriteIntentToken) dropLocked(target *tokenWaiter) {
	for i, waiter := range t.queue {
		if waiter == target {
			t.queue = append(t.queue[:i], t.queue[i+1:]...)
			break
		}
	}
	t.sweepLocked()
}

// sweepLocked admits every queued intent that conflicts with nothing active and
// nothing ahead of it in the queue, in queue order.
func (t *WriteIntentToken) sweepLocked() {
	if len(t.queue) == 0 {
		return
	}
	pending := make([]*tokenWaiter, 0, len(t.queue))
	admitted := make([]tokenIntent, 0, len(t.queue))
	for _, waiter := range t.queue {
		if _, blocked := t.firstBlockerLocked(waiter.intent, admitted); blocked {
			pending = append(pending, waiter)
			continue
		}
		admitted = append(admitted, waiter.intent)
		close(waiter.ready)
	}
	t.active = append(t.active, admitted...)
	t.queue = pending
}

// State reports the token's occupancy for diagnostics and tests: how many
// intents are admitted and how many are still queued.
func (t *WriteIntentToken) State() (active, queued int) {
	if t == nil {
		return 0, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.active), len(t.queue)
}
