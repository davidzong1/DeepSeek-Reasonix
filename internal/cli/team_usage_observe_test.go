package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// cacheObservationFixture is one writable member: an owner store, a started
// publisher bound to a fake controller, and the sink the member emits into.
type cacheObservationFixture struct {
	usageOwnerFixture
	publisher *memberUsagePublisher
	sink      event.Sink
	forwarded []event.Event
}

// cacheObservationRoute is the route label the observation fixture publishes,
// so a test can assert the record carries the builder's route rather than an
// empty field.
const cacheObservationRoute = "anthropic/3f2a1b4c5d6e"

// newCacheObservationFixture starts a publisher exactly as the member builder
// does — bind, then start, then emit — so the tests exercise the production
// order rather than a convenient one. The fixture is returned by pointer because
// the recording sink appends to it from the emitting goroutine.
func newCacheObservationFixture(t *testing.T) *cacheObservationFixture {
	t.Helper()
	base := newUsageOwnerFixture(t)
	key := team.OwnerKey{TeamID: base.teamName, MemberID: base.memberID}
	publisher := newMemberUsagePublisher(base.owners, key, cacheObservationRoute)
	publisher.Bind(cacheObservationBackend{SessionAPI: base.writer.ctrl})
	fixture := &cacheObservationFixture{usageOwnerFixture: base, publisher: publisher}
	fixture.sink = publisher.Sink(event.FuncSink(func(e event.Event) { fixture.forwarded = append(fixture.forwarded, e) }))
	publisher.Start()
	t.Cleanup(publisher.Close)
	return fixture
}

// cacheTestSink is a comparable sink, so the seam test can assert which sink it
// got by identity rather than by observing side effects.
type cacheTestSink struct{ seen *int }

func (s cacheTestSink) Emit(event.Event) { *s.seen++ }

// cacheObservationBackend answers the publisher's sample reads with fixed
// values, so a test asserts the observation channel alone.
type cacheObservationBackend struct {
	control.SessionAPI
}

func (cacheObservationBackend) ContextSnapshot() (int, int) { return 4096, 1000000 }
func (cacheObservationBackend) CompactRatio() float64       { return 0.8 }
func (cacheObservationBackend) LastUsage() *provider.Usage {
	return &provider.Usage{
		PromptTokens: 1000, ContextPromptTokens: 900, CompletionTokens: 40,
		CacheHitTokens: 500, CacheMissTokens: 200, CacheWriteTokens: 120, RequestCount: 1,
	}
}
func (cacheObservationBackend) SessionCache() (int, int) { return 500, 200 }

// observedCacheRequests reads back the records the fixture's writer recorded.
func (f cacheObservationFixture) observedCacheRequests(t *testing.T) []team.MemberCacheRequest {
	t.Helper()
	key := team.OwnerKey{TeamID: f.teamName, MemberID: f.memberID}
	var got []team.MemberCacheRequest
	waitForCondition(t, func() bool {
		recs, err := f.owners.ReadCacheRequests(context.Background(), key)
		if err != nil {
			t.Fatalf("read request log: %v", err)
		}
		got = recs
		return len(recs) > 0
	})
	return got
}

// fullCacheUsageEvent is one provider usage event with every mapped dimension
// set, so the mapping is asserted field by field rather than in summary.
func fullCacheUsageEvent() event.Event {
	return event.Event{
		Kind: event.Usage, ModelRef: "deepseek/deepseek-v4-flash",
		UsageSource: event.UsageSourceExecutor,
		TurnID:      "turn-7", Sequence: 42, SessionID: "session-3",
		Usage: &provider.Usage{
			PromptTokens: 1000, ContextPromptTokens: 900, CompletionTokens: 40,
			CacheHitTokens: 700, CacheMissTokens: 300, CacheWriteTokens: 120,
			RequestCount: 1, RequestCountObserved: true, FinishReason: "stop",
		},
		CacheDiagnostics: &event.CacheDiagnostics{
			PrefixHash: "aabbccdd", StablePrefixHash: "11223344",
			PrefixChanged: true, StablePrefixChanged: true,
			PrefixChangeReasons: []string{"tools"}, ToolSchemaTokens: 4200,
			SessionContext: &event.SessionContextDiagnostics{Version: 3, Digest: "deadbeef", Reasons: []string{"workspace"}},
		},
	}
}

