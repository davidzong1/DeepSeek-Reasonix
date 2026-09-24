package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"reasonix/internal/team"
)

// teamCacheReportUsage documents the team cache-report surface.
const teamCacheReportUsage = `usage: reasonix team cache-report [flags]

Aggregates the per-request cache observations Team members publish into their
owner directories. The report keeps four numbers apart that a single "hit rate"
conflates: the request-level rate, the session-cumulative token-weighted rate,
the equal-weight member mean, and the cross-member token-weighted total.

flags:
  --team NAME        restrict to one team (default: every registered team)
  --member ID        restrict to one member
  --model REF        restrict to one model ref; samples of different models are
                     never merged into one bucket
  --route BUCKET     restrict to one provider cache scope (the route_bucket the
                     member writer recorded); two routes never share a cache
  --from TIME        observation window start (RFC3339 or YYYY-MM-DD)
  --to TIME          observation window end (RFC3339 or YYYY-MM-DD)
  --min-requests N   sample gate per bucket (default 30)
  --min-members N    sample gate per bucket (default 3)
  --low-hit PCT      rate below which a bucket is diagnosed (default 0.90)
  --json             emit the report as JSON
  --export-requests  emit the raw per-request samples as JSONL instead of the
                     aggregate; this is the export the baseline is recomputed from
  --out FILE         write the output to FILE instead of stdout
`

// teamCacheReportCommand renders or exports one reproducible cache baseline.
// It only reads: the observations are written by the member writers themselves,
// and a report is a re-derivation of retained samples, never a new measurement.
// info is the running build's identity, recorded so the baseline names the
// binary its samples came from.
func teamCacheReportCommand(args []string, info BuildInfo) int {
	fs := flag.NewFlagSet("team cache-report", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, teamCacheReportUsage) }
	teamName := fs.String("team", "", "restrict to one team")
	memberID := fs.String("member", "", "restrict to one member")
	modelRef := fs.String("model", "", "restrict to one model ref")
	routeBucket := fs.String("route", "", "restrict to one provider cache scope")
	from := fs.String("from", "", "observation window start")
	to := fs.String("to", "", "observation window end")
	minRequests := fs.Int("min-requests", 0, "sample gate per bucket")
	minMembers := fs.Int("min-members", 0, "sample gate per bucket")
	lowHit := fs.Float64("low-hit", 0, "rate below which a bucket is diagnosed")
	asJSON := fs.Bool("json", false, "emit the report as JSON")
	exportRequests := fs.Bool("export-requests", false, "emit raw per-request samples as JSONL")
	out := fs.String("out", "", "write the output to FILE instead of stdout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "team cache-report: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	window, err := parseCacheReportWindow(*from, *to)
	if err != nil {
		fmt.Fprintln(os.Stderr, "team cache-report:", err)
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "team cache-report:", err)
		return 1
	}
	roots, err := openTeamDataRoots(cwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "team cache-report:", err)
		return 1
	}
	if roots.note != "" {
		fmt.Fprintln(os.Stderr, "team cache-report:", roots.note)
	}
	selection := cacheReportSelection{team: *teamName, member: *memberID, model: *modelRef, route: *routeBucket}
	requests, sessions, err := collectCacheSamples(roots, selection)
	if err != nil {
		fmt.Fprintln(os.Stderr, "team cache-report:", err)
		return 1
	}
	if *exportRequests {
		return writeCacheRequestExport(*out, requests)
	}
	resolved := info.withDefaults()
	report := team.BuildCacheReport(team.CacheReportInput{
		Requests: requests, Sessions: sessions,
		From: window.from, To: window.to,
		MinRequestsPerBucket: *minRequests, MinMembersPerBucket: *minMembers,
		LowHitThreshold: *lowHit,
		CodeVersion:     resolved.Version, CodeCommit: resolved.GitCommit,
	})
	return writeCacheReport(*out, report, *asJSON)
}

// cacheReportSelection is the operator's filter set. The filters exist so a
// baseline is never silently built across models, routes or teams: an
// incompatible group is excluded by naming one, not by averaging it away.
type cacheReportSelection struct {
	team   string
	member string
	model  string
	route  string
}

