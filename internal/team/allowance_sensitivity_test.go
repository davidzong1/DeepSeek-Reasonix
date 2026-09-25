// Package-internal audit of the append-block allowance (next-round plan §B.2
// items 6–7): the layered distribution the allowance needs, and how the residual
// partition moves when the rule changes. Analysis only — nothing here publishes
// a cause the report's vocabulary does not own.
package team

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

// undecidedAppendSplit is this audit's class for a sample the append split
// cannot decide: no predecessor prompt, a prompt that shrank without a reported
// prefix move, or a size the record never carried. It exists only in the
// sensitivity table. The published partition has no such class, so there it is
// residual unless the rule separates them.
const undecidedAppendSplit CacheMissCause = "undecided_append_split"

// allowanceRule is one attribution rule. The allowance value and the undecided
// policy are the two axes §B.2 item 7 names, so a rule is a pair rather than a
// number: what counts as an expected append, and what the published partition
// does with a sample whose append cannot be measured at all.
type allowanceRule struct {
	Allowance int
	// UndecidedSeparate keeps an unmeasurable append out of the published
	// residual class. It is the strict reading: an append nobody measured is not
	// evidence that the provider failed to cache it.
	UndecidedSeparate bool
}

// defaultAllowanceRules is the rule set the sensitivity table reports: the value
// in force, a smaller one, no exemption at all, and the strict reading of an
// undecided sample. The last two are the conservative directions, so a reader
// who disagrees with 128 can read what each would cost.
func defaultAllowanceRules() []allowanceRule {
	return []allowanceRule{
		{Allowance: cacheAppendBlockAllowance},
		{Allowance: cacheAppendBlockAllowance / 2},
		{Allowance: 0},
		{Allowance: cacheAppendBlockAllowance, UndecidedSeparate: true},
	}
}

// classifyWithRule mirrors classifyCacheMiss with the rule as a parameter and
// returns the sample's excess over the content it appended when the append split
// governs it. It reuses the production prefix and reason helpers, so the only
// restated line is the allowance comparison — which is exactly the line under
// audit. TestAllowanceRuleMatchesProductionAtTheValueInForce pins the
// restatement to production at the value in force.
func classifyWithRule(in cacheMissCauseInput, rule allowanceRule) (CacheMissCause, int, bool) {
	cause, excess, measured := classifyAppendRule(in, rule)
	if cause == undecidedAppendSplit && !rule.UndecidedSeparate {
		cause = CacheCauseProviderResidual
	}
	return cause, excess, measured
}

// classifyAppendRule is the policy-free classification: a sample is measured
// only when the prefix provably did not move and its growth is known. Everything
// else is undecided, never a residual by assumption.
func classifyAppendRule(in cacheMissCauseInput, rule allowanceRule) (CacheMissCause, int, bool) {
	rec := in.rec
	if startsColdPrefix(rec) {
		return CacheCauseColdPrefix, 0, false
	}
	if !rec.DiagnosticsAvailable {
		return CacheCauseUndiagnosed, 0, false
	}
	if rec.StablePrefixChanged || rec.PrefixChanged || len(rec.PrefixChangeReasons) > 0 {
		return prefixMoveCause(rec.PrefixChangeReasons), 0, false
	}
	if rec.MessagesComparable && rec.MessagesRewritten > 0 {
		return CacheCauseMessagesRewritten, 0, false
	}
	if !in.hasPrev || rec.ContextPromptTokens <= 0 || in.prevPrompt <= 0 {
		return undecidedAppendSplit, 0, false
	}
	growth := rec.ContextPromptTokens - in.prevPrompt
	if growth < 0 {
		return undecidedAppendSplit, 0, false
	}
	excess := rec.CacheMissTokens - growth
	if excess <= rule.Allowance {
		return CacheCauseAppendExpected, excess, true
	}
	return CacheCauseProviderResidual, excess, true
}

