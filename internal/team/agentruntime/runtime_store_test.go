package agentruntime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"reasonix/internal/team"
)

// newTestTaskBoard wires a runtime over a concrete SQLite store: the same
// connection serves as blackboard and durable task store, like a host opening
// board.db once.
func newTestTaskBoard(t *testing.T) (*Runtime, *team.SQLiteStore) {
	t.Helper()
	store, err := team.NewSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(func(memberID string) (AgentAPI, error) {
		return &stubAgent{}, nil
	}, store, "team:T", func(memberID string) team.Identity {
		return team.Identity{MemberID: memberID, Generation: 1}
	})
	rt.SetTaskStore(store)
	return rt, store
}

// TestRuntimeStartPersistsRunning pins the write-before-commit contract: the
// durable store records running before the agent sees the task, so a refused
// save leaves no half-launched agent.
func TestRuntimeStartPersistsRunning(t *testing.T) {
	rt, store := newTestTaskBoard(t)
	member := team.Member{ID: "alpha"}
	if err := rt.Start(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusAssigned}, member); err != nil {
		t.Fatal(err)
	}
	saved, err := store.LoadTask(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != team.TaskStatusRunning || saved.AssignedMember != "alpha" {
		t.Fatalf("saved = %+v, want running on alpha", saved)
	}
}

// TestRuntimeCancelPersistsCanceled: cancel writes canceled before the
// backend stops, so a crashed cancel still leaves the store truthful.
func TestRuntimeCancelPersistsCanceled(t *testing.T) {
	rt, store := newTestTaskBoard(t)
	if err := rt.Start(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusAssigned}, team.Member{ID: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Cancel("t1"); err != nil {
		t.Fatal(err)
	}
	saved, err := store.LoadTask(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != team.TaskStatusCanceled {
		t.Fatalf("saved status = %s, want canceled", saved.Status)
	}
}

// TestRuntimeCompletePersistsReported: the report path persists through the
// migration map, so a kill/reopen never re-drives a completed task.
func TestRuntimeCompletePersistsReported(t *testing.T) {
	rt, store := newTestTaskBoard(t)
	if err := rt.Start(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusAssigned}, team.Member{ID: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Complete("t1", "done"); err != nil {
		t.Fatal(err)
	}
	saved, err := store.LoadTask(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != team.TaskStatusReported {
		t.Fatalf("saved status = %s, want reported", saved.Status)
	}
	live, err := store.LoadLiveTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Fatalf("live = %+v, want none after a report", live)
	}
}

// TestRuntimeStartRefusesPersistFailure: a refused durable write aborts the
// start before the agent is submitted — the store is the gate, not the log.
func TestRuntimeStartRefusesPersistFailure(t *testing.T) {
	rt, agents := newTestRuntime(t, nil)
	rt.SetTaskStore(failingStore{})
	err := rt.Start(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusAssigned}, team.Member{ID: "alpha"})
	if err == nil {
		t.Fatal("a refused save must surface")
	}
	if len(agents["alpha"].submitted) != 0 {
		t.Fatalf("agent was submitted despite the refused save: %v", agents["alpha"].submitted)
	}
}

// TestRuntimeResumePersistsRunning: the recovery path re-persists running, so
// a second kill finds the resumed task live.
func TestRuntimeResumePersistsRunning(t *testing.T) {
	rt, store := newTestTaskBoard(t)
	task := team.Task{ID: "t1", Status: team.TaskStatusRunning, AssignedMember: "alpha"}
	if err := rt.Resume(context.Background(), task, team.Member{ID: "alpha"}); err != nil {
		t.Fatal(err)
	}
	saved, err := store.LoadTask(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != team.TaskStatusRunning {
		t.Fatalf("saved status = %s, want running", saved.Status)
	}
}

// TestRuntimeResumeRefusesPersistFailure: the recovery path writes running to
// the durable store BEFORE the re-submission — a refused save aborts the resume
// before the backend is touched, so a crashed restart cannot half-launch an
// agent that was never submitted (mirror of the Start persist gate).
func TestRuntimeResumeRefusesPersistFailure(t *testing.T) {
	rt, agents := newTestRuntime(t, nil)
	rt.SetTaskStore(failingStore{})
	task := team.Task{ID: "t1", Status: team.TaskStatusRunning, AssignedMember: "alpha"}
	err := rt.Resume(context.Background(), task, team.Member{ID: "alpha"})
	if err == nil {
		t.Fatal("a refused save must surface")
	}
	if len(agents["alpha"].submitted) != 0 {
		t.Fatalf("agent was submitted despite the refused save: %v", agents["alpha"].submitted)
	}
	// The rejected resume must not leave a reservation behind: the member is
	// free for a fresh start, never wedged by the failed recovery attempt.
	if member, busy := rt.byMember["alpha"]; busy {
		t.Fatalf("member reservation leaked after refused resume (%q still holds the task)", member)
	}
}

// TestRuntimeResumeRefusedSubmissionSettlesAssigned: a backend that refuses the
// re-submitted turn must surface as a resume failure and settle the durable task
// back on assigned — never a persisted "running" task that never ran again (the
// Resume execution gate, mirror of the Start no-ghost contract). The path is
// running -> failed -> assigned: both edges legal, and the task stays
// re-dispatchable rather than a ghost a third restart re-resumes.
func TestRuntimeResumeRefusedSubmissionSettlesAssigned(t *testing.T) {
	rt, store := newTestTaskBoard(t)
	rt.agents = func(string) (AgentAPI, error) { return &refusingAgent{}, nil }
	task := team.Task{ID: "t1", Status: team.TaskStatusRunning, AssignedMember: "alpha"}
	err := rt.Resume(context.Background(), task, team.Member{ID: "alpha"})
	if err == nil {
		t.Fatal("a refused re-submit must surface as a resume error")
	}
	saved, loadErr := store.LoadTask(context.Background(), "t1")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if saved.Status != team.TaskStatusAssigned {
		t.Fatalf("saved status = %s, want assigned (settled via failed, never a ghost running)", saved.Status)
	}
}

// failingStore rejects every write; it lets the persist-gate tests prove a
// refused save aborts the state move.
type failingStore struct{}

func (failingStore) SaveTask(context.Context, team.Task) error { return errors.New("disk full") }
func (failingStore) LoadTask(context.Context, team.TaskID) (team.Task, error) {
	return team.Task{}, team.ErrTaskNotFound
}
func (failingStore) LoadLiveTasks(context.Context) ([]team.Task, error) { return nil, nil }
