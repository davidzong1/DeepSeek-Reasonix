package workspacelease

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The metrics under test answer four field questions: how often a write queued,
// for which scope, for how long, and how often it gave up. Each case is decided
// by the snapshot or by the notice/State() of the Owner, never by a sleep.

func onlyScope(t *testing.T, m LeaseMetrics) ScopeWait {
	t.Helper()
	if len(m.Scopes) != 1 {
		t.Fatalf("blocked scopes = %+v, want exactly one (%s)", m.Scopes, m.Report())
	}
	return m.Scopes[0]
}

func onlyLock(t *testing.T, m LeaseMetrics) LockWait {
	t.Helper()
	if len(m.Locks) != 1 {
		t.Fatalf("blocked locks = %+v, want exactly one (%s)", m.Locks, m.Report())
	}
	return m.Locks[0]
}

// startBlockedWrite queues one write on a background goroutine and returns the
// two ways it can end. The caller decides when the blocker lets go.
func startBlockedWrite(t *testing.T, owner *Owner, ctx context.Context, hold func(context.Context) (func(), error)) (<-chan func(), <-chan error) {
	t.Helper()
	acquired := make(chan func(), 1)
	failed := make(chan error, 1)
	go func() {
		release, err := hold(ctx)
		if err != nil {
			failed <- err
			return
		}
		acquired <- release
	}()
	return acquired, failed
}

func requireHeld(t *testing.T, holder *Owner, path string) func() {
	t.Helper()
	release, err := holder.HoldWriteForPath(context.Background(), path)
	if err != nil {
		t.Fatalf("hold %s: %v", path, err)
	}
	if !holder.State().Acquired {
		t.Fatalf("holder did not acquire %s", path)
	}
	return release
}

