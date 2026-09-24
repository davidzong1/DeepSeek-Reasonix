package team

import (
	"fmt"
	"slices"
	"strings"

	"reasonix/internal/cachereason"
)

// CacheDiagnosisLabel is one attribution outcome. The vocabulary is closed: a
// stratum either earns a label from measurements over its own samples, or it is
// left unattributed. Naming a cause the samples do not support would be worse
// than naming none.
type CacheDiagnosisLabel string

const (
	// CacheLabelDataQuality means the stratum's rate cannot be read at face
	// value: too much of it was excluded, or no sample carried a diagnosis.
	CacheLabelDataQuality CacheDiagnosisLabel = "data_quality_or_semantics"
	// CacheLabelStablePrefixChanged means the cache-stable system+tools prefix
	// moved on samples that carry the stratum's miss.
	CacheLabelStablePrefixChanged CacheDiagnosisLabel = "stable_prefix_changed"
	// CacheLabelRewriteCorrelated means the prefix moves on those samples came
	// from a content rewrite the turn performed on its own content — a
	// compaction/fold, a prune, or the lossy truncate rescue.
	CacheLabelRewriteCorrelated CacheDiagnosisLabel = "rewrite_or_compaction_correlated"
	// CacheLabelSchemaCorrelated means the fixed-prefix samples miss a small,
	// stable amount of tokens comparable to the tool schema they carry.
	CacheLabelSchemaCorrelated CacheDiagnosisLabel = "schema_size_correlated"
	// CacheLabelTailGrowth means the miss grows with the request prompt across
	// strata, which a fixed prefix cannot explain.
	CacheLabelTailGrowth CacheDiagnosisLabel = "tail_growth_or_content_correlated"
	// CacheLabelRouteOrInterval means strata that differ only in how long the
	// writer waited differ in rate. It is a correlation with time, never proof
	// of a cache TTL.
	CacheLabelRouteOrInterval CacheDiagnosisLabel = "route_or_interval_correlated"
	// CacheLabelHighMissUnattributed means the prefix was stable and nothing
	// measured here explains the miss. The local hashes cover system+tools only,
	// so this is the honest answer, not a schema claim.
	CacheLabelHighMissUnattributed CacheDiagnosisLabel = "stable_prefix_high_miss_unattributed"
	// CacheLabelUnexplainedPrefixChange means the prefix moved for a reason the
	// report cannot name: none was reported, or the reported value is not in the
	// shared vocabulary. It is the plan's fallback for exactly this case, kept as
	// its own label so a vocabulary drift is never read as "no cause".
	CacheLabelUnexplainedPrefixChange CacheDiagnosisLabel = "unexplained_prefix_change"
	// CacheLabelInsufficientSample means the stratum is below the publication
	// gate. Its counts stand; no trend is claimed.
	CacheLabelInsufficientSample CacheDiagnosisLabel = "insufficient_sample"
)

// CacheFinding is one label with the measurement it rests on. The evidence line
// carries the numbers, so a reader audits the claim instead of trusting it.
type CacheFinding struct {
	Label    CacheDiagnosisLabel `json:"label"`
	Evidence string              `json:"evidence"`
}

// CacheDiagnosisMetrics are the measurements a stratum's findings were derived
// from, published whether or not they earned a label.
type CacheDiagnosisMetrics struct {
	DiagnosedSamples       int      `json:"diagnosed_samples"`
	ColdSamples            int      `json:"cold_samples"`
	UndiagnosedSamples     int      `json:"undiagnosed_samples"`
	StablePrefixChanged    int      `json:"stable_prefix_changed"`
	StablePrefixChangedHit int      `json:"stable_prefix_changed_miss_tokens"`
	FixedPrefixSamples     int      `json:"fixed_prefix_samples"`
	FixedPrefixMissP10     float64  `json:"fixed_prefix_miss_p10"`
	FixedPrefixMissP50     float64  `json:"fixed_prefix_miss_p50"`
	FixedPrefixMissP90     float64  `json:"fixed_prefix_miss_p90"`
	MedianSchemaTokens     float64  `json:"median_schema_tokens_estimate"`
	PrefixChangeReasons    []string `json:"prefix_change_reasons,omitempty"`
	// UnrecognizedChangeReasons lists the reason values this report does not know,
	// with their counts. It exists so a vocabulary the agent grew but the report
	// did not is visible in the baseline rather than silently absorbed.
	UnrecognizedChangeReasons []string `json:"unrecognized_change_reasons,omitempty"`
}

