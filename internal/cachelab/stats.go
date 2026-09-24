package cachelab

import (
	"fmt"
	"math/rand/v2"
	"sort"
)

// Prices are the registered per-million-token prices a run costs out its tokens
// at. Zero prices mean cost is reported as unknown: an invented price would look
// like evidence.
type Prices struct {
	HitPerMTok  float64
	MissPerMTok float64
	OutPerMTok  float64
}

// Registered reports whether any price was supplied.
func (p Prices) Registered() bool {
	return p.HitPerMTok > 0 || p.MissPerMTok > 0 || p.OutPerMTok > 0
}

// CostUSD prices one sample's tokens, plus the write tokens it reported, which
// providers bill as input. It returns the cost and whether it could be priced.
func (p Prices) CostUSD(s Sample) (float64, bool) {
	if !p.Registered() {
		return 0, false
	}
	in := float64(s.CacheHitTokens)*p.HitPerMTok +
		float64(s.CacheMissTokens)*p.MissPerMTok +
		float64(s.CacheWriteTokens)*p.MissPerMTok
	out := float64(s.CompletionTokens) * p.OutPerMTok
	return (in + out) / 1_000_000, true
}

// ArmStats summarises one arm under the frozen contract: every sample is counted
// in exactly one class, and only eligible warm samples produce a rate.
type ArmStats struct {
	Arm   string
	Stage Stage
	// Changed is the one factor this arm moves, copied from its registration.
	Changed string
	// WarmMin and WarmTarget are the arm's registered warm-request floor and
	// goal, so a report states the gate it was measured against.
	WarmMin    int
	WarmTarget int
	Samples    int
	// Class counts, one bucket per sample.
	FirstRequest      int
	Warm              int
	Repeat            int
	Retry             int
	Errors            int
	UsageMissing      int
	UsageEstimated    int
	NoCacheSplit      int
	InvalidAccounting int
	// Eligible is Warm+Repeat: the samples a rate may be computed over.
	Eligible         int
	HitTokens        int
	MissTokens       int
	PromptTokens     int
	Rate             float64
	Rates            []float64
	P25              float64
	P50              float64
	P75              float64
	LatencyP50MS     int64
	LatencyP95MS     int64
	CostUSD          float64
	CostKnown        bool
	Confounds        []string
	QualityPass      int
	QualityFail      int
	QualityUnchecked int
	// GateReached reports whether this arm met its registered warm minimum.
	GateReached bool
}

// Summarize computes one arm's statistics over its samples.
func Summarize(arm Arm, samples []Sample, prices Prices) ArmStats {
	stats := ArmStats{
		Arm: arm.ID, Stage: arm.Stage, Changed: arm.Changed,
		WarmMin: arm.WarmMin, WarmTarget: arm.WarmTarget, Samples: len(samples),
	}
	var latencies []int64
	confounds := map[string]bool{}
	for _, s := range samples {
		stats.count(s.Classify())
		for _, reason := range s.Confounds {
			confounds[reason] = true
		}
		stats.countQuality(s.QualityCheck)
		latencies = append(latencies, s.LatencyMS)
		if !s.Eligible() {
			continue
		}
		stats.Eligible++
		stats.HitTokens += s.CacheHitTokens
		stats.MissTokens += s.CacheMissTokens
		stats.PromptTokens += s.PromptTokens
		stats.Rates = append(stats.Rates, s.HitRate())
		if cost, ok := prices.CostUSD(s); ok {
			stats.CostUSD += cost
			stats.CostKnown = true
		}
	}
	if denom := stats.HitTokens + stats.MissTokens; denom > 0 {
		stats.Rate = float64(stats.HitTokens) / float64(denom)
	}
	sorted := append([]float64(nil), stats.Rates...)
	sort.Float64s(sorted)
	stats.P25 = quantile(sorted, 0.25)
	stats.P50 = quantile(sorted, 0.50)
	stats.P75 = quantile(sorted, 0.75)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	stats.LatencyP50MS = quantileInt(latencies, 0.50)
	stats.LatencyP95MS = quantileInt(latencies, 0.95)
	stats.Confounds = SortedConfounds(keysOf(confounds))
	stats.GateReached = stats.Eligible >= arm.WarmMin
	return stats
}

// count records one sample's class.
func (s *ArmStats) count(class Class) {
	switch class {
	case ClassFirstRequest:
		s.FirstRequest++
	case ClassWarm:
		s.Warm++
	case ClassRepeat:
		s.Repeat++
	case ClassRetry:
		s.Retry++
	case ClassError:
		s.Errors++
	case ClassUsageMissing:
		s.UsageMissing++
	case ClassUsageEstimated:
		s.UsageEstimated++
	case ClassNoCacheSplit:
		s.NoCacheSplit++
	case ClassInvalidAccounting:
		s.InvalidAccounting++
	}
}

// countQuality records one sample's task-quality outcome.
func (s *ArmStats) countQuality(outcome string) {
	switch outcome {
	case QualityPass:
		s.QualityPass++
	case QualityFail:
		s.QualityFail++
	default:
		s.QualityUnchecked++
	}
}

// Verdict is the registered conclusion vocabulary. "No registered effect" is not
// "no effect": it means the measured difference is not distinguishable from a
// difference smaller than the minimum effect of interest.
type Verdict string

