package team

import (
	"fmt"
	"strings"
)

// CacheTurnTotals is the per-turn cost ledger of one report: the plan's
// `input_tokens_per_turn`, `miss_tokens_per_request` and `cache_miss_tokens_per_request`
// family, expressed on the only denominator a member record carries — the
// writer's own logical turn identity.
//
// A turn is counted only when the sample carried one. Samples without a turn
// identity are counted apart, because dividing by a turn count that omits them
// would understate every per-turn figure in the same direction.
type CacheTurnTotals struct {
	// Turns counts distinct turn identities the samples carried.
	Turns int `json:"turns"`
	// RequestsWithoutTurn counts samples that carried no turn identity. They are
	// excluded from every per-turn figure and disclosed here.
	RequestsWithoutTurn int `json:"requests_without_turn"`
	Requests            int `json:"requests"`
	HitTokens           int `json:"hit_tokens"`
	MissTokens          int `json:"miss_tokens"`
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
}

// HitTokensPerTurn and the three companions report a per-turn figure only when
// there is a turn to divide by: a report over samples with no turn identity has
// no per-turn cost, which is not a cost of zero.
func (t CacheTurnTotals) HitTokensPerTurn() (float64, bool) { return t.perTurn(t.HitTokens) }

// MissTokensPerTurn is the plan's headline cost figure: how much uncached prompt
// one logical turn costs. It is the number a cache optimization must move.
func (t CacheTurnTotals) MissTokensPerTurn() (float64, bool) { return t.perTurn(t.MissTokens) }

// PromptTokensPerTurn is the plan's `input_tokens_per_turn`.
func (t CacheTurnTotals) PromptTokensPerTurn() (float64, bool) { return t.perTurn(t.PromptTokens) }

// RequestsPerTurn reports how many provider requests one logical turn cost.
func (t CacheTurnTotals) RequestsPerTurn() (float64, bool) { return t.perTurn(t.Requests) }

// CompletionTokensPerTurn is the plan's output-cost companion.
func (t CacheTurnTotals) CompletionTokensPerTurn() (float64, bool) {
	return t.perTurn(t.CompletionTokens)
}

func (t CacheTurnTotals) perTurn(value int) (float64, bool) {
	if t.Turns <= 0 {
		return 0, false
	}
	return float64(value) / float64(t.Turns), true
}

// CacheMaintenanceCost is the plan's compaction-cost telemetry, in the only form
// this dataset can support. It is a count of requests that carried a maintenance
// reason, not a count of maintenance operations: a fold is announced on the
// request that follows it, so a turn can be charged for a fold the previous turn
// performed. The off-by-one is stated here and in the report because a reader
// who assumed the counts were operations would read every figure wrong.
type CacheMaintenanceCost struct {
	// RewriteRequests counts samples whose prefix moved because the turn rewrote
	// its own provider-visible content.
	RewriteRequests int `json:"rewrite_requests"`
	// StructuralRequests counts samples whose framing moved — system prompt, tool
	// surface or session-context tail.
	StructuralRequests int `json:"structural_requests"`
	// Rotations counts cold-prefix samples that opened a session other than the
	// writer's first: a context rescue rotated the member onto a fresh session.
	Rotations int `json:"rotations"`
	// ColdStartMissTokens is the plan's `cold_start_miss_tokens`: the uncached
	// prompt every cold prefix paid, including each rotation's.
	ColdStartMissTokens int `json:"cold_start_miss_tokens"`
	// FirstSessionColdMissTokens is the subset that belongs to the writer's own
	// first session, which a rescue did not cause.
	FirstSessionColdMissTokens int `json:"first_session_cold_miss_tokens"`
	// FinishReasons is the histogram of provider finish reasons over the scoped
	// samples. It is a completion-outcome signal, never a task-quality verdict:
	// this dataset carries no task result.
	FinishReasons []string `json:"finish_reasons,omitempty"`
}

// RewriteRequestsPerTurn and RotationsPer100Turns are the plan's two rate forms.
// Each reports false when the report has no turn to divide by.
func (c CacheMaintenanceCost) RewriteRequestsPerTurn(turns int) (float64, bool) {
	return perTurns(c.RewriteRequests, turns)
}

