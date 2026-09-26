package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// countingSummaryProvider answers every summarizer request with one fixed digest
// and counts them, so a test can assert how many summaries a single
// provider-visible view paid for. It reports no usage, which leaves the
// estimator on its fallback scale and keeps the fixture's arithmetic stable.
type countingSummaryProvider struct {
	mu    sync.Mutex
	reply string
	calls int
}

func (p *countingSummaryProvider) Name() string { return "counting-summary" }

func (p *countingSummaryProvider) ContextBudgetPolicy() provider.ContextBudgetPolicy {
	return provider.ContextBudgetPolicy{WindowMode: provider.ContextWindowIndependent}
}

func (p *countingSummaryProvider) Stream(context.Context, provider.Request) (<-chan provider.Chunk, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	ch := make(chan provider.Chunk, 2)
	ch <- provider.Chunk{Type: provider.ChunkText, Text: p.reply}
	ch <- provider.Chunk{Type: provider.ChunkDone}
	close(ch)
	return ch, nil
}

func (p *countingSummaryProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// lowYieldWindow and lowYieldTurns describe a session whose pressure fold lands
// under the trigger while leaving less room than the fold's own headroom goal.
// The window is wide enough that the fold's 16% verbatim tail is a large share
// of the trigger, so a digest that only removes the older turns still leaves the
// result above the goal — the shape that re-enters maintenance on the next
// turn's own output.
const (
	lowYieldWindow = 50_000
	lowYieldTurns  = 32
)

func lowYieldSession() *Session {
	big := strings.Repeat("alpha beta gamma delta ", 200)
	sess := NewSession("sys")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "task"})
	for i := range lowYieldTurns {
		sess.Add(provider.Message{Role: provider.RoleAssistant, Content: fmt.Sprintf("step %d: %s", i, big)})
		sess.Add(provider.Message{Role: provider.RoleUser, Content: "continue"})
	}
	return sess
}

// lowYieldAgent builds the fixture and asserts the shape the tests below depend
// on: the view is above the trigger, below the ceiling, and its digest is large
// enough that the fold installs a real summary rather than falling through to
// the truncation rung. A shift in the estimator fails here with the numbers
// instead of silently turning every assertion below vacuous.
func lowYieldAgent(t *testing.T) (*Agent, *countingSummaryProvider) {
	t.Helper()
	prov := &countingSummaryProvider{reply: strings.Repeat("digest line of prose\n", 4000)}
	a := New(prov, tool.NewRegistry(), lowYieldSession(), Options{
		ContextWindow: lowYieldWindow, CompactRatio: 0.5, RecentKeep: 2, ArchiveDir: t.TempDir(),
	}, event.Discard)
	est, fold, hard := a.estimatedVisibleRequestTokens(a.modelVisibleMessages()), a.compactTrigger(), a.hardInputCeiling()
	if est < fold {
		t.Fatalf("fixture estimates %d tokens, under the %d trigger; it would not fold", est, fold)
	}
	if est >= hard {
		t.Fatalf("fixture estimates %d tokens, at or above the %d ceiling; it would take the overflow rung", est, hard)
	}
	return a, prov
}

// A fold that lands under the trigger but buys less room than its own goal has
// not recovered anything: the next turn's own output re-crosses the boundary it
// just paid to leave, and folding that again buys the same nothing. The latch is
// what stops the second payment — the view must be above the trigger and still
// latched when the repeat arrives, or the test proves nothing.
func TestLowYieldFoldLatchesInsteadOfRepayingTheSameView(t *testing.T) {
	a, prov := lowYieldAgent(t)
	if err := prepareContext(context.Background(), a, CompactionTriggerPressure); err != nil {
		t.Fatalf("first pressure fold: %v", err)
	}
	if prov.count() != 1 {
		t.Fatalf("first fold made %d summary calls, want 1", prov.count())
	}
	receipt := a.sess.compactionState.LastReceipt
	if receipt == nil || receipt.MaintenanceState != maintenanceStateLowYield {
		t.Fatalf("receipt = %+v, want a %s decision", receipt, maintenanceStateLowYield)
	}
	if receipt.HeadroomTokens >= a.maintenanceHeadroomGoal() {
		t.Fatalf("headroom %d reached the goal %d; the fixture is not a low-yield fold",
			receipt.HeadroomTokens, a.maintenanceHeadroomGoal())
	}
	if !a.sess.compaction.stuck {
		t.Fatal("a low-yield fold must latch the view it could not reclaim")
	}

	// Grow just enough to re-cross the trigger. This is the repeat the latch
	// exists to stop, so the growth must stay under the release threshold.
	latched := a.sess.compaction.stuckTokens
	release := latched + int(float64(lowYieldWindow)*maintenanceRetryGrowthRatio)
	big := strings.Repeat("word ", 200)
	for range 100 {
		a.sess.conversation.Add(provider.Message{Role: provider.RoleAssistant, Content: big})
		a.sess.conversation.Add(provider.Message{Role: provider.RoleUser, Content: "continue"})
		if est := a.estimatedVisibleRequestTokens(a.modelVisibleMessages()); est >= a.compactTrigger() {
			break
		}
	}
	est := a.estimatedVisibleRequestTokens(a.modelVisibleMessages())
	if est < a.compactTrigger() {
		t.Fatalf("fixture estimates %d tokens, under the %d trigger; the repeat would not fire", est, a.compactTrigger())
	}
	if est >= release {
		t.Fatalf("fixture grew to %d tokens, past the %d release threshold; the latch would not be under test", est, release)
	}

	if err := prepareContext(context.Background(), a, CompactionTriggerPressure); err != nil {
		t.Fatalf("repeat pressure fold: %v", err)
	}
	if prov.count() != 1 {
		t.Fatalf("the latched view paid for %d summaries, want the one it already bought", prov.count())
	}
	if !a.sess.compaction.stuck {
		t.Fatal("the latch must survive the repeat it suppressed")
	}
}

