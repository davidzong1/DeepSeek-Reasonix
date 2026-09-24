package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/team"
)

// cacheReportFixture is one registered team with one owner directory, which is
// the shape the report command reads.
type cacheReportFixture struct {
	roots  *teamDataRoots
	owners *team.OwnerStore
	key    team.OwnerKey
}

func newCacheReportFixture(t *testing.T) cacheReportFixture {
	t.Helper()
	dir := t.TempDir()
	owners, err := team.NewOwnerStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := team.NewTeamStoreAt("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddTeam(team.Team{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMember("alpha", team.MemberSlot{MemberID: "ipc-protocol"}); err != nil {
		t.Fatal(err)
	}
	return cacheReportFixture{
		roots:  &teamDataRoots{store: store, owners: owners, dataDir: dir},
		owners: owners, key: team.OwnerKey{TeamID: "alpha", MemberID: "ipc-protocol"},
	}
}

// seed writes one member's retained requests and its published session ledger,
// exactly as a writer would leave them.
func (f cacheReportFixture) seed(t *testing.T, requests []team.MemberCacheRequest, usage team.OwnerUsage) {
	t.Helper()
	ctx := context.Background()
	if err := f.owners.AppendCacheRequests(ctx, f.key, requests...); err != nil {
		t.Fatal(err)
	}
	if err := f.owners.WriteUsage(ctx, f.key, usage); err != nil {
		t.Fatal(err)
	}
}

// reportRequest is one record with the bucket key and cache split under test.
// The request count is marked observed, which is what the baseline now requires:
// a fixture that left the provenance unset would exercise the unverified path by
// accident.
func reportRequest(promptPrompt, hit, miss int, observedAt time.Time) team.MemberCacheRequest {
	return team.MemberCacheRequest{
		ObservedAt: observedAt.UTC().Format(time.RFC3339Nano),
		TeamID:     "alpha", MemberID: "ipc-protocol", ModelRef: "deepseek/deepseek-v4-flash",
		PromptTokens: hit + miss, ContextPromptTokens: promptPrompt,
		CacheHitTokens: hit, CacheMissTokens: miss,
		RequestCount: 1, RequestCountSource: team.RequestCountObserved,
		DiagnosticsAvailable: true,
	}
}

// TestCacheReportCommandReadsOwnerLogsAndSessionLedgers is the consumed
// boundary for the export: a report built from on-disk owner state separates the
// request-level rate from the session-cumulative rate, and discloses the samples
// it excluded rather than averaging them in.
func TestCacheReportCommandReadsOwnerLogsAndSessionLedgers(t *testing.T) {
	f := newCacheReportFixture(t)
	now := time.Now()
	requests := []team.MemberCacheRequest{
		reportRequest(200_000, 90_000, 10_000, now),
		reportRequest(200_000, 80_000, 20_000, now.Add(time.Second)),
	}
	aggregate := reportRequest(200_000, 180_000, 20_000, now.Add(2*time.Second))
	aggregate.RequestCount = 3
	requests = append(requests, aggregate)
	f.seed(t, requests, team.OwnerUsage{
		PublishedAt: now.UTC().Format(time.RFC3339Nano),
		CacheHit:    170_000, CacheMiss: 30_000,
		LastTurn: &team.OwnerUsageLastTurn{CacheHitTokens: 80_000, CacheMissTokens: 20_000},
	})

	collected, sessions, err := collectCacheSamples(f.roots, cacheReportSelection{})
	if err != nil {
		t.Fatal(err)
	}
	if len(collected) != 3 || len(sessions) != 1 {
		t.Fatalf("collected (%d requests, %d sessions), want 3 and 1", len(collected), len(sessions))
	}
	report := team.BuildCacheReport(team.CacheReportInput{
		Requests: collected, Sessions: sessions, GeneratedAt: now,
	})
	if len(report.Buckets) != 1 || report.Buckets[0].Key != string(team.CacheBucket128K256K) {
		t.Fatalf("buckets = %+v, want one 128k_256k bucket", report.Buckets)
	}
	if report.Buckets[0].Totals.Requests != 2 || report.Exclusions.AggregateRequests != 1 {
		t.Fatalf("bucket = %+v exclusions = %+v, want the aggregate disclosed", report.Buckets[0].Totals, report.Exclusions)
	}
	if !strings.Contains(renderCacheReport(report), "aggregate=1") {
		t.Fatalf("the rendered report must disclose the excluded aggregate:\n%s", renderCacheReport(report))
	}
	if rate, ok := report.Sessions.TokenWeightedRate, report.Sessions.HasRate; !ok || rate != 0.85 {
		t.Fatalf("session rate = (%v, %v), want 0.85", rate, report.Sessions.HasRate)
	}
}

// TestCacheReportCommandFiltersNeverMergeModels pins the comparability rule: a
// model filter selects one model's samples, so two models' rates are never
// averaged into one bucket.
func TestCacheReportCommandFiltersNeverMergeModels(t *testing.T) {
	f := newCacheReportFixture(t)
	now := time.Now()
	other := reportRequest(200_000, 10_000, 90_000, now)
	other.ModelRef = "anthropic/claude-sonnet-5"
	f.seed(t, []team.MemberCacheRequest{reportRequest(200_000, 90_000, 10_000, now), other}, team.OwnerUsage{})

	all, _, err := collectCacheSamples(f.roots, cacheReportSelection{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("unfiltered collection = %d records, want both models listed", len(all))
	}
	only, _, err := collectCacheSamples(f.roots, cacheReportSelection{model: "deepseek/deepseek-v4-flash"})
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].ModelRef != "deepseek/deepseek-v4-flash" {
		t.Fatalf("filtered collection = %+v, want only the named model", only)
	}
	none, _, err := collectCacheSamples(f.roots, cacheReportSelection{member: "nobody"})
	if err != nil || len(none) != 0 {
		t.Fatalf("an unmatched member filter = (%d records, %v), want none", len(none), err)
	}
}

