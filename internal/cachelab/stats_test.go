package cachelab

import (
	"math"
	"strings"
	"testing"
)

func TestClassifyOrdersExclusionsBeforeWarmth(t *testing.T) {
	base := sample(t, "B1-baseline-repeat", 1, 2, 50, 50)
	cases := []struct {
		name string
		mut  func(*Sample)
		want Class
	}{
		{"error outranks everything", func(s *Sample) { s.Error = "boom"; s.UsageSplit = false; s.Attempt = 3 }, ClassError},
		{"retry outranks missing usage", func(s *Sample) { s.Attempt = 2; s.UsageReported = false }, ClassRetry},
		{"missing usage", func(s *Sample) { s.UsageReported = false }, ClassUsageMissing},
		{"estimated usage", func(s *Sample) { s.UsageEstimated = true }, ClassUsageEstimated},
		{"no cache split", func(s *Sample) { s.UsageSplit = false }, ClassNoCacheSplit},
		{"invalid accounting", func(s *Sample) { s.CacheHitTokens, s.CacheMissTokens, s.PromptTokens = 500, 500, 100 }, ClassInvalidAccounting},
		{"first request", func(s *Sample) { s.TurnSeq = 1 }, ClassFirstRequest},
		{"repeat", func(s *Sample) { s.Repeat = 2 }, ClassRepeat},
		{"warm", func(*Sample) {}, ClassWarm},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			tc.mut(&s)
			if got := s.Classify(); got != tc.want {
				t.Fatalf("class = %s, want %s (%+v)", got, tc.want, s)
			}
		})
	}
	if !base.Eligible() {
		t.Fatalf("a warm, reported, accounted sample must be eligible: %+v", base)
	}
	if got := base.HitRate(); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("rate = %v, want 0.5", got)
	}
}

func TestSummarizeCountsEverySampleOnce(t *testing.T) {
	arm, err := ArmByID("B1-baseline-repeat")
	if err != nil {
		t.Fatal(err)
	}
	samples := []Sample{
		sample(t, arm.ID, 1, 1, 0, 100),
		sample(t, arm.ID, 2, 2, 80, 20),
		sample(t, arm.ID, 3, 3, 80, 20),
	}
	broken := sample(t, arm.ID, 4, 4, 0, 0)
	broken.UsageReported = false
	samples = append(samples, broken)
	failed := sample(t, arm.ID, 5, 5, 0, 0)
	failed.Error = "dial refused"
	failed.RequestHash = ""
	samples = append(samples, failed)

	stats := Summarize(arm, samples, Prices{})
	if stats.Samples != 5 {
		t.Fatalf("samples = %d, want 5", stats.Samples)
	}
	total := stats.FirstRequest + stats.Warm + stats.Repeat + stats.Retry + stats.Errors +
		stats.UsageMissing + stats.UsageEstimated + stats.NoCacheSplit + stats.InvalidAccounting
	if total != stats.Samples {
		t.Fatalf("classes sum to %d, want every sample counted once (%d)", total, stats.Samples)
	}
	if stats.Eligible != 2 || stats.HitTokens != 160 || stats.MissTokens != 40 {
		t.Fatalf("eligible = %d tokens = %d/%d, want 2 and 160/40", stats.Eligible, stats.HitTokens, stats.MissTokens)
	}
	if got := stats.Rate; math.Abs(got-0.8) > 1e-9 {
		t.Fatalf("rate = %v, want the token-weighted 0.8", got)
	}
	if stats.GateReached {
		t.Fatalf("2 eligible warm requests must not clear the registered floor of %d", arm.WarmMin)
	}
	if stats.CostKnown {
		t.Fatal("cost must be unknown without prices")
	}
}

