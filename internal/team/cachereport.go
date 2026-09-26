package team

import (
	"slices"
	"strings"
	"time"
)

// CacheReportBucketMinRequests and CacheReportBucketMinMembers are the sample
// gates a group must clear before a cross-member inference may be published
// from it. Below either gate the group still reports its raw counts and its own
// members' observations, and is labelled insufficient_sample: a trend read off
// three requests is a claim the data does not support.
const (
	CacheReportBucketMinRequests = 30
	CacheReportBucketMinMembers  = 3
)

// CacheReportLowHitThreshold is the rate below which a group is investigated and
// labelled. It is a diagnostic trigger, not a target: a group at or above it
// gets coverage and gate statements and no attribution, because there is nothing
// to attribute.
const CacheReportLowHitThreshold = 0.90

// CacheRequestBucket is the prompt-size bucket one request belongs to. The
// bucket is keyed on the request's own prompt/context size, never on the
// session's context gauge: the gauge moves for reasons the request did not.
type CacheRequestBucket string

const (
	CacheBucketLT32K    CacheRequestBucket = "lt_32k"
	CacheBucket32K128K  CacheRequestBucket = "32k_128k"
	CacheBucket128K256K CacheRequestBucket = "128k_256k"
	CacheBucket256K512K CacheRequestBucket = "256k_512k"
	CacheBucket512K768K CacheRequestBucket = "512k_768k"
	CacheBucket768K1M   CacheRequestBucket = "768k_1m"
	CacheBucketGTE1M    CacheRequestBucket = "gte_1m"
	CacheBucketUnknown  CacheRequestBucket = "unknown_prompt"
)

// CacheBucketKeyField names the record field the report buckets on.
const CacheBucketKeyField = "context_prompt_tokens"

// cacheRequestBuckets is the report order, low prompt to high. unknown_prompt is
// last because it is the absence of a bucket key, not the largest one.
var cacheRequestBuckets = []CacheRequestBucket{
	CacheBucketLT32K, CacheBucket32K128K, CacheBucket128K256K, CacheBucket256K512K,
	CacheBucket512K768K, CacheBucket768K1M, CacheBucketGTE1M, CacheBucketUnknown,
}

// CacheRequestBucketOf returns the bucket for one request's prompt size.
// Boundaries are left-closed and right-open; a non-positive size has no bucket
// key at all.
func CacheRequestBucketOf(promptTokens int) CacheRequestBucket {
	switch {
	case promptTokens <= 0:
		return CacheBucketUnknown
	case promptTokens < 32_768:
		return CacheBucketLT32K
	case promptTokens < 131_072:
		return CacheBucket32K128K
	case promptTokens < 262_144:
		return CacheBucket128K256K
	case promptTokens < 524_288:
		return CacheBucket256K512K
	case promptTokens < 786_432:
		return CacheBucket512K768K
	case promptTokens < 1_048_576:
		return CacheBucket768K1M
	default:
		return CacheBucketGTE1M
	}
}

// CacheRequestStage is one session-phase label. Labels overlap by design: a
// request that follows another AND lands on a prefix rewrite is both a warm
// candidate and a post-rewrite request, and collapsing that to one label would
// hide which of the two the sample was.
type CacheRequestStage string

const (
	CacheStageFirstRequest  CacheRequestStage = "first_request"
	CacheStageWarmCandidate CacheRequestStage = "warm_candidate"
	CacheStagePostRewrite   CacheRequestStage = "post_rewrite"
	CacheStageUnknown       CacheRequestStage = "unknown_stage"
)

// cacheRequestStages is the report order.
var cacheRequestStages = []CacheRequestStage{
	CacheStageFirstRequest, CacheStageWarmCandidate, CacheStagePostRewrite, CacheStageUnknown,
}

// CacheRequestStages returns the stage labels that apply to one record. A warm
// candidate is a request that has a predecessor in the same writer session; it
// is a candidate for provider cache reuse, never evidence that reuse happened.
func CacheRequestStages(r MemberCacheRequest) []CacheRequestStage {
	out := []CacheRequestStage{CacheStageFirstRequest}
	if r.HasPrevRequest {
		out[0] = CacheStageWarmCandidate
	}
	if r.PrefixChanged || r.StablePrefixChanged || len(r.PrefixChangeReasons) > 0 {
		out = append(out, CacheStagePostRewrite)
	}
	if !r.DiagnosticsAvailable {
		out = append(out, CacheStageUnknown)
	}
	return out
}