// cacheReportWindow is the parsed observation window; a zero bound is open.
type cacheReportWindow struct {
	from time.Time
	to   time.Time
}

// parseCacheReportWindow parses the window flags. A date-only value is read as
// the start of that UTC day, so "2026-09-01" to "2026-09-24" selects whole days.
func parseCacheReportWindow(from, to string) (cacheReportWindow, error) {
	var window cacheReportWindow
	parse := func(raw, name string) (time.Time, error) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return time.Time{}, nil
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
			if at, err := time.Parse(layout, raw); err == nil {
				return at.UTC(), nil
			}
		}
		return time.Time{}, fmt.Errorf("%s: %q is not RFC3339 or YYYY-MM-DD", name, raw)
	}
	var err error
	if window.from, err = parse(from, "--from"); err != nil {
		return window, err
	}
	if window.to, err = parse(to, "--to"); err != nil {
		return window, err
	}
	return window, nil
}

// collectCacheSamples reads every selected member's retained requests and its
// published session ledger. Both live in the member's owner directory, so one
// directory enumeration answers both and a member can never be sampled from one
// store and aggregated against another.
//
// The registry is the member list: a slot removed from the roster is no longer
// a member the report claims to describe, even while its owner directory
// survives.
func collectCacheSamples(roots *teamDataRoots, selection cacheReportSelection) ([]team.MemberCacheRequest, []team.CacheSessionTotals, error) {
	if roots == nil || roots.owners == nil {
		return nil, nil, nil
	}
	// Load's bool reports a legacy-file read, not presence. A host with no
	// registry yet reports the missing file as an error, which is a report over
	// no members rather than a failure to report.
	doc, _, err := roots.store.Load()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, err
	}
	ctx := context.Background()
	var requests []team.MemberCacheRequest
	var sessions []team.CacheSessionTotals
	for _, t := range doc.Teams {
		if selection.team != "" && t.Name != selection.team {
			continue
		}
		members, err := roots.owners.MemberIDs(t.Name)
		if err != nil {
			return nil, nil, err
		}
		for _, id := range members {
			if selection.member != "" && id != selection.member {
				continue
			}
			key := team.OwnerKey{TeamID: t.Name, MemberID: id}
			recs, err := roots.owners.ReadCacheRequests(ctx, key)
			if err != nil {
				return nil, nil, err
			}
			requests = append(requests, filterCacheRequests(recs, selection)...)
			if usage, ok, _ := roots.owners.ReadUsage(ctx, key); ok {
				sessions = append(sessions, cacheSessionTotals(key, usage))
			}
		}
	}
	return requests, sessions, nil
}

// filterCacheRequests keeps only the samples a named model or route selects. A
// member that switched models mid-session therefore contributes the selected
// model's requests, never a blend, and two provider cache scopes are compared
// only when the operator named one of them.
func filterCacheRequests(recs []team.MemberCacheRequest, selection cacheReportSelection) []team.MemberCacheRequest {
	if strings.TrimSpace(selection.model) == "" && strings.TrimSpace(selection.route) == "" {
		return recs
	}
	out := make([]team.MemberCacheRequest, 0, len(recs))
	for _, rec := range recs {
		if selection.model != "" && rec.ModelRef != selection.model {
			continue
		}
		if selection.route != "" && rec.RouteBucket != selection.route {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// cacheSessionTotals projects one published document onto the report's session
// input. An absent document contributes no session ledger rather than a zero
// one: a member that never published has no session rate, which is not 0%.
func cacheSessionTotals(key team.OwnerKey, usage team.OwnerUsage) team.CacheSessionTotals {
	out := team.CacheSessionTotals{
		TeamID: key.TeamID, MemberID: key.MemberID,
		CacheHit: usage.CacheHit, CacheMiss: usage.CacheMiss,
	}
	if usage.LastTurn != nil {
		out.LastTurnHit = usage.LastTurn.CacheHitTokens
		out.LastTurnMiss = usage.LastTurn.CacheMissTokens
	}
	return out
}

// writeCacheRequestExport emits the raw samples one JSON object per line, which
// is the form a later report is recomputed from. The export carries exactly the
// retained records: no prompt, tool argument or deliverable text exists in them
// to leak.
func writeCacheRequestExport(out string, requests []team.MemberCacheRequest) int {
	file, closeFile, err := cacheReportOutput(out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "team cache-report:", err)
		return 1
	}
	defer closeFile()
	enc := json.NewEncoder(file)
	for _, rec := range requests {
		if err := enc.Encode(rec); err != nil {
			fmt.Fprintln(os.Stderr, "team cache-report:", err)
			return 1
		}
	}
	return 0
}

// writeCacheReport emits the aggregate. The human form is a summary plus the
// per-bucket table; the JSON form is the whole report, which is what a later
// comparison should be diffed against.
func writeCacheReport(out string, report team.CacheReport, asJSON bool) int {
	file, closeFile, err := cacheReportOutput(out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "team cache-report:", err)
		return 1
	}
	defer closeFile()
	if asJSON {
		enc := json.NewEncoder(file)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintln(os.Stderr, "team cache-report:", err)
			return 1
		}
		return 0
	}
	fmt.Fprint(file, renderCacheReport(report))
	return 0
}

