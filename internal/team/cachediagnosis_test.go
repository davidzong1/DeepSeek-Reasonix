package team

import (
	"strings"
	"testing"
	"time"

	"reasonix/internal/cachereason"
)

// diagnosisObservedAt is a fixed in-window instant, so a windowed test is
// deterministic.
var diagnosisObservedAt = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

// diagnosisSample is one eligible request with the prefix signal under test.
// contextPrompt and schema drive the bucket and the fixed-tail check.
func diagnosisSample(member string, contextPrompt, hit, miss, schema int) MemberCacheRequest {
	return MemberCacheRequest{
		ObservedAt: diagnosisObservedAt.Format(time.RFC3339Nano),
		TeamID:     "alpha", MemberID: member, ModelRef: "deepseek/deepseek-v4-flash",
		PromptTokens: hit + miss, ContextPromptTokens: contextPrompt,
		CacheHitTokens: hit, CacheMissTokens: miss, RequestCount: 1,
		ToolSchemaTokensEstimate: schema, DiagnosticsAvailable: true,
	}
}

// warmPrefix marks a sample as a request that had a predecessor in the same
// writer session, which is the population the attribution diagnoses.
func warmPrefix(rec MemberCacheRequest) MemberCacheRequest {
	rec.HasPrevRequest = true
	rec.SecondsSincePrevRequest = 30
	return rec
}

// fixedPrefix marks a warm sample as observed with an unchanged stable prefix.
func fixedPrefix(rec MemberCacheRequest) MemberCacheRequest {
	rec = warmPrefix(rec)
	rec.StablePrefixChanged = false
	return rec
}

// changedPrefix marks a warm sample whose cache-stable prefix moved, optionally
// naming the rewrite that caused it.
func changedPrefix(rec MemberCacheRequest, reasons ...string) MemberCacheRequest {
	rec = warmPrefix(rec)
	rec.StablePrefixChanged = true
	rec.PrefixChanged = true
	rec.PrefixChangeReasons = reasons
	return rec
}

// coldPrefix marks the first request of a writer session.
func coldPrefix(rec MemberCacheRequest) MemberCacheRequest {
	rec.HasPrevRequest = false
	rec.SecondsSincePrevRequest = 0
	return rec
}

// diagnoseOne renders one stratum from the samples, using the same eligibility
// and accumulator the report does, so a diagnosis test never tests a parallel
// implementation.
func diagnoseOne(t *testing.T, samples []MemberCacheRequest, gates cacheGroupGates) CacheGroupStat {
	t.Helper()
	acc := newCacheAccumulator()
	for _, rec := range samples {
		if cacheRequestIsBaselineEligible(rec) {
			acc.add(rec, cacheRequestRate(rec))
		} else {
			acc.exclude(rec)
		}
	}
	return acc.stat("test", gates)
}

func defaultDiagnosisGates() cacheGroupGates {
	return cacheGroupGates{minRequests: CacheReportBucketMinRequests, minMembers: CacheReportBucketMinMembers, lowHit: CacheReportLowHitThreshold}
}

// labelsOf renders a stratum's finding labels for assertion.
func labelsOf(stat CacheGroupStat) string {
	labels := make([]string, 0, len(stat.Diagnosis.Findings))
	for _, finding := range stat.Diagnosis.Findings {
		labels = append(labels, string(finding.Label))
	}
	return strings.Join(labels, ",")
}

// TestDiagnosisStaysSilentOnAHealthyStratum pins the first rule: a stratum at or
// above the threshold is not attributed, because there is nothing to explain.
func TestDiagnosisStaysSilentOnAHealthyStratum(t *testing.T) {
	var samples []MemberCacheRequest
	for i := range 30 {
		samples = append(samples, fixedPrefix(diagnosisSample(string(rune('a'+i%3)), 200_000, 950, 50, 4000)))
	}
	stat := diagnoseOne(t, samples, defaultDiagnosisGates())
	if stat.Diagnosis.Investigated {
		t.Fatalf("a 95%% stratum must not be investigated: %+v", stat.Diagnosis)
	}
	if len(stat.Diagnosis.Findings) != 0 {
		t.Fatalf("a healthy stratum must carry no findings, got %v", labelsOf(stat))
	}
	if !stat.SampleGateReached {
		t.Fatal("the fixture must clear the gate, or the silence proves nothing")
	}
}

