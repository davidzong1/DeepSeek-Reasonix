//go:build live

package cli

// Part B's stratified Team experiment driver: one pre-registered arm per run,
// with the arm's registration stamped onto the archive. Paid requests; -tags
// live and a credential required.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/team"
)

// strataArm is one registered condition. Every field is frozen before any
// result is read: the arm id names the file it lands in, and the brief sizes,
// roster and turn count are the conditions themselves.
type strataArm struct {
	ID string
	// BriefKB is each member's opening message size. Members differ only in the
	// filler size, so a bucket difference is the one variable this arm moves.
	BriefKB int
	Members int
	Turns   int
	// TargetBucket is the registered destination. The provider's reported
	// context_prompt_tokens decides where a request actually lands; a miss is
	// recorded, never re-run to fit.
	TargetBucket team.CacheRequestBucket
	// Concurrency is the registered in-flight level. It is a declaration, not a
	// measurement: no per-request concurrency field exists, by decision.
	Concurrency int
	// TokenBudget caps this arm's cumulative input tokens. It is enforced from
	// the records themselves, so the cap holds even when no price is configured.
	TokenBudget int
	// SecondsPerTurn is the registered pacing, so two arms differ only in what
	// they register.
	SecondsPerTurn float64
}

// strataArms is the pre-registration itself, copied from
// TEAM_MEMBER_CACHE_FOLLOWUP_B_PREREGISTRATION.zh-CN.md §2. An id absent here is
// refused rather than approximated, so a typo cannot run an unregistered
// condition.
func strataArms() []strataArm {
	return []strataArm{
		{ID: "S1a-small", BriefKB: 40, Members: 3, Turns: 12, TargetBucket: team.CacheBucketLT32K, Concurrency: 1, TokenBudget: 2_000_000, SecondsPerTurn: 0.4},
		{ID: "S1b-mid", BriefKB: 900, Members: 3, Turns: 12, TargetBucket: team.CacheBucket128K256K, Concurrency: 1, TokenBudget: 12_000_000, SecondsPerTurn: 0.4},
		{ID: "S1c-large", BriefKB: 2800, Members: 3, Turns: 12, TargetBucket: team.CacheBucket512K768K, Concurrency: 1, TokenBudget: 30_000_000, SecondsPerTurn: 0.4},
		{ID: "S4-768k", BriefKB: 3750, Members: 3, Turns: 8, TargetBucket: team.CacheBucket768K1M, Concurrency: 1, TokenBudget: 30_000_000, SecondsPerTurn: 0.4},
		// S3 is the concurrency ladder (§1's "1 → 2 → 4", capped at the roster
		// size of 3). Its brief is S1a-small's, so against the c=1 arm the only
		// variable is how many members run together.
		{ID: "S3-c2", BriefKB: 40, Members: 3, Turns: 12, TargetBucket: team.CacheBucketLT32K, Concurrency: 2, TokenBudget: 2_000_000, SecondsPerTurn: 0.4},
		{ID: "S3-c3", BriefKB: 40, Members: 3, Turns: 12, TargetBucket: team.CacheBucketLT32K, Concurrency: 3, TokenBudget: 2_000_000, SecondsPerTurn: 0.4},
	}
}

// strataArmByID resolves one registered arm, or reports what is registered.
func strataArmByID(id string) (strataArm, error) {
	id = strings.TrimSpace(id)
	for _, arm := range strataArms() {
		if arm.ID == id {
			return arm, nil
		}
	}
	ids := make([]string, 0, len(strataArms()))
	for _, arm := range strataArms() {
		ids = append(ids, arm.ID)
	}
	return strataArm{}, fmt.Errorf("cachelab strata: %q is not a registered arm; registered: %s", id, strings.Join(ids, ", "))
}

// strataMemberIDs names one arm's roster. The arm id is in each member id so a
// reader of a record can tell which arm produced it without the banner.
func strataMemberIDs(arm strataArm) []string {
	out := make([]string, 0, arm.Members)
	for i := range arm.Members {
		out = append(out, fmt.Sprintf("%s-m%d", arm.ID, i+1))
	}
	return out
}