// CacheDiagnosis is one stratum's attribution.
type CacheDiagnosis struct {
	// Investigated is true when the stratum's rate fell below the threshold and
	// the findings below are an attempt to explain it.
	Investigated bool                  `json:"investigated"`
	Rate         float64               `json:"rate"`
	Threshold    float64               `json:"threshold"`
	Findings     []CacheFinding        `json:"findings,omitempty"`
	Metrics      CacheDiagnosisMetrics `json:"metrics"`
}

// cacheGroupObservations is everything one stratum's diagnosis may read: its own
// samples, its ledger, and the rules the report was built under.
type cacheGroupObservations struct {
	samples     []MemberCacheRequest
	totals      CacheTokenTotals
	coverage    CacheGroupCoverage
	gateReached bool
	gates       cacheGroupGates
}

// diagnoseCacheGroup attributes one stratum. It follows the plan's order: rule
// out the data and the semantics first, then the stable prefix, then the
// rewrite, and only then consider a fixed schema tail. A stratum that is not
// low-hit is never attributed — there is nothing to explain.
func diagnoseCacheGroup(in cacheGroupObservations) CacheDiagnosis {
	diagnosis := CacheDiagnosis{Threshold: in.gates.lowHit}
	if rate, ok := in.totals.Rate(); ok {
		diagnosis.Rate = rate
		diagnosis.Investigated = rate < in.gates.lowHit
	}
	if !in.gateReached {
		diagnosis.Findings = append(diagnosis.Findings, CacheFinding{
			Label: CacheLabelInsufficientSample,
			Evidence: fmt.Sprintf("%d requests from %d members, below the %d/%d gate",
				in.totals.Requests, in.totals.Members, in.gates.minRequests, in.gates.minMembers),
		})
	}
	if !diagnosis.Investigated {
		return diagnosis
	}
	in.diagnose(&diagnosis)
	return diagnosis
}

// diagnose fills the findings of an investigated stratum.
//
// A first request is excluded from attribution: nothing was cached before it, so
// its whole prompt is an expected miss, and calling that an unattributed tail
// problem would bury the real signal. Cold samples are counted in the metrics
// and already reported by the first_request stratum.
func (in cacheGroupObservations) diagnose(out *CacheDiagnosis) {
	cold, changed, fixed, undiagnosed := splitByDiagnostics(in.samples)
	out.Metrics.ColdSamples = len(cold)
	out.Metrics.DiagnosedSamples = len(changed) + len(fixed)
	out.Metrics.UndiagnosedSamples = undiagnosed
	out.Metrics.StablePrefixChanged = len(changed)
	out.Metrics.StablePrefixChangedHit = missTokens(changed)
	out.Metrics.FixedPrefixSamples = len(fixed)
	misses := float64Slice(fixed, func(rec MemberCacheRequest) float64 { return float64(rec.CacheMissTokens) })
	slices.Sort(misses)
	out.Metrics.FixedPrefixMissP10 = nearestRank(misses, 0.10)
	out.Metrics.FixedPrefixMissP50 = nearestRank(misses, 0.50)
	out.Metrics.FixedPrefixMissP90 = nearestRank(misses, 0.90)
	out.Metrics.MedianSchemaTokens = medianOfNonZero(fixed, func(rec MemberCacheRequest) float64 {
		return float64(rec.ToolSchemaTokensEstimate)
	})
	out.Metrics.PrefixChangeReasons = prefixReasonHistogram(changed)

	in.findDataQuality(out)
	if out.Metrics.DiagnosedSamples == 0 {
		return
	}
	// Shares are of the warm miss, not of the whole stratum: a cold start's miss
	// is not something a prefix or rewrite story could ever account for.
	warmMiss := missTokens(changed) + missTokens(fixed)
	in.findPrefixChange(changed, warmMiss, out)
	in.findRewrite(changed, warmMiss, out)
	in.findFixedPrefix(fixed, out)
}