// TestObservedRequestMapsEveryUsageField is the mapping guard: one usage event
// becomes one stored record carrying the cache split, the request-shape fields
// and the prefix diagnosis, associated with its team, member and window.
func TestObservedRequestMapsEveryUsageField(t *testing.T) {
	f := newCacheObservationFixture(t)
	f.sink.Emit(fullCacheUsageEvent())
	got := f.observedCacheRequests(t)[0]

	if got.TeamID != f.teamName || got.MemberID != f.memberID {
		t.Fatalf("scope = %s/%s, want %s/%s", got.TeamID, got.MemberID, f.teamName, f.memberID)
	}
	if got.Provider != "deepseek" || got.ModelRef != "deepseek/deepseek-v4-flash" {
		t.Fatalf("route = (%q, %q)", got.Provider, got.ModelRef)
	}
	if got.RouteBucket != cacheObservationRoute {
		t.Fatalf("route bucket = %q, want the builder's own route label %q", got.RouteBucket, cacheObservationRoute)
	}
	if got.RequestID != "turn:turn-7:42" || got.RequestIDSource != "turn_event" {
		t.Fatalf("request identity = (%q, %q), want the turn sequence", got.RequestID, got.RequestIDSource)
	}
	// The session identity is the only field that groups requests into a session,
	// and it travels straight from the event: a record whose session was not
	// observed carries an empty value rather than a synthesized one.
	if got.SessionID != "session-3" || got.TurnID != "turn-7" || got.SessionSequence != 42 {
		t.Fatalf("turn identity = (%q, %q, %d), want the event's own session, turn and sequence",
			got.SessionID, got.TurnID, got.SessionSequence)
	}
	if got.PromptTokens != 1000 || got.ContextPromptTokens != 900 || got.CacheHitTokens != 700 ||
		got.CacheMissTokens != 300 || got.CacheWriteTokens != 120 || got.CompletionTokens != 40 || got.RequestCount != 1 {
		t.Fatalf("token fields = %+v, want the event's own numbers", got)
	}
	if got.RequestCountSource != team.RequestCountObserved {
		t.Fatalf("request count source = %q, want the measured provenance carried through", got.RequestCountSource)
	}
	if got.SessionRequestSeq != 1 || got.HasPrevRequest {
		t.Fatalf("first observed request must be sequence 1 with no predecessor, got %+v", got)
	}
	if !got.DiagnosticsAvailable || got.StablePrefixHash != "11223344" || got.ToolSchemaTokensEstimate != 4200 ||
		strings.Join(got.PrefixChangeReasons, ",") != "tools" || got.SessionContextDigest != "deadbeef" {
		t.Fatalf("diagnostics = %+v, want the event's own prefix shape", got)
	}
	if got.ContextUsed != 4096 || got.ContextWindow != 1000000 {
		t.Fatalf("context gauge = (%d,%d), want the writer's own reads", got.ContextUsed, got.ContextWindow)
	}
	if !got.AccountingValid || len(got.AccountingIssues) != 0 {
		t.Fatalf("accounting = (%v, %v), want valid", got.AccountingValid, got.AccountingIssues)
	}
	if got.SchemaVersion != team.SchemaVersion {
		t.Fatalf("schema version = %d, want %d", got.SchemaVersion, team.SchemaVersion)
	}
}

