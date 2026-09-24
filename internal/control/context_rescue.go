package control

import (
	"context"
	"errors"
	"sync"

	"reasonix/internal/agent"
	"reasonix/internal/event"
)

// contextRescueResumeText is the host-framed instruction that restarts the main
// line in the continuation session. It is host framing, not the member's own
// words, so it is submitted through the framed path.
const contextRescueResumeText = "Your previous session exhausted its context window before the pending work finished. " +
	"Its main line was summarised into the recovery briefing at the start of this session: pick that work up from where it stops. " +
	"Do not re-establish what the briefing already records, and re-verify anything it marks unknown before relying on it."

// contextRescueState owns the one rescue a controller may have in flight. It is
// independent of the rotation gate: the gate protects one swap, this holds the
// queued plan and keeps admission and the inbox from starting a turn into a
// session that is about to be rotated away.
//
// The agent certifies the plan inside a model turn and refuses to send the
// over-ceiling request, but the controller cannot act from there: the rotation
// gate refuses while a turn is running. So the plan is captured at that turn's
// terminal boundary and applied at the first moment the controller owns the
// session again — the level-triggered slot the goal driver also uses.
type contextRescueState struct {
	mu      sync.Mutex
	pending *ContinuationPlan
	running bool
}

// noteContextRescue captures a certified rescue plan from a failed model turn.
// It reports whether a plan was captured. The plan is not applied here: the
// caller is inside the failing turn's terminal path.
func (c *Controller) noteContextRescue(err error) bool {
	if c == nil || err == nil {
		return false
	}
	certified, ok := agent.ContextRescuePlanFromError(err)
	if !ok {
		return false
	}
	plan, buildErr := c.continuationPlanFromRescue(certified)
	if buildErr != nil {
		// A plan the controller cannot express is reported, never silently
		// dropped: the member is over its ceiling and needs to hear that.
		c.sink.Emit(event.Event{
			Kind: event.Notice, Level: event.LevelWarn,
			Text: "context rescue could not be applied: " + buildErr.Error(),
		})
		return false
	}
	c.rescue.mu.Lock()
	c.rescue.pending = &plan
	c.rescue.mu.Unlock()
	// The turn-done error stays the agent's own diagnostic; this is the copy the
	// user reads. Without it the failure looks like a defect rather than a
	// handover that is about to happen.
	c.sink.Emit(event.Event{
		Kind: event.Notice, Level: event.LevelWarn,
		Text: "This session's context window could not be recovered, so the request was not sent. " +
			"The main line is being carried into a fresh session that continues it.",
	})
	return true
}

// contextRescuePending reports whether a rescue is queued or being applied.
// Admission and inbox dispatch consult it so nothing starts a turn into the
// session the rescue is about to leave.
func (c *Controller) contextRescuePending() bool {
	if c == nil {
		return false
	}
	c.rescue.mu.Lock()
	defer c.rescue.mu.Unlock()
	return c.rescue.pending != nil
}

// continuationPlanFromRescue adapts a certified agent plan to this package's
// contract. The message is carried verbatim; the reduction ratio and token
// counts are recorded, never recomputed.
func (c *Controller) continuationPlanFromRescue(certified agent.ContextRecoveryPlan) (ContinuationPlan, error) {
	_, runtime, exclusive := c.v3Binding()
	if !exclusive || runtime == nil || runtime.Session() == nil {
		return ContinuationPlan{}, ErrContinuationUnsupported
	}
	source := runtime.Ref()
	lineage, attempt, inherited := readContinuationLineage(context.Background(), runtime.Session())
	if !inherited {
		// A chain starts here: every later rescue in it reuses this identity, so
		// the attempt ceiling counts main-line rescues rather than sessions.
		lineage = "rescue-chain:" + source.SessionID
		attempt = 0
	}
	return ContinuationPlan{
		Trigger:         certified.Trigger,
		SourceTokens:    certified.SourceTokens,
		CompactedTokens: certified.CompactedTokens,
		ReductionRatio:  certified.ReductionRatio,
		Summary:         certified.Summary,
		// The agent fingerprints the summary its own way; this contract's hash
		// is recomputed so the marker records one the controller can verify.
		SummaryHash:   continuationDigest(certified.Summary),
		SummaryTokens: certified.BlockTokens,
		DedupKey:      certified.DedupKey,
		Lineage:       lineage,
		Attempt:       attempt + 1,
		Message:       certified.Message,
		Resume:        c.resumeContextContinuation,
	}, nil
}

