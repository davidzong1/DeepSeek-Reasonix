package cachelab

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// ledgerRow is one historical statistics row as this recomputation reads it.
// Only the fields the audit needs are taken: a ledger row carries no prompt or
// completion text and this reader does not want any.
type ledgerRow struct {
	TS        string `json:"ts"`
	Model     string `json:"model"`
	CacheHit  *int   `json:"cache_hit"`
	CacheMiss *int   `json:"cache_miss"`
}

// hour is the local hour the ledger itself wrote. It is read straight out of
// the timestamp string rather than converted to UTC: the daily file is named by
// the ledger's own local day, and converting would move rows across the very
// hour boundary the comparison is about.
func (r ledgerRow) hour() string {
	if len(r.TS) < 13 {
		return "??"
	}
	return r.TS[11:13]
}

func (r ledgerRow) hit() int {
	if r.CacheHit == nil {
		return 0
	}
	return *r.CacheHit
}

func (r ledgerRow) miss() int {
	if r.CacheMiss == nil {
		return 0
	}
	return *r.CacheMiss
}

// readLedgerDay decodes one day's rows. A row whose counter keys are absent
// still counts as a row: the writer omits a zero, so the key's absence is a
// zero value, and the row is disclosed separately rather than dropped.
func readLedgerDay(t *testing.T, path string) []ledgerRow {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no ledger day at %s: %v", path, err)
	}
	var out []ledgerRow
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row ledgerRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		out = append(out, row)
	}
	return out
}

// ledgerTotals sums one row set. Rate is the token-weighted rate over the rows
// that reported a split; a set with no split has no rate, which is not 0%.
func ledgerTotals(rows []ledgerRow) (hit, miss int, rate float64, ok bool) {
	for _, row := range rows {
		hit += row.hit()
		miss += row.miss()
	}
	if hit+miss <= 0 {
		return hit, miss, 0, false
	}
	return hit, miss, 100 * float64(hit) / float64(hit+miss), true
}

// TestRecomputeLedgerNumbers replays the historical-ledger recomputation over a
// directory of read-only statistics files, so the follow-up audit's numbers can
// be re-derived from the repository rather than only from the report. It is
// inert unless pointed at a ledger, so it never reads an operator's data in CI:
//
//	PART_C_LEDGER_DIR=~/.reasonix/stats go test ./internal/cachelab/ \
//	  -run TestRecomputeLedgerNumbers -v -count=1
//
// The day set defaults to the two days the earlier reports compared. Every file
// is opened read-only and never written.
func TestRecomputeLedgerNumbers(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("PART_C_LEDGER_DIR"))
	if dir == "" {
		t.Skip("set PART_C_LEDGER_DIR to a read-only statistics directory to recompute its numbers")
	}
	const routePrefix = "deepseek-v4-flash-roojin"
	days := []string{"2026-09-23", "2026-09-24"}

	type dayResult struct {
		day           string
		lines         int
		onRoute       int
		withoutPrompt int
		hit, miss     int
		rate          float64
		hasRate       bool
		hourRows      map[string]int
		hourHit       map[string]int
		hourMiss      map[string]int
		firstN        int
		firstHit      int
		firstMiss     int
	}
	results := map[string]*dayResult{}
	dayOrder := make([]string, 0, len(days))

	for _, day := range days {
		path := filepath.Join(dir, day+".jsonl")
		if _, err := os.Stat(path); err != nil {
			t.Skipf("no ledger day at %s: %v", path, err)
		}
		all := readLedgerDay(t, path)
		res := &dayResult{
			day: day, lines: len(all),
			hourRows: map[string]int{}, hourHit: map[string]int{}, hourMiss: map[string]int{},
		}
		var onRoute []ledgerRow
		for _, row := range all {
			if !strings.HasPrefix(row.Model, routePrefix) {
				continue
			}
			res.onRoute++
			if row.CacheHit == nil && row.CacheMiss == nil {
				res.withoutPrompt++
			}
			res.hit += row.hit()
			res.miss += row.miss()
			hour := row.hour()
			res.hourRows[hour]++
			res.hourHit[hour] += row.hit()
			res.hourMiss[hour] += row.miss()
			onRoute = append(onRoute, row)
		}
		res.rate, res.hasRate = rateOf(res.hit, res.miss)
		// The equal-N rule the earlier reports used: the first 1714 rows of the
		// day in timestamp order.
		slices.SortStableFunc(onRoute, func(a, b ledgerRow) int { return strings.Compare(a.TS, b.TS) })
		res.firstN = min(1714, len(onRoute))
		for _, row := range onRoute[:res.firstN] {
			res.firstHit += row.hit()
			res.firstMiss += row.miss()
		}
		results[day] = res
		dayOrder = append(dayOrder, day)
	}

	for _, day := range dayOrder {
		res := results[day]
		firstRate, _ := rateOf(res.firstHit, res.firstMiss)
		t.Logf("%s: lines=%d on_route=%d no_counters=%d rate=%.2f%% miss/row=%.0f | first_%d rate=%.2f%%",
			day, res.lines, res.onRoute, res.withoutPrompt, res.rate, meanOf(res.miss, res.onRoute),
			res.firstN, firstRate)
		hours := make([]string, 0, len(res.hourRows))
		for hour := range res.hourRows {
			hours = append(hours, hour)
		}
		slices.Sort(hours)
		t.Logf("   hour coverage: %s", strings.Join(hours, ","))
		for _, hour := range hours {
			rate, ok := rateOf(res.hourHit[hour], res.hourMiss[hour])
			if !ok {
				continue
			}
			t.Logf("   %sh rows=%d rate=%.2f%%", hour, res.hourRows[hour], rate)
		}
	}

	if len(dayOrder) != 2 {
		return
	}
	first, second := results[dayOrder[0]], results[dayOrder[1]]
	var common []string
	for hour := range first.hourRows {
		if second.hourRows[hour] > 0 {
			common = append(common, hour)
		}
	}
	slices.Sort(common)
	t.Logf("hours in both days: %s", strings.Join(common, ","))
	for _, day := range dayOrder {
		res := results[day]
		var hit, miss, n int
		for hour := range res.hourRows {
			if !slices.Contains(common, hour) {
				continue
			}
			hit += res.hourHit[hour]
			miss += res.hourMiss[hour]
			n += res.hourRows[hour]
		}
		rate, ok := rateOf(hit, miss)
		if !ok {
			continue
		}
		t.Logf("clock-hour matched %s: n=%d rate=%.2f%% miss/row=%.0f", day, n, rate, meanOf(miss, n))
	}
}

// rateOf is the token-weighted rate of one split, and reports false when there
// is no denominator at all. A set with no split has no rate: it is neither 0%
// nor 100%.
func rateOf(hit, miss int) (float64, bool) {
	if hit+miss <= 0 {
		return 0, false
	}
	return 100 * float64(hit) / float64(hit+miss), true
}

// meanOf divides by a row count, reporting zero when there is nothing to divide.
func meanOf(total, rows int) float64 {
	if rows <= 0 {
		return 0
	}
	return float64(total) / float64(rows)
}