// allowanceLayer is one stratum of the audit: a route, and within it a
// prompt-size bucket. Account scope is deliberately not a key — no record
// carries one, and route_bucket is a route/pool fingerprint, not an account.
type allowanceLayer struct {
	RouteBucket  string
	PromptBucket string
	Requests     int
	// Measured counts samples whose excess the split could compute; Within and
	// Violations partition it by whether the excess fits the allowance.
	Measured   int
	Within     int
	Violations int
	// Unknown counts the samples the split could not govern: a session's first
	// request, a moved prefix, an unobserved request shape, a rewritten array, or
	// an unmeasurable growth. "Not observed" is not "no excess".
	Unknown int
	// Excess is every measured sample's miss beyond its appended content, in log
	// order, so a row's percentiles are derivable from the row itself.
	Excess []int
}

// allowanceAudit is the layered distribution §B.2 item 6 asks for.
type allowanceAudit struct {
	Allowance      int
	ScopedRequests int
	Layers         []allowanceLayer
	// AccountScopeMeasured is false for every record type this repository
	// journals: the dimension the cross-environment claim needs is not collected,
	// so the audit reports it unmeasured instead of substituting route_bucket.
	AccountScopeMeasured bool
	// ConcurrencyMeasured is likewise false: no record carries an in-flight
	// request count, so a layer's concurrency is unknown rather than 1.
	ConcurrencyMeasured bool
	// UnknownRoute counts records that named no route, which is a coverage gap
	// rather than a layer of its own.
	UnknownRoute int
}

// auditAppendAllowance derives the layered distribution in log order. The
// predecessor prompt comes from the production tracker, so the audit's notion of
// "the request before this one in this writer session" is the report's own.
func auditAppendAllowance(records []MemberCacheRequest, allowance int) allowanceAudit {
	audit := allowanceAudit{Allowance: allowance}
	tracker := newCacheMissCauseTracker()
	index := map[[2]string]int{}
	for i := range records {
		rec := records[i]
		in := tracker.observe(rec)
		audit.ScopedRequests++
		route := strings.TrimSpace(rec.RouteBucket)
		if route == "" {
			audit.UnknownRoute++
		}
		key := [2]string{route, string(CacheRequestBucketOf(rec.ContextPromptTokens))}
		at, ok := index[key]
		if !ok {
			audit.Layers = append(audit.Layers, allowanceLayer{RouteBucket: key[0], PromptBucket: key[1]})
			at = len(audit.Layers) - 1
			index[key] = at
		}
		layer := &audit.Layers[at]
		layer.Requests++
		_, excess, measured := classifyAppendRule(in, allowanceRule{Allowance: allowance})
		if !measured {
			layer.Unknown++
			continue
		}
		layer.Measured++
		layer.Excess = append(layer.Excess, excess)
		if excess <= allowance {
			layer.Within++
		} else {
			layer.Violations++
		}
	}
	slices.SortFunc(audit.Layers, func(a, b allowanceLayer) int {
		if c := strings.Compare(a.RouteBucket, b.RouteBucket); c != 0 {
			return c
		}
		return strings.Compare(a.PromptBucket, b.PromptBucket)
	})
	return audit
}

// allowanceRuleReport is one rule's partition of one sample set. The counts are
// the policy-free facts; the rule decides only whether the undecided samples are
// published inside the residual or beside it.
type allowanceRuleReport struct {
	Rule           allowanceRule
	Scoped         int
	AppendExpected int
	Residual       int
	Undecided      int
	Other          int
	ScopedMiss     int
	AppendMiss     int
	ResidualMiss   int
	UndecidedMiss  int
}

// PublishedResidual is the residual the report would show under this rule: the
// strict residual, plus the samples the rule does not separate.
func (r allowanceRuleReport) PublishedResidual() int {
	if r.Rule.UndecidedSeparate {
		return r.Residual
	}
	return r.Residual + r.Undecided
}

// ResidualShare is the published residual's share of the scoped miss, which is
// the number the whole 128 question is about. It is zero only when nothing was
// scoped, which the caller must state separately.
func (r allowanceRuleReport) ResidualShare() float64 {
	if r.ScopedMiss <= 0 {
		return 0
	}
	miss := r.ResidualMiss
	if !r.Rule.UndecidedSeparate {
		miss += r.UndecidedMiss
	}
	return float64(miss) / float64(r.ScopedMiss)
}