// TestDiagnosisNamesAChangedStablePrefix pins the attribution order: a moved
// cache-stable prefix that carries the miss is named before any schema story.
func TestDiagnosisNamesAChangedStablePrefix(t *testing.T) {
	var samples []MemberCacheRequest
	for i := range 30 {
		samples = append(samples, changedPrefix(diagnosisSample(string(rune('a'+i%3)), 200_000, 100, 900, 4000), "tools"))
	}
	stat := diagnoseOne(t, samples, defaultDiagnosisGates())
	if labels := labelsOf(stat); !strings.Contains(labels, string(CacheLabelStablePrefixChanged)) {
		t.Fatalf("labels = %v, want the stable-prefix change named", labels)
	}
	if stat.Diagnosis.Metrics.StablePrefixChanged != 30 {
		t.Fatalf("metrics = %+v, want all 30 samples counted as changed", stat.Diagnosis.Metrics)
	}
	if !strings.Contains(stat.Diagnosis.Findings[0].Evidence, "100%") {
		t.Fatalf("the evidence must carry the miss share, got %q", stat.Diagnosis.Findings[0].Evidence)
	}
}

// TestDiagnosisSeparatesARewriteFromAPlainPrefixChange pins the second split:
// only a prefix move caused by the turn's own rewrite is a fold-boundary
// candidate, so it earns its own label.
func TestDiagnosisSeparatesARewriteFromAPlainPrefixChange(t *testing.T) {
	var samples []MemberCacheRequest
	for i := range 30 {
		samples = append(samples, changedPrefix(diagnosisSample(string(rune('a'+i%3)), 200_000, 100, 900, 4000), "compact_auto"))
	}
	stat := diagnoseOne(t, samples, defaultDiagnosisGates())
	labels := labelsOf(stat)
	if !strings.Contains(labels, string(CacheLabelRewriteCorrelated)) || !strings.Contains(labels, string(CacheLabelStablePrefixChanged)) {
		t.Fatalf("labels = %v, want both the change and the rewrite named", labels)
	}
	if !strings.Contains(strings.Join(stat.Diagnosis.Metrics.PrefixChangeReasons, ","), "compact_auto=30") {
		t.Fatalf("metrics = %+v, want the reason histogram", stat.Diagnosis.Metrics.PrefixChangeReasons)
	}

	plain := diagnoseOne(t, samples[:0], defaultDiagnosisGates())
	if strings.Contains(labelsOf(plain), string(CacheLabelRewriteCorrelated)) {
		t.Fatal("an empty stratum must not be attributed a rewrite")
	}
}

// TestDiagnosisNamesAFixedSchemaTailOnlyWhenItFits pins the schema rule with
// both outcomes: a small stable miss of the same order as the tool schema is
// named as the schema tail, and a miss that is either far above or far below it
// stays unattributed — the local hash cannot speak for the message tail, and a
// miss the schema could not account for must not be blamed on it.
func TestDiagnosisNamesAFixedSchemaTailOnlyWhenItFits(t *testing.T) {
	cases := []struct {
		name      string
		miss      int
		wantLabel CacheDiagnosisLabel
	}{
		{"same order as the schema", 4_000, CacheLabelSchemaCorrelated},
		{"far above the schema", 40_000, CacheLabelHighMissUnattributed},
		{"far below the schema", 100, CacheLabelHighMissUnattributed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := fixedPrefix(diagnosisSample("m1", 200_000, 100, tc.miss, 3900))
			stat := diagnoseOne(t, []MemberCacheRequest{rec, rec, rec}, defaultDiagnosisGates())
			labels := labelsOf(stat)
			if !strings.Contains(labels, string(tc.wantLabel)) {
				t.Fatalf("labels = %v, want %s (metrics %+v)", labels, tc.wantLabel, stat.Diagnosis.Metrics)
			}
			// A schema claim and an unattributed verdict are mutually exclusive.
			if strings.Contains(labels, string(CacheLabelSchemaCorrelated)) != (tc.wantLabel == CacheLabelSchemaCorrelated) {
				t.Fatalf("labels = %v, want exactly one of the two verdicts", labels)
			}
		})
	}
}

