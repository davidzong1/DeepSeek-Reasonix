package cli

// Acceptance for the team knowledge recall/expire seam: the recall tool
// contract, per-team isolation, soft-delete retire/expire, and the defect gate.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/knowledge_base/model"
	"reasonix/internal/team"
	"reasonix/internal/tool"
)

// krecallHarnessTeams returns per-team task services over one shared KB data
// root and one discussion data root, so cross-team isolation is exercised
// against the real <dataRoot>/<team> partitions.
func krecallHarnessTeams(t *testing.T) (alpha, beta *teamTaskService) {
	t.Helper()
	root := t.TempDir()
	store, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	slot := []team.MemberSlot{
		{MemberID: "lead", Leader: true, Status: team.MemberStatusActive},
		{MemberID: "coder", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}
	doc := team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{
		{Name: "alpha", Template: slot},
		{Name: "beta", Template: slot},
	}}
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	svc := newTeamTaskService(store, nil, "", func(team.MemberBinding) (control.SessionAPI, error) {
		return &taskBackendStub{}, nil
	})
	svc.setKnowledgeDataRoot(filepath.Join(root, "kb"))
	svc.setDiscussionDataDir(root)
	return svc.forTeam("alpha"), svc.forTeam("beta")
}

// krecallSeed ingests one rule-marked conclusion thought through the real
// manager, flushes it, and returns the top live item matching the query token.
// The text must carry an allow marker ("we decided", "convention:") or the
// rule-less classifier would deny it before it ever becomes an item; a token
// beyond the indexed title degrades to this team's live scan, so ≥1 is the
// contract here.
func krecallSeed(t *testing.T, svc *teamTaskService, teamID, text, q string) model.KnowledgeItem {
	t.Helper()
	tk, err := svc.ensureKB()
	if err != nil || tk == nil {
		t.Fatalf("ensureKB: tk=%v err=%v", tk, err)
	}
	if _, err := tk.Manager.Ingest(context.Background(), []model.Thought{{
		ID: model.NewID(), TeamID: teamID, AgentID: "alice", Kind: model.ThoughtConclusion, Text: text,
	}}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if err := tk.Manager.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	got, err := tk.Manager.Query(context.Background(), model.Query{Text: q, Scope: model.ScopeTeam, Limit: 5})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) < 1 {
		t.Fatalf("seed text %q must yield a live item matching %q, got 0", text, q)
	}
	return got[0].Item
}

// krecallRetire retires an item through the real manager — the soft-delete a
// future expiry sweep would ride on.
func krecallRetire(t *testing.T, svc *teamTaskService, id string) {
	t.Helper()
	tk, err := svc.ensureKB()
	if err != nil || tk == nil {
		t.Fatalf("ensureKB: tk=%v err=%v", tk, err)
	}
	if err := tk.Manager.Retire(context.Background(), []string{id}, model.ReasonNoLongerTrue); err != nil {
		t.Fatalf("retire: %v", err)
	}
	if err := tk.Manager.Flush(context.Background()); err != nil {
		t.Fatalf("flush after retire: %v", err)
	}
}

// krecallExec drives the member tool face end to end, exactly as a member
// backend would invoke team_knowledge_recall.
func krecallExec(t *testing.T, svc *teamTaskService, query string) (string, error) {
	t.Helper()
	for _, tl := range newMemberTaskTools(svc, svc.teamName, "coder") {
		if tl.Name() != "team_knowledge_recall" {
			continue
		}
		args, err := json.Marshal(map[string]string{"query": query})
		if err != nil {
			t.Fatal(err)
		}
		return tl.Execute(context.Background(), args)
	}
	t.Fatalf("team_knowledge_recall absent from member tool set")
	return "", nil
}

// TestRecallToolContractOnBothFaces pins the frozen tool shape: present and
// read-only on both faces, schema declares exactly the required query param,
// and no KB-affecting write tool shares a face.
func TestRecallToolContractOnBothFaces(t *testing.T) {
	alpha, _ := krecallHarnessTeams(t)
	leaderRecall := findKRecallTool(t, newLeaderTaskTools(alpha, "alpha", "lead"))
	memberRecall := findKRecallTool(t, newMemberTaskTools(alpha, "alpha", "coder"))
	if string(leaderRecall.Schema()) != string(memberRecall.Schema()) {
		t.Fatalf("leader and member recall schemas must match")
	}
	for name, recall := range map[string]tool.Tool{"leader": leaderRecall, "member": memberRecall} {
		if !recall.ReadOnly() {
			t.Errorf("%s team_knowledge_recall must be read-only", name)
		}
		if concrete, ok := recall.(*teamTaskTool); !ok || !concrete.PlanModeSafe() {
			t.Errorf("%s team_knowledge_recall must be plan-safe", name)
		}
		var dec struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(recall.Schema(), &dec); err != nil {
			t.Fatalf("%s schema: %v", name, err)
		}
		if len(dec.Properties) != 1 || dec.Properties["query"] == nil {
			t.Errorf("%s schema must declare only query, got %v", name, dec.Properties)
		}
		if len(dec.Required) != 1 || dec.Required[0] != "query" {
			t.Errorf("%s schema must require query, got %v", name, dec.Required)
		}
		if !strings.Contains(recall.Description(), "Read-only") {
			t.Errorf("%s description must state read-only", name)
		}
	}
}

