package cli

// First-dispatch P0 regression — a member backend whose persisted session
// starts without write authority, or that already runs a task, must make
// leader_assign_subtask surface an exact refusal, never a ghost running.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/team"
	teamscheduler "reasonix/internal/team/scheduler"
)

// recorderTurnRunner is a real turn runner that records every model input so a
// member controller can participate in a turn without a provider. The turn
// body runs on the controller's own admission goroutine, so waitForTurns
// synchronizes on the recorded input before an assertion.
type recorderTurnRunner struct {
	mu     sync.Mutex
	inputs []string
	called chan struct{}
}

func newRecorderTurnRunner() *recorderTurnRunner {
	return &recorderTurnRunner{called: make(chan struct{}, 8)}
}

func (r *recorderTurnRunner) Run(_ context.Context, input string) error {
	r.mu.Lock()
	r.inputs = append(r.inputs, input)
	r.mu.Unlock()
	select {
	case r.called <- struct{}{}:
	default:
	}
	return nil
}

func (r *recorderTurnRunner) turns() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.inputs)
}

// waitForTurns blocks until the runner saw want submissions or the deadline
// passes, so an assertion never races the admission goroutine's execution.
func waitForTurns(t *testing.T, r *recorderTurnRunner, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for r.turns() < want {
		if time.Now().After(deadline) {
			t.Fatalf("runner saw %d turns, want %d", r.turns(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// newMemberController is the healthy member backend shape: a controller
// pointed at the member's session file with a lease-backed write authority
// bound — exactly the binding the ambient CLI path performs, so turns admit
// with turnStarted.
func newMemberController(t *testing.T, path string, rec *recorderTurnRunner) *control.Controller {
	t.Helper()
	sess := agent.NewSession("sys")
	exec := agent.New(nil, nil, sess, agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{
		Executor: exec, SystemPrompt: "sys", SessionPath: path,
		SessionDir: filepath.Dir(path), Sink: event.Discard, Runner: rec,
	})
	lease, err := agent.TryAcquireSessionLease(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lease.Release)
	if err := control.IssueAndBindWriteAuthority(ctrl, lease); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctrl.Close)
	return ctrl
}

// newDanglingMemberController is the pre-fix member backend: a persisted
// session that require production fail-closed admission (RequireWriteAuthority)
// but never received a bound authority — every turn is refused at admission 6
// (turnDroppedWriteAuthority).
func newDanglingMemberController(t *testing.T, path string) *control.Controller {
	t.Helper()
	sess := agent.NewSession("sys")
	exec := agent.New(nil, nil, sess, agent.Options{}, event.Discard)
	sess.RequireWriteAuthority()
	ctrl := control.New(control.Options{
		Executor: exec, SystemPrompt: "sys", SessionPath: path,
		SessionDir: filepath.Dir(path), Sink: event.Discard, Runner: newRecorderTurnRunner(),
	})
	t.Cleanup(ctrl.Close)
	return ctrl
}

// newFirstDispatchService wires team alpha (lead + coder) over a real board;
// the coder member resolves to the controller built by b, so each test owns
// its member's authority and busyness without touching production files.
func newFirstDispatchService(t *testing.T, b func(team.MemberBinding) control.SessionAPI) (*teamTaskService, *team.SQLiteStore) {
	t.Helper()
	root := t.TempDir()
	teamStore, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := teamStore.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{
		Name: "alpha", Template: []team.MemberSlot{
			{MemberID: "lead", Leader: true, Role: team.RoleCoder, Status: team.MemberStatusActive},
			{MemberID: "coder", Role: team.RoleCoder, Status: team.MemberStatusActive},
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	board, err := team.NewSQLiteStore(context.Background(), filepath.Join(root, "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	service := newTeamTaskService(teamStore, board, "", func(bind team.MemberBinding) (control.SessionAPI, error) {
		if bind.MemberID == "lead" {
			return &taskBackendStub{}, nil
		}
		return b(bind), nil
	})
	return service.forTeam("alpha"), board
}

// TestTeamTaskServiceCoderFirstAssignPersistentSessionMissingAuthority pins
// the acceptance gap: a persisted member session that demands write authority
// but has none refuses even the FIRST dispatch, surfaced as an admission-6
// diagnostic, and the durable task stays assigned — never a ghost running.
func TestTeamTaskServiceCoderFirstAssignPersistentSessionMissingAuthority(t *testing.T) {
	svc, board := newFirstDispatchService(t, func(bind team.MemberBinding) control.SessionAPI {
		return newDanglingMemberController(t, filepath.Join(t.TempDir(), "coder-session.jsonl"))
	})
	_, err := svc.assignSubtask(context.Background(), "coder", "build the widget", "ctx")
	if err == nil {
		t.Fatal("a member session without write authority must refuse the first dispatch")
	}
	if !errors.Is(err, teamscheduler.ErrStartFailed) {
		t.Fatalf("err = %v, want ErrStartFailed", err)
	}
	if !strings.Contains(err.Error(), "did not accept the turn") || !strings.HasSuffix(err.Error(), "admission 6)") {
		t.Fatalf("err = %q, want the admission-6 refusal marker for diagnosis", err)
	}
	tasks, err := board.LoadLiveTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != team.TaskStatusAssigned {
		t.Fatalf("live tasks = %+v, want exactly one assigned task, never a ghost running", tasks)
	}
}

// TestTeamTaskServiceCoderFirstAssignBusyMemberExplicitReject pins the busy
// contract at the real chain: a second dispatch onto a member whose first
// task is still live is refused with the explicit member-already-running
// marker, the second task rolls back to assigned, and the first task's backend
// was driven exactly once.
func TestTeamTaskServiceCoderFirstAssignBusyMemberExplicitReject(t *testing.T) {
	rec := newRecorderTurnRunner()
	svc, board := newFirstDispatchService(t, func(bind team.MemberBinding) control.SessionAPI {
		return newMemberController(t, filepath.Join(t.TempDir(), "coder-session.jsonl"), rec)
	})
	first, err := svc.assignSubtask(context.Background(), "coder", "first task", "")
	if err != nil {
		t.Fatalf("a writable member must accept its first dispatch: %v", err)
	}
	if first.MemberID != "coder" || first.Status != teamscheduler.StatusRunning {
		t.Fatalf("first dispatch = %+v, want coder running", first)
	}
	waitForTurns(t, rec, 1) // the first turn is in flight before the busy probe
	if _, err := svc.assignSubtask(context.Background(), "coder", "second task", ""); err == nil {
		t.Fatal("a busy member must reject the second dispatch, not succeed silently")
	} else {
		if !errors.Is(err, teamscheduler.ErrStartFailed) || !strings.Contains(err.Error(), "member already running") {
			t.Fatalf("err = %v, want the explicit ErrMemberBusy refusal", err)
		}
	}
	if turns := rec.turns(); turns != 1 {
		t.Fatalf("coder's runner saw %d turns, want 1 (busy must never double-drive)", turns)
	}
	tasks, err := board.LoadLiveTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("live tasks = %+v, want the running first and the assigned second", tasks)
	}
	state := map[team.TaskStatus]string{}
	for _, task := range tasks {
		if prev, ok := state[task.Status]; ok {
			t.Fatalf("two tasks share status %s: %q and %q", task.Status, prev, task.Desc)
		}
		state[task.Status] = task.Desc
	}
	if state[team.TaskStatusRunning] != "first task" || state[team.TaskStatusAssigned] != "second task" {
		t.Fatalf("state = %+v, want the first running and the second assigned (rollback)", state)
	}
}

// TestTeamTaskServiceLeaderFirstAssignCleanSessionSucceeds pins the happy
// first dispatch: a writable member's first task starts, its backend receives
// the injected subtask exactly once, and the durable task lands running.
func TestTeamTaskServiceLeaderFirstAssignCleanSessionSucceeds(t *testing.T) {
	rec := newRecorderTurnRunner()
	svc, board := newFirstDispatchService(t, func(bind team.MemberBinding) control.SessionAPI {
		return newMemberController(t, filepath.Join(t.TempDir(), "coder-session.jsonl"), rec)
	})
	assignment, err := svc.assignSubtask(context.Background(), "coder", "implement the change", "team ctx")
	if err != nil {
		t.Fatalf("first dispatch to a writable member must succeed: %v", err)
	}
	if assignment.MemberID != "coder" || assignment.Status != teamscheduler.StatusRunning {
		t.Fatalf("assignment = %+v, want coder running", assignment)
	}
	waitForTurns(t, rec, 1)
	var got string
	rec.mu.Lock()
	got = rec.inputs[0]
	rec.mu.Unlock()
	if !strings.Contains(got, "implement the change") {
		t.Fatalf("runner input = %q, want the injected subtask submitted exactly once", got)
	}
	task, err := board.LoadTask(context.Background(), assignment.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != team.TaskStatusRunning || task.AssignedMember != "coder" {
		t.Fatalf("persisted task = %+v, want coder running", task)
	}
}
