package team

import (
	"encoding/json"
	"testing"
	"time"
)

// reportSample builds one eligible record. Overrides are applied by the caller,
// so each test states only the field it is about.
func reportSample(member string, contextPrompt int, hit, miss int) MemberCacheRequest {
	return MemberCacheRequest{
		ObservedAt: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		TeamID:     "alpha", MemberID: member,
		ModelRef:            "deepseek/deepseek-v4-flash",
		PromptTokens:        hit + miss,
		ContextPromptTokens: contextPrompt,
		CacheHitTokens:      hit, CacheMissTokens: miss,
		RequestCount: 1, AccountingValid: true,
		DiagnosticsAvailable: true,
	}
}

// TestCacheRequestBucketBoundaries pins every boundary as left-closed and
// right-open, which is the contract the published bucket names imply.
func TestCacheRequestBucketBoundaries(t *testing.T) {
	cases := []struct {
		prompt int
		want   CacheRequestBucket
	}{
		{-1, CacheBucketUnknown}, {0, CacheBucketUnknown},
		{1, CacheBucketLT32K}, {32_767, CacheBucketLT32K}, {32_768, CacheBucket32K128K},
		{131_071, CacheBucket32K128K}, {131_072, CacheBucket128K256K},
		{262_143, CacheBucket128K256K}, {262_144, CacheBucket256K512K},
		{524_287, CacheBucket256K512K}, {524_288, CacheBucket512K768K},
		{786_431, CacheBucket512K768K}, {786_432, CacheBucket768K1M},
		{1_048_575, CacheBucket768K1M}, {1_048_576, CacheBucketGTE1M},
	}
	for _, tc := range cases {
		if got := CacheRequestBucketOf(tc.prompt); got != tc.want {
			t.Fatalf("CacheRequestBucketOf(%d) = %q, want %q", tc.prompt, got, tc.want)
		}
	}
}

// TestCacheReportBucketsOnTheRequestPromptNotTheContextGauge pins the metric
// rule the whole baseline rests on: the bucket key is the request's own prompt
// shape, so a session whose gauge reads a million tokens does not place a small
// request in the 1M bucket.
func TestCacheReportBucketsOnTheRequestPromptNotTheContextGauge(t *testing.T) {
	rec := reportSample("m1", 1_000, 900, 100)
	rec.ContextUsed, rec.ContextWindow = 1_800_000, 2_000_000
	report := BuildCacheReport(CacheReportInput{Requests: []MemberCacheRequest{rec}, GeneratedAt: time.Now()})
	if len(report.Buckets) != 1 || report.Buckets[0].Key != string(CacheBucketLT32K) {
		t.Fatalf("buckets = %+v, want one lt_32k bucket: the context gauge is not a bucket key", report.Buckets)
	}
	if report.PromptBasis.ContextPrompt != 1 {
		t.Fatalf("prompt basis = %+v, want the request prompt counted", report.PromptBasis)
	}
}

// TestCacheReportFallsBackToTheBillablePrompt pins the declared fallback and its
// visibility: a usage with no context shape is bucketed on the billable prompt
// and the report says how many samples relied on that.
func TestCacheReportFallsBackToTheBillablePrompt(t *testing.T) {
	rec := reportSample("m1", 0, 900, 100)
	rec.PromptTokens = 40_000
	report := BuildCacheReport(CacheReportInput{Requests: []MemberCacheRequest{rec}, GeneratedAt: time.Now()})
	if report.PromptBasis.PromptFallback != 1 || report.PromptBasis.ContextPrompt != 0 {
		t.Fatalf("prompt basis = %+v, want the fallback counted separately", report.PromptBasis)
	}
	if len(report.Buckets) != 1 || report.Buckets[0].Key != string(CacheBucket32K128K) {
		t.Fatalf("buckets = %+v, want the billable prompt to have keyed the bucket", report.Buckets)
	}
}