// CacheRequestIntervalBucket is the gap-bucket label for the time since the
// writer's previous request. These bands are observation strata only: they
// assert nothing about a provider cache TTL.
type CacheRequestIntervalBucket string

const (
	CacheIntervalUnder1m CacheRequestIntervalBucket = "lt_1m"
	CacheInterval1mTo5m  CacheRequestIntervalBucket = "1m_5m"
	CacheInterval5mTo30m CacheRequestIntervalBucket = "5m_30m"
	CacheIntervalOver30m CacheRequestIntervalBucket = "gte_30m"
	CacheIntervalUnknown CacheRequestIntervalBucket = "unknown"
)

// cacheRequestIntervals is the report order.
var cacheRequestIntervals = []CacheRequestIntervalBucket{
	CacheIntervalUnder1m, CacheInterval1mTo5m, CacheInterval5mTo30m, CacheIntervalOver30m, CacheIntervalUnknown,
}

// CacheRequestIntervalOf returns the gap bucket for one record. A first request
// has no gap to bucket, and a negative one is not a measurement.
func CacheRequestIntervalOf(r MemberCacheRequest) CacheRequestIntervalBucket {
	if !r.HasPrevRequest {
		return CacheIntervalUnknown
	}
	switch seconds := r.SecondsSincePrevRequest; {
	case seconds < 0:
		return CacheIntervalUnknown
	case seconds < 60:
		return CacheIntervalUnder1m
	case seconds < 300:
		return CacheInterval1mTo5m
	case seconds < 1800:
		return CacheInterval5mTo30m
	default:
		return CacheIntervalOver30m
	}
}

// CacheGroupStat is one bucket, stage or interval stratum.
type CacheGroupStat struct {
	Key       string            `json:"key"`
	Totals    CacheTokenTotals  `json:"totals"`
	Weighted  float64           `json:"weighted_rate"`
	HasRate   bool              `json:"has_rate"`
	Requests  CacheRateStats    `json:"request_rates"`
	MembersOf []CacheMemberRate `json:"members,omitempty"`
	// Coverage is this stratum's receive ledger: how many samples arrived for it
	// and how many the baseline could use. A rate over a small share of the
	// stratum is not the stratum's rate, and the counts say which it is.
	Coverage CacheGroupCoverage `json:"coverage"`
	// MeanPromptTokens is the stratum's mean bucket key, so a reader can see the
	// request size a rate describes without re-deriving it.
	MeanPromptTokens float64 `json:"mean_prompt_tokens"`
	// HitTokensPerRequest/MissTokensPerRequest are the two halves of the prompt
	// per request, published beside the rate because the rate alone cannot say
	// whether the uncached work moved or only the prompt's composition did.
	HitTokensPerRequest  float64 `json:"hit_tokens_per_request"`
	MissTokensPerRequest float64 `json:"miss_tokens_per_request"`
	HasPerRequestTokens  bool    `json:"has_per_request_tokens"`
	// MemberSimpleMean is the equal-weight mean of the members' token-weighted
	// rates, so it differs from the group's own weighted rate whenever members
	// differ in size. It is the primary cross-member figure.
	MemberSimpleMean float64 `json:"member_simple_mean"`
	HasMemberMean    bool    `json:"has_member_mean"`
	// SampleGateReached is false when the group is too small to carry a
	// cross-member inference. Its raw counts stay published either way.
	SampleGateReached bool `json:"sample_gate_reached"`
	// Diagnosis attributes a low-hit group, or states that the samples do not
	// support an attribution. It is empty for a group that is not low-hit.
	Diagnosis CacheDiagnosis `json:"diagnosis"`
}

// CacheGroupExclusions is one stratum's exclusion ledger, by reason. Reasons
// overlap, so they are counts and not a partition: one sample can be both an
// estimate and a multi-request aggregate.
type CacheGroupExclusions struct {
	UnknownUsage       int `json:"unknown_usage"`
	EstimatedUsage     int `json:"estimated_usage"`
	AggregateRequests  int `json:"aggregate_requests"`
	AccountingInvalid  int `json:"accounting_invalid"`
	NoCacheSplit       int `json:"no_cache_split"`
	UnparsableObserved int `json:"unparsable_observed_at"`
	// UnverifiedRequestCount counts samples whose request count was not a
	// measurement. The count beside such a sample may read 1, but nobody
	// observed it, so it cannot qualify the sample as a single provider request.
	UnverifiedRequestCount int `json:"unverified_request_count"`
}

