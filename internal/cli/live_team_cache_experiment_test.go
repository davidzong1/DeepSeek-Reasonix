//go:build live

package cli

// Part B's controlled experiment driver: one pre-registered arm per run against
// the real provider through a loopback recorder. Without credentials it reports
// what is missing instead of simulating a result.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"time"

	"reasonix/internal/boot"
	"reasonix/internal/cachelab"
	"reasonix/internal/netclient"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// TestLiveProviderCacheExperiment drives one registered arm end to end: frozen
// request bytes through the recorder to the real provider, the provider's own
// usage journalled per request, the registered stop rules enforced between
// requests, and one probe of the production member path so the assembled path's
// own records can be compared with the boundary record.
//
// Run one arm with:
//
//	REASONIX_LIVE_CACHE_ARM=B0-pilot REASONIX_LIVE_CACHE_JOURNAL=<path> \
//	  go test -tags live ./internal/cli/ -run TestLiveProviderCacheExperiment -v -count=1
//
// It asserts fixture invariants — one cold start, every sample accounted for, no
// prompt text journalled — and logs the real numbers instead of asserting a hit
// rate, which is the provider's behaviour and not this code's contract.
func TestLiveProviderCacheExperiment(t *testing.T) {
	creds := liveCacheCredentials()
	if creds.baseURL == "" || creds.apiKey == "" {
		t.Skip("set REASONIX_LIVE_CACHE_BASE_URL/REASONIX_LIVE_CACHE_API_KEY (or ANTHROPIC_BASE_URL/ANTHROPIC_AUTH_TOKEN) to run an experiment arm; without them no arm result exists")
	}
	arm := experimentArm(t)
	model := strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_MODEL"))
	if model == "" {
		model = liveCacheModel
	}
	nonce := fmt.Sprintf("%d", time.Now().UnixNano())
	fixture := experimentFixture(arm, nonce)
	if err := fixture.Validate(); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	journal, err := cachelab.OpenJournal(experimentJournalPath(arm, nonce))
	if err != nil {
		t.Fatal(err)
	}
	journal.Guard(fixture.GuardLiterals()...)
	recorder, err := cachelab.NewRecorder(creds.baseURL, journal)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(recorder.Close)
	t.Cleanup(func() { _ = journal.Close() })

	// The client dials the recorder; the recorder forwards to the real provider,
	// so the provider sees the client's own bytes and the journal sees them too.
	client := newLiveCacheProvider(t, liveCacheCredential{baseURL: recorder.URL(), apiKey: creds.apiKey, bearer: creds.bearer}, model)
	prices := experimentPrices()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	want := arm.WarmTarget + 1
	stopped := ""
	for turn := 1; turn <= want; turn++ {
		if reason := cachelab.StopReason(arm, recorder.Samples(), experimentSpend(recorder.Samples(), prices), prices.Registered()); reason != "" {
			stopped = reason
			break
		}
		recorder.Begin(cachelab.TurnContext{
			Arm: arm.ID, RunID: nonce, TeamID: "live-experiment", MemberID: "provider-boundary",
			ModelRef: model, Client: "reasonix", ClientCommit: experimentBuildID(),
			ConfigDigest: cachelab.ConfigDigest(arm.ID, fixture.Digest(), recorder.UpstreamHost(), model),
			TurnSeq:      turn, Expect: fixture.Marker,
		})
		answer, err := runExperimentTurn(ctx, client, fixture)
		if err != nil {
			t.Logf("turn %d: %v", turn, err)
		}
		if !strings.Contains(answer, fixture.Marker) {
			// The task is synthetic and asks for one fixed word, so a non-matching
			// answer is reported in the open: it is the task-quality result, and
			// only the answer to a synthetic prompt is ever shown.
			t.Logf("turn %d answered %q, which does not carry the marker %q", turn, truncateAnswer(answer), fixture.Marker)
		}
		time.Sleep(experimentPause(arm))
	}

	samples := recorder.WaitForSamples(want, 30*time.Second)
	stored, skipped, err := cachelab.ReadJournal(journal.Path())
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Fatalf("the journal skipped %d lines, so the record is not trustworthy", skipped)
	}
	// One journal file may accumulate runs, so the report reads this run back out
	// of it: the durability check and the analysis use the same records.
	ownRun := cachelab.ByRun(stored, nonce)
	if len(ownRun) != len(samples) {
		t.Fatalf("this run recorded %d samples but the journal holds %d for it", len(samples), len(ownRun))
	}
	t.Logf("arm %s (%s): registered target %d warm, %d samples recorded", arm.ID, arm.Changed, arm.WarmTarget, len(samples))
	t.Logf("journal: %s", journal.Path())
	if stopped != "" {
		t.Logf("stopped early: %s", stopped)
	}
	if probe := experimentAssemblyProbe(t, ctx, creds, recorder, model); probe != "" {
		t.Logf("production assembly probe: %s", probe)
	}
	t.Log(cachelab.RenderReport(cachelab.RegisteredArms(), ownRun, prices))
	assertExperimentRecord(t, arm, samples, journal.Path(), fixture)
}