func TestSummarizePricesOnlyWhenRegistered(t *testing.T) {
	arm, err := ArmByID("B1-baseline-repeat")
	if err != nil {
		t.Fatal(err)
	}
	s := sample(t, arm.ID, 1, 2, 1_000_000, 2_000_000)
	s.CompletionTokens = 1_000_000
	prices := Prices{HitPerMTok: 0.1, MissPerMTok: 0.4, OutPerMTok: 2}
	stats := Summarize(arm, []Sample{s}, prices)
	if !stats.CostKnown {
		t.Fatal("registered prices must yield a cost")
	}
	if want := 0.1*1 + 0.4*2 + 2*1; math.Abs(stats.CostUSD-want) > 1e-9 {
		t.Fatalf("cost = %v, want %v", stats.CostUSD, want)
	}
	if _, ok := (Prices{}).CostUSD(s); ok {
		t.Fatal("unpriced tokens must report cost as unknown")
	}
}

func TestCompareWithholdsVerdictsUntilTheGateIsMet(t *testing.T) {
	base, err := ArmByID("B1-baseline-repeat")
	if err != nil {
		t.Fatal(err)
	}
	arm := base
	arm.ID = "B2-interval-short"
	arm.Variable = VariableInterval
	arm.IntervalMS = 2_000

	thin := []Sample{
		sample(t, base.ID, 1, 1, 0, 100),
		sample(t, base.ID, 2, 2, 0, 100),
		sample(t, arm.ID, 3, 1, 0, 100),
		sample(t, arm.ID, 4, 2, 100, 0),
	}
	cmp := Compare(base, arm, thin, Prices{})
	if cmp.Verdict != VerdictInconclusive {
		t.Fatalf("verdict = %s, want %s below the registered gate", cmp.Verdict, VerdictInconclusive)
	}
	if len(cmp.Notes) == 0 {
		t.Fatal("an inconclusive comparison must say why")
	}
}

// armSamples builds count eligible warm samples at one hit ratio.
func armSamples(armID string, count int, hit, miss int) []Sample {
	out := make([]Sample, 0, count)
	for i := range count {
		s := Sample{
			Arm: armID, Seq: i + 1, At: "2026-09-24T08:00:00Z", RequestHash: "hash",
			TurnSeq: i + 2, Status: 200, UsageReported: true, UsageSplit: true,
			CacheHitTokens: hit, CacheMissTokens: miss, PromptTokens: hit + miss,
			QualityCheck: QualityPass,
		}
		out = append(out, s)
	}
	return out
}

func TestCompareVerdicts(t *testing.T) {
	base, err := ArmByID("B1-baseline-repeat")
	if err != nil {
		t.Fatal(err)
	}
	arm := base
	arm.ID = "B3-system-tail"
	arm.Variable = VariableComponent
	arm.Component = ComponentSystemTail

	t.Run("supported", func(t *testing.T) {
		samples := append(armSamples(base.ID, FormalWarmTarget, 20, 80), armSamples(arm.ID, FormalWarmTarget, 60, 40)...)
		cmp := Compare(base, arm, samples, Prices{})
		if cmp.Verdict != VerdictSupported {
			t.Fatalf("verdict = %s (lower %.2fpp), want supported: 40pp clears the %.1fpp margin", cmp.Verdict, cmp.LowerPP, MinEffectPP)
		}
		if cmp.DeltaPP != 40 {
			t.Fatalf("delta = %v, want +40pp", cmp.DeltaPP)
		}
	})

	t.Run("no registered effect", func(t *testing.T) {
		samples := append(armSamples(base.ID, FormalWarmTarget, 50, 50), armSamples(arm.ID, FormalWarmTarget, 51, 49)...)
		cmp := Compare(base, arm, samples, Prices{})
		if cmp.Verdict != VerdictNoRegisteredEffect {
			t.Fatalf("verdict = %s, want %s", cmp.Verdict, VerdictNoRegisteredEffect)
		}
	})

	t.Run("contradicted", func(t *testing.T) {
		samples := append(armSamples(base.ID, FormalWarmTarget, 80, 20), armSamples(arm.ID, FormalWarmTarget, 20, 80)...)
		cmp := Compare(base, arm, samples, Prices{})
		if cmp.Verdict != VerdictContradicted {
			t.Fatalf("verdict = %s, want %s", cmp.Verdict, VerdictContradicted)
		}
	})

	t.Run("confounded", func(t *testing.T) {
		tested := armSamples(arm.ID, FormalWarmTarget, 90, 10)
		for i := range tested {
			tested[i].AddConfound(ConfoundToolSurfaceDrift)
		}
		samples := append(armSamples(base.ID, FormalWarmTarget, 20, 80), tested...)
		cmp := Compare(base, arm, samples, Prices{})
		if cmp.Verdict != VerdictInconclusive {
			t.Fatalf("verdict = %s, want %s for a confounded arm", cmp.Verdict, VerdictInconclusive)
		}
	})
}