// TestCacheReportKeepsTheFourRatesApart is the definition-of-done check: the
// request-level rate, the token-weighted rate, the equal-weight member mean and
// the session-cumulative rate are four different numbers and the report must
// publish all four rather than one "hit rate".
func TestCacheReportKeepsTheFourRatesApart(t *testing.T) {
	requests := []MemberCacheRequest{
		// A small member with a poor rate, and a large member with a good one.
		reportSample("small", 1_000, 100, 900),
		reportSample("large", 20_000, 90_000, 10_000),
		reportSample("large", 20_000, 90_000, 10_000),
	}
	sessions := []CacheSessionTotals{
		{TeamID: "alpha", MemberID: "small", CacheHit: 400, CacheMiss: 600, LastTurnHit: 100, LastTurnMiss: 900},
		{TeamID: "alpha", MemberID: "large", CacheHit: 180_000, CacheMiss: 20_000},
	}
	report := BuildCacheReport(CacheReportInput{
		Requests: requests, Sessions: sessions, GeneratedAt: time.Now(),
	})
	overall := report.Overall
	if !overall.HasRate || !almost(overall.Weighted, 180100.0/201000.0) {
		t.Fatalf("token-weighted = (%v, %v), want 180100/201000", overall.Weighted, overall.HasRate)
	}
	// Member simple mean: (0.1 + 0.9) / 2, which no weighting produces.
	if !overall.HasMemberMean || !almost(overall.MemberSimpleMean, 0.5) {
		t.Fatalf("member simple mean = (%v, %v), want 0.5", overall.MemberSimpleMean, overall.HasMemberMean)
	}
	// Request mean: (0.1 + 0.9 + 0.9) / 3.
	if !almost(overall.Requests.Mean, 0.6333333) {
		t.Fatalf("request mean = %v, want 0.6333", overall.Requests.Mean)
	}
	// Session cumulative: 180400 / 201000, a different number again.
	if !report.Sessions.HasRate || !almost(report.Sessions.TokenWeightedRate, 0.8975124) {
		t.Fatalf("session rate = (%v, %v)", report.Sessions.TokenWeightedRate, report.Sessions.HasRate)
	}
	if !report.Sessions.HasLastTurnRate || !almost(report.Sessions.LastTurnWeightedRate, 0.1) {
		t.Fatalf("last-turn rate = (%v, %v)", report.Sessions.LastTurnWeightedRate, report.Sessions.HasLastTurnRate)
	}
	if len(report.Sessions.Members) != 2 || len(overall.MembersOf) != 2 {
		t.Fatalf("members = %d session / %d overall, want 2 each", len(report.Sessions.Members), len(overall.MembersOf))
	}
}

// TestCacheReportDisclosesExclusionsWithoutCorrecting pins the data-quality
// rule: an unknown, estimated, aggregated, unclosed, split-less or unstamped
// sample is excluded and counted under every reason that applies, and its
// values are never folded into the baseline or rounded to a plausible zero.
func TestCacheReportDisclosesExclusionsWithoutCorrecting(t *testing.T) {
	good := reportSample("m1", 1_000, 900, 100)
	unknown := reportSample("m1", 1_000, 900, 100)
	unknown.UsageUnknown = true
	estimated := reportSample("m1", 1_000, 0, 0)
	estimated.UsageEstimated = true
	aggregate := reportSample("m1", 1_000, 1_800, 200)
	aggregate.RequestCount = 3
	// A split that does not close against the prompt: a reader must reach that
	// verdict from the values, not from the flag the record happens to carry.
	unclosed := reportSample("m1", 1_000, 900, 500)
	unclosed.PromptTokens = 1_000
	unclosed.AccountingValid = false
	noSplit := reportSample("m1", 1_000, 0, 0)
	unstamped := reportSample("m1", 1_000, 900, 100)
	unstamped.ObservedAt = ""
	report := BuildCacheReport(CacheReportInput{
		Requests:    []MemberCacheRequest{good, unknown, estimated, aggregate, unclosed, noSplit, unstamped},
		GeneratedAt: time.Now(),
	})
	want := CacheReportExclusions{
		Received: 7, Included: 1, UnknownUsage: 1, EstimatedUsage: 1,
		AggregateRequests: 1, AccountingInvalid: 1, NoCacheSplit: 2, UnparsableObserved: 1,
	}
	if report.Exclusions != want {
		t.Fatalf("exclusions = %+v, want %+v", report.Exclusions, want)
	}
	if report.Overall.Totals.Requests != 1 {
		t.Fatalf("baseline requests = %d, want only the eligible sample", report.Overall.Totals.Requests)
	}
}

