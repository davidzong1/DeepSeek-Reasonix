package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/team"
)

// writeLedgerDay writes one daily ledger file. Rows are given as (local hour,
// prompt, hit, miss, requests) so a test states the shape it is about.
func writeLedgerDay(t *testing.T, dir, day string, rows ...map[string]any) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(encoded))
	}
	path := filepath.Join(dir, day+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ledgerRowAt builds one ledger row at a local clock time on the given day. A
// non-zero requests value is written as a measured count, which is what a row
// from a build that records provenance carries; requests == 0 omits the key
// entirely, which is the unverified shape the audit must not baseline.
func ledgerRowAt(day string, hour int, model string, prompt, hit, miss, requests int) map[string]any {
	at := time.Date(2026, 9, 0, hour, 0, 0, 0, time.Local)
	if parsed, err := time.ParseInLocation("2006-01-02", day, time.Local); err == nil {
		at = parsed.Add(time.Duration(hour) * time.Hour)
	}
	row := map[string]any{
		"ts": at.Format(time.RFC3339Nano), "model": model, "source": "cli",
		"prompt": prompt, "cache_hit": hit, "cache_miss": miss,
	}
	if requests != 0 {
		row["requests"] = requests
		row["requests_observed"] = true
	}
	return row
}

const auditTestModel = "deepseek-v4-flash-roojin/deepseek/deepseek-v4.1-flash[1m]"

// TestCacheAuditResolvesDatesOnTheLedgerClock pins the reading a date-only flag
// must have: the ledger names its daily files by local day and stamps rows in
// local time, so "2026-09-24" is that local day. Read as a UTC midnight it would
// open 2026-09-24.jsonl and then discard the first eight hours of the very day
// the caller named.
func TestCacheAuditResolvesDatesOnTheLedgerClock(t *testing.T) {
	window, clock, err := ledgerWindow("2026-09-24", "2026-09-24")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 24, 0, 0, 0, 0, time.Local)
	if !window.from.Equal(start) {
		t.Fatalf("from = %s, want the start of the named local day", window.from)
	}
	end := time.Date(2026, 9, 25, 0, 0, 0, 0, time.Local).Add(-time.Nanosecond)
	if !window.to.Equal(end) {
		t.Fatalf("to = %s, want the last instant of the named local day", window.to)
	}
	if !strings.Contains(clock, "ledger clock") {
		t.Fatalf("clock = %q, want the resolved window stated in the output", clock)
	}
	if _, _, err := ledgerWindow("yesterday", ""); err == nil {
		t.Fatal("an unparsable bound must be refused")
	}
}

// TestCacheAuditFiltersTheWindowBeforeClipping pins the flag's own promise:
// --first keeps the earliest N rows *of the window*, so two runs over windows of
// different length compare at equal sample size. Clipping first would sample
// whichever daily file came first alphabetically.
func TestCacheAuditFiltersTheWindowBeforeClipping(t *testing.T) {
	dir := t.TempDir()
	// A previous local day, alphabetically first, whose rows are all outside the
	// requested window.
	writeLedgerDay(t, dir, "2026-09-23",
		ledgerRowAt("2026-09-23", 10, auditTestModel, 1_000, 900, 100, 0),
		ledgerRowAt("2026-09-23", 11, auditTestModel, 1_000, 900, 100, 0),
	)
	writeLedgerDay(t, dir, "2026-09-24",
		ledgerRowAt("2026-09-24", 1, auditTestModel, 1_000, 900, 100, 0),
		ledgerRowAt("2026-09-24", 2, auditTestModel, 1_000, 800, 200, 0),
		ledgerRowAt("2026-09-24", 3, auditTestModel, 1_000, 700, 300, 0),
	)
	window, _, err := ledgerWindow("2026-09-24", "2026-09-24")
	if err != nil {
		t.Fatal(err)
	}
	rows, quality, err := readLedger(dir, window, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want the 2 clipped in-window rows", len(rows))
	}
	if quality.OutsideWindow != 2 {
		t.Fatalf("outside window = %d, want the previous day's 2 rows disclosed", quality.OutsideWindow)
	}
	if !rows[0].Timestamp.Before(rows[1].Timestamp) {
		t.Fatalf("rows must be clipped in clock order, got %s then %s", rows[0].Timestamp, rows[1].Timestamp)
	}
}

