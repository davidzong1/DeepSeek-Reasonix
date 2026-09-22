package workspacelease

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// waitBuckets bound a wait's duration. A percentile is reported as the bound of
// the bucket a wait fell into, so it reads as "at most this long" — the honest
// reading for a histogram that has to stay bounded in a long session.
var waitBuckets = [...]time.Duration{
	time.Millisecond, 10 * time.Millisecond, 100 * time.Millisecond,
	time.Second, 10 * time.Second, time.Minute, 10 * time.Minute,
}

// waitHistogram counts waits per bucket, plus one open-ended overflow bucket.
type waitHistogram [len(waitBuckets) + 1]uint64

// overflowScopes is the key distinct blocked scopes fold into past the cap.
const overflowScopes = "other scopes"

// waitScopeCap bounds the distinct scopes one session tracks, so a long session
// writing many files cannot grow this map without limit. 64 covers any team.
const waitScopeCap = 64

// waitStat is one blocked scope's totals. It is only written while a writer is
// queued, so an uncontended write never touches it.
type waitStat struct {
	waits    uint64
	timeouts uint64
	nanos    int64
	maxNanos int64
	hist     waitHistogram
}

func (s *waitStat) add(d time.Duration) {
	nanos := int64(d)
	s.nanos += nanos
	if nanos > s.maxNanos {
		s.maxNanos = nanos
	}
	s.hist.add(d)
}

func (h *waitHistogram) add(d time.Duration) {
	h[bucketFor(d)]++
}

func bucketFor(d time.Duration) int {
	for i, bound := range waitBuckets {
		if d <= bound {
			return i
		}
	}
	return len(waitBuckets)
}

// percentileUpper is the bound of the bucket the p-th wait fell into. An
// overflowing bucket answers with the longest wait observed, which is still an
// upper bound on the waits counted in it.
func percentileUpper(h waitHistogram, total uint64, p float64, maxNanos int64) time.Duration {
	if total == 0 {
		return 0
	}
	threshold := uint64(math.Ceil(p * float64(total)))
	if threshold == 0 {
		threshold = 1
	}
	var seen uint64
	for i, count := range h {
		seen += count
		if seen < threshold {
			continue
		}
		if i < len(waitBuckets) {
			return waitBuckets[i]
		}
		return time.Duration(maxNanos)
	}
	return time.Duration(maxNanos)
}

// leaseMetrics is what the lease cost the writers that had to queue. It lives
// under o.mu like the rest of the lease and is only touched while a writer is
// blocked, so the uncontended path pays nothing for it.
type leaseMetrics struct {
	waits     uint64
	timeouts  uint64
	repaid    uint64
	inFlight  int
	nanos     int64
	maxNanos  int64
	hist      waitHistogram
	scopes    map[string]*waitStat
	locks     map[string]*waitStat
	blocked   bool
	label     string
	startedAt time.Time
}

// beginAcquisitionLocked names the scope one acquisition is queued for. The
// timestamp is taken when it first blocks instead, so a write that never waits
// never reads the clock.
func (m *leaseMetrics) beginAcquisitionLocked(label string) {
	m.blocked = false
	m.startedAt = time.Time{}
	if strings.TrimSpace(label) == "" {
		label = "whole workspace"
	}
	m.label = label
}

// noteBlockedLocked counts one queued acquisition and the lock file that queued
// it. Repeated lock files inside one acquisition count once for the scope.
func (m *leaseMetrics) noteBlockedLocked(class string, now time.Time) {
	stat := m.scopeStat(m.label)
	if !m.blocked {
		m.blocked = true
		m.waits++
		m.inFlight++
		m.startedAt = now
		stat.waits++
	}
	m.lockStat(class).waits++
}

// finishAcquisitionLocked records what a queued acquisition cost, including the
// waits its caller gave up on: any other error is a failure, not a timeout.
func (m *leaseMetrics) finishAcquisitionLocked(err error, now time.Time) {
	if !m.blocked {
		m.label = ""
		return
	}
	elapsed := now.Sub(m.startedAt)
	stat := m.scopeStat(m.label)
	if elapsed > 0 {
		stat.add(elapsed)
		m.nanos += int64(elapsed)
		if int64(elapsed) > m.maxNanos {
			m.maxNanos = int64(elapsed)
		}
		m.hist.add(elapsed)
	}
	if isWaitTimeout(err) {
		m.timeouts++
		stat.timeouts++
	}
	m.inFlight--
	m.blocked = false
	m.label = ""
	m.startedAt = time.Time{}
}