// TestCacheReportWindowAndModelFiltersAreCounted pins that a sample outside the
// window is disclosed rather than silently dropped, and that an unowned record
// never enters a member baseline.
func TestCacheReportWindowAndModelFiltersAreCounted(t *testing.T) {
	inWindow := reportSample("m1", 1_000, 900, 100)
	outside := reportSample("m2", 1_000, 900, 100)
	outside.ObservedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	unowned := reportSample("m3", 1_000, 900, 100)
	unowned.MemberID = ""
	report := BuildCacheReport(CacheReportInput{
		Requests:    []MemberCacheRequest{inWindow, outside, unowned},
		From:        time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		GeneratedAt: time.Now(),
	})
	if report.Exclusions.OutsideWindow != 1 || report.Exclusions.NonMemberScope != 1 {
		t.Fatalf("exclusions = %+v, want one of each", report.Exclusions)
	}
	if len(report.Members) != 1 || report.Members[0] != "m1" {
		t.Fatalf("members = %v, want only the in-window member", report.Members)
	}
}

// TestCacheReportStagesOverlap pins the stage contract: a request can be a warm
// candidate and a post-rewrite request at once, and both strata must carry it.
func TestCacheReportStagesOverlap(t *testing.T) {
	rec := reportSample("m1", 1_000, 900, 100)
	rec.HasPrevRequest, rec.SecondsSincePrevRequest = true, 30
	rec.PrefixChanged, rec.PrefixChangeReasons = true, []string{"compact_auto"}
	report := BuildCacheReport(CacheReportInput{Requests: []MemberCacheRequest{rec}, GeneratedAt: time.Now()})
	keys := map[string]int{}
	for _, group := range report.Stages {
		keys[group.Key] = group.Totals.Requests
	}
	if keys[string(CacheStageWarmCandidate)] != 1 || keys[string(CacheStagePostRewrite)] != 1 {
		t.Fatalf("stages = %+v, want the sample in both overlapping strata", report.Stages)
	}
	if keys[string(CacheStageFirstRequest)] != 0 {
		t.Fatalf("stages = %+v, want no first-request stratum", report.Stages)
	}
	if got := CacheRequestIntervalOf(rec); got != CacheIntervalUnder1m {
		t.Fatalf("interval = %q, want lt_1m", got)
	}
}

// TestCacheReportMarksInsufficientSamples pins the publication gate: a small
// group still publishes its counts but is never presented as a trend.
func TestCacheReportMarksInsufficientSamples(t *testing.T) {
	var requests []MemberCacheRequest
	for range 5 {
		requests = append(requests, reportSample("m1", 1_000, 900, 100))
	}
	report := BuildCacheReport(CacheReportInput{Requests: requests, GeneratedAt: time.Now()})
	if report.Buckets[0].SampleGateReached {
		t.Fatal("5 requests from 1 member must not clear the gate")
	}
	if report.Buckets[0].Totals.Requests != 5 {
		t.Fatalf("an ungated bucket must still publish its counts, got %+v", report.Buckets[0].Totals)
	}
	report = BuildCacheReport(CacheReportInput{
		Requests: requests, MinRequestsPerBucket: 5, MinMembersPerBucket: 1, GeneratedAt: time.Now(),
	})
	if !report.Buckets[0].SampleGateReached {
		t.Fatal("a lowered gate must be reachable, so the field reports the gate rather than a constant")
	}
}