// TestDiagnosisDoesNotBlameAColdStart is the live-run lesson: a first request
// misses because nothing was cached before it, so attributing that miss to a
// prefix or tail problem would bury the real signal. Cold samples are counted
// and left unattributed.
func TestDiagnosisDoesNotBlameAColdStart(t *testing.T) {
	cold := coldPrefix(diagnosisSample("m1", 200_000, 0, 31_402, 575))
	stat := diagnoseOne(t, []MemberCacheRequest{cold, cold, cold}, defaultDiagnosisGates())
	if len(stat.Diagnosis.Findings) != 1 || stat.Diagnosis.Findings[0].Label != CacheLabelInsufficientSample {
		t.Fatalf("labels = %v, want only the gate statement for a cold stratum", labelsOf(stat))
	}
	if stat.Diagnosis.Metrics.ColdSamples != 3 || stat.Diagnosis.Metrics.DiagnosedSamples != 0 {
		t.Fatalf("metrics = %+v, want the cold samples counted apart", stat.Diagnosis.Metrics)
	}

	// A stratum that is half cold must attribute only the warm half's miss.
	warm := fixedPrefix(diagnosisSample("m1", 200_000, 100, 900, 4000))
	mixed := diagnoseOne(t, []MemberCacheRequest{cold, warm, warm}, defaultDiagnosisGates())
	if mixed.Diagnosis.Metrics.ColdSamples != 1 {
		t.Fatalf("metrics = %+v, want one cold sample", mixed.Diagnosis.Metrics)
	}
	if !strings.Contains(labelsOf(mixed), string(CacheLabelHighMissUnattributed)) {
		t.Fatalf("labels = %v, want the warm half diagnosed", labelsOf(mixed))
	}
}

// TestDiagnosisRulesOutTheDataFirst pins the first step of the flow: when much
// of a bucket was excluded, its rate describes the remainder and the label says
// so instead of letting a cache story stand on a partial sample.
func TestDiagnosisRulesOutTheDataFirst(t *testing.T) {
	estimated := diagnosisSample("m1", 200_000, 100, 900, 4000)
	estimated.UsageEstimated = true
	samples := []MemberCacheRequest{
		fixedPrefix(diagnosisSample("m1", 200_000, 100, 900, 4000)),
		estimated, estimated, estimated,
	}
	stat := diagnoseOne(t, samples, defaultDiagnosisGates())
	if !strings.Contains(labelsOf(stat), string(CacheLabelDataQuality)) {
		t.Fatalf("labels = %v, want the exclusion share disclosed", labelsOf(stat))
	}
	if stat.Coverage.Excluded != 3 || stat.Coverage.Included != 1 {
		t.Fatalf("coverage = %+v, want 3 excluded of 4 received", stat.Coverage)
	}
}

// TestDiagnosisUndiagnosedSamplesAreNotReadAsUnchanged pins the distinction the
// record carries: a sample with no diagnosis is counted apart, never folded
// into the fixed-prefix set as if the prefix had been observed unchanged.
func TestDiagnosisUndiagnosedSamplesAreNotReadAsUnchanged(t *testing.T) {
	blind := warmPrefix(diagnosisSample("m1", 200_000, 100, 900, 4000))
	blind.DiagnosticsAvailable = false
	stat := diagnoseOne(t, []MemberCacheRequest{blind, blind, blind}, defaultDiagnosisGates())
	if stat.Diagnosis.Metrics.UndiagnosedSamples != 3 || stat.Diagnosis.Metrics.FixedPrefixSamples != 0 {
		t.Fatalf("metrics = %+v, want the blind samples counted apart", stat.Diagnosis.Metrics)
	}
	if !strings.Contains(labelsOf(stat), string(CacheLabelDataQuality)) {
		t.Fatalf("labels = %v, want the missing diagnosis disclosed", labelsOf(stat))
	}
}