// add folds one sample's exclusion reasons into this ledger. Reasons are
// independent, so a sample that is both estimated and an aggregate advances both
// counters: this is a disclosure, not a partition.
func (e *CacheGroupExclusions) add(reasons CacheGroupExclusions) {
	e.UnknownUsage += reasons.UnknownUsage
	e.EstimatedUsage += reasons.EstimatedUsage
	e.AggregateRequests += reasons.AggregateRequests
	e.AccountingInvalid += reasons.AccountingInvalid
	e.NoCacheSplit += reasons.NoCacheSplit
	e.UnparsableObserved += reasons.UnparsableObserved
	e.UnverifiedRequestCount += reasons.UnverifiedRequestCount
}

// CacheGroupCoverage is one stratum's receive ledger. Excluded and Included are
// a partition of Received; Reasons is the overlapping breakdown of Excluded.
type CacheGroupCoverage struct {
	Received int                  `json:"received"`
	Included int                  `json:"included"`
	Excluded int                  `json:"excluded"`
	Reasons  CacheGroupExclusions `json:"reasons"`
}

// excludedShare is the fraction of a stratum's samples the baseline could not
// use, which is what a data-quality label is raised on.
func (c CacheGroupCoverage) excludedShare() float64 {
	if c.Received <= 0 {
		return 0
	}
	return float64(c.Excluded) / float64(c.Received)
}

// CachePromptBasis counts which field supplied each sample's bucket key, so a
// reader can see how much of the report rests on the fallback.
type CachePromptBasis struct {
	ContextPrompt  int `json:"context_prompt"`
	PromptFallback int `json:"prompt_fallback"`
	Missing        int `json:"missing"`
}

// CacheReportExclusions counts every sample this report did not include in the
// main baseline, by reason. Reasons overlap: one sample can be both estimated
// and a multi-request aggregate, and each is disclosed rather than collapsed.
type CacheReportExclusions struct {
	Received           int `json:"received"`
	Included           int `json:"included"`
	OutsideWindow      int `json:"outside_window"`
	UnknownUsage       int `json:"unknown_usage"`
	EstimatedUsage     int `json:"estimated_usage"`
	AggregateRequests  int `json:"aggregate_requests"`
	AccountingInvalid  int `json:"accounting_invalid"`
	NoCacheSplit       int `json:"no_cache_split"`
	UnparsableObserved int `json:"unparsable_observed_at"`
	NonMemberScope     int `json:"non_member_scope"`
	// UnverifiedRequestCount counts samples excluded because their request count
	// was not a measurement, so no per-request rate may be computed from them.
	UnverifiedRequestCount int `json:"unverified_request_count"`
	// AggregateHitTokens/AggregateMissTokens are the tokens the excluded
	// multi-request aggregates carried, so an all-samples rate adds them back
	// explicitly instead of finding the totals quietly short.
	AggregateHitTokens  int `json:"aggregate_hit_tokens"`
	AggregateMissTokens int `json:"aggregate_miss_tokens"`
	// UnverifiedHitTokens/UnverifiedMissTokens are the tokens the samples with an
	// unverified request count carried, booked apart exactly as the aggregates are.
	UnverifiedHitTokens  int `json:"unverified_hit_tokens"`
	UnverifiedMissTokens int `json:"unverified_miss_tokens"`
}

// AllSamplesTotals is the report's whole-input token ledger: the baseline plus
// every excluded class whose tokens were booked. A reader who wants "how were
// all the tokens I read served" adds them back here instead of re-deriving the
// exclusions, and a reader who wants the baseline reads Overall.
//
// The two unverified classes are named apart from the aggregates because they
// answer different questions: an aggregate is one sample describing several
// requests, while an unverified count is one sample whose request count nobody
// measured. Both are unfit for a per-request rate, and neither is a miss.
func (r CacheReport) AllSamplesTotals() CacheTokenTotals {
	totals := r.Overall.Totals
	totals.HitTokens += r.Exclusions.AggregateHitTokens + r.Exclusions.UnverifiedHitTokens
	totals.MissTokens += r.Exclusions.AggregateMissTokens + r.Exclusions.UnverifiedMissTokens
	return totals
}