// noteRepaidLocked counts a retained loan a blocked peer took back. It lands on
// the lender, the only Owner that can tell a served wait from an empty one.
func (m *leaseMetrics) noteRepaidLocked() {
	m.repaid++
}

// noteLockTimeoutLocked counts a lock file a writer gave up waiting on.
func (m *leaseMetrics) noteLockTimeoutLocked(class string) {
	m.lockStat(class).timeouts++
}

func (m *leaseMetrics) scopeStat(label string) *waitStat {
	if m.scopes == nil {
		m.scopes = map[string]*waitStat{}
	}
	if stat := m.scopes[label]; stat != nil {
		return stat
	}
	// The overflow bucket needs a slot of its own, so the cap counts both.
	if len(m.scopes) >= waitScopeCap-1 {
		label = overflowScopes
		if stat := m.scopes[label]; stat != nil {
			return stat
		}
	}
	stat := &waitStat{}
	m.scopes[label] = stat
	return stat
}

func (m *leaseMetrics) lockStat(class string) *waitStat {
	if m.locks == nil {
		m.locks = map[string]*waitStat{}
	}
	stat := m.locks[class]
	if stat == nil {
		stat = &waitStat{}
		m.locks[class] = stat
	}
	return stat
}

// lockClass names one lock file's role in the hierarchy: the finest answer to
// "what blocked the writer" that needs no canonical key.
func (o *Owner) lockClass(lockPath string) string {
	if strings.HasSuffix(lockPath, queueLockSuffix) {
		return "queue"
	}
	if lockPath == o.lockPath || lockPath == o.holderRootPath {
		return "workspace"
	}
	switch base := filepath.Base(lockPath); {
	case strings.HasPrefix(base, "path-"):
		return "file"
	case strings.HasPrefix(base, "tree-"):
		return "directory"
	default:
		return "ancestor"
	}
}

// noteBlockedLock counts one lock file this writer queued on. It records the
// scope's wait once per acquisition and the lock file's queue every time.
func (o *Owner) noteBlockedLock(lockPath string) {
	now := time.Now()
	o.mu.Lock()
	o.lease.metrics.noteBlockedLocked(o.lockClass(lockPath), now)
	o.mu.Unlock()
}

// noteLockTimeout attributes a wait the caller stopped to the lock file it was
// queued on. A failure that is not a cancellation is not a timeout.
func (o *Owner) noteLockTimeout(lockPath string, err error) {
	if !isWaitTimeout(err) {
		return
	}
	o.mu.Lock()
	o.lease.metrics.noteLockTimeoutLocked(o.lockClass(lockPath))
	o.mu.Unlock()
}

