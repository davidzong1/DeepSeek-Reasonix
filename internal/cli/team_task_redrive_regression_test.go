package cli

// Regression suite for the recovery/retry/reassign lifecycle (§4): a durable
// row nothing drives is re-drivable by the leader, and a host restart
// reattaches every still-live row to the fresh runtime exactly once.

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/control"
	"reasonix/internal/team"
	teamscheduler "reasonix/internal/team/scheduler"
)

// redriveFixture roots a team with a leader, a coder and a tester over a real
// SQLite board; the per-member stub backends record how often each was asked
// to run a turn.
func redriveFixture(t *testing.T, bind func(team.MemberBinding) (control.SessionAPI, error)) (*teamTaskService, *team.SQLiteStore) {
	t.Helper()
	root := t.TempDir()
	teamStore, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := teamStore.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{
		Name: "alpha", Template: []team.MemberSlot{
			{MemberID: "lead", Leader: true, Status: team.MemberStatusActive, Role: team.RoleCoder},
			{MemberID: "coder", Role: team.RoleCoder, Status: team.MemberStatusActive},
			{MemberID: "tester", Role: team.RoleTester, Status: team.MemberStatusActive},
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	board, err := team.NewSQLiteStore(context.Background(), filepath.Join(root, "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { board.Close() })
	return newTeamTaskService(teamStore, board, "", bind).forTeam("alpha"), board
}

func leaderRedriveTool(t *testing.T, svc *teamTaskService, name string) *teamTaskTool {
	t.Helper()
	for _, candidate := range newLeaderTaskTools(svc, "alpha", "lead") {
		if candidate.Name() == name {
			return candidate.(*teamTaskTool)
		}
	}
	t.Fatalf("%s tool is missing", name)
	return nil
}

// TestLeaderRetryDrivesUndrivenAssignedRow pins the retry entry: a durable
// assigned row nothing drives (the exact shape a refused dispatch or a dead
// runtime leaves) is re-started on its recorded member, keeps its TaskID, and
// the member receives exactly one turn.
func TestLeaderRetryDrivesUndrivenAssignedRow(t *testing.T) {
	backend := &taskBackendStub{}
	svc, board := redriveFixture(t, func(team.MemberBinding) (control.SessionAPI, error) { return backend, nil })
	plantTask(t, board, "alpha-orphan", team.TaskStatusAssigned)

	out, err := leaderRedriveTool(t, svc, "leader_retry_task").Execute(context.Background(), json.RawMessage(`{"task_id":"alpha-orphan"}`))
	if err != nil {
		t.Fatalf("retry must re-drive an undriven assigned row: %v", err)
	}
	if !strings.Contains(out, "alpha-orphan") || !strings.Contains(out, "running") {
		t.Fatalf("retry output = %q, want the same task id re-started", out)
	}
	task, err := board.LoadTask(context.Background(), "alpha-orphan")
	if err != nil || task.Status != team.TaskStatusRunning {
		t.Fatalf("row after retry = %+v err=%v, want running", task, err)
	}
	if task.AssignedMember != "coder" {
		t.Fatalf("retry must keep the recorded member, got %q", task.AssignedMember)
	}
	if backend.submits != 1 {
		t.Fatalf("the member must receive exactly one turn, got %d", backend.submits)
	}
	// The re-driven row is now reportable end to end.
	if _, err := svc.report("coder", "alpha-orphan", "done"); err != nil {
		t.Fatalf("report after retry must close the row: %v", err)
	}
}

// TestLeaderRetryRefusesExecutingRow pins the idempotency gate: a row the
// runtime is actually driving is refused, never double-submitted.
func TestLeaderRetryRefusesExecutingRow(t *testing.T) {
	backend := &taskBackendStub{}
	svc, _ := redriveFixture(t, func(team.MemberBinding) (control.SessionAPI, error) { return backend, nil })
	assignment, err := svc.assignSubtask(context.Background(), "coder", "the real work", "ctx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaderRedriveTool(t, svc, "leader_retry_task").Execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"task_id":%q}`, assignment.TaskID))); err == nil {
		t.Fatal("retry of an executing row must refuse")
	}
	if backend.submits != 1 {
		t.Fatalf("an executing row must never be re-submitted, got %d", backend.submits)
	}
}

// TestLeaderRetryRefusesClosedRow pins the durable routing of the refusal: a
// terminal row and an unknown id are both refused by name with their recorded
// state, never a raw runtime error.
func TestLeaderRetryRefusesClosedRow(t *testing.T) {
	svc, board := redriveFixture(t, nil)
	plantTask(t, board, "alpha-done", team.TaskStatusReported)
	_, err := leaderRedriveTool(t, svc, "leader_retry_task").Execute(context.Background(), json.RawMessage(`{"task_id":"alpha-done"}`))
	if err == nil || !strings.Contains(err.Error(), "already closed (recorded reported)") {
		t.Fatalf("retry of a reported row = %v, want the recorded-state refusal", err)
	}
	if _, err := leaderRedriveTool(t, svc, "leader_retry_task").Execute(context.Background(), json.RawMessage(`{"task_id":"alpha-nope"}`)); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("retry of an unknown id = %v, want an explicit refusal", err)
	}
}

// TestLeaderCancelCancelsUndrivenRow pins the orphan cancel: a durable row
// nothing drives is canceled durably without touching any live backend.
func TestLeaderCancelCancelsUndrivenRow(t *testing.T) {
	svc, board := redriveFixture(t, nil)
	plantTask(t, board, "alpha-orphan", team.TaskStatusRunning)
	out, err := leaderRedriveTool(t, svc, "leader_cancel_task").Execute(context.Background(), json.RawMessage(`{"task_id":"alpha-orphan"}`))
	if err != nil {
		t.Fatalf("cancel of an undriven row must succeed: %v", err)
	}
	if !strings.Contains(out, "canceled") {
		t.Fatalf("cancel output = %q", out)
	}
	task, err := board.LoadTask(context.Background(), "alpha-orphan")
	if err != nil || task.Status != team.TaskStatusCanceled {
		t.Fatalf("orphan after cancel = %+v err=%v, want canceled", task, err)
	}
}

// TestLeaderCancelStopsExecutingRow pins the driven cancel: a row the runtime
// drives cancels through the runtime, stopping the backend's turn.
func TestLeaderCancelStopsExecutingRow(t *testing.T) {
	backend := &taskBackendStub{}
	svc, board := redriveFixture(t, func(team.MemberBinding) (control.SessionAPI, error) { return backend, nil })
	assignment, err := svc.assignSubtask(context.Background(), "coder", "the work", "ctx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaderRedriveTool(t, svc, "leader_cancel_task").Execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"task_id":%q}`, assignment.TaskID))); err != nil {
		t.Fatalf("cancel of a driven row must succeed: %v", err)
	}
	task, err := board.LoadTask(context.Background(), assignment.TaskID)
	if err != nil || task.Status != team.TaskStatusCanceled {
		t.Fatalf("driven row after cancel = %+v err=%v, want canceled", task, err)
	}
	if live, err := board.LoadLiveTasks(context.Background()); err != nil || len(live) != 0 {
		t.Fatalf("live rows after cancel = %v err=%v, want none", live, err)
	}
}

// TestLeaderReassignKeepsTaskIDAndRestartsOnTarget pins the reassign entry: an
// undriven row moves to the named member under the same TaskID, that member
// receives the turn, and the previously recorded member is untouched.
func TestLeaderReassignKeepsTaskIDAndRestartsOnTarget(t *testing.T) {
	backends := map[string]*taskBackendStub{"coder": {}, "tester": {}}
	svc, board := redriveFixture(t, func(b team.MemberBinding) (control.SessionAPI, error) {
		if st, ok := backends[b.MemberID]; ok {
			return st, nil
		}
		return nil, fmt.Errorf("no stub for %s", b.MemberID)
	})
	plantTask(t, board, "alpha-orphan", team.TaskStatusAssigned)

	out, err := leaderRedriveTool(t, svc, "leader_reassign_task").Execute(context.Background(), json.RawMessage(`{"task_id":"alpha-orphan","member_name":"tester"}`))
	if err != nil {
		t.Fatalf("reassign must succeed: %v", err)
	}
	if !strings.Contains(out, "alpha-orphan") || !strings.Contains(out, "tester") {
		t.Fatalf("reassign output = %q, want the same task id on tester", out)
	}
	task, err := board.LoadTask(context.Background(), "alpha-orphan")
	if err != nil || task.Status != team.TaskStatusRunning {
		t.Fatalf("row after reassign = %+v err=%v, want running", task, err)
	}
	if task.AssignedMember != "tester" {
		t.Fatalf("reassign must move the row to tester, got %q", task.AssignedMember)
	}
	if backends["tester"].submits != 1 || backends["coder"].submits != 0 {
		t.Fatalf("reassign must drive only the new member: tester=%d coder=%d", backends["tester"].submits, backends["coder"].submits)
	}
	if _, err := svc.report("tester", "alpha-orphan", "done"); err != nil {
		t.Fatalf("report by the new member must close the row: %v", err)
	}
}

// TestLeaderReassignRefusesExecutingAndWrongMember pins the reassign gates: an
// executing row and a member outside the fleet refuse without touching the
// row.
func TestLeaderReassignRefusesExecutingAndWrongMember(t *testing.T) {
	backend := &taskBackendStub{}
	svc, board := redriveFixture(t, func(team.MemberBinding) (control.SessionAPI, error) { return backend, nil })
	plantTask(t, board, "alpha-orphan", team.TaskStatusAssigned)
	assignment, err := svc.assignSubtask(context.Background(), "coder", "the live work", "ctx")
	if err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string]string{
		"executing gate": fmt.Sprintf(`{"task_id":%q,"member_name":"tester"}`, assignment.TaskID),
		"unknown member": `{"task_id":"alpha-orphan","member_name":"ghost"}`,
		"missing member": `{"task_id":"alpha-orphan","member_name":""}`,
	} {
		if _, err := leaderRedriveTool(t, svc, "leader_reassign_task").Execute(context.Background(), json.RawMessage(args)); err == nil {
			t.Fatalf("%s: reassign must refuse", name)
		}
	}
	// The orphan row survived every refusal untouched.
	task, err := board.LoadTask(context.Background(), "alpha-orphan")
	if err != nil || task.Status != team.TaskStatusAssigned || task.AssignedMember != "coder" {
		t.Fatalf("row after refused reassigns = %+v err=%v, want untouched", task, err)
	}
	if backend.submits != 1 {
		t.Fatalf("an executing row must never be re-submitted by reassign, got %d", backend.submits)
	}
}

// TestReattachResumesLiveRowsOnceAfterRestart pins the host recovery anchor: a
// fresh runtime over a board that still carries a running row re-drives it
// exactly once (a second reattach is a no-op), and the resumed row is
// reportable. A row whose member is in the template but out of the active
// fleet (disabled, archived, leader) settles durably instead.
func TestReattachResumesLiveRowsOnceAfterRestart(t *testing.T) {
	root := t.TempDir()
	teamStore, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := teamStore.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{
		Name: "alpha", Template: []team.MemberSlot{
			{MemberID: "lead", Leader: true, Status: team.MemberStatusActive, Role: team.RoleCoder},
			{MemberID: "coder", Role: team.RoleCoder, Status: team.MemberStatusActive},
			{MemberID: "retired", Role: team.RoleTester, Status: team.MemberStatusDisabled},
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	board, err := team.NewSQLiteStore(context.Background(), filepath.Join(root, "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { board.Close() })

	// First lifetime: assign one task and plant an orphan row on a member the
	// second lifetime's active fleet no longer includes.
	firstBackend := &taskBackendStub{}
	first := newTeamTaskService(teamStore, board, "", func(team.MemberBinding) (control.SessionAPI, error) { return firstBackend, nil }).forTeam("alpha")
	assignment, err := first.assignSubtask(context.Background(), "coder", "interrupted work", "ctx")
	if err != nil {
		t.Fatal(err)
	}
	board.SaveTask(context.Background(), team.Task{ID: "alpha-ghost", RequireRole: team.RoleTester, Desc: "ghost", Status: team.TaskStatusRunning, AssignedMember: "retired", CreatedAt: "2026-01-01T00:00:00Z"})
	firstBackend.submits = 0 // the first lifetime is dead; nothing else may run on it

	// Second lifetime: a brand-new runtime over the same board, the exact shape
	// a host restart builds. Reattach must resume the running row once and
	// settle the disabled member's row durably.
	secondBackend := &taskBackendStub{}
	second := newTeamTaskService(teamStore, board, "", func(team.MemberBinding) (control.SessionAPI, error) { return secondBackend, nil }).forTeam("alpha")
	claimed := map[team.TaskID]bool{}
	note, err := second.reattachLive(context.Background(), claimed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "resumed 1 interrupted task") || !strings.Contains(note, "settled 1") {
		t.Fatalf("reattach note = %q, want one resumed and one settled", note)
	}
	if secondBackend.submits != 1 {
		t.Fatalf("the fresh runtime must re-drive the interrupted row once, got %d submits", secondBackend.submits)
	}
	// The disabled member's row settled through the migration map.
	ghost, err := board.LoadTask(context.Background(), "alpha-ghost")
	if err != nil || ghost.Status != team.TaskStatusFailed {
		t.Fatalf("ghost row = %+v err=%v, want failed (running -> failed)", ghost, err)
	}
	// The resumed row is still durable-live (running) and now driven.
	task, err := board.LoadTask(context.Background(), assignment.TaskID)
	if err != nil || task.Status != team.TaskStatusRunning {
		t.Fatalf("resumed row = %+v err=%v, want running", task, err)
	}
	if !second.driving(assignment.TaskID) {
		t.Fatalf("the fresh runtime must drive the resumed row")
	}

	// A second reattach is a no-op: the runtime already drives the resumed row.
	note2, err := second.reattachLive(context.Background(), claimed)
	if err != nil {
		t.Fatal(err)
	}
	if note2 != "" || secondBackend.submits != 1 {
		t.Fatalf("second reattach = %q submits=%d, want quiet no-op", note2, secondBackend.submits)
	}

	// The resumed task is now driven by the fresh runtime and reportable.
	reported, err := second.report("coder", "", "finished after restart")
	if err != nil {
		t.Fatalf("report after reattach must succeed: %v", err)
	}
	if !strings.Contains(reported, "reported") {
		t.Fatalf("report output = %q", reported)
	}
}

// TestReattachSettlesMemberGoneRows pins the fleet-convergence rule: rows of
// every template member are owned by the team's reattach even when the member
// left the active fleet, so a disabled or archived member's row settles
// instead of being stolen by a sibling team on the same board.
func TestReattachSettlesMemberGoneRows(t *testing.T) {
	svc, board := redriveFixture(t, nil)
	// "gone" was never in this team's template: the row must be left for the
	// team that owns it — reattach must not settle another team's row.
	board.SaveTask(context.Background(), team.Task{ID: "beta-owned", RequireRole: team.RoleTester, Desc: "beta", Status: team.TaskStatusRunning, AssignedMember: "gone", CreatedAt: "2026-01-01T00:00:00Z"})
	claimed := map[team.TaskID]bool{}
	note, err := svc.reattachLive(context.Background(), claimed)
	if err != nil {
		t.Fatal(err)
	}
	if note != "" {
		t.Fatalf("reattach must not claim another team's row, note = %q", note)
	}
	task, err := board.LoadTask(context.Background(), "beta-owned")
	if err != nil || task.Status != team.TaskStatusRunning {
		t.Fatalf("another team's row must stay untouched: %+v err=%v", task, err)
	}
}

// TestReattachAllTeamsRestoresEveryTeamPins reattachAllTeams: rows of several
// teams sharing one board are each resumed through their own team service and
// claimed exactly once.
func TestReattachAllTeamsRestoresEveryTeam(t *testing.T) {
	root := t.TempDir()
	teamStore, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := teamStore.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{
		{Name: "alpha", Template: []team.MemberSlot{{MemberID: "lead", Leader: true, Status: team.MemberStatusActive}, {MemberID: "coder", Role: team.RoleCoder, Status: team.MemberStatusActive}}},
		{Name: "beta", Template: []team.MemberSlot{{MemberID: "blead", Leader: true, Status: team.MemberStatusActive}, {MemberID: "tester", Role: team.RoleTester, Status: team.MemberStatusActive}}},
	}}); err != nil {
		t.Fatal(err)
	}
	board, err := team.NewSQLiteStore(context.Background(), filepath.Join(root, "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { board.Close() })
	board.SaveTask(context.Background(), team.Task{ID: "alpha-a", RequireRole: team.RoleCoder, Desc: "a", Status: team.TaskStatusRunning, AssignedMember: "coder", CreatedAt: "2026-01-01T00:00:00Z"})
	board.SaveTask(context.Background(), team.Task{ID: "beta-b", RequireRole: team.RoleTester, Desc: "b", Status: team.TaskStatusRunning, AssignedMember: "tester", CreatedAt: "2026-01-01T00:00:00Z"})
	submits := map[string]*taskBackendStub{"coder": {}, "tester": {}}
	rootSvc := newTeamTaskService(teamStore, board, "", func(b team.MemberBinding) (control.SessionAPI, error) {
		if st, ok := submits[b.MemberID]; ok {
			return st, nil
		}
		return nil, fmt.Errorf("no stub for %s", b.MemberID)
	})
	note, err := rootSvc.reattachAllTeams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "alpha-a on coder") || !strings.Contains(note, "beta-b on tester") {
		t.Fatalf("reattachAllTeams note = %q, want both teams' rows resumed", note)
	}
	if submits["coder"].submits != 1 || submits["tester"].submits != 1 {
		t.Fatalf("both members must be re-driven once: %d/%d", submits["coder"].submits, submits["tester"].submits)
	}
}

// TestLeaderRedriveToolsAreWritable pins the tool contract: the three recovery
// tools mutate state, so they are never read-only or plan-mode safe.
func TestLeaderRedriveToolsAreWritable(t *testing.T) {
	svc, _ := redriveFixture(t, nil)
	for _, name := range []string{"leader_retry_task", "leader_cancel_task", "leader_reassign_task"} {
		tool := leaderRedriveTool(t, svc, name)
		if tool.ReadOnly() || tool.PlanModeSafe() {
			t.Fatalf("%s must be a writable, never read-only", name)
		}
	}
}

// TestReassignRunningRowSettlesThroughMigrationMap pins the legal-edge route:
// an undriven running row reassigns through running -> failed -> assigned
// before restarting on the new member — no path invents an illegal transition.
func TestReassignRunningRowSettlesThroughMigrationMap(t *testing.T) {
	backends := map[string]*taskBackendStub{"coder": {}, "tester": {}}
	svc, board := redriveFixture(t, func(b team.MemberBinding) (control.SessionAPI, error) {
		if st, ok := backends[b.MemberID]; ok {
			return st, nil
		}
		return nil, fmt.Errorf("no stub for %s", b.MemberID)
	})
	plantTask(t, board, "alpha-zombie", team.TaskStatusRunning)
	if _, err := leaderRedriveTool(t, svc, "leader_reassign_task").Execute(context.Background(), json.RawMessage(`{"task_id":"alpha-zombie","member_name":"tester"}`)); err != nil {
		t.Fatalf("reassign of an undriven running row must succeed: %v", err)
	}
	task, err := board.LoadTask(context.Background(), "alpha-zombie")
	if err != nil || task.Status != team.TaskStatusRunning || task.AssignedMember != "tester" {
		t.Fatalf("zombie after reassign = %+v err=%v, want running on tester", task, err)
	}
	if backends["tester"].submits != 1 {
		t.Fatalf("tester must receive the turn, got %d", backends["tester"].submits)
	}
}

// TestRetryFailedRowTakesReassignEdge pins the failed-row route: a row that
// failed durably (running -> failed via a dead restore) is retryable through
// the one legal edge failed -> assigned, keeping its TaskID.
func TestRetryFailedRowTakesReassignEdge(t *testing.T) {
	backend := &taskBackendStub{}
	svc, board := redriveFixture(t, func(team.MemberBinding) (control.SessionAPI, error) { return backend, nil })
	plantTask(t, board, "alpha-failed", team.TaskStatusFailed)
	out, err := leaderRedriveTool(t, svc, "leader_retry_task").Execute(context.Background(), json.RawMessage(`{"task_id":"alpha-failed"}`))
	if err != nil {
		t.Fatalf("retry of a failed row must succeed: %v", err)
	}
	if !strings.Contains(out, "alpha-failed") {
		t.Fatalf("retry output = %q", out)
	}
	task, err := board.LoadTask(context.Background(), "alpha-failed")
	if err != nil || task.Status != team.TaskStatusRunning {
		t.Fatalf("failed row after retry = %+v err=%v, want running", task, err)
	}
}

// TestReattachIgnoresRowsAlreadyDriven pins the reattach once-guard at the
// service level: rows the current runtime is already driving are skipped, so a
// reattach call racing or repeating after a dispatch never double-submits.
func TestReattachIgnoresRowsAlreadyDriven(t *testing.T) {
	backend := &taskBackendStub{}
	svc, _ := redriveFixture(t, func(team.MemberBinding) (control.SessionAPI, error) { return backend, nil })
	assignment, err := svc.assignSubtask(context.Background(), "coder", "live work", "ctx")
	if err != nil {
		t.Fatal(err)
	}
	claimed := map[team.TaskID]bool{}
	note, err := svc.reattachLive(context.Background(), claimed)
	if err != nil {
		t.Fatal(err)
	}
	if note != "" || claimed[assignment.TaskID] {
		t.Fatalf("reattach must skip a driven row: note=%q claimed=%v", note, claimed[assignment.TaskID])
	}
	if backend.submits != 1 {
		t.Fatalf("a driven row must never be re-submitted, got %d", backend.submits)
	}
}

// TestRedriveUsesRuntimeAcrossTeams pins the shared-runtime contract for the
// recovery tools: the per-team services a leader and its members resolve share
// one runtime, so a retry's drive is reportable through the member's own
// service.
func TestRedriveUsesRuntimeAcrossTeams(t *testing.T) {
	backend := &taskBackendStub{}
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
	t.Cleanup(func() { board.Close() })
	rootSvc := newTeamTaskService(teamStore, board, "", func(team.MemberBinding) (control.SessionAPI, error) { return backend, nil })
	leaderSvc := rootSvc.forTeam("alpha")
	memberSvc := rootSvc.forTeam("alpha")
	if leaderSvc != memberSvc {
		t.Fatalf("leader and member must share the per-team service")
	}
	plantTask(t, board, "alpha-orphan", team.TaskStatusAssigned)
	if _, err := leaderRedriveTool(t, leaderSvc, "leader_retry_task").Execute(context.Background(), json.RawMessage(`{"task_id":"alpha-orphan"}`)); err != nil {
		t.Fatalf("retry through the shared runtime must succeed: %v", err)
	}
	if _, err := memberSvc.report("coder", "", "closed after shared-runtime retry"); err != nil {
		t.Fatalf("a report through the member service must close the re-driven row: %v", err)
	}
}

var _ = teamscheduler.Assignment{} // keep the scheduler import referenced in helpers
