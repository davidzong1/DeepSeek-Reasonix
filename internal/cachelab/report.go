package cachelab

import (
	"fmt"
	"strings"
)

// RenderReport renders one run's journal under the experiment contract: every
// sample is accounted for, only eligible warm requests produce a rate, and any
// arm compared to a baseline gets the paired interval and the verdict that
// followed from it. The text is meant to be pasted into the experiment record
// beside the journal path; it never contains prompt or tool text.
func RenderReport(arms []Arm, samples []Sample, prices Prices) string {
	var b strings.Builder
	fmt.Fprintf(&b, "cachelab experiment report: %d samples across %d arms\n", len(samples), len(ByArm(samples)))
	fmt.Fprintf(&b, "registered minimum effect of interest: %.1fpp (paired interval must clear it on the low side)\n", MinEffectPP)
	if !prices.Registered() {
		b.WriteString("cost: unknown (no per-token prices supplied)\n")
	}
	for _, arm := range arms {
		received, ok := ByArm(samples)[arm.ID]
		if !ok {
			fmt.Fprintf(&b, "\n%s: no samples\n", arm.ID)
			continue
		}
		b.WriteString("\n")
		b.WriteString(renderArm(Summarize(arm, received, prices)))
	}
	base, ok := baselineArm(arms, samples)
	if !ok {
		b.WriteString("\nno baseline arm produced samples, so no comparison is available\n")
		return b.String()
	}
	for _, arm := range arms {
		if arm.ID == base.ID || len(ByArm(samples)[arm.ID]) == 0 {
			continue
		}
		b.WriteString("\n")
		b.WriteString(renderComparison(Compare(base, arm, samples, prices)))
	}
	return b.String()
}

// renderArm renders one arm's totals, exclusions, distribution and gate.
func renderArm(stats ArmStats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s, changed: %s)\n", stats.Arm, stats.Stage, stats.Changed)
	fmt.Fprintf(&b, "  samples=%d eligible_warm=%d target=%d min=%d gate_met=%s\n",
		stats.Samples, stats.Eligible, stats.WarmTarget, stats.WarmMin, toBool(stats.GateReached))
	fmt.Fprintf(&b, "  excluded: first=%d retry=%d error=%d usage_missing=%d no_cache_split=%d usage_estimated=%d invalid_accounting=%d\n",
		stats.FirstRequest, stats.Retry, stats.Errors, stats.UsageMissing, stats.NoCacheSplit, stats.UsageEstimated, stats.InvalidAccounting)
	fmt.Fprintf(&b, "  warm_rate=%.2f%% hit=%d miss=%d prompt=%d tokens\n",
		stats.Rate*100, stats.HitTokens, stats.MissTokens, stats.PromptTokens)
	fmt.Fprintf(&b, "  request_rate p25=%.3f p50=%.3f p75=%.3f\n", stats.P25, stats.P50, stats.P75)
	fmt.Fprintf(&b, "  latency_ms p50=%d p95=%d\n", stats.LatencyP50MS, stats.LatencyP95MS)
	fmt.Fprintf(&b, "  quality: pass=%d fail=%d unchecked=%d\n", stats.QualityPass, stats.QualityFail, stats.QualityUnchecked)
	if stats.CostKnown {
		fmt.Fprintf(&b, "  cost=%.4f USD\n", stats.CostUSD)
	}
	if len(stats.Confounds) > 0 {
		fmt.Fprintf(&b, "  confounds: %s\n", strings.Join(stats.Confounds, ", "))
	}
	return b.String()
}

// renderComparison renders one paired comparison and the verdict vocabulary.
func renderComparison(cmp Comparison) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s vs %s: delta=%s pairs=%d interval=[%s, %s] verdict=%s\n",
		cmp.Arm, cmp.Baseline, FormatPP(cmp.DeltaPP), cmp.Pairs, FormatPP(cmp.LowerPP), FormatPP(cmp.UpperPP), cmp.Verdict)
	for _, note := range cmp.Notes {
		fmt.Fprintf(&b, "  withheld: %s\n", note)
	}
	return b.String()
}

// baselineArm picks the comparison baseline: the first baseline arm that
// produced samples. Without one, a difference has nothing to be measured
// against and no verdict is issued.
func baselineArm(arms []Arm, samples []Sample) (Arm, bool) {
	byArm := ByArm(samples)
	for _, arm := range arms {
		if arm.Variable == VariableBaseline && len(byArm[arm.ID]) > 0 {
			return arm, true
		}
	}
	return Arm{}, false
}

// toBool renders a gate flag as text for the report.
func toBool(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
