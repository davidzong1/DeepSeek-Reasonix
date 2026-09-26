package agent

import (
	"context"
	"errors"
	"fmt"

	"reasonix/internal/event"
	"reasonix/internal/i18n"
	"reasonix/internal/provider"
)

type contextRecoveryBudget struct {
	retries int
}

func (a *Agent) recoverContextLimit(ctx context.Context, frozen samplingRequest, err error, budget *contextRecoveryBudget) (samplingRequest, bool, string, error) {
	limit := provider.AsContextLimitError(err)
	if a == nil || limit == nil || budget == nil {
		return samplingRequest{}, false, contextRecoveryFailed, nil
	}
	omitted := frozen.req.MaxTokens == 0
	if limit.PromptTokens > 0 {
		a.setPromptTokenCalibrationFromActive(limit.PromptTokens)
	}
	a.learnContextBudget(limit.WindowTokens, limit.CompletionTokens, omitted)
	adm := a.lastAdmission()
	adm.ObservedWindow = limit.WindowTokens
	adm.ObservedPrompt = limit.PromptTokens
	adm.ObservedCompletion = limit.CompletionTokens
	a.storeAdmission(adm)

	window := a.effectiveContextWindow()
	prompt := limit.PromptTokens
	if prompt <= 0 {
		prompt = a.estimatedRequestTokens(frozen.req)
	}
	physical := window - prompt - outputBudgetReserve
	// An overflow without token numbers cannot size a retry: the estimate that
	// admitted the request is the number the provider just rejected.
	if limit.PromptTokens <= 0 && limit.WindowTokens <= 0 {
		physical = 0
	}
	if physical > 0 && budget.retries == 0 {
		next := freezeProviderRequest(frozen.req)
		next.MaxTokens = physical
		if frozen.req.MaxTokens > 0 && frozen.req.MaxTokens < physical {
			next.MaxTokens = frozen.req.MaxTokens
		}
		budget.retries++
		// Publish the request that will actually be retried, not the stale
		// pre-error admission. The Context Panel reads this atomic snapshot while
		// the turn is still active and after it completes.
		adm.WindowMode = provider.ContextWindowShared.String()
		adm.Source = provider.ContextBudgetSourceLearned
		adm.WindowTokens = window
		adm.PromptTokens = prompt
		adm.PhysicalRemaining = physical
		if adm.RequestedOutputTokens <= 0 {
			adm.RequestedOutputTokens = limit.CompletionTokens
		}
		if omitted && adm.AutoOutputTokens <= 0 {
			adm.AutoOutputTokens = limit.CompletionTokens
		}
		adm.EffectiveOutputTokens = next.MaxTokens
		adm.Clipped = adm.RequestedOutputTokens > 0 && next.MaxTokens < adm.RequestedOutputTokens
		adm.ApplyMaxTokens = next.MaxTokens > 0
		adm.LastRecovery = contextRecoveryLearnedRetry
		a.storeAdmission(adm)
		a.emitContextRecoveryNotice(contextRecoveryLearnedRetry, limit, next.MaxTokens)
		shape := a.requestCalibrationShape(next)
		a.sess.output.activeReqShape.Store(&shape)
		return samplingRequest{req: next}, true, contextRecoveryLearnedRetry, nil
	}
	if physical <= 0 && budget.retries == 0 {
		startProjectionVersion := a.currentProjectionVersion()
		if _, perr := a.contextManager().Prepare(ctx, ContextPreparePolicy{
			Trigger: CompactionTriggerOverflow,
			Force:   true,
			// Keep the rescue policy across the provider-rejection recovery
			// path. This call used to omit the opt-in and therefore converted
			// an enabled rescue into truncation exactly at the hard boundary.
			AllowContextRescue: a.contextRescue,
		}); perr != nil {
			// A rescue plan means the over-ceiling request must not be retried
			// in this session. Return it intact so the controller can rotate.
			if errors.Is(perr, ErrContextRescuePlanned) {
				return samplingRequest{}, false, contextRecoveryFailed, perr
			}
			a.setLastRecovery(contextRecoveryFailed)
			return samplingRequest{}, false, contextRecoveryFailed, nil
		}
		if a.currentProjectionVersion() <= startProjectionVersion {
			a.setLastRecovery(contextRecoveryFailed)
			return samplingRequest{}, false, contextRecoveryFailed, nil
		}
		rebuilt, rerr := a.buildSamplingRequest(ctx, CompactionTriggerPressure)
		if rerr != nil {
			a.setLastRecovery(contextRecoveryFailed)
			return samplingRequest{}, false, contextRecoveryFailed, nil
		}
		if aerr := a.applyAdmissionToRequest(&rebuilt.req); aerr != nil {
			a.setLastRecovery(contextRecoveryFailed)
			return samplingRequest{}, false, contextRecoveryFailed, nil
		}
		budget.retries++
		a.setLastRecovery(contextRecoveryCompacted)
		a.emitContextRecoveryNotice(contextRecoveryCompacted, limit, rebuilt.req.MaxTokens)
		shape := a.requestCalibrationShape(rebuilt.req)
		a.sess.output.activeReqShape.Store(&shape)
		return samplingRequest{req: freezeProviderRequest(rebuilt.req)}, true, contextRecoveryCompacted, nil
	}
	a.setLastRecovery(contextRecoveryFailed)
	return samplingRequest{}, false, contextRecoveryFailed, nil
}

func (a *Agent) emitContextRecoveryNotice(kind string, limit *provider.ContextLimitError, nextOutput int) {
	if a == nil || a.svc.sink == nil {
		return
	}
	text := i18n.M.ContextRecoveryAdjustBudget
	if kind == contextRecoveryCompacted {
		text = i18n.M.ContextRecoveryCompacted
	}
	detail := fmt.Sprintf("recovery=%s next_output=%d", kind, nextOutput)
	if limit != nil {
		detail = fmt.Sprintf("%s window=%d prompt=%d completion=%d requested=%d",
			detail, limit.WindowTokens, limit.PromptTokens, limit.CompletionTokens, limit.RequestedTokens)
	}
	a.svc.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: text, Detail: detail})
}
