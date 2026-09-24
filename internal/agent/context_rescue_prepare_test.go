package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

// Admission-level context rescue tests: these drive ContextManager.Prepare and
// assert whether a plan is certified and how it reaches a caller that discards
// the PreparedContext on error. Planner contract tests live next door.

// The production call shape discards the PreparedContext the moment err is
// non-nil (`prepared, err := ...; if err != nil { return err }`), so the plan
// must travel in the error. Without that, a certified plan can never reach the
// controller that would rotate the session.
func TestContextRescuePlanSurvivesTheDiscardingCallShape(t *testing.T) {
	msgs := rescueOverCeilingTranscript()
	prov := &rescueProvider{
		defaultReply: "## Goal\nfinish the pending refactor\n\n## Pending & next step\nrun the package tests",
		failAt:       map[int]error{0: errors.New("summarizer unavailable")},
	}
	a := newRescueAgent(t, prov, msgs)
	calibrateRescueEstimate(a, msgs)

	// Mirror buildSamplingRequest exactly: keep nothing but the error.
	var carried *ContextRecoveryPlan
	_, err := func() (ContextRecoveryPlan, error) {
		prepared, err := prepareWithRescue(t, a, true)
		carried = prepared.Recovery
		return ContextRecoveryPlan{}, err
	}()
	if !errors.Is(err, ErrContextRescuePlanned) {
		t.Fatalf("err = %v, want ErrContextRescuePlanned", err)
	}
	plan, ok := ContextRescuePlanFromError(err)
	if !ok {
		t.Fatal("the plan must be recoverable from the error alone")
	}
	if carried == nil {
		t.Fatal("Prepare must also report the plan on the returned context")
	}
	if plan.DedupKey != carried.DedupKey || plan.BlockTokens != carried.BlockTokens || plan.SummaryHash != carried.SummaryHash {
		t.Fatal("the error carrier and the context carrier must be the same plan")
	}
	if plan.DedupKey == "" || plan.Summary == "" || plan.SummaryHash != summaryContentHash(plan.Summary) {
		t.Fatal("the recovered plan must be complete and self-consistent")
	}
	if !contextRescueBlockFits(plan.BlockTokens) {
		t.Fatalf("block = %d tokens, must be under %d", plan.BlockTokens, contextRescueMaxBlockTokens)
	}
	if !IsContextRescueMessage(plan.Message) {
		t.Fatal("the recovered plan must carry the continuation message")
	}
}

func TestContextRescuePlanFromErrorRejectsEverythingElse(t *testing.T) {
	for name, err := range map[string]error{
		"nil":                 nil,
		"unrelated":           errors.New("unrelated"),
		"classified failure":  newContextRescueError(ContextRescueOverBudget, errors.New("too big")),
		"summary failure":     newContextRescueError(ContextRescueSummaryFailed, errors.New("summarizer")),
		"cancellation":        context.Canceled,
		"not eligible":        errContextRescueNotEligible,
		"compaction required": fmt.Errorf("%w: no room", ErrCompactionRequired),
	} {
		if plan, ok := ContextRescuePlanFromError(err); ok {
			t.Errorf("%s: yielded a plan (block %d), want none", name, plan.BlockTokens)
		}
	}

	carrier := &ContextRescueRequired{Plan: ContextRecoveryPlan{
		CompactedTokens: 10, ReductionRatio: 0.01,
		Message: HostGeneratedUserMessage(contextRescueTagOpen + "\nbriefing\n" + contextRescueTagClose),
	}}
	if !errors.Is(carrier, ErrContextRescuePlanned) {
		t.Fatal("the carrier must still satisfy errors.Is against the sentinel")
	}
	if errors.Is(carrier, ErrCompactionRequired) {
		t.Fatal("the carrier must not match an unrelated sentinel")
	}
	if plan, ok := ContextRescuePlanFromError(carrier); !ok || plan.CompactedTokens != 10 {
		t.Fatal("the carrier must yield its plan")
	}
	if IsContextRescueMessage(provider.Message{Role: provider.RoleUser, Content: contextRescueTagOpen}) {
		t.Fatal("a rescue tag without host provenance is not a rescue briefing")
	}
}

