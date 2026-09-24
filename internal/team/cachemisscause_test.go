package team

import (
	"testing"
	"time"
)

// missCauseSample builds one received sample with the fields the classification
// reads. Every field is set explicitly so a test states the shape it is testing
// rather than inheriting it from a helper.
func missCauseSample(member string, seq, contextPrompt, hit, miss int) MemberCacheRequest {
	rec := reportSample(member, contextPrompt, hit, miss)
	rec.SessionRequestSeq = seq
	rec.HasPrevRequest = seq > 1
	rec.SecondsSincePrevRequest = 1
	return rec
}

// TestCacheMissCauseIsAPartitionOfTheScopedPopulation pins the property the
// report's reconciliation rests on: every received member-scoped sample lands in
// exactly one cause, so the causes' requests sum to the scoped count and their
// miss tokens sum to the scoped miss.
func TestCacheMissCauseIsAPartitionOfTheScopedPopulation(t *testing.T) {
	requests := []MemberCacheRequest{
		missCauseSample("m1", 1, 10_000, 0, 10_000),
		missCauseSample("m1", 2, 10_200, 9_900, 300),
		missCauseSample("m1", 3, 10_400, 9_900, 500),
		missCauseSample("m2", 1, 20_000, 0, 20_000),
	}
	report := BuildCacheReport(CacheReportInput{Requests: requests, GeneratedAt: time.Now()})
	causes := report.MissCauses
	if causes.ScopedRequests != report.Coverage.Scoped {
		t.Fatalf("partition covers %d requests, coverage counted %d scoped", causes.ScopedRequests, report.Coverage.Scoped)
	}
	sumRequests, sumMiss, sumHit := 0, 0, 0
	for _, stat := range causes.Causes {
		sumRequests += stat.Requests
		sumMiss += stat.MissTokens
		sumHit += stat.HitTokens
	}
	if sumRequests != causes.ScopedRequests {
		t.Fatalf("cause requests sum to %d, want the scoped %d", sumRequests, causes.ScopedRequests)
	}
	if sumMiss != causes.ScopedMissTokens || sumHit != causes.ScopedHitTokens {
		t.Fatalf("cause tokens sum to (%d, %d), want the scoped (%d, %d)", sumHit, sumMiss, causes.ScopedHitTokens, causes.ScopedMissTokens)
	}
}

// TestCacheMissCauseSeparatesColdFromAppendFromResidual pins the three-way split
// the plan's step 3 exists for. A cold opener, an append-only request whose miss
// is the content it appended, and a request whose prefix did not move but whose
// miss outruns that content must land in three different classes.
func TestCacheMissCauseSeparatesColdFromAppendFromResidual(t *testing.T) {
	requests := []MemberCacheRequest{
		missCauseSample("m1", 1, 10_000, 0, 10_000),
		missCauseSample("m1", 2, 10_200, 9_900, 300),
		missCauseSample("m1", 3, 10_400, 9_900, 9_900),
	}
	report := BuildCacheReport(CacheReportInput{Requests: requests, GeneratedAt: time.Now()})
	byCause := map[CacheMissCause]CacheMissCauseStat{}
	for _, stat := range report.MissCauses.Causes {
		byCause[stat.Cause] = stat
	}
	if got := byCause[CacheCauseColdPrefix].Requests; got != 1 {
		t.Fatalf("cold_prefix requests = %d, want 1", got)
	}
	if got := byCause[CacheCauseAppendExpected].Requests; got != 1 {
		t.Fatalf("append_only_expected requests = %d, want 1 (the 200-token append with a 300-token miss)", got)
	}
	if got := byCause[CacheCauseProviderResidual].Requests; got != 1 {
		t.Fatalf("provider_residual_unexplained requests = %d, want 1 (a 200-token append with a 9,900-token miss)", got)
	}
	if byCause[CacheCauseProviderResidual].MissTokens != 9_900 {
		t.Fatalf("residual miss = %d, want the 9,900 tokens nothing local explains", byCause[CacheCauseProviderResidual].MissTokens)
	}
}