// allowanceRuleReports partitions the same records under every rule, so the
// table's rows differ only by the rule and never by the sample set.
func allowanceRuleReports(records []MemberCacheRequest, rules []allowanceRule) []allowanceRuleReport {
	out := make([]allowanceRuleReport, 0, len(rules))
	for _, rule := range rules {
		tracker := newCacheMissCauseTracker()
		report := allowanceRuleReport{Rule: rule}
		for i := range records {
			in := tracker.observe(records[i])
			cause, _, _ := classifyAppendRule(in, rule)
			report.Scoped++
			report.ScopedMiss += records[i].CacheMissTokens
			switch cause {
			case CacheCauseAppendExpected:
				report.AppendExpected++
				report.AppendMiss += records[i].CacheMissTokens
			case CacheCauseProviderResidual:
				report.Residual++
				report.ResidualMiss += records[i].CacheMissTokens
			case undecidedAppendSplit:
				report.Undecided++
				report.UndecidedMiss += records[i].CacheMissTokens
			default:
				report.Other++
			}
		}
		out = append(out, report)
	}
	return out
}

// allowancePercentile is the nearest-rank percentile over an ascending slice,
// matching how the cache report states its own miss percentiles.
func allowancePercentile(ascending []int, p float64) int {
	if len(ascending) == 0 {
		return 0
	}
	rank := int(float64(len(ascending)) * p)
	if float64(rank) < float64(len(ascending))*p {
		rank++
	}
	if rank < 1 {
		rank = 1
	}
	if rank > len(ascending) {
		rank = len(ascending)
	}
	return ascending[rank-1]
}

// renderAllowanceAudit renders both tables. The dimensions the record cannot
// supply print as unmeasured rather than as a substituted value: an account
// column filled from route_bucket, or a concurrency of 1 nobody observed, is
// exactly the false scope §B.2 item 5 forbids.
func renderAllowanceAudit(audit allowanceAudit, reports []allowanceRuleReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "append-block allowance audit (allowance in force %d tok)\n", audit.Allowance)
	b.WriteString("  account_scope: unrecorded - route_bucket is a route/pool fingerprint, not an account scope\n")
	b.WriteString("  concurrency: n/a - no record carries an in-flight request count\n")
	b.WriteString("  layer                prompt_bucket  requests  measured  within  over_allowance  unknown  excess_p50  excess_p90  excess_max\n")
	for _, layer := range audit.Layers {
		excess := slices.Clone(layer.Excess)
		slices.Sort(excess)
		fmt.Fprintf(&b, "  %-20s %-14s %8d %9d %7d %15d %8d %11d %11d %11d\n",
			emptyDash(layer.RouteBucket), layer.PromptBucket, layer.Requests, layer.Measured,
			layer.Within, layer.Violations, layer.Unknown,
			allowancePercentile(excess, 0.50), allowancePercentile(excess, 0.90), allowancePercentile(excess, 1))
	}
	if audit.UnknownRoute > 0 {
		fmt.Fprintf(&b, "  note: %d records named no route and are counted outside the layers\n", audit.UnknownRoute)
	}
	b.WriteString("rule sensitivity (same sample set, one rule per row)\n")
	b.WriteString("  allowance  undecided_separate  scoped  append_expected  residual  undecided  other  published_residual  residual_miss_share\n")
	for _, report := range reports {
		fmt.Fprintf(&b, "  %9d  %18t  %6d  %15d  %8d  %9d  %5d  %19d  %19s\n",
			report.Rule.Allowance, report.Rule.UndecidedSeparate, report.Scoped, report.AppendExpected,
			report.Residual, report.Undecided, report.Other, report.PublishedResidual(),
			fmt.Sprintf("%.2f%%", report.ResidualShare()*100))
	}
	return b.String()
}

func emptyDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