// As a resolved branch of the default ladder: an ineffective fold that did not
// opt in must not be reachable through the new error carrier either.
func TestContextRescueDefaultLadderNeverCarriesAPlan(t *testing.T) {
	msgs := rescueOverCeilingTranscript()
	prov := &rescueProvider{
		defaultReply: "## Goal\nfinish the pending refactor",
		failAt:       map[int]error{0: errors.New("summarizer unavailable")},
	}
	a := newRescueAgent(t, prov, msgs)
	calibrateRescueEstimate(a, msgs)

	_, err := prepareWithRescue(t, a, false)
	if errors.Is(err, ErrContextRescuePlanned) {
		t.Fatal("an unopted transaction must not report a ready plan")
	}
	if _, ok := ContextRescuePlanFromError(err); ok {
		t.Fatal("an unopted transaction must not carry a plan")
	}
}

// rescueOverCeilingTranscript builds a transcript whose provider-visible
// request estimate sits above the hard ceiling. That is the only window a
// rescue can fire in: past the fold trigger, so maintenance runs at all, and at
// or above the ceiling, so a view compaction cannot shrink has nowhere left.
func rescueOverCeilingTranscript() []provider.Message {
	msgs := []provider.Message{{Role: provider.RoleSystem, Content: "system rules"}}
	body := strings.Repeat("x", 2_200)
	for i := range 25 {
		msgs = append(msgs,
			provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("question %d %s", i, body)},
			provider.Message{Role: provider.RoleAssistant, Content: fmt.Sprintf("answer %d %s", i, body)},
		)
	}
	return msgs
}

func prepareWithRescue(t *testing.T, a *Agent, allow bool) (PreparedContext, error) {
	t.Helper()
	return a.contextManager().Prepare(context.Background(), ContextPreparePolicy{
		Trigger: CompactionTriggerPressure, AllowContextRescue: allow,
	})
}

// The whole point of the rung: compaction cannot shrink the view, so Prepare
// certifies a continuation payload and refuses to send the rejected view.
func TestContextRescuePlansThroughPrepare(t *testing.T) {
	msgs := rescueOverCeilingTranscript()
	prov := &rescueProvider{
		defaultReply: "## Goal\nfinish the pending refactor\n\n## Pending & next step\nrun the package tests",
		failAt:       map[int]error{0: errors.New("summarizer unavailable")},
	}
	a := newRescueAgent(t, prov, msgs)
	calibrateRescueEstimate(a, msgs)
	before, _ := a.sess.conversation.snapshotMessagesVersion()

	prepared, err := prepareWithRescue(t, a, true)
	if !errors.Is(err, ErrContextRescuePlanned) {
		t.Fatalf("err = %v, want ErrContextRescuePlanned", err)
	}
	if prepared.Recovery == nil {
		t.Fatal("a planned rescue must return the plan")
	}
	plan := prepared.Recovery
	if plan.CompactedTokens < a.hardInputCeiling() {
		t.Fatalf("compacted tokens = %d, want at or above the hard ceiling %d", plan.CompactedTokens, a.hardInputCeiling())
	}
	if plan.ReductionRatio >= contextRescueReductionRatio {
		t.Fatalf("reduction ratio = %v, want under %v", plan.ReductionRatio, contextRescueReductionRatio)
	}
	if !contextRescueBlockFits(plan.BlockTokens) {
		t.Fatalf("block = %d tokens, must be under %d", plan.BlockTokens, contextRescueMaxBlockTokens)
	}
	if plan.DedupKey == "" {
		t.Fatal("a planned rescue must carry its lineage re-entry key")
	}
	if prepared.InputTokens < a.hardInputCeiling() {
		t.Fatalf("the reported view = %d tokens, want the rejected over-ceiling view", prepared.InputTokens)
	}
	// The source session is untouched: a plan proposes, it does not rotate.
	after, _ := a.sess.conversation.snapshotMessagesVersion()
	if len(after) != len(before) {
		t.Fatalf("transcript length changed from %d to %d", len(before), len(after))
	}
	if got := a.currentProjectionVersion(); got != 0 {
		t.Fatalf("projection version = %d, want 0: nothing may be installed", got)
	}
}

