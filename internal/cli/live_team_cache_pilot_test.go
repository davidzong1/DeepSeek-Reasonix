//go:build live

package cli

// Part B's real-Team pilot driver: the K1-era baseline collected from the member
// writers' own records. Paid requests; -tags live and a credential required.
// The experiment driver next door measures the provider boundary instead.
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/team"
)

// pilotMembers is the roster the pilot assembles: three non-leader members, each
// opening with a differently sized brief so their requests land in different
// prompt buckets. Three members is the smallest roster that can clear the
// report's member gate at all; it is still far below the cross-member inference
// gate, which the pilot does not claim.
var pilotMembers = []struct {
	id       string
	role     team.RoleID
	fillerKB int
}{
	{"pilot-small", "member", 40},
	{"pilot-mid", "member", 200},
	{"pilot-large", "member", 560},
}

// pilotTurns is how many turns each member runs. Ten turns per member is what
// gives the report a first-request plus nine warm candidates per member, so the
// warm/first split is observable without pretending the sample is independent.
const pilotTurns = 10

// pilotTaskFamily is the frozen task family this pilot registers. It is a label
// the driver stamps, not a value read from anywhere: no upstream in this
// repository names a task family, so claiming one from the data would be
// inventing it. The brief below is the whole of the task, and its family is
// "long-context single-word recall" — deliberately the cheapest family that
// still exercises a large stable prefix.
const pilotTaskFamily = "long-context-single-word-recall"

