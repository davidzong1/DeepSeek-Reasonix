package cli

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/jobs"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// memberObservationSink returns the event sink one member backend emits into.
// A leader keeps the sink it was given: the baseline is a member dataset, and
// the leader runs the operator's own session, so recording it would report a
// different workload as a member's. A member's sink gains the observation
// wrapper, which forwards unchanged and records only once the publisher starts.
func memberObservationSink(observatory *memberUsagePublisher, inner event.Sink, leader bool) event.Sink {
	if leader {
		return inner
	}
	return observatory.Sink(inner)
}

// memberUsagePublishInterval is the writer's publish cadence. It is a liveness
// heartbeat, not a data rate: a reader hides the numbers once a document ages
// past followerUsageTTL, so the interval only has to be short enough that a live
// writer never looks stopped, and long enough that one small document per member
// per interval costs less than the frames it feeds.
const memberUsagePublishInterval = 2 * time.Second

// memberRequestQueue is how many observed requests a writer may hold before the
// oldest unrecorded one is dropped. The queue exists so a provider request never
// waits on telemetry disk I/O; it is not a durability buffer, and a full queue
// costs a diagnostic sample, never a member request. It is allocated by Sink, so
// a publisher that records nothing — a follower, the ambient window — carries
// none of it.
const memberRequestQueue = 64

// memberUsagePublisher publishes one writable member's usage gauges into that
// member's owner directory, for read-only windows in other processes, and
// records that member's per-request cache observations into its bounded log.
//
// It owns its own cadence rather than riding the roster tick, because the tick
// is not a writer-liveness signal: leaving the team keeps the member backends
// alive while the tick stops, and a publisher riding it would fall silent with
// the writer still working — so a reader would hide numbers that are current.
//
// It is constructed at exactly one place: the writable branch of the member
// builder, after the session write authority is bound. Every other exit builds a
// read-only follower, so "only the session's writer publishes" is a property of
// the call graph, not a runtime check. Recording is gated on Start for the same
// reason: the observation sink is installed before the controller exists, and
// only the writable path starts the publisher.
type memberUsagePublisher struct {
	owners *team.OwnerStore
	key    team.OwnerKey
	// route labels the route this member's requests travel, so a baseline can
	// separate one provider cache scope from another. It is fixed at build time
	// and empty when the builder cannot name a route.
	route string
	// ctrl is bound once, after the controller is built and before Start. It is
	// never read before then, and never rebound after.
	ctrl control.SessionAPI

	stop    chan struct{}
	done    chan struct{}
	stopOne sync.Once
	// requests carries observed requests from the emitting goroutine to the
	// publisher's own goroutine, so no provider request waits on this log. It
	// exists only once Sink has installed the observation wrapper.
	requests chan team.MemberCacheRequest

	mu      sync.Mutex
	started bool
	// lastFailure is the last publish failure reported, so a persistent fault is
	// said once instead of every interval. Telemetry is optional: a failure is
	// logged and dropped, never surfaced as member state.
	lastFailure string
	// observation is the writer's per-request state, grouped because the sink
	// tee moves all of it together on each observed request.
	observation memberUsageObservation
}

// memberUsageObservation is what the observation sink remembers between
// requests: the writer's own request counter, the previous request instant for
// the interval, and the latest gauges and diagnosis the snapshot republishes.
type memberUsageObservation struct {
	seq           int
	lastRequest   time.Time
	contextUsed   int
	contextWindow int
	diagnostics   *team.OwnerUsageLastTurnDiagnostics
}

// newMemberUsagePublisher returns a publisher for one writer member, or nil when
// there is no owner store to publish into (a host without team data). The
// controller is bound later with Bind: the publisher's observation sink must
// exist before the controller is built, while the controller it samples only
// exists after. route labels the provider cache scope those requests reach, and
// is empty for a publisher that records nothing.
func newMemberUsagePublisher(owners *team.OwnerStore, key team.OwnerKey, route string) *memberUsagePublisher {
	if owners == nil {
		return nil
	}
	return &memberUsagePublisher{
		owners: owners, key: key, route: strings.TrimSpace(route),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
}

// Bind attaches the controller this publisher samples. Called once, on the
// writable path, before Start. A nil controller leaves the publisher publishing
// zero gauges rather than panicking, which is the same total answer the sample
// gives for a broken backend.
func (p *memberUsagePublisher) Bind(ctrl control.SessionAPI) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.ctrl = ctrl
	p.mu.Unlock()
}

