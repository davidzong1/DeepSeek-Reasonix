package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// rescueWindow keeps every Part A bound legible in one place: the hard input
// ceiling is rescueWindow - protocolReserveTokens, and the scripted provider
// calibrates the agent at one token per request character.
const rescueWindow = 100_000

// rescueProvider is a scripted summarizer. Reply i answers call i; calls past
// the script fall back to defaultReply, so a test scripts only the call it is
// about. It reports the request's own character count as the prompt token
// count, which calibrates the agent at one token per request character.
type rescueProvider struct {
	mu            sync.Mutex
	replies       []string
	defaultReply  string
	failAt        map[int]error
	finishAt      map[int]string
	finishDefault string
	silent        bool // report no usage, leaving the token scale uncalibrated
	mutateAt      map[int]func()
	reqs          []provider.Request
}

func (p *rescueProvider) Name() string { return "rescue-scripted" }

// An independent completion window keeps the summarizer request out of the
// shared-window admission arithmetic: these tests are about the rescue
// contract, not about output budgeting.
func (p *rescueProvider) ContextBudgetPolicy() provider.ContextBudgetPolicy {
	return provider.ContextBudgetPolicy{WindowMode: provider.ContextWindowIndependent}
}

func (p *rescueProvider) Stream(_ context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	p.mu.Lock()
	idx := len(p.reqs)
	p.reqs = append(p.reqs, req)
	reply := p.defaultReply
	if idx < len(p.replies) {
		reply = p.replies[idx]
	}
	err := p.failAt[idx]
	finish := p.finishDefault
	if at, ok := p.finishAt[idx]; ok {
		finish = at
	}
	silent, mutate := p.silent, p.mutateAt[idx]
	p.mu.Unlock()
	if mutate != nil {
		mutate()
	}
	if err != nil {
		return nil, err
	}
	ch := make(chan provider.Chunk, 3)
	ch <- provider.Chunk{Type: provider.ChunkText, Text: reply}
	if !silent {
		chars, _, _ := requestCalibrationTextShape(req, sharedWindowInputPolicyOf(p))
		ch <- provider.Chunk{Type: provider.ChunkUsage, Usage: &provider.Usage{
			PromptTokens: int(chars), CompletionTokens: 1, TotalTokens: int(chars) + 1, FinishReason: finish,
		}}
	} else if finish != "" {
		ch <- provider.Chunk{Type: provider.ChunkUsage, Usage: &provider.Usage{FinishReason: finish}}
	}
	ch <- provider.Chunk{Type: provider.ChunkDone}
	close(ch)
	return ch, nil
}

func (p *rescueProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

func (p *rescueProvider) call(i int) provider.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reqs[i]
}

// newRescueAgent builds an agent over msgs without calibrating its token scale:
// the planner takes its token counts from the caller, so only tests that drive
// Prepare need a comparable pre-maintenance estimate.
func newRescueAgent(t *testing.T, prov provider.Provider, msgs []provider.Message) *Agent {
	t.Helper()
	sess := NewSession("")
	sess.Replace(msgs)
	return New(prov, tool.NewRegistry(), sess, Options{
		ContextWindow: rescueWindow, CompactRatio: 0.80, RecentKeep: 2, ArchiveDir: t.TempDir(),
	}, event.Discard)
}

// calibrateRescueEstimate seeds the one-token-per-character scale the scripted
// provider keeps reporting, so the pre-maintenance ceiling estimate is
// comparable with every measurement the rescue makes afterwards.
func calibrateRescueEstimate(a *Agent, msgs []provider.Message) {
	shape := a.requestCalibrationShape(provider.Request{Messages: msgs})
	a.setPromptTokenCalibration(int(shape.requestChars), shape)
}

// rescuePlannerAgent is the smallest frozen view the planner contract needs: a
// system prefix it must not summarize, and a main line it must.
func rescuePlannerAgent(t *testing.T, prov provider.Provider, body ...provider.Message) *Agent {
	t.Helper()
	msgs := append([]provider.Message{{Role: provider.RoleSystem, Content: "system rules"}}, body...)
	if len(body) == 0 {
		msgs = append(msgs, provider.Message{Role: provider.RoleUser, Content: "carry the task forward"})
	}
	return newRescueAgent(t, prov, msgs)
}