// Growth is what releases the latch: once the view has outgrown the fold that
// failed to reclaim it, there is a new fold boundary and the fold is worth
// paying for again.
func TestLowYieldLatchReleasesOnGrowth(t *testing.T) {
	a, prov := lowYieldAgent(t)
	if err := prepareContext(context.Background(), a, CompactionTriggerPressure); err != nil {
		t.Fatalf("first pressure fold: %v", err)
	}
	latched := a.sess.compaction.stuckTokens
	if latched <= 0 {
		t.Fatal("the latch must record the estimate its own fold ran on")
	}

	big := strings.Repeat("word ", 400)
	for range 100 {
		a.sess.conversation.Add(provider.Message{Role: provider.RoleAssistant, Content: big})
		a.sess.conversation.Add(provider.Message{Role: provider.RoleUser, Content: "continue"})
		est := a.estimatedVisibleRequestTokens(a.modelVisibleMessages())
		if est >= latched+int(float64(lowYieldWindow)*maintenanceRetryGrowthRatio) {
			break
		}
	}
	est := a.estimatedVisibleRequestTokens(a.modelVisibleMessages())
	if est < latched+int(float64(lowYieldWindow)*maintenanceRetryGrowthRatio) {
		t.Fatalf("fixture did not outgrow the latch: %d against %d", est, latched)
	}

	if err := prepareContext(context.Background(), a, CompactionTriggerPressure); err != nil {
		t.Fatalf("pressure fold after growth: %v", err)
	}
	if prov.count() != 2 {
		t.Fatalf("grown view made %d summary calls, want a fresh one", prov.count())
	}
}

// Overflow is the physical recovery path, not the ordinary one. The low-yield
// latch suppresses pressure retries below the ceiling; a view that has reached
// the ceiling must still be recovered, or the turn leaves with a request the
// provider rejects.
func TestOverflowBypassesTheLowYieldLatch(t *testing.T) {
	a, prov := lowYieldAgent(t)
	if err := prepareContext(context.Background(), a, CompactionTriggerPressure); err != nil {
		t.Fatalf("first pressure fold: %v", err)
	}
	if !a.sess.compaction.stuck {
		t.Fatal("the fixture did not latch; the bypass would be untested")
	}
	before := a.currentProjectionVersion()

	big := strings.Repeat("word ", 400)
	for range 100 {
		a.sess.conversation.Add(provider.Message{Role: provider.RoleAssistant, Content: big})
		a.sess.conversation.Add(provider.Message{Role: provider.RoleUser, Content: "continue"})
		if a.estimatedVisibleRequestTokens(a.modelVisibleMessages()) >= a.hardInputCeiling() {
			break
		}
	}
	if est, hard := a.estimatedVisibleRequestTokens(a.modelVisibleMessages()), a.hardInputCeiling(); est < hard {
		t.Fatalf("fixture did not reach the hard ceiling: %d < %d", est, hard)
	}

	if err := prepareContext(context.Background(), a, CompactionTriggerOverflow); err != nil {
		t.Fatalf("overflow recovery under a low-yield latch: %v", err)
	}
	if a.currentProjectionVersion() == before {
		t.Fatal("overflow recovery was suppressed by the low-yield latch")
	}
	if prov.count() < 2 {
		t.Fatalf("overflow recovery made %d summary calls, want it to pay for the ceiling", prov.count())
	}
}