// CacheReportInput is one reproduction of the baseline: the samples, the
// session ledgers, and the window and thresholds they were selected under. All
// of it is recorded in the output, so a result can be re-run and not merely
// re-read.
type CacheReportInput struct {
	Requests []MemberCacheRequest
	Sessions []CacheSessionTotals
	// From and To bound the observation window; a zero bound is open.
	From time.Time
	To   time.Time
	// MinRequestsPerBucket/MinMembersPerBucket default to the package gates.
	MinRequestsPerBucket int
	MinMembersPerBucket  int
	// LowHitThreshold is the rate below which a stratum is investigated; it
	// defaults to CacheReportLowHitThreshold.
	LowHitThreshold float64
	// CodeVersion/CodeCommit name the build the samples were produced by, so a
	// later comparison cannot silently span two binaries.
	CodeVersion string
	CodeCommit  string
	// Source names the dataset the samples came from; it is echoed into the
	// report so a ledger audit can never be read as a member-level result.
	Source string
	// GeneratedAt stamps the report; zero uses the caller's clock.
	GeneratedAt time.Time
}

// CacheReport is one reproducible baseline snapshot.
type CacheReport struct {
	SchemaVersion int    `json:"schema_version"`
	GeneratedAt   string `json:"generated_at"`
	// CodeVersion/CodeCommit anchor the report to the build that produced the
	// samples, so a later comparison cannot silently span two binaries.
	CodeVersion string `json:"code_version,omitempty"`
	CodeCommit  string `json:"code_commit,omitempty"`
	// Source names the dataset: the member writer's own records, or a historical
	// route-level ledger. The two must never be read as one, so the report says
	// which it is rather than leaving it to the caller.
	Source      string `json:"source,omitempty"`
	WindowFrom  string `json:"window_from,omitempty"`
	WindowTo    string `json:"window_to,omitempty"`
	MinRequests int    `json:"min_requests_per_bucket"`
	MinMembers  int    `json:"min_members_per_bucket"`
	// LowHitThreshold is the rate below which a stratum was investigated.
	LowHitThreshold float64 `json:"low_hit_threshold"`
	// BucketKeyField names the field the buckets were keyed on.
	BucketKeyField string   `json:"bucket_key_field"`
	Members        []string `json:"members"`
	ModelRefs      []string `json:"model_refs"`
	// RouteBuckets lists the distinct provider cache scopes the samples reached.
	// Two routes never share a provider cache, so a report spanning more than one
	// must be read as a summary, never as one route's rate.
	RouteBuckets []string              `json:"route_buckets"`
	Overall      CacheGroupStat        `json:"overall"`
	Buckets      []CacheGroupStat      `json:"buckets"`
	Stages       []CacheGroupStat      `json:"stages"`
	Intervals    []CacheGroupStat      `json:"intervals"`
	PromptBasis  CachePromptBasis      `json:"prompt_basis"`
	Exclusions   CacheReportExclusions `json:"exclusions"`
	// Coverage is the field-coverage ledger over the member-scoped samples: how
	// much of the report rests on a dimension that was actually present.
	Coverage CacheReportCoverage `json:"coverage"`
	// MissCauses partitions the same scoped population by the local event that can
	// explain each request's miss: it separates "the turn rewrote its own prefix"
	// from "nothing local changed and the provider still missed".
	MissCauses CacheMissCauseReport `json:"miss_causes"`
	// TurnCost is the plan's per-logical-turn cost ledger and its compaction-cost
	// telemetry, both derived from the same scoped samples as everything above.
	TurnCost CacheTurnTotals `json:"turn_cost"`
	// MaintenanceCost is the announcement-count view of what maintenance did to
	// the scoped samples, including rescue rotations and cold-start miss tokens.
	MaintenanceCost CacheMaintenanceCost `json:"maintenance_cost"`
	// Diagnosis holds the findings that compare strata with each other, which no
	// single stratum can state on its own.
	Diagnosis []CacheFinding `json:"diagnosis,omitempty"`
	// Sessions is the session-cumulative view: per member, and the cross-member
	// token-weighted total. It is reported separately because it is not a
	// per-request metric and must never be quoted as one.
	Sessions CacheSessionReport `json:"sessions"`
}