// rescuePlanInputs are the token counts the planner is handed on the real path:
// the admission estimate before and after a fold that removed nine percent of
// the request while leaving the view above the hard ceiling.
const (
	rescuePlanSource    = 110_000
	rescuePlanCompacted = 100_000
	rescuePlanHard      = 99_744
)

func rescuePlanPolicy() ContextPreparePolicy {
	return ContextPreparePolicy{Trigger: CompactionTriggerPressure, AllowContextRescue: true}
}

func planRescueForTest(t *testing.T, a *Agent, source, compacted, hard int) (ContextRecoveryPlan, error) {
	t.Helper()
	return a.contextManager().planContextRescue(context.Background(), rescuePlanPolicy(), source, compacted, hard)
}

func rescueStateOf(t *testing.T, a *Agent) CompactionState {
	t.Helper()
	a.sess.compactionMu.Lock()
	defer a.sess.compactionMu.Unlock()
	return a.sess.compactionState
}

// The two readings of "compression under 10%" mean opposite things. Only the
// reduction reading may trigger a rescue: a view that shrank by less than a
// tenth was not rescued by folding, while a view whose remainder is under a
// tenth was rescued extremely well and must never rotate the session.
func TestContextRescueEligibilityUsesReductionNotRemainder(t *testing.T) {
	cases := []struct {
		name                    string
		source, compacted, hard int
		wantRatio               float64
		wantEligible            bool
	}{
		{"zero reduction at the ceiling", 1000, 1000, 800, 0, true},
		{"fold grew the request", 1000, 1200, 800, -0.2, true},
		{"just under a tenth removed", 1000, 901, 800, 0.099, true},
		{"exactly a tenth removed", 1000, 900, 800, 0.100, false},
		{"more than a tenth removed", 1000, 800, 800, 0.200, false},
		{"compaction landed under the ceiling", 1000, 700, 800, 0.300, false},
		// The inverse reading at its sharpest: nine percent of the request
		// survives, so "compacted/source < 0.10" would fire, yet folding removed
		// ninety-one percent of it. Only the reduction reading is correct.
		{"tiny remainder from a productive fold", 100_000, 9_000, 8_000, 0.910, false},
		{"no source estimate", 0, 900, 800, 0, false},
		{"negative source estimate", -5, 900, 800, 0, false},
		{"no known window", 1000, 1000, 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ratio, eligible := contextRescueEligible(tc.source, tc.compacted, tc.hard)
			if eligible != tc.wantEligible {
				t.Fatalf("eligible = %v, want %v (ratio %v)", eligible, tc.wantEligible, ratio)
			}
			if !eligible {
				return
			}
			if math.Abs(ratio-tc.wantRatio) > 1e-9 {
				t.Fatalf("ratio = %v, want %v", ratio, tc.wantRatio)
			}
		})
	}
}

// The injected block bound is strict: the continuation message must be under
// contextRescueMaxBlockTokens, so a measurement of exactly the bound is
// rejected rather than accepted at the limit.
func TestContextRescueBlockBoundIsStrict(t *testing.T) {
	if !contextRescueBlockFits(contextRescueMaxBlockTokens - 1) {
		t.Errorf("a block just under %d must fit", contextRescueMaxBlockTokens)
	}
	if contextRescueBlockFits(contextRescueMaxBlockTokens) {
		t.Errorf("a block of exactly %d must be rejected", contextRescueMaxBlockTokens)
	}
	if contextRescueBlockFits(contextRescueMaxBlockTokens + 1) {
		t.Errorf("a block over %d must be rejected", contextRescueMaxBlockTokens)
	}
	if contextRescueBlockFits(0) {
		t.Error("an unmeasured block must not be treated as fitting")
	}
}