// TestReportDiagnosisComparesGapStrata pins the report-level half: a rate that
// differs with elapsed time is reported as an association, and the evidence
// never claims a proven cache TTL.
func TestReportDiagnosisComparesGapStrata(t *testing.T) {
	var samples []MemberCacheRequest
	for i := range 30 {
		fast := fixedPrefix(diagnosisSample(string(rune('a'+i%3)), 200_000, 950, 50, 4000))
		fast.SecondsSincePrevRequest = 10
		slow := fixedPrefix(diagnosisSample(string(rune('a'+i%3)), 200_000, 500, 500, 4000))
		slow.SecondsSincePrevRequest = 3600
		samples = append(samples, fast, slow)
	}
	report := BuildCacheReport(CacheReportInput{Requests: samples, GeneratedAt: diagnosisObservedAt})
	findings := labelsInReport(report)
	if !strings.Contains(findings, string(CacheLabelRouteOrInterval)) {
		t.Fatalf("findings = %v, want the interval association reported", findings)
	}
	for _, finding := range report.Diagnosis {
		if finding.Label == CacheLabelRouteOrInterval && !strings.Contains(finding.Evidence, "not a proven cache TTL") {
			t.Fatalf("the interval evidence must not claim a TTL: %q", finding.Evidence)
		}
	}
}

// TestReportDiagnosisDetectsTailGrowth pins the cross-bucket half: an uncached
// share that grows with the request prompt is not something a fixed prefix and a
// fixed schema can produce, so it earns the tail label.
func TestReportDiagnosisDetectsTailGrowth(t *testing.T) {
	var samples []MemberCacheRequest
	for i := range 20 {
		samples = append(samples, fixedPrefix(diagnosisSample(string(rune('a'+i%3)), 1_000, 900, 100, 4000)))
		samples = append(samples, fixedPrefix(diagnosisSample(string(rune('a'+i%3)), 300_000, 5_000, 5_000, 4000)))
	}
	report := BuildCacheReport(CacheReportInput{Requests: samples, GeneratedAt: diagnosisObservedAt})
	if got := labelsInReport(report); !strings.Contains(got, string(CacheLabelTailGrowth)) {
		t.Fatalf("findings = %v, want the tail-growth label (buckets %d)", got, len(report.Buckets))
	}
}

// TestGroupDiagnosisIsReproducible pins that the diagnosis is a pure function of
// the samples, which is what makes two reports comparable.
func TestGroupDiagnosisIsReproducible(t *testing.T) {
	samples := []MemberCacheRequest{
		changedPrefix(diagnosisSample("m1", 200_000, 100, 900, 4000), "compact_auto"),
		fixedPrefix(diagnosisSample("m2", 200_000, 100, 5000, 3900)),
		fixedPrefix(diagnosisSample("m2", 200_000, 100, 5100, 3900)),
	}
	first := labelsOf(diagnoseOne(t, samples, defaultDiagnosisGates()))
	second := labelsOf(diagnoseOne(t, samples, defaultDiagnosisGates()))
	if first != second {
		t.Fatalf("two runs differ: %q vs %q", first, second)
	}
}

