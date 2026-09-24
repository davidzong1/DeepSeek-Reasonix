package team

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// reportSample builds one eligible record. Overrides are applied by the caller,
// so each test states only the field it is about. The request count is marked
// observed because that is what eligibility now requires: a fixture that left
// the provenance unset would be testing the unverified path by accident.
func reportSample(member string, contextPrompt int, hit, miss int) MemberCacheRequest {
	return MemberCacheRequest{
		ObservedAt: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		TeamID:     "alpha", MemberID: member,
		ModelRef:            "deepseek/deepseek-v4-flash",
		PromptTokens:        hit + miss,
		ContextPromptTokens: contextPrompt,
		CacheHitTokens:      hit, CacheMissTokens: miss,
		RequestCount: 1, RequestCountSource: RequestCountObserved, AccountingValid: true,
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
		// The aggregate's own tokens are booked, so a reader who wants the
		// all-samples rate adds them back instead of finding the totals short.
		AggregateHitTokens: 1_800, AggregateMissTokens: 200,
	}
	if report.Exclusions != want {
		t.Fatalf("exclusions = %+v, want %+v", report.Exclusions, want)
	}
	if report.Overall.Totals.Requests != 1 {
		t.Fatalf("baseline requests = %d, want only the eligible sample", report.Overall.Totals.Requests)
	}
	// The reconciliation the ledger audit depends on: baseline + booked
	// aggregate tokens is the all-samples total, and nothing is dropped between.
	allSamplesHit := report.Overall.Totals.HitTokens + report.Exclusions.AggregateHitTokens
	allSamplesMiss := report.Overall.Totals.MissTokens + report.Exclusions.AggregateMissTokens
	if allSamplesHit != 900+1_800 || allSamplesMiss != 100+200 {
		t.Fatalf("all-samples = hit %d miss %d, want 2,700/300", allSamplesHit, allSamplesMiss)
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

// TestCacheReportRequiresAMeasuredRequestCount is the eligibility rule the plan
// asks for: a single provider request must be verified, not assumed. The
// compatibility rule reads a missing count as one, so a sample nobody measured
// would otherwise enter a per-request rate as if it had been counted.
func TestCacheReportRequiresAMeasuredRequestCount(t *testing.T) {
	measured := reportSample("m1", 1_000, 900, 100)
	defaulted := reportSample("m1", 1_000, 900, 100)
	defaulted.RequestCountSource = RequestCountDefaulted
	unrecorded := reportSample("m1", 1_000, 900, 100)
	unrecorded.RequestCountSource = ""
	unrecognized := reportSample("m1", 1_000, 900, 100)
	unrecognized.RequestCountSource = "some-future-value"
	report := BuildCacheReport(CacheReportInput{
		Requests:    []MemberCacheRequest{measured, defaulted, unrecorded, unrecognized},
		GeneratedAt: time.Now(),
	})
	if report.Overall.Totals.Requests != 1 || report.Exclusions.Included != 1 {
		t.Fatalf("baseline = %d included / %d requests, want only the measured sample",
			report.Exclusions.Included, report.Overall.Totals.Requests)
	}
	if report.Exclusions.UnverifiedRequestCount != 3 {
		t.Fatalf("unverified = %d, want the three unmeasured samples disclosed", report.Exclusions.UnverifiedRequestCount)
	}
	// Every unmeasured sample's tokens are booked, so the all-samples total is
	// reconstructible and the baseline is never quietly short.
	if report.Exclusions.UnverifiedHitTokens != 2_700 || report.Exclusions.UnverifiedMissTokens != 300 {
		t.Fatalf("booked unverified tokens = %+v", report.Exclusions)
	}
	all := report.AllSamplesTotals()
	if all.HitTokens != 3_600 || all.MissTokens != 400 {
		t.Fatalf("all-samples = hit %d miss %d, want 3,600/400", all.HitTokens, all.MissTokens)
	}
	if all.Requests != 1 || all.Members != 1 {
		t.Fatalf("all-samples request count = %+v, want the baseline's own, never the unmeasured ones", all)
	}
	// The unrecognized value is named apart rather than absorbed into "defaulted":
	// a producer that grew a value must be visible in the report.
	if report.Coverage.RequestCountObserved != 1 || report.Coverage.RequestCountDefaulted != 1 ||
		report.Coverage.RequestCountUnrecorded != 1 || report.Coverage.RequestCountUnrecognized != 1 {
		t.Fatalf("coverage = %+v, want each provenance counted separately", report.Coverage)
	}
}

// TestCacheReportCoverageDescribesTheScopedPopulation pins what the coverage
// ledger is over: the member-scoped samples the report claims to describe, not
// the subset that survived eligibility. A rate published over a population whose
// dimensions are mostly absent must say so.
func TestCacheReportCoverageDescribesTheScopedPopulation(t *testing.T) {
	full := reportSample("m1", 1_000, 900, 100)
	full.UsageSource, full.RouteBucket = "executor", "anthropic/aaaa"
	bare := reportSample("m2", 1_000, 900, 100)
	bare.DiagnosticsAvailable = false
	outside := reportSample("m3", 1_000, 900, 100)
	outside.ObservedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	unowned := reportSample("m4", 1_000, 900, 100)
	unowned.MemberID = ""
	report := BuildCacheReport(CacheReportInput{
		Requests: []MemberCacheRequest{full, bare, outside, unowned},
		From:     time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), GeneratedAt: time.Now(),
	})
	c := report.Coverage
	if c.Scoped != 2 {
		t.Fatalf("scoped = %d, want the two member-scoped samples: an out-of-window or unowned sample is not scoped", c.Scoped)
	}
	if c.UsageSourcePresent != 1 || c.UsageSourceAbsent != 1 {
		t.Fatalf("usage source coverage = %+v, want one of each", c)
	}
	if c.RouteBucketPresent != 1 || c.RouteBucketAbsent != 1 {
		t.Fatalf("route coverage = %+v, want one of each", c)
	}
	if c.DiagnosticsPresent != 1 || c.DiagnosticsAbsent != 1 {
		t.Fatalf("diagnostics coverage = %+v, want one of each", c)
	}
	if c.ModelRefPresent != 2 {
		t.Fatalf("model coverage = %+v, want both scoped samples located", c)
	}
	// The excluded sample still counts as scoped: coverage is over the population
	// the report read, so an exclusion never hides the absence it was excluded for.
	encoded, err := json.Marshal(report.Coverage)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"request_count_observed":2`) {
		t.Fatalf("the coverage ledger must be published with the report: %s", encoded)
	}
}

// TestCacheReportKeepsLowHitSamplesInTheBaseline pins the no-filtering rule: a
// sample whose rate is poor is neither dropped nor reclassified. The only way a
// sample leaves the baseline is a disclosed exclusion, and a low rate is not one.
func TestCacheReportKeepsLowHitSamplesInTheBaseline(t *testing.T) {
	poor := reportSample("m1", 1_000, 10, 990)
	good := reportSample("m1", 1_000, 990, 10)
	report := BuildCacheReport(CacheReportInput{
		Requests: []MemberCacheRequest{poor, good}, GeneratedAt: time.Now(),
	})
	if report.Overall.Totals.Requests != 2 || report.Exclusions.Included != 2 {
		t.Fatalf("baseline = %+v, want both samples kept whatever their rate", report.Overall.Totals)
	}
	if report.Overall.Totals.HitTokens != 1_000 || report.Overall.Totals.MissTokens != 1_000 {
		t.Fatalf("tokens = %+v, want the poor sample's tokens counted, not discarded", report.Overall.Totals)
	}
	if report.Exclusions.UnverifiedRequestCount != 0 {
		t.Fatalf("a poor rate must not be booked as an exclusion: %+v", report.Exclusions)
	}
}

// TestCacheReportCountProvenanceVocabularyIsClosed pins the vocabulary the
// writer maps onto: a count is observed only when the producer measured it and
// reported a positive number, and anything else is the compatibility default.
func TestCacheReportCountProvenanceVocabularyIsClosed(t *testing.T) {
	cases := []struct {
		count    int
		observed bool
		want     string
	}{
		{1, true, RequestCountObserved},
		{3, true, RequestCountObserved},
		{0, true, RequestCountDefaulted},
		{1, false, RequestCountDefaulted},
		{0, false, RequestCountDefaulted},
	}
	for _, tc := range cases {
		if got := RequestCountSourceOf(tc.count, tc.observed); got != tc.want {
			t.Fatalf("RequestCountSourceOf(%d, %v) = %q, want %q", tc.count, tc.observed, got, tc.want)
		}
	}
	if !(MemberCacheRequest{RequestCountSource: RequestCountObserved}).RequestCountVerified() {
		t.Fatal("an observed source must verify")
	}
	for _, source := range []string{RequestCountDefaulted, RequestCountUnrecorded, "", "future"} {
		if (MemberCacheRequest{RequestCountSource: source}).RequestCountVerified() {
			t.Fatalf("source %q must not verify a request count", source)
		}
	}
}

// TestCacheReportPublishesMissTokensPerRequest pins the column a candidate
// optimization is judged on. A rate alone cannot separate "the uncached work
// shrank" from "the prompt grew around a constant uncached overhead", so the
// per-request halves are published beside it.
func TestCacheReportPublishesMissTokensPerRequest(t *testing.T) {
	// A constant 100-token overhead per request: the rate rises with prompt size
	// while nothing about the uncached work changes.
	small := reportSample("m1", 1_000, 900, 100)
	large := reportSample("m1", 40_000, 39_900, 100)
	report := BuildCacheReport(CacheReportInput{
		Requests: []MemberCacheRequest{small, large}, GeneratedAt: time.Now(),
	})
	overall := report.Overall
	if !overall.HasPerRequestTokens || !almost(overall.MissTokensPerRequest, 100) {
		t.Fatalf("miss/request = (%v, %v), want the constant 100", overall.MissTokensPerRequest, overall.HasPerRequestTokens)
	}
	if !almost(overall.HitTokensPerRequest, 20_400) {
		t.Fatalf("hit/request = %v, want 20400", overall.HitTokensPerRequest)
	}
	// The composition effect is exactly the point: the large request's rate is
	// higher although it misses the same absolute amount.
	var smallRate, largeRate float64
	for _, group := range report.Buckets {
		if group.Key == string(CacheBucketLT32K) {
			smallRate = group.Weighted
		}
		if group.Key == string(CacheBucket32K128K) {
			largeRate = group.Weighted
		}
	}
	if !(largeRate > smallRate) {
		t.Fatalf("rates = (%v, %v), want the larger prompt to show the higher rate", smallRate, largeRate)
	}
	// A group with no eligible request has no per-request figure, not a zero.
	empty := CacheTokenTotals{}
	if _, ok := empty.MissTokensPerRequest(); ok {
		t.Fatal("a group with no requests must have no per-request figure")
	}
}

// TestCacheReportPerRequestColumnsAreEncoded guards the JSON surface a reader
// keys on: the per-request columns must be present in the encoded report, since
// a consumer cannot see a field that only exists in the struct.
func TestCacheReportPerRequestColumnsAreEncoded(t *testing.T) {
	report := BuildCacheReport(CacheReportInput{
		Requests:    []MemberCacheRequest{reportSample("m1", 1_000, 900, 100)},
		GeneratedAt: time.Now(),
	})
	encoded, err := json.Marshal(report.Overall)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"miss_tokens_per_request":100`, `"hit_tokens_per_request":900`, `"has_per_request_tokens":true`} {
		if !strings.Contains(string(encoded), key) {
			t.Fatalf("the overall stratum must publish %s:\n%s", key, encoded)
		}
	}
}

// almost compares rates with the tolerance a float division needs.
func almost(got, want float64) bool {
	diff := got - want
	return diff < 1e-6 && diff > -1e-6
}
