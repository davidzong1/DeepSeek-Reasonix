package cli

// Regression suite for the member_report_result "unknown task" repair: a
// report is accepted only for a task the runtime actually drives; undriven
// durable rows get an actionable refusal, never ErrTaskUnknown raw.

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/team"
	"reasonix/internal/team/agentruntime"
)

// reportRegressionFixture roots a two-member team on a real SQLite board and
// returns the per-team service bound to a stub backend. nil bind keeps the
// service drive-free for tests that only plant durable rows.
func reportRegressionFixture(t *testing.T, bind func(team.MemberBinding) (control.SessionAPI, error)) (*teamTaskService, *team.SQLiteStore) {
	t.Helper()
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
	return newTeamTaskService(teamStore, board, "", bind).forTeam("alpha"), board
}

// plantTask saves one durable live row straight to the board, the exact shape
// a refused dispatch or a dead runtime leaves behind.
func plantTask(t *testing.T, board *team.SQLiteStore, id string, status team.TaskStatus) {
	t.Helper()
	if err := board.SaveTask(context.Background(), team.Task{
		ID: team.TaskID(id), RequireRole: team.RoleCoder, Desc: "planted " + id,
		Status: status, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		AssignedMember: "coder",
	}); err != nil {
		t.Fatal(err)
	}
}

func reportTool(t *testing.T, svc *teamTaskService) *teamTaskTool {
	t.Helper()
	for _, candidate := range newMemberTaskTools(svc, "alpha", "coder") {
		if candidate.Name() == "member_report_result" {
			return candidate.(*teamTaskTool)
		}
	}
	t.Fatal("member_report_result tool is missing")
	return nil
}

// TestReportLegacyResultOnlyFormatStaysSupported pins the backward-compatible
// default: the pre-operation callers that send only {"result": ...} — no
// operation, no task_id — still close the driven task through the runtime.
func TestReportLegacyResultOnlyFormatStaysSupported(t *testing.T) {
	backend := &taskBackendStub{}
	svc, board := reportRegressionFixture(t, func(team.MemberBinding) (control.SessionAPI, error) { return backend, nil })
	assignment, err := svc.assignSubtask(context.Background(), "coder", "implement the change", "ctx")
	if err != nil {
		t.Fatal(err)
	}
	out, err := reportTool(t, svc).Execute(context.Background(), json.RawMessage(`{"result":"implemented and tested"}`))
	if err != nil {
		t.Fatalf("legacy result-only report must succeed: %v", err)
	}
	if !strings.Contains(out, string(assignment.TaskID)) {
		t.Fatalf("report output = %q, want the closed task id", out)
	}
	task, err := board.LoadTask(context.Background(), assignment.TaskID)
	if err != nil || task.Status != team.TaskStatusReported {
		t.Fatalf("legacy report must route through runtime.Complete: task=%+v err=%v", task, err)
	}
}