// findDataQuality rules out the data and the semantics before any cache story:
// a rate computed over a minority of a stratum's samples describes the minority,
// and a stratum of warm samples that carry no diagnosis cannot be classified at
// all. A stratum of only cold samples is neither — its miss is expected.
func (in cacheGroupObservations) findDataQuality(out *CacheDiagnosis) {
	switch {
	case out.Metrics.UndiagnosedSamples > 0 && out.Metrics.DiagnosedSamples == 0:
		out.Findings = append(out.Findings, CacheFinding{
			Label:    CacheLabelDataQuality,
			Evidence: "no warm sample carried a prefix diagnosis, so no prefix claim is available",
		})
	case in.coverage.excludedShare() >= 0.2:
		out.Findings = append(out.Findings, CacheFinding{
			Label: CacheLabelDataQuality,
			Evidence: fmt.Sprintf("%d of %d received samples were excluded (%s), so this rate describes the remainder",
				in.coverage.Excluded, in.coverage.Received, exclusionSummary(in.coverage.Reasons)),
		})
	}
}

// findPrefixChange checks the cache-stable prefix first, because a moved prefix
// explains a miss without any schema or content story.
func (in cacheGroupObservations) findPrefixChange(changed []MemberCacheRequest, warmMiss int, out *CacheDiagnosis) {
	if len(changed) == 0 || warmMiss <= 0 {
		return
	}
	share := float64(out.Metrics.StablePrefixChangedHit) / float64(warmMiss)
	if share < 0.5 {
		return
	}
	out.Findings = append(out.Findings, CacheFinding{
		Label: CacheLabelStablePrefixChanged,
		Evidence: fmt.Sprintf("%d of %d warm samples changed the stable prefix and carry %.0f%% of the warm miss tokens",
			len(changed), out.Metrics.DiagnosedSamples, share*100),
	})
}

// findRewrite separates the rewrite-caused prefix moves from a plain identity
// change, since only the former is a fold-boundary candidate.
func (in cacheGroupObservations) findRewrite(changed []MemberCacheRequest, warmMiss int, out *CacheDiagnosis) {
	if len(changed) == 0 || warmMiss <= 0 {
		return
	}
	rewritten, unexplained := splitByReasonKind(changed, map[string]int{}, out)
	if len(rewritten) > 0 {
		share := float64(missTokens(rewritten)) / float64(warmMiss)
		if share >= 0.5 {
			out.Findings = append(out.Findings, CacheFinding{
				Label: CacheLabelRewriteCorrelated,
				Evidence: fmt.Sprintf("%d of %d changed-prefix samples carry a rewrite reason (%s) and %.0f%% of the warm miss tokens",
					len(rewritten), len(changed), strings.Join(out.Metrics.PrefixChangeReasons, ", "), share*100),
			})
		}
	}
	in.findUnexplainedChange(unexplained, out, warmMiss)
}