// cacheReportOutput resolves the output sink, defaulting to stdout.
func cacheReportOutput(out string) (*os.File, func(), error) {
	if strings.TrimSpace(out) == "" {
		return os.Stdout, func() {}, nil
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { _ = f.Close() }, nil
}

// renderCacheReport renders the human summary. Every rate is printed with its
// sample counts, so a number is never read without the weight behind it.
func renderCacheReport(report team.CacheReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "window: %s .. %s\n", orDash(report.WindowFrom), orDash(report.WindowTo))
	fmt.Fprintf(&b, "generated: %s  build: %s/%s\n", report.GeneratedAt, orDash(report.CodeVersion), orDash(report.CodeCommit))
	fmt.Fprintf(&b, "bucket key: %s  gates: >=%d requests, >=%d members  low-hit threshold: %.0f%%\n",
		report.BucketKeyField, report.MinRequests, report.MinMembers, report.LowHitThreshold*100)
	fmt.Fprintf(&b, "members: %d (%s)\n", len(report.Members), orDash(strings.Join(report.Members, ", ")))
	fmt.Fprintf(&b, "models: %s\n", orDash(strings.Join(report.ModelRefs, ", ")))
	fmt.Fprintf(&b, "routes: %s\n", orDash(strings.Join(report.RouteBuckets, ", ")))
	fmt.Fprintf(&b, "prompt basis: context=%d fallback=%d missing=%d\n",
		report.PromptBasis.ContextPrompt, report.PromptBasis.PromptFallback, report.PromptBasis.Missing)
	fmt.Fprintf(&b, "exclusions: received=%d included=%d outside_window=%d unknown=%d estimated=%d aggregate=%d accounting_invalid=%d no_split=%d unparsable_ts=%d non_member=%d\n",
		report.Exclusions.Received, report.Exclusions.Included, report.Exclusions.OutsideWindow,
		report.Exclusions.UnknownUsage, report.Exclusions.EstimatedUsage, report.Exclusions.AggregateRequests,
		report.Exclusions.AccountingInvalid, report.Exclusions.NoCacheSplit,
		report.Exclusions.UnparsableObserved, report.Exclusions.NonMemberScope)
	fmt.Fprint(&b, "\noverall (request-level, eligible samples only)\n")
	b.WriteString(renderCacheGroup(report.Overall))
	b.WriteString("\nby prompt bucket\n")
	for _, group := range report.Buckets {
		b.WriteString(renderCacheGroup(group))
	}
	b.WriteString("\nby session stage (labels overlap)\n")
	for _, group := range report.Stages {
		b.WriteString(renderCacheGroup(group))
	}
	b.WriteString("\nby gap since the writer's previous request\n")
	for _, group := range report.Intervals {
		b.WriteString(renderCacheGroup(group))
	}
	b.WriteString("\nreport-level diagnosis (compares strata)\n")
	if len(report.Diagnosis) == 0 {
		b.WriteString("  none: no cross-stratum difference was measured\n")
	}
	for _, finding := range report.Diagnosis {
		fmt.Fprintf(&b, "  [%s] %s\n", finding.Label, finding.Evidence)
	}
	b.WriteString("\nsession cumulative (a different metric from every rate above)\n")
	fmt.Fprintf(&b, "  members=%d tokens: hit=%d miss=%d token_weighted=%s member_simple_mean=%s\n",
		report.Sessions.Totals.Members, report.Sessions.Totals.HitTokens, report.Sessions.Totals.MissTokens,
		formatCacheRate(report.Sessions.TokenWeightedRate, report.Sessions.HasRate),
		formatCacheRate(report.Sessions.MemberSimpleMean, report.Sessions.HasMemberMean))
	fmt.Fprintf(&b, "  last turn across members: token_weighted=%s\n",
		formatCacheRate(report.Sessions.LastTurnWeightedRate, report.Sessions.HasLastTurnRate))
	return b.String()
}