func TestContextRescuePlansABoundedContinuation(t *testing.T) {
	const digest = "## Goal\nfinish the pending refactor\n\n## Pending & next step\nrun the package tests"
	prov := &rescueProvider{defaultReply: digest}
	a := rescuePlannerAgent(t, prov)
	canonical, _ := a.sess.conversation.snapshotMessagesVersion()

	plan, err := planRescueForTest(t, a, rescuePlanSource, rescuePlanCompacted, rescuePlanHard)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Trigger != CompactionTriggerPressure {
		t.Errorf("trigger = %q, want %q", plan.Trigger, CompactionTriggerPressure)
	}
	if plan.SourceTokens != rescuePlanSource || plan.CompactedTokens != rescuePlanCompacted {
		t.Errorf("tokens = (%d, %d), want (%d, %d)", plan.SourceTokens, plan.CompactedTokens, rescuePlanSource, rescuePlanCompacted)
	}
	wantRatio := float64(rescuePlanSource-rescuePlanCompacted) / float64(rescuePlanSource)
	if math.Abs(plan.ReductionRatio-wantRatio) > 1e-9 {
		t.Errorf("reduction ratio = %v, want %v", plan.ReductionRatio, wantRatio)
	}
	if plan.Reason == "" {
		t.Error("plan must record why the rescue fired")
	}
	if plan.Summary != digest {
		t.Errorf("summary = %q, want the summarizer reply", plan.Summary)
	}
	if plan.SummaryHash == "" || plan.SummaryHash != summaryContentHash(plan.Summary) {
		t.Errorf("summary hash = %q, want the hash of the briefing", plan.SummaryHash)
	}
	if !contextRescueBlockFits(plan.BlockTokens) {
		t.Errorf("block = %d tokens, must be under %d", plan.BlockTokens, contextRescueMaxBlockTokens)
	}
	if plan.SummaryTokens <= 0 || plan.SummaryTokens > plan.BlockTokens {
		t.Errorf("summary tokens = %d, block tokens = %d", plan.SummaryTokens, plan.BlockTokens)
	}
	if !IsContextRescueMessage(plan.Message) {
		t.Fatalf("continuation message is not a rescue briefing: %q", plan.Message.Content)
	}
	if plan.Message.Role != provider.RoleUser || plan.Message.Origin != provider.MessageOriginHost {
		t.Errorf("continuation message = role %q origin %q, want a host-generated user message", plan.Message.Role, plan.Message.Origin)
	}
	if !strings.Contains(plan.Message.Content, digest) {
		t.Error("continuation message must carry the briefing")
	}
	for _, want := range []string{
		"trigger=pressure",
		"source_tokens=110000",
		"compacted_tokens=100000",
		"summary_sha=" + plan.SummaryHash,
	} {
		if !strings.Contains(plan.Message.Content, want) {
			t.Errorf("continuation message is missing %q", want)
		}
	}
	state := rescueStateOf(t, a)
	if plan.Generation != state.Generation {
		t.Errorf("generation = %d, want %d", plan.Generation, state.Generation)
	}
	if _, version := a.sess.conversation.snapshotMessagesVersion(); plan.TranscriptVersion != version {
		t.Errorf("transcript version = %d, want %d", plan.TranscriptVersion, version)
	}
	if plan.ProjectionVersion != state.Projection.ProjectionVersion {
		t.Errorf("projection version = %d, want %d", plan.ProjectionVersion, state.Projection.ProjectionVersion)
	}
	if plan.CoveredCount != len(canonical) {
		t.Errorf("covered count = %d, want %d", plan.CoveredCount, len(canonical))
	}
	if plan.DedupKey == "" {
		t.Error("plan must carry a lineage re-entry key")
	}
	if plan.PendingTools != 0 {
		t.Errorf("pending tools = %d, want 0", plan.PendingTools)
	}
	if plan.Spans != 1 {
		t.Errorf("spans = %d, want 1", plan.Spans)
	}
	if got := a.currentProjectionVersion(); got != 0 {
		t.Errorf("projection version = %d, want 0: a plan is a proposal and must not install anything", got)
	}
	if prov.calls() != 1 {
		t.Errorf("summarizer calls = %d, want 1", prov.calls())
	}
	if body := prov.call(0).Messages[len(prov.call(0).Messages)-1].Content; !strings.Contains(body, "cross-session continuation payload") {
		t.Error("the briefing must be requested with the continuation contract")
	}
}

