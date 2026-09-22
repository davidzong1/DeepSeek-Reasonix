package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// The token decides who queues until whom. Every case is asserted from a wait
// report delivered at enqueue, from an admission order, or from the token's own
// occupancy — never from elapsed time. The timeouts are deadlock guards.

// tokenProbe drives one acquisition from its own goroutine: queued fires when
// the call actually has to queue, admitted when it holds the intent.
type tokenProbe struct {
	queued   chan WriteIntentWait
	admitted chan func()
	failed   chan error
}

func startTokenAcquire(ctx context.Context, token *WriteIntentToken, peer string, intent WriteIntent) *tokenProbe {
	probe := &tokenProbe{
		queued:   make(chan WriteIntentWait, 1),
		admitted: make(chan func(), 1),
		failed:   make(chan error, 1),
	}
	go func() {
		release, err := token.Acquire(ctx, peer, intent, func(wait WriteIntentWait) {
			probe.queued <- wait
		})
		if err != nil {
			probe.failed <- err
			return
		}
		probe.admitted <- release
	}()
	return probe
}

// awaitQueued returns the wait the probe reported, and fails when the call was
// admitted instead — which is the only way "it did not queue" can be proven.
func awaitQueued(t *testing.T, probe *tokenProbe, peer string) WriteIntentWait {
	t.Helper()
	select {
	case wait := <-probe.queued:
		return wait
	case err := <-probe.failed:
		t.Fatalf("%s failed instead of queueing: %v", peer, err)
	case <-probe.admitted:
		t.Fatalf("%s was admitted immediately, want it queued", peer)
	case <-time.After(5 * time.Second):
		t.Fatalf("%s neither queued nor was admitted", peer)
	}
	return WriteIntentWait{}
}

func awaitAdmitted(t *testing.T, probe *tokenProbe, peer string) func() {
	t.Helper()
	select {
	case release := <-probe.admitted:
		return release
	case err := <-probe.failed:
		t.Fatalf("%s: %v", peer, err)
	case <-time.After(5 * time.Second):
		t.Fatalf("%s was never admitted", peer)
	}
	return nil
}

// requireNotAdmitted fails when the probe holds an intent, without waiting for one.
func requireNotAdmitted(t *testing.T, probe *tokenProbe, peer string) {
	t.Helper()
	select {
	case <-probe.admitted:
		t.Fatalf("%s was admitted before its blocker released", peer)
	default:
	}
}

func requireHeld(t *testing.T, token *WriteIntentToken, peer string, intent WriteIntent) func() {
	t.Helper()
	release, err := token.Acquire(context.Background(), peer, intent, func(wait WriteIntentWait) {
		t.Fatalf("%s queued behind %s despite a disjoint scope", peer, wait.Holder)
	})
	if err != nil {
		t.Fatalf("%s: %v", peer, err)
	}
	return release
}

func occupancy(token *WriteIntentToken) string {
	active, queued := token.State()
	return fmt.Sprintf("%d active, %d queued", active, queued)
}

func TestWriteIntentTokenAdmitsDisjointPeersConcurrently(t *testing.T) {
	root := t.TempDir()
	token := NewWriteIntentToken(root)
	scopes := []WriteIntent{
		{Paths: []string{filepath.Join(root, "internal", "a.go")}, Label: "internal/a.go"},
		{Paths: []string{filepath.Join(root, "internal", "b.go")}, Label: "internal/b.go"},
		{Paths: []string{filepath.Join(root, "cmd", "main.go")}, Label: "cmd/main.go"},
	}

	var releases []func()
	for i, intent := range scopes {
		release, err := token.Acquire(context.Background(),
			fmt.Sprintf("member m%d", i+1), intent, func(wait WriteIntentWait) {
				t.Errorf("member m%d queued behind %s on %s", i+1, wait.Holder, wait.Scope)
			})
		if err != nil {
			t.Fatalf("member m%d: %v", i+1, err)
		}
		releases = append(releases, release)
	}
	if active, queued := token.State(); active != len(scopes) || queued != 0 {
		t.Fatalf("disjoint occupancy = %s, want all %d admitted", occupancy(token), len(scopes))
	}
	for _, release := range releases {
		release()
	}
	if active, queued := token.State(); active != 0 || queued != 0 {
		t.Fatalf("occupancy after release = %s, want empty", occupancy(token))
	}
}

