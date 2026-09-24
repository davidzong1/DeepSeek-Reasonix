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

// cacheMemberRecordSource labels a report built from the member writers' own
// retained records. The audit's ledger source is a different dataset and the
// reports must be tellable apart, so both name themselves.
const cacheMemberRecordSource = "member records (owner writer)"

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
		Source:          cacheMemberRecordSource,
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
	// The writer's maintenance spend is the session's own cumulative count, so it
	// travels with the session ledger rather than with any one request. Absent on
	// a document written before it existed, which is unknown rather than zero.
	out.MaintenancePublished = usage.Maintenance != nil
	if usage.Maintenance != nil {
		out.Maintenance = team.CacheSessionMaintenance{
			SummaryRequests:    usage.Maintenance.SummaryRequests,
			ProjectionInstalls: usage.Maintenance.ProjectionInstalls,
			RescueCount:        usage.Maintenance.RescueCount,
			RepeatBlocks:       usage.Maintenance.RepeatBlocks,
		}
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

// renderCacheCoverage renders the field-coverage ledger, which is what says how
// much of the report rests on dimensions that were actually present. It is
// printed above every rate because a rate read without it is a claim about a
// population the reader has not seen.
func renderCacheCoverage(report team.CacheReport) string {
	c := report.Coverage
	if c.Scoped == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "coverage over %d scoped samples: request_count measured=%d defaulted=%d unrecorded=%d unrecognized=%d\n",
		c.Scoped, c.RequestCountObserved, c.RequestCountDefaulted, c.RequestCountUnrecorded, c.RequestCountUnrecognized)
	fmt.Fprintf(&b, "  route_bucket=%d/%d model_ref=%d/%d usage_source=%d/%d prefix_diagnostics=%d/%d (present/scoped)\n",
		c.RouteBucketPresent, c.Scoped, c.ModelRefPresent, c.Scoped,
		c.UsageSourcePresent, c.Scoped, c.DiagnosticsPresent, c.Scoped)
	fmt.Fprintf(&b, "  session_identity=%d/%d (present/scoped); a sample without one cannot have its cold status decided\n",
		c.SessionIdentityPresent, c.Scoped)
	fmt.Fprintf(&b, "  message_shape_comparable=%d/%d (scoped); a sample without it cannot be told apart from a rewrite\n",
		c.MessageShapeComparable, c.Scoped)
	return b.String()
}

// renderCacheMissCauses renders the partition of received samples by the local
// event that can explain each miss. It is the report's answer to "which of these
// misses did this repository cause", and its last rows are the classes that say
// "none of the above" — which is the honest answer more often than not.
func renderCacheMissCauses(report team.CacheReport) string {
	causes := report.MissCauses
	if len(causes.Causes) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "miss causes over %d scoped samples (partition; append allowance %d tok):\n",
		causes.ScopedRequests, causes.AppendBlockAllowance)
	for _, stat := range causes.Causes {
		share := 0.0
		if causes.ScopedMissTokens > 0 {
			share = float64(stat.MissTokens) / float64(causes.ScopedMissTokens)
		}
		fmt.Fprintf(&b, "  %-30s requests=%-6d eligible=%-6d miss=%-10d (%.0f%% of scoped miss)\n",
			stat.Cause, stat.Requests, stat.BaselineEligible, stat.MissTokens, share*100)
	}
	if causes.UncheckedAppendSamples > 0 {
		fmt.Fprintf(&b, "  note: %d residual samples had no predecessor prompt, so the append split could not be made for them\n",
			causes.UncheckedAppendSamples)
	}
	return b.String()
}

// renderCacheTurnCost renders the per-turn cost ledger, the maintenance cost and
// the list of what this dataset cannot measure. Every per-turn figure prints n/a
// rather than a fabricated zero when the samples carried no turn identity, and
// the unobservable list travels with the numbers so an absent metric is never
// read as a zero one.
func renderCacheTurnCost(turns team.CacheTurnTotals, maintenance team.CacheMaintenanceCost) string {
	var b strings.Builder
	fmt.Fprintf(&b, "per logical turn (turns=%d requests=%d requests_without_turn=%d)\n",
		turns.Turns, turns.Requests, turns.RequestsWithoutTurn)
	fmt.Fprintf(&b, "  prompt/turn=%s hit/turn=%s miss/turn=%s completion/turn=%s requests/turn=%s\n",
		formatPerTurn(turns.PromptTokensPerTurn()), formatPerTurn(turns.HitTokensPerTurn()),
		formatPerTurn(turns.MissTokensPerTurn()), formatPerTurn(turns.CompletionTokensPerTurn()),
		formatPerTurn(turns.RequestsPerTurn()))
	fmt.Fprintf(&b, "maintenance cost (announcement counts, not operation counts)\n")
	fmt.Fprintf(&b, "  rewrite_requests=%d structural_requests=%d rotations=%d rotations_per_100_turns=%s rewrite_requests_per_turn=%s\n",
		maintenance.RewriteRequests, maintenance.StructuralRequests, maintenance.Rotations,
		formatPerTurn(maintenance.RotationsPer100Turns(turns.Turns)),
		formatPerTurn(maintenance.RewriteRequestsPerTurn(turns.Turns)))
	fmt.Fprintf(&b, "  cold_start_miss_tokens=%d (first session %d, rotations %d)\n",
		maintenance.ColdStartMissTokens, maintenance.FirstSessionColdMissTokens,
		maintenance.ColdStartMissTokens-maintenance.FirstSessionColdMissTokens)
	if len(maintenance.FinishReasons) > 0 {
		fmt.Fprintf(&b, "  finish reasons (a completion signal, not a task-quality verdict): %s\n",
			strings.Join(maintenance.FinishReasons, ", "))
	}
	b.WriteString("what this dataset cannot measure (stated here, never reported as zero)\n")
	for _, line := range team.CacheReportUnobservable() {
		fmt.Fprintf(&b, "  - %s\n", line)
	}
	return b.String()
}

