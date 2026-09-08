package cli

// Cross-team ownership regression for the leader redrive surface (§4): on a
// shared board, retry/cancel/reassign refuse rows another team's template owns,
// leaving the sibling's row and members untouched.

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/control"
	"reasonix/internal/team"
)

// multiTeamRedriveFixture roots two teams — alpha with a coder, beta with a
// tester — over one shared SQLite board, each with its own per-member stub
// backend, the shape a multi-team host session builds.
func multiTeamRedriveFixture(t *testing.T) (alpha, beta *teamTaskService, board *team.SQLiteStore, submits map[string]*taskBackendStub) {
	t.Helper()
	root := t.TempDir()
	teamStore, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := teamStore.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{
		{Name: "alpha", Template: []team.MemberSlot{
			{MemberID: "lead", Leader: true, Status: team.MemberStatusActive, Role: team.RoleCoder},
			{MemberID: "coder", Role: team.RoleCoder, Status: team.MemberStatusActive},
		}},
		{Name: "beta", Template: []team.MemberSlot{
			{MemberID: "blead", Leader: true, Status: team.MemberStatusActive, Role: team.RoleTester},
			{MemberID: "tester", Role: team.RoleTester, Status: team.MemberStatusActive},
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	board, err = team.NewSQLiteStore(context.Background(), filepath.Join(root, "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { board.Close() })
	submits = map[string]*taskBackendStub{"coder": {}, "tester": {}}
	rootSvc := newTeamTaskService(teamStore, board, "", func(b team.MemberBinding) (control.SessionAPI, error) {
		if st, ok := submits[b.MemberID]; ok {
			return st, nil
		}
		return nil, fmt.Errorf("no stub for %s", b.MemberID)
	})
	return rootSvc.forTeam("alpha"), rootSvc.forTeam("beta"), board, submits
}

func namedLeaderRedriveTool(t *testing.T, svc *teamTaskService, teamName, leaderID, name string) *teamTaskTool {
	t.Helper()
	for _, candidate := range newLeaderTaskTools(svc, teamName, leaderID) {
		if candidate.Name() == name {
			return candidate.(*teamTaskTool)
		}
	}
	t.Fatalf("%s tool is missing", name)
	return nil
}

// TestLeaderRedriveRefusesAnotherTeamsRows pins the ownership gate: alpha's
// leader retrying, canceling, or reassigning a row beta's template owns is
// refused by name, and the row — assigned, running, or already closed — never
// moves. The reassign refusal also proves a sibling row cannot be hijacked
// onto alpha's own coder.
func TestLeaderRedriveRefusesAnotherTeamsRows(t *testing.T) {
	alpha, beta, board, submits := multiTeamRedriveFixture(t)
	plant := func(id string, status team.TaskStatus) {
		t.Helper()
		if err := board.SaveTask(context.Background(), team.Task{
			ID: team.TaskID(id), RequireRole: team.RoleTester, Desc: "beta work",
			Status: status, AssignedMember: "tester", CreatedAt: "2026-01-01T00:00:00Z",
		}); err != nil {
			t.Fatal(err)
		}
	}
	retry := namedLeaderRedriveTool(t, alpha, "alpha", "lead", "leader_retry_task")
	cancel := namedLeaderRedriveTool(t, alpha, "alpha", "lead", "leader_cancel_task")
	reassign := namedLeaderRedriveTool(t, alpha, "alpha", "lead", "leader_reassign_task")

	plant("beta-orphan", team.TaskStatusAssigned)
	if _, err := retry.Execute(context.Background(), json.RawMessage(`{"task_id":"beta-orphan"}`)); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("retry of a sibling team's row = %v, want the ownership refusal", err)
	}
	if _, err := cancel.Execute(context.Background(), json.RawMessage(`{"task_id":"beta-orphan"}`)); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("cancel of a sibling team's row = %v, want the ownership refusal", err)
	}
	if _, err := reassign.Execute(context.Background(), json.RawMessage(`{"task_id":"beta-orphan","member_name":"coder"}`)); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("reassign of a sibling team's row onto alpha's coder = %v, want the ownership refusal", err)
	}
	task, err := board.LoadTask(context.Background(), "beta-orphan")
	if err != nil || task.Status != team.TaskStatusAssigned || task.AssignedMember != "tester" {
		t.Fatalf("sibling row after refused redrives = %+v err=%v, want untouched", task, err)
	}

	plant("beta-zombie", team.TaskStatusRunning)
	if _, err := cancel.Execute(context.Background(), json.RawMessage(`{"task_id":"beta-zombie"}`)); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("cancel of a sibling's running row = %v, want the ownership refusal", err)
	}
	if task, err := board.LoadTask(context.Background(), "beta-zombie"); err != nil || task.Status != team.TaskStatusRunning {
		t.Fatalf("sibling running row after refused cancel = %+v err=%v, want untouched", task, err)
	}

	// The ownership gate precedes the terminal-state refusal: a sibling's closed
	// row is refused as not owned, never reported as this team's closed task.
	plant("beta-done", team.TaskStatusReported)
	if _, err := retry.Execute(context.Background(), json.RawMessage(`{"task_id":"beta-done"}`)); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("retry of a sibling's closed row = %v, want the ownership refusal", err)
	}

	if submits["coder"].submits != 0 || submits["tester"].submits != 0 {
		t.Fatalf("no member of either team may receive a turn: coder=%d tester=%d", submits["coder"].submits, submits["tester"].submits)
	}
	// beta's own tool still owns and drives its row on the shared board.
	if _, err := namedLeaderRedriveTool(t, beta, "beta", "blead", "leader_retry_task").Execute(context.Background(), json.RawMessage(`{"task_id":"beta-orphan"}`)); err != nil {
		t.Fatalf("beta must still retry its own row: %v", err)
	}
	if submits["tester"].submits != 1 {
		t.Fatalf("beta's retry must drive beta's tester, got %d", submits["tester"].submits)
	}
}