// assertExperimentRecord pins what the fixture owns: every request was digests-
// only, the run started cold exactly once, and nothing in the journal is
// unaccounted for or leaked.
func assertExperimentRecord(t *testing.T, arm cachelab.Arm, samples []cachelab.Sample, journalPath string, fixture cachelab.Fixture) {
	t.Helper()
	for _, s := range samples {
		if s.Arm != arm.ID {
			t.Fatalf("sample %d belongs to arm %s, want %s", s.Seq, s.Arm, arm.ID)
		}
		if s.RequestHash != "" && s.RequestBytes <= 0 {
			t.Fatalf("sample %d has a digest but no length", s.Seq)
		}
	}
	if len(samples) > 0 && samples[0].Classify() != cachelab.ClassFirstRequest {
		t.Fatalf("the first request of a run is classified %s, want %s", samples[0].Classify(), cachelab.ClassFirstRequest)
	}
	raw, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, literal := range fixture.GuardLiterals() {
		if strings.Contains(string(raw), literal) {
			t.Fatalf("the journal holds fixture text %q: prompt content must never be written", literal)
		}
	}
	stats := cachelab.Summarize(arm, samples, cachelab.Prices{})
	t.Logf("arm %s: eligible_warm=%d/%d first=%d errors=%d usage_missing=%d no_cache_split=%d quality_pass=%d quality_fail=%d",
		arm.ID, stats.Eligible, arm.WarmMin, stats.FirstRequest, stats.Errors, stats.UsageMissing, stats.NoCacheSplit, stats.QualityPass, stats.QualityFail)
	if stats.Eligible == 0 {
		t.Logf("no eligible warm sample: the provider reported no usable cache split for this arm, so no rate exists to compare")
	}
	if stats.Errors > 0 && stats.Eligible == 0 {
		t.Logf("every request failed; the arm records a provider or credential problem, not a cache result")
	}
}

// experimentArm reads the arm to run. An unknown or gated arm is refused rather
// than approximated: the gated variables need a configuration this driver cannot
// construct, and running them anyway would produce an unregistered condition.
func experimentArm(t *testing.T) cachelab.Arm {
	t.Helper()
	id := strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_ARM"))
	if id == "" {
		id = "B0-pilot"
	}
	arm, err := cachelab.ArmByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if arm.Gated {
		t.Skipf("arm %s changes %s and needs %s; run it only with that configuration controlled, then record it separately",
			arm.ID, arm.Changed, arm.Gate)
	}
	if os.Getenv("REASONIX_LIVE_CACHE_ALLOW_GATED") != "" {
		t.Logf("REASONIX_LIVE_CACHE_ALLOW_GATED is set, but this driver refuses gated arms: %s", arm.Gate)
	}
	return arm
}

// experimentFixture builds the arm's frozen bytes: its registered component
// perturbation, or its registered ladder size, or the baseline request.
func experimentFixture(arm cachelab.Arm, nonce string) cachelab.Fixture {
	size := arm.Bytes
	if size <= 0 {
		size = cachelab.LadderSmallBytes
	}
	return cachelab.BuildFixture(cachelab.FixtureSpec{
		Nonce:     nonce,
		Marker:    "CACHELAB-ACK",
		Bytes:     size,
		Component: arm.Component,
	})
}

// experimentJournalPath is where the arm's record lands. REASONIX_LIVE_CACHE_JOURNAL
// keeps it beyond the test's temp directory; the default still survives the run
// so a failure can be read back.
func experimentJournalPath(arm cachelab.Arm, nonce string) string {
	if path := strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_JOURNAL")); path != "" {
		return path
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("cachelab-%s-%s.jsonl", arm.ID, nonce))
}