// CacheSessionReport is the session-cumulative half of the report: the second
// of the four numbers the baseline must keep apart.
type CacheSessionReport struct {
	Members              []CacheMemberRate `json:"members"`
	Totals               CacheTokenTotals  `json:"totals"`
	TokenWeightedRate    float64           `json:"token_weighted_rate"`
	HasRate              bool              `json:"has_rate"`
	MemberSimpleMean     float64           `json:"member_simple_mean"`
	HasMemberMean        bool              `json:"has_member_mean"`
	LastTurnWeightedRate float64           `json:"last_turn_token_weighted_rate"`
	HasLastTurnRate      bool              `json:"has_last_turn_rate"`
	// Maintenance is the summed published maintenance spend, and
	// MaintenanceMembers is how many members published one. The count travels
	// with the sum so a partial total is never read as a whole-session one.
	Maintenance        CacheSessionMaintenance `json:"maintenance"`
	MaintenanceMembers int                     `json:"maintenance_members"`
}

// BuildCacheReport aggregates one reproducible baseline. Every received sample
// is accounted for exactly once — as included, or as excluded under each reason
// that applies — so a reader can reconcile the report against the records it
// was built from.
func BuildCacheReport(in CacheReportInput) CacheReport {
	gates := in.gates()
	generated := in.GeneratedAt
	if generated.IsZero() {
		generated = time.Now().UTC()
	}
	report := CacheReport{
		SchemaVersion:   SchemaVersion,
		GeneratedAt:     generated.UTC().Format(time.RFC3339Nano),
		CodeVersion:     strings.TrimSpace(in.CodeVersion),
		CodeCommit:      strings.TrimSpace(in.CodeCommit),
		Source:          strings.TrimSpace(in.Source),
		MinRequests:     gates.minRequests,
		MinMembers:      gates.minMembers,
		LowHitThreshold: gates.lowHit,
		BucketKeyField:  CacheBucketKeyField,
		Sessions:        buildCacheSessionReport(in.Sessions),
	}
	if !in.From.IsZero() {
		report.WindowFrom = in.From.UTC().Format(time.RFC3339Nano)
	}
	if !in.To.IsZero() {
		report.WindowTo = in.To.UTC().Format(time.RFC3339Nano)
	}
	groups := newCacheGroups()
	members := map[string]bool{}
	models := map[string]bool{}
	routes := map[string]bool{}
	missCauses := newCacheMissCauseTracker()
	turnCost := newCacheTurnReport()
	for _, rec := range in.Requests {
		report.Exclusions.Received++
		if !in.windowContains(rec) {
			report.Exclusions.OutsideWindow++
			continue
		}
		if strings.TrimSpace(rec.TeamID) == "" || strings.TrimSpace(rec.MemberID) == "" {
			report.Exclusions.NonMemberScope++
			continue
		}
		report.Coverage.observe(rec)
		bucket := in.bucketKeyOf(rec, &report.PromptBasis)
		cause := missCauses.observe(rec)
		eligible := cacheRequestIsBaselineEligible(rec)
		report.MissCauses.add(cause, eligible)
		turnCost.add(rec, classifyCacheMiss(cause))
		if !eligible {
			countCacheExclusions(rec, &report.Exclusions)
			bookExcludedTokens(rec, &report.Exclusions)
			groups.buckets[bucket].exclude(rec)
			continue
		}
		report.Exclusions.Included++
		members[rec.MemberID] = true
		if ref := strings.TrimSpace(rec.ModelRef); ref != "" {
			models[ref] = true
		}
		if route := strings.TrimSpace(rec.RouteBucket); route != "" {
			routes[route] = true
		}
		groups.add(rec, bucket, cacheRequestRate(rec))
	}
	report.Members = sortedKeys(members)
	report.ModelRefs = sortedKeys(models)
	report.RouteBuckets = sortedKeys(routes)
	report.Overall = groups.overall.stat("all", gates)
	report.Buckets = renderGroupStats(cacheRequestBuckets, groups.buckets, gates)
	report.Stages = renderGroupStats(cacheRequestStages, groups.stages, gates)
	report.Intervals = renderGroupStats(cacheRequestIntervals, groups.intervals, gates)
	report.Diagnosis = diagnoseCacheReport(report.Buckets, report.Intervals)
	report.MissCauses.order()
	report.TurnCost, report.MaintenanceCost = turnCost.result()
	return report
}