// RotationsPer100Turns is the plan's `rescue_count_per_100_turns`.
func (c CacheMaintenanceCost) RotationsPer100Turns(turns int) (float64, bool) {
	if turns <= 0 {
		return 0, false
	}
	return float64(c.Rotations) * 100 / float64(turns), true
}

func perTurns(value, turns int) (float64, bool) {
	if turns <= 0 {
		return 0, false
	}
	return float64(value) / float64(turns), true
}

// cacheTurnReport folds one report's scoped samples into the per-turn ledger and
// the maintenance cost. It reads the samples the report already classified, so
// the two views cannot disagree about which samples they describe.
type cacheTurnReport struct {
	turns       CacheTurnTotals
	maintenance CacheMaintenanceCost
	seenTurns   map[string]bool
	finish      map[string]int
}

func newCacheTurnReport() *cacheTurnReport {
	return &cacheTurnReport{seenTurns: map[string]bool{}, finish: map[string]int{}}
}

// add files one received sample into the per-turn ledger and the maintenance
// cost. cause is the sample's already-computed miss cause, passed in so the two
// classifications read one verdict rather than deriving it twice.
func (r *cacheTurnReport) add(rec MemberCacheRequest, cause CacheMissCause) {
	r.turns.Requests++
	r.turns.HitTokens += rec.CacheHitTokens
	r.turns.MissTokens += rec.CacheMissTokens
	r.turns.PromptTokens += rec.ContextPromptTokens
	r.turns.CompletionTokens += rec.CompletionTokens
	if turn := strings.TrimSpace(rec.TurnID); turn != "" {
		if !r.seenTurns[turn] {
			r.seenTurns[turn] = true
		}
	} else {
		r.turns.RequestsWithoutTurn++
	}
	if reason := strings.TrimSpace(rec.FinishReason); reason != "" {
		r.finish[reason]++
	}
	switch cause {
	case CacheCauseRewrite:
		r.maintenance.RewriteRequests++
	case CacheCauseStructural:
		r.maintenance.StructuralRequests++
	case CacheCauseColdPrefix:
		r.maintenance.ColdStartMissTokens += rec.CacheMissTokens
		if rec.SessionOrdinal > 1 {
			r.maintenance.Rotations++
		} else {
			r.maintenance.FirstSessionColdMissTokens += rec.CacheMissTokens
		}
	}
}

// result renders the folded ledger. Turns is the distinct-turn count, so a turn
// that cost three requests is one turn and three requests.
func (r *cacheTurnReport) result() (CacheTurnTotals, CacheMaintenanceCost) {
	turns := r.turns
	turns.Turns = len(r.seenTurns)
	maintenance := r.maintenance
	maintenance.FinishReasons = finishReasonHistogram(r.finish)
	return turns, maintenance
}

// finishReasonHistogram renders the finish-reason counts as "value=count" in key
// order, so two reports over the same samples print the same line.
func finishReasonHistogram(counts map[string]int) []string {
	out := make([]string, 0, len(counts))
	for _, reason := range sortedKeys(counts) {
		out = append(out, fmt.Sprintf("%s=%d", reason, counts[reason]))
	}
	return out
}

// CacheReportUnobservable names, in the report itself, the metrics the plan asks
// for that this dataset cannot produce. Publishing the gaps beside the numbers is
// what keeps a reader from assuming an absent figure was a zero one, and it is
// the list a later reader checks before adding a metric.
func CacheReportUnobservable() []string {
	return []string{
		"task quality: a member usage record carries no task result, so no quality_success_rate is derivable here",
		"provider request latency: a usage event carries no request duration, so no latency_p50/p90 is derivable here",
		"maintenance operations: only the request that announces a rewrite is recorded, so the counts are announcement counts, not operation counts",
		"provider cache key: the local prefix hash covers system+tools and is not the provider's own key",
		"provider scope, TTL, eviction and account pool: not observable from this host at all",
		"attempt-level rows: a multi-attempt usage is stored as one aggregate and cannot be split back into attempts",
	}
}