// TestReportToolRefusesUndrivenOwnedRow pins the direct-tool surface: with one
// durable assigned row nobody drives, the default operation and an explicit
// task_id both fail with the actionable undriven refusal — never a raw
// "agentruntime: unknown task" — while an id the member does not own stays an
// explicit refusal.
func TestReportToolRefusesUndrivenOwnedRow(t *testing.T) {
	svc, board := reportRegressionFixture(t, nil)
	plantTask(t, board, "alpha-zombie", team.TaskStatusAssigned)
	tool := reportTool(t, svc)

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"result":"done anyway"}`)); err == nil {
		t.Fatal("default-operation report over an undriven row must fail")
	} else {
		assertUndrivenReportError(t, err)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"task_id":"alpha-zombie","result":"done anyway"}`)); err == nil {
		t.Fatal("an explicit undriven task_id must fail")
	} else {
		assertUndrivenReportError(t, err)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"task_id":"no-such-task","result":"x"}`)); err == nil || !strings.Contains(err.Error(), "not an unfinished task") {
		t.Fatalf("an unknown task_id must stay an explicit refusal, got %v", err)
	}
}

// TestReportUndrivenMultiOwnedRefusedListsEveryRow pins the default-operation
// shape with several owned rows and nothing executing: the refusal lists every
// row with its durable status instead of guessing, and an explicit id is not a
// way around the gate.
func TestReportUndrivenMultiOwnedRefusedListsEveryRow(t *testing.T) {
	svc, board := reportRegressionFixture(t, nil)
	plantTask(t, board, "alpha-a", team.TaskStatusAssigned)
	plantTask(t, board, "alpha-b", team.TaskStatusRunning)

	_, err := svc.report("coder", "", "done")
	if err == nil {
		t.Fatal("report over two undriven rows must fail, got nil")
	}
	assertUndrivenReportError(t, err)
	for _, id := range []string{"alpha-a", "alpha-b"} {
		if !strings.Contains(err.Error(), id) {
			t.Fatalf("the refusal must name every owned row (%s): %v", id, err)
		}
	}
	// The explicit id is honoured only while the runtime drives it.
	if _, err := svc.report("coder", "alpha-b", "done"); err == nil {
		t.Fatal("an explicit undriven task_id must not close the row")
	} else {
		assertUndrivenReportError(t, err)
	}
}

// TestReportDrivenTaskWinsOverUndrivenRow pins the D10 shape under the new
// gate: a driven task plus an undispatched row still reports the driven one —
// the gate refuses undriven rows, it does not freeze a member whose real task
// is executing.
func TestReportDrivenTaskWinsOverUndrivenRow(t *testing.T) {
	backend := &taskBackendStub{}
	svc, board := reportRegressionFixture(t, func(team.MemberBinding) (control.SessionAPI, error) { return backend, nil })
	plantTask(t, board, "alpha-queued", team.TaskStatusAssigned)
	assignment, err := svc.assignSubtask(context.Background(), "coder", "the real work", "ctx")
	if err != nil {
		t.Fatal(err)
	}
	out, err := svc.report("coder", "", "built green")
	if err != nil {
		t.Fatalf("the driven task must disambiguate the report: %v", err)
	}
	if !strings.Contains(out, string(assignment.TaskID)) {
		t.Fatalf("report closed %q, want the driven %s", out, assignment.TaskID)
	}
	// The queued row is untouched and still visible to the leader.
	live, err := board.LoadLiveTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].ID != team.TaskID("alpha-queued") {
		t.Fatalf("the undriven row must survive the report, live = %+v", live)
	}
}

// TestReportSecondCloseIsNoUnfinished pins the service-side double report: once
// the first report migrated the row to reported, a replaying caller gets the
// friendly no-unfinished-task answer — the runtime's own double-report refusal
// stays pinned at the agentruntime layer.
func TestReportSecondCloseIsNoUnfinished(t *testing.T) {
	backend := &taskBackendStub{}
	svc, _ := reportRegressionFixture(t, func(team.MemberBinding) (control.SessionAPI, error) { return backend, nil })
	assignment, err := svc.assignSubtask(context.Background(), "coder", "the work", "ctx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.report("coder", "", "done"); err != nil {
		t.Fatal(err)
	}
	out, err := svc.report("coder", "", "done again")
	if err != nil {
		t.Fatalf("a second close must be a friendly no-op, not an error: %v", err)
	}
	if !strings.Contains(out, "no unfinished task") || strings.Contains(out, string(assignment.TaskID)) {
		t.Fatalf("second close = %q, want the no-unfinished-task answer", out)
	}
}

// TestReportConcurrentDoubleCloseConverges pins the TOCTOU window between
// pickReportTarget's driving check and claimTerminal: two reports racing over
// the same driven task converge on one reported row. At least one must win —
// the reported-to-reported idempotency makes a double success legal — and no
// return value may leak the raw runtime refusal.
func TestReportConcurrentDoubleCloseConverges(t *testing.T) {
	backend := &taskBackendStub{}
	svc, board := reportRegressionFixture(t, func(team.MemberBinding) (control.SessionAPI, error) { return backend, nil })
	assignment, err := svc.assignSubtask(context.Background(), "coder", "concurrent work", "ctx")
	if err != nil {
		t.Fatal(err)
	}
	results := make([]string, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = svc.report("coder", "", "done")
		}(i)
	}
	wg.Wait()
	successes := 0
	for i := range 2 {
		if errs[i] != nil {
			text := errs[i].Error()
			if strings.Contains(text, "agentruntime: unknown task") {
				t.Fatalf("a concurrent report must never leak the raw runtime refusal: %v", errs[i])
			}
			if !strings.Contains(text, "already closed") && !strings.Contains(text, "executing") && !strings.Contains(text, "record could not be re-read") {
				t.Fatalf("report %d = %v, want an actionable already-closed or undriven answer", i, errs[i])
			}
			continue
		}
		if strings.Contains(results[i], "reported to leader") {
			successes++
		}
	}
	// A double success is legal: the runner-up may land after the winner's
	// reported-to-reported idempotent migration and close the same row again.
	if successes < 1 {
		t.Fatalf("at least one concurrent report must win, won %d (outs=%q errs=%v)", successes, results, errs)
	}
	task, err := board.LoadTask(context.Background(), assignment.TaskID)
	if err != nil || task.Status != team.TaskStatusReported {
		t.Fatalf("board must converge on one reported row: %+v err=%v", task, err)
	}
	live, err := board.LoadLiveTasks(context.Background())
	if err != nil || len(live) != 0 {
		t.Fatalf("no live rows may survive the concurrent close: %v err=%v", live, err)
	}
}

// TestTranslateCompleteErrorFollowsDurableRow pins the post-gate translation
// with an injected ErrTaskUnknown: the durable row alone decides the member's
// answer — already-closed for a terminal or vanished row, the undriven refusal
// for a live row nothing drives — and any other Complete error passes through
// untouched.
func TestTranslateCompleteErrorFollowsDurableRow(t *testing.T) {
	svc, board := reportRegressionFixture(t, nil)
	target := &team.Task{ID: team.TaskID("alpha-close"), AssignedMember: "coder"}
	assert := func(name, fragment string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: translation must not return nil", name)
		}
		text := err.Error()
		if strings.Contains(text, "agentruntime: unknown task") {
			t.Fatalf("%s: must never leak the raw runtime refusal, got: %s", name, text)
		}
		if !strings.Contains(text, fragment) {
			t.Fatalf("%s: got %q, want %q", name, text, fragment)
		}
	}
	plantTask(t, board, "alpha-close", team.TaskStatusReported)
	assert("row already reported", "already closed (recorded reported)", svc.translateCompleteError(target, agentruntime.ErrTaskUnknown))
	plantTask(t, board, "alpha-close", team.TaskStatusCanceled)
	assert("row already canceled", "already closed (recorded canceled)", svc.translateCompleteError(target, agentruntime.ErrTaskUnknown))
	plantTask(t, board, "alpha-close", team.TaskStatusAssigned)
	assert("undriven assigned row", "not executing (recorded assigned", svc.translateCompleteError(target, agentruntime.ErrTaskUnknown))
	plantTask(t, board, "alpha-close", team.TaskStatusRunning)
	assert("undriven running row", "not executing (recorded running", svc.translateCompleteError(target, agentruntime.ErrTaskUnknown))
	assert("row gone from the board", "record is gone", svc.translateCompleteError(&team.Task{ID: team.TaskID("alpha-absent")}, agentruntime.ErrTaskUnknown))
	boom := errors.New("store: disk full")
	if err := svc.translateCompleteError(target, boom); !errors.Is(err, boom) {
		t.Fatalf("a non-unknown Complete error must pass through, got %v", err)
	}
}