// experimentPause is the gap after each request. The registered long-interval arm
// uses its own value; every other arm uses the short interval the plan fixes for
// serial runs, so a zero-gap arm is a real inter-request gap, not a burst.
func experimentPause(arm cachelab.Arm) time.Duration {
	if arm.IntervalMS > 0 {
		return time.Duration(arm.IntervalMS) * time.Millisecond
	}
	return 500 * time.Millisecond
}

// experimentPrices reads the registered per-million-token prices. Without them
// the run reports cost as unknown instead of inventing a number.
func experimentPrices() cachelab.Prices {
	read := func(key string) float64 {
		value, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(key)), 64)
		if err != nil {
			return 0
		}
		return value
	}
	return cachelab.Prices{
		HitPerMTok:  read("REASONIX_LIVE_CACHE_PRICE_HIT_PER_MTOK"),
		MissPerMTok: read("REASONIX_LIVE_CACHE_PRICE_MISS_PER_MTOK"),
		OutPerMTok:  read("REASONIX_LIVE_CACHE_PRICE_OUT_PER_MTOK"),
	}
}

// experimentSpend prices the samples recorded so far, so the registered cost cap
// is enforced against real tokens rather than against a request count.
func experimentSpend(samples []cachelab.Sample, prices cachelab.Prices) float64 {
	total := 0.0
	for _, s := range samples {
		if cost, ok := prices.CostUSD(s); ok {
			total += cost
		}
	}
	return total
}

// experimentBuildID is the client identity recorded with every sample, so a
// result can be attributed to one build.
func experimentBuildID() string {
	if build := strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_CLIENT_BUILD")); build != "" {
		return build
	}
	info, ok := debug.ReadBuildInfo()
	if !ok || info == nil {
		return "unknown"
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			return setting.Value
		}
	}
	return "unknown"
}

// runExperimentTurn sends the frozen fixture once and drains the stream, so the
// provider's usage reaches the recorder before the next request starts.
func runExperimentTurn(ctx context.Context, client provider.Provider, fixture cachelab.Fixture) (string, error) {
	stream, err := client.Stream(ctx, experimentRequest(fixture))
	if err != nil {
		return "", err
	}
	var answer strings.Builder
	for chunk := range stream {
		switch chunk.Type {
		case provider.ChunkError:
			if chunk.Err != nil {
				return answer.String(), chunk.Err
			}
		case provider.ChunkText:
			answer.WriteString(chunk.Text)
		}
	}
	return answer.String(), nil
}

// truncateAnswer keeps a logged answer short. Only a synthetic task's answer is
// ever logged; a real member turn's text never reaches this path.
func truncateAnswer(answer string) string {
	answer = strings.TrimSpace(answer)
	if len(answer) <= 80 {
		return answer
	}
	return answer[:80] + "..."
}

// experimentRequest maps the frozen fixture onto one provider request: the system
// block, the sized user turn, and the read-only probe tool surface. Two repeats
// of one arm differ in nothing here, which is what makes the recorder's byte
// comparison meaningful.
func experimentRequest(fixture cachelab.Fixture) provider.Request {
	schemas := make([]provider.ToolSchema, 0, len(fixture.Tools))
	for _, tool := range fixture.Tools {
		schemas = append(schemas, provider.ToolSchema{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters})
	}
	return provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: fixture.System},
			{Role: provider.RoleUser, Content: fixture.User},
		},
		Tools:       schemas,
		MaxTokens:   fixture.MaxTokens,
		Temperature: provider.TemperaturePtr(fixture.Temperature),
	}
}