// TestObservedRequestsCarryNoContent is the privacy guard: prompt text,
// tool arguments and file paths never reach the record, because the record has
// no field for them and the mapping reads none of the content-bearing events.
func TestObservedRequestsCarryNoContent(t *testing.T) {
	f := newCacheObservationFixture(t)
	secret := "sk-do-not-persist-this-token"
	f.sink.Emit(event.Event{Kind: event.Text, Text: "the customer's prompt " + secret})
	f.sink.Emit(event.Event{Kind: event.ToolResult, Tool: event.Tool{ID: "t1", Name: "read_file", Output: secret}})
	f.sink.Emit(fullCacheUsageEvent())
	got := f.observedCacheRequests(t)
	if len(got) != 1 {
		t.Fatalf("stored %d records, want only the usage event to be a request", len(got))
	}
	encoded, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "customer's prompt") {
		t.Fatalf("a stored record carries content: %s", encoded)
	}
	// The log is the durable artifact, so assert it too rather than the struct.
	raw, err := os.ReadFile(filepath.Join(f.ownerDir, ".cache_requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("the request log carries content: %s", raw)
	}
}

// TestOneUsageEventWritesOneRequest pins the statistical unit: a turn is not a
// request, so each usage event contributes exactly one record and the session
// totals are never re-derived from the log.
func TestOneUsageEventWritesOneRequest(t *testing.T) {
	f := newCacheObservationFixture(t)
	for i := range 3 {
		e := fullCacheUsageEvent()
		e.Sequence = uint64(i + 1)
		e.Usage.RequestCount = 1
		f.sink.Emit(e)
	}
	waitForCondition(t, func() bool {
		recs, _ := f.owners.ReadCacheRequests(context.Background(), team.OwnerKey{TeamID: f.teamName, MemberID: f.memberID})
		return len(recs) == 3
	})
	recs, err := f.owners.ReadCacheRequests(context.Background(), team.OwnerKey{TeamID: f.teamName, MemberID: f.memberID})
	if err != nil {
		t.Fatal(err)
	}
	for i, rec := range recs {
		if rec.SessionRequestSeq != i+1 {
			t.Fatalf("record %d seq = %d, want %d", i, rec.SessionRequestSeq, i+1)
		}
		if i > 0 && (!rec.HasPrevRequest || rec.SecondsSincePrevRequest < 0) {
			t.Fatalf("record %d must carry a measured interval, got %+v", i, rec)
		}
	}
	if len(f.forwarded) != 3 {
		t.Fatalf("the wrapped sink forwarded %d events, want every one", len(f.forwarded))
	}
	if f.forwarded[0].Usage == nil || f.forwarded[0].CacheDiagnostics == nil {
		t.Fatal("the wrapper must forward the event unchanged")
	}
}

// TestMultiAttemptUsageIsStoredAsAnAggregate pins that a recovered multi-attempt
// usage is recorded with its true request count instead of being passed off as
// one request. The report excludes it; the record must say so.
func TestMultiAttemptUsageIsStoredAsAnAggregate(t *testing.T) {
	f := newCacheObservationFixture(t)
	e := fullCacheUsageEvent()
	e.Usage.RequestCount = 3
	e.Usage.PromptTokens = 3000
	e.Usage.CacheMissTokens = 900
	f.sink.Emit(e)
	got := f.observedCacheRequests(t)[0]
	if got.RequestCount != 3 || got.ContextPromptTokens != 900 {
		t.Fatalf("aggregate record = %+v, want request_count 3 and the settled attempt's prompt", got)
	}
	report := team.BuildCacheReport(team.CacheReportInput{Requests: got2slice(got), GeneratedAt: time.Now()})
	if report.Exclusions.AggregateRequests != 1 || report.Overall.Totals.Requests != 0 {
		t.Fatalf("report = %+v, want the aggregate excluded from the baseline", report.Exclusions)
	}
}

func got2slice(rec team.MemberCacheRequest) []team.MemberCacheRequest {
	return []team.MemberCacheRequest{rec}
}

