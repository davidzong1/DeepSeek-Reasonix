package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"reasonix/internal/team"
)

// stubExecutor records executor calls for the runtime-scheduler tests.
type stubExecutor struct {
	started  []string
	canceled []string
	resumed  []string
	startErr error
}

func (s *stubExecutor) Start(_ context.Context, task team.Task, _ team.Member) error {
	if s.startErr != nil {
		return s.startErr
	}
	s.started = append(s.started, string(task.ID))
	return nil
}

func (s *stubExecutor) Cancel(taskID team.TaskID) error {
	s.canceled = append(s.canceled, string(taskID))
	return nil
}

func (s *stubExecutor) Resume(_ context.Context, task team.Task, _ team.Member) error {
	s.resumed = append(s.resumed, string(task.ID))
	return nil
}

// TestRuntimeSchedulerAssignStarts pins the live scheduler contract: the
// assignment really starts the task — status running, executor called —
// and never claims a fake runtime-pending.
func TestRuntimeSchedulerAssignStarts(t *testing.T) {
	exec := &stubExecutor{}
	s := NewRuntimeScheduler(exec)
	a, err := s.Assign(team.Task{ID: "t1", RequireRole: team.RoleCoder}, []team.Member{idleMember("m1", team.RoleCoder)})
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != StatusRunning {
		t.Fatalf("status = %s, want running", a.Status)
	}
	if len(exec.started) != 1 || exec.started[0] != "t1" {
		t.Fatalf("executor started = %v, want [t1]", exec.started)
	}
}

func TestRuntimeSchedulerAssignNoExecutor(t *testing.T) {
	s := NewRuntimeScheduler(nil)
	_, err := s.Assign(team.Task{ID: "t1"}, []team.Member{idleMember("m1", team.RoleCoder)})
	if !errors.Is(err, ErrNoExecutor) {
		t.Fatalf("err = %v, want ErrNoExecutor", err)
	}
}

func TestRuntimeSchedulerAssignNoMember(t *testing.T) {
	s := NewRuntimeScheduler(&stubExecutor{})
	_, err := s.Assign(team.Task{ID: "t1", RequireRole: team.RoleTester}, []team.Member{idleMember("m1", team.RoleCoder)})
	if !errors.Is(err, ErrNoSuitableMember) {
		t.Fatalf("err = %v, want ErrNoSuitableMember", err)
	}
}

// TestRuntimeSchedulerStartFailure: a refused start must surface, not record
// a ledger entry that pretends execution.
func TestRuntimeSchedulerStartFailure(t *testing.T) {
	s := NewRuntimeScheduler(&stubExecutor{startErr: errors.New("agent down")})
	_, err := s.Assign(team.Task{ID: "t1"}, []team.Member{idleMember("m1", team.RoleCoder)})
	if !errors.Is(err, ErrStartFailed) {
		t.Fatalf("err = %v, want ErrStartFailed", err)
	}
}

func TestRuntimeSchedulerCancel(t *testing.T) {
	exec := &stubExecutor{}
	s := NewRuntimeScheduler(exec)
	a, err := s.Cancel("t1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != StatusCanceled || len(exec.canceled) != 1 {
		t.Fatalf("cancel = %+v, executor %v", a, exec.canceled)
	}
}

// TestRuntimeSchedulerRestoreResumes: a persisted running task is re-driven
// on its member after a restart, never left in a fake pending state.
func TestRuntimeSchedulerRestoreResumes(t *testing.T) {
	exec := &stubExecutor{}
	s := NewRuntimeScheduler(exec)
	tasks := []team.Task{{ID: "t1", Status: team.TaskStatusRunning, AssignedMember: "m1"}}
	restored, err := s.Restore(tasks, []team.Member{idleMember("m1", team.RoleCoder)})
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || restored[0].Status != StatusRunning || restored[0].MemberID != "m1" {
		t.Fatalf("restored = %+v, want one running on m1", restored)
	}
	if len(exec.resumed) != 1 {
		t.Fatalf("executor resumed = %v, want [t1]", exec.resumed)
	}
}

// TestRuntimeSchedulerRestoreMemberGone: a task whose member left the fleet
// is marked failed at restore, not silently forgotten.
func TestRuntimeSchedulerRestoreMemberGone(t *testing.T) {
	exec := &stubExecutor{}
	s := NewRuntimeScheduler(exec)
	tasks := []team.Task{{ID: "t1", Status: team.TaskStatusRunning, AssignedMember: "gone"}}
	restored, err := s.Restore(tasks, []team.Member{idleMember("m1", team.RoleCoder)})
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || restored[0].Status != StatusFailed {
		t.Fatalf("restored = %+v, want one failed", restored)
	}
	if len(exec.resumed) != 0 {
		t.Fatalf("executor must not resume a vanished member, got %v", exec.resumed)
	}
}

// TestRuntimeSchedulerRestoreMemberGonePersists: with a durable store wired
// in, a failed restore is written through the migration map — running fails,
// so the store no longer re-drives the task after another kill/reopen.
func TestRuntimeSchedulerRestoreMemberGonePersists(t *testing.T) {
	exec := &stubExecutor{}
	s := NewRuntimeScheduler(exec)
	store, err := team.NewSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetTaskStore(store)
	if err := store.SaveTask(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusRunning, AssignedMember: "gone"}); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.LoadLiveTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Restore(tasks, []team.Member{idleMember("m1", team.RoleCoder)}); err != nil {
		t.Fatal(err)
	}
	saved, err := store.LoadTask(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != team.TaskStatusFailed {
		t.Fatalf("saved status = %s, want failed", saved.Status)
	}
	live, err := store.LoadLiveTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Fatalf("live = %+v, want none after the failed restore", live)
	}
}

// TestRuntimeSchedulerRestoreAssignedMemberGoneCancels: the migration map has
// no assigned -> failed edge, so an assigned task whose member vanished
// cancels instead of failing.
func TestRuntimeSchedulerRestoreAssignedMemberGoneCancels(t *testing.T) {
	exec := &stubExecutor{}
	s := NewRuntimeScheduler(exec)
	store, err := team.NewSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetTaskStore(store)
	tasks := []team.Task{{ID: "t1", Status: team.TaskStatusAssigned, AssignedMember: "gone"}}
	restored, err := s.Restore(tasks, []team.Member{idleMember("m1", team.RoleCoder)})
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || restored[0].Status != StatusFailed {
		t.Fatalf("restored = %+v, want one failed assignment", restored)
	}
	saved, err := store.LoadTask(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != team.TaskStatusCanceled {
		t.Fatalf("saved status = %s, want canceled (no assigned->failed edge)", saved.Status)
	}
}