// TestReportCoverageIsPerStratum pins the receive ledger: a bucket reports how
// many of its own samples arrived and how many were usable, so a rate over a
// minority is visible where the rate is printed.
func TestReportCoverageIsPerStratum(t *testing.T) {
	good := fixedPrefix(diagnosisSample("m1", 200_000, 900, 100, 4000))
	aggregate := diagnosisSample("m1", 200_000, 1800, 200, 4000)
	aggregate.RequestCount = 3
	other := fixedPrefix(diagnosisSample("m1", 1_000, 900, 100, 4000))
	report := BuildCacheReport(CacheReportInput{
		Requests: []MemberCacheRequest{good, aggregate, other}, GeneratedAt: diagnosisObservedAt,
	})
	byKey := map[string]CacheGroupStat{}
	for _, bucket := range report.Buckets {
		byKey[bucket.Key] = bucket
	}
	mid := byKey[string(CacheBucket128K256K)]
	if mid.Coverage.Received != 2 || mid.Coverage.Included != 1 || mid.Coverage.Excluded != 1 {
		t.Fatalf("coverage = %+v, want 1 of 2 usable in the 128k bucket", mid.Coverage)
	}
	if mid.Coverage.Reasons.AggregateRequests != 1 {
		t.Fatalf("reasons = %+v, want the aggregate reason booked", mid.Coverage.Reasons)
	}
	small := byKey[string(CacheBucketLT32K)]
	if small.Coverage.Received != 1 || small.Coverage.Excluded != 0 {
		t.Fatalf("coverage = %+v, want the small bucket untouched", small.Coverage)
	}
	if mid.MeanPromptTokens != 200_000 {
		t.Fatalf("mean prompt = %v, want the bucket key mean", mid.MeanPromptTokens)
	}
}

// TestReportRecordsTheBuildItMeasured pins the anchor: a baseline names the
// version and commit that produced its samples, so a comparison cannot silently
// span two binaries.
func TestReportRecordsTheBuildItMeasured(t *testing.T) {
	report := BuildCacheReport(CacheReportInput{
		GeneratedAt: diagnosisObservedAt, CodeVersion: "v1.2.3", CodeCommit: "abcdef0",
	})
	if report.CodeVersion != "v1.2.3" || report.CodeCommit != "abcdef0" {
		t.Fatalf("build = (%q, %q), want the supplied identity", report.CodeVersion, report.CodeCommit)
	}
	if report.LowHitThreshold != CacheReportLowHitThreshold {
		t.Fatalf("threshold = %v, want the default recorded", report.LowHitThreshold)
	}
	if lowered := BuildCacheReport(CacheReportInput{GeneratedAt: diagnosisObservedAt, LowHitThreshold: 0.5}); lowered.LowHitThreshold != 0.5 {
		t.Fatalf("threshold = %v, want the caller's value kept", lowered.LowHitThreshold)
	}
}

// labelsInReport renders the report-level findings for assertion.
func labelsInReport(report CacheReport) string {
	labels := make([]string, 0, len(report.Diagnosis))
	for _, finding := range report.Diagnosis {
		labels = append(labels, string(finding.Label))
	}
	return strings.Join(labels, ",")
}

// TestDiagnosisClassifiesEveryDeclaredReason iterates the shared vocabulary
// rather than a list of its own, so a reason added to internal/cachereason is
// covered here the moment it is declared. It is the test that would have caught
// the mis-attributions this vocabulary was created to end: a live rewrite that
// the report did not know reads as "the prefix moved for no stated cause".
func TestDiagnosisClassifiesEveryDeclaredReason(t *testing.T) {
	for _, reason := range cachereason.Values() {
		t.Run(reason, func(t *testing.T) {
			kind, ok := cachereason.KindOf(reason)
			if !ok {
				t.Fatalf("%q is in the vocabulary but has no kind", reason)
			}
			var samples []MemberCacheRequest
			for i := range 30 {
				samples = append(samples, changedPrefix(diagnosisSample(string(rune('a'+i%3)), 200_000, 100, 900, 4000), reason))
			}
			stat := diagnoseOne(t, samples, defaultDiagnosisGates())
			labels := labelsOf(stat)
			if !strings.Contains(labels, string(CacheLabelStablePrefixChanged)) {
				t.Fatalf("labels = %v, want the prefix change reported", labels)
			}
			// A declared value is never unexplained: saying so would tell the
			// reader to extend a vocabulary that already has it.
			if strings.Contains(labels, string(CacheLabelUnexplainedPrefixChange)) {
				t.Fatalf("labels = %v, want no unexplained claim for a declared value", labels)
			}
			if len(stat.Diagnosis.Metrics.UnrecognizedChangeReasons) != 0 {
				t.Fatalf("unrecognized = %v, want none for a declared value", stat.Diagnosis.Metrics.UnrecognizedChangeReasons)
			}
			wantRewrite := kind == cachereason.Rewrite
			if got := strings.Contains(labels, string(CacheLabelRewriteCorrelated)); got != wantRewrite {
				t.Fatalf("rewrite label = %v, want %v for kind %q (labels %v)", got, wantRewrite, kind, labels)
			}
		})
	}
}