// TestUnmeasuredRequestCountIsStoredAsDefaulted pins the provenance rule at the
// writer: a usage event that carried no measured count is recorded with the
// compatibility default of one request AND with the fact that nobody measured
// it, so a reader cannot mistake the default for an observation.
func TestUnmeasuredRequestCountIsStoredAsDefaulted(t *testing.T) {
	f := newCacheObservationFixture(t)
	e := fullCacheUsageEvent()
	e.Usage.RequestCount = 0
	e.Usage.RequestCountObserved = false
	f.sink.Emit(e)
	got := f.observedCacheRequests(t)[0]
	if got.RequestCount != 1 || got.RequestCountSource != team.RequestCountDefaulted {
		t.Fatalf("record = (count %d, source %q), want (1, %q)", got.RequestCount, got.RequestCountSource, team.RequestCountDefaulted)
	}
	report := team.BuildCacheReport(team.CacheReportInput{Requests: got2slice(got), GeneratedAt: time.Now()})
	if report.Overall.Totals.Requests != 0 || report.Exclusions.UnverifiedRequestCount != 1 {
		t.Fatalf("report = %+v, want a defaulted count excluded from the per-request baseline", report.Exclusions)
	}
	if report.Exclusions.UnverifiedHitTokens != got.CacheHitTokens || report.Exclusions.UnverifiedMissTokens != got.CacheMissTokens {
		t.Fatalf("booked tokens = %+v, want the sample's own tokens", report.Exclusions)
	}
}

// TestObservationSinkForALeaderIsTheGivenSink pins the isolation rule at the
// seam the builder uses: a leader's sink carries no observation wrapper, so the
// leader's requests cannot enter the member dataset.
func TestObservationSinkForALeaderIsTheGivenSink(t *testing.T) {
	f := newCacheObservationFixture(t)
	seen := 0
	inner := event.Sink(cacheTestSink{seen: &seen})
	if got := memberObservationSink(f.publisher, inner, true); got != inner {
		t.Fatalf("a leader's sink must be the given sink, got %T", got)
	}
	if got := memberObservationSink(f.publisher, inner, false); got == inner {
		t.Fatal("a member's sink must be wrapped for observation")
	}
	if got := memberObservationSink(nil, inner, false); got == nil {
		t.Fatal("a host without team data must still get a usable sink")
	}
}

// TestFollowerRecordsNothing pins the writer-only rule: a publisher that was
// never started — the read-only follower's shape — wraps the sink, forwards
// every event, and stores no request.
func TestFollowerRecordsNothing(t *testing.T) {
	base := newUsageOwnerFixture(t)
	key := team.OwnerKey{TeamID: base.teamName, MemberID: base.memberID}
	publisher := newMemberUsagePublisher(base.owners, key, "")
	forwarded := 0
	sink := publisher.Sink(event.FuncSink(func(event.Event) { forwarded++ }))
	sink.Emit(fullCacheUsageEvent())
	if forwarded != 1 {
		t.Fatalf("a follower's sink forwarded %d events, want 1", forwarded)
	}
	recs, err := base.owners.ReadCacheRequests(context.Background(), key)
	if err != nil || len(recs) != 0 {
		t.Fatalf("a follower stored %d records, want none", len(recs))
	}
}

// TestRequestLogFailureDoesNotReachTheMember pins fault isolation: a writer
// whose owner store cannot be written still forwards every event and reports the
// fault once, never surfacing it as member state.
func TestRequestLogFailureDoesNotReachTheMember(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	owners, err := team.NewOwnerStore(blocker)
	if err != nil {
		t.Fatal(err)
	}
	publisher := newMemberUsagePublisher(owners, team.OwnerKey{TeamID: "alpha", MemberID: "m1"}, "")
	forwarded := 0
	sink := publisher.Sink(event.FuncSink(func(event.Event) { forwarded++ }))
	publisher.Start()
	t.Cleanup(publisher.Close)

	sink.Emit(fullCacheUsageEvent())
	sink.Emit(fullCacheUsageEvent())
	if forwarded != 2 {
		t.Fatalf("forwarded %d events, want both", forwarded)
	}
	waitForCondition(t, func() bool {
		publisher.mu.Lock()
		defer publisher.mu.Unlock()
		return strings.Contains(publisher.lastFailure, "request log")
	})
}