// cacheGroupGates is the sample gate and low-hit threshold one report was built
// with, carried together so a stratum can never be rendered under different
// rules than the report it belongs to.
type cacheGroupGates struct {
	minRequests int
	minMembers  int
	lowHit      float64
}

func (in CacheReportInput) gates() cacheGroupGates {
	gates := cacheGroupGates{
		minRequests: in.MinRequestsPerBucket,
		minMembers:  in.MinMembersPerBucket,
		lowHit:      in.LowHitThreshold,
	}
	if gates.minRequests <= 0 {
		gates.minRequests = CacheReportBucketMinRequests
	}
	if gates.minMembers <= 0 {
		gates.minMembers = CacheReportBucketMinMembers
	}
	if gates.lowHit <= 0 || gates.lowHit > 1 {
		gates.lowHit = CacheReportLowHitThreshold
	}
	return gates
}

func (in CacheReportInput) windowContains(rec MemberCacheRequest) bool {
	if in.From.IsZero() && in.To.IsZero() {
		return true
	}
	at, ok := rec.observed()
	if !ok {
		return false
	}
	if !in.From.IsZero() && at.Before(in.From) {
		return false
	}
	return in.To.IsZero() || !at.After(in.To)
}

// bucketKeyOf returns the sample's bucket and books which field supplied it.
// The request's own prompt shape wins; the billable prompt is the fallback for
// a usage that carries no context shape at all.
func (in CacheReportInput) bucketKeyOf(rec MemberCacheRequest, basis *CachePromptBasis) CacheRequestBucket {
	switch {
	case rec.ContextPromptTokens > 0:
		basis.ContextPrompt++
		return CacheRequestBucketOf(rec.ContextPromptTokens)
	case rec.PromptTokens > 0:
		basis.PromptFallback++
		return CacheRequestBucketOf(rec.PromptTokens)
	default:
		basis.Missing++
		return CacheBucketUnknown
	}
}

// cacheExclusionReasons returns every reason one sample is not main-baseline
// material. Reasons are independent, so a sample that is both estimated and an
// aggregate advances both counts: this is a disclosure, not a partition.
func cacheExclusionReasons(rec MemberCacheRequest) CacheGroupExclusions {
	var out CacheGroupExclusions
	if rec.UsageUnknown {
		out.UnknownUsage = 1
	}
	if rec.UsageEstimated {
		out.EstimatedUsage = 1
	}
	if rec.RequestCount > 1 {
		out.AggregateRequests = 1
	}
	if !rec.RequestCountVerified() || rec.RequestCount <= 0 {
		out.UnverifiedRequestCount = 1
	}
	if valid, _ := rec.Accounting(); !valid {
		out.AccountingInvalid = 1
	}
	if rec.CacheHitTokens+rec.CacheMissTokens <= 0 {
		out.NoCacheSplit = 1
	}
	if _, ok := rec.observed(); !ok {
		out.UnparsableObserved = 1
	}
	return out
}

// bookExcludedTokens records the tokens of one excluded sample in the class its
// request count puts it in. Each sample's tokens are booked exactly once — an
// aggregate first, since a multi-request row's tokens describe several requests
// — so the all-samples total stays a sum of disjoint classes rather than
// double-counting a sample that is both an aggregate and unverified.
func bookExcludedTokens(rec MemberCacheRequest, out *CacheReportExclusions) {
	switch {
	case rec.RequestCount > 1:
		out.AggregateHitTokens += rec.CacheHitTokens
		out.AggregateMissTokens += rec.CacheMissTokens
	case !rec.RequestCountVerified() || rec.RequestCount <= 0:
		out.UnverifiedHitTokens += rec.CacheHitTokens
		out.UnverifiedMissTokens += rec.CacheMissTokens
	}
}