// One frozen view is one continuation: the dedup key identifies the view, not
// the briefing text, so a nondeterministic summarizer cannot defeat re-entry
// protection.
func TestContextRescueDedupKeyIdentifiesTheFrozenView(t *testing.T) {
	prov := &rescueProvider{replies: []string{"first briefing", "second briefing"}}
	a := rescuePlannerAgent(t, prov)

	first, err := planRescueForTest(t, a, rescuePlanSource, rescuePlanCompacted, rescuePlanHard)
	if err != nil {
		t.Fatalf("first plan: %v", err)
	}
	second, err := planRescueForTest(t, a, rescuePlanSource, rescuePlanCompacted, rescuePlanHard)
	if err != nil {
		t.Fatalf("second plan: %v", err)
	}
	if first.DedupKey != second.DedupKey {
		t.Fatalf("dedup key moved across an identical view: %q -> %q", first.DedupKey, second.DedupKey)
	}
	if first.SummaryHash == second.SummaryHash {
		t.Fatal("fixture must produce different briefings to prove the key ignores them")
	}
}

// A fold that actually reclaimed the request is the existing ladder's business.
// The planner must say so without paying for a summarizer call.
func TestContextRescueSkipsAProductiveFold(t *testing.T) {
	prov := &rescueProvider{defaultReply: "unused"}
	a := rescuePlannerAgent(t, prov)

	_, err := planRescueForTest(t, a, 100_000, 9_000, 8_000)
	if !errors.Is(err, errContextRescueNotEligible) {
		t.Fatalf("err = %v, want not-eligible", err)
	}
	if prov.calls() != 0 {
		t.Fatalf("summarizer calls = %d, want 0 for an ineligible view", prov.calls())
	}
	if code := ContextRescueCode(errors.New("unrelated")); code != "" {
		t.Fatalf("code for an unrelated error = %q, want empty", code)
	}
}

func TestContextRescueRejectsAFailedBriefing(t *testing.T) {
	prov := &rescueProvider{
		defaultReply: "unused",
		failAt:       map[int]error{0: errors.New("summarizer unavailable")},
	}
	a := rescuePlannerAgent(t, prov)

	plan, err := planRescueForTest(t, a, rescuePlanSource, rescuePlanCompacted, rescuePlanHard)
	if err == nil {
		t.Fatal("a failed briefing must not produce a plan")
	}
	if code := ContextRescueCode(err); code != ContextRescueSummaryFailed {
		t.Fatalf("code = %q, want %q (%v)", code, ContextRescueSummaryFailed, err)
	}
	if plan.Message.Content != "" || plan.Summary != "" || plan.DedupKey != "" {
		t.Fatal("a rejected rescue must return a zero plan")
	}
}

func TestContextRescueRejectsATruncatedBriefing(t *testing.T) {
	prov := &rescueProvider{defaultReply: "partial briefing", finishDefault: "length"}
	a := rescuePlannerAgent(t, prov,
		provider.Message{Role: provider.RoleUser, Content: "first request"},
		provider.Message{Role: provider.RoleAssistant, Content: "first answer"},
		provider.Message{Role: provider.RoleUser, Content: "second request"},
		provider.Message{Role: provider.RoleAssistant, Content: "second answer"},
	)

	if _, err := planRescueForTest(t, a, rescuePlanSource, rescuePlanCompacted, rescuePlanHard); ContextRescueCode(err) != ContextRescueSummaryTruncated {
		t.Fatalf("code = %q, want %q (%v)", ContextRescueCode(err), ContextRescueSummaryTruncated, err)
	}
}

func TestContextRescueRejectsAnEmptyBriefing(t *testing.T) {
	prov := &rescueProvider{defaultReply: "   \n\t "}
	a := rescuePlannerAgent(t, prov)

	plan, err := planRescueForTest(t, a, rescuePlanSource, rescuePlanCompacted, rescuePlanHard)
	if err == nil {
		t.Fatal("an empty briefing must not produce a plan")
	}
	if code := ContextRescueCode(err); code != ContextRescueSummaryFailed {
		t.Fatalf("code = %q, want %q (%v)", code, ContextRescueSummaryFailed, err)
	}
	if plan.Summary != "" {
		t.Fatalf("summary = %q, want empty", plan.Summary)
	}
}