func TestWriteIntentTokenQueuesOverlappingPeersInArrivalOrder(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "internal", "shared.go")
	intent := WriteIntent{Paths: []string{target}, Label: "internal/shared.go"}
	token := NewWriteIntentToken(root)

	first := requireHeld(t, token, "member m1", intent)
	// Each peer joins the queue only after the one before it is already queued,
	// so the arrival order this case asserts is pinned rather than raced for.
	second := startTokenAcquire(context.Background(), token, "member m2", intent)
	if wait := awaitQueued(t, second, "member m2"); wait.Holder != "member m1" || wait.Scope != "internal/shared.go" {
		t.Fatalf("member m2 wait report = %+v, want the member holding internal/shared.go", wait)
	}
	third := startTokenAcquire(context.Background(), token, "member m3", intent)
	if wait := awaitQueued(t, third, "member m3"); wait.Holder != "member m1" || wait.Scope != "internal/shared.go" {
		t.Fatalf("member m3 wait report = %+v, want the member holding internal/shared.go", wait)
	}
	if active, queued := token.State(); active != 1 || queued != 2 {
		t.Fatalf("overlapping occupancy = %s, want one holder and two queued", occupancy(token))
	}

	// Arrival order is the admission order, and the second waiter does not
	// overtake the first when the holder releases.
	first()
	releaseSecond := awaitAdmitted(t, second, "member m2")
	requireNotAdmitted(t, third, "member m3")
	if active, queued := token.State(); active != 1 || queued != 1 {
		t.Fatalf("occupancy before the second release = %s, want m3 still queued", occupancy(token))
	}
	releaseSecond()
	releaseThird := awaitAdmitted(t, third, "member m3")
	releaseThird()
	if active, queued := token.State(); active != 0 || queued != 0 {
		t.Fatalf("occupancy after the queue drained = %s, want empty", occupancy(token))
	}
}

func TestWriteIntentTokenLetsADisjointPeerOvertakeTheQueue(t *testing.T) {
	root := t.TempDir()
	token := NewWriteIntentToken(root)
	held := WriteIntent{Paths: []string{filepath.Join(root, "internal", "shared.go")}, Label: "internal/shared.go"}

	releaseHeld := requireHeld(t, token, "member m1", held)
	queued := startTokenAcquire(context.Background(), token, "member m2", held)
	if wait := awaitQueued(t, queued, "member m2"); wait.Holder != "member m1" {
		t.Fatalf("queued peer wait = %+v, want member m1", wait)
	}

	// A peer whose scope is disjoint arrives after the queued one: it must not
	// inherit that queue, or one conflicted file would stall every other file.
	other := WriteIntent{Paths: []string{filepath.Join(root, "internal", "other.go")}, Label: "internal/other.go"}
	releaseOther := requireHeld(t, token, "member m3", other)
	requireNotAdmitted(t, queued, "member m2")

	releaseOther()
	releaseHeld()
	awaitAdmitted(t, queued, "member m2")()
}

func TestWriteIntentTokenWholeWorkspaceBlocksEveryScope(t *testing.T) {
	root := t.TempDir()
	token := NewWriteIntentToken(root)
	whole := WriteIntent{Whole: true, Label: "the whole workspace"}
	file := WriteIntent{Paths: []string{filepath.Join(root, "internal", "a.go")}, Label: "internal/a.go"}

	releaseWhole := requireHeld(t, token, "member m1", whole)
	behind := startTokenAcquire(context.Background(), token, "member m2", file)
	if wait := awaitQueued(t, behind, "member m2"); wait.Holder != "member m1" || wait.Scope != "the whole workspace" {
		t.Fatalf("path intent behind a whole-workspace holder = %+v, want that holder named", wait)
	}
	releaseWhole()
	awaitAdmitted(t, behind, "member m2")()

	// And the reverse: a live path holder blocks the whole-workspace call, which
	// can write that path.
	releaseFile := requireHeld(t, token, "member m3", file)
	wholeBehind := startTokenAcquire(context.Background(), token, "member m1", whole)
	awaitQueued(t, wholeBehind, "member m1")
	releaseFile()
	awaitAdmitted(t, wholeBehind, "member m1")()
}

func TestWriteIntentTokenCancelledWaiterReleasesThePeersBehindIt(t *testing.T) {
	root := t.TempDir()
	token := NewWriteIntentToken(root)
	intent := WriteIntent{Paths: []string{filepath.Join(root, "internal", "shared.go")}, Label: "internal/shared.go"}

	releaseHeld := requireHeld(t, token, "member m1", intent)
	canceled, cancel := context.WithCancel(context.Background())
	doomed := startTokenAcquire(canceled, token, "member m2", intent)
	behind := startTokenAcquire(context.Background(), token, "member m3", intent)
	awaitQueued(t, doomed, "member m2")
	awaitQueued(t, behind, "member m3")
	if active, queued := token.State(); active != 1 || queued != 2 {
		t.Fatalf("occupancy = %s, want one holder and two queued", occupancy(token))
	}

	cancel()
	select {
	case err := <-doomed.failed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled waiter error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled waiter never returned")
	}

	// The cancellation must not strand the peer queued behind it: releasing the
	// holder has to admit what is left of the queue.
	releaseHeld()
	awaitAdmitted(t, behind, "member m3")()
	if active, queued := token.State(); active != 0 || queued != 0 {
		t.Fatalf("occupancy after the cancelled queue drained = %s, want empty", occupancy(token))
	}
}

