package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// Refusal and failure-injection coverage for the context-rescue rotation. The
// happy path and its fixture live in session_continuation_test.go; these tests
// are the ones that assert a refused rescue never moves the session.

func TestContinuationRotationRefusesInvalidAndStalePlans(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	f.rotation.reserve = func(SessionRotationRequest) string { return "member-continuation" }
	summary := "goal: finish"

	cases := []struct {
		name   string
		mutate func(*ContinuationPlan)
		want   error
	}{
		{"empty summary", func(p *ContinuationPlan) { p.Summary, p.SummaryHash = "", continuationDigest("") }, ErrContinuationPlanInvalid},
		{"mismatched hash", func(p *ContinuationPlan) { p.SummaryHash = continuationDigest("other") }, ErrContinuationPlanInvalid},
		{"missing dedup key", func(p *ContinuationPlan) { p.DedupKey = "" }, ErrContinuationPlanInvalid},
		{"missing lineage", func(p *ContinuationPlan) { p.Lineage = "" }, ErrContinuationPlanInvalid},
		{"zero attempt", func(p *ContinuationPlan) { p.Attempt = 0 }, ErrContinuationPlanInvalid},
		{"ratio out of range", func(p *ContinuationPlan) { p.ReductionRatio = 1.5 }, ErrContinuationPlanInvalid},
		{"attempt past the ceiling", func(p *ContinuationPlan) { p.Attempt = MaxAutomaticContinuations + 1 }, ErrContinuationCeiling},
		{"no source generation", func(p *ContinuationPlan) { p.Generation = 0 }, ErrContinuationStale},
		{"stale source generation", func(p *ContinuationPlan) { p.Generation++ }, ErrContinuationStale},
		{"impossible declared summary", func(p *ContinuationPlan) {
			p.SummaryTokens = ContinuationMessageBudget + 1
		}, ErrContinuationBudget},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := f.plan(t, summary)
			tc.mutate(&plan)
			if _, err := f.ctrl.ContinueSessionFromPlan(t.Context(), plan); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if ref, _ := f.ctrl.SessionRef(); ref != f.source {
				t.Fatal("a refused plan moved the session")
			}
		})
	}
	if len(f.rotation.requests) != 0 {
		t.Fatalf("host was asked to reserve %d identities for refused plans", len(f.rotation.requests))
	}
}

func TestContinuationRotationRefusesOverBudgetMessage(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	f.rotation.reserve = func(SessionRotationRequest) string { return "member-continuation" }

	// A plan that declares a small summary but carries a large one. The
	// summariser's own output budget does not bound the injected message, so the
	// controller re-measures and refuses rather than trusting the declaration.
	plan := f.plan(t, "goal: finish")
	plan.Summary = strings.Repeat("recovered detail ", ContinuationMessageBudget/4)
	plan.SummaryHash = continuationDigest(plan.Summary)
	plan.SummaryTokens = 10

	_, err := f.ctrl.ContinueSessionFromPlan(t.Context(), plan)
	if !errors.Is(err, ErrContinuationBudget) {
		t.Fatalf("error = %v, want ErrContinuationBudget", err)
	}
	if ref, _ := f.ctrl.SessionRef(); ref != f.source {
		t.Fatal("an over-budget rescue moved the session")
	}
	if len(f.rotation.requests) != 0 {
		t.Fatal("host was asked to reserve an identity for an over-budget message")
	}
}

func TestContinuationRotationAcceptsAMessageJustUnderTheCeiling(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	f.rotation.reserve = func(SessionRotationRequest) string { return "member-continuation" }

	// A summary that fills most of the summary budget: the wrapper has to fit
	// in the remainder, which is the boundary the two budgets exist to hold.
	plan := f.plan(t, strings.Repeat("z", ContinuationSummaryBudget-200))
	if plan.SummaryTokens >= ContinuationSummaryBudget {
		t.Fatalf("fixture summary is %d tokens, over the summary budget", plan.SummaryTokens)
	}
	result, err := f.ctrl.ContinueSessionFromPlan(t.Context(), plan)
	if err != nil {
		t.Fatalf("ContinueSessionFromPlan: %v", err)
	}
	if result.MessageTokens >= ContinuationMessageBudget {
		t.Fatalf("accepted message is %d tokens, at or over the ceiling", result.MessageTokens)
	}
	if result.MessageTokens <= result.SummaryTokens {
		t.Fatal("message estimate does not account for the wrapper")
	}
}

