//go:build live

package cli

// Part C's condition pilot: one registered condition through the production member
// options against the real provider, under a registered trigger profile. Run it
// with REASONIX_LIVE_CACHE_CONDITION=<id>; see TestLiveConditionMatrixPilot.

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
	"reasonix/internal/cachelab"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/team"
)

// conditionTurns is how many turns each pilot arm runs. Four is what makes the
// second request a warm candidate and leaves room for a maintenance event to
// follow one, which is all a provenance pilot needs.
const conditionTurns = 4

// conditionBriefKB sizes the opening message. It must place the prompt above the
// pressure profile's trigger, or the arm proves nothing about the maintenance
// path; the driver asserts that rather than trusting the constant.
const conditionBriefKB = 240

// TestLiveConditionMatrixPilot drives one condition arm end to end.
func TestLiveConditionMatrixPilot(t *testing.T) {
	creds := liveCacheCredentials()
	if creds.baseURL == "" || creds.apiKey == "" {
		t.Skip("set REASONIX_LIVE_CACHE_BASE_URL/REASONIX_LIVE_CACHE_API_KEY (or ANTHROPIC_BASE_URL/ANTHROPIC_AUTH_TOKEN) to run a condition arm")
	}
	conditionID := strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_CONDITION"))
	if conditionID == "" {
		t.Skip("set REASONIX_LIVE_CACHE_CONDITION to a registered condition id; a pilot without one would be an unregistered build")
	}
	spec, err := cachelab.ConditionByID(conditionID)
	if err != nil {
		t.Fatal(err)
	}
	profileID := strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_TRIGGER"))
	if profileID == "" {
		profileID = "pressure"
	}
	profile, err := cachelab.TriggerProfileByID(profileID)
	if err != nil {
		t.Fatal(err)
	}
	model := strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_MODEL"))
	if model == "" {
		model = liveCacheModel
	}
	archive := conditionArchiveDir()
	build := pilotBuildID(t)
	runID := fmt.Sprintf("%d", time.Now().UnixNano())

	// A private user state root, so a live run never touches the operator's own
	// member skills or deliverable tools.
	t.Setenv("REASONIX_STATE_HOME", t.TempDir())
	if dir := config.MemoryUserDir(); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.Default()
	cfg.DefaultModel = "cond-gw/" + model + "[1m]"
	cfg.Agent.CompactRatio = profile.Ratio
	cfg.Agent.VisibleWindowTokens = profile.VisibleWindowTokens
	applyConditionSwitches(t, cfg, spec)

	// A real store and owner root, not a test double: the production builder
	// resolves the pool entry and writes the member's records through them, so a
	// driver that skipped either would measure an assembly production lacks.
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
	user := team.AgentUser{
		UserID: "cond-gw", Provider: "deepseek", Model: model + "[1m]",
		BaseURL: creds.baseURL, APIKey: creds.apiKey,
	}
	confounds := experimentProtocolConfounds(t, user, creds.baseURL, creds.baseURL)

	journal, err := cachelab.OpenJournal(conditionJournalPath(runID))
	if err != nil {
		t.Fatal(err)
	}
	journal.Guard(conditionGuardLiterals()...)
	defer journal.Close()
	recorder, err := cachelab.NewRecorder(creds.baseURL, journal)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()

	// The member dials the recorder, which forwards the client's own bytes to the
	// real provider. The pool entry moves here only after the protocol comparison
	// above ran against the real endpoint.
	user.BaseURL = recorder.URL()
	if err := store.AddTeam(team.Team{Name: conditionTeam}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddAgentUser(user); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMember(conditionTeam, team.MemberSlot{
		MemberID: conditionMember, Role: "member", AgentUserRef: user.UserID,
	}); err != nil {
		t.Fatal(err)
	}
	sessionFile, err := team.MemberSessionFile(conditionTeam, conditionMember)
	if err != nil {
		t.Fatal(err)
	}

	deps := memberBackendDeps{
		ctx: t.Context(), users: store, store: store, sessions: sessions,
		events: newMemberEventPump(), owners: owners, workspaceRoot: t.TempDir(),
		base: func() boot.Options {
			return boot.Options{SessionDir: t.TempDir(), Stderr: io.Discard, ConfigSnapshot: cfg}
		},
	}
	// The production builder itself, not a hand-rolled subset of it: it binds the
	// member's session file, takes the write lease and starts the usage publisher,
	// which is what makes the member's own records exist at all.
	backend, err := newMemberBackendBuilder(deps)(team.MemberBinding{
		Team: conditionTeam, MemberID: conditionMember, AgentUserRef: user.UserID,
		SessionFile: sessionFile,
	})
	if err != nil {
		t.Fatalf("assemble member under condition %s: %v", spec.Condition, err)
	}
	defer backend.Close()
	// The member's own controller: the backend IS the SessionAPI the builder
	// returned, so the boundaries and the maintenance spend read off it are the
	// ones this member's agent holds.
	status, ok := backend.(control.Status)
	if !ok {
		t.Fatalf("the member backend does not expose its status: %T", backend)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	started := time.Now().UTC()
	before := status.ContextMaintenanceSnapshot()
	for turn := 1; turn <= conditionTurns; turn++ {
		recorder.Begin(cachelab.TurnContext{
			Arm: "C-" + string(spec.Condition), RunID: runID, TeamID: conditionTeam, MemberID: conditionMember,
			ModelRef: model, Client: "reasonix", ClientCommit: build,
			ConfigDigest: cachelab.ConfigDigest(string(spec.Condition), profile.ID, recorder.UpstreamHost(), model),
			TurnSeq:      turn, Expect: conditionMarker, Confounds: confounds,
		})
		prompt := fmt.Sprintf("Turn %d: reply with exactly the word %s and nothing else.", turn, conditionMarker)
		if turn == 1 {
			prompt = conditionBrief(conditionBriefKB, runID) + "\n" + prompt
		}
		if err := backend.RunTurn(ctx, prompt); err != nil {
			t.Fatalf("condition %s turn %d: %v", spec.Condition, turn, err)
		}
		time.Sleep(400 * time.Millisecond)
	}
	finished := time.Now().UTC()
	after := status.ContextMaintenanceSnapshot()

	samples := recorder.WaitForSamples(conditionTurns, 30*time.Second)
	records := waitForConditionRecords(t, owners)
	// A condition arm is the baseline's request bytes under one build, so it
	// carries the baseline's own warm gate — which counts eligible warm samples,
	// not turns, because one turn can legitimately produce several requests.
	arm := conditionArm(spec)
	stopped := cachelab.StopReason(arm, samples, 0, false)
	assertConditionPilot(t, spec, profile, after, samples, records)
	logConditionPilot(t, runID, spec, profile, before, after, samples, records)
	conditionArchive(t, archive, runID, build, spec, profile, before, after, samples, records, started, finished)
	if stopped != "" {
		t.Logf("registered stop reason reached: %s", stopped)
	}
}

// conditionMarker is the exact word the frozen task asks for, so the quality
// check is mechanical and needs no model judgement.
const conditionMarker = "COND-MATRIX-ACK"

// conditionArm is the registered arm a condition run executes: the baseline
// request bytes under one build, gated exactly as the baseline it is compared to.
func conditionArm(spec cachelab.ConditionSpec) cachelab.Arm {
	arm, err := cachelab.ArmByID("B1-baseline-repeat")
	if err != nil {
		panic(err)
	}
	arm.ID = "C-" + string(spec.Condition)
	arm.Changed = "client condition " + string(spec.Condition)
	arm.Conditions = []cachelab.Condition{spec.Condition}
	return arm
}

// conditionArchiveDir is where one arm's evidence lands. It must be named
// explicitly: the test binary redirects HOME to a disposable directory, so a
// home-derived default would be deleted when the process exits. The files go
// under a condition-matrix subdirectory because the archive is shared with the
// other parts' evidence.
func conditionArchiveDir() string {
	root := strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_ARCHIVE"))
	if root == "" {
		root = filepath.Join(os.TempDir(), "cachelab-condition")
	}
	return filepath.Join(root, "condition-matrix")
}

// conditionBrief sizes the opening message and makes its prefix unique to the
// run. The nonce is not decoration: a brief shared between arms would let one
// arm warm the next, and an arm whose first request reads a prefix it did not
// send is not a cold start — the matrix would then compare warmths it created
// rather than the builds it registered. It is the same rule cachelab.Fixture
// already applies to the frozen ladder.
func conditionBrief(kilobytes int, nonce string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Reference brief, run-nonce %s. Read it, then answer the single-word question at the end.\n", nonce)
	for i := 0; b.Len() < kilobytes*1024; i++ {
		fmt.Fprintf(&b, "brief line %06d: reference material for this member's opening context.\n", i)
	}
	return b.String()
}

// The pilot's own team and member. One member is enough for a provenance pilot:
// this arm is about one build's own configuration, not about member interaction.
const (
	conditionTeam   = "cond"
	conditionMember = "cond"
)

// conditionGuardLiterals are the strings the journal must never hold. They are
// the brief's own prose, which the member's request carries and the journal must
// not.
// conditionGuardLiterals are the prompt's own words. The run nonce is NOT one of
// them: it is legitimately journalled as run_id, and guarding it would make the
// guard refuse every line — an empty journal that still looks like a clean run.
func conditionGuardLiterals() []string {
	return []string{"reference material for this member's opening context", conditionMarker}
}

// conditionJournalPath is where the arm's boundary record lands. It goes under
// the archive's own condition-matrix directory rather than beside the archive
// root: the archive is shared with the other parts' evidence, and an
// experiment's files must not land among them.
func conditionJournalPath(runID string) string {
	return filepath.Join(conditionArchiveDir(), "condition-journal-"+runID+".jsonl")
}

// waitForConditionRecords waits for the writer to drain its queue.
func waitForConditionRecords(t *testing.T, owners *team.OwnerStore) []team.MemberCacheRequest {
	t.Helper()
	var last []team.MemberCacheRequest
	for range 300 {
		recs, err := owners.ReadCacheRequests(context.Background(), team.OwnerKey{TeamID: conditionTeam, MemberID: conditionMember})
		if err != nil {
			t.Fatalf("ReadCacheRequests: %v", err)
		}
		last = recs
		if len(recs) >= conditionTurns {
			return recs
		}
		time.Sleep(100 * time.Millisecond)
	}
	return last
}

// assertConditionPilot pins what this driver owns: the arm consumed its own
// recipe on the member's agent, the maintenance path was entered, and the two
// records describe the same requests.
func assertConditionPilot(t *testing.T, spec cachelab.ConditionSpec, profile cachelab.TriggerProfile,
	after agent.ContextMaintenanceSnapshot, samples []cachelab.Sample, records []team.MemberCacheRequest) {
	t.Helper()
	// The recipe must reach the member's OWN agent, not only the config it was
	// assembled from: FoldTrigger is the consumed form of compact_ratio.
	if profile.Ratio > 0 && after.FoldTrigger <= 0 {
		t.Fatalf("profile %s set ratio %.2f but the member's agent reports no trigger", profile.ID, profile.Ratio)
	}
	// Checked by where the profile put the trigger, not by a ratio recomputed from
	// a guessed window: the registered profiles sit on opposite sides of the
	// ceiling's midpoint, and a profile that never arrived leaves production's.
	if after.FoldTrigger <= 0 || after.HardInputCeiling <= 0 {
		t.Fatalf("the member reports no boundaries (%d/%d); the build did not complete", after.FoldTrigger, after.HardInputCeiling)
	}
	half := after.HardInputCeiling / 2
	lowered := after.FoldTrigger < half
	if wantLowered := profile.Ratio > 0; lowered != wantLowered {
		t.Fatalf("profile %s put the trigger at %d against the %d ceiling (lowered=%v, want %v): the profile did not reach the member's agent",
			profile.ID, after.FoldTrigger, after.HardInputCeiling, lowered, wantLowered)
	}
	// The goal must fit inside the trigger, or every fold latches by construction
	// and the arm would report the profile's arithmetic as a maintenance finding.
	if after.HeadroomGoal > 0 && after.HeadroomGoal >= after.FoldTrigger {
		t.Fatalf("profile %s leaves a headroom goal of %d against a %d trigger: no fold could ever meet it",
			profile.ID, after.HeadroomGoal, after.FoldTrigger)
	}
	if spec.Switches["agent.cache_aware_compaction"] != "false" {
		t.Fatalf("condition %s does not hold cache_aware_compaction at the matrix's fixed value", spec.Condition)
	}
	if len(samples) < conditionTurns {
		t.Fatalf("the boundary recorded %d of %d requests", len(samples), conditionTurns)
	}
	if len(records) < conditionTurns {
		t.Fatalf("the member writer recorded %d of %d requests", len(records), conditionTurns)
	}
	// When the maintenance path ran, the records that followed it must carry the
	// decision: this is the end-to-end proof that the projection reaches a reader
	// through the production member path, not only through a unit fixture.
	if after.MaintenanceCost.SummaryRequests > 0 {
		carried := 0
		for _, rec := range records {
			if rec.MaintenanceObserved && rec.MaintenanceState == after.MaintenanceState {
				carried++
			}
		}
		if carried == 0 {
			t.Fatalf("the arm paid for %d summaries and ended in %q, but no member record carries that decision",
				after.MaintenanceCost.SummaryRequests, after.MaintenanceState)
		}
	}
	// The journal must hold what the recorder published: a guard that refuses
	// every line, or a torn write, otherwise leaves a run whose in-memory samples
	// look complete and whose durable record is empty.
	if lines := journalLines(t, samples); lines != len(samples) {
		t.Fatalf("the journal holds %d of the %d published samples", lines, len(samples))
	}
	// Every recorded sample belongs to this arm's run, which is what makes the
	// two records comparable rather than two runs pooled.
	for _, s := range samples {
		if s.Arm != "C-"+string(spec.Condition) {
			t.Fatalf("sample %d belongs to arm %s", s.Seq, s.Arm)
		}
		if s.ConfigDigest == "" {
			t.Fatalf("sample %d carries no configuration digest, so a drifted run would be undetectable", s.Seq)
		}
	}
}

// journalLines counts the run's own samples in the durable journal, reading it
// back the way a later analysis would.
func journalLines(t *testing.T, samples []cachelab.Sample) int {
	t.Helper()
	if len(samples) == 0 {
		return 0
	}
	stored, skipped, err := cachelab.ReadJournal(conditionJournalPath(samples[0].RunID))
	if err != nil {
		t.Fatalf("read the journal back: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("the journal skipped %d lines, so the record is not trustworthy", skipped)
	}
	return len(stored)
}

// logConditionPilot prints the arm's real numbers. Nothing here is asserted:
// they are the pilot's descriptive record.
func logConditionPilot(t *testing.T, runID string, spec cachelab.ConditionSpec, profile cachelab.TriggerProfile,
	before, after agent.ContextMaintenanceSnapshot, samples []cachelab.Sample, records []team.MemberCacheRequest) {
	t.Helper()
	t.Logf("condition=%s trigger_profile=%s production_representative=%v", spec.Condition, profile.ID, profile.ProductionRepresentative)
	t.Logf("member agent: fold_trigger=%d hard_ceiling=%d headroom_goal=%d (before: fold_trigger=%d)",
		after.FoldTrigger, after.HardInputCeiling, after.HeadroomGoal, before.FoldTrigger)
	t.Logf("maintenance spend: summaries=%d installs=%d rescues=%d repeat_blocks=%d state=%q",
		after.MaintenanceCost.SummaryRequests, after.MaintenanceCost.ProjectionInstalls, after.MaintenanceCost.RescueCount, after.MaintenanceCost.RepeatBlocks, after.MaintenanceState)
	stats := cachelab.Summarize(conditionArm(spec), samples, cachelab.Prices{})
	t.Logf("boundary: samples=%d eligible_warm=%d first=%d errors=%d no_cache_split=%d warm_rate=%.2f%%",
		stats.Samples, stats.Eligible, stats.FirstRequest, stats.Errors, stats.NoCacheSplit, stats.Rate*100)
	for _, rec := range records {
		t.Logf("member record: seq=%d prompt=%d hit=%d miss=%d reqs=%d reasons=%v rewritten=%d maintenance=%v/%s headroom=%d reduction=%.3f",
			rec.SessionRequestSeq, rec.ContextPromptTokens, rec.CacheHitTokens, rec.CacheMissTokens,
			rec.RequestCount, rec.PrefixChangeReasons, rec.MessagesRewritten,
			rec.MaintenanceObserved, rec.MaintenanceState, rec.HeadroomTokens, rec.ReductionRatio)
	}
	t.Logf("journal: %s", conditionJournalPath(runID))
}

// conditionArchive writes the arm's evidence: the boundary record exactly as the
// journal holds it, the member records exactly as the writer stored them, and a
// banner naming the build, the condition and the profile. It carries no prompt,
// tool argument or credential: the records never held any, and the banner is
// built from identifiers the driver already knows.
func conditionArchive(t *testing.T, dir, runID, build string, spec cachelab.ConditionSpec, profile cachelab.TriggerProfile,
	before, after agent.ContextMaintenanceSnapshot, samples []cachelab.Sample, records []team.MemberCacheRequest,
	started, finished time.Time) {
	t.Helper()
	writeConditionJSONL(t, filepath.Join(dir, "condition-boundary-"+runID+".jsonl"), samples)
	writeConditionJSONL(t, filepath.Join(dir, "condition-members-"+runID+".jsonl"), records)
	banner := []string{
		"run_id: " + runID,
		"started_utc: " + started.Format(time.RFC3339Nano),
		"finished_utc: " + finished.Format(time.RFC3339Nano),
		"build_commit: " + build,
		"condition: " + string(spec.Condition),
		"trigger_profile: " + profile.ID,
		"production_representative: " + fmt.Sprintf("%v", profile.ProductionRepresentative),
		fmt.Sprintf("switches: %v", spec.Switches),
		fmt.Sprintf("member_fold_trigger: %d", after.FoldTrigger),
		fmt.Sprintf("member_headroom_goal: %d", after.HeadroomGoal),
		fmt.Sprintf("member_hard_ceiling: %d", after.HardInputCeiling),
		fmt.Sprintf("maintenance_cost: summaries=%d installs=%d rescues=%d repeat_blocks=%d",
			after.MaintenanceCost.SummaryRequests, after.MaintenanceCost.ProjectionInstalls, after.MaintenanceCost.RescueCount, after.MaintenanceCost.RepeatBlocks),
		"concurrency_level: 1 (serial, one request in flight; registered, not measured)",
	}
	path := filepath.Join(dir, "condition-banner-"+runID+".txt")
	if err := os.WriteFile(path, []byte(strings.Join(banner, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("archived: %s", dir)
}

// writeConditionJSONL writes one slice as newline-delimited JSON at 0600.
func writeConditionJSONL[T any](t *testing.T, path string, rows []T) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