func findKRecallTool(t *testing.T, tools []tool.Tool) tool.Tool {
	t.Helper()
	for _, tl := range tools {
		if tl.Name() == "team_knowledge_recall" {
			return tl
		}
	}
	t.Fatalf("team_knowledge_recall absent from tool set")
	return nil
}

// TestRecallEmptyQueryAndDisabledKBGuards pins the two degradation contracts:
// a blank query is refused, and a host without a KB reports a friendly string
// instead of failing the read tool.
func TestRecallEmptyQueryAndDisabledKBGuards(t *testing.T) {
	alpha, _ := krecallHarnessTeams(t)
	if _, err := krecallExec(t, alpha, "   "); err == nil || !strings.Contains(err.Error(), "query is required") {
		t.Fatalf("blank query must be refused, got %v", err)
	}
	store, err := team.NewTeamStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	off := newTeamTaskService(store, nil, "alpha", func(team.MemberBinding) (control.SessionAPI, error) {
		return &taskBackendStub{}, nil
	})
	if out, err := off.recallKnowledge("anything"); err != nil || !strings.Contains(out, "not enabled") {
		t.Fatalf("disabled KB must degrade to a friendly string, got out=%q err=%v", out, err)
	}
}

// TestRecallTeamIsolationAndRetiredExclusion pins scope: each team's recall
// sees only its own live knowledge (never a sibling team's), and a retired item
// is a soft delete the live query no longer returns.
func TestRecallTeamIsolationAndRetiredExclusion(t *testing.T) {
	const alphaTok, betaTok = "signing", "wakeup"
	alpha, beta := krecallHarnessTeams(t)
	alphaItem := krecallSeed(t, alpha, "alpha",
		"we decided the recall tool is the single read seam for a team; signing rotation stays out of the KB scope", alphaTok)
	krecallSeed(t, beta, "beta",
		"convention: every wakeup event carries an idempotency key so a replay never double fires", betaTok)

	out, err := krecallExec(t, alpha, alphaTok)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, alphaTok) || strings.Contains(out, betaTok) {
		t.Fatalf("alpha recall must return only alpha knowledge:\n%s", out)
	}
	// A cross-team query must never surface beta's content. (A query for an
	// out-of-dictionary token degrades to this team's own live scan, so the
	// assertion is the absence of the other team's marker, not "no match".)
	if out, err := krecallExec(t, alpha, betaTok); err != nil || strings.Contains(out, betaTok) {
		t.Fatalf("alpha recall must not leak beta knowledge, out=%q err=%v", out, err)
	}
	if out, err := krecallExec(t, beta, betaTok); err != nil || !strings.Contains(out, betaTok) || strings.Contains(out, alphaTok) {
		t.Fatalf("beta recall must return beta knowledge only, out=%q err=%v", out, err)
	}

	krecallRetire(t, alpha, alphaItem.ID)
	if out, err := krecallExec(t, alpha, alphaTok); err != nil || !strings.Contains(out, "no matching knowledge") {
		t.Fatalf("retired item must be excluded from live recall, out=%q err=%v", out, err)
	}
}

// TestRetireIsIdempotentSoftDelete pins the substrate an expiry sweep depends
// on: retiring the same id twice and an id that was never ingested are both
// no-ops, never errors.
func TestRetireIsIdempotentSoftDelete(t *testing.T) {
	alpha, _ := krecallHarnessTeams(t)
	item := krecallSeed(t, alpha, "alpha",
		"we decided signing rotation is deterministic so a double sweep stays a no-op", "signing")
	for range 2 {
		krecallRetire(t, alpha, item.ID)
	}
	tk, err := alpha.ensureKB()
	if err != nil || tk == nil {
		t.Fatalf("ensureKB: tk=%v err=%v", tk, err)
	}
	if err := tk.Manager.Retire(context.Background(), []string{model.NewID()}, model.ReasonNoLongerTrue); err != nil {
		t.Fatalf("retiring an unknown id must be a no-op, got %v", err)
	}
	if err := tk.Manager.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if out, err := krecallExec(t, alpha, "signing"); err != nil || !strings.Contains(out, "no matching knowledge") {
		t.Fatalf("double-retired item must stay excluded, out=%q err=%v", out, err)
	}
}

