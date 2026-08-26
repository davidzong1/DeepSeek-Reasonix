package team

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func newTaskStore(t *testing.T) (*SQLiteStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "board.db")
	store, err := NewSQLiteStore(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return store, path
}

// TestTaskStoreSaveLoad pins the upsert round trip: the full task survives,
// and re-saving the same id overwrites instead of duplicating.
func TestTaskStoreSaveLoad(t *testing.T) {
	store, _ := newTaskStore(t)
	ctx := context.Background()
	task := Task{ID: "t1", RequireRole: RoleCoder, Desc: "build it", Expected: "green",
		Status: TaskStatusAssigned, AssignedMember: "m1", CreatedAt: "2026-08-25T00:00:00Z"}
	if err := store.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Desc != "build it" || got.Status != TaskStatusAssigned || got.AssignedMember != "m1" {
		t.Fatalf("loaded = %+v, want the saved task", got)
	}
	task.Status = TaskStatusRunning
	if err := store.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	got, err = store.LoadTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != TaskStatusRunning {
		t.Fatalf("after upsert status = %s, want running", got.Status)
	}
}

func TestTaskStoreLoadUnknown(t *testing.T) {
	store, _ := newTaskStore(t)
	_, err := store.LoadTask(context.Background(), "nope")
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("err = %v, want ErrTaskNotFound", err)
	}
}

// TestTaskStoreLoadLiveTasks pins the kill/reopen recovery set: only
// assigned or running tasks are re-driven; terminal states stay archived.
func TestTaskStoreLoadLiveTasks(t *testing.T) {
	store, _ := newTaskStore(t)
	ctx := context.Background()
	for _, st := range []TaskStatus{TaskStatusAssigned, TaskStatusRunning, TaskStatusReported, TaskStatusCanceled} {
		if err := store.SaveTask(ctx, Task{ID: TaskID("t-" + st), Status: st}); err != nil {
			t.Fatal(err)
		}
	}
	live, err := store.LoadLiveTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 {
		t.Fatalf("live = %d tasks, want 2 (assigned + running)", len(live))
	}
	for _, tk := range live {
		if tk.Status != TaskStatusAssigned && tk.Status != TaskStatusRunning {
			t.Fatalf("live task %s in terminal state %s", tk.ID, tk.Status)
		}
	}
}

// TestTaskStoreSurvivesReopen pins the kill/reopen contract: the same
// database reopened on restart returns the live tasks from the previous
// process, so the recovery loop re-drives them.
func TestTaskStoreSurvivesReopen(t *testing.T) {
	_, path := newTaskStore(t)
	first, err := NewSQLiteStore(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := first.SaveTask(ctx, Task{ID: "t1", Status: TaskStatusRunning, AssignedMember: "m1"}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := NewSQLiteStore(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	live, err := second.LoadLiveTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].ID != "t1" || live[0].AssignedMember != "m1" {
		t.Fatalf("after reopen live = %+v, want t1 running on m1", live)
	}
}