// Without the opt-in, an ineffective fold keeps its existing behaviour. Part A
// must not change the default ladder for any current caller.
func TestContextRescueLeavesTheDefaultLadderAlone(t *testing.T) {
	msgs := rescueOverCeilingTranscript()
	prov := &rescueProvider{
		defaultReply: "## Goal\nfinish the pending refactor",
		failAt:       map[int]error{0: errors.New("summarizer unavailable")},
	}
	a := newRescueAgent(t, prov, msgs)
	calibrateRescueEstimate(a, msgs)

	prepared, err := prepareWithRescue(t, a, false)
	if errors.Is(err, ErrContextRescuePlanned) {
		t.Fatal("a transaction that did not opt in must never plan a rescue")
	}
	if prepared.Recovery != nil {
		t.Fatal("a transaction that did not opt in must never return a plan")
	}
	if prov.calls() != 1 {
		t.Fatalf("summarizer calls = %d, want only the failed fold attempt", prov.calls())
	}
}

// A fold that does reclaim the request continues in place: no plan, no error,
// and the compacted view is what the caller sends.
func TestContextRescueDoesNotFireWhenCompactionRecovers(t *testing.T) {
	msgs := rescueOverCeilingTranscript()
	prov := &rescueProvider{defaultReply: "## Goal\nfinish the pending refactor"}
	a := newRescueAgent(t, prov, msgs)
	calibrateRescueEstimate(a, msgs)

	prepared, err := prepareWithRescue(t, a, true)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if prepared.Recovery != nil {
		t.Fatal("a recovered fold must not plan a continuation")
	}
	if prepared.InputTokens >= a.hardInputCeiling() {
		t.Fatalf("prepared view = %d tokens, want it under the hard ceiling %d", prepared.InputTokens, a.hardInputCeiling())
	}
	if got := a.currentProjectionVersion(); got == 0 {
		t.Fatal("a recovered fold must install its projection")
	}
}

// A rescue that cannot be certified must not be papered over with a lossy view
// the caller never agreed to: the classified failure comes back and the session
// stays intact.
func TestContextRescueSurfacesAClassifiedFailureFromPrepare(t *testing.T) {
	msgs := rescueOverCeilingTranscript()
	prov := &rescueProvider{
		failAt: map[int]error{
			0: errors.New("summarizer unavailable"),
			1: errors.New("summarizer still unavailable"),
		},
	}
	a := newRescueAgent(t, prov, msgs)
	calibrateRescueEstimate(a, msgs)
	before, _ := a.sess.conversation.snapshotMessagesVersion()

	prepared, err := prepareWithRescue(t, a, true)
	if err == nil {
		t.Fatal("an uncertifiable rescue must return an error")
	}
	if errors.Is(err, ErrContextRescuePlanned) {
		t.Fatal("a failed rescue must not report a ready plan")
	}
	if code := ContextRescueCode(err); code != ContextRescueSummaryFailed {
		t.Fatalf("code = %q, want %q (%v)", code, ContextRescueSummaryFailed, err)
	}
	if prepared.Recovery != nil {
		t.Fatal("a failed rescue must not return a plan")
	}
	after, _ := a.sess.conversation.snapshotMessagesVersion()
	if len(after) != len(before) {
		t.Fatalf("transcript length changed from %d to %d", len(before), len(after))
	}
}

// Cancellation belongs to the caller's context, not to a rescue classification:
// a caller must be able to keep propagating it unchanged.
func TestContextRescuePropagatesCancellation(t *testing.T) {
	prov := &rescueProvider{defaultReply: "## Goal\nfinish the task"}
	a := rescuePlannerAgent(t, prov)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := a.contextManager().planContextRescue(ctx, rescuePlanPolicy(), rescuePlanSource, rescuePlanCompacted, rescuePlanHard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if ContextRescueCode(err) != "" {
		t.Fatalf("cancellation must not be classified as a rescue failure: %q", ContextRescueCode(err))
	}
	if prov.calls() != 0 {
		t.Fatalf("summarizer calls = %d, want 0", prov.calls())
	}
}
