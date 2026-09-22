package workspacelease

// A completed tool call keeps its hold for a running background job, so the job
// does not re-acquire its domains. That window is a loan: this registry is how a
// blocked writer finds the lender, process-locally, and repays it at once (L3b).

import (
	"slices"
	"strings"
	"sync"
)

// retainedLocks maps one lock file to the Owners in this process still holding
// it for a completed tool call. Membership means "an inactive hold covers this
// path", so releasing those holds is always safe: it can only let go of a hold
// no caller is using.
var retainedLocks = struct {
	mu     sync.Mutex
	byPath map[string]map[*Owner]struct{}
}{byPath: map[string]map[*Owner]struct{}{}}

// publishRetainedLocked registers the inactive holds of one Owner and remembers
// what it registered. Callers hold o.mu, which is what makes the publish/retract
// pair exact: both run only under that mutex, so the registry never disagrees
// with the hold set.
func publishRetainedLocked(o *Owner) {
	paths := o.retainedPathsLocked()
	if len(paths) == 0 {
		return
	}
	retainedLocks.mu.Lock()
	defer retainedLocks.mu.Unlock()
	for _, path := range paths {
		owners := retainedLocks.byPath[path]
		if owners == nil {
			owners = map[*Owner]struct{}{}
			retainedLocks.byPath[path] = owners
		}
		owners[o] = struct{}{}
		o.lease.retained = appendUniquePaths(o.lease.retained, []string{path})
	}
}

// retractRetainedLocked removes whatever this Owner published. Callers hold
// o.mu. It returns without touching the registry for an Owner that has never
// published, which is the common case: sessions with no background job.
func retractRetainedLocked(o *Owner) {
	if len(o.lease.retained) == 0 {
		return
	}
	paths := o.lease.retained
	o.lease.retained = nil
	retainedLocks.mu.Lock()
	defer retainedLocks.mu.Unlock()
	for _, path := range paths {
		owners := retainedLocks.byPath[path]
		if owners == nil {
			continue
		}
		delete(owners, o)
		if len(owners) == 0 {
			delete(retainedLocks.byPath, path)
		}
	}
}

// retainedPathsLocked lists the lock files a completed hold still covers.
func (o *Owner) retainedPathsLocked() []string {
	var paths []string
	for _, hold := range o.lease.holds {
		if hold.refs == 0 {
			paths = appendUniquePaths(paths, hold.paths)
		}
	}
	return paths
}

// appendUniquePaths keeps one copy of each path, preserving order so a publish
// is deterministic.
func appendUniquePaths(paths []string, more []string) []string {
	for _, path := range more {
		if path != "" && !slices.Contains(paths, path) {
			paths = append(paths, path)
		}
	}
	return paths
}

// yieldRetainedOn repays the retained holds this process keeps on one lock file
// and reports whether any Owner was asked. The owners are collected before any
// of them is touched, so the registry mutex is never held while an Owner's own
// mutex is taken — publications run in the opposite order.
func yieldRetainedOn(lockPath string) bool {
	if strings.TrimSpace(lockPath) == "" {
		return false
	}
	retainedLocks.mu.Lock()
	var owners []*Owner
	for owner := range retainedLocks.byPath[lockPath] {
		owners = append(owners, owner)
	}
	retainedLocks.mu.Unlock()
	if len(owners) == 0 {
		return false
	}
	yielded := false
	for _, owner := range owners {
		if owner.yieldRetainedLocks() {
			yielded = true
		}
	}
	return yielded
}

// yieldRetainedLocks gives back every completed hold this Owner keeps for a
// running background job. A hold with live references is never touched, so this
// can only shorten a retention window, never widen a write's scope.
func (o *Owner) yieldRetainedLocks() bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	releases := o.repayRetainedLocked()
	if len(releases) > 0 {
		o.lease.metrics.noteRepaidLocked()
	}
	o.mu.Unlock()
	if len(releases) == 0 {
		return false
	}
	runReleases(releases)
	return true
}