// TestCacheMissCauseNamesARewriteAndNotAStructuralMove pins the vocabulary
// split: a fold is a rewrite the report can name, while a moved system prompt is
// structural and there is nothing to fold.
func TestCacheMissCauseNamesARewriteAndNotAStructuralMove(t *testing.T) {
	folded := missCauseSample("m1", 2, 40_000, 30_000, 10_000)
	folded.PrefixChanged, folded.StablePrefixChanged = true, false
	folded.PrefixChangeReasons = []string{"compact_auto"}
	if got := CacheMissCauseOf(folded); got != CacheCauseRewrite {
		t.Fatalf("a fold sample is classified %q, want %q", got, CacheCauseRewrite)
	}
	moved := missCauseSample("m1", 3, 40_000, 30_000, 10_000)
	moved.PrefixChanged, moved.StablePrefixChanged = true, true
	moved.PrefixChangeReasons = []string{"system"}
	if got := CacheMissCauseOf(moved); got != CacheCauseStructural {
		t.Fatalf("a moved system prompt is classified %q, want %q", got, CacheCauseStructural)
	}
	unknown := missCauseSample("m1", 4, 40_000, 30_000, 10_000)
	unknown.PrefixChanged = true
	unknown.PrefixChangeReasons = []string{"a_value_the_report_does_not_know"}
	if got := CacheMissCauseOf(unknown); got != CacheCauseUnexplainedPrefix {
		t.Fatalf("an unrecognized reason is classified %q, want %q", got, CacheCauseUnexplainedPrefix)
	}
}

// TestCacheMissCauseReportsARotationAsCold pins the rescue case: the first
// request after a session rotation opens a fresh provider-side prefix, so it is
// as cold as a session's first request even though the writer has a predecessor
// for it.
func TestCacheMissCauseReportsARotationAsCold(t *testing.T) {
	rotated := missCauseSample("m1", 5, 40_000, 0, 40_000)
	rotated.SessionIDHash = "0123456789abcdef"
	rotated.SessionOrdinal = 2
	rotated.SessionFirstRequestSeq = 5
	if got := CacheMissCauseOf(rotated); got != CacheCauseColdPrefix {
		t.Fatalf("a rotation's first request is classified %q, want %q", got, CacheCauseColdPrefix)
	}
	// The same shape without an ordinal is an older document: the rotation cannot
	// be proven, so the sample must not be reported as cold.
	unproven := rotated
	unproven.SessionOrdinal, unproven.SessionFirstRequestSeq = 0, 0
	if got := CacheMissCauseOf(unproven); got == CacheCauseColdPrefix {
		t.Fatal("a record with no session ordinal must not be reported as a cold prefix")
	}
}

// TestCacheMissCauseDoesNotExplainAMissByAShrinkingPrompt pins the conservative
// direction of the append rule: a prompt that shrank without a reported prefix
// change is not an append, so its miss stays residual rather than being excused.
func TestCacheMissCauseDoesNotExplainAMissByAShrinkingPrompt(t *testing.T) {
	in := cacheMissCauseInput{
		rec:        missCauseSample("m1", 2, 9_000, 8_000, 1_000),
		prevPrompt: 10_000, hasPrev: true,
	}
	if got := classifyCacheMiss(in); got != CacheCauseProviderResidual {
		t.Fatalf("a shrinking prompt is classified %q, want %q", got, CacheCauseProviderResidual)
	}
}

// TestCacheMissCauseLeavesAnUndiagnosedSampleUnattributed pins that "not
// observed" is not "did not change": a sample with no diagnosis is counted apart
// rather than assumed into the append-only class.
func TestCacheMissCauseLeavesAnUndiagnosedSampleUnattributed(t *testing.T) {
	rec := missCauseSample("m1", 2, 10_200, 9_900, 300)
	rec.DiagnosticsAvailable = false
	if got := CacheMissCauseOf(rec); got != CacheCauseUndiagnosed {
		t.Fatalf("an undiagnosed sample is classified %q, want %q", got, CacheCauseUndiagnosed)
	}
}

// TestCacheMissCauseDoesNotClaimARouteLevelRowWasCold pins the disclosure the
// route-level audit makes: a row that never carried the writer's own session
// state cannot be reported as a cold start, because nobody observed whether
// anything preceded it. Reporting it as cold would attribute a whole dataset's
// miss to a start that was never seen.
func TestCacheMissCauseDoesNotClaimARouteLevelRowWasCold(t *testing.T) {
	row := MemberCacheRequest{
		TeamID: "stats-ledger", MemberID: "deepseek",
		PromptTokens: 1_000, ContextPromptTokens: 1_000,
		CacheHitTokens: 900, CacheMissTokens: 100,
		RequestCount: 1, RequestCountSource: RequestCountObserved,
	}
	if got := CacheMissCauseOf(row); got == CacheCauseColdPrefix {
		t.Fatalf("a route-level row with no writer session state is classified %q; nobody observed it was cold", got)
	}
	// A member writer's own first request is still cold: the writer observed that
	// nothing preceded it in its session.
	writer := row
	writer.TeamID, writer.MemberID = "alpha", "m1"
	writer.SessionRequestSeq = 1
	if got := CacheMissCauseOf(writer); got != CacheCauseColdPrefix {
		t.Fatalf("a writer's first request is classified %q, want %q", got, CacheCauseColdPrefix)
	}
}