// allowanceFixtureRecord is one hand-built record for the always-run tests.
func allowanceFixtureRecord(seq, prompt, miss int, reasons ...string) MemberCacheRequest {
	return MemberCacheRequest{
		MemberID: "m1", RouteBucket: "anthropic/aaaaaaaaaaaa",
		SessionIDHash: "writer", SessionOrdinal: 1, SessionRequestSeq: seq,
		HasPrevRequest: seq > 1, DiagnosticsAvailable: true,
		ContextPromptTokens: prompt, PromptTokens: prompt, CacheMissTokens: miss,
		CacheHitTokens:     max(prompt-miss, 0),
		MessagesComparable: true, PrefixChangeReasons: reasons,
	}
}

// TestAllowanceRuleMatchesProductionAtTheValueInForce is the guard that makes
// every other row here trustworthy: at the allowance the report publishes, the
// audit's restated rule must agree with production on every combination of the
// inputs it reads. Without it the sensitivity table would be a second, silently
// diverging truth source for the residual.
func TestAllowanceRuleMatchesProductionAtTheValueInForce(t *testing.T) {
	for _, prev := range []cacheMissCauseInput{
		{hasPrev: false, prevPrompt: 0},
		{hasPrev: true, prevPrompt: 0},
		{hasPrev: true, prevPrompt: 1_000},
	} {
		for _, prompt := range []int{0, 900, 1_000, 1_100} {
			for _, miss := range []int{0, 100, 128, 129, 272} {
				for _, diag := range []bool{true, false} {
					for _, reasons := range [][]string{
						nil,
						{"compact_auto"},
						{"prune"},
						{"messages"},
						{"not_a_reason"},
						{"tools"},
					} {
						for _, rewritten := range []int{0, 3} {
							rec := allowanceFixtureRecord(2, prompt, miss, reasons...)
							rec.DiagnosticsAvailable = diag
							rec.MessagesRewritten = rewritten
							rec.MessageCount = 4
							if !diag {
								rec.PrefixChangeReasons = nil
							}
							in := cacheMissCauseInput{rec: rec, prevPrompt: prev.prevPrompt, hasPrev: prev.hasPrev}
							want := classifyCacheMiss(in)
							got, _, _ := classifyWithRule(in, allowanceRule{Allowance: cacheAppendBlockAllowance})
							if got != want {
								t.Fatalf("rule on %+v: got %q, want production %q", in, got, want)
							}
						}
					}
				}
			}
		}
	}
	// A session's first request is cold in both, including the rotation shape.
	for _, rec := range []MemberCacheRequest{
		{SessionRequestSeq: 1},
		{SessionOrdinal: 2, SessionRequestSeq: 5, SessionFirstRequestSeq: 5},
	} {
		in := cacheMissCauseInput{rec: rec}
		if got, _, _ := classifyWithRule(in, allowanceRule{Allowance: cacheAppendBlockAllowance}); got != classifyCacheMiss(in) {
			t.Fatalf("cold shape %+v: got %q, want %q", rec, got, classifyCacheMiss(in))
		}
	}
}

// TestAllowanceAuditRefusesToReadRouteBucketAsAnAccountScope pins §B.2 item 5
// mechanically: two routes produce two layers, and the unmeasured dimensions are
// stated rather than filled in from a value that means something else.
func TestAllowanceAuditRefusesToReadRouteBucketAsAnAccountScope(t *testing.T) {
	first := allowanceFixtureRecord(1, 10_000, 10_000)
	first.RouteBucket = "anthropic/aaaaaaaaaaaa"
	second := allowanceFixtureRecord(2, 11_000, 1_080)
	second.RouteBucket = "openai/bbbbbbbbbbbb"
	records := []MemberCacheRequest{first, second}
	audit := auditAppendAllowance(records, cacheAppendBlockAllowance)
	if len(audit.Layers) != 2 {
		t.Fatalf("layers = %d, want 2 routes kept apart", len(audit.Layers))
	}
	if audit.AccountScopeMeasured {
		t.Fatal("the audit claimed an account dimension the record type does not carry")
	}
	if audit.ConcurrencyMeasured {
		t.Fatal("the audit claimed a concurrency dimension the record type does not carry")
	}
	table := renderAllowanceAudit(audit, allowanceRuleReports(records, defaultAllowanceRules()))
	if !strings.Contains(table, "account_scope: unrecorded") || !strings.Contains(table, "concurrency: n/a") {
		t.Fatalf("the table must state the two unmeasured dimensions:\n%s", table)
	}
	if !strings.Contains(table, "anthropic/aaaaaaaaaaaa") || !strings.Contains(table, "openai/bbbbbbbbbbbb") {
		t.Fatalf("both routes must be named as layers:\n%s", table)
	}
}