// countCacheExclusions books one sample's exclusion reasons into the report's
// whole-input ledger.
func countCacheExclusions(rec MemberCacheRequest, out *CacheReportExclusions) {
	reasons := cacheExclusionReasons(rec)
	out.UnknownUsage += reasons.UnknownUsage
	out.EstimatedUsage += reasons.EstimatedUsage
	out.AggregateRequests += reasons.AggregateRequests
	out.AccountingInvalid += reasons.AccountingInvalid
	out.NoCacheSplit += reasons.NoCacheSplit
	out.UnparsableObserved += reasons.UnparsableObserved
	out.UnverifiedRequestCount += reasons.UnverifiedRequestCount
} // cacheRequestIsBaselineEligible reports whether one sample may enter the main
// baseline: an exact, single-request, accounting-valid usage that reported a
// cache split and carries a usable observation time. Everything else is
// disclosed in the exclusion ledger instead, never silently corrected.
//
// A single provider request must be verified, not assumed. RequestCount's
// compatibility rule reads a missing count as one, so accepting that value would
// let a sample nobody measured into a per-request rate — the one place the
// default is not good enough. A verified count of one is the only shape that
// qualifies.
//
// The accounting verdict is recomputed from the values rather than read from the
// stored flag: the record's flag says what the writer found, but a reader that
// must exclude a sample has to reach that conclusion itself, or a record that
// never carried the flag — a foreign or hand-edited line — would pass by
// default.
func cacheRequestIsBaselineEligible(rec MemberCacheRequest) bool {
	if rec.UsageUnknown || rec.UsageEstimated || rec.RequestCount > 1 {
		return false
	}
	if !rec.RequestCountVerified() || rec.RequestCount <= 0 {
		return false
	}
	if valid, _ := rec.Accounting(); !valid {
		return false
	}
	if rec.CacheHitTokens+rec.CacheMissTokens <= 0 {
		return false
	}
	_, ok := rec.observed()
	return ok
}

// cacheRequestRate is one eligible sample's request-level hit rate.
func cacheRequestRate(rec MemberCacheRequest) float64 {
	total := rec.CacheHitTokens + rec.CacheMissTokens
	if total <= 0 {
		return 0
	}
	return float64(rec.CacheHitTokens) / float64(total)
}

// cacheGroups is the whole accumulator set for one report: the overall total
// plus the three stratifications, which are fed from the same samples.
type cacheGroups struct {
	overall   *cacheAccumulator
	buckets   map[CacheRequestBucket]*cacheAccumulator
	stages    map[CacheRequestStage]*cacheAccumulator
	intervals map[CacheRequestIntervalBucket]*cacheAccumulator
}

func newCacheGroups() *cacheGroups {
	groups := &cacheGroups{overall: newCacheAccumulator()}
	groups.buckets = map[CacheRequestBucket]*cacheAccumulator{}
	for _, key := range cacheRequestBuckets {
		groups.buckets[key] = newCacheAccumulator()
	}
	groups.stages = map[CacheRequestStage]*cacheAccumulator{}
	for _, key := range cacheRequestStages {
		groups.stages[key] = newCacheAccumulator()
	}
	groups.intervals = map[CacheRequestIntervalBucket]*cacheAccumulator{}
	for _, key := range cacheRequestIntervals {
		groups.intervals[key] = newCacheAccumulator()
	}
	return groups
}

// add files one eligible sample into the overall total and every stratum that
// applies to it. Stages overlap, so one sample can land in two of them.
func (g *cacheGroups) add(rec MemberCacheRequest, bucket CacheRequestBucket, rate float64) {
	g.overall.add(rec, rate)
	g.buckets[bucket].add(rec, rate)
	for _, stage := range CacheRequestStages(rec) {
		g.stages[stage].add(rec, rate)
	}
	g.intervals[CacheRequestIntervalOf(rec)].add(rec, rate)
}

// renderGroupStats renders one stratification in its fixed order, dropping the
// strata no sample reached. Every stratum is a distinct string type, so the
// renderer is generic over the key.
func renderGroupStats[T ~string](keys []T, accs map[T]*cacheAccumulator, gates cacheGroupGates) []CacheGroupStat {
	out := make([]CacheGroupStat, 0, len(keys))
	for _, key := range keys {
		acc := accs[key]
		if acc == nil || acc.requests == 0 {
			continue
		}
		out = append(out, acc.stat(string(key), gates))
	}
	return out
}

// cacheAccumulator folds samples into one stratum. It keeps the raw token
// ledger, the per-request rates, the received samples and the exclusion ledger,
// because the token-weighted rate, the distribution of request rates and the
// coverage behind them are different numbers the report must never conflate.
type cacheAccumulator struct {
	requests  int
	hit       int
	miss      int
	prompt    int
	rates     []float64
	memberIDs []string
	perMember map[string]*cacheMemberAccumulator
	// samples are the eligible records themselves, kept so a stratum can be
	// diagnosed from its own observations rather than from its averages.
	samples  []MemberCacheRequest
	excluded int
	reasons  CacheGroupExclusions
}

