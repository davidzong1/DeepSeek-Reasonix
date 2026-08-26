package boot

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"reasonix/internal/team"
	"reasonix/internal/team/scheduler"
)

// stubTaskStore serves a fixed live set and records LoadLiveTasks calls; it
// keeps the recovery-loop tests off SQLite where the store shape is not the
// point.
type stubTaskStore struct {
	live    []team.Task
	loadErr error
	calls   int
}

func (s *stubTaskStore) SaveTask(context.Context, team.Task) error { return nil }
func (s *stubTaskStore) LoadTask(context.Context, team.TaskID) (team.Task, error) {
	return team.Task{}, team.ErrTaskNotFound
}
func (s *stubTaskStore) LoadLiveTasks(context.Context) ([]team.Task, error) {
	s.calls++
	return s.live, s.loadErr
}

// stubRecoverExecutor records resumes so the tests can prove the loop really
// re-drives the members that are still in the fleet.
type stubRecoverExecutor struct{ resumed []string }

func (e *stubRecoverExecutor) Start(context.Context, team.Task, team.Member) error { return nil }
func (e *stubRecoverExecutor) Cancel(team.TaskID) error                            { return nil }
func (e *stubRecoverExecutor) Resume(_ context.Context, task team.Task, _ team.Member) error {
	e.resumed = append(e.resumed, string(task.ID))
	return nil
}

// TestRecoverTeamRuntimeResumesLive pins the host recovery loop contract: a
// persisted running task whose member is still in the fleet is re-driven
// through the scheduler, exactly once.
func TestRecoverTeamRuntimeResumesLive(t *testing.T) {
	store := &stubTaskStore{live: []team.Task{{ID: "t1", Status: team.TaskStatusRunning, AssignedMember: "m1"}}}
	exec := &stubRecoverExecutor{}
	sched := scheduler.NewRuntimeScheduler(exec)
	restored, err := RecoverTeamRuntime(context.Background(), store, sched, []team.Member{{ID: "m1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || restored[0].TaskID != "t1" || restored[0].Status != scheduler.StatusRunning {
		t.Fatalf("restored = %+v, want t1 running", restored)
	}
	if len(exec.resumed) != 1 || exec.resumed[0] != "t1" {
		t.Fatalf("executor resumed = %v, want [t1]", exec.resumed)
	}
	if store.calls != 1 {
		t.Fatalf("LoadLiveTasks calls = %d, want 1", store.calls)
	}
}

// TestRecoverTeamRuntimeEmptyStore: no live tasks means the scheduler is never
// consulted and the executor never runs.
func TestRecoverTeamRuntimeEmptyStore(t *testing.T) {
	exec := &stubRecoverExecutor{}
	restored, err := RecoverTeamRuntime(context.Background(), &stubTaskStore{}, scheduler.NewRuntimeScheduler(exec), []team.Member{{ID: "m1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 0 || len(exec.resumed) != 0 {
		t.Fatalf("restored = %+v, resumed = %v, want none", restored, exec.resumed)
	}
}

// TestRecoverTeamRuntimeStoreError: a failed store read aborts the recovery
// loop before any task is resumed — the store is the gate, not the log.
func TestRecoverTeamRuntimeStoreError(t *testing.T) {
	exec := &stubRecoverExecutor{}
	_, err := RecoverTeamRuntime(context.Background(), &stubTaskStore{loadErr: errors.New("disk gone")}, scheduler.NewRuntimeScheduler(exec), []team.Member{{ID: "m1"}})
	if err == nil {
		t.Fatal("a refused store read must surface")
	}
	if len(exec.resumed) != 0 {
		t.Fatalf("executor resumed %v despite the store error", exec.resumed)
	}
}

// TestRecoverTeamRuntimeMemberGonePersists pins the kill/reopen closure at the
// host boundary: a running task whose member vanished fails through the
// migration map, so a second restart never re-drives it.
func TestRecoverTeamRuntimeMemberGonePersists(t *testing.T) {
	store, err := team.NewSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTask(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusRunning, AssignedMember: "gone"}); err != nil {
		t.Fatal(err)
	}
	exec := &stubRecoverExecutor{}
	restored, err := RecoverTeamRuntime(context.Background(), store, scheduler.NewRuntimeScheduler(exec), []team.Member{{ID: "m1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || restored[0].Status != scheduler.StatusFailed {
		t.Fatalf("restored = %+v, want one failed", restored)
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

// TestRecoverTeamRuntimeAssignedGoneCancels: the migration map has no
// assigned -> failed edge, so an assigned task whose member vanished cancels —
// the host loop must not invent a transition the map rejects.
func TestRecoverTeamRuntimeAssignedGoneCancels(t *testing.T) {
	store, err := team.NewSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTask(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusAssigned, AssignedMember: "gone"}); err != nil {
		t.Fatal(err)
	}
	exec := &stubRecoverExecutor{}
	if _, err := RecoverTeamRuntime(context.Background(), store, scheduler.NewRuntimeScheduler(exec), []team.Member{{ID: "m1"}}); err != nil {
		t.Fatal(err)
	}
	saved, err := store.LoadTask(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != team.TaskStatusCanceled {
		t.Fatalf("saved status = %s, want canceled (no assigned->failed edge)", saved.Status)
	}
}