// TestCacheMissCauseCountsAnExcludedSampleWithoutLosingIt pins that the
// partition covers excluded samples too: an unverified-count sample is not in
// the baseline, but it is still in the received population the partition
// describes, and the eligible count says which it was.
func TestCacheMissCauseCountsAnExcludedSampleWithoutLosingIt(t *testing.T) {
	excluded := missCauseSample("m1", 2, 10_200, 9_900, 300)
	excluded.RequestCountSource = RequestCountUnrecorded
	requests := []MemberCacheRequest{missCauseSample("m1", 1, 10_000, 0, 10_000), excluded}
	report := BuildCacheReport(CacheReportInput{Requests: requests, GeneratedAt: time.Now()})
	eligible := 0
	for _, stat := range report.MissCauses.Causes {
		eligible += stat.BaselineEligible
	}
	if eligible != report.Exclusions.Included {
		t.Fatalf("cause eligible sum = %d, want the report's included %d", eligible, report.Exclusions.Included)
	}
	if report.MissCauses.ScopedRequests != 2 {
		t.Fatalf("scoped requests = %d, want both samples counted", report.MissCauses.ScopedRequests)
	}
}

// TestCacheMissCauseTrackerFindsThePredecessorByWriterSequence pins that the
// append split reads the writer's own sequence rather than arrival order, so a
// report built from a reordered or partially filtered sample set still compares
// each request with the request the writer actually issued before it.
func TestCacheMissCauseTrackerFindsThePredecessorByWriterSequence(t *testing.T) {
	third := missCauseSample("m1", 3, 10_400, 9_900, 300)
	second := missCauseSample("m1", 2, 10_200, 9_900, 100)
	first := missCauseSample("m1", 1, 10_000, 0, 10_000)
	tracker := newCacheMissCauseTracker()
	// Feed in reverse: the predecessor of seq 3 is still seq 2's prompt, so the
	// answer must not depend on which record the reader happened to see first.
	tracker.observe(third)
	in := tracker.observe(second)
	if in.hasPrev {
		t.Fatalf("seq 2 has no earlier request in this session, got prevPrompt=%d", in.prevPrompt)
	}
	in = tracker.observe(first)
	if in.hasPrev {
		t.Fatal("the session's first request must have no predecessor")
	}
	tracker = newCacheMissCauseTracker()
	tracker.observe(second)
	in = tracker.observe(third)
	if !in.hasPrev || in.prevPrompt != 10_200 {
		t.Fatalf("seq 3's predecessor prompt = %d (hasPrev=%v), want 10200", in.prevPrompt, in.hasPrev)
	}
}

// TestCacheMissCauseTrackerSeparatesRotations pins that two sessions of one
// member are tracked apart: a rotation must not read as a prompt shrink against
// the previous session's last request.
func TestCacheMissCauseTrackerSeparatesRotations(t *testing.T) {
	before := missCauseSample("m1", 4, 400_000, 390_000, 10_000)
	before.SessionIDHash = "aaaa"
	before.SessionOrdinal = 1
	after := missCauseSample("m1", 5, 10_000, 0, 10_000)
	after.SessionIDHash = "bbbb"
	after.SessionOrdinal = 2
	after.SessionFirstRequestSeq = 5
	tracker := newCacheMissCauseTracker()
	tracker.observe(before)
	in := tracker.observe(after)
	if in.hasPrev {
		t.Fatalf("a rotated session's first request must not inherit the previous session's prompt: prevPrompt=%d", in.prevPrompt)
	}
	if got := classifyCacheMiss(in); got != CacheCauseColdPrefix {
		t.Fatalf("the rotation's first request is classified %q, want %q", got, CacheCauseColdPrefix)
	}
}

// TestCacheMissCauseReportPublishesTheAllowanceAndOrdersTheCauses is the
// published surface of the partition: every cause carries its own counts, and
// the append rule's slack is stated rather than implicit.
func TestCacheMissCauseReportPublishesTheAllowanceAndOrdersTheCauses(t *testing.T) {
	report := BuildCacheReport(CacheReportInput{
		Requests: []MemberCacheRequest{
			missCauseSample("m1", 1, 10_000, 0, 10_000),
			missCauseSample("m1", 2, 10_200, 9_900, 300),
		},
		GeneratedAt: time.Now(),
	})
	if report.MissCauses.AppendBlockAllowance != cacheAppendBlockAllowance {
		t.Fatalf("allowance = %d, want the rule's own value %d",
			report.MissCauses.AppendBlockAllowance, cacheAppendBlockAllowance)
	}
	if len(report.MissCauses.Causes) != 2 {
		t.Fatalf("causes = %+v, want the two classes the samples reached", report.MissCauses.Causes)
	}
	if report.MissCauses.Causes[0].Cause != CacheCauseColdPrefix {
		t.Fatalf("first cause = %q, want the fixed vocabulary order to start at %q",
			report.MissCauses.Causes[0].Cause, CacheCauseColdPrefix)
	}
	for _, stat := range report.MissCauses.Causes {
		if stat.Cause == CacheCauseColdPrefix && stat.MissTokens != 10_000 {
			t.Fatalf("cold miss = %d, want the whole opener prompt", stat.MissTokens)
		}
	}
}