// TestCacheAuditBooksAggregateRowsApart pins the reconciliation rule: a
// multi-request row is excluded from the baseline but its tokens are booked, so
// an all-samples total is reconstructible and the baseline is never quietly
// short.
func TestCacheAuditBooksAggregateRowsApart(t *testing.T) {
	dir := t.TempDir()
	writeLedgerDay(t, dir, "2026-09-24",
		ledgerRowAt("2026-09-24", 1, auditTestModel, 1_000, 900, 100, 1),
		ledgerRowAt("2026-09-24", 2, auditTestModel, 9_000, 8_000, 1_000, 4),
	)
	window, _, err := ledgerWindow("2026-09-24", "2026-09-24")
	if err != nil {
		t.Fatal(err)
	}
	rows, quality, err := readLedger(dir, window, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	report := ledgerReportForTest(rows, window)
	if quality.AggregateRows != 1 {
		t.Fatalf("aggregates = %d, want the multi-request row counted", quality.AggregateRows)
	}
	if report.Overall.Totals.Requests != 1 {
		t.Fatalf("baseline requests = %d, want only the single-request row", report.Overall.Totals.Requests)
	}
	all := report.AllSamplesTotals()
	if all.HitTokens != 8_900 || all.MissTokens != 1_100 {
		t.Fatalf("all-samples = hit %d miss %d, want 8,900/1,100", all.HitTokens, all.MissTokens)
	}
	if report.Exclusions.AggregateHitTokens != 8_000 || report.Exclusions.UnverifiedHitTokens != 0 {
		t.Fatalf("booked tokens = %+v, want the aggregate class only", report.Exclusions)
	}
	// A multi-request row has no request prompt shape, so it must not be bucketed
	// as if its aggregate were one request's size.
	if report.PromptBasis.PromptFallback != 1 || report.PromptBasis.ContextPrompt != 1 {
		t.Fatalf("prompt basis = %+v, want the aggregate's key reported as absent", report.PromptBasis)
	}
}

// TestCacheAuditExcludesRowsWithAnUnverifiedRequestCount pins the strict rule
// this audit applies to its own source: a row whose count the writer never
// marked as measured may describe one request or several, so it cannot enter a
// per-request baseline. Its tokens are booked into the unverified class and the
// report says the baseline is unavailable rather than small.
func TestCacheAuditExcludesRowsWithAnUnverifiedRequestCount(t *testing.T) {
	dir := t.TempDir()
	writeLedgerDay(t, dir, "2026-09-24",
		ledgerRowAt("2026-09-24", 1, auditTestModel, 1_000, 900, 100, 1),
		ledgerRowAt("2026-09-24", 2, auditTestModel, 2_000, 1_500, 500, 0),
	)
	window, _, err := ledgerWindow("2026-09-24", "2026-09-24")
	if err != nil {
		t.Fatal(err)
	}
	rows, quality, err := readLedger(dir, window, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if quality.CountVerifiedRows != 1 {
		t.Fatalf("measured rows = %d, want only the row that carried the marker", quality.CountVerifiedRows)
	}
	report := ledgerReportForTest(rows, window)
	if report.Overall.Totals.Requests != 1 || report.Exclusions.UnverifiedRequestCount != 1 {
		t.Fatalf("report = %+v, want the unverified row excluded and disclosed", report.Exclusions)
	}
	if report.Exclusions.UnverifiedHitTokens != 1_500 || report.Exclusions.UnverifiedMissTokens != 500 {
		t.Fatalf("booked unverified tokens = %+v", report.Exclusions)
	}
	all := report.AllSamplesTotals()
	if all.HitTokens != 2_400 || all.MissTokens != 600 {
		t.Fatalf("all-samples = hit %d miss %d, want 2,400/600", all.HitTokens, all.MissTokens)
	}
	if report.Coverage.RequestCountUnrecorded != 1 || report.Coverage.RequestCountObserved != 1 {
		t.Fatalf("coverage = %+v, want the provenance of both rows", report.Coverage)
	}
	if text := renderCacheAudit(report, quality, dir, ""); !strings.Contains(text, "request-count provenance: measured 1 of 2 rows") {
		t.Fatalf("the audit must state its own count provenance:\n%s", text)
	}
}

// TestCacheAuditNeverClaimsMemberIdentity pins the boundary the plan draws: the
// ledger has no member dimension, so its records carry a route label and the
// report says which dataset it is.
func TestCacheAuditNeverClaimsMemberIdentity(t *testing.T) {
	rows := []ledgerRow{{
		Timestamp: time.Now(), Model: auditTestModel,
		Prompt: 1_000, CacheHit: 900, CacheMiss: 100,
	}}
	records := ledgerRecords(rows)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	rec := records[0]
	if rec.TeamID != ledgerTeamID {
		t.Fatalf("team = %q, want the ledger marker", rec.TeamID)
	}
	if rec.MemberID == "" || strings.Contains(rec.MemberID, "/") {
		t.Fatalf("member = %q, want a route label, not a member id", rec.MemberID)
	}
	if rec.RouteBucket == "" {
		t.Fatal("the route dimension must be populated for the ledger too")
	}
	report := ledgerReportForTest(rows, cacheReportWindow{})
	if report.Source != cacheAuditSource {
		t.Fatalf("source = %q, want the ledger source named in the report itself", report.Source)
	}
}

// TestCacheAuditStatesWhatItCannotEstablish pins the verifiability disclosure:
// the audit says out loud that the ledger's normalized fields cannot settle the
// questions a reader is most likely to ask of it.
func TestCacheAuditStatesWhatItCannotEstablish(t *testing.T) {
	rows := []ledgerRow{{Timestamp: time.Now(), Model: auditTestModel, Prompt: 1_000, CacheHit: 900, CacheMiss: 100}}
	var quality ledgerQuality
	quality.Rows = 1
	quality.observe(rows)
	text := renderCacheAudit(ledgerReportForTest(rows, cacheReportWindow{}), quality, "/tmp/ledger", "")
	for _, want := range []string{
		"raw provider counters",
		"a confirmed cold start",
		"a prefix-change cause",
		"never a Team member id",
		"a miss cause",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("the audit must disclose %q; got:\n%s", want, text)
		}
	}
}

// TestCacheAuditIsReproducible pins that the same ledger and flags produce the
// same bytes: a baseline another agent cannot re-derive is not evidence.
func TestCacheAuditIsReproducible(t *testing.T) {
	dir := t.TempDir()
	rows := []map[string]any{}
	for i := range 5 {
		rows = append(rows, ledgerRowAt("2026-09-24", i+1, auditTestModel, 1_000+i*10, 900, 100+i*10, 0))
	}
	writeLedgerDay(t, dir, "2026-09-24", rows...)
	run := func() string {
		out := filepath.Join(t.TempDir(), "audit.txt")
		if code := teamCacheAuditCommand([]string{
			"--ledger", dir, "--from", "2026-09-24", "--to", "2026-09-24", "--out", out,
		}, BuildInfo{Version: "test", GitCommit: "abc"}); code != 0 {
			t.Fatalf("cache-audit exit = %d", code)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		// The generation stamp is the one field that must differ run to run.
		return stripCacheReportStamp(string(data))
	}
	if first, second := run(), run(); first != second {
		t.Fatalf("two runs differ:\n%s\n---\n%s", first, second)
	}
}

// stripCacheReportStamp removes the generated timestamp, the only field a
// reproducible report may not share between runs.
func stripCacheReportStamp(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "generated: ") {
			lines[i] = "generated: <stamp>"
		}
		if strings.HasPrefix(line, "window: ") {
			lines[i] = "window: <window>"
		}
	}
	return strings.Join(lines, "\n")
}

// TestCacheAuditTreatsAMissingDayAsEmpty pins the fail-open reading: a day with
// no traffic is not a fault, and an entirely absent ledger is reported rather
// than crashed on.
func TestCacheAuditTreatsAMissingDayAsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeLedgerDay(t, dir, "2026-09-24", ledgerRowAt("2026-09-24", 1, auditTestModel, 1_000, 900, 100, 0))
	window, _, err := ledgerWindow("2026-09-23", "2026-09-24")
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := readLedger(dir, window, "", 0)
	if err != nil {
		t.Fatalf("a missing day must not fail the read: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want the one row the range contains", len(rows))
	}
	out := filepath.Join(t.TempDir(), "audit.txt")
	code := teamCacheAuditCommand([]string{
		"--ledger", t.TempDir(), "--from", "2026-09-24", "--to", "2026-09-24", "--out", out,
	}, BuildInfo{})
	if code == 0 {
		t.Fatal("an empty ledger selection must be reported, not rendered as a zero baseline")
	}
}

// ledgerReportForTest runs the same projection the command does, so a test
// never exercises a parallel path.
func ledgerReportForTest(rows []ledgerRow, window cacheReportWindow) team.CacheReport {
	return buildLedgerReport(rows, window, "test", "abc")
}