type cacheMemberAccumulator struct {
	requests int
	hit      int
	miss     int
	rates    []float64
}

func newCacheAccumulator() *cacheAccumulator {
	return &cacheAccumulator{perMember: map[string]*cacheMemberAccumulator{}}
}

func (a *cacheAccumulator) add(rec MemberCacheRequest, rate float64) {
	a.requests++
	a.hit += rec.CacheHitTokens
	a.miss += rec.CacheMissTokens
	a.prompt += rec.ContextPromptTokens
	a.rates = append(a.rates, rate)
	a.samples = append(a.samples, rec)
	member := a.perMember[rec.MemberID]
	if member == nil {
		member = &cacheMemberAccumulator{}
		a.perMember[rec.MemberID] = member
		a.memberIDs = append(a.memberIDs, rec.MemberID)
	}
	member.requests++
	member.hit += rec.CacheHitTokens
	member.miss += rec.CacheMissTokens
	member.rates = append(member.rates, rate)
}

// exclude books one received sample the baseline could not use, under every
// reason that applies. A sample that is both an estimate and an aggregate
// advances both counters: the ledger discloses reasons, it does not partition
// them.
func (a *cacheAccumulator) exclude(rec MemberCacheRequest) {
	a.excluded++
	a.reasons.add(cacheExclusionReasons(rec))
}

// coverage is this stratum's receive ledger. Included is the eligible count and
// Excluded the rest, so the two always sum to Received.
func (a *cacheAccumulator) coverage() CacheGroupCoverage {
	return CacheGroupCoverage{
		Received: a.requests + a.excluded, Included: a.requests,
		Excluded: a.excluded, Reasons: a.reasons,
	}
}

// stat renders one stratum. The stratum's own weighted rate and the equal-weight
// member mean are computed independently: the first answers "how were this
// stratum's prompt tokens served", the second "how does a typical member fare".
func (a *cacheAccumulator) stat(key string, gates cacheGroupGates) CacheGroupStat {
	totals := CacheTokenTotals{Requests: a.requests, Members: len(a.perMember), HitTokens: a.hit, MissTokens: a.miss}
	stat := CacheGroupStat{
		Key: key, Totals: totals, Requests: cacheRateStats(a.rates), Coverage: a.coverage(),
	}
	if a.requests > 0 {
		stat.MeanPromptTokens = float64(a.prompt) / float64(a.requests)
	}
	stat.HitTokensPerRequest, _ = totals.HitTokensPerRequest()
	stat.MissTokensPerRequest, stat.HasPerRequestTokens = totals.MissTokensPerRequest()
	stat.Weighted, stat.HasRate = totals.Rate()
	stat.MembersOf = a.memberRates()
	stat.MemberSimpleMean, stat.HasMemberMean = simpleMeanOfMembers(stat.MembersOf)
	stat.SampleGateReached = totals.Requests >= gates.minRequests && totals.Members >= gates.minMembers
	stat.Diagnosis = diagnoseCacheGroup(cacheGroupObservations{
		samples: a.samples, totals: totals, coverage: stat.Coverage,
		gateReached: stat.SampleGateReached, gates: gates,
	})
	return stat
}

// memberRates returns each member's own token-weighted rate in member-id order,
// so two runs over the same samples produce byte-identical reports.
func (a *cacheAccumulator) memberRates() []CacheMemberRate {
	ids := slices.Clone(a.memberIDs)
	slices.Sort(ids)
	out := make([]CacheMemberRate, 0, len(ids))
	for _, id := range ids {
		acc := a.perMember[id]
		totals := CacheTokenTotals{Requests: acc.requests, Members: 1, HitTokens: acc.hit, MissTokens: acc.miss}
		rate, ok := totals.Rate()
		out = append(out, CacheMemberRate{
			MemberID:        id,
			Requests:        acc.requests,
			HitTokens:       acc.hit,
			MissTokens:      acc.miss,
			WeightedRate:    rate,
			HasWeightedRate: ok,
			MeanRequestRate: cacheRateStats(acc.rates).Mean,
		})
	}
	return out
}