// TestDiagnosisDisclosesAnUnknownReasonInsteadOfGuessing pins the honest
// default: a prefix that moves for a reason outside the shared vocabulary is
// named as unexplained, its value is published, and it is never silently
// reclassified as a rewrite or as a tail problem.
func TestDiagnosisDisclosesAnUnknownReasonInsteadOfGuessing(t *testing.T) {
	var samples []MemberCacheRequest
	for i := range 30 {
		samples = append(samples, changedPrefix(diagnosisSample(string(rune('a'+i%3)), 200_000, 100, 900, 4000), "some_future_rewrite"))
	}
	stat := diagnoseOne(t, samples, defaultDiagnosisGates())
	labels := labelsOf(stat)
	if !strings.Contains(labels, string(CacheLabelUnexplainedPrefixChange)) {
		t.Fatalf("labels = %v, want the unexplained fallback", labels)
	}
	if strings.Contains(labels, string(CacheLabelRewriteCorrelated)) {
		t.Fatalf("labels = %v, an unknown value must not be read as a rewrite", labels)
	}
	if got := strings.Join(stat.Diagnosis.Metrics.UnrecognizedChangeReasons, ","); got != "some_future_rewrite=30" {
		t.Fatalf("unrecognized = %q, want the value published with its count", got)
	}
	if !strings.Contains(stat.Diagnosis.Findings[len(stat.Diagnosis.Findings)-1].Evidence, "some_future_rewrite") {
		t.Fatalf("the evidence must name the unknown value, got %q", stat.Diagnosis.Findings[0].Evidence)
	}
}

// TestDiagnosisTreatsAStructuralChangeAsExplained pins the boundary of the
// fallback: a tool-surface change is a known reason with a known cause, so it is
// neither a rewrite nor unexplained — reporting it as unexplained would make the
// fallback meaningless.
func TestDiagnosisTreatsAStructuralChangeAsExplained(t *testing.T) {
	var samples []MemberCacheRequest
	for i := range 30 {
		samples = append(samples, changedPrefix(diagnosisSample(string(rune('a'+i%3)), 200_000, 100, 900, 4000), "tools"))
	}
	stat := diagnoseOne(t, samples, defaultDiagnosisGates())
	labels := labelsOf(stat)
	if !strings.Contains(labels, string(CacheLabelStablePrefixChanged)) {
		t.Fatalf("labels = %v, want the prefix change reported", labels)
	}
	for _, unwanted := range []CacheDiagnosisLabel{CacheLabelUnexplainedPrefixChange, CacheLabelRewriteCorrelated} {
		if strings.Contains(labels, string(unwanted)) {
			t.Fatalf("labels = %v, want no %s for a known structural change", labels, unwanted)
		}
	}
}

// TestDiagnosisTreatsAMissingReasonAsUnexplained pins the other half of the
// fallback: a prefix that moved with no reason at all is exactly what the step
// exists to surface.
func TestDiagnosisTreatsAMissingReasonAsUnexplained(t *testing.T) {
	var samples []MemberCacheRequest
	for i := range 30 {
		samples = append(samples, changedPrefix(diagnosisSample(string(rune('a'+i%3)), 200_000, 100, 900, 4000)))
	}
	stat := diagnoseOne(t, samples, defaultDiagnosisGates())
	labels := labelsOf(stat)
	if !strings.Contains(labels, string(CacheLabelUnexplainedPrefixChange)) {
		t.Fatalf("labels = %v, want the unexplained fallback for a reason-less change", labels)
	}
	if !strings.Contains(stat.Diagnosis.Findings[len(stat.Diagnosis.Findings)-1].Evidence, "no reason") {
		t.Fatalf("the evidence must say no reason was reported")
	}
}