// resumeContextContinuation enqueues the continuation turn on this member's own
// backend. The plan's Resume runs after the rotation gate is released, so this
// goes through the ordinary admission guard.
func (c *Controller) resumeContextContinuation(_ context.Context, resume ContinuationResume) error {
	if resume.Continuation.SessionID == "" {
		return ErrContinuationPlanInvalid
	}
	return c.SubmitUserTurnFramedOrError(contextRescueResumeText, contextRescueResumeText)
}

// applyPendingContextRescue applies the queued rescue if one is waiting and the
// controller now owns the session. It is level-triggered: a refusal caused by a
// still-running turn leaves the plan queued for the next completion.
func (c *Controller) applyPendingContextRescue() {
	plan, ok := c.takeContextRescue()
	if !ok {
		return
	}
	// The generation is stamped here rather than at capture: between the two the
	// controller appended the failed turn's own terminal records, and what the
	// rotation must vouch for is that nothing raced it after this point.
	plan.Generation = c.SessionGeneration()
	result, err := c.ContinueSessionFromPlan(context.Background(), plan)
	if err != nil && isTransientContinuationRefusal(err) {
		// The session was not ours yet. Keep the plan queued and the gates held:
		// the turn that beat us republishes work when it settles, and that is
		// where this is retried. Only a verdict clears the queue.
		c.requeueContextRescue(plan)
		return
	}
	c.finishContextRescue()

	if err != nil {
		c.sink.Emit(event.Event{
			Kind: event.Notice, Level: event.LevelWarn,
			Text: "context rescue was refused and the main line is not continuing: " + err.Error(),
		})
		return
	}
	if !result.Resumed {
		c.sink.Emit(event.Event{
			Kind: event.Notice, Level: event.LevelWarn,
			Text: "context rescue created continuation session " + result.Continuation.SessionID +
				" but could not restart the turn; resume it to continue the main line",
		})
	}
}

// takeContextRescue claims the queued plan for exactly one applier. Claiming
// also clears the queue flag: from here the rotation's own gate is what keeps
// other work out, and holding this one too would block the resume the rescue is
// about to enqueue — the continuation turn must be admitted into the session
// the rotation just published.
func (c *Controller) takeContextRescue() (ContinuationPlan, bool) {
	c.rescue.mu.Lock()
	defer c.rescue.mu.Unlock()
	if c.rescue.pending == nil || c.rescue.running {
		return ContinuationPlan{}, false
	}
	c.rescue.running = true
	plan := *c.rescue.pending
	c.rescue.pending = nil
	return plan, true
}

func (c *Controller) finishContextRescue() {
	c.rescue.mu.Lock()
	c.rescue.pending = nil
	c.rescue.running = false
	c.rescue.mu.Unlock()
	// The rescue held the inbox and goal gates: release them so parked, durable
	// and goal work resumes against the continuation.
	c.maybeDispatchInbox()
	c.kickGoalDriver()
}

func (c *Controller) requeueContextRescue(plan ContinuationPlan) {
	c.rescue.mu.Lock()
	c.rescue.pending = &plan
	c.rescue.running = false
	c.rescue.mu.Unlock()
}

// isTransientContinuationRefusal reports refusals that mean "not this moment".
// A running turn or an in-flight rotation is not a verdict on the plan, so the
// rescue is retried rather than reported as a failure.
func isTransientContinuationRefusal(err error) bool {
	return IsSessionRotationBusy(err) || errors.Is(err, ErrMaintenanceBusy)
}