func TestContinuationRotationRefusesAnUnusableHostIdentity(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	// A host that hands back an identity whose session directory already
	// exists: creation fails, and the rescue must leave the member where it was.
	f.rotation.reserve = func(SessionRotationRequest) string { return f.source.SessionID + "-taken" }
	if _, err := f.service.Create(t.Context(), session.CreateOptions{SessionID: f.source.SessionID + "-taken"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ctrl.ContinueSessionFromPlan(t.Context(), f.plan(t, "goal: finish")); err == nil {
		t.Fatal("an unusable host identity must surface")
	}
	if ref, _ := f.ctrl.SessionRef(); ref != f.source {
		t.Fatal("the controller moved to a continuation that was never published")
	}
	if len(f.rotation.requests) != 1 {
		t.Fatalf("host saw %d rotation requests, want the failed one", len(f.rotation.requests))
	}
	// The failed attempt released its reservation, so the rescue is retryable.
	f.rotation.reserve = func(SessionRotationRequest) string { return "member-continuation" }
	result, err := f.ctrl.ContinueSessionFromPlan(t.Context(), f.plan(t, "goal: finish"))
	if err != nil {
		t.Fatalf("retry after a rejected identity: %v", err)
	}
	if result.Continuation.SessionID != "member-continuation" {
		t.Fatalf("continuation = %+v", result.Continuation)
	}
}

// TestContinuationMarkerIsWrittenOncePerSource pins the marker's idempotency
// directly. The end-to-end replay is caught earlier by the duplicate scan, so
// this is the only place the write-once branch itself is exercised.
func TestContinuationMarkerIsWrittenOncePerSource(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	_, runtime, _ := f.ctrl.v3Binding()
	plan := f.plan(t, "goal: finish")

	attempt := &continuationAttempt{
		controller: f.ctrl, plan: plan, source: f.source, store: runtime.Session(),
	}
	continuation := session.SessionRef{HostID: f.source.HostID, SessionID: "member-continuation"}
	if err := f.ctrl.writeContinuationMarker(t.Context(), attempt, continuation); err != nil {
		t.Fatalf("first marker: %v", err)
	}
	before := runtime.Session().EventSequence()
	err := f.ctrl.writeContinuationMarker(t.Context(), attempt, continuation)
	if !errors.Is(err, ErrContinuationDuplicate) {
		t.Fatalf("second marker error = %v, want ErrContinuationDuplicate", err)
	}
	if after := runtime.Session().EventSequence(); after != before {
		t.Fatalf("a replayed marker appended %d events", after-before)
	}
	// The first marker is durable, which is what a restarted process reads.
	page, err := runtime.Session().AcceptedPage(t.Context(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	markers := 0
	for _, commit := range page.Commits {
		for _, ev := range commit.Events {
			if ev.Kind == "diagnostic" && bytes.Contains(ev.Payload, []byte("context-continuation-v1")) {
				markers++
			}
		}
	}
	if markers != 1 {
		t.Fatalf("source records %d continuation markers, want 1", markers)
	}
}

func TestContinuationRotationRefusesWithoutExclusiveSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	sess := agent.NewSession("sys")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "legacy context"})
	if err := sess.SaveIfAbsent(path); err != nil {
		t.Fatal(err)
	}
	exec := agent.New(nil, nil, sess, agent.Options{}, event.Discard)
	legacy := newOwnedTestController(t, Options{
		Executor: exec, Sink: event.Discard, SessionDir: dir, SessionPath: path, SystemPrompt: "sys",
	})
	plan := ContinuationPlan{
		Summary: "goal: finish", SummaryHash: continuationDigest("goal: finish"),
		DedupKey: "rescue-1", Lineage: "lineage", Attempt: 1, Generation: 1,
	}
	if _, err := legacy.ContinueSessionFromPlan(t.Context(), plan); !errors.Is(err, ErrContinuationUnsupported) {
		t.Fatalf("error = %v, want ErrContinuationUnsupported", err)
	}
	// The legacy path has no non-destructive rotation, so a rescue must refuse
	// rather than fall back to the clear that deletes the transcript.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a refused rescue destroyed the legacy transcript: %v", err)
	}
}

