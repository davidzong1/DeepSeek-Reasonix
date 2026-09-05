package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/control"
	"reasonix/internal/knowledge_base/model"
	"reasonix/internal/team"
	"reasonix/internal/tool"
)

// mustTeamTool finds a tool by name in a leader/member tool slice.
func mustTeamTool(t *testing.T, tools []tool.Tool, name string) tool.Tool {
	t.Helper()
	for _, tl := range tools {
		if tl.Name() == name {
			return tl
		}
	}
	t.Fatalf("tool %q not present", name)
	return nil
}

// TestTeamRuntimeKnowledgeCaptureRecall proves the F7 host wiring end to end
// through the real durable team runtime: a member's completed turn (its report
// tail) is ingested into the team knowledge base, and the same runtime can
// recall it. The KB is wired to actual member work, not a standalone helper.
func TestTeamRuntimeKnowledgeCaptureRecall(t *testing.T) {
	root := t.TempDir()
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
	board, err := team.NewSQLiteStore(context.Background(), filepath.Join(root, "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer board.Close()
	backend := &taskBackendStub{}
	service := newTeamTaskService(teamStore, board, "", func(team.MemberBinding) (control.SessionAPI, error) {
		return backend, nil
	})
	service.setKnowledgeDataRoot(filepath.Join(root, "kb"))
	defer service.closeKnowledge()
	svc := service.forTeam("alpha")

	// Leader dispatches, member reports — through the exact tool surface the
	// assembled backends expose, so the capture runs the real runtime path.
	if _, err := mustTeamTool(t, newLeaderTaskTools(svc, "alpha", "lead"), "leader_assign_subtask").Execute(context.Background(), json.RawMessage(`{"member_name":"coder","subtask":"choose the audit store"}`)); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if _, err := mustTeamTool(t, newMemberTaskTools(svc, "alpha", "coder"), "member_report_result").Execute(context.Background(), json.RawMessage(`{"result":"decision: adopt sqlite as the audit store\nwe chose sqlite over files for transactional queries"}`)); err != nil {
		t.Fatalf("report: %v", err)
	}

	tk, err := svc.ensureKB()
	if err != nil {
		t.Fatalf("ensureKB: %v", err)
	}
	if tk == nil {
		t.Fatal("capture should have opened the team knowledge base")
	}
	if err := tk.Manager.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	got, err := tk.Manager.Query(context.Background(), model.Query{Text: "sqlite", Scope: model.ScopeTeam, Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("query returned %d items, want the reported decision", len(got))
	}
	it := got[0].Item
	if it.AuthorID != "coder" || !it.Live() || it.Kind != model.ItemDecision {
		t.Fatalf("reported item %+v is not the live coder decision", it)
	}
	// The recall tool surfaces the same knowledge to members.
	out, err := svc.recallKnowledge("sqlite")
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !strings.Contains(out, "sqlite") {
		t.Fatalf("recall did not surface the decision: %q", out)
	}

	// Closing the service drains the worker; reopening from the same data root
	// replays the committed item, so knowledge survives the host teardown.
	svc.closeKnowledge()
	tk2, err := svc.ensureKB()
	if err != nil || tk2 == nil {
		t.Fatalf("reopen after close: tk=%v err=%v", tk2, err)
	}
	if err := tk2.Manager.Flush(context.Background()); err != nil {
		t.Fatalf("reopen Flush: %v", err)
	}
	got2, err := tk2.Manager.Query(context.Background(), model.Query{Text: "sqlite", Scope: model.ScopeTeam, Limit: 10})
	if err != nil || len(got2) != 1 {
		t.Fatalf("reopen query: n=%d err=%v", len(got2), err)
	}
}

// TestTeamRuntimeKnowledgeStaysOffByDefault pins the hermetic default: without
// a configured data root a completed turn never opens a knowledge base, so
// plain team hosts and existing tests keep their behavior and write nothing.
func TestTeamRuntimeKnowledgeStaysOffByDefault(t *testing.T) {
	root := t.TempDir()
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
	board, err := team.NewSQLiteStore(context.Background(), filepath.Join(root, "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer board.Close()
	service := newTeamTaskService(teamStore, board, "", func(team.MemberBinding) (control.SessionAPI, error) {
		return &taskBackendStub{}, nil
	})
	svc := service.forTeam("alpha") // no setKnowledgeDataRoot: KB stays off
	if tk, err := svc.ensureKB(); tk != nil || err != nil {
		t.Fatalf("KB must stay off without a data root: tk=%v err=%v", tk, err)
	}
	if out, err := svc.recallKnowledge("anything"); err != nil || !strings.Contains(out, "not enabled") {
		t.Fatalf("recall on a disabled KB should report not-enabled: %q err=%v", out, err)
	}
	svc.closeKnowledge() // must be a nil-safe no-op
}
