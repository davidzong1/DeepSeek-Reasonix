//go:build live

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/boot"
	"reasonix/internal/team"
)

// liveMemberSizes are the first-message sizes, in characters, that place each
// member's requests in a different reported prompt bucket. The bucket is decided
// by the provider-reported prompt, so the test logs what actually landed rather
// than asserting the sizes did.
var liveMemberSizes = []struct {
	member string
	chars  int
	turns  int
}{
	{"probe-small", 6_000, 4},
	{"probe-mid", 150_000, 4},
	{"probe-big", 700_000, 4},
}

// TestLiveTeamMemberCacheSession runs a real Team session through the production
// member backend builder, so every layer Part A audits is exercised for real:
// the registry binding, the session lease, the observation sink, the writer's
// bounded record log in the member's own owner directory, and the leader
// exclusion.
//
// It asserts the provenance the plan's A2.3 asks for — records exist, they are
// member-scoped, and the leader contributed none — and logs the member-level
// report rather than asserting a rate, which is the provider's behaviour.
func TestLiveTeamMemberCacheSession(t *testing.T) {
	creds := liveCacheCredentials()
	if creds.baseURL == "" || creds.apiKey == "" {
		t.Skip("set REASONIX_LIVE_CACHE_BASE_URL/REASONIX_LIVE_CACHE_API_KEY (or ANTHROPIC_BASE_URL/ANTHROPIC_AUTH_TOKEN) to run the live member session")
	}
	// A private user state root: the member deliverable tools and the team skill
	// tree resolve there, and a live run must not touch the operator's own.
	t.Setenv("REASONIX_STATE_HOME", t.TempDir())

	model := strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_MODEL"))
	if model == "" {
		model = liveCacheModel
	}
	owners, err := team.NewOwnerStore(filepath.Join(t.TempDir(), "team"))
	if err != nil {
		t.Fatal(err)
	}
	const teamName = "livecache"
	pool := fakePool{users: map[string]team.AgentUser{}}
	bindings := []team.MemberBinding{}
	for _, spec := range liveMemberSizes {
		pool.users[spec.member] = team.AgentUser{
			UserID: spec.member, Provider: "deepseek", Model: model + "[1m]",
			BaseURL: creds.baseURL, APIKey: creds.apiKey,
		}
		bindings = append(bindings, liveBinding(t, teamName, spec.member, false))
	}
	// The leader is in the same pool and runs the same turns: the only difference
	// is its role, which is exactly the variable the exclusion rule is about.
	pool.users["probe-leader"] = team.AgentUser{
		UserID: "probe-leader", Provider: "deepseek", Model: model + "[1m]",
		BaseURL: creds.baseURL, APIKey: creds.apiKey,
	}
	leader := liveBinding(t, teamName, "probe-leader", true)

	workspace := t.TempDir()
	deps := memberBackendDeps{
		ctx: t.Context(), owners: owners, users: pool,
		events:        newMemberEventPump(),
		workspaceRoot: workspace,
		base: func() boot.Options {
			return boot.Options{SessionDir: t.TempDir(), Stderr: io.Discard}
		},
	}
	build := newMemberBackendBuilder(deps)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	run := func(binding team.MemberBinding, chars, turns int) {
		backend, err := build(binding)
		if err != nil {
			t.Fatalf("assemble %s: %v", binding.MemberID, err)
		}
		defer backend.Close()
		for turn := 1; turn <= turns; turn++ {
			input := "Reply with exactly the word ACK and nothing else."
			if turn == 1 && chars > 0 {
				// A large first message is the honest way to place this member in a
				// larger prompt bucket: it is real conversation content, and it stays
				// in the prefix for every later turn.
				input = liveFiller(chars) + "\n\n" + input
			}
			if err := backend.RunTurn(ctx, input); err != nil {
				t.Fatalf("%s turn %d: %v", binding.MemberID, turn, err)
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
	for i, spec := range liveMemberSizes {
		run(bindings[i], spec.chars, spec.turns)
	}
	run(leader, 0, 2)

	requests := collectLiveMemberRecords(t, owners, teamName, bindings)
	if len(requests) == 0 {
		t.Fatal("a real member session must have retained at least one record")
	}
	leaderRecords := readLiveMemberRecords(t, owners, teamName, leader.MemberID)
	if len(leaderRecords) != 0 {
		t.Fatalf("the leader retained %d records; the member dataset must exclude it", len(leaderRecords))
	}
	report := team.BuildCacheReport(team.CacheReportInput{
		Requests: requests, Source: cacheMemberRecordSource, GeneratedAt: time.Now(),
	})
	logLiveMemberRecords(t, requests)
	t.Logf("member-level report:\n%s", renderCacheReport(report))
	assertLiveMemberReport(t, report, bindings)
}

// liveBinding resolves one member's canonical session file, the path the builder
// binds.
func liveBinding(t *testing.T, teamName, memberID string, leader bool) team.MemberBinding {
	t.Helper()
	name, err := team.MemberSessionFile(teamName, memberID)
	if err != nil {
		t.Fatal(err)
	}
	return team.MemberBinding{
		Team: teamName, MemberID: memberID, Leader: leader,
		AgentUserRef: memberID, SessionFile: name,
	}
}

// liveFiller returns a body of the requested size. Its content is irrelevant to
// the cache diagnosis; only its size and stability are.
func liveFiller(chars int) string {
	var b strings.Builder
	b.Grow(chars + 64)
	b.WriteString("Reference material, fixed for this session:\n")
	for i := 0; b.Len() < chars; i++ {
		fmt.Fprintf(&b, "item %06d: a stable clause that never changes between turns.\n", i)
	}
	return b.String()
}

func readLiveMemberRecords(t *testing.T, owners *team.OwnerStore, teamName, memberID string) []team.MemberCacheRequest {
	t.Helper()
	recs, err := owners.ReadCacheRequests(context.Background(), team.OwnerKey{TeamID: teamName, MemberID: memberID})
	if err != nil {
		t.Fatalf("read %s records: %v", memberID, err)
	}
	return recs
}

// collectLiveMemberRecords waits for the writers to drain their queues, then
// returns every member's records in member order.
func collectLiveMemberRecords(t *testing.T, owners *team.OwnerStore, teamName string, bindings []team.MemberBinding) []team.MemberCacheRequest {
	t.Helper()
	var out []team.MemberCacheRequest
	for range 100 {
		out = out[:0]
		for _, binding := range bindings {
			out = append(out, readLiveMemberRecords(t, owners, teamName, binding.MemberID)...)
		}
		if len(out) >= len(bindings)*2 {
			return out
		}
		time.Sleep(200 * time.Millisecond)
	}
	return out
}

func logLiveMemberRecords(t *testing.T, requests []team.MemberCacheRequest) {
	t.Helper()
	for _, rec := range requests {
		t.Logf("member=%-12s route=%s seq=%d prompt=%d hit=%d miss=%d reqs=%d diag=%v stable_changed=%v reasons=%v schema=%d",
			rec.MemberID, rec.RouteBucket, rec.SessionRequestSeq, rec.ContextPromptTokens,
			rec.CacheHitTokens, rec.CacheMissTokens, rec.RequestCount, rec.DiagnosticsAvailable,
			rec.StablePrefixChanged, rec.PrefixChangeReasons, rec.ToolSchemaTokensEstimate)
	}
}

// assertLiveMemberReport pins what A2.3 and A2.4 need from a real session: the
// report is member-scoped, every record carries a route and a diagnosis, and the
// members are distinguishable rather than merged.
func assertLiveMemberReport(t *testing.T, report team.CacheReport, bindings []team.MemberBinding) {
	t.Helper()
	if !strings.Contains(report.Source, "member records") {
		t.Fatalf("source = %q, want the member-record dataset named", report.Source)
	}
	if len(report.Members) != len(bindings) {
		t.Fatalf("report members = %v, want all %d distinct members", report.Members, len(bindings))
	}
	if len(report.RouteBuckets) == 0 {
		t.Fatal("every member record must carry a route bucket")
	}
	if len(report.Buckets) == 0 {
		t.Fatal("the session must have reached at least one prompt bucket")
	}
	for _, group := range report.Buckets {
		if group.Coverage.Received == 0 {
			t.Fatalf("bucket %s reports no received samples", group.Key)
		}
	}
}