// The classifier is the contract the receipt publishes; these are the seven
// outcomes and the boundary between "recovered" and "installed but no room".
func TestMaintenanceDecisionNamesEveryOutcome(t *testing.T) {
	const fold, hard = 10_000, 19_744
	cases := []struct {
		name string
		d    maintenanceDecision
		want string
	}{
		{"below the boundary", maintenanceDecision{Estimate: 5_000, Fold: fold, Hard: hard, Goal: 3_200}, maintenanceStateBelowBoundary},
		{"above the boundary, no projection", maintenanceDecision{Estimate: 12_000, Fold: fold, Hard: hard, Goal: 3_200}, maintenanceStateAboveBoundary},
		{"at the ceiling, no projection", maintenanceDecision{Estimate: hard, Fold: fold, Hard: hard, Goal: 3_200}, maintenanceStateAtCeiling},
		{"installed with room to spare", maintenanceDecision{Estimate: 12_000, Fold: fold, Hard: hard, Goal: 3_200, Result: 4_000, Applied: true}, maintenanceStateRecovered},
		{"installed without room", maintenanceDecision{Estimate: 12_000, Fold: fold, Hard: hard, Goal: 3_200, Result: 9_305, Applied: true}, maintenanceStateLowYield},
		{"rescued", maintenanceDecision{Estimate: hard, Fold: fold, Hard: hard, Goal: 3_200, Rescued: true}, maintenanceStateRescued},
		{"blocked", maintenanceDecision{Estimate: 12_000, Fold: fold, Hard: hard, Goal: 3_200, Blocked: true}, maintenanceStateBlocked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.d.State(); got != tc.want {
				t.Fatalf("state = %q, want %q", got, tc.want)
			}
		})
	}
}

// The headroom a decision reports is measured against the boundary the fold was
// taken under. A fold that lands above it reports negative headroom rather than
// a clamped zero, because "the next message re-enters maintenance" is the fact
// the number exists to carry.
func TestMaintenanceDecisionReportsHeadroomAndReduction(t *testing.T) {
	d := maintenanceDecision{Estimate: 20_000, Fold: 10_000, Hard: 19_744, Goal: 3_200, Result: 10_500, Applied: true}
	if got := d.Headroom(); got != -500 {
		t.Fatalf("headroom = %d, want -500", got)
	}
	if d.GoalMet() {
		t.Fatal("a fold that landed above its trigger has not met the goal")
	}
	if got, want := d.Reduction(), 0.475; got < want-0.001 || got > want+0.001 {
		t.Fatalf("reduction = %v, want ~%v", got, want)
	}

	// An unknown window cannot size a goal, and must not latch a session on a
	// boundary it never had.
	unknown := maintenanceDecision{Estimate: 20_000, Fold: 10_000, Result: 9_999, Applied: true}
	if !unknown.GoalMet() {
		t.Fatal("a decision with no measurable goal must fail open")
	}
}

// The four counters are the maintenance cost a hit rate cannot show. Each one
// must count the event it names, and the summary counter must catch every
// summary funnel rather than only the ordinary fold.
func TestMaintenanceCostCountsEverySummaryPath(t *testing.T) {
	a, _ := lowYieldAgent(t)
	if got := a.maintenanceCostSnapshot(); got != (MaintenanceCost{}) {
		t.Fatalf("a fresh session starts with cost %+v", got)
	}
	if err := prepareContext(context.Background(), a, CompactionTriggerPressure); err != nil {
		t.Fatalf("pressure fold: %v", err)
	}
	cost := a.maintenanceCostSnapshot()
	if cost.SummaryRequests != 1 {
		t.Fatalf("summary requests = %d, want 1", cost.SummaryRequests)
	}
	if cost.ProjectionInstalls != 1 {
		t.Fatalf("projection installs = %d, want 1", cost.ProjectionInstalls)
	}
	if cost.RescueCount != 0 || cost.RepeatBlocks != 0 {
		t.Fatalf("cost = %+v, want no rescue and no repeat block", cost)
	}
}

// ladderTranscript is rescueOverCeilingTranscript's shape with a tunable turn
// count, so a test can sit a session anywhere between the trigger and the
// ceiling instead of only past it.
func ladderTranscript(turns int) []provider.Message {
	msgs := []provider.Message{{Role: provider.RoleSystem, Content: "system rules"}}
	body := strings.Repeat("x", 2_200)
	for i := range turns {
		msgs = append(msgs,
			provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("question %d %s", i, body)},
			provider.Message{Role: provider.RoleAssistant, Content: fmt.Sprintf("answer %d %s", i, body)},
		)
	}
	return msgs
}