func TestContinuationRotationStopsAnAutomaticChainAtTheCeiling(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	next := 0
	f.rotation.reserve = func(SessionRotationRequest) string {
		next++
		return "member-continuation-" + strings.Repeat("x", next)
	}
	for attempt := 1; attempt <= MaxAutomaticContinuations; attempt++ {
		plan := f.plan(t, "goal: finish")
		plan.Attempt = attempt
		plan.DedupKey = "rescue-" + strings.Repeat("d", attempt)
		if _, err := f.ctrl.ContinueSessionFromPlan(t.Context(), plan); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		f.advance(t, "more work")
	}
	// A fresh dedup key on the same lineage must not buy another rotation.
	plan := f.plan(t, "goal: finish")
	plan.Attempt = MaxAutomaticContinuations
	plan.DedupKey = "rescue-final"
	_, err := f.ctrl.ContinueSessionFromPlan(t.Context(), plan)
	if !errors.Is(err, ErrContinuationCeiling) {
		t.Fatalf("error = %v, want ErrContinuationCeiling", err)
	}
	if len(f.rotation.requests) != MaxAutomaticContinuations {
		t.Fatalf("host reserved %d identities, want %d", len(f.rotation.requests), MaxAutomaticContinuations)
	}
}

func TestContinuationRotationRequiresAFreshHostIdentity(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	f.rotation.reserve = func(SessionRotationRequest) string { return f.source.SessionID }
	if _, err := f.ctrl.ContinueSessionFromPlan(t.Context(), f.plan(t, "goal: finish")); !errors.Is(err, ErrContinuationPlanInvalid) {
		t.Fatalf("error = %v, want ErrContinuationPlanInvalid", err)
	}
	if ref, _ := f.ctrl.SessionRef(); ref != f.source {
		t.Fatal("a host identity bug moved the session")
	}
}

