package cli

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/team"
)

const teamCacheAuditUsage = `usage: reasonix team cache-audit [flags]

Audits the historical per-request statistics ledger (config.StatsDir()) and
renders the same cache baselines as "team cache-report". The ledger has no
member identity and no prefix diagnosis, so its output is route-level and the
report says so: never compare it with a member-level report as one dataset.

flags:
  --ledger DIR       statistics ledger directory (default: the configured stats dir)
  --model PREFIX     only rows whose model ref starts with PREFIX
  --from WHEN        window start (RFC3339 or YYYY-MM-DD)
  --to WHEN          window end (RFC3339 or YYYY-MM-DD)
  --first N          keep only the first N rows after filtering, so two runs over
                     windows of different length can be compared at equal sample size
  --json             emit the report as JSON
  --out FILE         write the output to FILE instead of stdout

What this audit cannot establish (stated in its own output):
  - the provider's raw counters: the adapter normalizes them, so the ledger
    cannot independently verify upstream accounting;
  - a confirmed cold start: hit == 0 means no cache read was reported, and the
    ledger carries no session identity;
  - a prefix-change cause: the ledger predates prefix diagnostics.
`

// ledgerRow is one line of the statistics ledger as this audit reads it. It is
// decoded leniently and with explicit zero defaults: the writer omits zero
// values, so a missing key means "zero", not "absent".
type ledgerRow struct {
	Timestamp  time.Time `json:"ts"`
	Model      string    `json:"model"`
	Source     string    `json:"source"`
	Prompt     int       `json:"prompt"`
	Completion int       `json:"completion"`
	CacheHit   int       `json:"cache_hit"`
	CacheMiss  int       `json:"cache_miss"`
	Requests   int       `json:"requests"`
	// RequestsObserved is the provenance of Requests. A row written before the
	// field existed leaves it false, so its count cannot be told apart from the
	// "zero means one" default and stays unverified.
	RequestsObserved bool `json:"requests_observed"`
}

// requestCountSource names where one ledger row's request count came from, in
// the same closed vocabulary the member records use. The ledger has one extra
// way to be unverified: the field was not written at all. A row the writer
// marked as measured but left without a count is unrecorded too — the marker
// without a count is not a measurement.
func (r ledgerRow) requestCountSource() string {
	if r.RequestsObserved && r.Requests > 0 {
		return team.RequestCountObserved
	}
	return team.RequestCountUnrecorded
}

// ledgerQuality is what the audit can say about the rows it read, before any
// rate is computed. Every count is published with the report.
type ledgerQuality struct {
	Rows             int
	Unparsable       int
	OutsideWindow    int
	MissingSplit     int
	AggregateRows    int
	DoubleCountShape int
	// CountVerifiedRows counts the rows whose request count the writer marked as
	// measured. Every other row carries a count this audit cannot audit, which is
	// why the ledger's per-request baseline is unavailable rather than small.
	CountVerifiedRows int
	ModelRefs         []string
	FirstObserved     string
	LastObserved      string
}

// teamCacheAuditCommand renders one reproducible audit of the historical
// ledger. It only reads, and it never merges the ledger into member-level
// records: the two datasets answer different questions and the report names
// which one it is looking at.
func teamCacheAuditCommand(args []string, info BuildInfo) int {
	fs := flag.NewFlagSet("team cache-audit", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, teamCacheAuditUsage) }
	ledger := fs.String("ledger", "", "statistics ledger directory")
	modelPrefix := fs.String("model", "", "only rows whose model ref starts with PREFIX")
	from := fs.String("from", "", "window start")
	to := fs.String("to", "", "window end")
	first := fs.Int("first", 0, "keep only the first N rows after filtering")
	asJSON := fs.Bool("json", false, "emit the report as JSON")
	out := fs.String("out", "", "write the output to FILE instead of stdout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "team cache-audit: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	window, clock, err := ledgerWindow(*from, *to)
	if err != nil {
		fmt.Fprintln(os.Stderr, "team cache-audit:", err)
		return 2
	}
	dir := strings.TrimSpace(*ledger)
	if dir == "" {
		dir = statsLedgerDir()
	}
	rows, quality, err := readLedger(dir, window, strings.TrimSpace(*modelPrefix), *first)
	if err != nil {
		fmt.Fprintln(os.Stderr, "team cache-audit:", err)
		return 1
	}
	if len(rows) == 0 {
		fmt.Fprintf(os.Stderr, "team cache-audit: no rows in %s for the given window and model filter\n", dir)
		return 1
	}
	resolved := info.withDefaults()
	report := buildLedgerReport(rows, window, resolved.Version, resolved.GitCommit)
	return writeCacheAudit(*out, report, quality, dir, clock, *asJSON)
}