// experimentAssemblyProbe runs two real turns through the production member
// builder with the member's pool entry pointed at the recorder, then reads the
// member's own record log. It reports what the assembled path did; it asserts
// nothing, because the member surface is the production one and not the fixture.
func experimentAssemblyProbe(t *testing.T, ctx context.Context, creds liveCacheCredential, recorder *cachelab.Recorder, model string) string {
	t.Helper()
	// A private user state root: member skills and deliverable tools resolve
	// there, and an experiment must not touch the operator's own state.
	t.Setenv("REASONIX_STATE_HOME", t.TempDir())
	root := t.TempDir()
	store, err := team.NewTeamStoreAt("", root)
	if err != nil {
		return fmt.Sprintf("store: %v", err)
	}
	sessions, err := team.NewTeamSessionStoreDir(root)
	if err != nil {
		return fmt.Sprintf("sessions: %v", err)
	}
	owners, err := team.NewOwnerStore(root)
	if err != nil {
		return fmt.Sprintf("owners: %v", err)
	}
	user := team.AgentUser{
		UserID: "probe", Provider: "deepseek", Model: model,
		BaseURL: recorder.URL(), APIKey: creds.apiKey,
	}
	if err := store.AddAgentUser(user); err != nil {
		return fmt.Sprintf("pool entry: %v", err)
	}
	confounds := experimentProtocolConfounds(t, user, creds.baseURL, recorder.URL())
	deps := memberBackendDeps{
		ctx: ctx, users: store, store: store, sessions: sessions,
		events: newMemberEventPump(), owners: owners, workspaceRoot: t.TempDir(),
		base: func() boot.Options { return boot.Options{SessionDir: t.TempDir(), Stderr: io.Discard} },
	}
	backend, err := newMemberBackendBuilder(deps)(team.MemberBinding{
		Team: "probe", MemberID: "probe", AgentUserRef: "probe", Leader: false,
		SessionFile: sessionFilePath(t, "probe"),
	})
	if err != nil {
		return fmt.Sprintf("assemble: %v", err)
	}
	defer backend.Close()
	for turn := 1; turn <= 2; turn++ {
		recorder.Begin(cachelab.TurnContext{
			Arm: "assembly-probe", TeamID: "probe", MemberID: "probe", ModelRef: model,
			ClientCommit: experimentBuildID(), TurnSeq: turn, Confounds: confounds,
		})
		if err := backend.RunTurn(ctx, "Reply with exactly the word PROBE-ACK and nothing else."); err != nil {
			return fmt.Sprintf("turn %d: %v", turn, err)
		}
	}
	records, err := owners.ReadCacheRequests(ctx, team.OwnerKey{TeamID: "probe", MemberID: "probe"})
	if err != nil {
		return fmt.Sprintf("member records: %v", err)
	}
	return fmt.Sprintf("member records=%d confounds=%v", len(records), confounds)
}

// sessionFilePath resolves the probe member's session file.
func sessionFilePath(t *testing.T, memberID string) string {
	t.Helper()
	name, err := team.MemberSessionFile("probe", memberID)
	if err != nil {
		t.Fatalf("member session file: %v", err)
	}
	return name
}

// TestExperimentRecorderEndpointKeepsTheClientProtocol answers, without a
// credential, whether pointing a member at the loopback recorder makes the client
// derive a different reasoning protocol than the real endpoint. A difference is
// a confound on every sample the recorder takes, and the test names it.
func TestExperimentRecorderEndpointKeepsTheClientProtocol(t *testing.T) {
	real := strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_BASE_URL"))
	if real == "" {
		real = strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL"))
	}
	if real == "" {
		t.Skip("set REASONIX_LIVE_CACHE_BASE_URL (or ANTHROPIC_BASE_URL) to compare the endpoint contract")
	}
	user := team.AgentUser{UserID: "probe", Provider: "deepseek", Model: liveCacheModel, BaseURL: real, APIKey: "unused"}
	confounds := experimentProtocolConfounds(t, user, real, "http://127.0.0.1:1")
	if len(confounds) == 0 {
		t.Logf("the recorder endpoint derives the same reasoning protocol as %s", real)
		return
	}
	t.Logf("the recorder endpoint changes the derived protocol: %v", confounds)
}

// experimentProtocolConfounds reports whether replacing a member's endpoint with
// the loopback recorder would change the reasoning protocol the client derives,
// which would make every recorded sample a different request than production.
func experimentProtocolConfounds(t *testing.T, user team.AgentUser, realURL, recorderURL string) []string {
	t.Helper()
	real, err := newMemberProviderResolver(user, netclient.ProxySpec{Mode: netclient.ModeOff})
	if err != nil {
		t.Fatalf("resolve real endpoint: %v", err)
	}
	user.BaseURL = recorderURL
	recording, err := newMemberProviderResolver(user, netclient.ProxySpec{Mode: netclient.ModeOff})
	if err != nil {
		t.Fatalf("resolve recorder endpoint: %v", err)
	}
	if real.reasoningProtocol == recording.reasoningProtocol {
		return nil
	}
	return []string{cachelab.ConfoundEndpointProtocol}
}
