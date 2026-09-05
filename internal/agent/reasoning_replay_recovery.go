package agent

import (
	"reasonix/internal/event"
	"reasonix/internal/i18n"
	"reasonix/internal/provider"
)

// reasoningReplayRecoveryBudget bounds the thinking-400 catch-and-repair to one
// retry per model round and records the repaired provider-message prefix.
type reasoningReplayRecoveryBudget struct {
	retries int
	cutoff  int
	anchor  string
}

// recoverReasoningReplay400 applies the vendor-documented self-heal for a
// provider that rejected replayed thinking history with HTTP 400: rebuild the
// frozen request's messages through the strong projection and retry exactly
// once. Everything except Messages stays byte-identical to the rejected
// request; the repaired message count bounds future projection.
func (a *Agent) recoverReasoningReplay400(frozen samplingRequest, err error, budget *reasoningReplayRecoveryBudget) (samplingRequest, bool) {
	if a == nil || budget == nil || budget.retries > 0 {
		return samplingRequest{}, false
	}
	if provider.AsReasoningReplayError(err) == nil {
		return samplingRequest{}, false
	}
	repaired, changed := provider.ProjectReasoningStrippedMessages(a.svc.prov, frozen.req.Messages)
	if !changed {
		return samplingRequest{}, false
	}
	budget.retries++
	budget.cutoff = len(repaired)
	if budget.cutoff > 0 {
		budget.anchor = reasoningReplayMessageFingerprint(repaired[budget.cutoff-1])
	}
	next := frozen.req
	next.Messages = repaired
	return samplingRequest{req: next}, true
}

// tryRecoverReasoningReplay400 is the streamWithSamplingRecovery branch for a
// thinking-400: the frozen history's replayed reasoning is stale for this
// provider, so repair the projection once and retry; every other 400 falls
// through to the terminal path. On a repair the speculative attempt's buffered
// events are discarded and the attempt is audited before the caller replays.
func (a *Agent) tryRecoverReasoningReplay400(streamSink *deferredStreamSink, frozen samplingRequest, attemptID string, attempt int, err error, budget *reasoningReplayRecoveryBudget) (samplingRequest, bool) {
	next, ok := a.recoverReasoningReplay400(frozen, err, budget)
	if !ok {
		return samplingRequest{}, false
	}
	if streamSink != nil {
		streamSink.Discard()
	}
	a.emitStreamAttempt(attemptID, event.StreamAttemptDiscard, attempt, "reasoning_replay_400", err)
	event.RecordProtocolRecovery(a.svc.sink, event.ProtocolRecoveryAudit{Kind: event.ProtocolRecoveryReasoningReplay400Detected})
	return next, true
}

// activateReasoningReplayStrongProjection records the repaired history prefix.
// Messages appended after it keep their normal reasoning/tool replay.
func (a *Agent) activateReasoningReplayStrongProjection(cutoff int, anchor string) {
	if a == nil {
		return
	}
	a.sess.reasoningReplayStrongProjection = cutoff
	a.sess.reasoningReplayStrongProjectionAnchor = anchor
	event.RecordProtocolRecovery(a.svc.sink, event.ProtocolRecoveryAudit{Kind: event.ProtocolRecoveryReasoningReplay400Recovered})
	a.emitReasoningReplayRepairNotice()
}

func (a *Agent) emitReasoningReplayRepairNotice() {
	if a == nil || a.svc.sink == nil {
		return
	}
	a.svc.sink.Emit(event.Event{
		Kind:   event.Notice,
		Level:  event.LevelWarn,
		Code:   event.NoticeCodeReasoningReplayRepair,
		Text:   i18n.M.ReasoningReplayRepair,
		Detail: "provider rejected replayed thinking blocks (HTTP 400); retried once with reasoning stripped from the projected history",
	})
}