// ledgerWindow resolves the window flags on the ledger's own clock.
//
// The ledger stamps rows with the host's local time and names its daily files by
// local day, so a bare date means that local day. Parsing it as a UTC midnight
// would read 2026-09-24.jsonl and then filter out its first eight hours — a
// silent truncation of the very day the caller named. RFC3339 bounds are taken
// as the instants they spell, and the resolved window is printed so the reading
// is never implicit.
func ledgerWindow(from, to string) (cacheReportWindow, string, error) {
	parse := func(raw, name string) (time.Time, bool, error) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return time.Time{}, false, nil
		}
		if at, err := time.ParseInLocation("2006-01-02", raw, time.Local); err == nil {
			return at, true, nil
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if at, err := time.Parse(layout, raw); err == nil {
				return at.UTC(), false, nil
			}
		}
		return time.Time{}, false, fmt.Errorf("%s: %q is not RFC3339 or YYYY-MM-DD", name, raw)
	}
	var window cacheReportWindow
	fromAt, _, err := parse(from, "--from")
	if err != nil {
		return window, "", err
	}
	toAt, toDay, err := parse(to, "--to")
	if err != nil {
		return window, "", err
	}
	window.from = fromAt
	// A named day is inclusive of the whole day: the bound is the last instant
	// before the next local midnight.
	if toDay {
		toAt = toAt.AddDate(0, 0, 1).Add(-time.Nanosecond)
	}
	window.to = toAt
	clock := ""
	if !window.from.IsZero() || !window.to.IsZero() {
		clock = fmt.Sprintf("window resolved on the ledger clock (%s): %s .. %s",
			time.Local, orDash(formatLedgerInstant(window.from)), orDash(formatLedgerInstant(window.to)))
	}
	return window, clock, nil
}

// formatLedgerInstant renders a resolved bound, or an empty string for an open
// end.
func formatLedgerInstant(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.Format(time.RFC3339)
}

// cacheAuditSource labels a report built from the historical ledger. It is
// deliberately not the member-record source: a reader must be able to tell the
// two apart from the report alone.
const cacheAuditSource = "stats-ledger (route-level, no prefix diagnosis)"

// statsLedgerDir resolves the statistics directory the running host writes to.
// It defers to config.StatsDir() rather than re-deriving the path from an
// environment variable: the state root has several overrides, and an audit that
// resolved its own would read a different tree than the recorder wrote.
func statsLedgerDir() string {
	return config.StatsDir()
}

// readLedger loads the daily files intersecting the window, keeps the rows the
// window actually contains, and reports what it could not use.
//
// The window filter runs before any clipping: --first selects the earliest N
// rows *of the window being compared*, so two runs over windows of different
// length are comparable. Clipping first would sample whichever file happened to
// come first alphabetically, which is the opposite of what the flag promises.
func readLedger(dir string, window cacheReportWindow, modelPrefix string, first int) ([]ledgerRow, ledgerQuality, error) {
	days, err := ledgerDays(dir, window)
	if err != nil {
		return nil, ledgerQuality{}, err
	}
	var rows []ledgerRow
	var quality ledgerQuality
	models := map[string]bool{}
	for _, day := range days {
		dayRows, dayQuality, err := readLedgerDay(filepath.Join(dir, day+".jsonl"), modelPrefix, window)
		if err != nil {
			return nil, ledgerQuality{}, err
		}
		quality.Unparsable += dayQuality.Unparsable
		quality.OutsideWindow += dayQuality.OutsideWindow
		rows = append(rows, dayRows...)
	}
	// Arrival order across files is file order, not clock order; the ledger's own
	// clock is the ordering a reader compares on, so sort before any clipping.
	slices.SortStableFunc(rows, func(a, b ledgerRow) int { return a.Timestamp.Compare(b.Timestamp) })
	for _, row := range rows {
		models[row.Model] = true
	}
	quality.Rows = len(rows)
	quality.ModelRefs = make([]string, 0, len(models))
	for ref := range models {
		quality.ModelRefs = append(quality.ModelRefs, ref)
	}
	slices.Sort(quality.ModelRefs)
	if first > 0 && len(rows) > first {
		rows = rows[:first]
		quality.Rows = len(rows)
	}
	quality.observe(rows)
	return rows, quality, nil
}

