package cli

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/jobs"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// memberUsagePublishInterval is the writer's publish cadence. It is a liveness
// heartbeat, not a data rate: a reader hides the numbers once a document ages
// past followerUsageTTL, so the interval only has to be short enough that a live
// writer never looks stopped, and long enough that one small document per member
// per interval costs less than the frames it feeds.
const memberUsagePublishInterval = 2 * time.Second

// memberUsagePublisher publishes one writable member's usage gauges into that
// member's owner directory, for read-only windows in other processes.
//
// It owns its own cadence rather than riding the roster tick, because the tick
// is not a writer-liveness signal: leaving the team keeps the member backends
// alive — an assembled member keeps running its turn — while the tick stops, and
// a publisher riding it would fall silent with the writer still working, so a
// reader would hide numbers that are current.
//
// It is constructed at exactly one place: the writable branch of the member
// builder, after the session write authority is bound. Every other exit builds a
// read-only follower, and a follower has no publisher at all — so "only the
// session's writer publishes" is a property of the call graph, not a runtime
// check.
type memberUsagePublisher struct {
	owners *team.OwnerStore
	key    team.OwnerKey
	ctrl   control.SessionAPI

	stop    chan struct{}
	done    chan struct{}
	stopOne sync.Once
	mu      sync.Mutex
	started bool
	// lastFailure is the last publish failure reported, so a persistent fault is
	// said once instead of every interval. Telemetry is optional: a failure is
	// logged and dropped, never surfaced as member state.
	lastFailure string
}

// newMemberUsagePublisher returns a publisher for one writer member, or nil when
// there is no owner store to publish into (a host without team data).
func newMemberUsagePublisher(owners *team.OwnerStore, key team.OwnerKey, ctrl control.SessionAPI) *memberUsagePublisher {
	if owners == nil || ctrl == nil {
		return nil
	}
	return &memberUsagePublisher{
		owners: owners, key: key, ctrl: ctrl,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
}

// Start publishes once and then keeps the document fresh until Close. Called
// once, from the member builder; a nil publisher (no owner store) does nothing.
func (p *memberUsagePublisher) Start() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return
	}
	p.started = true
	p.mu.Unlock()

	slog.Info("team member usage publisher started", "team", p.key.TeamID, "member", p.key.MemberID)
	p.publish()
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(memberUsagePublishInterval)
		defer ticker.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-ticker.C:
				p.publish()
			}
		}
	}()
}

// Close stops the publisher and waits for the in-flight publish, so retirement
// order is real: the backend closes the publisher before the controller and the
// lease, and no publish can land after the writer gave its session up.
// Idempotent and nil-safe — memberLeasedBackend is stored by value, so copies of
// the backend are normal.
func (p *memberUsagePublisher) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	started := p.started
	p.mu.Unlock()
	p.stopOne.Do(func() { close(p.stop) })
	if started {
		<-p.done
	}
}

// publish writes one observation. A panic in a controller read must not take the
// process down — this runs on its own goroutine, where nothing recovers — so the
// sample is guarded like the status band's own reads.
func (p *memberUsagePublisher) publish() {
	if p == nil || p.owners == nil || p.ctrl == nil {
		return
	}
	usage, err := sampleMemberUsage(p.ctrl, time.Now())
	if err != nil {
		p.reportFailure("sample: " + err.Error())
		return
	}
	if err := p.owners.WriteUsage(context.Background(), p.key, usage); err != nil {
		p.reportFailure(err.Error())
		return
	}
	p.mu.Lock()
	p.lastFailure = ""
	p.mu.Unlock()
}

// reportFailure logs one publish fault, once per distinct cause.
func (p *memberUsagePublisher) reportFailure(reason string) {
	p.mu.Lock()
	repeat := p.lastFailure == reason
	p.lastFailure = reason
	p.mu.Unlock()
	if repeat {
		return
	}
	slog.Warn("team member usage publish failed", "team", p.key.TeamID, "member", p.key.MemberID, "reason", reason)
}

// sampleMemberUsage reads one writer's gauges for publication. The band reads
// these same five methods at frame rate on the update goroutine, and the
// follower must answer them without panicking, so the sample is total: a broken
// or absent backend answers zeros instead of taking down the publisher.
func sampleMemberUsage(ctrl control.SessionAPI, now time.Time) (usage team.OwnerUsage, err error) {
	usage.PublishedAt = now.UTC().Format(time.RFC3339Nano)
	if ctrl == nil {
		return usage, nil
	}
	defer func() {
		if r := recover(); r != nil {
			usage, err = team.OwnerUsage{}, fmt.Errorf("controller read panicked: %v", r)
		}
	}()
	usage.ContextUsed, usage.ContextWindow = ctrl.ContextSnapshot()
	usage.CompactRatio = ctrl.CompactRatio()
	usage.LastTurn = ownerUsageLastTurn(ctrl.LastUsage())
	usage.CacheHit, usage.CacheMiss = ctrl.SessionCache()
	usage.Jobs = ownerUsageJobs(ctrl.Jobs())
	return usage, nil
}

// ownerUsageLastTurn maps provider usage onto the published document. The wire
// shape is mirrored in package team (it must not import the provider tree), so
// this and providerUsageFromLastTurn below are the only two places the two
// shapes meet — and a test round-trips them.
func ownerUsageLastTurn(u *provider.Usage) *team.OwnerUsageLastTurn {
	if u == nil {
		return nil
	}
	return &team.OwnerUsageLastTurn{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CacheHitTokens:   u.CacheHitTokens,
		CacheMissTokens:  u.CacheMissTokens,
		ReasoningTokens:  u.ReasoningTokens,
		Unknown:          u.Unknown,
	}
}

// providerUsageFromLastTurn is the inverse of ownerUsageLastTurn.
func providerUsageFromLastTurn(in *team.OwnerUsageLastTurn) *provider.Usage {
	if in == nil {
		return nil
	}
	return &provider.Usage{
		PromptTokens:     in.PromptTokens,
		CompletionTokens: in.CompletionTokens,
		TotalTokens:      in.TotalTokens,
		CacheHitTokens:   in.CacheHitTokens,
		CacheMissTokens:  in.CacheMissTokens,
		ReasoningTokens:  in.ReasoningTokens,
		Unknown:          in.Unknown,
	}
}

// ownerUsageJobs maps the writer's running jobs onto the published document.
// Display data is published verbatim: a follower must render the writer's rows
// or nothing at all, never fabricated placeholders for a count.
func ownerUsageJobs(views []jobs.View) []team.OwnerUsageJob {
	if len(views) == 0 {
		return nil
	}
	out := make([]team.OwnerUsageJob, 0, len(views))
	for _, v := range views {
		out = append(out, team.OwnerUsageJob{
			ID: v.ID, Kind: v.Kind, Label: v.Label, Status: v.Status, StartedAt: v.StartedAt,
		})
	}
	return out
}

// jobViewsFromOwnerUsage is the inverse of ownerUsageJobs.
func jobViewsFromOwnerUsage(in []team.OwnerUsageJob) []jobs.View {
	if len(in) == 0 {
		return nil
	}
	out := make([]jobs.View, 0, len(in))
	for _, j := range in {
		out = append(out, jobs.View{ID: j.ID, Kind: j.Kind, Label: j.Label, Status: j.Status, StartedAt: j.StartedAt})
	}
	return out
}
