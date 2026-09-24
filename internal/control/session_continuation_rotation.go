package control

import (
	"context"
	"encoding/json"
	"fmt"

	"reasonix/internal/event"
	"reasonix/internal/extension"
	"reasonix/internal/extension/dispatch"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// continuationAttempt is one admitted rescue: the frozen source identity, the
// rendered recovery message, and the gate the attempt holds.
type continuationAttempt struct {
	controller    *Controller
	plan          ContinuationPlan
	source        session.SessionRef
	store         *session.Session
	message       provider.Message
	messageTokens int
	committed     bool
	released      bool
}

// release ends the rotation gate and drops an attempt that never committed.
// Releasing the reservation is what lets the same plan retry after a refusal.
func (a *continuationAttempt) release() {
	if a == nil || a.released {
		return
	}
	a.released = true
	a.controller.endRotation()
	if !a.committed {
		a.controller.continuations.abandon(a.plan)
	}
}

// ContinueSessionFromPlan is the dedicated context-rescue rotation, and the
// only supported way to move a rescue onto a fresh session: it never calls
// ClearSession, never deletes the source, and refuses rather than degrading.
//
// The operation runs at a turn admission boundary. It claims the rotation gate
// for the whole snapshot-then-swap, so a running turn is rejected with
// errTurnRunningRotation (IsSessionRotationBusy reports it) and the caller can
// park the rescue until the turn converges. A concurrent rotation is rejected
// with errRotationInProgress.
//
// Ordering is the transaction: flush the source, create and publish the child
// under the host's identity reservation while writing the source's durable
// rescue marker, and only then release the gate and resume. Every failure path
// before publication leaves the source session bound and usable.
func (c *Controller) ContinueSessionFromPlan(ctx context.Context, plan ContinuationPlan) (ContinuationResult, error) {
	attempt, replayed, err := c.admitContinuation(ctx, plan)
	if err != nil {
		// A replayed attempt that failed only in its resume still reports the
		// committed continuation, so the error cannot read as "the rescue never
		// happened" and strand the caller without the identity to retry against.
		if replayed != nil {
			return *replayed, err
		}
		return ContinuationResult{}, err
	}
	if replayed != nil {
		return *replayed, nil
	}
	defer attempt.release()
	result, err := c.commitContinuation(ctx, attempt)
	if err != nil {
		return ContinuationResult{}, err
	}
	return c.resumeContinuation(ctx, attempt, result)
}

// admitContinuation answers the replay and ceiling questions, renders the
// message, and claims the rotation gate. It is the whole fail-closed boundary:
// everything it refuses leaves the session untouched and the attempt
// unreserved. A non-nil replayed result is a completed attempt that never took
// the gate; otherwise the returned attempt owns it until release.
func (c *Controller) admitContinuation(ctx context.Context, plan ContinuationPlan) (*continuationAttempt, *ContinuationResult, error) {
	if c == nil || c.executor == nil {
		return nil, nil, ErrContinuationUnsupported
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := validateContinuationPlan(plan); err != nil {
		c.emitContinuationTelemetry(plan, continuationOutcomeRejected, 0)
		return nil, nil, err
	}
	prior, reservation := c.continuations.begin(plan)
	switch reservation {
	case continuationBusy:
		// Racing the same attempt through beginRotation would only trade a clear
		// refusal for a gate error.
		c.emitContinuationTelemetry(plan, continuationOutcomeRejected, 0)
		return nil, nil, fmt.Errorf("%w: lineage %s attempt %d is already rotating",
			ErrContinuationDuplicate, plan.Lineage, plan.Attempt)
	case continuationReplay:
		result, err := c.finishContinuationReplay(ctx, plan, prior)
		if err != nil {
			return nil, &result, err
		}
		return nil, &result, nil
	}
	_, runtime, exclusive := c.v3Binding()
	if !exclusive || runtime == nil || runtime.Session() == nil {
		c.continuations.abandon(plan)
		c.emitContinuationTelemetry(plan, continuationOutcomeRejected, 0)
		return nil, nil, ErrContinuationUnsupported
	}
	attempt, err := c.claimContinuation(ctx, plan, runtime)
	if err != nil {
		return nil, nil, err
	}
	return attempt, nil, nil
}

// claimContinuation takes the rotation gate and completes the checks that need
// the gate held: the durable duplicate scan, the source generation, and the
// message budget. A failure here releases the gate and the reservation.
func (c *Controller) claimContinuation(ctx context.Context, plan ContinuationPlan, runtime *session.Runtime) (*continuationAttempt, error) {
	attempt := &continuationAttempt{controller: c, plan: plan, source: runtime.Ref(), store: runtime.Session()}
	if err := c.beginRotation(); err != nil {
		c.continuations.abandon(plan)
		c.emitContinuationTelemetry(plan, continuationOutcomeRejected, 0)
		return nil, err
	}
	// From here the gate is held, so release() must run on every exit. A
	// successful claim hands the attempt (and the gate) to the caller instead.
	claimed := false
	defer func() {
		if !claimed {
			attempt.release()
		}
	}()

	// The ceiling is per lineage, not per plan: a caller that keeps minting new
	// dedup keys for one main line must still stop.
	if spent := c.continuations.committed(plan.Lineage); spent >= MaxAutomaticContinuations {
		c.emitContinuationTelemetry(plan, continuationOutcomeRejected, 0)
		return nil, fmt.Errorf("%w: lineage %s already used %d of %d",
			ErrContinuationCeiling, plan.Lineage, spent, MaxAutomaticContinuations)
	}
	// A previous run of this exact attempt may already have rotated. That has to
	// be answered from the source's own log: a restarted process has an empty
	// ledger and must still refuse.
	duplicate, err := findContinuationDuplicate(ctx, attempt.store, plan)
	if err != nil {
		return nil, err
	}
	if duplicate {
		c.emitContinuationTelemetry(plan, continuationOutcomeRejected, 0)
		return nil, fmt.Errorf("%w: source %s already records lineage %s",
			ErrContinuationDuplicate, attempt.source.SessionID, plan.Lineage)
	}
	if err := c.ensureContinuationSourceCurrent(attempt.store, plan); err != nil {
		c.emitContinuationTelemetry(plan, continuationOutcomeRejected, 0)
		return nil, err
	}

	attempt.message = continuationMessage(plan, attempt.source)
	attempt.messageTokens = continuationMessageTokenEstimate(attempt.message)
	if attempt.messageTokens >= ContinuationMessageBudget {
		c.emitContinuationTelemetry(plan, continuationOutcomeRejected, attempt.messageTokens)
		return nil, fmt.Errorf("%w: message is %d tokens, ceiling is %d",
			ErrContinuationBudget, attempt.messageTokens, ContinuationMessageBudget)
	}
	claimed = true
	return attempt, nil
}

// commitContinuation snapshots the source, reserves the child with the host,
// publishes it seeded with the recovery message, and lands the durable marker
// on the source. The gate stays held for the whole sequence.
func (c *Controller) commitContinuation(ctx context.Context, attempt *continuationAttempt) (ContinuationResult, error) {
	plan, source := attempt.plan, attempt.source
	// Nothing is created before the source is durable. A crash from here to the
	// host reservation costs at most the in-memory ledger entry.
	if err := c.Snapshot(); err != nil {
		return ContinuationResult{}, err
	}
	if err := c.extensionSessionPhase(ctx, extension.PointSessionRotate, dispatch.PhaseRotate, source.SessionID); err != nil {
		return ContinuationResult{}, err
	}
	createOptions, hostCommit, err := c.prepareContinuationRotation(ctx, plan, source)
	if err != nil {
		return ContinuationResult{}, err
	}

	// The source ends here, exactly as it does for /new — but with a reason the
	// host can distinguish, so it keeps the session instead of archiving it.
	c.hooks.SessionEnd(context.Background(), ContinuationReason)
	c.extensionSessionEvent(extension.PointSessionEnd, dispatch.PhaseEnd, source.SessionID)

	// The marker is written from inside the host's commit step: the child is
	// already durable and the source is still the bound execution session, so
	// the write takes the ordinary authority path. A failure aborts publication.
	markContinuation := func(commitCtx context.Context, continuation session.SessionRef) error {
		if hostCommit != nil {
			if err := hostCommit(commitCtx, continuation); err != nil {
				return err
			}
		}
		return c.writeContinuationMarker(commitCtx, attempt, continuation)
	}
	ref, err := c.bindSeededSessionWithCommit(ctx, createOptions, []provider.Message{attempt.message}, continuationLineageEvents(plan, source), markContinuation)
	if err != nil {
		return ContinuationResult{}, err
	}
	attempt.committed = true

	result := ContinuationResult{
		Source: source, Continuation: ref, Lineage: plan.Lineage, Attempt: plan.Attempt,
		DedupKey: plan.DedupKey, MessageID: attempt.message.ID, SummaryHash: plan.SummaryHash,
		SummaryTokens: plan.SummaryTokens, MessageTokens: attempt.messageTokens,
		Telemetry: continuationTelemetry(plan, continuationOutcomeRotated, attempt.messageTokens),
	}
	c.startExclusiveSession(ref, ContinuationReason)
	// The fresh session's plan/goal posture comes from its own empty projection,
	// so this disarms the previous goal exactly as /new does. The main line
	// crosses in the recovery block and the resumed turn, not in the goal FSM.
	c.continuations.finish(plan, result)
	return result, nil
}

// resumeContinuation releases the gate and enqueues the one member-scoped
// follow-on turn. The gate must be released first: the resumed turn is admitted
// through the ordinary guard, which refuses while a rotation is in flight, so
// holding it would deadlock the continuation against its own rotation.
func (c *Controller) resumeContinuation(ctx context.Context, attempt *continuationAttempt, result ContinuationResult) (ContinuationResult, error) {
	plan := attempt.plan
	attempt.release()
	if plan.Resume == nil {
		// A nil Resume is the caller's decision to drive this session itself, so
		// the restart recovery must not second-guess it.
		c.settleContinuationRecovery(result.Continuation)
		c.emitContinuationTelemetry(plan, result.Telemetry.Outcome, result.MessageTokens)
		return result, nil
	}
	// Claim before handing the resume to admission, not after: until the resumed
	// turn records itself the durable predicate still reads "no turn began", so a
	// concurrent recovery kick would queue a second main line. Failure releases.
	c.settleContinuationRecovery(result.Continuation)
	resume := ContinuationResume{
		Source: result.Source, Continuation: result.Continuation, Lineage: plan.Lineage,
		Attempt: plan.Attempt, DedupKey: plan.DedupKey,
	}
	if err := plan.Resume(ctx, resume); err != nil {
		c.releaseContinuationRecovery(result.Continuation)
		result.Resumed = false
		result.Telemetry = continuationTelemetry(plan, continuationOutcomeResumeErr, result.MessageTokens)
		c.continuations.finish(plan, result)
		c.emitContinuationTelemetry(plan, continuationOutcomeResumeErr, result.MessageTokens)
		// The rotation committed: the source is preserved and the continuation
		// exists with its recovery block. Only the follow-on turn failed, and the
		// error must not read as "the rescue did not happen".
		return result, fmt.Errorf("continuation session %s is active; resume failed: %w", result.Continuation.SessionID, err)
	}
	result.Resumed = true
	result.Telemetry = continuationTelemetry(plan, continuationOutcomeResumed, result.MessageTokens)
	c.continuations.finish(plan, result)
	c.emitContinuationTelemetry(plan, continuationOutcomeResumed, result.MessageTokens)
	return result, nil
}

// finishContinuationReplay answers a replayed dedup key without touching the
// session: it is the in-process half of "never start a rescue twice", and the
// durable half is the source marker.
func (c *Controller) finishContinuationReplay(ctx context.Context, plan ContinuationPlan, prior ContinuationResult) (ContinuationResult, error) {
	c.emitContinuationTelemetry(plan, continuationOutcomeReplayed, prior.MessageTokens)
	prior.Telemetry = continuationTelemetry(plan, continuationOutcomeReplayed, prior.MessageTokens)
	if prior.Resumed || plan.Resume == nil {
		return prior, nil
	}
	// The rotation committed but its resume did not. Resume once more against
	// the already-published continuation rather than rotating again, claiming it
	// first for the same reason resumeContinuation does.
	c.settleContinuationRecovery(prior.Continuation)
	resume := ContinuationResume{
		Source: prior.Source, Continuation: prior.Continuation, Lineage: prior.Lineage,
		Attempt: prior.Attempt, DedupKey: prior.DedupKey,
	}
	if err := plan.Resume(ctx, resume); err != nil {
		c.releaseContinuationRecovery(prior.Continuation)
		prior.Resumed = false
		prior.Telemetry = continuationTelemetry(plan, continuationOutcomeResumeErr, prior.MessageTokens)
		c.continuations.finish(plan, prior)
		c.emitContinuationTelemetry(plan, continuationOutcomeResumeErr, prior.MessageTokens)
		return prior, fmt.Errorf("continuation session %s is active; resume failed: %w", prior.Continuation.SessionID, err)
	}
	prior.Resumed = true
	prior.Telemetry = continuationTelemetry(plan, continuationOutcomeResumed, prior.MessageTokens)
	c.continuations.finish(plan, prior)
	return prior, nil
}

// ensureContinuationSourceCurrent refuses a plan frozen from a source that has
// since moved. Comparing the plan's generation against the live session
// sequence is what makes "this summary describes this transcript" checkable
// instead of assumed.
func (c *Controller) ensureContinuationSourceCurrent(store *session.Session, plan ContinuationPlan) error {
	if plan.Generation == 0 {
		// An unset generation is a caller that cannot vouch for the transcript.
		// Fail closed: a stale recovery block is worse than a refused rescue.
		return fmt.Errorf("%w: plan carries no source generation", ErrContinuationStale)
	}
	if current := store.EventSequence(); current != plan.Generation {
		return fmt.Errorf("%w: plan generation %d, source at %d", ErrContinuationStale, plan.Generation, current)
	}
	return nil
}

// continuationMarkerScanLimit bounds the duplicate scan. A marker for one
// attempt is written one commit after the transcript it summarises was frozen,
// so the window only has to absorb whatever else the source accepted in
// between; a source that moved further is refused as stale anyway.
const continuationMarkerScanLimit = 64

// findContinuationDuplicate reports whether the source already carries the
// marker for this exact attempt. Reading from the plan's generation forward is
// both the only place the marker can be and what bounds the scan.
func findContinuationDuplicate(ctx context.Context, store *session.Session, plan ContinuationPlan) (bool, error) {
	if store == nil {
		return false, nil
	}
	page, err := store.AcceptedPage(ctx, plan.Generation, continuationMarkerScanLimit)
	if err != nil {
		return false, fmt.Errorf("read continuation marker: %w", err)
	}
	for _, commit := range page.Commits {
		for _, ev := range commit.Events {
			if ev.Kind != "diagnostic" || len(ev.Payload) == 0 {
				continue
			}
			var marker struct {
				Type     string `json:"type"`
				DedupKey string `json:"dedupKey"`
			}
			if err := json.Unmarshal(ev.Payload, &marker); err != nil {
				continue
			}
			if marker.Type == "context-continuation-v1" && marker.DedupKey == plan.DedupKey {
				return true, nil
			}
		}
	}
	return false, nil
}

// prepareContinuationRotation asks the identity-owning host to reserve the
// fresh identity, marking the request as a continuation so a host that archives
// on "clear" keeps the source instead.
func (c *Controller) prepareContinuationRotation(ctx context.Context, plan ContinuationPlan, source session.SessionRef) (session.CreateOptions, func(context.Context, session.SessionRef) error, error) {
	hook := c.sessionRotationHandler()
	if hook == nil {
		// An embedded controller with no identity owner still rotates; the
		// parent link below is what keeps source and continuation linked.
		return session.CreateOptions{ParentSessionID: source.SessionID}, nil, nil
	}
	admission, err := hook(ctx, SessionRotationRequest{
		Source: source, Reason: ContinuationReason,
		Continuation: &ContinuationLineage{
			Lineage: plan.Lineage, DedupKey: plan.DedupKey, Attempt: plan.Attempt,
			Trigger: plan.Trigger, SummaryHash: plan.SummaryHash,
			SummaryTokens: plan.SummaryTokens, SourceTokens: plan.SourceTokens,
			CompactedTokens: plan.CompactedTokens, ReductionRatio: plan.ReductionRatio,
		},
	})
	if err != nil {
		return session.CreateOptions{}, nil, err
	}
	if admission.CreateOptions.ParentSessionID == "" {
		// Lineage is the controller's to assert even when the host does not set
		// it: source and continuation must be linked in the immutable header.
		admission.CreateOptions.ParentSessionID = source.SessionID
	}
	if admission.CreateOptions.SessionID == "" || admission.CreateOptions.SessionID == source.SessionID {
		// A continuation that reuses the source identity is not a continuation.
		// Publishing it would overwrite the very session the rescue preserves.
		return session.CreateOptions{}, nil, fmt.Errorf("%w: host reserved no fresh session identity", ErrContinuationPlanInvalid)
	}
	return admission.CreateOptions, admission.Commit, nil
}

func (c *Controller) sessionRotationHandler() func(context.Context, SessionRotationRequest) (SessionRotationPlan, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onSessionRotation
}

// writeContinuationMarker records the rescue on the source session. It is
// idempotent by operation id, so a replay returns the already-committed record
// instead of writing a second one.
func (c *Controller) writeContinuationMarker(ctx context.Context, attempt *continuationAttempt, continuation session.SessionRef) error {
	store := attempt.store
	if store == nil {
		return session.ErrSessionNotRunning
	}
	payload, err := continuationMarkerPayload(attempt.plan, attempt.source, continuation, attempt.messageTokens)
	if err != nil {
		return err
	}
	if !c.sessionEventCommitAllowed() {
		return session.ErrStaleExecution
	}
	before := store.EventSequence()
	c.turnEvents.commitMu.Lock()
	commit, err := c.appendSessionBatch(ctx, store, session.Batch{
		OperationID: continuationMarkerOperationID(attempt.plan),
		Events:      []session.Event{{Kind: "diagnostic", Optional: true, Payload: payload}},
	})
	c.turnEvents.commitMu.Unlock()
	if err != nil {
		return fmt.Errorf("record continuation on source session: %w", err)
	}
	if commit.LastSequence() <= before {
		// The source already carried this marker, which means the host reserved
		// a second identity for a rescue that already ran. Refuse before the
		// swap so the publisher's own rollback closes the duplicate child.
		return fmt.Errorf("%w: source %s already records lineage %s",
			ErrContinuationDuplicate, attempt.source.SessionID, attempt.plan.Lineage)
	}
	// The marker must outlive this process: it is the only durable witness that
	// this rescue already ran, so a restart that replays the plan finds it
	// instead of rotating a second time.
	receipt, err := store.Flush(ctx)
	if err != nil {
		return fmt.Errorf("flush continuation marker: %w", err)
	}
	if receipt.DurableSequence < commit.LastSequence() {
		return fmt.Errorf("continuation marker is not durable through sequence %d", commit.LastSequence())
	}
	return nil
}

// emitContinuationTelemetry publishes one rescue record. It is an
// operator-audience notice: never forwarded to an end-user chat, with the
// structured record in Detail so the summary text itself never has to be. The
// same record reaches the caller through ContinuationResult.Telemetry, which is
// what a host folds into its per-request cache diagnostics.
func (c *Controller) emitContinuationTelemetry(plan ContinuationPlan, outcome string, messageTokens int) {
	if c == nil || c.sink == nil {
		return
	}
	record := continuationTelemetry(plan, outcome, messageTokens)
	detail, err := json.Marshal(struct {
		Type string `json:"type"`
		ContinuationTelemetry
	}{Type: "context-continuation-telemetry-v1", ContinuationTelemetry: record})
	if err != nil {
		return
	}
	level := event.LevelInfo
	switch outcome {
	case continuationOutcomeRejected, continuationOutcomeResumeErr:
		level = event.LevelWarn
	}
	c.sink.Emit(event.Event{
		Kind:     event.Notice,
		Level:    level,
		Audience: event.NoticeAudienceOperator,
		Text: fmt.Sprintf("context rescue %s: lineage %s attempt %d, summary %d tokens",
			outcome, plan.Lineage, plan.Attempt, plan.SummaryTokens),
		Detail: string(detail),
	})
}

func continuationTelemetry(plan ContinuationPlan, outcome string, messageTokens int) ContinuationTelemetry {
	return ContinuationTelemetry{
		Trigger:         plan.Trigger,
		SourceTokens:    plan.SourceTokens,
		CompactedTokens: plan.CompactedTokens,
		ReductionRatio:  plan.ReductionRatio,
		SummaryTokens:   plan.SummaryTokens,
		MessageTokens:   messageTokens,
		Generation:      plan.Generation,
		Attempt:         plan.Attempt,
		Outcome:         outcome,
	}
}

// SessionGeneration reports the bound session's accepted event sequence, which
// is the value ContinuationPlan.Generation must carry. A rescue evaluator that
// freezes a transcript reads this first and passes it through, so the plan is
// bound to the exact transcript it summarised rather than to "the session".
//
// It returns 0 when no exclusive session is bound, which
// ContinueSessionFromPlan treats as "cannot vouch" and refuses.
func (c *Controller) SessionGeneration() uint64 {
	if c == nil {
		return 0
	}
	_, runtime, exclusive := c.v3Binding()
	if !exclusive || runtime == nil || runtime.Session() == nil {
		return 0
	}
	return runtime.Session().EventSequence()
}