// TestAllowanceLayerAccountingCloses pins the layer invariant: every scoped
// request is either measured or unknown, and every measured request is either
// within the allowance or a violation. A dropped sample would make the counts a
// sample of the data rather than a partition of it.
func TestAllowanceLayerAccountingCloses(t *testing.T) {
	records := []MemberCacheRequest{
		allowanceFixtureRecord(1, 10_000, 10_000),
		allowanceFixtureRecord(2, 11_000, 1_080),
		allowanceFixtureRecord(3, 13_000, 2_272),
		allowanceFixtureRecord(4, 13_500, 772),
		allowanceFixtureRecord(5, 14_000, 762, "compact_auto"),
		allowanceFixtureRecord(6, 13_000, 700),
		allowanceFixtureRecord(7, 15_000, 100),
	}
	records[0].DiagnosticsAvailable = false // undiagnosed
	records[6].MessagesRewritten = 2        // rewritten array, nobody claimed it
	audit := auditAppendAllowance(records, cacheAppendBlockAllowance)
	scoped, measured, unknown := 0, 0, 0
	for _, layer := range audit.Layers {
		if layer.Requests != layer.Measured+layer.Unknown {
			t.Errorf("layer %v: requests %d != measured %d + unknown %d", layer.PromptBucket, layer.Requests, layer.Measured, layer.Unknown)
		}
		if layer.Measured != layer.Within+layer.Violations {
			t.Errorf("layer %v: measured %d != within %d + violations %d", layer.PromptBucket, layer.Measured, layer.Within, layer.Violations)
		}
		if layer.Violations > 0 && len(layer.Excess) == 0 {
			t.Errorf("layer %v: violations without excesses", layer.PromptBucket)
		}
		scoped += layer.Requests
		measured += layer.Measured
		unknown += layer.Unknown
	}
	if scoped != len(records) {
		t.Fatalf("layers hold %d records, want %d", scoped, len(records))
	}
	// Four shapes the split must count apart: undiagnosed, a moved prefix, a
	// shrink, and a rewritten array no operation claimed.
	if unknown != 4 {
		t.Fatalf("unknown = %d, want 4", unknown)
	}
	if measured+unknown != len(records) {
		t.Fatalf("measured %d + unknown %d != %d", measured, unknown, len(records))
	}
}