func TestContinuationRotationFailureInHostCommitKeepsSourceUsable(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	f.rotation.failCommit = errors.New("workspace registry rejected the reservation")

	if _, err := f.ctrl.ContinueSessionFromPlan(t.Context(), f.plan(t, "goal: finish")); err == nil {
		t.Fatal("a failed host commit must surface")
	}
	if ref, _ := f.ctrl.SessionRef(); ref != f.source {
		t.Fatal("the controller moved to a continuation the host refused")
	}
	// The source is still the bound, writable session: a rescue that failed in
	// the host's commit step must leave the member exactly where it was.
	source, err := f.service.Open(t.Context(), f.source)
	if err != nil {
		t.Fatalf("source session is unusable after a failed commit: %v", err)
	}
	if _, err := source.Runtime().Session().AppendBatch(t.Context(), "after-failure",
		[]session.Event{{Kind: "diagnostic", Optional: true, Payload: json.RawMessage(`{"type":"probe"}`)}}); err != nil {
		t.Fatalf("source session is not writable after a failed commit: %v", err)
	}
	if err := source.Release(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The abandoned attempt released its reservation, and the host reserves a
	// fresh identity because the failed one never became a session it owns.
	f.rotation.failCommit = nil
	result, err := f.ctrl.ContinueSessionFromPlan(t.Context(), f.plan(t, "goal: finish"))
	if err != nil {
		t.Fatalf("retry after a failed host commit: %v", err)
	}
	if result.Continuation.SessionID == "" {
		t.Fatalf("retry did not publish a continuation: %+v", result)
	}
	if len(f.rotation.requests) != 2 {
		t.Fatalf("host saw %d rotation requests, want the failed one and the retry", len(f.rotation.requests))
	}
}

func TestContinuationRotationIsolatesMembers(t *testing.T) {
	first := newContinuationFixture(t)
	second := newContinuationFixture(t)
	first.advance(t, "member one work")
	second.advance(t, "member two work")
	first.rotation.reserve = func(SessionRotationRequest) string { return "member-one-continuation" }
	second.rotation.reserve = func(SessionRotationRequest) string { return "member-two-continuation" }

	if _, err := first.ctrl.ContinueSessionFromPlan(t.Context(), first.plan(t, "goal: one")); err != nil {
		t.Fatal(err)
	}
	// The peer keeps its own identity, its own generation and its own host.
	if ref, _ := second.ctrl.SessionRef(); ref != second.source {
		t.Fatalf("member two was rotated by member one's rescue: %+v", ref)
	}
	if len(second.rotation.requests) != 0 {
		t.Fatal("member two's host was asked to reserve an identity")
	}
	if _, err := second.ctrl.ContinueSessionFromPlan(t.Context(), second.plan(t, "goal: two")); err != nil {
		t.Fatalf("member two's own rescue: %v", err)
	}
	if ref, _ := second.ctrl.SessionRef(); ref.SessionID != "member-two-continuation" {
		t.Fatalf("member two session = %+v", ref)
	}
}

func TestContinuationRotationInjectsAPreRenderedMessageVerbatim(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	f.rotation.reserve = func(SessionRotationRequest) string { return "member-continuation" }

	// The agent-side rescue renders the briefing. The controller must inject it
	// byte-for-byte rather than wrapping Summary a second time, or the model
	// would receive one briefing nested inside another.
	briefing := "<context-continuation>\ntrigger=x summary_sha=abc\ngoal: finish the port\n</context-continuation>"
	plan := f.plan(t, "goal: finish")
	plan.Message = provider.Message{Role: provider.RoleUser, Content: briefing}
	result, err := f.ctrl.ContinueSessionFromPlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	continuation, err := f.service.Open(t.Context(), result.Continuation)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = continuation.Release(context.Background()) }()
	projection := continuation.Runtime().Session().Snapshot().Projection
	if len(projection.Messages) != 2 {
		t.Fatalf("continuation transcript = %d messages, want 2", len(projection.Messages))
	}
	opening := projection.Messages[1]
	if opening.Content != briefing {
		t.Fatalf("injected message was rewritten:\n got %q\nwant %q", opening.Content, briefing)
	}
	if opening.Origin != provider.MessageOriginHost {
		t.Fatalf("injected message origin = %q, want host", opening.Origin)
	}
	if opening.ID == "" || opening.ID != result.MessageID {
		t.Fatalf("injected message id = %q, result reports %q", opening.ID, result.MessageID)
	}
	if count := strings.Count(opening.Content, "goal: finish"); count != 1 {
		t.Fatalf("briefing appears %d times; the controller wrapped it again", count)
	}
}

func TestContinuationRotationWorksWithoutAnIdentityOwner(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	// An embedded controller has no host to reserve an identity, so the service
	// allocates one. The parent link is still the controller's to assert.
	f.ctrl.mu.Lock()
	f.ctrl.onSessionRotation = nil
	f.ctrl.mu.Unlock()

	result, err := f.ctrl.ContinueSessionFromPlan(t.Context(), f.plan(t, "goal: finish"))
	if err != nil {
		t.Fatalf("ContinueSessionFromPlan: %v", err)
	}
	if result.Continuation.SessionID == "" || result.Continuation == f.source {
		t.Fatalf("continuation = %+v", result.Continuation)
	}
	assertParentLink(t, f.service, result.Continuation, f.source.SessionID)
	assertSourceMarkerRecorded(t, f.service, f.source, "lineage-member-source")
}
