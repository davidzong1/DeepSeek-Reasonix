package agent

import (
	"log/slog"
	"sync/atomic"
)

// The states one maintenance decision can end in. They are finer than the
// receipt's applied|blocked|failed status, which existing consumers switch on: a
// fold that installed a projection but bought no room is "applied" and still not
// recovered, and the next message re-crosses the trigger it just paid to leave.
// Naming that state is what lets the ordinary path stop instead of paying for
// the same view again.
const (
	maintenanceStateBelowBoundary = "below_boundary"
	maintenanceStateAboveBoundary = "above_boundary"
	maintenanceStateAtCeiling     = "at_ceiling"
	maintenanceStateRecovered     = "recovered"
	maintenanceStateLowYield      = "low_yield"
	maintenanceStateRescued       = "rescued"
	maintenanceStateBlocked       = "blocked"
)

// maintenanceDecision is everything the classifier reads: the provider-visible
// estimate that entered maintenance, the trigger and ceiling in force, the
// headroom goal, and what the transaction did. It is a value rather than a set
// of receipt fields, so the state names can be tested without a session.
type maintenanceDecision struct {
	Estimate int
	Fold     int
	Hard     int
	// Goal is the room the fold must leave to count as recovered. The caller
	// supplies it, so the classifier stays a value.
	Goal int
	// Result is the installed view's estimate. Only meaningful with Applied.
	Result int
	// Applied reports that this transaction installed a projection.
	Applied bool
	// Rescued reports that control passed to the continuation rescue.
	Rescued bool
	// Blocked reports that this generation is barred from another summary.
	Blocked bool
}

// Headroom is how much new content the installed view absorbs before the
// ordinary trigger is crossed again. Negative means the fold landed above the
// boundary it was taken to reach, so the next message re-enters maintenance
// immediately — the repeat-compaction shape this goal exists to name.
func (d maintenanceDecision) Headroom() int {
	if d.Fold <= 0 || !d.Applied {
		return 0
	}
	return d.Fold - d.Result
}

// Reduction is the share of the request the fold removed, on the same scale
// context rescue tests its eligibility against.
func (d maintenanceDecision) Reduction() float64 {
	if d.Estimate <= 0 || !d.Applied {
		return 0
	}
	return float64(d.Estimate-d.Result) / float64(d.Estimate)
}

// GoalMet reports whether the installed view can absorb another turn's worth of
// content before maintenance is due again. It fails open when the policy cannot
// size a goal: a session that cannot measure its window must not be latched
// forever on a boundary it never had.
func (d maintenanceDecision) GoalMet() bool {
	if d.Fold <= 0 || d.Goal <= 0 {
		return true
	}
	return d.Headroom() >= d.Goal
}

// State names the decision. Order matters: a rescued transaction is rescued even
// when the same view is also barred from another summary, and an installed
// projection is judged on the room it bought rather than on its status alone.
func (d maintenanceDecision) State() string {
	switch {
	case d.Rescued:
		return maintenanceStateRescued
	case d.Blocked:
		return maintenanceStateBlocked
	case d.Applied && d.GoalMet():
		return maintenanceStateRecovered
	case d.Applied:
		return maintenanceStateLowYield
	case d.Hard > 0 && d.Estimate >= d.Hard:
		return maintenanceStateAtCeiling
	case d.Fold > 0 && d.Estimate >= d.Fold:
		return maintenanceStateAboveBoundary
	default:
		return maintenanceStateBelowBoundary
	}
}

// maintenanceSpend is this session's cumulative maintenance cost. A hit rate
// alone cannot tell "the state machine settled" from "it settled by folding
// every turn"; these counters are the difference. The two latches the classifier
// produces are counted here too, because they are the two states that mean the
// ladder did not finish its work.
type maintenanceSpend struct {
	summaryRequests    atomic.Int64
	projectionInstalls atomic.Int64
	rescueCount        atomic.Int64
	repeatBlocks       atomic.Int64
}

// MaintenanceCost is the readable snapshot of maintenanceSpend: this session's
// cumulative maintenance spend, which a hit rate alone cannot show.
type MaintenanceCost struct {
	SummaryRequests    int
	ProjectionInstalls int
	RescueCount        int
	RepeatBlocks       int
}

// noteSummaryRequest counts one summarizer request. Every summary funnel —
// ordinary fold, transcript form, fragment path, and continuation rescue —
// reaches the provider through runSummaryRequest, so this is the one place the
// session's summary spend can be counted without missing a path.
func (a *Agent) noteSummaryRequest() {
	if a == nil {
		return
	}
	a.sess.compaction.spend.summaryRequests.Add(1)
}

// noteProjectionInstall counts one installed provider-visible projection. Only
// the two install sites call it, matching noteProjectionRewrite: a receipt that
// is merely re-emitted republishes an install that already happened.
func (a *Agent) noteProjectionInstall() {
	if a == nil {
		return
	}
	a.sess.compaction.spend.projectionInstalls.Add(1)
}