// readLedgerDay reads one daily file, keeping only the rows the window contains.
// Undecodable lines are counted, not fatal: the writer can leave a torn trailing
// line after a crash.
func readLedgerDay(path, modelPrefix string, window cacheReportWindow) ([]ledgerRow, ledgerQuality, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ledgerQuality{}, nil
		}
		return nil, ledgerQuality{}, err
	}
	defer f.Close()
	var rows []ledgerRow
	var quality ledgerQuality
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row ledgerRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			quality.Unparsable++
			continue
		}
		if modelPrefix != "" && !strings.HasPrefix(row.Model, modelPrefix) {
			continue
		}
		if !ledgerWindowContains(window, row.Timestamp) {
			quality.OutsideWindow++
			continue
		}
		rows = append(rows, row)
	}
	return rows, quality, sc.Err()
}

// ledgerWindowContains reports whether one row's instant is inside the window.
func ledgerWindowContains(window cacheReportWindow, at time.Time) bool {
	if !window.from.IsZero() && at.Before(window.from) {
		return false
	}
	return window.to.IsZero() || !at.After(window.to)
}

// observe recomputes the data-quality counters over a row set. It is called
// again after clipping so the published counters describe the rows actually
// used, not the rows read.
func (q *ledgerQuality) observe(rows []ledgerRow) {
	q.MissingSplit, q.AggregateRows, q.DoubleCountShape, q.CountVerifiedRows = 0, 0, 0, 0
	q.FirstObserved, q.LastObserved = "", ""
	for _, row := range rows {
		if row.CacheHit+row.CacheMiss <= 0 {
			q.MissingSplit++
		}
		if row.Requests > 1 {
			q.AggregateRows++
		}
		if row.RequestsObserved && row.Requests > 0 {
			q.CountVerifiedRows++
		}
		// The double-count signature: with an inclusive upstream read as
		// exclusive, prompt - 2*hit lands small and positive. It is a heuristic
		// on normalized fields, and the only discriminator this ledger allows.
		if d := row.Prompt - 2*row.CacheHit; d > 0 && d < 500 {
			q.DoubleCountShape++
		}
		stamp := row.Timestamp.UTC().Format(time.RFC3339)
		if q.FirstObserved == "" {
			q.FirstObserved = stamp
		}
		q.LastObserved = stamp
	}
}

// ledgerDays lists the daily file names intersecting the window. An open window
// falls back to a bounded recent range so an unflagged audit still terminates.
func ledgerDays(dir string, window cacheReportWindow) ([]string, error) {
	from, to := window.from, window.to
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var days []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(entry.Name(), ".jsonl")
		if _, err := time.Parse("2006-01-02", day); err != nil {
			continue
		}
		if !from.IsZero() || !to.IsZero() {
			at, err := time.Parse("2006-01-02", day)
			if err != nil {
				continue
			}
			if !from.IsZero() && at.AddDate(0, 0, 1).Before(from) {
				continue
			}
			if !to.IsZero() && at.After(to) {
				continue
			}
		}
		days = append(days, day)
	}
	if len(days) == 0 {
		return nil, fmt.Errorf("no daily ledger files in %s", dir)
	}
	slices.Sort(days)
	return days, nil
}

// buildLedgerReport projects ledger rows into the shared baseline report. The
// command and its tests both call this, so neither can drift from the other.
func buildLedgerReport(rows []ledgerRow, window cacheReportWindow, version, commit string) team.CacheReport {
	return team.BuildCacheReport(team.CacheReportInput{
		Requests: ledgerRecords(rows), From: window.from, To: window.to,
		Source:      cacheAuditSource,
		CodeVersion: version, CodeCommit: commit,
	})
}

// ledgerRecords projects ledger rows onto the report's record shape. The team
// and member ids are synthetic route-level labels: the ledger has no member
// identity, and inventing one would let a route-level rate be quoted as a
// member-level result.
func ledgerRecords(rows []ledgerRow) []team.MemberCacheRequest {
	out := make([]team.MemberCacheRequest, 0, len(rows))
	for _, row := range rows {
		requests := row.Requests
		if requests <= 0 {
			requests = 1
		}
		contextPrompt := 0
		if requests == 1 {
			// prompt == hit + miss by construction, so a single-request row's
			// billable prompt is its own request shape. A multi-request row's is
			// an aggregate over attempts and stays unknown.
			contextPrompt = row.Prompt
		}
		out = append(out, team.MemberCacheRequest{
			ObservedAt: row.Timestamp.UTC().Format(time.RFC3339Nano),
			TeamID:     ledgerTeamID, MemberID: ledgerRouteID(row.Model),
			Provider: ledgerRouteID(row.Model), ModelRef: row.Model,
			RouteBucket:  ledgerRouteID(row.Model),
			PromptTokens: row.Prompt, ContextPromptTokens: contextPrompt,
			CacheHitTokens: row.CacheHit, CacheMissTokens: row.CacheMiss,
			RequestCount: requests, RequestCountSource: row.requestCountSource(),
		})
	}
	return out
}