// TestRecallReturnsDepositedDiscussionOutcome pins the terminal-discussion
// path: a consensus outcome auto-sediments into the KB, and the recall tool
// surfaces it — the freeze "discussion finality auto-sediments, on-demand read
// finds it".
func TestRecallReturnsDepositedDiscussionOutcome(t *testing.T) {
	alpha, _ := krecallHarnessTeams(t)
	topic := "deposit-recall-e2e"
	if _, err := alpha.DiscussionStart("lead", topic, []string{"coder"}, 1); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := alpha.DiscussionSubmitConclusion("coder", 1, "proposal: deposit then recall must hit"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := alpha.DiscussionNextRound("lead", discussionAdvanceInput{
		ConsensusReached: true,
		Consensus:        "decision: recall sees the deposited outcome",
		TechnicalRoute:   "deposit then read",
	}); err != nil {
		t.Fatalf("next round: %v", err)
	}
	tk, err := alpha.ensureKB()
	if err != nil || tk == nil {
		t.Fatalf("ensureKB: tk=%v err=%v", tk, err)
	}
	if err := tk.Manager.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	out, err := krecallExec(t, alpha, topic)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, topic) {
		t.Fatalf("recall must surface the deposited discussion outcome:\n%s", out)
	}
}

// TestMajorDefectSkipsKnowledgeCapture pins the board-only rule at the KB
// boundary: a result whose first line names a defect never becomes a knowledge
// item (its report already landed on the shared blackboard upstream), while a
// normal result still sediments.
func TestMajorDefectSkipsKnowledgeCapture(t *testing.T) {
	alpha, _ := krecallHarnessTeams(t)
	// Each body is long enough and carries a classifier allow marker, so absent
	// the gate it would become knowledge — the exclusion below is the gate's
	// doing, not the rule-less classifier's.
	for _, d := range []string{
		"defect: transport binds the default even for a force-off member, contradicting the convention we decided",
		"Blocked by: upstream returns nil on start so the roster never renders; we decided to fail loud",
		"critical: provider rejects auth on every member session and the convention requires an explicit error",
		"fatal: opening the roster panics on an empty pool, against the decision to degrade to a friendly hint",
	} {
		alpha.captureTurn("coder", d)
	}
	alpha.captureTurn("coder", "we decided signing keys rotate on role change so no stale grant outlives its window")
	tk, err := alpha.ensureKB()
	if err != nil || tk == nil {
		t.Fatalf("ensureKB: tk=%v err=%v", tk, err)
	}
	if err := tk.Manager.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := tk.Manager.Query(context.Background(), model.Query{Text: "signing", Scope: model.ScopeTeam, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("defect results must not sediment into the KB; only the normal result should, got %d item(s)", len(got))
	}
	for _, m := range []string{"defect", "blocked", "critical", "fatal"} {
		if strings.Contains(strings.ToLower(got[0].Item.Body), m) {
			t.Fatalf("the only KB item must be the normal result, got defect content %q", got[0].Item.Body)
		}
	}
}

// TestIsMajorDefectResultDeterministic pins the pure gate: defect markers lead
// the first line; a normal result or one that merely mentions a defect is not
// misrouted.
func TestIsMajorDefectResultDeterministic(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"defect: roster panics on empty pool", true},
		{"Blocked by: coder terminal wedged", true},
		{"CRITICAL: provider auth expired", true},
		{"fatal: nil deref in scheduler", true},
		{"task shipped; verify on staging", false},
		{"The fix avoids the reported defect regression", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isMajorDefectResult(tc.text); got != tc.want {
			t.Errorf("isMajorDefectResult(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

// krecallExpire drives the leader expire tool end to end, exactly as a leader
// backend would invoke team_knowledge_expire.
func krecallExpire(t *testing.T, svc *teamTaskService, before, reason string) (string, error) {
	t.Helper()
	arg := map[string]string{"before": before}
	if reason != "" {
		arg["reason"] = reason
	}
	args, err := json.Marshal(arg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range newLeaderTaskTools(svc, svc.teamName, "lead") {
		if tl.Name() == "team_knowledge_expire" {
			return tl.Execute(context.Background(), args)
		}
	}
	t.Fatalf("team_knowledge_expire absent from leader tool set")
	return "", nil
}

// TestLeaderExpireToolIsLeaderOnly pins the contract: the expiry write sits on
// the leader face with schema {before required, reason optional}, is not
// read-only, and never appears on the member face.
func TestLeaderExpireToolIsLeaderOnly(t *testing.T) {
	alpha, _ := krecallHarnessTeams(t)
	var expire tool.Tool
	for _, tl := range newLeaderTaskTools(alpha, "alpha", "lead") {
		if tl.Name() == "team_knowledge_expire" {
			expire = tl
		}
	}
	if expire == nil {
		t.Fatal("leader tool set must include team_knowledge_expire")
	}
	if expire.ReadOnly() {
		t.Error("team_knowledge_expire is a write and must not be read-only")
	}
	var dec struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(expire.Schema(), &dec); err != nil {
		t.Fatal(err)
	}
	if len(dec.Properties) != 2 || dec.Properties["before"] == nil || dec.Properties["reason"] == nil {
		t.Errorf("schema must declare before and reason, got %v", dec.Properties)
	}
	if len(dec.Required) != 1 || dec.Required[0] != "before" {
		t.Errorf("schema must require only before, got %v", dec.Required)
	}
	for _, tl := range newMemberTaskTools(alpha, "alpha", "coder") {
		if tl.Name() == "team_knowledge_expire" {
			t.Error("member tool set must never include team_knowledge_expire")
		}
	}
}

// TestLeaderExpireRetiresBeforeCutKeepsAfter pins expiry semantics through the
// real tool: an item last updated before the RFC 3339 cutoff is retired, an item
// seeded after it stays, a re-run is a no-op, and a sibling team is untouched.
func TestLeaderExpireRetiresBeforeCutKeepsAfter(t *testing.T) {
	alpha, beta := krecallHarnessTeams(t)
	krecallSeed(t, alpha, "alpha",
		"we decided signing stale knowledge predates the migration cut", "signing")
	cut := time.Now().UTC()
	krecallSeed(t, alpha, "alpha",
		"we decided rotation fresh knowledge is seeded after the migration cut", "rotation")
	krecallSeed(t, beta, "beta",
		"we decided wakeup beta knowledge must survive an alpha expiry", "wakeup")

	if out, err := krecallExpire(t, alpha, cut.Format(time.RFC3339Nano), ""); err != nil || !strings.Contains(out, "retired 1 no_longer_true") {
		t.Fatalf("first expire must default to no_longer_true and retire exactly 1, out=%q err=%v", out, err)
	}
	if out, err := krecallExec(t, alpha, "signing"); err != nil || strings.Contains(out, "signing") {
		t.Fatalf("pre-cut item must be retired, out=%q err=%v", out, err)
	}
	if out, err := krecallExec(t, alpha, "rotation"); err != nil || !strings.Contains(out, "rotation") {
		t.Fatalf("post-cut item must stay live, out=%q err=%v", out, err)
	}
	if out, err := krecallExpire(t, alpha, cut.Format(time.RFC3339Nano), ""); err != nil || !strings.Contains(out, "retired 0 no_longer_true") {
		t.Fatalf("re-running expire must retire 0, out=%q err=%v", out, err)
	}
	if out, err := krecallExec(t, alpha, "rotation"); err != nil || !strings.Contains(out, "rotation") {
		t.Fatalf("post-cut item must survive the second run, out=%q err=%v", out, err)
	}
	if out, err := krecallExec(t, beta, "wakeup"); err != nil || !strings.Contains(out, "wakeup") {
		t.Fatalf("sibling team must be untouched by an alpha expiry, out=%q err=%v", out, err)
	}
}

// TestLeaderExpireRejectsBadInput pins actionable failures: a missing before, a
// non-RFC timestamp, and an off-whitelist reason each name the fix.
func TestLeaderExpireRejectsBadInput(t *testing.T) {
	alpha, _ := krecallHarnessTeams(t)
	if _, err := krecallExpire(t, alpha, "", ""); err == nil || !strings.Contains(err.Error(), "before is required") {
		t.Fatalf("empty before must be actionable, got %v", err)
	}
	if _, err := krecallExpire(t, alpha, "not-a-time", ""); err == nil || !strings.Contains(err.Error(), "RFC 3339") {
		t.Fatalf("bad timestamp must be actionable, got %v", err)
	}
	valid := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if _, err := krecallExpire(t, alpha, valid, "banana"); err == nil || !strings.Contains(err.Error(), "no_longer_true, tombstone, personal_data") {
		t.Fatalf("off-whitelist reason must be actionable, got %v", err)
	}
}