// noteMaintenanceDecision counts one transaction's terminal state. Only spend is
// incremented here; the receipt carries the per-decision detail.
func (a *Agent) noteMaintenanceDecision(state string) {
	if a == nil {
		return
	}
	switch state {
	case maintenanceStateRescued:
		a.sess.compaction.spend.rescueCount.Add(1)
	case maintenanceStateBlocked:
		a.sess.compaction.spend.repeatBlocks.Add(1)
	}
}

// maintenanceCostSnapshot reads the counters as one value so a caller cannot
// pair a count with a decision it did not come from.
func (a *Agent) maintenanceCostSnapshot() MaintenanceCost {
	if a == nil {
		return MaintenanceCost{}
	}
	return MaintenanceCost{
		SummaryRequests:    int(a.sess.compaction.spend.summaryRequests.Load()),
		ProjectionInstalls: int(a.sess.compaction.spend.projectionInstalls.Load()),
		RescueCount:        int(a.sess.compaction.spend.rescueCount.Load()),
		RepeatBlocks:       int(a.sess.compaction.spend.repeatBlocks.Load()),
	}
}

// releaseMaintenanceLatch clears a low-yield latch once the view it bars is no
// longer the decision that set it. Growth is that signal: a view that has
// outgrown the latched fold has a new foldable region, so re-folding it is no
// longer the same decision paid twice. A view that has only gained the message
// which re-crossed the trigger is that same decision, and folding it again buys
// the same nothing — the repeat this latch exists to stop. A latch with no
// recorded estimate (a legacy sidecar) fails open, and the hard ceiling bypasses
// the latch entirely, so a session that never grows is still recovered.
func (a *Agent) releaseMaintenanceLatch(inputHash string, est int) {
	if a == nil || !a.sess.compaction.stuck || a.sess.compaction.stuckInputHash == inputHash {
		return
	}
	if !a.maintenanceGrowthDue(a.sess.compaction.stuckTokens, est) {
		return
	}
	a.sess.compaction.stuck = false
	a.sess.compaction.stuckInputHash = ""
	a.sess.compaction.stuckTokens = 0
	a.sess.compaction.consecutive = 0
}

// maintenanceHeadroomGoal is the room a successful fold must leave. It is the
// recent-tail budget: the fold deliberately keeps that much verbatim history, so
// a view with less room than that re-crosses the trigger on the next turn's own
// tool output — the fold would have paid a summary to buy nothing. An unknown
// window has no tail budget and therefore no goal.
func (a *Agent) maintenanceHeadroomGoal() int {
	if a == nil {
		return 0
	}
	return a.recentTailBudget()
}

// maintenanceDecisionFor builds the decision for a projection this transaction
// installed, from the boundaries it was taken under.
func (a *Agent) maintenanceDecisionFor(sourceTokens, resultTokens, fold, hard int) maintenanceDecision {
	return maintenanceDecision{
		Estimate: sourceTokens, Fold: fold, Hard: hard,
		Goal: a.maintenanceHeadroomGoal(), Result: resultTokens, Applied: true,
	}
}

// settleMaintenanceFold decides what a fold that landed inside its boundary
// leaves behind. Meeting the headroom goal clears every latch, because the next
// request genuinely has room. Falling short of it latches the view as low-yield
// so the ordinary path stops re-paying for the same fold boundary; the latch is
// released by growth, so new foldable content still retries.
//
// Manual compaction is a user request and never latches. Overflow keeps its own
// ladder: it is the physical recovery path, and refusing it below the ceiling is
// what would strand a turn.
func (a *Agent) settleMaintenanceFold(policy ContextPreparePolicy, sourceTokens, resultTokens, fold, hard int) {
	if a == nil {
		return
	}
	decision := a.maintenanceDecisionFor(sourceTokens, resultTokens, fold, hard)
	if !a.lowYieldLatch || policy.Trigger != CompactionTriggerPressure || decision.GoalMet() {
		a.resetCompactionProgress()
		return
	}
	a.latchLowYield(decision)
}

// latchLowYield records that a fold installed a projection and still left the
// view without the room the decision was taken for. The estimate it records is
// the source the fold itself ran on, which is what maintenanceGrowthDue compares
// against; the hard ceiling still bypasses the latch entirely.
func (a *Agent) latchLowYield(decision maintenanceDecision) {
	if a == nil {
		return
	}
	a.sess.compaction.stuck = true
	a.sess.compaction.stuckInputHash = a.contextMaintenanceInputHash(a.modelVisibleMessages())
	a.sess.compaction.stuckTokens = decision.Estimate
	a.sess.compaction.consecutive++
	slog.Info("agent: maintenance fold bought no headroom",
		"headroom", decision.Headroom(), "goal", decision.Goal,
		"reduction", decision.Reduction(), "trigger", decision.Fold, "result", decision.Result)
}