// TestLegacyUsageDocumentStillReads pins wire compatibility: a document written
// before the additive request-shape and diagnosis fields existed still decodes,
// and its absent diagnosis stays distinguishable from "the prefix did not
// change" — the fields a newer reader adds are simply empty.
func TestLegacyUsageDocumentStillReads(t *testing.T) {
	base := newUsageOwnerFixture(t)
	legacy := `{"schema_version":1,"published_at":"2026-09-23T10:00:00Z",` +
		`"context_used":12000,"context_window":128000,` +
		`"last_turn":{"prompt_tokens":1000,"cache_hit_tokens":700,"cache_miss_tokens":300},` +
		`"session_cache_hit":700,"session_cache_miss":300}`
	if err := os.WriteFile(filepath.Join(base.ownerDir, ".usage.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	key := team.OwnerKey{TeamID: base.teamName, MemberID: base.memberID}
	doc, ok, err := base.owners.ReadUsage(context.Background(), key)
	if err != nil || !ok {
		t.Fatalf("ReadUsage = (%+v, %v, %v), want a legacy document to read", doc, ok, err)
	}
	if doc.LastTurn == nil || doc.LastTurn.CacheHitTokens != 700 {
		t.Fatalf("last turn = %+v, want the legacy numbers", doc.LastTurn)
	}
	if doc.LastTurn.CacheDiagnostics != nil || doc.LastTurn.RequestCount != 0 || doc.LastTurn.RequestCountSource != "" {
		t.Fatalf("a legacy document must carry no diagnosis and no count provenance, got %+v", doc.LastTurn)
	}
	usage := providerUsageFromLastTurn(doc.LastTurn)
	if usage == nil || usage.CacheHitTokens != 700 || usage.RequestCount != 0 {
		t.Fatalf("legacy mapping = %+v, want the mirrored fields only", usage)
	}
}

// TestLastTurnSnapshotCarriesTheLatestDiagnosis pins the additive snapshot: the
// published document carries the latest observed prefix diagnosis and the
// request-shape fields, and a document with no diagnosis stays distinguishable
// from one whose prefix did not change.
func TestLastTurnSnapshotCarriesTheLatestDiagnosis(t *testing.T) {
	f := newCacheObservationFixture(t)
	f.sink.Emit(fullCacheUsageEvent())
	key := team.OwnerKey{TeamID: f.teamName, MemberID: f.memberID}
	var doc team.OwnerUsage
	waitForCondition(t, func() bool {
		read, ok, err := f.owners.ReadUsage(context.Background(), key)
		if err != nil || !ok || read.LastTurn == nil {
			return false
		}
		doc = read
		return read.LastTurn.CacheDiagnostics != nil
	})
	if doc.LastTurn.CacheDiagnostics == nil || !doc.LastTurn.CacheDiagnostics.Available {
		t.Fatalf("last turn = %+v, want the latest diagnosis", doc.LastTurn)
	}
	if got := doc.LastTurn.CacheDiagnostics.PrefixChangeReasons; strings.Join(got, ",") != "tools" {
		t.Fatalf("diagnosis reasons = %v, want the event's own", got)
	}
	if doc.LastTurn.RequestCount != 1 || doc.LastTurn.ContextPromptTokens != 900 || doc.LastTurn.CacheWriteTokens != 120 {
		t.Fatalf("last turn = %+v, want the additive request-shape fields mapped through", doc.LastTurn)
	}
}

// cacheMaintenanceEvent is one decision as the agent emits it, carrying only the
// diagnosis fields this projection reads.
func cacheMaintenanceEvent(state string, headroom int, reduction float64) event.Event {
	return event.Event{Kind: event.ContextMaintenanceEvent, Maintenance: &event.ContextMaintenance{
		Status: "applied", Action: "summary", MaintenanceState: state,
		HeadroomTokens: headroom, ReductionRatio: reduction,
	}}
}

// A request recorded after a decision carries that decision, so the diagnosis
// can be verified per sample rather than only as a session total.
func TestObservedRequestCarriesThePrecedingMaintenanceDecision(t *testing.T) {
	f := newCacheObservationFixture(t)
	f.sink.Emit(cacheMaintenanceEvent("low_yield", 2801, 0.42))
	f.sink.Emit(fullCacheUsageEvent())
	got := f.observedCacheRequests(t)[0]

	if !got.MaintenanceObserved {
		t.Fatalf("record = %+v, want the preceding decision observed", got)
	}
	if got.MaintenanceState != "low_yield" || got.HeadroomTokens != 2801 || got.ReductionRatio != 0.42 {
		t.Fatalf("maintenance = (%q,%d,%v), want the emitted decision", got.MaintenanceState, got.HeadroomTokens, got.ReductionRatio)
	}
}

// The absence of a decision must survive as unobserved. The distinction is the
// whole point of the gate: a fold can buy no headroom, so a zero headroom is a
// real measurement and cannot double as "nothing happened".
func TestObservedRequestWithoutADecisionLeavesTheDiagnosisUnobserved(t *testing.T) {
	f := newCacheObservationFixture(t)
	f.sink.Emit(fullCacheUsageEvent())
	got := f.observedCacheRequests(t)[0]

	if got.MaintenanceObserved || got.MaintenanceState != "" {
		t.Fatalf("record = %+v, want an unobserved diagnosis", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"maintenance_state", `"headroom_tokens"`, `"reduction_ratio"`, `"maintenance_observed"`} {
		if strings.Contains(string(encoded), key) {
			t.Fatalf("an unobserved diagnosis wrote %s: %s", key, encoded)
		}
	}
}

// A recorded zero must be readable as a measurement, not as a gap: a fold that
// exactly met its boundary leaves no headroom, and that is a fact.
func TestObservedRequestKeepsARecordedZero(t *testing.T) {
	f := newCacheObservationFixture(t)
	f.sink.Emit(cacheMaintenanceEvent("recovered", 0, 0))
	f.sink.Emit(fullCacheUsageEvent())
	got := f.observedCacheRequests(t)[0]

	if !got.MaintenanceObserved || got.MaintenanceState != "recovered" {
		t.Fatalf("record = %+v, want the observed decision", got)
	}
	if got.HeadroomTokens != 0 || got.ReductionRatio != 0 {
		t.Fatalf("headroom/reduction = %d/%v, want the recorded zeros", got.HeadroomTokens, got.ReductionRatio)
	}
}

// The decision is stamped on the requests that follow it, and an event carrying
// no state is not a decision that undid the last one.
func TestMaintenanceDecisionStampsFollowingRequestsOnly(t *testing.T) {
	f := newCacheObservationFixture(t)
	first := fullCacheUsageEvent()
	f.sink.Emit(first)
	f.sink.Emit(cacheMaintenanceEvent("at_ceiling", 12, 0.9))
	f.sink.Emit(event.Event{Kind: event.ContextMaintenanceEvent, Maintenance: &event.ContextMaintenance{Status: "noop"}})
	second := fullCacheUsageEvent()
	second.Sequence = 43
	f.sink.Emit(second)

	waitForCondition(t, func() bool {
		recs, _ := f.owners.ReadCacheRequests(context.Background(), team.OwnerKey{TeamID: f.teamName, MemberID: f.memberID})
		return len(recs) == 2
	})
	recs, err := f.owners.ReadCacheRequests(context.Background(), team.OwnerKey{TeamID: f.teamName, MemberID: f.memberID})
	if err != nil {
		t.Fatal(err)
	}
	if recs[0].MaintenanceObserved {
		t.Fatalf("the request before the decision carries it: %+v", recs[0])
	}
	if !recs[1].MaintenanceObserved || recs[1].MaintenanceState != "at_ceiling" {
		t.Fatalf("the request after the decision = %+v, want it observed", recs[1])
	}
}

// The frame the frontend sees must not change shape because the record gained a
// field: the maintenance event is still forwarded, diagnosis included.
func TestMaintenanceEventStillReachesTheFrontend(t *testing.T) {
	f := newCacheObservationFixture(t)
	f.sink.Emit(cacheMaintenanceEvent("blocked", 7, 0.1))
	if len(f.forwarded) != 1 {
		t.Fatalf("forwarded %d events, want the maintenance event forwarded too", len(f.forwarded))
	}
	m := f.forwarded[0].Maintenance
	if m == nil || m.MaintenanceState != "blocked" || m.HeadroomTokens != 7 || m.ReductionRatio != 0.1 {
		t.Fatalf("forwarded maintenance = %+v, want the decision unchanged", m)
	}
}