func TestWriteIntentTokenUnnameableScopeQueuesConservatively(t *testing.T) {
	root := t.TempDir()
	token := NewWriteIntentToken(root)

	releaseHeld := requireHeld(t, token, "member m1",
		WriteIntent{Paths: []string{filepath.Join(root, "inner.go")}, Label: "inner.go"})
	// Outside the workspace, so the normalizer refuses it: the intent must claim
	// the whole workspace rather than an empty scope, which would let it run
	// beside a holder that could be editing the same file through another path.
	outside := filepath.Join(filepath.Dir(root), "elsewhere.go")
	queued := startTokenAcquire(context.Background(), token, "member m2",
		WriteIntent{Paths: []string{outside}, Label: outside})
	if wait := awaitQueued(t, queued, "member m2"); wait.Holder != "member m1" {
		t.Fatalf("unnameable scope wait = %+v, want it queued behind the holder", wait)
	}
	releaseHeld()
	awaitAdmitted(t, queued, "member m2")()
}

// TestWriteIntentTokenNeverQueuesAPeerBehindItself keeps a deadlock out of the
// token: one agent's two overlapping calls (a batch, a reused peer label) must
// not wait for each other. The lease still excludes them.
func TestWriteIntentTokenNeverQueuesAPeerBehindItself(t *testing.T) {
	root := t.TempDir()
	token := NewWriteIntentToken(root)
	intent := WriteIntent{Paths: []string{filepath.Join(root, "internal", "shared.go")}, Label: "internal/shared.go"}

	first := requireHeld(t, token, "member m1", intent)
	second := requireHeld(t, token, "member m1", intent)
	if active, queued := token.State(); active != 2 || queued != 0 {
		t.Fatalf("same-peer occupancy = %s, want both admitted rather than self-queued", occupancy(token))
	}
	second()
	first()

	// A different peer still queues behind an overlapping holder, so the skip
	// above is about the peer identity, not about overlap.
	holder := requireHeld(t, token, "member m1", intent)
	queued := startTokenAcquire(context.Background(), token, "member m2", intent)
	if wait := awaitQueued(t, queued, "member m2"); wait.Holder != "member m1" {
		t.Fatalf("other-peer wait = %+v, want it queued behind member m1", wait)
	}
	holder()
	awaitAdmitted(t, queued, "member m2")()
}

func TestWriteIntentTokenSiblingFilesInOneDirectoryStayConcurrent(t *testing.T) {
	root := t.TempDir()
	token := NewWriteIntentToken(root)
	dir := filepath.Join(root, "internal", "team")

	// Two different files in one directory are what the write scheduler treats as
	// concurrent, so the token must not queue them.
	first := requireHeld(t, token, "member m1",
		WriteIntent{Paths: []string{filepath.Join(dir, "a.go")}, Label: "internal/team/a.go"})
	second := requireHeld(t, token, "member m2",
		WriteIntent{Paths: []string{filepath.Join(dir, "b.go")}, Label: "internal/team/b.go"})
	if active, queued := token.State(); active != 2 || queued != 0 {
		t.Fatalf("sibling files occupancy = %s, want both admitted", occupancy(token))
	}
	second()
	first()
}

// BenchmarkWriteIntentTokenAcquire measures the token's own cost. It now sits in
// front of every write tool call, so it has to stay far below the lease's own
// ~0.5ms acquisition (route §5.1) unless it buys back more than it costs.
func BenchmarkWriteIntentTokenAcquire(b *testing.B) {
	root := b.TempDir()
	token := NewWriteIntentToken(root)
	intent := WriteIntent{Paths: []string{filepath.Join(root, "internal", "team", "a.go")}, Label: "internal/team/a.go"}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		release, err := token.Acquire(context.Background(), "member m1", intent, nil)
		if err != nil {
			b.Fatal(err)
		}
		release()
	}
}

func TestWriteIntentTokenReleaseIsIdempotentAndNilSafe(t *testing.T) {
	root := t.TempDir()
	token := NewWriteIntentToken(root)
	release, err := token.Acquire(context.Background(), "member m1",
		WriteIntent{Paths: []string{filepath.Join(root, "a.go")}, Label: "a.go"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
	release()
	if active, queued := token.State(); active != 0 || queued != 0 {
		t.Fatalf("occupancy after a double release = %s, want empty", occupancy(token))
	}

	var nilToken *WriteIntentToken
	release, err = nilToken.Acquire(context.Background(), "member m1", WriteIntent{Whole: true}, nil)
	if err != nil {
		t.Fatalf("nil token: %v", err)
	}
	release()
	if active, queued := nilToken.State(); active != 0 || queued != 0 {
		t.Fatalf("nil token occupancy = (%d, %d)", active, queued)
	}
}
