package control

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// certifiedRescue builds the error a model turn returns when the agent-side
// rescue certifies a plan. The plan's own production is Part A's tested half;
// this is the exact carrier it hands across.
func certifiedRescue(summary, dedupKey string) error {
	message := provider.Message{
		Role: provider.RoleUser, Origin: provider.MessageOriginHost,
		Content: "<context-rescue>\ntrigger=pressure\n\n" + summary + "\n</context-rescue>",
	}
	return &agent.ContextRescueRequired{Plan: agent.ContextRecoveryPlan{
		Trigger:         "pressure",
		Reason:          "reduction below the rescue threshold",
		SourceTokens:    200_000,
		CompactedTokens: 195_000,
		ReductionRatio:  0.025,
		Summary:         summary,
		SummaryHash:     "agent-side-fingerprint",
		SummaryTokens:   continuationTextTokens(summary),
		BlockTokens:     continuationMessageTokenEstimate(message),
		Message:         message,
		Generation:      7,
		DedupKey:        dedupKey,
		PendingTools:    1,
	}}
}

// awaitCondition polls until cond holds, so a test never races the turn's own
// goroutine for a state the controller publishes asynchronously.
func awaitCondition(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestContextRescuePlanAdapterCarriesTheCertifiedFields(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")

	if !f.ctrl.noteContextRescue(certifiedRescue("goal: finish the port", "dedup-1")) {
		t.Fatal("a certified rescue error must be captured")
	}
	if !f.ctrl.contextRescuePending() {
		t.Fatal("the captured rescue is not queued")
	}
	plan, ok := f.ctrl.takeContextRescue()
	if !ok {
		t.Fatal("the queued rescue was not claimable")
	}
	if plan.Trigger != "pressure" || plan.SourceTokens != 200_000 || plan.CompactedTokens != 195_000 {
		t.Fatalf("measured fields were not carried: %+v", plan)
	}
	if plan.ReductionRatio != 0.025 {
		t.Fatalf("reduction ratio was reinterpreted: %v", plan.ReductionRatio)
	}
	// The contract owns its own fingerprint, so the marker records one the
	// controller can verify rather than the agent's opaque tag.
	if plan.SummaryHash != continuationDigest("goal: finish the port") {
		t.Fatalf("summary hash = %q", plan.SummaryHash)
	}
	if plan.SummaryTokens != continuationMessageTokenEstimate(plan.Message) {
		t.Fatalf("declared tokens %d do not describe the injected message", plan.SummaryTokens)
	}
	if !strings.Contains(plan.Message.Content, "goal: finish the port") {
		t.Fatal("the certified message was not carried verbatim")
	}
	// A first rescue in a fresh chain is attempt 1 of a chain keyed to the
	// session it started from.
	if plan.Attempt != 1 || !strings.HasPrefix(plan.Lineage, "rescue-chain:") {
		t.Fatalf("lineage = %q attempt = %d", plan.Lineage, plan.Attempt)
	}
	if plan.Resume == nil {
		t.Fatal("the queued rescue has no resume to enqueue the continuation turn")
	}
}

func TestContextRescuePlanAdapterIgnoresOrdinaryErrors(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	for _, err := range []error{nil, errors.New("provider exploded"), agent.ErrContextRescuePlanned} {
		if f.ctrl.noteContextRescue(err) {
			t.Fatalf("error %v was captured as a rescue", err)
		}
	}
	if f.ctrl.contextRescuePending() {
		t.Fatal("an ordinary error queued a rescue")
	}
}

func TestContextRescueChainInheritsItsLineageAndAttempt(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	f.rotation.reserve = func(SessionRotationRequest) string { return "member-continuation" }

	// First rescue: a new chain.
	if !f.ctrl.noteContextRescue(certifiedRescue("goal: first", "dedup-1")) {
		t.Fatal("first rescue was not captured")
	}
	f.ctrl.applyPendingContextRescue()
	awaitCondition(t, "the first continuation", func() bool {
		ref, ok := f.ctrl.SessionRef()
		return ok && ref.SessionID == "member-continuation"
	})
	first, ok := f.ctrl.takeContextRescue()
	if ok {
		t.Fatalf("a completed rescue stayed queued: %+v", first)
	}

	// The continuation session carries the chain identity, so the next rescue
	// counts against the same ceiling instead of starting a new chain.
	second := certifiedRescue("goal: second", "dedup-2")
	if !f.ctrl.noteContextRescue(second) {
		t.Fatal("second rescue was not captured")
	}
	plan, ok := f.ctrl.takeContextRescue()
	if !ok {
		t.Fatal("the second rescue was not claimable")
	}
	if plan.Attempt != 2 {
		t.Fatalf("second rescue attempt = %d, want 2", plan.Attempt)
	}
	if got := f.ctrl.continuations.committed(plan.Lineage); got != 1 {
		t.Fatalf("lineage recorded %d committed rescues, want 1", got)
	}
	// Leave the claimed plan unapplied: on this controller it is the second
	// rescue's lineage that is under test, not a second rotation.
	f.ctrl.requeueContextRescue(plan)
}

func TestContextRescueRunsAtTheTurnBoundaryAndPreservesTheSource(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	rotations := 0
	f.rotation.reserve = func(SessionRotationRequest) string {
		rotations++
		return "member-continuation"
	}
	// The real terminal boundary must capture and apply without any test-side
	// help: this submits a turn whose body returns the certified error.
	result := f.ctrl.runGuarded(func(context.Context) error {
		return certifiedRescue("goal: finish the port", "dedup-1")
	})
	if result != turnStarted {
		t.Fatalf("admission = %v, want a started turn", result)
	}
	awaitCondition(t, "the continuation session", func() bool {
		ref, ok := f.ctrl.SessionRef()
		return ok && ref.SessionID == "member-continuation"
	})
	awaitCondition(t, "the rescue to settle", func() bool { return !f.ctrl.contextRescuePending() })
	if rotations != 1 {
		t.Fatalf("host reserved %d identities, want 1", rotations)
	}
	// The main line actually restarted: the resumed turn reached the model
	// runner on the continuation session, carrying the host framing.
	awaitCondition(t, "the resumed turn", func() bool { return len(f.runner.seen()) > 0 })
	if got := f.runner.seen(); len(got) != 1 || !strings.Contains(got[0], "recovery briefing") {
		t.Fatalf("resumed turn input = %q", got)
	}

	// The source transcript survives, with the rescue recorded on it.
	source, err := f.service.Open(t.Context(), f.source)
	if err != nil {
		t.Fatalf("source session is gone: %v", err)
	}
	defer func() { _ = source.Release(context.Background()) }()
	if !hasMessageContent(source.Runtime().Session().ExecutionSnapshot().Projection.ModelMessages, "main line work") {
		t.Fatal("the source transcript lost its content")
	}
	assertSourceMarkerRecorded(t, f.service, f.source, "rescue-chain:"+f.source.SessionID)

	// The continuation opens with the certified briefing, injected verbatim.
	ref, _ := f.ctrl.SessionRef()
	continuation, err := f.service.Open(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = continuation.Release(context.Background()) }()
	projection := continuation.Runtime().Session().Snapshot().Projection
	if len(projection.Messages) < 2 {
		t.Fatalf("continuation transcript = %d messages, want the briefing", len(projection.Messages))
	}
	opening := projection.Messages[1]
	if !strings.Contains(opening.Content, "<context-rescue>") || !strings.Contains(opening.Content, "goal: finish the port") {
		t.Fatalf("continuation did not open with the certified briefing: %q", opening.Content)
	}
	if strings.Count(opening.Content, "goal: finish the port") != 1 {
		t.Fatal("the briefing was wrapped again by the controller")
	}
}

func TestContextRescueHoldsAdmissionUntilItSettles(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	f.rotation.reserve = func(SessionRotationRequest) string { return "member-continuation" }

	// A queued rescue means the session is about to be replaced, so new work
	// must not be admitted into the window the rescue is escaping.
	if !f.ctrl.noteContextRescue(certifiedRescue("goal: finish", "dedup-1")) {
		t.Fatal("rescue not captured")
	}
	// Atomic: the turn body runs on the controller's own goroutine.
	var starts atomic.Int64
	count := func(context.Context) error { starts.Add(1); return nil }
	res := f.ctrl.runGuarded(count)
	if res == turnStarted || starts.Load() != 0 {
		t.Fatalf("admission = %v with %d turns started; the rescue must hold new work", res, starts.Load())
	}

	f.ctrl.applyPendingContextRescue()
	awaitCondition(t, "the rescue to settle", func() bool { return !f.ctrl.contextRescuePending() })
	awaitCondition(t, "the controller to go idle", func() bool { return !f.ctrl.RuntimeStatus().Running })
	if res := f.ctrl.runGuarded(count); res != turnStarted {
		t.Fatalf("admission after the rescue = %v", res)
	}
	awaitCondition(t, "the held work to run", func() bool { return starts.Load() > 0 })
}

// publishContinuationAwaitingResume produces exactly the state a crash in the
// rescue window leaves behind: the continuation is published and durable, its
// resume was intended, and no turn ever began in it. A resume that fails to
// land is that window seen from the durable side, which is the only side a
// restarted process can read.
func publishContinuationAwaitingResume(t *testing.T, f *continuationFixture) session.SessionRef {
	t.Helper()
	plan := f.plan(t, "goal: finish the port")
	plan.Resume = func(context.Context, ContinuationResume) error {
		return errors.New("the process died before the resume landed")
	}
	result, err := f.ctrl.ContinueSessionFromPlan(t.Context(), plan)
	if err == nil || result.Resumed {
		t.Fatalf("the fixture must leave the continuation un-resumed: err=%v resumed=%v", err, result.Resumed)
	}
	if !continuationAwaitingResume(t.Context(), mustSession(t, f.service, result.Continuation)) {
		t.Fatal("the fixture did not produce the crash-window state")
	}
	return result.Continuation
}

// TestContinuationRecoveryLeavesACallerDrivenContinuationAlone pins the other
// half of the contract: a nil Resume is a caller saying it will drive the
// session itself, and a restart must not second-guess that.
func TestContinuationRecoveryLeavesACallerDrivenContinuationAlone(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	f.rotation.reserve = func(SessionRotationRequest) string { return "member-continuation" }
	// f.plan carries no Resume at all.
	result, err := f.ctrl.ContinueSessionFromPlan(t.Context(), f.plan(t, "goal: finish the port"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.ctrl.ReleaseSessionRuntimeBinding(); err != nil {
		t.Fatalf("release the crashed owner: %v", err)
	}
	rebuilt := newContinuationControllerOver(t, f.service, result.Continuation)
	rebuilt.ctrl.NotifyInboxRuntimeReady()
	rebuilt.ctrl.RecoverUnstartedContinuation()
	time.Sleep(50 * time.Millisecond)
	if got := rebuilt.runner.seen(); len(got) != 0 {
		t.Fatalf("recovery drove a continuation the caller owns: %q", got)
	}
}

func TestContinuationRecoveryRestartsAnUnresumedMainLine(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	f.rotation.reserve = func(SessionRotationRequest) string { return "member-continuation" }
	continuation := publishContinuationAwaitingResume(t, f)

	// A restarted process: the old owner is gone, the identity and its durable
	// state remain, and the new controller has no in-memory ledger at all.
	if err := f.ctrl.ReleaseSessionRuntimeBinding(); err != nil {
		t.Fatalf("release the crashed owner: %v", err)
	}
	rebuilt := newContinuationControllerOver(t, f.service, continuation)
	if !continuationAwaitingResume(t.Context(), mustSession(t, f.service, continuation)) {
		t.Fatal("the published-and-unresumed continuation is not recognized")
	}
	rebuilt.ctrl.NotifyInboxRuntimeReady()
	awaitCondition(t, "the recovered resume turn", func() bool { return len(rebuilt.runner.seen()) == 1 })
	if got := rebuilt.runner.seen()[0]; !strings.Contains(got, "recovery briefing") {
		t.Fatalf("recovered resume input = %q", got)
	}

	// The kick is level-triggered but once per session: a host that announces
	// readiness twice must not queue a second resume.
	rebuilt.ctrl.NotifyInboxRuntimeReady()
	rebuilt.ctrl.RecoverUnstartedContinuation()
	awaitCondition(t, "the resumed turn to record itself", func() bool {
		return !continuationAwaitingResume(t.Context(), mustSession(t, f.service, continuation))
	})
	if got := rebuilt.runner.seen(); len(got) != 1 {
		t.Fatalf("recovery ran %d resumes, want exactly 1", len(got))
	}
}

func TestContinuationRecoveryLeavesAResumedContinuationAlone(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	f.rotation.reserve = func(SessionRotationRequest) string { return "member-continuation" }
	plan := f.plan(t, "goal: finish the port")
	plan.Resume = f.ctrl.resumeContextContinuation
	result, err := f.ctrl.ContinueSessionFromPlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resumed {
		t.Fatal("the fixture must resume the continuation")
	}
	awaitCondition(t, "the resume to be recorded", func() bool {
		return !continuationAwaitingResume(t.Context(), mustSession(t, f.service, result.Continuation))
	})

	// A later restart must not start a second main line on top of a running one.
	rebuilt := newContinuationControllerOver(t, f.service, result.Continuation)
	rebuilt.ctrl.NotifyInboxRuntimeReady()
	time.Sleep(50 * time.Millisecond)
	if got := rebuilt.runner.seen(); len(got) != 0 {
		t.Fatalf("recovery resumed an already-resumed continuation: %q", got)
	}
}

func TestContinuationRecoveryIgnoresOrdinarySessions(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	f.ctrl.RecoverUnstartedContinuation()
	f.ctrl.NotifyInboxRuntimeReady()
	time.Sleep(50 * time.Millisecond)
	if got := f.runner.seen(); len(got) != 0 {
		t.Fatalf("recovery drove an ordinary session: %q", got)
	}
	if _, ok := f.ctrl.SessionRef(); !ok {
		t.Fatal("recovery disturbed the session binding")
	}
}

func TestContinuationRecoveryRetriesWhenAdmissionRefuses(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	f.rotation.reserve = func(SessionRotationRequest) string { return "member-continuation" }
	continuation := publishContinuationAwaitingResume(t, f)
	if err := f.ctrl.ReleaseSessionRuntimeBinding(); err != nil {
		t.Fatalf("release the crashed owner: %v", err)
	}
	rebuilt := newContinuationControllerOver(t, f.service, continuation)

	// Maintenance refuses admission without starting a turn, so the continuation
	// stays un-resumed and the predicate keeps holding.
	rebuilt.ctrl.mu.Lock()
	rebuilt.ctrl.maintenance = &controllerMaintenance{}
	rebuilt.ctrl.mu.Unlock()
	rebuilt.ctrl.RecoverUnstartedContinuation()
	if got := rebuilt.runner.seen(); len(got) != 0 {
		t.Fatalf("a refused recovery still resumed: %q", got)
	}
	if !continuationAwaitingResume(t.Context(), mustSession(t, f.service, continuation)) {
		t.Fatal("a refused recovery consumed the continuation instead of retrying it")
	}

	// The refusal released the claim, so a later kick takes it.
	rebuilt.ctrl.mu.Lock()
	rebuilt.ctrl.maintenance = nil
	rebuilt.ctrl.mu.Unlock()
	rebuilt.ctrl.RecoverUnstartedContinuation()
	awaitCondition(t, "the retried resume", func() bool { return len(rebuilt.runner.seen()) == 1 })
	if !strings.Contains(rebuilt.runner.seen()[0], "recovery briefing") {
		t.Fatalf("retried resume input = %q", rebuilt.runner.seen()[0])
	}
}

func TestContinuationRecoveryDefersToARotationInFlight(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	continuation := publishContinuationAwaitingResume(t, f)
	if err := f.ctrl.ReleaseSessionRuntimeBinding(); err != nil {
		t.Fatalf("release the crashed owner: %v", err)
	}
	rebuilt := newContinuationControllerOver(t, f.service, continuation)

	// A rotation owns the resume it is publishing; the kick must not claim the
	// session out from under it and consume the one recovery attempt.
	rebuilt.ctrl.mu.Lock()
	rebuilt.ctrl.rotating = true
	rebuilt.ctrl.mu.Unlock()
	rebuilt.ctrl.RecoverUnstartedContinuation()
	if len(rebuilt.runner.seen()) != 0 {
		t.Fatal("the kick drove a session a rotation was working on")
	}

	rebuilt.ctrl.mu.Lock()
	rebuilt.ctrl.rotating = false
	rebuilt.ctrl.mu.Unlock()
	rebuilt.ctrl.RecoverUnstartedContinuation()
	awaitCondition(t, "the deferred resume", func() bool { return len(rebuilt.runner.seen()) == 1 })
}

func mustSession(t *testing.T, service *session.Service, ref session.SessionRef) *session.Session {
	t.Helper()
	binding, err := service.Open(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = binding.Release(context.Background()) })
	return binding.Runtime().Session()
}

func TestContextRescueIsInertWithoutACertifiedPlan(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	f.rotation.reserve = func(SessionRotationRequest) string { return "never-reserved" }

	// An ordinary failing turn must not rotate anything or leave a rescue queued.
	if res := f.ctrl.runGuarded(func(context.Context) error {
		return errors.New("provider exploded")
	}); res != turnStarted {
		t.Fatalf("admission = %v", res)
	}
	awaitCondition(t, "the failing turn to settle", func() bool { return !f.ctrl.contextRescuePending() })
	if ref, _ := f.ctrl.SessionRef(); ref != f.source {
		t.Fatal("an ordinary failure rotated the session")
	}
	if len(f.rotation.requests) != 0 {
		t.Fatal("an ordinary failure asked the host to reserve an identity")
	}
}