func TestBootstrapIntervalIsDeterministic(t *testing.T) {
	deltas := []float64{0.4, 0.4, 0.5, 0.35, 0.45, 0.4}
	lo1, hi1 := bootstrapIntervalPP(deltas)
	lo2, hi2 := bootstrapIntervalPP(deltas)
	if lo1 != lo2 || hi1 != hi2 {
		t.Fatalf("interval moved between runs: [%v,%v] then [%v,%v]", lo1, hi1, lo2, hi2)
	}
	if lo1 >= hi1 {
		t.Fatalf("interval = [%v,%v], want a positive width", lo1, hi1)
	}
	if lo, hi := bootstrapIntervalPP(nil); lo != 0 || hi != 0 {
		t.Fatalf("empty interval = [%v,%v], want zero", lo, hi)
	}
}

func TestStopReasonOnlyFiresOnRegisteredCauses(t *testing.T) {
	arm, err := ArmByID("B0-pilot")
	if err != nil {
		t.Fatal(err)
	}
	healthy := []Sample{
		sample(t, arm.ID, 1, 1, 0, 100),
		sample(t, arm.ID, 2, 2, 90, 10),
	}
	if got := StopReason(arm, healthy, 0, false); got != "" {
		t.Fatalf("stop = %q, want a healthy run to continue", got)
	}
	if got := StopReason(arm, healthy, CostCapUSD+1, true); got == "" {
		t.Fatal("a priced run over the cost cap must stop")
	}
	if got := StopReason(arm, healthy, CostCapUSD+1, false); got != "" {
		t.Fatal("an unpriced run cannot claim a cost overrun")
	}

	failing := []Sample{}
	for i := range MaxConsecutiveErrors {
		s := sample(t, arm.ID, i+1, i+1, 0, 0)
		s.Status = 500
		s.Error = "upstream error"
		s.RequestHash = "hash"
		failing = append(failing, s)
	}
	if got := StopReason(arm, failing, 0, false); got == "" {
		t.Fatal("consecutive service errors must stop the run")
	}

	blind := []Sample{}
	for i := range MaxConsecutiveUsageMissing {
		s := sample(t, arm.ID, i+1, i+1, 0, 0)
		s.UsageReported = false
		blind = append(blind, s)
	}
	if got := StopReason(arm, blind, 0, false); got == "" {
		t.Fatal("a provider that stopped reporting usage must stop the run")
	}

	drifted := append([]Sample(nil), healthy...)
	drifted[1].AddConfound(ConfoundToolSurfaceDrift)
	if got := StopReason(arm, drifted, 0, false); got == "" {
		t.Fatal("a drifted tool surface inside one arm must stop the run")
	}
}

func TestRenderReportAccountsForEveryArm(t *testing.T) {
	arms := RegisteredArms()
	samples := append(armSamples("B1-baseline-repeat", 3, 10, 90), armSamples("B4-ladder-mid", 3, 90, 10)...)
	text := RenderReport(arms, samples, Prices{HitPerMTok: 0.05, MissPerMTok: 0.2})
	for _, want := range []string{"B1-baseline-repeat", "B4-ladder-mid", "no samples", "verdict=", "cost="} {
		if !strings.Contains(text, want) {
			t.Fatalf("report is missing %q:\n%s", want, text)
		}
	}
}