func TestMetricsStaySilentWhenNothingQueues(t *testing.T) {
	root, locks := t.TempDir(), t.TempDir()
	owner := newOwnerWithGrace(t, root, locks, time.Minute)
	owner.BeginRun()
	defer owner.EndRun()
	for i := range 8 {
		release, err := owner.HoldWriteForPath(context.Background(), filepath.Join(root, "src", "file"+string(rune('a'+i))+".go"))
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	m := owner.Metrics()
	if m.Waits != 0 || m.Timeouts != 0 || m.InFlight != 0 || m.Repaid != 0 || m.Max != 0 || len(m.Scopes) != 0 || len(m.Locks) != 0 {
		t.Fatalf("uncontended writes recorded waits: %+v", m)
	}
	if got := owner.MetricsReport(); got != "workspace lease: no waits recorded" {
		t.Fatalf("report for an uncontended session = %q", got)
	}
}

func TestMetricsCountOneWaitPerBlockedAcquisition(t *testing.T) {
	root, locks := t.TempDir(), t.TempDir()
	target := filepath.Join(root, "internal", "shared.go")
	holder := newOwnerWithGrace(t, root, locks, time.Minute)
	peer, notices := newNoticeProbe(t, root, locks)
	holder.BeginRun()
	defer holder.EndRun()
	peer.BeginRun()
	defer peer.EndRun()
	release := requireHeld(t, holder, target)
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	acquired, failed := startBlockedWrite(t, peer, ctx, func(ctx context.Context) (func(), error) {
		return peer.HoldWriteForPath(ctx, target)
	})
	notices.requireQueued(t, "the blocked write")

	// A wait that is still running is visible as in flight, which is what makes
	// a live stall diagnosable instead of only a finished one.
	if got := peer.Metrics().InFlight; got != 1 {
		t.Fatalf("in-flight waits = %d, want 1 (%s)", got, peer.MetricsReport())
	}
	release()
	select {
	case acquired := <-acquired:
		acquired()
	case err := <-failed:
		t.Fatalf("blocked write failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("blocked write never acquired")
	}
	elapsed := time.Since(started)

	m := peer.Metrics()
	if m.Waits != 1 || m.Timeouts != 0 || m.InFlight != 0 {
		t.Fatalf("waits/timeouts/inFlight = %d/%d/%d, want 1/0/0 (%s)", m.Waits, m.Timeouts, m.InFlight, m.Report())
	}
	scope := onlyScope(t, m)
	if scope.Scope != "shared.go" || scope.Waits != 1 || scope.Timeouts != 0 {
		t.Fatalf("blocked scope = %+v, want shared.go 1", scope)
	}
	if m.Max <= 0 || m.Max > elapsed {
		t.Fatalf("max wait %s outside (0, %s]", m.Max, elapsed)
	}
	// The first lock a path write queues on is the path stripe itself.
	lock := onlyLock(t, m)
	if lock.Lock != "file" || lock.Waits != 1 || lock.Timeouts != 0 {
		t.Fatalf("blocked lock = %+v, want file 1", lock)
	}
	report := peer.MetricsReport()
	for _, want := range []string{"1 waits", "shared.go 1", "blocked locks: file 1"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report %q does not mention %q", report, want)
		}
	}
	if owner := holder.Metrics(); owner.Waits != 0 {
		t.Fatalf("the holder recorded a wait it never made: %+v", owner)
	}
}

func TestMetricsAttributeAWorkspaceWaitToTheWorkspaceScope(t *testing.T) {
	root, locks := t.TempDir(), t.TempDir()
	holder := newOwnerWithGrace(t, root, locks, time.Minute)
	peer, notices := newNoticeProbe(t, root, locks)
	holder.BeginRun()
	defer holder.EndRun()
	peer.BeginRun()
	defer peer.EndRun()
	release := requireHeld(t, holder, filepath.Join(root, "internal", "held.go"))
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	acquired, failed := startBlockedWrite(t, peer, ctx, func(ctx context.Context) (func(), error) {
		return peer.HoldWrite(ctx)
	})
	notices.requireQueued(t, "the blocked whole-workspace write")
	release()
	select {
	case acquired := <-acquired:
		acquired()
	case err := <-failed:
		t.Fatalf("blocked workspace write failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("blocked workspace write never acquired")
	}

	m := peer.Metrics()
	t.Logf("a blocked workspace write reports:\n%s", m.Report())
	scope := onlyScope(t, m)
	if scope.Scope != "whole workspace" {
		t.Fatalf("blocked scope = %q, want the whole workspace (%s)", scope.Scope, m.Report())
	}
	if lock := onlyLock(t, m); lock.Lock != "workspace" {
		t.Fatalf("blocked lock = %+v, want the workspace root lock", lock)
	}
}

func TestMetricsCountGivingUpAsATimeout(t *testing.T) {
	root, locks := t.TempDir(), t.TempDir()
	target := filepath.Join(root, "internal", "shared.go")
	holder := newOwnerWithGrace(t, root, locks, time.Minute)
	peer, notices := newNoticeProbe(t, root, locks)
	holder.BeginRun()
	defer holder.EndRun()
	peer.BeginRun()
	defer peer.EndRun()
	release := requireHeld(t, holder, target)
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, failed := startBlockedWrite(t, peer, ctx, func(ctx context.Context) (func(), error) {
		return peer.HoldWriteForPath(ctx, target)
	})
	notices.requireQueued(t, "the write that gives up")
	cancel()
	select {
	case err := <-failed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("giving up reported %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled write stayed queued")
	}

	m := peer.Metrics()
	if m.Waits != 1 || m.Timeouts != 1 || m.InFlight != 0 {
		t.Fatalf("waits/timeouts/inFlight = %d/%d/%d, want 1/1/0 (%s)", m.Waits, m.Timeouts, m.InFlight, m.Report())
	}
	scope := onlyScope(t, m)
	if scope.Timeouts != 1 || scope.Waits != 1 || m.Max <= 0 {
		t.Fatalf("timed-out scope = %+v (max %s), want 1 timeout and a measured wait", scope, m.Max)
	}
	// The timeout is attributed to the lock file that actually held the wait,
	// which is what points a reader at the domain to fix.
	if lock := onlyLock(t, m); lock.Lock != "file" || lock.Timeouts != 1 {
		t.Fatalf("timed-out lock = %+v, want file with 1 timeout", lock)
	}
}

func TestMetricsCountPeerRepaidLoansButNotSelfRepayments(t *testing.T) {
	root, locks := t.TempDir(), t.TempDir()
	target := filepath.Join(root, "internal", "shared.go")
	farmer := newOwnerWithGrace(t, root, locks, time.Minute)
	peer, peerNotices := newNoticeProbe(t, root, locks)
	defer close(retainCompletedHold(t, farmer, pathHold(farmer, target)))

	peer.BeginRun()
	defer peer.EndRun()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release, err := peer.HoldWriteForPath(ctx, target)
	if err != nil {
		t.Fatalf("peer did not take back the retained loan: %v", err)
	}
	release()
	peerNotices.requireNoWait(t, "a peer repaying a loan")
	// The loan is repaid on the lender's ledger: the served peer never queued, so
	// its own snapshot must stay silent, and the lender is the only side that can
	// tell a served wait from one that never happened.
	if m := peer.Metrics(); m.Waits != 0 || m.Repaid != 0 {
		t.Fatalf("the served peer recorded waits/repaid = %d/%d (%s)", m.Waits, m.Repaid, m.Report())
	}
	lender := farmer.Metrics()
	if lender.Repaid != 1 || lender.Waits != 0 {
		t.Fatalf("lender repaid/waits = %d/%d, want 1/0 (%s)", lender.Repaid, lender.Waits, lender.Report())
	}
	if report := farmer.MetricsReport(); !strings.Contains(report, "1 repaid loans") {
		t.Fatalf("report %q does not count the repaid loan", report)
	}

	// An Owner that repays its own loan saved a wait; it did not serve a peer,
	// so it must not inflate the number that measures the yield's value.
	self := newOwnerWithGrace(t, root, locks, time.Minute)
	other := filepath.Join(root, "internal", "own.go")
	defer close(retainCompletedHold(t, self, pathHold(self, other)))
	self.BeginRun()
	defer self.EndRun()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if err := self.AcquireWrite(ctx2); err != nil {
		t.Fatalf("owner waited out its own retained hold: %v", err)
	}
	if m := self.Metrics(); m.Repaid != 0 || m.Waits != 0 {
		t.Fatalf("self-repayment counted as a served peer: %+v", m)
	}
}

func TestWaitHistogramReportsBucketBoundsOnly(t *testing.T) {
	var h waitHistogram
	for _, d := range []time.Duration{2 * time.Millisecond, 20 * time.Millisecond, 2 * time.Second} {
		h.add(d)
	}
	if got := percentileUpper(h, 3, 0.50, int64(2*time.Second)); got != 100*time.Millisecond {
		t.Fatalf("p50 upper = %s, want the 100ms bucket", got)
	}
	if got := percentileUpper(h, 3, 0.90, int64(2*time.Second)); got != 10*time.Second {
		t.Fatalf("p90 upper = %s, want the 10s bucket", got)
	}
	// A wait past the last bucket answers with the longest wait observed rather
	// than pretending the open-ended bucket has a bound.
	var long waitHistogram
	longWait := 20 * time.Minute
	long.add(longWait)
	if got := percentileUpper(long, 1, 0.99, int64(longWait)); got != longWait {
		t.Fatalf("overflow bucket answered %s, want the observed %s", got, longWait)
	}
	if got := percentileUpper(waitHistogram{}, 0, 0.99, 0); got != 0 {
		t.Fatalf("empty histogram answered %s, want no upper bound", got)
	}
	// A wait exactly on a bound counts in that bound's bucket, so no wait is
	// reported as longer than it was.
	if got := bucketFor(time.Minute); waitBuckets[got] != time.Minute {
		t.Fatalf("a minute landed in a %s bucket", waitBuckets[got])
	}
	if got := bucketFor(waitBuckets[len(waitBuckets)-1] + time.Second); got != len(waitBuckets) {
		t.Fatalf("a wait past the last bound landed in bucket %d, want the overflow bucket", got)
	}
}

func TestLockClassNamesEveryLockDomain(t *testing.T) {
	root, locks := t.TempDir(), t.TempDir()
	owner := newOwnerWithGrace(t, root, locks, time.Minute)
	key, _, err := canonicalFileKey(filepath.Join(root, "internal", "x.go"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		lock string
		want string
	}{
		{owner.lockPath + queueLockSuffix, "queue"},
		{owner.pathLockPath(key) + queueLockSuffix, "queue"},
		{owner.lockPath, "workspace"},
		{owner.holderRootPath, "workspace"},
		{owner.pathLockPath(key), "file"},
		{owner.treeLockPath(key), "directory"},
		{filepath.Join(locks, "0123456789abcdef.lock"), "ancestor"},
	}
	for _, tc := range cases {
		if got := owner.lockClass(tc.lock); got != tc.want {
			t.Fatalf("lockClass(%s) = %s, want %s", filepath.Base(tc.lock), got, tc.want)
		}
	}
}

func TestMetricsFoldScopesPastTheCap(t *testing.T) {
	const blocked = waitScopeCap + 3
	var m leaseMetrics
	for i := range blocked {
		m.beginAcquisitionLocked("scope-" + strconv.Itoa(i))
		m.noteBlockedLocked("file", time.Now())
		m.finishAcquisitionLocked(nil, time.Now())
	}
	if len(m.scopes) > waitScopeCap {
		t.Fatalf("scope map grew past its cap: %d entries", len(m.scopes))
	}
	folded, ok := m.scopes[overflowScopes]
	if !ok || folded.waits != blocked-(waitScopeCap-1) {
		t.Fatalf("scopes past the cap were not folded: %+v", m.scopes)
	}
	var waits uint64
	for _, scope := range m.scopes {
		waits += scope.waits
	}
	if waits != blocked {
		t.Fatalf("scopes hold %d waits, want every one of %d", waits, blocked)
	}
	if m.inFlight != 0 || m.blocked {
		t.Fatalf("finished acquisitions left a wait in flight: %+v", m)
	}
}

// A served wait is usually short, so the report must never round a real wait
// away: "0s" beside a wait count would read as "nothing happened".
func TestMetricsReportNeverRoundsAWaitAway(t *testing.T) {
	short := 200 * time.Microsecond
	m := LeaseMetrics{
		Waits: 1, Waited: short, Max: short, P50Upper: time.Millisecond,
		Scopes: []ScopeWait{{Scope: "whole workspace", Waits: 1, Waited: short, Max: short, P50Upper: time.Millisecond}},
		Locks:  []LockWait{{Lock: "workspace", Waits: 1}},
	}
	report := m.Report()
	for _, want := range []string{"1 waits", "waited <1ms total", "max <1ms", "whole workspace 1", "blocked locks: workspace 1"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report %q does not mention %q", report, want)
		}
	}
	if strings.Contains(report, "waited 0s") {
		t.Fatalf("report rounded a counted wait away: %q", report)
	}
	// The single line for a lease that never queued stays exactly as promised.
	if got := (LeaseMetrics{}).Report(); got != "workspace lease: no waits recorded" {
		t.Fatalf("report for an idle lease = %q", got)
	}
}

func TestMetricsOnAMissingLeaseReportUnavailable(t *testing.T) {
	var missing *Owner
	if got := missing.Metrics(); got.Waits != 0 || got.InFlight != 0 || len(got.Scopes) != 0 {
		t.Fatalf("a missing lease reported waits: %+v", got)
	}
	if got := missing.MetricsReport(); got != "workspace lease: unavailable" {
		t.Fatalf("report for a missing lease = %q", got)
	}
}