// The rescue rung is reachable only from the ceiling. A pressure fold that fails
// above the trigger but below the ceiling has not failed the transaction — the
// view still has a way out — so it must bar the view for this generation and
// return it, never carry the session into a continuation.
func TestTheLadderOnlyReachesRescueFromTheCeiling(t *testing.T) {
	msgs := ladderTranscript(20)
	prov := &rescueProvider{
		defaultReply: "## Goal\nfinish the pending refactor",
		failAt:       map[int]error{0: errors.New("summarizer unavailable")},
	}
	a := newRescueAgent(t, prov, msgs)
	calibrateRescueEstimate(a, msgs)
	fold, hard := a.compactTrigger(), a.hardInputCeiling()
	est := a.estimatedVisibleRequestTokens(a.modelVisibleMessages())
	if est < fold || est >= hard {
		t.Fatalf("fixture estimates %d tokens against trigger %d and ceiling %d", est, fold, hard)
	}

	prepared, err := prepareWithRescue(t, a, true)
	if err != nil {
		t.Fatalf("a below-ceiling fold must not surface an error: %v", err)
	}
	if errors.Is(err, ErrContextRescuePlanned) || prepared.Recovery != nil {
		t.Fatal("a view under the ceiling must never be carried into a continuation")
	}
	if got := a.maintenanceCostSnapshot().RescueCount; got != 0 {
		t.Fatalf("rescue count = %d below the ceiling, want 0", got)
	}
	if !a.ContextMaintenanceSnapshot().Blocked {
		t.Fatal("a failed fold must bar its own view so the generation does not pay for it twice")
	}
	if a.sess.compaction.stuck {
		t.Fatal("the low-yield latch belongs to folds that installed a projection, not to failed ones")
	}

	// At the ceiling the same ladder takes the rescue rung, because ordinary
	// compaction is no longer a way out.
	over := rescueOverCeilingTranscript()
	deep := newRescueAgent(t, &rescueProvider{
		defaultReply: "## Goal\nfinish the pending refactor\n\n## Pending & next step\nrun the package tests",
		failAt:       map[int]error{0: errors.New("summarizer unavailable")},
	}, over)
	calibrateRescueEstimate(deep, over)
	if est, ceiling := deep.estimatedVisibleRequestTokens(deep.modelVisibleMessages()), deep.hardInputCeiling(); est < ceiling {
		t.Fatalf("fixture estimates %d tokens, under the %d ceiling; the rescue rung would be unreachable", est, ceiling)
	}
	plan, err := prepareWithRescue(t, deep, true)
	if !errors.Is(err, ErrContextRescuePlanned) {
		t.Fatalf("err = %v, want ErrContextRescuePlanned", err)
	}
	if plan.Recovery == nil {
		t.Fatal("the ceiling rung must return the certified plan")
	}
	if got := deep.maintenanceCostSnapshot().RescueCount; got != 1 {
		t.Fatalf("rescue count = %d at the ceiling, want 1", got)
	}
}

// A certified rescue counts one rescue and installs nothing: a plan proposes a
// continuation, it does not rewrite the view it rejected. The same ladder
// without the opt-in never enters the rescue state at all.
func TestRescueIsTheLastRungAndCountsOnce(t *testing.T) {
	msgs := rescueOverCeilingTranscript()
	prov := &rescueProvider{
		defaultReply: "## Goal\nfinish the pending refactor\n\n## Pending & next step\nrun the package tests",
		failAt:       map[int]error{0: errors.New("summarizer unavailable")},
	}
	a := newRescueAgent(t, prov, msgs)
	calibrateRescueEstimate(a, msgs)

	prepared, err := prepareWithRescue(t, a, true)
	if !errors.Is(err, ErrContextRescuePlanned) {
		t.Fatalf("err = %v, want ErrContextRescuePlanned", err)
	}
	if prepared.Recovery == nil {
		t.Fatal("a planned rescue must return the plan")
	}
	cost := a.maintenanceCostSnapshot()
	if cost.RescueCount != 1 {
		t.Fatalf("rescue count = %d, want 1", cost.RescueCount)
	}
	if cost.ProjectionInstalls != 0 {
		t.Fatalf("projection installs = %d, want 0: a plan proposes, it does not install", cost.ProjectionInstalls)
	}
	if cost.SummaryRequests == 0 {
		t.Fatal("the rescue's own summarizer calls must be counted")
	}

	// The same ladder without the opt-in never enters the rescue state: it keeps
	// the existing truncation behaviour and records no rescue at all.
	plain := newRescueAgent(t, &rescueProvider{
		defaultReply: "## Goal\nfinish the pending refactor",
		failAt:       map[int]error{0: errors.New("summarizer unavailable")},
	}, rescueOverCeilingTranscript())
	calibrateRescueEstimate(plain, msgs)
	if _, err := prepareWithRescue(t, plain, false); errors.Is(err, ErrContextRescuePlanned) {
		t.Fatal("a transaction that did not opt in must never plan a rescue")
	}
	if got := plain.maintenanceCostSnapshot().RescueCount; got != 0 {
		t.Fatalf("the ordinary ladder recorded %d rescues, want 0", got)
	}
}
