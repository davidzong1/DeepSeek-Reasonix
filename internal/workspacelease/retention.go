package workspacelease

// Retention keeps a completed hold for a running background job, so the job does
// not re-acquire its domains on every write. The window is a loan against churn:
// a writer blocked on those domains repays it at once (see yield.go).

import "time"

// collectInactiveLocked ends or extends the retention after a hold completed. It
// is the one funnel where a hold completes while a job runs, so it is also where
// the retention is published: a hold that completes after the window was already
// armed must still be findable by a blocked peer.
func (o *Owner) collectInactiveLocked() []func() {
	if !o.hasInactiveLocked() {
		return nil
	}
	if o.activity.background > 0 {
		publishRetainedLocked(o)
		o.armGraceLocked()
		return nil
	}
	return o.takeInactiveLocked()
}

// hasInactiveLocked reports whether a completed hold is still held.
func (o *Owner) hasInactiveLocked() bool {
	for _, hold := range o.lease.holds {
		if hold.refs == 0 {
			return true
		}
	}
	return false
}

// repayRetainedLocked gives back every completed hold this Owner keeps for a
// running background job, for callers that are themselves blocked on it. The
// releases are returned so the caller can run them without this mutex.
func (o *Owner) repayRetainedLocked() []func() {
	if o.activity.background == 0 || !o.hasInactiveLocked() {
		return nil
	}
	return o.takeInactiveLocked()
}

// takeInactiveLocked releases the completed holds and returns their releases.
// Callers hold o.mu and run the releases afterwards.
func (o *Owner) takeInactiveLocked() []func() {
	o.cancelGraceLocked()
	var releases []func()
	for id, hold := range o.lease.holds {
		if hold.refs != 0 {
			continue
		}
		delete(o.lease.holds, id)
		if hold.release != nil {
			releases = append(releases, hold.release)
		}
	}
	if len(releases) > 0 {
		o.signalChangedLocked()
	}
	return releases
}

// armGraceLocked bounds a retention window: a resident job must not own the
// workspace past it, and the release must never fire under an active run.
func (o *Owner) armGraceLocked() {
	if o.graceAfter <= 0 || o.lease.graceTimer != nil {
		return
	}
	epoch := o.lease.epoch
	o.lease.graceTimer = time.AfterFunc(o.graceAfter, func() {
		o.mu.Lock()
		if o.lease.graceTimer == nil || o.lease.epoch != epoch {
			o.mu.Unlock()
			return
		}
		releases := o.takeInactiveLocked()
		o.mu.Unlock()
		runReleases(releases)
	})
}

// cancelGraceLocked ends a retention window. Callers hold o.mu (every call site
// is inside a locked section), which the publish and retract pair relies on.
func (o *Owner) cancelGraceLocked() {
	retractRetainedLocked(o)
	if o.lease.graceTimer != nil {
		o.lease.graceTimer.Stop()
		o.lease.graceTimer = nil
	}
}