const (
	// VerdictInconclusive covers every path where the evidence gate was not met.
	VerdictInconclusive Verdict = "inconclusive"
	// VerdictSupported means the paired interval cleared the minimum effect.
	VerdictSupported Verdict = "supported"
	// VerdictContradicted means the paired interval excluded the minimum effect
	// in the opposite direction.
	VerdictContradicted Verdict = "contradicted"
	// VerdictNoRegisteredEffect means the interval did not separate from zero by
	// the registered margin.
	VerdictNoRegisteredEffect Verdict = "no_registered_effect"
)

// bootstrapSeed fixes the resampling stream so a comparison is reproducible.
const bootstrapSeed = 20260924

// bootstrapResamples is the registered resample count for paired intervals.
const bootstrapResamples = 2000

// Comparison is one arm measured against a baseline, paired by request order.
type Comparison struct {
	Baseline string
	Arm      string
	// Pairs counts the paired eligible requests the interval was computed over.
	Pairs   int
	DeltaPP float64
	LowerPP float64
	UpperPP float64
	Verdict Verdict
	// Notes record why a verdict was withheld, in the contract's own terms.
	Notes []string
}

// Compare pairs a baseline's eligible samples with an arm's in request order and
// reports the token-weighted difference with a paired bootstrap interval. Pairing
// is positional because the arms were driven turn-for-turn; an unmatched tail is
// dropped rather than compared across different turns.
func Compare(baseline, arm Arm, samples []Sample, prices Prices) Comparison {
	byArm := ByArm(samples)
	base, tested := eligibleSamples(byArm[baseline.ID]), eligibleSamples(byArm[arm.ID])
	baseStats := Summarize(baseline, byArm[baseline.ID], prices)
	armStats := Summarize(arm, byArm[arm.ID], prices)
	cmp := Comparison{Baseline: baseline.ID, Arm: arm.ID}
	cmp.DeltaPP = (armStats.Rate - baseStats.Rate) * 100
	cmp.Pairs = min(len(base), len(tested))
	if baseline.Bytes != arm.Bytes {
		// A ladder arm is registered at a different frozen size: the difference to
		// a baseline of another size describes two workloads, it does not measure
		// a length effect.
		cmp.Notes = append(cmp.Notes, "the baseline and the arm are registered at different request sizes, so this difference is descriptive")
	}
	if !baseStats.GateReached {
		cmp.Notes = append(cmp.Notes, fmt.Sprintf("baseline %s has %d eligible warm requests, under its registered %d", baseline.ID, baseStats.Eligible, baseline.WarmMin))
	}
	if !armStats.GateReached {
		cmp.Notes = append(cmp.Notes, fmt.Sprintf("arm %s has %d eligible warm requests, under its registered %d", arm.ID, armStats.Eligible, arm.WarmMin))
	}
	cmp.Notes = append(cmp.Notes, confoundNotes("baseline", baseStats)...)
	cmp.Notes = append(cmp.Notes, confoundNotes("arm", armStats)...)
	if cmp.Pairs == 0 {
		cmp.Notes = append(cmp.Notes, "no paired eligible requests")
	}
	if len(cmp.Notes) > 0 {
		cmp.Verdict = VerdictInconclusive
		return cmp
	}
	deltas := make([]float64, 0, cmp.Pairs)
	for i := range cmp.Pairs {
		deltas = append(deltas, tested[i].HitRate()-base[i].HitRate())
	}
	cmp.LowerPP, cmp.UpperPP = bootstrapIntervalPP(deltas)
	switch {
	case cmp.LowerPP > MinEffectPP:
		cmp.Verdict = VerdictSupported
	case cmp.UpperPP < -MinEffectPP:
		cmp.Verdict = VerdictContradicted
	default:
		cmp.Verdict = VerdictNoRegisteredEffect
	}
	return cmp
}

// eligibleSamples keeps only the samples a rate may be computed over.
func eligibleSamples(samples []Sample) []Sample {
	out := make([]Sample, 0, len(samples))
	for _, s := range samples {
		if s.Eligible() {
			out = append(out, s)
		}
	}
	return out
}

// confoundNotes names every confound recorded in one arm's samples.
func confoundNotes(side string, stats ArmStats) []string {
	var out []string
	for _, reason := range stats.Confounds {
		out = append(out, fmt.Sprintf("%s carries confound %s", side, reason))
	}
	return out
}

// bootstrapIntervalPP resamples the paired per-request deltas and returns the
// central 95% interval in percentage points.
func bootstrapIntervalPP(deltas []float64) (float64, float64) {
	if len(deltas) == 0 {
		return 0, 0
	}
	rng := rand.New(rand.NewPCG(bootstrapSeed, bootstrapSeed+1))
	means := make([]float64, bootstrapResamples)
	for i := range means {
		var sum float64
		for range deltas {
			sum += deltas[rng.IntN(len(deltas))]
		}
		means[i] = sum / float64(len(deltas))
	}
	sort.Float64s(means)
	lo := means[int(0.025*float64(len(means)))]
	hi := means[min(int(0.975*float64(len(means))), len(means)-1)]
	return lo * 100, hi * 100
}

// quantile returns the frac quantile of a sorted slice, or 0 when empty.
func quantile(sorted []float64, frac float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(frac * float64(len(sorted)-1))
	return sorted[idx]
}

// quantileInt returns the frac quantile of a sorted slice, or 0 when empty.
func quantileInt(sorted []int64, frac float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(frac * float64(len(sorted)-1))
	return sorted[idx]
}

// keysOf lists a set's keys, for a stable confound report.
func keysOf(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	return out
}

// FormatPP renders a percentage-point value with a sign, for reports.
func FormatPP(pp float64) string { return fmt.Sprintf("%+.2fpp", pp) }