// TestLeaderRedriveKeepsOwnRowsOnSharedBoard guards the gate from overreach:
// on the same shared board, each team's leader still retries its own rows
// while the sibling's row is left exactly where it was.
func TestLeaderRedriveKeepsOwnRowsOnSharedBoard(t *testing.T) {
	alpha, beta, board, submits := multiTeamRedriveFixture(t)
	if err := board.SaveTask(context.Background(), team.Task{
		ID: "alpha-a", RequireRole: team.RoleCoder, Desc: "alpha work",
		Status: team.TaskStatusAssigned, AssignedMember: "coder", CreatedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := board.SaveTask(context.Background(), team.Task{
		ID: "beta-b", RequireRole: team.RoleTester, Desc: "beta work",
		Status: team.TaskStatusAssigned, AssignedMember: "tester", CreatedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := namedLeaderRedriveTool(t, alpha, "alpha", "lead", "leader_retry_task").Execute(context.Background(), json.RawMessage(`{"task_id":"alpha-a"}`)); err != nil {
		t.Fatalf("alpha must retry its own row: %v", err)
	}
	if _, err := namedLeaderRedriveTool(t, beta, "beta", "blead", "leader_retry_task").Execute(context.Background(), json.RawMessage(`{"task_id":"beta-b"}`)); err != nil {
		t.Fatalf("beta must retry its own row: %v", err)
	}
	if submits["coder"].submits != 1 || submits["tester"].submits != 1 {
		t.Fatalf("each team must drive exactly its own member: coder=%d tester=%d", submits["coder"].submits, submits["tester"].submits)
	}
	if task, err := board.LoadTask(context.Background(), "beta-b"); err != nil || task.Status != team.TaskStatusRunning || task.AssignedMember != "tester" {
		t.Fatalf("beta's row after both retries = %+v err=%v, want running on tester", task, err)
	}
}