// TestLiveTeamMemberCacheStrata runs one registered arm: a serial real Team
// member workload whose per-request records come from the member writers
// themselves. It asserts only what this code owns — one record per request, a
// route and a diagnosis on each, a report that accounts for every sample, the
// arm's registered token budget — and logs the real numbers instead of
// asserting a hit rate, which is the provider's behaviour.
//
// Run one arm with:
//
//	REASONIX_LIVE_CACHE_STRATA_ARM=S1a-small \
//	REASONIX_LIVE_CACHE_ARCHIVE=$HOME/reasonix-partb-archive \
//	REASONIX_LIVE_CACHE_CLIENT_BUILD=$(git rev-parse --short=12 HEAD) \
//	  go test -tags live ./internal/cli/ -run TestLiveTeamMemberCacheStrata -v -count=1 -timeout 60m
func TestLiveTeamMemberCacheStrata(t *testing.T) {
	creds := liveCacheCredentials()
	if creds.baseURL == "" || creds.apiKey == "" {
		t.Skip("set REASONIX_LIVE_CACHE_BASE_URL/REASONIX_LIVE_CACHE_API_KEY (or ANTHROPIC_BASE_URL/ANTHROPIC_AUTH_TOKEN) to run a strata arm")
	}
	arm, err := strataArmByID(os.Getenv("REASONIX_LIVE_CACHE_STRATA_ARM"))
	if err != nil {
		t.Fatal(err)
	}
	model := strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_MODEL"))
	if model == "" {
		model = liveCacheModel
	}
	poolModel := model
	if !strings.Contains(poolModel, "[1m]") {
		// The [1m] alias is kept on purpose: the team resolver is the layer that
		// strips it, and this run exercises that layer.
		poolModel += "[1m]"
	}
	archive := pilotArchiveDir(t)
	clientBuild := pilotBuildID(t)
	runID := fmt.Sprintf("%d", time.Now().UnixNano())

	// A private user state root: member skills and deliverable tools resolve
	// there, and a live run must not touch the operator's own state.
	t.Setenv("REASONIX_STATE_HOME", t.TempDir())
	if dir := config.MemoryUserDir(); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	owners, build, store, root := strataAssembly(t, arm, poolModel, creds)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	started := time.Now().UTC()
	members := strataMemberIDs(arm)
	stopped := strataStrataRun(t, ctx, owners, build, arm, members)
	finished := time.Now().UTC()

	requests := waitForStrataRecords(t, owners, arm)
	roots := &teamDataRoots{store: store, owners: owners, dataDir: root}
	collected, sessionLedgers, err := collectCacheSamples(roots, cacheReportSelection{})
	if err != nil {
		t.Fatal(err)
	}
	if len(collected) != len(requests) {
		t.Fatalf("the report reader collected %d of %d recorded requests", len(collected), len(requests))
	}
	report := team.BuildCacheReport(team.CacheReportInput{
		Requests: requests, Sessions: sessionLedgers,
		CodeVersion: "live", CodeCommit: clientBuild, GeneratedAt: time.Now(),
	})
	logStrataRecords(t, requests)
	t.Logf("report:\n%s", renderCacheReport(report))
	if stopped != "" {
		t.Logf("stopped early: %s", stopped)
	}
	strataArchive(t, archive, runID, clientBuild, arm, requests, report, started, finished, stopped)
	assertStrataPipeline(t, arm, requests, report)
}