// An over-budget briefing gets exactly one bounded rewrite. When the rewrite
// fits, the plan uses it.
func TestContextRescueSqueezesAnOverBudgetBriefingOnce(t *testing.T) {
	const rewritten = "## Goal\nfinished the refactor\n\n## Pending & next step\nrun the tests"
	prov := &rescueProvider{replies: []string{strings.Repeat("y", 14_000)}, defaultReply: rewritten}
	a := rescuePlannerAgent(t, prov)

	plan, err := planRescueForTest(t, a, rescuePlanSource, rescuePlanCompacted, rescuePlanHard)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if prov.calls() != 2 {
		t.Fatalf("summarizer calls = %d, want 2 (briefing plus one rewrite)", prov.calls())
	}
	if !contextRescueBlockFits(plan.BlockTokens) {
		t.Fatalf("block = %d tokens, must be under %d", plan.BlockTokens, contextRescueMaxBlockTokens)
	}
	if plan.Summary != rewritten {
		t.Fatalf("summary = %q, want the rewritten briefing", plan.Summary)
	}
	if plan.SummaryHash != summaryContentHash(rewritten) {
		t.Fatal("the plan must fingerprint the briefing it actually carries")
	}
	last := prov.call(1).Messages[len(prov.call(1).Messages)-1].Content
	if !strings.Contains(last, "too long to serve as a cross-session continuation payload") {
		t.Fatal("the second call must be the bounded rewrite, not a fresh summary")
	}
}

// A rewrite that still does not fit is reported. The briefing is never trimmed
// into something that would silently drop standing constraints, and the session
// is never rotated.
func TestContextRescueReportsAnOverBudgetBriefingWithoutTrimming(t *testing.T) {
	prov := &rescueProvider{defaultReply: strings.Repeat("y", 14_000)}
	a := rescuePlannerAgent(t, prov)

	plan, err := planRescueForTest(t, a, rescuePlanSource, rescuePlanCompacted, rescuePlanHard)
	if err == nil {
		t.Fatal("an unfittable briefing must not produce a plan")
	}
	if code := ContextRescueCode(err); code != ContextRescueOverBudget {
		t.Fatalf("code = %q, want %q (%v)", code, ContextRescueOverBudget, err)
	}
	if prov.calls() != 2 {
		t.Fatalf("summarizer calls = %d, want the rewrite to be a single bounded attempt", prov.calls())
	}
	if plan.BlockTokens != 0 || plan.Message.Content != "" {
		t.Fatal("a rejected rescue must return a zero plan")
	}
}

// The bound is only meaningful on a scale that has been measured. Without a
// calibrated estimator the rescue refuses rather than certify a block whose
// real size is unknown.
func TestContextRescueFailsClosedWithoutACalibratedEstimate(t *testing.T) {
	prov := &rescueProvider{defaultReply: "## Goal\nfinish the task", silent: true}
	a := rescuePlannerAgent(t, prov)

	plan, err := planRescueForTest(t, a, rescuePlanSource, rescuePlanCompacted, rescuePlanHard)
	if err == nil {
		t.Fatal("an uncalibrated token scale must not produce a plan")
	}
	if code := ContextRescueCode(err); code != ContextRescueUntrustedEstimate {
		t.Fatalf("code = %q, want %q (%v)", code, ContextRescueUntrustedEstimate, err)
	}
	if plan.Message.Content != "" {
		t.Fatal("a rejected rescue must return a zero plan")
	}
}