// formatPerTurn renders a per-turn figure that may have no denominator.
func formatPerTurn(value float64, ok bool) string {
	if !ok {
		return "n/a"
	}
	return fmt.Sprintf("%.0f", value)
}

// renderCacheAllSamples renders the whole-input token ledger beside the
// baseline. The baseline answers "how were the requests I could measure
// served"; this answers "how were all the tokens I read served", and the two are
// different numbers whenever anything was excluded.
func renderCacheAllSamples(report team.CacheReport) string {
	totals := report.AllSamplesTotals()
	if totals.HitTokens+totals.MissTokens <= 0 {
		return ""
	}
	return fmt.Sprintf("all samples (baseline + booked exclusions, never a per-request rate): "+
		"hit=%d miss=%d weighted=%s  booked: aggregate hit=%d miss=%d  unverified_count hit=%d miss=%d\n",
		totals.HitTokens, totals.MissTokens, formatCacheRate(totals.Rate()),
		report.Exclusions.AggregateHitTokens, report.Exclusions.AggregateMissTokens,
		report.Exclusions.UnverifiedHitTokens, report.Exclusions.UnverifiedMissTokens)
}

// renderCacheReport renders the human summary. Every rate is printed with its
// sample counts, so a number is never read without the weight behind it.
func renderCacheReport(report team.CacheReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "window: %s .. %s\n", orDash(report.WindowFrom), orDash(report.WindowTo))
	fmt.Fprintf(&b, "generated: %s  build: %s/%s\n", report.GeneratedAt, orDash(report.CodeVersion), orDash(report.CodeCommit))
	fmt.Fprintf(&b, "source: %s\n", orDash(report.Source))
	fmt.Fprintf(&b, "bucket key: %s  gates: >=%d requests, >=%d members  low-hit threshold: %.0f%%\n",
		report.BucketKeyField, report.MinRequests, report.MinMembers, report.LowHitThreshold*100)
	if report.Exclusions.Received == 0 {
		fmt.Fprintf(&b, "records: none retained yet — a writable member writer publishes them while a Team session runs\n")
	}
	fmt.Fprintf(&b, "members: %d (%s)\n", len(report.Members), orDash(strings.Join(report.Members, ", ")))
	fmt.Fprintf(&b, "models: %s\n", orDash(strings.Join(report.ModelRefs, ", ")))
	fmt.Fprintf(&b, "routes: %s\n", orDash(strings.Join(report.RouteBuckets, ", ")))
	fmt.Fprintf(&b, "prompt basis: context=%d fallback=%d missing=%d\n",
		report.PromptBasis.ContextPrompt, report.PromptBasis.PromptFallback, report.PromptBasis.Missing)
	fmt.Fprintf(&b, "exclusions: received=%d included=%d outside_window=%d unknown=%d estimated=%d aggregate=%d accounting_invalid=%d no_split=%d unparsable_ts=%d non_member=%d unverified_count=%d\n",
		report.Exclusions.Received, report.Exclusions.Included, report.Exclusions.OutsideWindow,
		report.Exclusions.UnknownUsage, report.Exclusions.EstimatedUsage, report.Exclusions.AggregateRequests,
		report.Exclusions.AccountingInvalid, report.Exclusions.NoCacheSplit,
		report.Exclusions.UnparsableObserved, report.Exclusions.NonMemberScope,
		report.Exclusions.UnverifiedRequestCount)
	b.WriteString(renderCacheCoverage(report))
	b.WriteString(renderCacheAllSamples(report))
	b.WriteString(renderCacheMissCauses(report))
	b.WriteString(renderCacheTurnCost(report.TurnCost, report.MaintenanceCost))
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
	m := report.Sessions.Maintenance
	fmt.Fprintf(&b, "  maintenance spend: summary_requests=%d projection_installs=%d rescues=%d repeat_blocks=%d (published by %d of %d members)\n",
		m.SummaryRequests, m.ProjectionInstalls, m.RescueCount, m.RepeatBlocks,
		report.Sessions.MaintenanceMembers, report.Sessions.Totals.Members)
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
	fmt.Fprintf(&b, "      coverage: received=%d included=%d excluded=%d%s  mean_prompt=%.0f  hit/req=%.0f miss/req=%.0f\n",
		group.Coverage.Received, group.Coverage.Included, group.Coverage.Excluded,
		formatExclusionReasons(group.Coverage.Reasons), group.MeanPromptTokens,
		group.HitTokensPerRequest, group.MissTokensPerRequest)
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
		{"unverified_count", reasons.UnverifiedRequestCount},
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