// TestAllowanceSensitivityMovesTheResidualInTheExpectedDirection pins the
// direction of the sensitivity table, and that the report does not quietly
// prefer the most favourable reading: relaxing the allowance can only move
// samples out of the residual, and the strict reading can only move more in.
func TestAllowanceSensitivityMovesTheResidualInTheExpectedDirection(t *testing.T) {
	records := []MemberCacheRequest{
		allowanceFixtureRecord(1, 10_000, 10_000), // cold
		allowanceFixtureRecord(2, 11_000, 1_080),  // excess 80
		allowanceFixtureRecord(3, 13_000, 2_272),  // excess 272
		allowanceFixtureRecord(4, 13_500, 772),    // excess 272
		allowanceFixtureRecord(5, 16_000, 1_500),  // growth 2500, excess 0
		allowanceFixtureRecord(6, 16_000, 3_000),  // growth 0, excess 3000
		allowanceFixtureRecord(7, 0, 500),         // no prompt shape: undecided
	}
	reports := allowanceRuleReports(records, defaultAllowanceRules())
	if len(reports) != 4 {
		t.Fatalf("reports = %d, want the four declared rules", len(reports))
	}
	inForce, smaller, zero, separate := reports[0], reports[1], reports[2], reports[3]
	if inForce.ResidualShare() > smaller.ResidualShare() {
		t.Errorf("the residual share must not fall when the allowance is halved: 128=%.4f, 64=%.4f", inForce.ResidualShare(), smaller.ResidualShare())
	}
	if smaller.ResidualShare() > zero.ResidualShare() {
		t.Errorf("the residual share must not fall with no exemption: 64=%.4f, 0=%.4f", smaller.ResidualShare(), zero.ResidualShare())
	}
	if zero.ResidualShare() == inForce.ResidualShare() {
		t.Fatal("the fixture does not move under the rule, so the sensitivity table would be vacuous")
	}
	if separate.ResidualShare() > inForce.ResidualShare() {
		t.Errorf("separating the undecided samples must not raise the residual: strict=%.4f, absorbed=%.4f", separate.ResidualShare(), inForce.ResidualShare())
	}
	if inForce.Residual != 3 || inForce.Undecided != 1 {
		t.Errorf("in-force rule: residual=%d undecided=%d, want 3 strict residual and 1 undecided", inForce.Residual, inForce.Undecided)
	}
	if inForce.PublishedResidual() != 4 || separate.PublishedResidual() != 3 {
		t.Errorf("published residual: absorbed=%d strict=%d, want the undecided sample to move out of it",
			inForce.PublishedResidual(), separate.PublishedResidual())
	}
	if inForce.AppendExpected != 2 {
		t.Errorf("append_expected = %d, want the two samples inside the allowance", inForce.AppendExpected)
	}
	for _, report := range reports {
		if report.Scoped != len(records) {
			t.Errorf("rule %+v scoped %d records, want %d: every row must describe the same set", report.Rule, report.Scoped, len(records))
		}
		if report.AppendExpected+report.Residual+report.Undecided+report.Other != report.Scoped {
			t.Errorf("rule %+v does not partition its samples", report.Rule)
		}
	}
}

// TestAllowanceAuditFromARealStore is the measurement itself: it reads a
// writer's archived record log and prints the layered table and the sensitivity
// table. It skips without REASONIX_CACHE_SAMPLE_LOG, because a cross-environment
// distribution can only come from real provider runs:
//
//	REASONIX_CACHE_SAMPLE_LOG=<team>/<member>/.cache_requests.jsonl \
//	  go test ./internal/team/ -run TestAllowanceAuditFromARealStore -v
//
// A log with no measurable append-only sample is reported inconclusive rather
// than passed: the allowance is then neither supported nor contradicted by it.
func TestAllowanceAuditFromARealStore(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("REASONIX_CACHE_SAMPLE_LOG"))
	if path == "" {
		t.Skip("set REASONIX_CACHE_SAMPLE_LOG to a writer's .cache_requests.jsonl to audit the allowance against real samples")
	}
	records, err := readAllowanceSampleLog(path)
	if err != nil {
		t.Fatal(err)
	}
	audit := auditAppendAllowance(records, cacheAppendBlockAllowance)
	reports := allowanceRuleReports(records, defaultAllowanceRules())
	t.Log("\n" + renderAllowanceAudit(audit, reports))

	measured, violations := 0, 0
	routes := map[string]bool{}
	for _, layer := range audit.Layers {
		measured += layer.Measured
		violations += layer.Violations
		if layer.RouteBucket != "" {
			routes[layer.RouteBucket] = true
		}
	}
	if measured == 0 {
		t.Skipf("no measurable append-only sample in %d records: the allowance is inconclusive on this log, not confirmed", len(records))
	}
	if len(routes) < 2 {
		t.Logf("note: %d route(s) in this log, so no cross-route comparison is possible from it", len(routes))
	}
	t.Logf("append allowance %d: %d measurable, %d over allowance, %d route(s)", cacheAppendBlockAllowance, measured, violations, len(routes))
}

// readAllowanceSampleLog reads a writer's JSONL log, skipping a torn line
// exactly as the report reader does.
func readAllowanceSampleLog(path string) ([]MemberCacheRequest, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var records []MemberCacheRequest
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<22)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec MemberCacheRequest
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		records = append(records, rec)
	}
	return records, scanner.Err()
}