// TestLiveTeamMemberCachePilot is the P2 deliverable: a serial, fixed-condition
// real Team member pilot whose per-request records carry the provenance the
// report needs. It asserts only what this code owns — one record per request, a
// route and a diagnosis on each, a report that accounts for every sample — and
// logs the real numbers rather than asserting a hit rate.
//
// Run it with:
//
//	REASONIX_LIVE_CACHE_MODEL=deepseek/deepseek-v4.1-flash \
//	  go test -tags live ./internal/cli/ -run TestLiveTeamMemberCachePilot -v -count=1 -timeout 30m
//
// The archive directory is created if absent. The journal the driver writes is
// the member log itself; this test copies it out under a run-stamped name so a
// later run cannot overwrite the evidence.
func TestLiveTeamMemberCachePilot(t *testing.T) {
	creds := liveCacheCredentials()
	if creds.baseURL == "" || creds.apiKey == "" {
		t.Skip("set REASONIX_LIVE_CACHE_BASE_URL/REASONIX_LIVE_CACHE_API_KEY (or ANTHROPIC_BASE_URL/ANTHROPIC_AUTH_TOKEN) to run the pilot")
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
	if err := store.AddTeam(team.Team{Name: "pilot"}); err != nil {
		t.Fatal(err)
	}
	// The pool entry lives in the store, not in a test double: AddMember validates
	// a member's ref against the registry, and the builder resolves it from the
	// same place production does.
	if err := store.AddAgentUser(team.AgentUser{
		UserID: "pilot-gw", Provider: "deepseek", Model: poolModel,
		BaseURL: creds.baseURL, APIKey: creds.apiKey,
	}); err != nil {
		t.Fatal(err)
	}
	for _, member := range pilotMembers {
		if err := store.AddMember("pilot", team.MemberSlot{MemberID: member.id, Role: member.role, AgentUserRef: "pilot-gw"}); err != nil {
			t.Fatal(err)
		}
	}
	deps := memberBackendDeps{
		ctx: t.Context(), users: store, store: store, sessions: sessions,
		events: newMemberEventPump(), owners: owners,
		workspaceRoot: t.TempDir(),
		base:          func() boot.Options { return boot.Options{SessionDir: t.TempDir(), Stderr: io.Discard} },
	}
	build := newMemberBackendBuilder(deps)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	started := time.Now().UTC()
	for _, member := range pilotMembers {
		sessionFile, err := team.MemberSessionFile("pilot", member.id)
		if err != nil {
			t.Fatal(err)
		}
		backend, err := build(team.MemberBinding{
			Team: "pilot", MemberID: member.id, Role: member.role, AgentUserRef: "pilot-gw",
			SessionFile: sessionFile,
		})
		if err != nil {
			t.Fatalf("assemble member %s: %v", member.id, err)
		}
		for turn := 1; turn <= pilotTurns; turn++ {
			prompt := fmt.Sprintf("Turn %d: reply with exactly the word ACK%d and nothing else.", turn, turn)
			if turn == 1 {
				prompt = pilotBrief(member.fillerKB) + "\n" + prompt
			}
			if err := backend.RunTurn(ctx, prompt); err != nil {
				backend.Close()
				t.Fatalf("member %s turn %d: %v", member.id, turn, err)
			}
			// Serial on purpose: one request in flight at a time, which is the
			// registered concurrency level of this pilot.
			time.Sleep(400 * time.Millisecond)
		}
		backend.Close()
	}
	finished := time.Now().UTC()

	requests := waitForPilotRecords(t, owners)
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
	logPilotRecords(t, requests)
	t.Logf("report:\n%s", renderCacheReport(report))
	pilotArchive(t, archive, runID, clientBuild, requests, report, started, finished)
	assertPilotPipeline(t, requests, report)
}

// pilotArchiveDir resolves where a pilot's evidence lands. It must be named
// explicitly: the cli test binary redirects HOME to a disposable directory for
// the whole package, so a home-derived default would be deleted when the test
// process exits — and the plan forbids an experiment's only copy living
// somewhere that gets cleaned. The operator therefore names the archive root,
// and a path under the OS temp directory is refused for the same reason.
func pilotArchiveDir(t *testing.T) string {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_ARCHIVE"))
	if dir == "" {
		t.Fatal("set REASONIX_LIVE_CACHE_ARCHIVE to a durable directory: the test binary's HOME is disposable, so a default archive would be deleted with it")
	}
	if tmp, err := filepath.EvalSymlinks(os.TempDir()); err == nil {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil && strings.HasPrefix(resolved, tmp+string(filepath.Separator)) {
			t.Fatalf("REASONIX_LIVE_CACHE_ARCHIVE=%q is under the OS temp directory, which the plan forbids as an experiment's only copy", dir)
		}
	}
	day := time.Now().UTC().Format("2006-01-02")
	full := filepath.Join(dir, day)
	if err := os.MkdirAll(full, 0o700); err != nil {
		t.Fatal(err)
	}
	return full
}

// pilotArchive writes the run's evidence: the records exactly as the writers
// stored them, the report, and the run's own banner. It carries no prompt, tool
// argument or credential: the records never held any, and the banner is built
// from identifiers the driver already knows.
func pilotArchive(t *testing.T, dir, runID, clientBuild string, requests []team.MemberCacheRequest, report team.CacheReport, started, finished time.Time) {
	t.Helper()
	recordsPath := filepath.Join(dir, "pilot-records-"+runID+".jsonl")
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
	reportPath := filepath.Join(dir, "pilot-report-"+runID+".json")
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	banner := strings.Join([]string{
		"run_id: " + runID,
		"started_utc: " + started.Format(time.RFC3339Nano),
		"finished_utc: " + finished.Format(time.RFC3339Nano),
		"build_commit: " + clientBuild,
		"task_family: " + pilotTaskFamily,
		"concurrency_level: 1 (serial, one request in flight; registered, not measured)",
		"members: " + pilotMemberIDs(),
		"turns_per_member: " + fmt.Sprint(pilotTurns),
		"model: " + liveCacheModel,
		"records_file: " + filepath.Base(recordsPath),
		"report_file: " + filepath.Base(reportPath),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "pilot-banner-"+runID+".txt"), []byte(banner), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("archived: %s", recordsPath)
	t.Logf("archived: %s", reportPath)
}

func pilotMemberIDs() string {
	ids := make([]string, 0, len(pilotMembers))
	for _, m := range pilotMembers {
		ids = append(ids, m.id)
	}
	return strings.Join(ids, ",")
}

// waitForPilotRecords waits for every pilot member's writer to drain its queue.
// It waits on this roster's own key rather than the baseline test's team name:
// the two rosters are different teams, and a shared waiter would read the wrong
// owner directory and report a record count of zero for a run that recorded
// everything.
func waitForPilotRecords(t *testing.T, owners *team.OwnerStore) []team.MemberCacheRequest {
	t.Helper()
	want := len(pilotMembers) * pilotTurns
	var last []team.MemberCacheRequest
	for range 600 {
		var all []team.MemberCacheRequest
		for _, member := range pilotMembers {
			recs, err := owners.ReadCacheRequests(context.Background(), team.OwnerKey{TeamID: "pilot", MemberID: member.id})
			if err != nil {
				t.Fatalf("ReadCacheRequests: %v", err)
			}
			all = append(all, recs...)
		}
		last = all
		if len(all) >= want {
			return all
		}
		time.Sleep(100 * time.Millisecond)
	}
	return last
}

// pilotBuildID names the build the records were produced by, so a later
// comparison cannot silently span two binaries. A test binary carries no VCS
// stamp of its own, so the operator must name the build: an archive whose banner
// says "unknown" cannot answer "which binary produced this", which is the one
// question the plan's archive rule exists for.
func pilotBuildID(t *testing.T) string {
	t.Helper()
	build := experimentBuildID()
	if build == "" || build == "unknown" {
		t.Fatal("set REASONIX_LIVE_CACHE_CLIENT_BUILD to the build's commit: a test binary carries no VCS stamp, and an archive that cannot name its build is not evidence")
	}
	return build
}

// pilotBrief sizes one member's opening message. A brief is how a member's
// context grows in practice, so the ladder is built the way the workload builds
// it; the content is fixed for the whole session, which is what makes later
// turns warm candidates.
func pilotBrief(kilobytes int) string {
	var b strings.Builder
	b.WriteString("Reference brief. Read it, then answer the single-word question at the end.\n")
	for i := 0; b.Len() < kilobytes*1024; i++ {
		fmt.Fprintf(&b, "brief line %06d: reference material for this member's opening context.\n", i)
	}
	return b.String()
}

// logPilotRecords prints one line per record, including the two provenance facts
// the plan's contract is built on: the request-count source and whether the
// record carried a session identity.
func logPilotRecords(t *testing.T, requests []team.MemberCacheRequest) {
	t.Helper()
	for _, rec := range requests {
		t.Logf("member=%s seq=%d bucket=%s route=%s ctx=%d prompt=%d hit=%d miss=%d reqsrc=%s diag=%v stage=%v",
			rec.MemberID, rec.SessionRequestSeq, team.CacheRequestBucketOf(rec.ContextPromptTokens), rec.RouteBucket,
			rec.ContextPromptTokens, rec.PromptTokens, rec.CacheHitTokens, rec.CacheMissTokens,
			rec.RequestCountSource, rec.DiagnosticsAvailable, team.CacheRequestStages(rec))
	}
}

// assertPilotPipeline pins what this code owns about the pilot: every member
// recorded a request per turn, every record names a route and a diagnosis, the
// request count was measured rather than assumed, and the report accounts for
// every sample. The hit rate is logged, never asserted.
func assertPilotPipeline(t *testing.T, requests []team.MemberCacheRequest, report team.CacheReport) {
	t.Helper()
	seen := map[string]int{}
	for _, rec := range requests {
		seen[rec.MemberID]++
		if rec.RouteBucket == "" {
			t.Fatalf("member %s request %d carries no route bucket", rec.MemberID, rec.SessionRequestSeq)
		}
		if rec.ContextPromptTokens <= 0 {
			t.Fatalf("member %s request %d has no prompt size", rec.MemberID, rec.SessionRequestSeq)
		}
		if !rec.RequestCountVerified() {
			t.Fatalf("member %s request %d carries an unverified request count (%q)", rec.MemberID, rec.SessionRequestSeq, rec.RequestCountSource)
		}
	}
	for _, member := range pilotMembers {
		if got := seen[member.id]; got != pilotTurns {
			t.Fatalf("member %s recorded %d requests, want %d", member.id, got, pilotTurns)
		}
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
	if len(report.Members) != len(pilotMembers) {
		t.Fatalf("report members = %v, want %d", report.Members, len(pilotMembers))
	}
	// The pilot deliberately does not clear the cross-member gate, and saying so
	// here keeps a later reader from quoting a three-member bucket as a result.
	if report.Overall.SampleGateReached {
		t.Logf("pilot cleared the %d/%d gate: %+v", report.MinRequests, report.MinMembers, report.Overall.Totals)
	} else {
		t.Logf("pilot is under the %d/%d gate, as registered: every bucket is insufficient_sample",
			report.MinRequests, report.MinMembers)
	}
}