// TestCacheReportExportIsTheRawSamplesAndNothingElse pins the export contract:
// the JSONL export is exactly the retained records, one per line, and a report
// rebuilt from it is the report built from the store.
func TestCacheReportExportIsTheRawSamplesAndNothingElse(t *testing.T) {
	f := newCacheReportFixture(t)
	now := time.Now()
	f.seed(t, []team.MemberCacheRequest{
		reportRequest(200_000, 90_000, 10_000, now),
		reportRequest(1_000, 900, 100, now.Add(time.Second)),
	}, team.OwnerUsage{})
	collected, _, err := collectCacheSamples(f.roots, cacheReportSelection{})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "requests.jsonl")
	if code := writeCacheRequestExport(out, collected); code != 0 {
		t.Fatalf("export exit code = %d, want 0", code)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(raw)), "\n") + 1; lines != 2 {
		t.Fatalf("export has %d lines, want one per request:\n%s", lines, raw)
	}
	for _, leak := range []string{"prompt text", "tool_arguments", "sk-"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("the export leaked %q:\n%s", leak, raw)
		}
	}
}

// TestCacheReportWindowFlagsAcceptDatesAndRefuseJunk pins the window contract:
// a date-only bound selects that UTC day, and an unparsable bound is refused
// rather than silently widened to "everything".
func TestCacheReportWindowFlagsAcceptDatesAndRefuseJunk(t *testing.T) {
	window, err := parseCacheReportWindow("2026-09-01", "2026-09-24T06:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if window.from.Format(time.RFC3339) != "2026-09-01T00:00:00Z" {
		t.Fatalf("from = %s, want the start of the named UTC day", window.from)
	}
	if window.to.Format(time.RFC3339) != "2026-09-24T06:00:00Z" {
		t.Fatalf("to = %s", window.to)
	}
	if _, err := parseCacheReportWindow("yesterday", ""); err == nil {
		t.Fatal("an unparsable window bound must be refused")
	}
	if open, err := parseCacheReportWindow("", ""); err != nil || !open.from.IsZero() || !open.to.IsZero() {
		t.Fatalf("empty bounds = (%+v, %v), want an open window", open, err)
	}
}

// TestCacheReportCommandFiltersRoutesAndListsThem pins the route dimension end to
// end: the report names every provider cache scope it saw, and a named route
// selects only that scope's samples — two routes must never be averaged into one
// rate, because a provider cache is per route.
func TestCacheReportCommandFiltersRoutesAndListsThem(t *testing.T) {
	f := newCacheReportFixture(t)
	now := time.Now()
	fast := reportRequest(200_000, 90_000, 10_000, now)
	fast.RouteBucket = "anthropic/aaaaaaaaaaaa"
	slow := reportRequest(200_000, 10_000, 90_000, now.Add(time.Second))
	slow.RouteBucket = "openai/bbbbbbbbbbbb"
	f.seed(t, []team.MemberCacheRequest{fast, slow}, team.OwnerUsage{})

	all, _, err := collectCacheSamples(f.roots, cacheReportSelection{})
	if err != nil {
		t.Fatal(err)
	}
	report := team.BuildCacheReport(team.CacheReportInput{Requests: all, GeneratedAt: now})
	if len(report.RouteBuckets) != 2 {
		t.Fatalf("route buckets = %v, want both named", report.RouteBuckets)
	}
	if !strings.Contains(renderCacheReport(report), "routes: anthropic/aaaaaaaaaaaa, openai/bbbbbbbbbbbb") {
		t.Fatalf("the rendered report must name the routes:\n%s", renderCacheReport(report))
	}

	one, _, err := collectCacheSamples(f.roots, cacheReportSelection{route: "anthropic/aaaaaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0].RouteBucket != "anthropic/aaaaaaaaaaaa" {
		t.Fatalf("filtered collection = %+v, want only the named route", one)
	}
	filtered := team.BuildCacheReport(team.CacheReportInput{Requests: one, GeneratedAt: now})
	if !filtered.Overall.HasRate || filtered.Overall.Weighted != 0.9 {
		t.Fatalf("filtered rate = (%v, %v), want the fast route's own 0.9", filtered.Overall.Weighted, filtered.Overall.HasRate)
	}
	if unfiltered := team.BuildCacheReport(team.CacheReportInput{Requests: all, GeneratedAt: now}); unfiltered.Overall.Weighted != 0.5 {
		t.Fatalf("unfiltered rate = %v, want the merged 0.5 — the filter is what keeps them apart", unfiltered.Overall.Weighted)
	}
	if none, _, err := collectCacheSamples(f.roots, cacheReportSelection{route: "anthropic/zzzzzzzzzzzz"}); err != nil || len(none) != 0 {
		t.Fatalf("an unmatched route filter = (%d records, %v), want none", len(none), err)
	}
}