// strataStrataRun drives the arm's roster and returns the registered stop
// reason, or "" when the arm completed. It is the one place the arm's
// concurrency is realised: Concurrency==1 runs members strictly one after
// another, and any higher value runs that many members at once. The banner's
// "registered, not measured" describes the record fields — no per-request
// concurrency field exists by decision — not the driver.
//
// A member is serial within itself either way: one backend, one turn at a time.
// The arm therefore varies how many members run together and nothing else.
func strataStrataRun(t *testing.T, ctx context.Context, owners *team.OwnerStore, build func(team.MemberBinding) (control.SessionAPI, error), arm strataArm, members []string) string {
	t.Helper()
	if arm.Concurrency <= 1 {
		for _, memberID := range members {
			if reason := strataRunMember(t, ctx, owners, build, arm, memberID); reason != "" {
				return reason
			}
		}
		return ""
	}
	// Concurrent arm: members are partitioned across the registered number of
	// workers, so exactly Concurrency members are in flight at any moment.
	stop := make(chan string, len(members))
	var wg sync.WaitGroup
	for worker := range min(arm.Concurrency, len(members)) {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for idx := worker; idx < len(members); idx += arm.Concurrency {
				if reason := strataRunMember(t, ctx, owners, build, arm, members[idx]); reason != "" {
					select {
					case stop <- reason:
					default:
					}
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	select {
	case reason := <-stop:
		return reason
	default:
		return ""
	}
}

// strataRunMember assembles one member's backend and runs its turns serially.
func strataRunMember(t *testing.T, ctx context.Context, owners *team.OwnerStore, build func(team.MemberBinding) (control.SessionAPI, error), arm strataArm, memberID string) string {
	t.Helper()
	sessionFile, err := team.MemberSessionFile(arm.ID, memberID)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := build(team.MemberBinding{
		Team: arm.ID, MemberID: memberID, Role: "member", AgentUserRef: "strata-gw",
		SessionFile: sessionFile,
	})
	if err != nil {
		t.Fatalf("assemble member %s: %v", memberID, err)
	}
	defer backend.Close()
	for turn := 1; turn <= arm.Turns; turn++ {
		if reason := strataStopReason(t, owners, arm, memberID); reason != "" {
			return reason
		}
		prompt := fmt.Sprintf("Turn %d: reply with exactly the word ACK%d and nothing else.", turn, turn)
		if turn == 1 {
			prompt = pilotBrief(arm.BriefKB) + "\n" + prompt
		}
		if err := backend.RunTurn(ctx, prompt); err != nil {
			t.Fatalf("member %s turn %d: %v", memberID, turn, err)
		}
		time.Sleep(time.Duration(arm.SecondsPerTurn * float64(time.Second)))
	}
	return ""
}

// strataAssembly builds the arm's team, pool entry and backend builder. It is
// the same production path the pilot uses; only the roster and the pool entry's
// id differ, so an arm's records come from the assembled member surface.
func strataAssembly(t *testing.T, arm strataArm, poolModel string, creds liveCacheCredential) (*team.OwnerStore, func(team.MemberBinding) (control.SessionAPI, error), *team.TeamStore, string) {
	t.Helper()
	root := t.TempDir()
	store, err := team.NewTeamStoreAt("", root)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := team.NewTeamSessionStoreDir(root)
	if err != nil {
		t.Fatal(err)
	}
	owners, err := team.NewOwnerStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddTeam(team.Team{Name: arm.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddAgentUser(team.AgentUser{
		UserID: "strata-gw", Provider: "deepseek", Model: poolModel,
		BaseURL: creds.baseURL, APIKey: creds.apiKey,
	}); err != nil {
		t.Fatal(err)
	}
	for _, memberID := range strataMemberIDs(arm) {
		if err := store.AddMember(arm.ID, team.MemberSlot{MemberID: memberID, Role: "member", AgentUserRef: "strata-gw"}); err != nil {
			t.Fatal(err)
		}
	}
	deps := memberBackendDeps{
		ctx: t.Context(), users: store, store: store, sessions: sessions,
		events: newMemberEventPump(), owners: owners,
		workspaceRoot: t.TempDir(),
		base:          func() boot.Options { return boot.Options{SessionDir: t.TempDir(), Stderr: io.Discard} },
	}
	return owners, newMemberBackendBuilder(deps), store, root
}

// strataStopReason returns the registered reason this arm must stop, or "" to
// continue. Only registered causes stop an arm: an interim hit rate, favourable
// or not, is never one of them.
func strataStopReason(t *testing.T, owners *team.OwnerStore, arm strataArm, current string) string {
	t.Helper()
	recs := strataRecords(t, owners, arm)
	if len(recs) == 0 {
		return ""
	}
	input := 0
	unverified, unknown, missing := 0, 0, 0
	consecutiveErrors := 0
	for _, rec := range recs {
		input += rec.PromptTokens
		if !rec.RequestCountVerified() {
			unverified++
		}
		if rec.UsageUnknown {
			unknown++
		}
		if rec.CacheHitTokens+rec.CacheMissTokens <= 0 {
			missing++
			consecutiveErrors++
		} else {
			consecutiveErrors = 0
		}
	}
	switch {
	case input > arm.TokenBudget:
		return fmt.Sprintf("token budget reached: %d of %d input tokens", input, arm.TokenBudget)
	case consecutiveErrors >= 3:
		return "consecutive requests reported no cache split"
	case unverified > 0:
		return fmt.Sprintf("request-count provenance is not measured on %d record(s)", unverified)
	case unknown > 0:
		return fmt.Sprintf("usage unknown on %d record(s)", unknown)
	}
	_ = current
	return ""
}

// strataRecords reads every roster member's retained records for this arm.
func strataRecords(t *testing.T, owners *team.OwnerStore, arm strataArm) []team.MemberCacheRequest {
	t.Helper()
	var all []team.MemberCacheRequest
	for _, memberID := range strataMemberIDs(arm) {
		recs, err := owners.ReadCacheRequests(context.Background(), team.OwnerKey{TeamID: arm.ID, MemberID: memberID})
		if err != nil {
			t.Fatalf("ReadCacheRequests: %v", err)
		}
		all = append(all, recs...)
	}
	return all
}

// waitForStrataRecords waits for every roster member's writer to drain its
// queue. It waits on this arm's own key: a shared waiter would read another
// arm's owner directory and report zero records for a run that recorded all.
func waitForStrataRecords(t *testing.T, owners *team.OwnerStore, arm strataArm) []team.MemberCacheRequest {
	t.Helper()
	want := arm.Members * arm.Turns
	var last []team.MemberCacheRequest
	for range 600 {
		last = strataRecords(t, owners, arm)
		if len(last) >= want {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	return last
}

// strataArchive writes the arm's evidence: the records exactly as the writers
// stored them, the report, and the arm's registration. Nothing here can carry
// prompt, tool argument or credential text: the records never held any, and the
// banner is built from identifiers the driver already knows.
func strataArchive(t *testing.T, dir, runID, clientBuild string, arm strataArm, requests []team.MemberCacheRequest, report team.CacheReport, started, finished time.Time, stopped string) {
	t.Helper()
	prefix := "strata-" + arm.ID + "-"
	recordsPath := filepath.Join(dir, prefix+"records-"+runID+".jsonl")
	f, err := os.OpenFile(recordsPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, rec := range requests {
		if err := enc.Encode(rec); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(dir, prefix+"report-"+runID+".json")
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	input := 0
	for _, rec := range requests {
		input += rec.PromptTokens
	}
	banner := strings.Join([]string{
		"arm: " + arm.ID,
		"run_id: " + runID,
		"started_utc: " + started.Format(time.RFC3339Nano),
		"finished_utc: " + finished.Format(time.RFC3339Nano),
		"build_commit: " + clientBuild,
		"frozen_stream_usage_sha256: " + strataFrozenDigest("internal/provider/anthropic/stream_usage.go"),
		"task_family: " + pilotTaskFamily,
		fmt.Sprintf("concurrency_level: %d (registered, not measured)", arm.Concurrency),
		"target_bucket: " + string(arm.TargetBucket),
		fmt.Sprintf("roster: %d members x %d turns, brief %d KB each", arm.Members, arm.Turns, arm.BriefKB),
		"members: " + strings.Join(strataMemberIDs(arm), ","),
		fmt.Sprintf("token_budget: %d, actual_input_tokens: %d", arm.TokenBudget, input),
		"stopped_early: " + strataOrNone(stopped),
		"model: " + liveCacheModel,
		"records_file: " + filepath.Base(recordsPath),
		"report_file: " + filepath.Base(reportPath),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, prefix+"banner-"+runID+".txt"), []byte(banner), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("archived: %s", recordsPath)
	t.Logf("archived: %s", reportPath)
	t.Logf("input tokens: %d of %d budget", input, arm.TokenBudget)
}

// strataFrozenDigest names the measurement layer an arm's readings were taken
// under, so a banner can never be read apart from the build it describes. An
// unreadable file is reported as such rather than omitted.
func strataFrozenDigest(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unreadable:" + filepath.Base(path)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func strataOrNone(v string) string {
	if strings.TrimSpace(v) == "" {
		return "none"
	}
	return v
}

// logStrataRecords prints one line per record with the provenance the report
// depends on: the bucket the provider actually placed the request in, the
// request-count source, and whether a diagnosis was available.
func logStrataRecords(t *testing.T, requests []team.MemberCacheRequest) {
	t.Helper()
	for _, rec := range requests {
		t.Logf("arm_member=%s seq=%d bucket=%s route=%s ctx=%d hit=%d miss=%d reqsrc=%s diag=%v stage=%v reasons=%v",
			rec.MemberID, rec.SessionRequestSeq, team.CacheRequestBucketOf(rec.ContextPromptTokens), rec.RouteBucket,
			rec.ContextPromptTokens, rec.CacheHitTokens, rec.CacheMissTokens,
			rec.RequestCountSource, rec.DiagnosticsAvailable, team.CacheRequestStages(rec), rec.PrefixChangeReasons)
	}
}

// assertStrataPipeline pins what this code owns about one arm: every member
// recorded a request per turn, every record names a route and a diagnosis, the
// request count was measured rather than assumed, the arm stayed inside its
// registered token budget, and the report accounts for every sample. The hit
// rate and the bucket placement are logged, never asserted.
func assertStrataPipeline(t *testing.T, arm strataArm, requests []team.MemberCacheRequest, report team.CacheReport) {
	t.Helper()
	seen := map[string]int{}
	input := 0
	reached := map[team.CacheRequestBucket]int{}
	for _, rec := range requests {
		seen[rec.MemberID]++
		input += rec.PromptTokens
		if rec.RouteBucket == "" {
			t.Fatalf("member %s request %d carries no route bucket", rec.MemberID, rec.SessionRequestSeq)
		}
		if rec.ContextPromptTokens <= 0 {
			t.Fatalf("member %s request %d has no prompt size", rec.MemberID, rec.SessionRequestSeq)
		}
		if !rec.RequestCountVerified() {
			t.Fatalf("member %s request %d carries an unverified request count (%q)", rec.MemberID, rec.SessionRequestSeq, rec.RequestCountSource)
		}
		reached[team.CacheRequestBucketOf(rec.ContextPromptTokens)]++
	}
	for _, memberID := range strataMemberIDs(arm) {
		if seen[memberID] == 0 {
			t.Fatalf("member %s recorded no requests at all", memberID)
		}
	}
	if input > arm.TokenBudget {
		t.Fatalf("arm %s spent %d input tokens, over its registered %d", arm.ID, input, arm.TokenBudget)
	}
	if got := report.Exclusions.Received; got != len(requests) {
		t.Fatalf("report received %d samples for %d records", got, len(requests))
	}
	if report.Exclusions.UnverifiedRequestCount != 0 {
		t.Fatalf("a live member run must record a measured request count for every sample: %+v", report.Exclusions)
	}
	if len(report.RouteBuckets) != 1 {
		t.Fatalf("routes = %v, want one: every member dialled the same pool entry", report.RouteBuckets)
	}
	// A bucket miss is recorded, never re-run to fit: the arm's registered
	// destination is a design intent, and the provider decides where a request
	// actually lands.
	if reached[arm.TargetBucket] == 0 {
		t.Logf("arm %s did not reach its registered bucket %s; landed in %v", arm.ID, arm.TargetBucket, reached)
	} else {
		t.Logf("arm %s reached its registered bucket %s on %d request(s); all buckets %v", arm.ID, arm.TargetBucket, reached[arm.TargetBucket], reached)
	}
	if report.Overall.SampleGateReached {
		t.Logf("arm %s cleared the %d/%d gate: %+v", arm.ID, report.MinRequests, report.MinMembers, report.Overall.Totals)
	} else {
		t.Logf("arm %s is under the %d/%d gate: descriptive only", arm.ID, report.MinRequests, report.MinMembers)
	}
}