// splitByReasonKind partitions the changed-prefix samples into those carrying a
// rewrite the report can name, and those whose reasons are missing or
// unrecognized. A sample whose reasons are all structural is neither: the prefix
// moved, the reason is known (the system prompt or tool surface changed), and
// there is nothing to explain — only a content rewrite or an unknown value is a
// cause this step has to report.
func splitByReasonKind(changed []MemberCacheRequest, unrecognized map[string]int, out *CacheDiagnosis) (rewritten, unexplained []MemberCacheRequest) {
	for _, rec := range changed {
		rewrite, unknown, hasReasons := reasonKindsOf(rec.PrefixChangeReasons)
		switch {
		case rewrite:
			rewritten = append(rewritten, rec)
		case !hasReasons || len(unknown) > 0:
			unexplained = append(unexplained, rec)
			for _, reason := range unknown {
				unrecognized[reason]++
			}
		}
	}
	out.Metrics.UnrecognizedChangeReasons = reasonHistogram(unrecognized)
	return rewritten, unexplained
}

// reasonKindsOf summarises one sample's reason values: whether any of them is a
// rewrite the vocabulary names, which of them it does not know, and whether there
// were any at all. A structural value is neither — it is known, and knowing it
// means this step has nothing to explain.
func reasonKindsOf(reasons []string) (rewrite bool, unrecognized []string, hasReasons bool) {
	for _, reason := range reasons {
		kind, ok := cachereason.KindOf(reason)
		switch {
		case !ok:
			unrecognized = append(unrecognized, reason)
		case kind == cachereason.Rewrite:
			rewrite = true
		}
	}
	return rewrite, unrecognized, len(reasons) > 0
}

// findUnexplainedChange is the fallback of the plan's step three: a prefix that
// moved with no reason, or with a reason this report does not know, is named as
// such and pointed at instrumentation. Guessing a cause here would be worse than
// saying the cause is unknown — the whole point of the step is to notice a change
// the vocabulary cannot yet describe.
func (in cacheGroupObservations) findUnexplainedChange(unexplained []MemberCacheRequest, out *CacheDiagnosis, warmMiss int) {
	if len(unexplained) == 0 || warmMiss <= 0 {
		return
	}
	share := float64(missTokens(unexplained)) / float64(warmMiss)
	if share < 0.5 {
		return
	}
	detail := "carry no reason"
	if len(out.Metrics.UnrecognizedChangeReasons) > 0 {
		detail = fmt.Sprintf("carry unrecognized reasons (%s)", strings.Join(out.Metrics.UnrecognizedChangeReasons, ", "))
	}
	out.Findings = append(out.Findings, CacheFinding{
		Label: CacheLabelUnexplainedPrefixChange,
		Evidence: fmt.Sprintf("%d of %d changed-prefix samples %s and carry %.0f%% of the warm miss tokens; extend the reason vocabulary rather than guessing a cause",
			len(unexplained), out.Metrics.DiagnosedSamples, detail, share*100),
	})
}

// findFixedPrefix handles the samples whose stable prefix did not move. The
// fixed-tail signature is a miss that is small, stable, and of the same order as
// the tool schema the request carries: a miss far below the schema means the
// schema is mostly cached, and a miss far above it cannot be the schema at all.
// Anything that does not fit stays unattributed, because the local hash covers
// system+tools only and cannot speak for the message tail.
func (in cacheGroupObservations) findFixedPrefix(fixed []MemberCacheRequest, out *CacheDiagnosis) {
	// A stratum whose fixed-prefix samples miss nothing has no tail to attribute:
	// its low rate is the cold start, already reported separately. Claiming an
	// unattributed tail would name a problem the samples do not contain.
	if len(fixed) == 0 || missTokens(fixed) == 0 {
		return
	}
	stats := out.Metrics
	stable := stats.FixedPrefixMissP10 > 0 && stats.FixedPrefixMissP90 <= 1.5*stats.FixedPrefixMissP10
	comparable := stats.MedianSchemaTokens > 0 &&
		stats.FixedPrefixMissP50 >= 0.5*stats.MedianSchemaTokens &&
		stats.FixedPrefixMissP50 <= 2*stats.MedianSchemaTokens
	if stable && comparable {
		out.Findings = append(out.Findings, CacheFinding{
			Label: CacheLabelSchemaCorrelated,
			Evidence: fmt.Sprintf("fixed prefix: median miss %.0f tok (P10 %.0f / P90 %.0f) is stable and of the same order as the median tool schema %.0f tok",
				stats.FixedPrefixMissP50, stats.FixedPrefixMissP10, stats.FixedPrefixMissP90, stats.MedianSchemaTokens),
		})
		return
	}
	out.Findings = append(out.Findings, CacheFinding{
		Label: CacheLabelHighMissUnattributed,
		Evidence: fmt.Sprintf("fixed prefix on %d samples: median miss %.0f tok (P10 %.0f / P90 %.0f) vs schema %.0f tok; the local hash covers system+tools only, so the tail is not attributable here",
			len(fixed), stats.FixedPrefixMissP50, stats.FixedPrefixMissP10, stats.FixedPrefixMissP90, stats.MedianSchemaTokens),
	})
}