// Sink wraps one member's event sink with per-request observation. The returned
// sink forwards every event unchanged and never blocks, so installing it cannot
// change what the frontend sees or how fast the agent runs. Recording begins at
// Start, so a member backend that resolves to a read-only follower records
// nothing even though its sink carries the wrapper.
func (p *memberUsagePublisher) Sink(inner event.Sink) event.Sink {
	if p == nil {
		return inner
	}
	p.mu.Lock()
	if p.requests == nil {
		p.requests = make(chan team.MemberCacheRequest, memberRequestQueue)
	}
	p.mu.Unlock()
	return event.FuncSink(func(e event.Event) {
		p.observe(e)
		if inner != nil {
			inner.Emit(e)
		}
	})
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
	requests := p.requests
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
			case rec := <-requests:
				p.recordRequest(rec)
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

// observe records one emitted event. Only a usage event with a payload is a
// request worth recording; everything else is forwarded by the caller.
func (p *memberUsagePublisher) observe(e event.Event) {
	if p == nil || e.Kind != event.Usage || e.Usage == nil {
		return
	}
	p.mu.Lock()
	if !p.started || p.requests == nil {
		p.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	p.observation.seq++
	rec := memberCacheRequest(e, p.key, p.route, p.observation, now)
	p.observation.lastRequest = now
	p.observation.diagnostics = lastTurnCacheDiagnostics(e.CacheDiagnostics)
	queue := p.requests
	p.mu.Unlock()
	// The queue is drained by the publisher's own goroutine: a provider request
	// must never wait on this log, so a full queue drops the sample instead.
	select {
	case queue <- rec:
	default:
	}
}

// recordRequest writes one observed request to the member's bounded log. A
// failure is reported once per distinct cause and dropped: the member's turn is
// unaffected by telemetry storage.
func (p *memberUsagePublisher) recordRequest(rec team.MemberCacheRequest) {
	if err := p.owners.AppendCacheRequests(context.Background(), p.key, rec); err != nil {
		p.reportFailure("request log: " + err.Error())
	}
}

// publish writes one observation. A panic in a controller read must not take the
// process down — this runs on its own goroutine, where nothing recovers — so the
// sample is guarded like the status band's own reads.
func (p *memberUsagePublisher) publish() {
	if p == nil || p.owners == nil {
		return
	}
	p.mu.Lock()
	ctrl := p.ctrl
	diagnostics := p.observation.diagnostics
	p.mu.Unlock()
	usage, err := sampleMemberUsage(ctrl, time.Now(), diagnostics)
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
	p.observation.contextUsed, p.observation.contextWindow = usage.ContextUsed, usage.ContextWindow
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
//
// diagnostics is the latest observed request's prefix diagnosis, attached from
// the observation sink because the controller exposes no equivalent read. It
// therefore describes the last request the sink saw, up to one publish interval
// behind the gauges beside it.
func sampleMemberUsage(ctrl control.SessionAPI, now time.Time, diagnostics *team.OwnerUsageLastTurnDiagnostics) (usage team.OwnerUsage, err error) {
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
	if usage.LastTurn != nil {
		usage.LastTurn.CacheDiagnostics = diagnostics
	}
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
		PromptTokens:        u.PromptTokens,
		CompletionTokens:    u.CompletionTokens,
		TotalTokens:         u.TotalTokens,
		CacheHitTokens:      u.CacheHitTokens,
		CacheMissTokens:     u.CacheMissTokens,
		ReasoningTokens:     u.ReasoningTokens,
		Unknown:             u.Unknown,
		RequestCount:        u.RequestCount,
		RequestCountSource:  team.RequestCountSourceOf(u.RequestCount, u.RequestCountObserved),
		Estimated:           u.Estimated,
		CacheWriteTokens:    u.CacheWriteTokens,
		ContextPromptTokens: u.ContextPromptTokens,
	}
}

// providerUsageFromLastTurn is the inverse of ownerUsageLastTurn.
func providerUsageFromLastTurn(in *team.OwnerUsageLastTurn) *provider.Usage {
	if in == nil {
		return nil
	}
	return &provider.Usage{
		PromptTokens:        in.PromptTokens,
		CompletionTokens:    in.CompletionTokens,
		TotalTokens:         in.TotalTokens,
		CacheHitTokens:      in.CacheHitTokens,
		CacheMissTokens:     in.CacheMissTokens,
		ReasoningTokens:     in.ReasoningTokens,
		Unknown:             in.Unknown,
		RequestCount:        in.RequestCount,
		Estimated:           in.Estimated,
		CacheWriteTokens:    in.CacheWriteTokens,
		ContextPromptTokens: in.ContextPromptTokens,
	}
}

// memberCacheRequest projects one usage event onto the member's request record.
// It reads only what the event, the writer's own counters and the build-time
// route label carry: prompt content, tool arguments, credentials and file paths
// are not reachable here, which is what makes the record safe to persist.
//
// The bucket-keyed prompt is ContextPromptTokens, the settled attempt's own
// size; PromptTokens is the billable input, which for a multi-attempt recovery
// is the sum of every attempt and would misplace the sample in a larger bucket.
func memberCacheRequest(e event.Event, key team.OwnerKey, route string, obs memberUsageObservation, now time.Time) team.MemberCacheRequest {
	usage := e.Usage
	modelRef := strings.TrimSpace(e.ModelRef)
	providerName, _, _ := strings.Cut(modelRef, "/")
	if providerName == "" {
		// An event with no model ref still travelled a route the builder named,
		// so the wire kind is the honest provider of last resort.
		providerName, _, _ = strings.Cut(strings.TrimSpace(route), "/")
	}
	rec := team.MemberCacheRequest{
		RequestID:           cacheRequestID(e, key, obs.seq),
		RequestIDSource:     cacheRequestIDSource(e),
		ObservedAt:          now.UTC().Format(time.RFC3339Nano),
		TeamID:              key.TeamID,
		MemberID:            key.MemberID,
		Provider:            providerName,
		ModelRef:            modelRef,
		RouteBucket:         strings.TrimSpace(route),
		TurnID:              e.TurnID,
		SessionSequence:     e.Sequence,
		SessionRequestSeq:   obs.seq,
		PromptTokens:        usage.PromptTokens,
		ContextPromptTokens: usage.ContextPromptTokens,
		CacheHitTokens:      usage.CacheHitTokens,
		CacheMissTokens:     usage.CacheMissTokens,
		CacheWriteTokens:    usage.CacheWriteTokens,
		CompletionTokens:    usage.CompletionTokens,
		RequestCount:        max(usage.RequestCount, 1),
		// The count's provenance travels with it: a reader of the record must be
		// able to tell a measured request from the compatibility default, and this
		// is the last layer that knows which one the provider reported.
		RequestCountSource: team.RequestCountSourceOf(usage.RequestCount, usage.RequestCountObserved),
		UsageUnknown:       usage.Unknown,
		UsageEstimated:     usage.Estimated,
		UsageSource:        strings.TrimSpace(e.UsageSource),
		FinishReason:       usage.FinishReason,
		// The gauges are the last sampled ones, at most one publish interval
		// old. They are context-state reference only and never a bucket key.
		ContextUsed:   obs.contextUsed,
		ContextWindow: obs.contextWindow,
	}
	if rec.HasPrevRequest = !obs.lastRequest.IsZero(); rec.HasPrevRequest {
		rec.SecondsSincePrevRequest = now.Sub(obs.lastRequest).Seconds()
	}
	applyCacheDiagnostics(&rec, e.CacheDiagnostics)
	return rec
}

// cacheRequestID correlates one record with the usage event it came from. The
// emitted event carries no provider request id, so a turn-stamped sequence is
// preferred and the writer's own counter is the fallback; RequestIDSource
// records which one a sample used.
func cacheRequestID(e event.Event, key team.OwnerKey, seq int) string {
	if e.TurnID != "" && e.Sequence > 0 {
		return fmt.Sprintf("turn:%s:%d", e.TurnID, e.Sequence)
	}
	return fmt.Sprintf("writer:%s:%s:%d", key.TeamID, key.MemberID, seq)
}

func cacheRequestIDSource(e event.Event) string {
	if e.TurnID != "" && e.Sequence > 0 {
		return "turn_event"
	}
	return "writer"
}

// applyCacheDiagnostics copies the content-free prefix diagnosis onto a record.
// A nil diagnosis leaves DiagnosticsAvailable false, which is distinct from a
// diagnosis whose fields all happen to be zero: the second means the prefix was
// observed and unchanged, the first means nothing was observed at all.
func applyCacheDiagnostics(rec *team.MemberCacheRequest, d *event.CacheDiagnostics) {
	if rec == nil || d == nil {
		return
	}
	rec.DiagnosticsAvailable = true
	rec.PrefixHash = d.PrefixHash
	rec.StablePrefixHash = d.StablePrefixHash
	rec.PrefixChanged = d.PrefixChanged
	rec.StablePrefixChanged = d.StablePrefixChanged
	rec.PrefixChangeReasons = append([]string(nil), d.PrefixChangeReasons...)
	rec.ToolSchemaTokensEstimate = d.ToolSchemaTokens
	if sc := d.SessionContext; sc != nil {
		rec.SessionContextDigest = sc.Digest
		rec.SessionContextReasons = append([]string(nil), sc.Reasons...)
	}
}

// lastTurnCacheDiagnostics projects the published snapshot's diagnosis. It keeps
// only digests, hashes and enumerations, so the document stays content-free.
func lastTurnCacheDiagnostics(d *event.CacheDiagnostics) *team.OwnerUsageLastTurnDiagnostics {
	if d == nil {
		return nil
	}
	out := &team.OwnerUsageLastTurnDiagnostics{
		Available:           true,
		PrefixHash:          d.PrefixHash,
		StablePrefixHash:    d.StablePrefixHash,
		PrefixChanged:       d.PrefixChanged,
		StablePrefixChanged: d.StablePrefixChanged,
		PrefixChangeReasons: append([]string(nil), d.PrefixChangeReasons...),
		ToolSchemaTokens:    d.ToolSchemaTokens,
	}
	if sc := d.SessionContext; sc != nil {
		out.SessionContextDigest = sc.Digest
		out.SessionContextReasons = append([]string(nil), sc.Reasons...)
	}
	return out
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