// TestCacheReportZeroDenominatorIsNotZeroPercent pins the fail-honest rule: a
// group with no cache split has no rate at all, printed as n/a downstream, and
// a provider that reports no split is never read as a total miss.
func TestCacheReportZeroDenominatorIsNotZeroPercent(t *testing.T) {
	totals := CacheTokenTotals{Requests: 3, Members: 1}
	if rate, ok := totals.Rate(); ok {
		t.Fatalf("rate of an empty denominator = (%v, true), want (_, false)", rate)
	}
	empty := buildCacheSessionReport([]CacheSessionTotals{{TeamID: "alpha", MemberID: "m1"}})
	if empty.HasRate || empty.HasMemberMean {
		t.Fatalf("a member with no session tokens must have no rate: %+v", empty)
	}
	if empty.Members[0].HasWeightedRate {
		t.Fatal("a member-level rate with no denominators must be reported as absent")
	}
}

// TestCacheRateStatsAreNearestRank pins the percentile definition, so a later
// comparison of two reports compares the same statistic.
func TestCacheRateStatsAreNearestRank(t *testing.T) {
	stats := cacheRateStats([]float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0})
	if stats.P10 != 0.1 || stats.P50 != 0.5 || stats.P90 != 0.9 {
		t.Fatalf("percentiles = (%v, %v, %v), want (0.1, 0.5, 0.9)", stats.P10, stats.P50, stats.P90)
	}
	if stats.Min != 0.1 || stats.Max != 1.0 || stats.Requests != 10 {
		t.Fatalf("stats = %+v", stats)
	}
	if zero := cacheRateStats(nil); zero.Requests != 0 || zero.Mean != 0 {
		t.Fatalf("an empty distribution = %+v, want zero", zero)
	}
}

// TestCacheReportIsReproducible pins that the same samples and the same query
// produce a byte-identical report, which is what makes a before/after
// comparison meaningful.
func TestCacheReportIsReproducible(t *testing.T) {
	generated := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	build := func() CacheReport {
		return BuildCacheReport(CacheReportInput{
			Requests: []MemberCacheRequest{
				reportSample("m2", 1_000, 100, 900),
				reportSample("m1", 200_000, 150_000, 50_000),
				reportSample("m1", 200_000, 160_000, 40_000),
			},
			Sessions:    []CacheSessionTotals{{TeamID: "alpha", MemberID: "m1", CacheHit: 1, CacheMiss: 1}},
			GeneratedAt: generated,
		})
	}
	first, err := json.Marshal(build())
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(build())
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("two runs over the same fixture differ:\n%s\n%s", first, second)
	}
}

// TestCacheReportDeclaresItsQuery pins that the report carries the window, the
// gates and the bucket key it was built with, so a result can be re-run rather
// than merely re-read.
func TestCacheReportDeclaresItsQuery(t *testing.T) {
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	report := BuildCacheReport(CacheReportInput{From: from, GeneratedAt: from})
	if report.BucketKeyField != CacheBucketKeyField {
		t.Fatalf("bucket key field = %q, want %q", report.BucketKeyField, CacheBucketKeyField)
	}
	if report.WindowFrom == "" || report.WindowTo != "" {
		t.Fatalf("window = (%q, %q), want the open-ended window recorded", report.WindowFrom, report.WindowTo)
	}
	if report.MinRequests != CacheReportBucketMinRequests || report.MinMembers != CacheReportBucketMinMembers {
		t.Fatalf("gates = (%d, %d), want the defaults recorded", report.MinRequests, report.MinMembers)
	}
	if report.SchemaVersion != SchemaVersion {
		t.Fatalf("schema version = %d, want %d", report.SchemaVersion, SchemaVersion)
	}
}

// almost compares rates with the tolerance a float division needs.
func almost(got, want float64) bool {
	diff := got - want
	return diff < 1e-6 && diff > -1e-6
}
