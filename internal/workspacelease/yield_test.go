package workspacelease

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// The yield under test: a completed hold kept for a running background job is a
// loan, so a writer blocked on that domain repays it at once. Every case is
// decided by a wait notice or by State(), never by elapsed time.

// noticeProbe records the moment an Owner queues, which is the only wait the
// lease reports.
type noticeProbe struct {
	queued chan struct{}
}

func newNoticeProbe(t *testing.T, root, lockDir string) (*Owner, *noticeProbe) {
	t.Helper()
	probe := &noticeProbe{queued: make(chan struct{}, 1)}
	owner, err := New(root, lockDir, func() { probe.queued <- struct{}{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return owner, probe
}

func (p *noticeProbe) requireQueued(t *testing.T, what string) {
	t.Helper()
	select {
	case <-p.queued:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s never reported a wait", what)
	}
}

func (p *noticeProbe) requireNoWait(t *testing.T, what string) {
	t.Helper()
	select {
	case <-p.queued:
		t.Fatalf("%s reported a wait it never had to make", what)
	default:
	}
}

// retainedCountFor is the white-box view of the process registry: how many
// Owners this process still keeps a completed hold for on one lock file.
func retainedCountFor(lockPath string) int {
	retainedLocks.mu.Lock()
	defer retainedLocks.mu.Unlock()
	return len(retainedLocks.byPath[lockPath])
}

// retainCompletedHold drives one Owner through the order a retention needs: the
// job starts while the tool still holds (which is what RetainUntil observes),
// then the tool returns, then the run ends — exactly the order boot's job
// observer produces. It returns the job's completion channel and fails when the
// hold was not retained, so no case below can pass vacuously.
func retainCompletedHold(t *testing.T, owner *Owner, hold func() (func(), error)) chan struct{} {
	t.Helper()
	owner.BeginRun()
	release, err := hold()
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	job := make(chan struct{})
	owner.RetainUntil(job)
	release()
	owner.EndRun()
	if !owner.State().Acquired {
		t.Fatal("the completed hold was not retained for the running job")
	}
	return job
}

func pathHold(owner *Owner, path string) func() (func(), error) {
	return func() (func(), error) { return owner.HoldWriteForPath(context.Background(), path) }
}

// orderedBySlot sorts paths the way the lease requires them to be held: a hold
// whose stripe sorts below one already held waits for that hold to be released,
// so a test taking two of them must take the lower one first.
func orderedBySlot(t *testing.T, owner *Owner, paths ...string) []string {
	t.Helper()
	type entry struct{ path, slot string }
	entries := make([]entry, 0, len(paths))
	for _, candidate := range paths {
		key, _, err := canonicalFileKey(candidate)
		if err != nil {
			t.Fatalf("canonical key for %s: %v", candidate, err)
		}
		entries = append(entries, entry{path: candidate, slot: owner.pathLockPath(key)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].slot < entries[j].slot })
	ordered := make([]string, 0, len(entries))
	for _, e := range entries {
		ordered = append(ordered, e.path)
	}
	return ordered
}

func TestRetainedPathHoldYieldsToABlockedPeer(t *testing.T) {
	root, locks := t.TempDir(), t.TempDir()
	target := filepath.Join(root, "internal", "shared.go")
	first := newOwnerWithGrace(t, root, locks, time.Minute)
	second, secondNotices := newNoticeProbe(t, root, locks)
	defer close(retainCompletedHold(t, first, pathHold(first, target)))

	second.BeginRun()
	defer second.EndRun()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	peerRelease, err := second.HoldWriteForPath(ctx, target)
	if err != nil {
		t.Fatalf("peer did not repay the retained hold: %v", err)
	}
	peerRelease()
	secondNotices.requireNoWait(t, "the peer")
	if first.State().Acquired {
		t.Fatal("the retained hold outlived the peer that needed it")
	}
}

func TestLiveHoldIsNeverYieldedToAPeer(t *testing.T) {
	root, locks := t.TempDir(), t.TempDir()
	target := filepath.Join(root, "internal", "shared.go")
	first := newOwnerWithGrace(t, root, locks, time.Minute)
	second, secondNotices := newNoticeProbe(t, root, locks)

	// The hold is live: a tool is writing through it, so no peer may take it.
	first.BeginRun()
	defer first.EndRun()
	release, err := first.HoldWriteForPath(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	job := make(chan struct{})
	first.RetainUntil(job)

	second.BeginRun()
	defer second.EndRun()
	done := make(chan error, 1)
	go func() {
		peerCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := second.HoldWriteForPath(peerCtx, target)
		done <- err
	}()
	secondNotices.requireQueued(t, "the peer behind a live hold")
	if !first.State().Acquired {
		t.Fatal("a live hold was released under a peer")
	}

	// The tool returns and the job ends: only then does the peer get the domain.
	release()
	close(job)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("peer never acquired after the live hold was released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("peer stayed blocked after the live hold was released")
	}
}

func TestYieldRepaysRetainedHoldsOnlyAndLeavesLiveOnes(t *testing.T) {
	root, locks := t.TempDir(), t.TempDir()
	ordered := orderedBySlot(t, newOwnerWithGrace(t, root, locks, time.Minute),
		filepath.Join(root, "internal", "live.go"), filepath.Join(root, "internal", "retained.go"))
	livePath, retainedPath := ordered[0], ordered[1]
	first := newOwnerWithGrace(t, root, locks, time.Minute)
	second, secondNotices := newNoticeProbe(t, root, locks)

	first.BeginRun()
	defer first.EndRun()
	live, err := first.HoldWriteForPath(context.Background(), livePath)
	if err != nil {
		t.Fatal(err)
	}
	defer live()
	retainedRelease, err := first.HoldWriteForPath(context.Background(), retainedPath)
	if err != nil {
		t.Fatal(err)
	}
	job := make(chan struct{})
	defer close(job)
	first.RetainUntil(job)
	retainedRelease()
	if !first.State().Acquired {
		t.Fatal("the completed hold was not retained")
	}

	second.BeginRun()
	defer second.EndRun()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	peerRelease, err := second.HoldWriteForPath(ctx, retainedPath)
	if err != nil {
		t.Fatalf("peer did not repay the retained path: %v", err)
	}
	peerRelease()
	secondNotices.requireNoWait(t, "the peer repaying a retained hold")
	if state := first.State(); !state.Acquired || state.HeldScope != "file" {
		t.Fatalf("yield gave away a live hold: %+v", state)
	}
}

// An Owner that blocks on its own completed hold must repay it too: a loan kept
// to save ~0.5ms is worth less than the wait it causes its own next acquisition.
func TestOwnerRepaysItsOwnRetainedHoldInsteadOfWaitingItOut(t *testing.T) {
	root, locks := t.TempDir(), t.TempDir()
	target := filepath.Join(root, "internal", "shared.go")
	owner := newOwnerWithGrace(t, root, locks, time.Minute)
	defer close(retainCompletedHold(t, owner, pathHold(owner, target)))

	owner.BeginRun()
	defer owner.EndRun()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := owner.AcquireWrite(ctx); err != nil {
		t.Fatalf("owner waited out its own retained hold: %v", err)
	}
	if state := owner.State(); !state.Acquired || state.HeldScope != "workspace" {
		t.Fatalf("workspace state after repaying the path hold: %+v", state)
	}
}

// A hold that completes after the retention window was already armed is still a
// loan: the publish happens where a hold completes, not only where the window
// starts, so a peer blocked on the later hold can still repay it.
func TestHoldCompletingAfterTheWindowIsArmedIsStillYieldable(t *testing.T) {
	root, locks := t.TempDir(), t.TempDir()
	ordered := orderedBySlot(t, newOwnerWithGrace(t, root, locks, time.Minute),
		filepath.Join(root, "internal", "first.go"), filepath.Join(root, "internal", "second.go"))
	firstPath, secondPath := ordered[0], ordered[1]
	first := newOwnerWithGrace(t, root, locks, time.Minute)
	peer, peerNotices := newNoticeProbe(t, root, locks)

	first.BeginRun()
	defer first.EndRun()
	firstRelease, err := first.HoldWriteForPath(context.Background(), firstPath)
	if err != nil {
		t.Fatal(err)
	}
	secondRelease, err := first.HoldWriteForPath(context.Background(), secondPath)
	if err != nil {
		t.Fatal(err)
	}
	job := make(chan struct{})
	defer close(job)
	first.RetainUntil(job)
	secondRelease() // arms the window and publishes the second hold
	firstRelease()  // completes later, still inside the running job
	if !first.State().Acquired {
		t.Fatal("both holds were released despite a running background job")
	}

	peer.BeginRun()
	defer peer.EndRun()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	release, err := peer.HoldWriteForPath(ctx, firstPath)
	if err != nil {
		t.Fatalf("peer did not repay the later-completed hold: %v", err)
	}
	release()
	peerNotices.requireNoWait(t, "the peer behind the later hold")
	if first.State().Acquired {
		t.Fatal("the later-completed hold outlived the peer that needed it")
	}
}

func TestYieldLeavesNoStaleRegistryEntry(t *testing.T) {
	root, locks := t.TempDir(), t.TempDir()
	target := filepath.Join(root, "internal", "shared.go")
	owner := newOwnerWithGrace(t, root, locks, time.Minute)
	job := retainCompletedHold(t, owner, pathHold(owner, target))
	held := owner.HeldKeys()
	if len(held) == 0 {
		t.Fatal("the retained hold reported no keys")
	}
	lockPath := owner.pathLockPath(held[0])
	if got := retainedCountFor(lockPath); got != 1 {
		t.Fatalf("registry entries for the retained path = %d, want 1", got)
	}

	close(job)
	waitForRelease(t, owner)
	if got := retainedCountFor(lockPath); got != 0 {
		t.Fatalf("registry kept %d entries after the hold was released", got)
	}

	// A later writer takes the same path normally: a released loan must not
	// leave a domain anyone can claim to yield.
	other, notices := newNoticeProbe(t, root, locks)
	other.BeginRun()
	defer other.EndRun()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	release, err := other.HoldWriteForPath(ctx, target)
	if err != nil {
		t.Fatalf("path not usable after the retained hold ended: %v", err)
	}
	notices.requireNoWait(t, "a writer taking a released path")
	release()
}

func TestYieldIsScopedToTheLockDomain(t *testing.T) {
	locks := t.TempDir()
	first, second := t.TempDir(), t.TempDir()
	holder := newOwnerWithGrace(t, first, locks, time.Minute)
	defer close(retainCompletedHold(t, holder, pathHold(holder, filepath.Join(first, "internal", "a.go"))))

	// Another workspace in the same process shares the lease directory but not
	// the domain: it must not be able to repay a loan it does not own.
	neighbour, notices := newNoticeProbe(t, second, locks)
	neighbour.BeginRun()
	defer neighbour.EndRun()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	release, err := neighbour.HoldWriteForPath(ctx, filepath.Join(second, "internal", "b.go"))
	if err != nil {
		t.Fatalf("neighbour workspace could not take its own path: %v", err)
	}
	release()
	notices.requireNoWait(t, "an unrelated workspace")
	if !holder.State().Acquired {
		t.Fatal("an unrelated workspace repaid another workspace's loan")
	}
}

func TestYieldDoesNotWeakenCancellation(t *testing.T) {
	root, locks := t.TempDir(), t.TempDir()
	target := filepath.Join(root, "internal", "shared.go")
	first := newOwnerWithGrace(t, root, locks, time.Minute)
	second := newOwnerWithGrace(t, root, locks, time.Minute)
	first.BeginRun()
	defer first.EndRun()
	release, err := first.HoldWriteForPath(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	job := make(chan struct{})
	defer close(job)
	first.RetainUntil(job)

	// A cancelled acquisition must still report its cancellation, whether the
	// yield raced it or not.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := second.HoldWriteForPath(ctx, target); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquisition = %v, want context.Canceled", err)
	}
	release()
}
