//go:build live

package cli

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

	"reasonix/internal/agent"
	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/provider/anthropic"
	"reasonix/internal/team"
	"reasonix/internal/tool"
)

// liveCacheModel is the model a live cache baseline runs against. It defaults to
// the DeepSeek model this repository's own operator runs, reachable through an
// Anthropic-compatible gateway.
//
// The id is the raw one: a "[1m]" context alias is a Reasonix-side suffix that
// the config layer strips before the request, and a gateway that receives it
// rejects the model outright.
const liveCacheModel = "deepseek/deepseek-v4.1-flash"

// TestLiveTeamMemberCacheBaseline runs a small real baseline through the whole
// observation pipeline: real provider requests, the member writer's observation
// sink, the bounded owner-directory log, and the report. It asserts the
// pipeline's own invariants and logs the real numbers rather than asserting a
// hit rate, which is the provider's behaviour and not this code's contract.
//
// It is a live test: it makes paid network calls and only runs with
// -tags live and a credential. Credentials resolve from REASONIX_LIVE_CACHE_*,
// falling back to the Anthropic-compatible gateway this agent itself runs on
// (ANTHROPIC_BASE_URL / ANTHROPIC_AUTH_TOKEN), which is where the DeepSeek
// model above is reachable here.
func TestLiveTeamMemberCacheBaseline(t *testing.T) {
	creds := liveCacheCredentials()
	if creds.baseURL == "" || creds.apiKey == "" {
		t.Skip("set REASONIX_LIVE_CACHE_BASE_URL/REASONIX_LIVE_CACHE_API_KEY (or ANTHROPIC_BASE_URL/ANTHROPIC_AUTH_TOKEN) to run the live cache baseline")
	}
	model := strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_MODEL"))
	if model == "" {
		model = liveCacheModel
	}
	prov := newLiveCacheProvider(t, creds, model)

	// The observation channel exactly as a writable member backend installs it.
	owners, err := team.NewOwnerStore(filepath.Join(t.TempDir(), "team"))
	if err != nil {
		t.Fatal(err)
	}
	key := team.OwnerKey{TeamID: "live", MemberID: "ipc-protocol"}
	publisher := newMemberUsagePublisher(owners, key, "")
	sink := publisher.Sink(event.FuncSink(func(event.Event) {}))
	publisher.Start()
	t.Cleanup(publisher.Close)

	reg := tool.NewRegistry()
	for _, tl := range liveCacheTools() {
		reg.Add(tl)
	}
	sess := agent.NewSession(liveCacheSystemPrompt(fmt.Sprintf("%d", time.Now().UnixNano())))
	runner := agent.New(prov, reg, sess, agent.Options{MaxSteps: 2, MaxOutputTokens: 256, ModelRef: model}, sink)

	// The controller is in the path on purpose: it is what stamps the model ref
	// and the turn identity onto the usage event, so a record written without it
	// would be missing exactly the fields the report groups by.
	ctrl := control.New(control.Options{
		Runner: runner, Executor: runner, Sink: sink, ModelRef: model,
		SessionDir: t.TempDir(), Label: "live-cache-baseline",
	})
	t.Cleanup(ctrl.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for turn := 1; turn <= liveCacheTurns; turn++ {
		prompt := fmt.Sprintf("Turn %d: reply with exactly the word ACK%d and nothing else.", turn, turn)
		if err := ctrl.RunTurn(ctx, prompt); err != nil {
			t.Fatalf("live turn %d: %v", turn, err)
		}
		// A short pause keeps consecutive requests in the same gap stratum and
		// lets the writer drain its queue between turns.
		time.Sleep(500 * time.Millisecond)
	}

	requests := waitForLiveCacheRecords(t, owners, key)
	hit, miss := runner.SessionCache()
	report := team.BuildCacheReport(team.CacheReportInput{
		Requests: requests,
		Sessions: []team.CacheSessionTotals{{
			TeamID: key.TeamID, MemberID: key.MemberID, CacheHit: hit, CacheMiss: miss,
		}},
		CodeVersion: "live", CodeCommit: "live", GeneratedAt: time.Now(),
	})
	logLiveCacheRecords(t, requests)
	t.Logf("session cumulative: hit=%d miss=%d", hit, miss)
	t.Logf("report:\n%s", renderCacheReport(report))
	assertLiveCachePipeline(t, requests, report)
}

const liveCacheTurns = 6

// liveCacheCredential is one resolved live endpoint. bearer records whether the
// credential is an Authorization token rather than an Anthropic x-api-key, which
// decides the auth header the adapter sends.
type liveCacheCredential struct {
	baseURL string
	apiKey  string
	bearer  bool
}

// liveCacheCredentials resolves the live endpoint and credential. The gateway
// fallback exists because the DeepSeek model above is reached through an
// Anthropic-compatible proxy here; an official key path sets the explicit pair,
// in which case ANTHROPIC_AUTH_TOKEN is not consulted at all.
func liveCacheCredentials() liveCacheCredential {
	creds := liveCacheCredential{
		baseURL: strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_BASE_URL")),
		apiKey:  strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_API_KEY")),
	}
	if creds.apiKey != "" {
		return creds
	}
	return liveCacheCredential{
		baseURL: strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL")),
		apiKey:  strings.TrimSpace(os.Getenv("ANTHROPIC_AUTH_TOKEN")),
		bearer:  true,
	}
}

func newLiveCacheProvider(t *testing.T, creds liveCacheCredential, model string) provider.Provider {
	t.Helper()
	prov, err := anthropic.New(provider.Config{
		Name: "deepseek-gateway", BaseURL: creds.baseURL, Model: model, APIKey: creds.apiKey,
		// auth_header sends Authorization: Bearer, which is what an
		// ANTHROPIC_AUTH_TOKEN gateway expects; an official key uses x-api-key.
		Extra: map[string]any{"thinking": "disabled", "effort": "disabled", "auth_header": creds.bearer},
	})
	if err != nil {
		t.Fatalf("anthropic.New: %v", err)
	}
	if closer, ok := prov.(interface{ CloseIdleConnections() }); ok {
		t.Cleanup(closer.CloseIdleConnections)
	}
	return prov
}

// liveCacheSystemPrompt returns a cache-stable system prompt large enough to
// land the requests in a reported prompt bucket rather than the smallest one.
// The content is irrelevant to the cache diagnosis; only its stability within
// the run matters. nonce makes the prefix unique to one run, so the first
// request is a genuine cold start instead of reusing whatever an earlier run
// left in the provider's cache.
func liveCacheSystemPrompt(nonce string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are a terse assistant under a cache-observation probe. Reply with the single word requested and nothing else.\nrun-nonce: %s\n", nonce)
	b.WriteString("The following reference material is fixed for the whole session:\n")
	for i := 0; b.Len() < 140_000; i++ {
		fmt.Fprintf(&b, "clause %05d: a stable prefix clause that never changes between turns in this session.\n", i)
	}
	return b.String()
}

// liveCacheTool is one read-only tool whose schema contributes to the
// provider-visible tool surface the diagnosis reads.
type liveCacheTool struct {
	name        string
	description string
	schema      string
}

func (l liveCacheTool) Name() string        { return l.name }
func (l liveCacheTool) Description() string { return l.description }
func (l liveCacheTool) Schema() json.RawMessage {
	return json.RawMessage(l.schema)
}
func (liveCacheTool) ReadOnly() bool { return true }
func (liveCacheTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

func liveCacheTools() []tool.Tool {
	names := []string{"probe_read", "probe_list", "probe_stat", "probe_diff", "probe_search"}
	out := make([]tool.Tool, 0, len(names))
	for _, name := range names {
		out = append(out, liveCacheTool{
			name:        name,
			description: "Read-only probe tool used to hold a stable, non-trivial provider-visible schema across turns.",
			schema:      `{"type":"object","properties":{"path":{"type":"string","description":"absolute path to inspect"},"pattern":{"type":"string","description":"optional glob filter applied to the result set"},"limit":{"type":"integer","description":"maximum number of rows to return"}},"required":["path"],"additionalProperties":false}`,
		})
	}
	return out
}

// waitForLiveCacheRecords waits for the writer's queue to drain into the log.
func waitForLiveCacheRecords(t *testing.T, owners *team.OwnerStore, key team.OwnerKey) []team.MemberCacheRequest {
	t.Helper()
	var last []team.MemberCacheRequest
	for range 100 {
		recs, err := owners.ReadCacheRequests(context.Background(), key)
		if err != nil {
			t.Fatalf("ReadCacheRequests: %v", err)
		}
		last = recs
		if len(recs) >= liveCacheTurns {
			return recs
		}
		time.Sleep(100 * time.Millisecond)
	}
	return last
}

func logLiveCacheRecords(t *testing.T, requests []team.MemberCacheRequest) {
	t.Helper()
	for _, rec := range requests {
		t.Logf("request seq=%d id=%s(%s) prompt=%d context_prompt=%d hit=%d miss=%d write=%d reqs=%d unknown=%v estimated=%v reasons=%v stable_changed=%v schema=%d",
			rec.SessionRequestSeq, rec.RequestID, rec.RequestIDSource, rec.PromptTokens, rec.ContextPromptTokens,
			rec.CacheHitTokens, rec.CacheMissTokens, rec.CacheWriteTokens, rec.RequestCount,
			rec.UsageUnknown, rec.UsageEstimated, rec.PrefixChangeReasons, rec.StablePrefixChanged,
			rec.ToolSchemaTokensEstimate)
	}
}

// assertLiveCachePipeline pins what this code owns: one record per provider
// request with a monotonic sequence and a diagnosis, and a report that accounts
// for every received sample. A hit rate is deliberately not asserted — it is the
// provider's behaviour, not this code's contract. The record count may exceed
// the turn count: a turn that calls a tool issues more than one request, and
// each request is its own sample.
func assertLiveCachePipeline(t *testing.T, requests []team.MemberCacheRequest, report team.CacheReport) {
	t.Helper()
	if len(requests) < liveCacheTurns {
		t.Fatalf("stored %d records for %d turns, want at least one per turn", len(requests), liveCacheTurns)
	}
	for i, rec := range requests {
		if rec.SessionRequestSeq != i+1 {
			t.Fatalf("record %d sequence = %d, want %d", i, rec.SessionRequestSeq, i+1)
		}
		if !rec.DiagnosticsAvailable {
			t.Fatalf("record %d carries no diagnosis, so no prefix claim is available", i)
		}
		if rec.ContextPromptTokens <= 0 {
			t.Fatalf("record %d has no context prompt, so it cannot be bucketed: %+v", i, rec)
		}
		if i > 0 && !rec.HasPrevRequest {
			t.Fatalf("record %d has no predecessor interval, so warm requests are unclassifiable", i)
		}
		if rec.RequestCount > 1 {
			t.Fatalf("record %d is a multi-request aggregate, which a single turn must not produce (%+v)", i, rec)
		}
	}
	if got := report.Exclusions.Received; got != len(requests) {
		t.Fatalf("report received %d samples for %d records", got, len(requests))
	}
	if report.Exclusions.Included+report.Exclusions.OutsideWindow+report.Exclusions.NonMemberScope != len(requests) {
		t.Fatalf("report does not account for every sample: %+v", report.Exclusions)
	}
	if len(report.Buckets) == 0 {
		t.Fatal("no bucket was reached, so the baseline reports nothing")
	}
}

// liveTeamMembers is the team the live run assembles: three non-leader members,
// each opening with a differently sized brief so their requests land in
// different prompt buckets. Three members is also the smallest roster that can
// clear the report's member gate, and ten turns each is the smallest run that
// clears its request gate — a smaller roster could only ever report
// insufficient_sample.
var liveTeamMembers = []struct {
	id         string
	role       team.RoleID
	fillerKB   int
	wantBucket team.CacheRequestBucket
}{
	{"cache-small", "member", 48, team.CacheBucketLT32K},
	{"cache-mid", "member", 240, team.CacheBucket32K128K},
	{"cache-large", "member", 600, team.CacheBucket128K256K},
}

const liveTeamTurns = 10

// TestLiveTeamMemberCacheBaselineTeam creates a real team, assembles each
// member's backend through the production builder, drives real turns through
// them, and reports over what the member writers recorded. It is the end-to-end
// evidence the assembled pipeline needs: nothing here is a fixture except the
// credential and the roster.
//
// Like the single-member probe it asserts only this code's invariants — one
// record per request, a bucket per member, a report that accounts for every
// sample — and logs the real numbers instead of asserting a hit rate.
func TestLiveTeamMemberCacheBaselineTeam(t *testing.T) {
	creds := liveCacheCredentials()
	if creds.baseURL == "" || creds.apiKey == "" {
		t.Skip("set REASONIX_LIVE_CACHE_BASE_URL/REASONIX_LIVE_CACHE_API_KEY (or ANTHROPIC_BASE_URL/ANTHROPIC_AUTH_TOKEN) to run the live team baseline")
	}
	model := strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_MODEL"))
	if model == "" {
		// The [1m] alias is kept here on purpose: the team resolver is the layer
		// that strips it, and this run is meant to exercise that layer.
		model = liveCacheModel + "[1m]"
	}

	// A member's build-time write roots include the user state root, and boot
	// refuses a scope it cannot resolve, so the root a real host always has is
	// created here rather than left to chance.
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
	if err := store.AddTeam(team.Team{Name: "live-team"}); err != nil {
		t.Fatal(err)
	}
	// The pool entry lives in the store, not in a test double: AddMember validates
	// a member's ref against the registry, and the builder resolves it from the
	// same place production does.
	if err := store.AddAgentUser(team.AgentUser{
		UserID: "gw", Provider: "deepseek", Model: model, BaseURL: creds.baseURL, APIKey: creds.apiKey,
	}); err != nil {
		t.Fatal(err)
	}
	for _, member := range liveTeamMembers {
		if err := store.AddMember("live-team", team.MemberSlot{MemberID: member.id, Role: member.role, AgentUserRef: "gw"}); err != nil {
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	for _, member := range liveTeamMembers {
		sessionFile, err := team.MemberSessionFile("live-team", member.id)
		if err != nil {
			t.Fatal(err)
		}
		binding := team.MemberBinding{
			Team: "live-team", MemberID: member.id, Role: member.role, AgentUserRef: "gw",
			SessionFile: sessionFile,
		}
		backend, err := build(binding)
		if err != nil {
			t.Fatalf("assemble member %s: %v", member.id, err)
		}
		t.Cleanup(backend.Close)
		for turn := 1; turn <= liveTeamTurns; turn++ {
			prompt := fmt.Sprintf("Turn %d: reply with exactly the word ACK%d and nothing else.", turn, turn)
			if turn == 1 {
				prompt = liveMemberBrief(member.fillerKB) + "\n" + prompt
			}
			if err := backend.RunTurn(ctx, prompt); err != nil {
				t.Fatalf("member %s turn %d: %v", member.id, turn, err)
			}
			time.Sleep(300 * time.Millisecond)
		}
	}

	requests := waitForTeamCacheRecords(t, owners)
	// The session ledgers are read the way the report command reads them, so the
	// session-cumulative number in this run is the production one.
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
		CodeVersion: "live", CodeCommit: "live", GeneratedAt: time.Now(),
	})
	logLiveTeamRecords(t, requests)
	t.Logf("report:\n%s", renderCacheReport(report))
	assertLiveTeamPipeline(t, requests, report)
}

// liveMemberBrief is the opening user message that sizes one member's prompt.
// A brief is exactly how a member's context grows in practice — it reads
// something large — so the ladder is built the way the workload builds it.
func liveMemberBrief(kilobytes int) string {
	var b strings.Builder
	b.WriteString("Reference brief. Read it, then answer the single-word question at the end.\n")
	for i := 0; b.Len() < kilobytes*1024; i++ {
		fmt.Fprintf(&b, "brief line %06d: reference material for this member's opening context.\n", i)
	}
	return b.String()
}

// waitForTeamCacheRecords waits for every member's writer to drain its queue.
func waitForTeamCacheRecords(t *testing.T, owners *team.OwnerStore) []team.MemberCacheRequest {
	t.Helper()
	want := len(liveTeamMembers) * liveTeamTurns
	var last []team.MemberCacheRequest
	for range 600 {
		var all []team.MemberCacheRequest
		for _, member := range liveTeamMembers {
			recs, err := owners.ReadCacheRequests(context.Background(), team.OwnerKey{TeamID: "live-team", MemberID: member.id})
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

func logLiveTeamRecords(t *testing.T, requests []team.MemberCacheRequest) {
	t.Helper()
	for _, rec := range requests {
		t.Logf("member=%s seq=%d bucket=%s route=%s prompt=%d hit=%d miss=%d reasons=%v",
			rec.MemberID, rec.SessionRequestSeq, team.CacheRequestBucketOf(rec.ContextPromptTokens), rec.RouteBucket,
			rec.ContextPromptTokens, rec.CacheHitTokens, rec.CacheMissTokens, rec.PrefixChangeReasons)
	}
}

// assertLiveTeamPipeline pins what this code owns about a real roster: every
// member recorded a request per turn, every record names a route and a bucket,
// and the report accounts for all of them. The cross-member rate is logged, not
// asserted — it is the provider's behaviour.
func assertLiveTeamPipeline(t *testing.T, requests []team.MemberCacheRequest, report team.CacheReport) {
	t.Helper()
	seen := map[string]int{}
	routes := map[string]bool{}
	buckets := map[string]bool{}
	for _, rec := range requests {
		seen[rec.MemberID]++
		if rec.RouteBucket == "" {
			t.Fatalf("member %s request %d carries no route bucket", rec.MemberID, rec.SessionRequestSeq)
		}
		routes[rec.RouteBucket] = true
		buckets[string(team.CacheRequestBucketOf(rec.ContextPromptTokens))] = true
		if rec.ContextPromptTokens <= 0 {
			t.Fatalf("member %s request %d has no prompt size", rec.MemberID, rec.SessionRequestSeq)
		}
	}
	for _, member := range liveTeamMembers {
		if got := seen[member.id]; got != liveTeamTurns {
			t.Fatalf("member %s recorded %d requests, want %d", member.id, got, liveTeamTurns)
		}
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %v, want one: every member dialled the same pool entry", routes)
	}
	if !buckets[string(team.CacheBucket32K128K)] && !buckets[string(team.CacheBucket128K256K)] {
		t.Fatalf("buckets = %v, want the sized members spread across prompt buckets", buckets)
	}
	if got := report.Exclusions.Received; got != len(requests) {
		t.Fatalf("report received %d samples for %d records", got, len(requests))
	}
	if !report.Overall.SampleGateReached {
		t.Fatalf("the roster must clear the %d/%d gate, got %+v",
			report.MinRequests, report.MinMembers, report.Overall.Totals)
	}
	if len(report.RouteBuckets) != 1 || len(report.Buckets) < 2 {
		t.Fatalf("report = %d routes / %d buckets, want 1 and at least 2", len(report.RouteBuckets), len(report.Buckets))
	}
}
