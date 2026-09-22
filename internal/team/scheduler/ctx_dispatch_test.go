package scheduler

// Acceptance tests for the B2 fix in TEAM_MEMBER_PARALLELISM_ROUTE.md: the
// dispatch path used to hand its executor context.Background(), so the durable
// write and turn submission it gates could not be abandoned by the caller.

import (
	"context"
	"errors"
	"testing"

	"reasonix/internal/team"
)

// ctxRecordingExecutor captures the context it is driven with and returns
// immediately, so the assertion is about which context reached the executor
// rather than how long anything took.
type ctxRecordingExecutor struct {
	start  context.Context
	resume context.Context
}

func (e *ctxRecordingExecutor) Start(ctx context.Context, _ team.Task, _ team.Member) error {
	e.start = ctx
	return nil
}

func (e *ctxRecordingExecutor) Cancel(team.TaskID) error { return nil }

func (e *ctxRecordingExecutor) Resume(ctx context.Context, _ team.Task, _ team.Member) error {
	e.resume = ctx
	return nil
}

// TestRuntimeSchedulerAssignKeepsTheCallersContext pins the dispatch half: the
// context the caller cancels is the one the executor — and so the durable write
// it makes — runs under, never a fresh uncancellable background context.
func TestRuntimeSchedulerAssignKeepsTheCallersContext(t *testing.T) {
	exec := &ctxRecordingExecutor{}
	s := NewRuntimeScheduler(exec)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := s.Assign(ctx, team.Task{ID: "t1", RequireRole: team.RoleCoder}, []team.Member{idleMember("m1", team.RoleCoder)}); err != nil {
		t.Fatal(err)
	}
	if exec.start == nil {
		t.Fatal("the executor was driven with no context")
	}
	if exec.start == context.Background() {
		t.Fatal("the executor was driven with context.Background(): the caller's context was replaced")
	}
	cancel()
	if !errors.Is(exec.start.Err(), context.Canceled) {
		t.Fatalf("cancelling the caller left the dispatch context alive: err = %v", exec.start.Err())
	}
}

// TestRuntimeSchedulerRestoreKeepsTheCallersContext is the recovery half: a
// resumed task runs under the context the recovering caller supplied, so a
// startup or leader-triggered restore is abandonable too.
func TestRuntimeSchedulerRestoreKeepsTheCallersContext(t *testing.T) {
	exec := &ctxRecordingExecutor{}
	s := NewRuntimeScheduler(exec)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tasks := []team.Task{{ID: "t1", Status: team.TaskStatusRunning, AssignedMember: "m1"}}
	restored, err := s.Restore(ctx, tasks, []team.Member{idleMember("m1", team.RoleCoder)})
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || restored[0].Status != StatusRunning {
		t.Fatalf("restored = %+v, want one running resume", restored)
	}
	if exec.resume == nil || exec.resume == context.Background() {
		t.Fatal("the executor was driven with a replacement context, want the caller's")
	}
	cancel()
	if !errors.Is(exec.resume.Err(), context.Canceled) {
		t.Fatalf("cancelling the caller left the restore context alive: err = %v", exec.resume.Err())
	}
}

// TestRuntimeSchedulerAssignToleratesANilContext pins the nil contract: a host
// with no context to give gets an uncancellable dispatch, never a panic from the
// executor's durable writes.
func TestRuntimeSchedulerAssignToleratesANilContext(t *testing.T) {
	exec := &ctxRecordingExecutor{}
	s := NewRuntimeScheduler(exec)
	if _, err := s.Assign(nil, team.Task{ID: "t1", RequireRole: team.RoleCoder}, []team.Member{idleMember("m1", team.RoleCoder)}); err != nil { //nolint:staticcheck // the nil-context tolerance is the subject of this test
		t.Fatal(err)
	}
	if exec.start == nil || exec.start.Err() != nil {
		t.Fatalf("nil caller context = %v, want an uncancellable context", exec.start)
	}
}