// diagnoseCacheReport states what no single stratum can: whether the strata
// differ from each other in a way that matches time or prompt growth.
func diagnoseCacheReport(buckets, intervals []CacheGroupStat) []CacheFinding {
	var out []CacheFinding
	if finding, ok := compareIntervals(intervals); ok {
		out = append(out, finding)
	}
	if finding, ok := compareBuckets(buckets); ok {
		out = append(out, finding)
	}
	return out
}

// compareIntervals reports a rate gap between gap-buckets. It only ever reports
// an association with elapsed time: proving a provider cache TTL needs a
// controlled same-route comparison, which a passive report cannot run.
func compareIntervals(intervals []CacheGroupStat) (CacheFinding, bool) {
	best, worst, ok := spreadOfRates(intervals)
	if !ok || best.Weighted-worst.Weighted < 0.1 {
		return CacheFinding{}, false
	}
	return CacheFinding{
		Label: CacheLabelRouteOrInterval,
		Evidence: fmt.Sprintf("rate differs by gap bucket: %s %.1f%% vs %s %.1f%% (association with elapsed time only, not a proven cache TTL)",
			worst.Key, worst.Weighted*100, best.Key, best.Weighted*100),
	}, true
}

// compareBuckets reports whether the uncached share grows with the request
// prompt, which a fixed prefix and a fixed schema cannot produce.
func compareBuckets(buckets []CacheGroupStat) (CacheFinding, bool) {
	low, high, ok := lowestAndHighestBucket(buckets)
	if !ok || low.Totals.HitTokens+low.Totals.MissTokens <= 0 || high.Totals.HitTokens+high.Totals.MissTokens <= 0 {
		return CacheFinding{}, false
	}
	lowMiss := missShare(low)
	highMiss := missShare(high)
	if highMiss < 0.5 || lowMiss <= 0 || highMiss < 1.5*lowMiss {
		return CacheFinding{}, false
	}
	return CacheFinding{
		Label: CacheLabelTailGrowth,
		Evidence: fmt.Sprintf("uncached share grows with prompt size: %s (mean prompt %.0f tok) misses %.0f%% vs %s (mean prompt %.0f tok) misses %.0f%%",
			low.Key, low.MeanPromptTokens, lowMiss*100, high.Key, high.MeanPromptTokens, highMiss*100),
	}, true
}

// spreadOfRates returns the highest- and lowest-rated strata among those with a
// rate, so a comparison is never made between a rate and an absent one.
func spreadOfRates(groups []CacheGroupStat) (best, worst CacheGroupStat, ok bool) {
	for _, group := range groups {
		if !group.HasRate || group.Totals.Requests < CacheReportBucketMinRequests {
			continue
		}
		if !ok {
			best, worst, ok = group, group, true
			continue
		}
		if group.Weighted > best.Weighted {
			best = group
		}
		if group.Weighted < worst.Weighted {
			worst = group
		}
	}
	return best, worst, ok
}

