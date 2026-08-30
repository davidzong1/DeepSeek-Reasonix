package cli

// Concurrent acceptance tests for the lock-free assembly contract (§W1):
// distinct members' binds overlap, the same member assembles once, and a
// failing build stays isolated to its member.

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/team"
)

// buildProbe records assembly per member and can hold named members blocked
// until the test releases them, standing in for a slow boot.Build.
type buildProbe struct {
	mu    sync.Mutex
	calls map[string]int
	block map[string]chan struct{}
}

// build returns the probe's assembly function. Members present in block wait
// until their release channel closes before returning.
func (p *buildProbe) build(b team.MemberBinding) (control.SessionAPI, error) {
	p.mu.Lock()
	p.calls[b.MemberID]++
	release := p.block[b.MemberID]
	p.mu.Unlock()
	if release != nil {
		<-release
	}
	return fakeBackend{closed: new(int)}, nil
}

// release unblocks the named member's next build.
func (p *buildProbe) release(member string) {
	p.mu.Lock()
	if ch := p.block[member]; ch != nil {
		p.block[member] = nil
		close(ch)
	}
	p.mu.Unlock()
}

func (p *buildProbe) callsFor(member string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[member]
}

func newBuildProbe(block ...string) *buildProbe {
	p := &buildProbe{calls: map[string]int{}, block: map[string]chan struct{}{}}
	for _, m := range block {
		p.block[m] = make(chan struct{})
	}
	return p
}

// TestTeamBackendsDistinctMembersAssembleInParallel pins the lock-free
// contract: while alpha's assembly is still blocked, beta's bind completes —
// a slow member boot never serializes the fleet behind it.
func TestTeamBackendsDistinctMembersAssembleInParallel(t *testing.T) {
	probe := newBuildProbe("alpha")
	r := newTeamBackends(probe.build, 4)

	alphaDone := make(chan error, 1)
	go func() {
		_, err := r.bind(binding("t", "alpha"))
		alphaDone <- err
	}()

	// Give the alpha build a moment to reach its block, then bind beta. beta's
	// build must not wait on alpha's guard.
	time.Sleep(20 * time.Millisecond)
	betaDone := make(chan error, 1)
	go func() {
		_, err := r.bind(binding("t", "beta"))
		betaDone <- err
	}()

	select {
	case err := <-betaDone:
		if err != nil {
			t.Fatalf("beta bind while alpha assembles = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("beta bind blocked behind alpha's in-flight assembly")
	}
	probe.release("alpha")
	if err := <-alphaDone; err != nil {
		t.Fatalf("alpha bind after release = %v", err)
	}
	if got := probe.callsFor("alpha"); got != 1 {
		t.Fatalf("alpha builds = %d, want 1", got)
	}
	if got := probe.callsFor("beta"); got != 1 {
		t.Fatalf("beta builds = %d, want 1", got)
	}
}

// TestTeamBackendsSameMemberAssemblesOnce pins the per-key in-flight guard:
// N concurrent binds of one member share a single build — the waiters return
// the leader's backend instead of assembling a duplicate.
func TestTeamBackendsSameMemberAssemblesOnce(t *testing.T) {
	probe := newBuildProbe("alpha")
	r := newTeamBackends(probe.build, 4)

	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = r.bind(binding("t", "alpha"))
		}(i)
	}
	// Wait until every caller is inside bind (the guard is held by the first),
	// then release the single build. The guard guarantees only one build ran.
	deadline := time.After(2 * time.Second)
	for probe.callsFor("alpha") == 0 {
		select {
		case <-deadline:
			t.Fatal("alpha build never started")
		default:
		}
	}
	time.Sleep(20 * time.Millisecond) // let all callers reach the guard
	probe.release("alpha")
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent bind #%d = %v", i, err)
		}
	}
	if got := probe.callsFor("alpha"); got != 1 {
		t.Fatalf("alpha builds = %d, want exactly 1 (in-flight guard dedup)", got)
	}
}