func isWaitTimeout(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// ScopeWait is what one blocked scope cost: how often writers queued for it, how
// long they waited, and how many gave up waiting.
type ScopeWait struct {
	Scope    string
	Waits    uint64
	Timeouts uint64
	Waited   time.Duration
	Max      time.Duration
	P50Upper time.Duration
	P90Upper time.Duration
	P99Upper time.Duration
}

// LockWait is the per lock-file view of contention: which domain of the lock
// hierarchy blocked writers, independent of what they were writing.
type LockWait struct {
	Lock     string
	Waits    uint64
	Timeouts uint64
}

// LeaseMetrics is a process-local snapshot of what waiting for the write lease
// cost. Durations are bucket bounds, so they are upper bounds, never samples.
type LeaseMetrics struct {
	Waits    uint64
	Timeouts uint64
	// Repaid counts the retained loans this Owner gave back because another
	// Owner in this process was blocked on them. The served peer sees no wait.
	Repaid   uint64
	InFlight int
	Waited   time.Duration
	Max      time.Duration
	P50Upper time.Duration
	P90Upper time.Duration
	P99Upper time.Duration
	Scopes   []ScopeWait
	Locks    []LockWait
}

// Metrics returns the wait metrics without performing lease I/O. A lease that
// never queued reports zero in every field.
func (o *Owner) Metrics() LeaseMetrics {
	if o == nil {
		return LeaseMetrics{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	m := o.lease.metrics
	snapshot := LeaseMetrics{
		Waits: m.waits, Timeouts: m.timeouts, Repaid: m.repaid, InFlight: m.inFlight,
		Waited: time.Duration(m.nanos), Max: time.Duration(m.maxNanos),
		P50Upper: percentileUpper(m.hist, m.waits, 0.50, m.maxNanos),
		P90Upper: percentileUpper(m.hist, m.waits, 0.90, m.maxNanos),
		P99Upper: percentileUpper(m.hist, m.waits, 0.99, m.maxNanos),
	}
	for scope, stat := range m.scopes {
		snapshot.Scopes = append(snapshot.Scopes, scopeWait(scope, stat))
	}
	sortScopeWaits(snapshot.Scopes)
	for lock, stat := range m.locks {
		snapshot.Locks = append(snapshot.Locks, LockWait{Lock: lock, Waits: stat.waits, Timeouts: stat.timeouts})
	}
	sortLockWaits(snapshot.Locks)
	return snapshot
}

func scopeWait(scope string, stat *waitStat) ScopeWait {
	return ScopeWait{
		Scope: scope, Waits: stat.waits, Timeouts: stat.timeouts,
		Waited: time.Duration(stat.nanos), Max: time.Duration(stat.maxNanos),
		P50Upper: percentileUpper(stat.hist, stat.waits, 0.50, stat.maxNanos),
		P90Upper: percentileUpper(stat.hist, stat.waits, 0.90, stat.maxNanos),
		P99Upper: percentileUpper(stat.hist, stat.waits, 0.99, stat.maxNanos),
	}
}

// sortScopeWaits orders the busiest scope first, so a report leads with the one
// that costs the most. Ties fall back to the name for a stable output.
func sortScopeWaits(scopes []ScopeWait) {
	sort.Slice(scopes, func(i, j int) bool {
		if scopes[i].Waits == scopes[j].Waits {
			return scopes[i].Scope < scopes[j].Scope
		}
		return scopes[i].Waits > scopes[j].Waits
	})
}

func sortLockWaits(locks []LockWait) {
	sort.Slice(locks, func(i, j int) bool {
		if locks[i].Waits == locks[j].Waits {
			return locks[i].Lock < locks[j].Lock
		}
		return locks[i].Waits > locks[j].Waits
	})
}

// reportScopeCap bounds how many scopes one report line lists; the remaining
// ones are summarized as a count, so a long session cannot flood a log.
const reportScopeCap = 6

// MetricsReport renders the metrics as a compact block for a diagnostics
// artifact or a log line. A lease that never queued prints one line saying so.
func (o *Owner) MetricsReport() string {
	if o == nil {
		return "workspace lease: unavailable"
	}
	return o.Metrics().Report()
}

// Report renders one snapshot. Every duration is a rounded upper bound, so the
// text never claims more precision than the histogram has.
func (m LeaseMetrics) Report() string {
	if m.Waits == 0 && m.Timeouts == 0 && m.InFlight == 0 && m.Repaid == 0 {
		return "workspace lease: no waits recorded"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "workspace lease: %d waits, %d timeouts, %d in flight, %d repaid loans\n",
		m.Waits, m.Timeouts, m.InFlight, m.Repaid)
	fmt.Fprintf(&out, "  waited %s total, max %s, p50 <=%s, p90 <=%s, p99 <=%s\n",
		round(m.Waited), round(m.Max), round(m.P50Upper), round(m.P90Upper), round(m.P99Upper))
	if len(m.Scopes) > 0 {
		parts := make([]string, 0, reportScopeCap+1)
		for i, scope := range m.Scopes {
			if i == reportScopeCap {
				parts = append(parts, fmt.Sprintf("and %d more scopes", len(m.Scopes)-i))
				break
			}
			parts = append(parts, fmt.Sprintf("%s %d (waited %s, max %s, %d timeouts)",
				scope.Scope, scope.Waits, round(scope.Waited), round(scope.Max), scope.Timeouts))
		}
		fmt.Fprintf(&out, "  blocked scopes: %s\n", strings.Join(parts, "; "))
	}
	if len(m.Locks) > 0 {
		parts := make([]string, 0, len(m.Locks))
		for _, lock := range m.Locks {
			parts = append(parts, fmt.Sprintf("%s %d (%d timeouts)", lock.Lock, lock.Waits, lock.Timeouts))
		}
		fmt.Fprintf(&out, "  blocked locks: %s\n", strings.Join(parts, ", "))
	}
	return strings.TrimSuffix(out.String(), "\n")
}

// round keeps a duration readable: sub-second waits in milliseconds, longer ones
// in whole seconds. A wait shorter than a millisecond reads as "<1ms" rather
// than rounding to zero, so a counted wait never looks like no wait at all.
func round(d time.Duration) string {
	switch {
	case d <= 0:
		return "0s"
	case d < time.Millisecond:
		return "<1ms"
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	default:
		return d.Round(100 * time.Millisecond).String()
	}
}