// lowestAndHighestBucket returns the strata at the two ends of the prompt axis,
// ignoring the bucket that has no prompt key at all.
func lowestAndHighestBucket(buckets []CacheGroupStat) (low, high CacheGroupStat, ok bool) {
	for _, group := range buckets {
		if group.Key == string(CacheBucketUnknown) || group.Totals.Requests == 0 {
			continue
		}
		if !ok {
			low, ok = group, true
		}
		high = group
	}
	return low, high, ok && low.Key != high.Key
}

func missShare(group CacheGroupStat) float64 {
	total := group.Totals.HitTokens + group.Totals.MissTokens
	if total <= 0 {
		return 0
	}
	return float64(group.Totals.MissTokens) / float64(total)
}

// splitByDiagnostics partitions a stratum's samples by the prefix signal the
// diagnosis reads. A cold sample — the first request of a writer session — is
// separated because nothing was cached before it, and a sample without a
// diagnosis is counted apart rather than treated as unchanged: "not observed" is
// not "did not change".
func splitByDiagnostics(samples []MemberCacheRequest) (cold, changed, fixed []MemberCacheRequest, undiagnosed int) {
	for _, rec := range samples {
		switch {
		case !rec.HasPrevRequest:
			cold = append(cold, rec)
		case !rec.DiagnosticsAvailable:
			undiagnosed++
		case rec.StablePrefixChanged:
			changed = append(changed, rec)
		default:
			fixed = append(fixed, rec)
		}
	}
	return cold, changed, fixed, undiagnosed
}

func missTokens(samples []MemberCacheRequest) int {
	total := 0
	for _, rec := range samples {
		total += rec.CacheMissTokens
	}
	return total
}

func float64Slice(samples []MemberCacheRequest, pick func(MemberCacheRequest) float64) []float64 {
	out := make([]float64, 0, len(samples))
	for _, rec := range samples {
		out = append(out, pick(rec))
	}
	return out
}

// medianOfNonZero returns the median of the positive values, and 0 when there
// are none: an estimate a writer never filled must not read as a measured zero.
func medianOfNonZero(samples []MemberCacheRequest, pick func(MemberCacheRequest) float64) float64 {
	values := make([]float64, 0, len(samples))
	for _, rec := range samples {
		if value := pick(rec); value > 0 {
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		return 0
	}
	slices.Sort(values)
	return nearestRank(values, 0.50)
}

// prefixReasonHistogram counts the change reasons across samples, in a stable
// order so a report is reproducible.
func prefixReasonHistogram(samples []MemberCacheRequest) []string {
	counts := map[string]int{}
	for _, rec := range samples {
		for _, reason := range rec.PrefixChangeReasons {
			counts[reason]++
		}
	}
	return reasonHistogram(counts)
}

// reasonHistogram renders reason counts as "value=count" in key order, so two
// reports over the same samples print the same line.
func reasonHistogram(counts map[string]int) []string {
	out := make([]string, 0, len(counts))
	for _, reason := range sortedKeys(counts) {
		out = append(out, fmt.Sprintf("%s=%d", reason, counts[reason]))
	}
	return out
}

// exclusionSummary renders one stratum's exclusion reasons compactly.
func exclusionSummary(reasons CacheGroupExclusions) string {
	parts := []string{}
	for _, entry := range []struct {
		name  string
		count int
	}{
		{"unknown", reasons.UnknownUsage},
		{"estimated", reasons.EstimatedUsage},
		{"aggregate", reasons.AggregateRequests},
		{"accounting_invalid", reasons.AccountingInvalid},
		{"no_split", reasons.NoCacheSplit},
		{"unparsable_ts", reasons.UnparsableObserved},
	} {
		if entry.count > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", entry.name, entry.count))
		}
	}
	if len(parts) == 0 {
		return "no reason"
	}
	return strings.Join(parts, ", ")
}