// TestTeamBackendsFailedBuildIsolatesMember pins failure isolation: a build
// that errors is isolated to its member — another member's bind proceeds
// concurrently, and a retry after the failure rebuilds cleanly (the guard is
// not wedged by the error).
func TestTeamBackendsFailedBuildIsolatesMember(t *testing.T) {
	boom := errors.New("no credential")
	probe := newBuildProbe("alpha")
	r := newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		if b.MemberID == "alpha" {
			return nil, boom
		}
		return probe.build(b)
	}, 4)

	if _, err := r.bind(binding("t", "alpha")); !errors.Is(err, boom) {
		t.Fatalf("alpha bind err = %v, want %v", err, boom)
	}
	if _, err := r.bind(binding("t", "beta")); err != nil {
		t.Fatalf("beta bind after alpha's failure = %v", err)
	}
	// A retry on the failed member rebuilds (build is called again, guard freed).
	if _, err := r.bind(binding("t", "alpha")); !errors.Is(err, boom) {
		t.Fatalf("alpha retry err = %v, want %v (guard must be free after the failure)", err, boom)
	}
	if got := probe.callsFor("beta"); got != 1 {
		t.Fatalf("beta builds = %d, want 1", got)
	}
}

// TestTeamBackendsConcurrentMixedBindsRaces the registry under a mixed load —
// distinct members and the same member both in flight, with a failed build in
// the mix — under the race detector. It is the stress counterpart to the
// behavioral tests above.
func TestTeamBackendsConcurrentMixedBindsRace(t *testing.T) {
	boom := errors.New("boom")
	var failed atomic.Bool
	closed := 0
	r := newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		if b.MemberID == "bomb" && !failed.Load() {
			return nil, boom
		}
		time.Sleep(time.Millisecond)
		return fakeBackend{closed: &closed}, nil
	}, 4)
	ids := []string{"a", "b", "c", "bomb"}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				id := ids[i%len(ids)]
				if _, err := r.bind(binding("t", id)); err != nil && !errors.Is(err, boom) {
					t.Errorf("bind %s: %v", id, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	failed.Store(true)
	// After the bomb cleared, the same member builds once more and is reusable.
	if _, err := r.bind(binding("t", "bomb")); err != nil {
		t.Fatalf("bomb retry = %v", err)
	}
	r.closeAll()
}

// TestTeamBackendsReleaseWhileBuildingClosesResult pins the abandonment path:
// a member released while its backend is still assembling must not register the
// finished controller — the bind fails and the fresh backend is closed.
func TestTeamBackendsReleaseWhileBuildingClosesResult(t *testing.T) {
	probe := newBuildProbe("alpha")
	var closed atomic.Int64
	r := newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		probe.mu.Lock()
		probe.calls[b.MemberID]++
		release := probe.block[b.MemberID]
		probe.mu.Unlock()
		if release != nil {
			<-release
		}
		closeFn := func() { closed.Add(1) }
		return countingBackend{SessionAPI: fakeBackend{}, onClose: closeFn}, nil
	}, 4)

	done := make(chan error, 1)
	go func() {
		_, err := r.bind(binding("t", "alpha"))
		done <- err
	}()
	// Wait until the build goroutine registered the in-flight guard (it enters
	// build and blocks on the release channel), so the release below races a
	// genuinely in-flight assembly rather than arriving before the guard exists.
	deadline := time.After(2 * time.Second)
	for probe.callsFor("alpha") == 0 {
		select {
		case <-deadline:
			t.Fatal("alpha build never started")
		default:
		}
	}
	r.release("t", "alpha") // retire while the build is still in flight
	probe.release("alpha")
	if err := <-done; !errors.Is(err, errBackendStopped) {
		t.Fatalf("bind racing a release = %v, want %v", err, errBackendStopped)
	}
	if got := closed.Load(); got != 1 {
		t.Fatalf("closed backends = %d, want 1 (the finished controller)", got)
	}
}

// countingBackend wraps a backend and counts Close calls atomically.
type countingBackend struct {
	control.SessionAPI
	onClose func()
}

func (c countingBackend) Close() { c.onClose() }