// renderCacheGroup renders one stratum: its rate, the distribution behind it,
// its sampling coverage, and the attribution its samples support.
func renderCacheGroup(group team.CacheGroupStat) string {
	gate := "insufficient_sample"
	if group.SampleGateReached {
		gate = "ok"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  %-16s requests=%-6d members=%-3d hit=%-10d miss=%-10d weighted=%s member_simple_mean=%s p10/p50/p90=%s  [%s]\n",
		group.Key, group.Totals.Requests, group.Totals.Members, group.Totals.HitTokens, group.Totals.MissTokens,
		formatCacheRate(group.Weighted, group.HasRate),
		formatCacheRate(group.MemberSimpleMean, group.HasMemberMean),
		formatPercentiles(group.Requests), gate)
	fmt.Fprintf(&b, "      coverage: received=%d included=%d excluded=%d%s  mean_prompt=%.0f\n",
		group.Coverage.Received, group.Coverage.Included, group.Coverage.Excluded,
		formatExclusionReasons(group.Coverage.Reasons), group.MeanPromptTokens)
	for _, member := range group.MembersOf {
		fmt.Fprintf(&b, "      member %-14s requests=%-6d weighted=%s mean_request=%s\n",
			member.MemberID, member.Requests,
			formatCacheRate(member.WeightedRate, member.HasWeightedRate), formatRate(member.MeanRequestRate))
	}
	for _, finding := range group.Diagnosis.Findings {
		fmt.Fprintf(&b, "      diagnosis [%s] %s\n", finding.Label, finding.Evidence)
	}
	return b.String()
}

// formatExclusionReasons renders a stratum's exclusion counts, or nothing when
// it excluded no sample.
func formatExclusionReasons(reasons team.CacheGroupExclusions) string {
	parts := []string{}
	for _, entry := range []struct {
		name  string
		count int
	}{
		{"unknown", reasons.UnknownUsage},
		{"estimated", reasons.EstimatedUsage},
		{"aggregate", reasons.AggregateRequests},
		{"accounting_invalid", reasons.AccountingInvalid},
		{"no_split", reasons.NoCacheSplit},
		{"unparsable_ts", reasons.UnparsableObserved},
	} {
		if entry.count > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", entry.name, entry.count))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// formatCacheRate renders a rate that may have no denominator. A group with no
// tokens or no members prints n/a rather than a fabricated 0%.
func formatCacheRate(rate float64, ok bool) string {
	if !ok {
		return "n/a"
	}
	return formatRate(rate)
}

// formatPercentiles renders a request-rate distribution, which has no
// percentiles at all when the group holds no eligible request.
func formatPercentiles(stats team.CacheRateStats) string {
	if stats.Requests == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%s/%s/%s", formatRate(stats.P10), formatRate(stats.P50), formatRate(stats.P90))
}

func formatRate(rate float64) string { return fmt.Sprintf("%.1f%%", rate*100) }

func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
