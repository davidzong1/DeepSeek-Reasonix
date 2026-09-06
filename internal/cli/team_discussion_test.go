package cli

// Host-level discussion tests: the service seam drives a session through the
// real team store and deposits its terminal outcome into the team knowledge
// base exactly once, recovering a crash between end and delivery on next start.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/control"
	"reasonix/internal/knowledge_base/model"
	"reasonix/internal/team"
)

func newDiscussionTestService(t *testing.T, root string) *teamTaskService {
	t.Helper()
	teamStore, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := teamStore.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{
		Name: "alpha", Template: []team.MemberSlot{
			{MemberID: "lead", Leader: true, Status: team.MemberStatusActive},
			{MemberID: "coder", Role: team.RoleCoder, Status: team.MemberStatusActive},
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	service := newTeamTaskService(teamStore, nil, "", func(team.MemberBinding) (control.SessionAPI, error) {
		return &taskBackendStub{}, nil
	})
	service.setKnowledgeDataRoot(filepath.Join(root, "kb"))
	service.setDiscussionDataDir(root)
	return service.forTeam("alpha")
}

// TestDiscussionDepositReachesKnowledgeBase pins the whole frozen route through
// the host: a consensus end leaves a pending durable marker, the deposit hands
// the leader's consensus to the KB once, and a new start is only allowed after
// delivery.
func TestDiscussionDepositReachesKnowledgeBase(t *testing.T) {
	svc := newDiscussionTestService(t, t.TempDir())

	started, err := svc.DiscussionStart("lead", "decide the durable store", nil, 3)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if started.Status != team.DiscussionActive || started.Round != 1 {
		t.Fatalf("start must be active/round1, got %+v", started)
	}
	if len(started.Participants) != 1 || started.Participants[0] != "coder" {
		t.Fatalf("derived participants must be the active non-leader roster, got %v", started.Participants)
	}

	// Structured conclusion is the contract; a wrong round is refused.
	if _, err := svc.DiscussionSubmitConclusion("coder", started.Round, "proposal: file store keeps reads simple"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := svc.DiscussionSubmitConclusion("coder", started.Round+1, "future round"); err == nil || !strings.Contains(err.Error(), "round") {
		t.Fatalf("submit to a non-current round must fail, got %v", err)
	}

	// Leader ends on consensus with a decision the KB rule gate promotes.
	out, err := svc.DiscussionNextRound("lead", discussionAdvanceInput{
		ConsensusReached: true,
		Consensus:        "decision: adopt the durable discussion doc",
		TechnicalRoute:   "one schema-versioned doc per team; CAS transitions; ended doc doubles as the outbox",
	})
	if err != nil {
		t.Fatalf("next round: %v", err)
	}
	if !strings.Contains(out, "已结束") {
		t.Fatalf("consensus end must seal the session, got %q", out)
	}

	// The deposit reached the KB and is marked delivered.
	tk, err := svc.ensureKB()
	if err != nil || tk == nil {
		t.Fatalf("ensureKB: tk=%v err=%v", tk, err)
	}
	if err := tk.Manager.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	got, err := tk.Manager.Query(context.Background(), model.Query{Text: "durable discussion doc", Scope: model.ScopeTeam, Limit: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("consensus must become one live KB item, got %d", len(got))
	}
	if it := got[0].Item; it.AuthorID != "lead" || !it.Live() || it.Kind != model.ItemDecision {
		t.Fatalf("deposited item %+v is not the leader's live decision", it)
	}

	ds, err := svc.discussionStore()
	if err != nil || ds == nil {
		t.Fatalf("discussion store unavailable: %v", err)
	}
	doc, err := ds.Load("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Deposit == nil || doc.Deposit.Status != team.DiscussionDepositDelivered {
		t.Fatalf("deposit must be marked delivered, got %+v", doc.Deposit)
	}

	// Delivered exactly once and idempotent to re-run; a new session may start.
	if err := svc.DiscussionDeposit(); err != nil {
		t.Fatalf("re-deposit after delivery must be a no-op, got %v", err)
	}
	if _, err := svc.DiscussionStart("lead", "second session", []string{"coder"}, 1); err != nil {
		t.Fatalf("start after delivered deposit must succeed, got %v", err)
	}
}

// TestDiscussionStartRecoversPendingDeposit pins the crash window: an ended
// session whose outcome never reached the KB (simulated by writing the pending
// marker directly) is delivered by the next start rather than overwritten.
func TestDiscussionStartRecoversPendingDeposit(t *testing.T) {
	root := t.TempDir()
	svc := newDiscussionTestService(t, root)

	// Simulate a crash after End but before delivery: write the pending deposit
	// through the real store, bypassing the service's auto-deliver path.
	fs, err := team.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	ds := team.NewDiscussionStore(fs)
	if err := ds.Update("alpha", func(d *team.DiscussionDoc) error {
		return d.Start("lead", "recover me", []string{"coder"}, 1, "crash-1", "2026-01-01T00:00:00Z")
	}); err != nil {
		t.Fatal(err)
	}
	if err := ds.Update("alpha", func(d *team.DiscussionDoc) error {
		return d.End("lead", team.EndReasonConsensus, "decision: recovery deposits before any new session", "", true, "2026-01-01T00:00:00Z")
	}); err != nil {
		t.Fatal(err)
	}

	// A fresh service (restart) starts a new session; the guard first delivers
	// the stranded outcome.
	if _, err := svc.DiscussionStart("lead", "after crash", []string{"coder"}, 1); err != nil {
		t.Fatalf("start must recover the pending deposit first, got %v", err)
	}
	tk, err := svc.ensureKB()
	if err != nil || tk == nil {
		t.Fatalf("ensureKB: tk=%v err=%v", tk, err)
	}
	if err := tk.Manager.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := tk.Manager.Query(context.Background(), model.Query{Text: "recovery deposits", Scope: model.ScopeTeam, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("recovered consensus must reach the KB, got %d items", len(got))
	}
}

// TestDiscussionConsensusTextSurvivesFlagFalse pins that outcome text is
// authoritative at a round boundary: a consensus written with the reached flag
// unset still ends the session and reaches the KB instead of being dropped on
// an Advance.
func TestDiscussionConsensusTextSurvivesFlagFalse(t *testing.T) {
	svc := newDiscussionTestService(t, t.TempDir())
	if _, err := svc.DiscussionStart("lead", "route only", nil, 3); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := svc.DiscussionSubmitConclusion("coder", 1, "proposal: file store"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	out, err := svc.DiscussionNextRound("lead", discussionAdvanceInput{
		Consensus:      "decision: adopt the file store",
		TechnicalRoute: "one doc per team under CAS",
	})
	if err != nil {
		t.Fatalf("outcome text must end the session even with the flag unset, got %v", err)
	}
	if !strings.Contains(out, "已结束") {
		t.Fatalf("session must be sealed with the outcome, got %q", out)
	}
	tk, err := svc.ensureKB()
	if err != nil || tk == nil {
		t.Fatalf("ensureKB: tk=%v err=%v", tk, err)
	}
	if err := tk.Manager.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	got, err := tk.Manager.Query(context.Background(), model.Query{Text: "adopt the file store", Scope: model.ScopeTeam, Limit: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("the written consensus must reach the KB once, got %d items", len(got))
	}
}

// TestDiscussionNextRoundRefusesUncollectedAdvance pins the matrix gate at the
// host: a leader cannot move a round until every participant concluded it.
func TestDiscussionNextRoundRefusesUncollectedAdvance(t *testing.T) {
	svc := newDiscussionTestService(t, t.TempDir())
	if _, err := svc.DiscussionStart("lead", "wait for all", []string{"coder"}, 3); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := svc.DiscussionNextRound("lead", discussionAdvanceInput{}); err == nil || !strings.Contains(err.Error(), team.ErrRoundIncomplete.Error()) {
		t.Fatalf("advancing an uncollected round must fail closed with %q, got %v", team.ErrRoundIncomplete, err)
	}
	if _, err := svc.DiscussionSubmitConclusion("coder", 1, "conclusion: file store"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := svc.DiscussionNextRound("lead", discussionAdvanceInput{}); err != nil {
		t.Fatalf("advancing a collected round must succeed, got %v", err)
	}
}

// TestDiscussionPendingDepositRecoversAfterKBReturns pins the stranded-deposit
// contract: an ended consensus whose KB delivery could not happen is not a dead
// end — while the KB is disabled Start fails with an actionable error, and the
// first Start after the KB returns delivers the outcome exactly once.
func TestDiscussionPendingDepositRecoversAfterKBReturns(t *testing.T) {
	root := t.TempDir()
	svc := newDiscussionTestService(t, root)
	// End a consensus with deposit=true through the real store, stranding the
	// marker as if the host died right after End.
	fs, err := team.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	ds := team.NewDiscussionStore(fs)
	if err := ds.Update("alpha", func(d *team.DiscussionDoc) error {
		return d.Start("lead", "stranded consensus", []string{"coder"}, 1, "strand-1", "2026-01-01T00:00:00Z")
	}); err != nil {
		t.Fatal(err)
	}
	if err := ds.Update("alpha", func(d *team.DiscussionDoc) error {
		return d.End("lead", team.EndReasonConsensus, "decision: delivery is retryable", "", true, "2026-01-01T00:00:00Z")
	}); err != nil {
		t.Fatal(err)
	}
	// KB off: Start must not be silently blocked behind a bare state sentinel.
	svc.setKnowledgeDataRoot("")
	if _, err := svc.DiscussionStart("lead", "blocked", []string{"coder"}, 1); err == nil || !strings.Contains(err.Error(), "knowledge base") {
		t.Fatalf("start with a stranded deposit and no KB must fail with an actionable error, got %v", err)
	}
	// KB back: the next start recovers the outcome exactly once and proceeds.
	svc.setKnowledgeDataRoot(filepath.Join(root, "kb"))
	if _, err := svc.DiscussionStart("lead", "after recovery", []string{"coder"}, 1); err != nil {
		t.Fatalf("start must recover the stranded deposit once the KB returns, got %v", err)
	}
	tk, err := svc.ensureKB()
	if err != nil || tk == nil {
		t.Fatalf("ensureKB: tk=%v err=%v", tk, err)
	}
	if err := tk.Manager.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := tk.Manager.Query(context.Background(), model.Query{Text: "delivery is retryable", Scope: model.ScopeTeam, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("the recovered consensus must reach the KB exactly once, got %d items", len(got))
	}
}