// A briefing describes a frozen view. If the transcript moves while it is being
// written, the digest no longer describes what the caller would rotate away
// from, so it is discarded.
func TestContextRescueRejectsAViewThatMovedMidBriefing(t *testing.T) {
	prov := &rescueProvider{defaultReply: "## Goal\nfinish the task"}
	a := rescuePlannerAgent(t, prov,
		provider.Message{Role: provider.RoleUser, Content: "first request"},
		provider.Message{Role: provider.RoleAssistant, Content: "first answer"},
	)
	prov.mutateAt = map[int]func(){
		0: func() {
			current, _ := a.sess.conversation.snapshotMessagesVersion()
			appended := append(append([]provider.Message(nil), current...),
				provider.Message{Role: provider.RoleUser, Content: "typed while the briefing ran"})
			a.sess.conversation.Replace(appended)
		},
	}

	plan, err := planRescueForTest(t, a, rescuePlanSource, rescuePlanCompacted, rescuePlanHard)
	if err == nil {
		t.Fatal("a view that moved during generation must not produce a plan")
	}
	if code := ContextRescueCode(err); code != ContextRescueStaleTranscript {
		t.Fatalf("code = %q, want %q (%v)", code, ContextRescueStaleTranscript, err)
	}
	if plan.DedupKey != "" {
		t.Fatal("a rejected rescue must return a zero plan")
	}
}

// Tool calls with no recorded result have an unknown outcome. The briefing must
// say so, because a replay would repeat a side effect the previous session may
// already have applied.
func TestContextRescueMarksUnfinishedToolCallsUnknown(t *testing.T) {
	prov := &rescueProvider{defaultReply: "## Goal\nfinish the task"}
	a := rescuePlannerAgent(t, prov,
		provider.Message{Role: provider.RoleUser, Content: "run the migration"},
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "call-done", Name: "read_file", Arguments: `{"path":"a"}`},
		}},
		provider.Message{Role: provider.RoleTool, ToolCallID: "call-done", Name: "read_file", Content: "contents"},
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "call-pending", Name: "bash", Arguments: `{"cmd":"migrate"}`},
		}},
	)

	plan, err := planRescueForTest(t, a, rescuePlanSource, rescuePlanCompacted, rescuePlanHard)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.PendingTools != 1 {
		t.Fatalf("pending tools = %d, want 1", plan.PendingTools)
	}
	content := plan.Message.Content
	if !strings.Contains(content, "## Pending tool calls (do not replay)") {
		t.Fatal("the briefing must carry the pending-tool section")
	}
	if !strings.Contains(content, "call-pending") {
		t.Fatal("the unfinished call must be named")
	}
	if strings.Contains(content, "call-done") {
		t.Fatal("a call with a recorded result is not pending")
	}
}

// An over-length fold falls back to the replay-safe fragment and tree-reduce
// path, and the plan reports the requests that actually produced it.
func TestContextRescueFallsBackToTheFragmentPath(t *testing.T) {
	// The whole-region briefing is refused with a provider overflow; the
	// fragment path is exactly the answer to that.
	prov := &rescueProvider{
		defaultReply: "## Goal\nfinish the task",
		failAt: map[int]error{0: &provider.ContextLimitError{
			APIError:     &provider.APIError{Provider: "rescue-scripted", Status: 400, Body: "context length"},
			WindowTokens: rescueWindow, PromptTokens: rescueWindow + 1, CompletionTokens: 8_000,
		}},
	}
	region := make([]provider.Message, 0, 60)
	for i := range 30 {
		region = append(region,
			provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("question %d %s", i, strings.Repeat("q", 1_200))},
			provider.Message{Role: provider.RoleAssistant, Content: fmt.Sprintf("answer %d %s", i, strings.Repeat("a", 1_200))},
		)
	}
	a := rescuePlannerAgent(t, prov, region...)

	plan, err := planRescueForTest(t, a, rescuePlanSource, rescuePlanCompacted, rescuePlanHard)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Spans < 2 {
		t.Fatalf("spans = %d, want the fragment/tree-reduce path to have been used", plan.Spans)
	}
	if prov.calls() < 3 {
		t.Fatalf("summarizer calls = %d, want fragments plus a merge", prov.calls())
	}
	if !contextRescueBlockFits(plan.BlockTokens) {
		t.Fatalf("block = %d tokens, must be under %d", plan.BlockTokens, contextRescueMaxBlockTokens)
	}
}