const ledgerTeamID = "stats-ledger"

// ledgerRouteID names the route a ledger row travelled. The ledger records no
// route, so the model ref's provider segment is the closest honest label.
func ledgerRouteID(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "unknown-route"
	}
	if provider, _, ok := strings.Cut(model, "/"); ok && provider != "" {
		return provider
	}
	return model
}

// renderCacheAudit renders the audit: what was read, what could not be verified,
// and the baselines themselves.
func renderCacheAudit(report team.CacheReport, quality ledgerQuality, dir, clock string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "ledger: %s\n", dir)
	if clock != "" {
		fmt.Fprintf(&b, "%s\n", clock)
	}
	fmt.Fprintf(&b, "source: %s\n", report.Source)
	fmt.Fprintf(&b, "rows in window: %d  outside window: %d  unparsable: %d  observed: %s .. %s\n",
		quality.Rows, quality.OutsideWindow, quality.Unparsable,
		orDash(quality.FirstObserved), orDash(quality.LastObserved))
	fmt.Fprintf(&b, "route labels (not members): %s\n", orDash(strings.Join(quality.ModelRefs, ", ")))
	fmt.Fprintf(&b, "data quality: missing cache split %d  multi-request aggregates %d (booked separately, see exclusions)  "+
		"double-count shape (0 < prompt-2*hit < 500) %d\n",
		quality.MissingSplit, quality.AggregateRows, quality.DoubleCountShape)
	fmt.Fprintf(&b, "request-count provenance: measured %d of %d rows; the rest carry a count no writer marked as measured\n",
		quality.CountVerifiedRows, quality.Rows)
	b.WriteString("\nwhat this audit cannot establish\n")
	fmt.Fprintf(&b, "  - raw provider counters: the adapter normalizes them, so this ledger cannot independently verify upstream accounting\n")
	fmt.Fprintf(&b, "  - a confirmed cold start: hit == 0 means no cache read was reported, and these rows carry no session identity\n")
	fmt.Fprintf(&b, "  - a prefix-change cause: the ledger predates prefix diagnostics, so P1 (prefix moved) and P2 (content grew) are indistinguishable here\n")
	fmt.Fprintf(&b, "  - member attribution: %s is a route label derived from the model ref, never a Team member id\n", ledgerTeamID)
	fmt.Fprintf(&b, "  - the request prompt of a multi-request row: its prompt is an aggregate over attempts, so only single-request rows carry a bucket key\n")
	fmt.Fprintf(&b, "  - a per-request rate over rows whose count is unverified: such a row may describe one request or several, so it is excluded from the per-request baseline and booked into the all-samples total instead\n")
	fmt.Fprintf(&b, "  - a miss cause: the cause partition needs the writer's own session state, which a route-level row never carried, so every row here is reported as undiagnosed rather than attributed\n")
	b.WriteString("\n")
	b.WriteString(renderCacheReport(report))
	return b.String()
}

// writeCacheAudit emits the audit as text or JSON, to stdout or to a file.
func writeCacheAudit(out string, report team.CacheReport, quality ledgerQuality, dir, clock string, asJSON bool) int {
	handle, done, err := cacheReportOutput(out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "team cache-audit:", err)
		return 1
	}
	defer done()
	if asJSON {
		payload := struct {
			team.CacheReport
			Ledger string        `json:"ledger"`
			Clock  string        `json:"ledger_clock,omitempty"`
			Read   ledgerQuality `json:"ledger_read"`
		}{CacheReport: report, Ledger: dir, Clock: clock, Read: quality}
		enc := json.NewEncoder(handle)
		enc.SetIndent("", "  ")
		if err := enc.Encode(payload); err != nil {
			fmt.Fprintln(os.Stderr, "team cache-audit:", err)
			return 1
		}
		return 0
	}
	if _, err := fmt.Fprint(handle, renderCacheAudit(report, quality, dir, clock)); err != nil {
		fmt.Fprintln(os.Stderr, "team cache-audit:", err)
		return 1
	}
	return 0
}
