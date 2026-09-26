package team

import (
	"strings"
	"testing"
	"time"
)

// turnSample builds one sample with the turn identity and finish reason the
// per-turn ledger reads.
func turnSample(member, turn string, contextPrompt, hit, miss int) MemberCacheRequest {
	rec := reportSample(member, contextPrompt, hit, miss)
	rec.TurnID = turn
	rec.FinishReason = "stop"
	return rec
}

// TestCacheTurnCostDividesByDistinctTurnsNotRequests pins the denominator the
// plan fixes: a logical turn that cost three provider requests is one turn, so a
// per-turn figure is not the per-request figure under another name.
func TestCacheTurnCostDividesByDistinctTurnsNotRequests(t *testing.T) {
	requests := []MemberCacheRequest{
		turnSample("m1", "turn-1", 10_000, 9_000, 1_000),
		turnSample("m1", "turn-1", 10_100, 9_000, 1_100),
		turnSample("m1", "turn-1", 10_200, 9_000, 1_200),
		turnSample("m1", "turn-2", 10_300, 9_000, 1_300),
	}
	report := BuildCacheReport(CacheReportInput{Requests: requests, GeneratedAt: time.Now()})
	cost := report.TurnCost
	if cost.Turns != 2 || cost.Requests != 4 {
		t.Fatalf("turns=%d requests=%d, want 2 turns over 4 requests", cost.Turns, cost.Requests)
	}
	missPerTurn, ok := cost.MissTokensPerTurn()
	if !ok || missPerTurn != 2_300 {
		t.Fatalf("miss/turn = %v (ok=%v), want 4600/2 = 2300", missPerTurn, ok)
	}
	requestsPerTurn, ok := cost.RequestsPerTurn()
	if !ok || requestsPerTurn != 2 {
		t.Fatalf("requests/turn = %v (ok=%v), want 4/2 = 2", requestsPerTurn, ok)
	}
}

// TestCacheTurnCostRefusesAPerTurnFigureWithoutTurns pins that a report over
// samples with no turn identity has no per-turn cost rather than a zero one, and
// that the samples it could not count are disclosed.
func TestCacheTurnCostRefusesAPerTurnFigureWithoutTurns(t *testing.T) {
	requests := []MemberCacheRequest{
		reportSample("m1", 10_000, 9_000, 1_000),
		reportSample("m1", 10_100, 9_000, 1_100),
	}
	report := BuildCacheReport(CacheReportInput{Requests: requests, GeneratedAt: time.Now()})
	if report.TurnCost.Turns != 0 {
		t.Fatalf("turns = %d, want none: no sample carried a turn identity", report.TurnCost.Turns)
	}
	if report.TurnCost.RequestsWithoutTurn != 2 {
		t.Fatalf("requests_without_turn = %d, want both samples disclosed", report.TurnCost.RequestsWithoutTurn)
	}
	if _, ok := report.TurnCost.MissTokensPerTurn(); ok {
		t.Fatal("a report with no turn must not publish a per-turn figure")
	}
	if _, ok := report.MaintenanceCost.RotationsPer100Turns(0); ok {
		t.Fatal("a report with no turn must not publish a per-100-turn figure")
	}
}

// TestCacheMaintenanceCostCountsColdRotationsApartFromTheFirstSession pins the
// rescue cost the plan asks to be measured separately: a rotation's cold prefix
// is charged to the rescue, while the writer's own first session is not.
func TestCacheMaintenanceCostCountsColdRotationsApartFromTheFirstSession(t *testing.T) {
	opener := turnSample("m1", "turn-1", 10_000, 0, 10_000)
	opener.SessionOrdinal = 1
	rotated := turnSample("m1", "turn-9", 30_000, 0, 30_000)
	rotated.HasPrevRequest = true
	rotated.SessionIDHash = "ffff"
	rotated.SessionOrdinal = 2
	rotated.SessionFirstRequestSeq = 9
	rotated.SessionRequestSeq = 9
	requests := []MemberCacheRequest{opener, rotated}
	report := BuildCacheReport(CacheReportInput{Requests: requests, GeneratedAt: time.Now()})
	cost := report.MaintenanceCost
	if cost.Rotations != 1 {
		t.Fatalf("rotations = %d, want the one rotated session counted", cost.Rotations)
	}
	if cost.ColdStartMissTokens != 40_000 {
		t.Fatalf("cold_start_miss_tokens = %d, want both cold prefixes", cost.ColdStartMissTokens)
	}
	if cost.FirstSessionColdMissTokens != 10_000 {
		t.Fatalf("first_session_cold_miss_tokens = %d, want only the writer's own opener", cost.FirstSessionColdMissTokens)
	}
	per100, ok := cost.RotationsPer100Turns(report.TurnCost.Turns)
	if !ok || per100 != 50 {
		t.Fatalf("rotations/100 turns = %v (ok=%v), want 1 rotation over 2 turns = 50", per100, ok)
	}
}

// TestCacheMaintenanceCostCountsRewriteAnnouncements pins that a fold's
// announcement is what gets counted, and that the report says so: a fold is
// announced on the request that follows it, so the count is not an operation
// count.
func TestCacheMaintenanceCostCountsRewriteAnnouncements(t *testing.T) {
	folded := turnSample("m1", "turn-2", 40_000, 30_000, 10_000)
	folded.HasPrevRequest = true
	folded.PrefixChanged = true
	folded.PrefixChangeReasons = []string{"compact_auto"}
	report := BuildCacheReport(CacheReportInput{Requests: []MemberCacheRequest{folded}, GeneratedAt: time.Now()})
	if report.MaintenanceCost.RewriteRequests != 1 {
		t.Fatalf("rewrite_requests = %d, want the announcing request counted", report.MaintenanceCost.RewriteRequests)
	}
	unobservable := CacheReportUnobservable()
	joined := strings.Join(unobservable, "\n")
	if !strings.Contains(joined, "announcement counts") {
		t.Fatalf("the unobservable list must name the maintenance-count caveat:\n%s", joined)
	}
}

// TestCacheReportPublishesWhatItCannotMeasure pins that the report states its own
// gaps. A reader must be able to see, from the report alone, that task quality
// and provider latency are absent rather than zero.
func TestCacheReportPublishesWhatItCannotMeasure(t *testing.T) {
	gaps := CacheReportUnobservable()
	if len(gaps) < 4 {
		t.Fatalf("unobservable list = %v, want the plan's unmeasurable metrics named", gaps)
	}
	joined := strings.Join(gaps, "\n")
	for _, want := range []string{"task quality", "latency", "provider cache key"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("unobservable list does not name %q:\n%s", want, joined)
		}
	}
}