// TestCacheMissCauseNamesAnUnclaimedMessageRewrite pins the class the plan's F4
// entry needs: a request that rewrote conversation bytes the provider had already
// read, with no fold, prune or truncate claiming it, must not fall into the class
// that means "nothing local explains this".
func TestCacheMissCauseNamesAnUnclaimedMessageRewrite(t *testing.T) {
	// The production shape: the array moved bytes the provider had read and
	// nothing claimed them, so CompareShape reports the "messages" reason. The
	// synthetic shape (rewritten>0, no reasons) is never emitted.
	rec := missCauseSample("m1", 2, 10_200, 9_900, 300)
	rec.PrefixChanged = true
	rec.PrefixChangeReasons = []string{"messages"}
	rec.MessagesComparable = true
	rec.MessagesRewritten = 4
	rec.FirstDivergenceOffset = 7
	if got := CacheMissCauseOf(rec); got != CacheCauseMessagesRewritten {
		t.Fatalf("the producer's unclaimed-rewrite shape is classified %q, want %q", got, CacheCauseMessagesRewritten)
	}
	// A fold that also rewrote messages stays a fold: an operation claimed it.
	folded := rec
	folded.PrefixChangeReasons = []string{"compact_auto"}
	if got := CacheMissCauseOf(folded); got != CacheCauseRewrite {
		t.Fatalf("a claimed fold that rewrote messages is classified %q, want %q", got, CacheCauseRewrite)
	}
	// The same holds when a structural reason and the unclaimed value arrive
	// together: the array rewrite is still the finding, because the structural
	// change is about the framing and does not explain rewritten history.
	both := rec
	both.PrefixChangeReasons = []string{"session_context", "messages"}
	if got := CacheMissCauseOf(both); got != CacheCauseMessagesRewritten {
		t.Fatalf("a sample carrying both a tail revision and an unclaimed rewrite is classified %q, want %q",
			got, CacheCauseMessagesRewritten)
	}
	// Without comparability the offsets mean "not measured", so a record that
	// somehow carried the reason without a comparable array is not claimed as a
	// measured rewrite.
	unmeasured := rec
	unmeasured.PrefixChanged = false
	unmeasured.PrefixChangeReasons = nil
	unmeasured.MessagesComparable = false
	if got := CacheMissCauseOf(unmeasured); got == CacheCauseMessagesRewritten {
		t.Fatal("a sample with no comparable previous request must not be named as a message rewrite")
	}
	// An append-only request is not a rewrite: comparability true, zero rewritten.
	// The append split needs the predecessor's prompt, which the single-record
	// entry point does not have, so this half goes through the classified form.
	appendOnly := cacheMissCauseInput{
		rec:        missCauseSample("m1", 2, 10_200, 9_900, 300),
		prevPrompt: 10_000, hasPrev: true,
	}
	appendOnly.rec.MessagesComparable = true
	if got := classifyCacheMiss(appendOnly); got != CacheCauseAppendExpected {
		t.Fatalf("an append-only request is classified %q, want %q", got, CacheCauseAppendExpected)
	}
}

// TestCacheMissCauseKeepsTheEmptyReasonBranchForForeignRecords pins the shape
// this repository's agent never produces: a prefix move with no reason at all.
// CompareShape always names at least one reason for a move, so a record that
// reaches this branch did not come from the agent — a hand-written line, or a
// producer that grew a field without growing the vocabulary. It stays a distinct
// class rather than being folded into the residual one, because "the prefix
// moved and nobody said why" is not the same finding as "nothing moved".
func TestCacheMissCauseKeepsTheEmptyReasonBranchForForeignRecords(t *testing.T) {
	foreign := missCauseSample("m1", 2, 40_000, 30_000, 10_000)
	foreign.StablePrefixChanged = true
	if got := CacheMissCauseOf(foreign); got != CacheCauseUnexplainedPrefix {
		t.Fatalf("a prefix move with no reason is classified %q, want %q", got, CacheCauseUnexplainedPrefix)
	}
}
