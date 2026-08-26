package agentruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"reasonix/internal/team"
)

// TestRuntimeCompleteUnknownTask: the report path refuses a task the runtime
// never started — reporting is a single-point migration, not an open door.
func TestRuntimeCompleteUnknownTask(t *testing.T) {
	rt, _ := newTestRuntime(t, nil)
	if err := rt.Complete("ghost", "done"); !errors.Is(err, ErrTaskUnknown) {
		t.Fatalf("complete on an unknown task = %v, want ErrTaskUnknown", err)
	}
}

// TestRuntimeCompleteFreesMemberSlot: a reported task releases its member, so
// the next dispatch does not bounce off ErrMemberBusy — report is both the
// return path and the slot release (mirror of cancel).
func TestRuntimeCompleteFreesMemberSlot(t *testing.T) {
	rt, agents := newTestRuntime(t, nil)
	member := team.Member{ID: "alpha"}
	if err := rt.Start(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusAssigned}, member); err != nil {
		t.Fatal(err)
	}
	if err := rt.Complete("t1", "done"); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background(), team.Task{ID: "t2", Status: team.TaskStatusAssigned}, member); err != nil {
		t.Fatalf("member slot must be free after a report: %v", err)
	}
	if len(agents["alpha"].submitted) != 2 {
		t.Fatalf("submissions = %d, want 2 after the second start", len(agents["alpha"].submitted))
	}
}

// TestRuntimeCompleteRejectsDoubleReport: a second report on the same task is
// refused — the entry is dropped on the first report, so a replaying caller
// can never double-count one result.
func TestRuntimeCompleteRejectsDoubleReport(t *testing.T) {
	rt, _ := newTestRuntime(t, nil)
	if err := rt.Start(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusAssigned}, team.Member{ID: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Complete("t1", "done"); err != nil {
		t.Fatal(err)
	}
	if err := rt.Complete("t1", "done again"); !errors.Is(err, ErrTaskUnknown) {
		t.Fatalf("second report = %v, want ErrTaskUnknown", err)
	}
}

// TestRuntimeWakeupsInRegistrationOrder: leader wakeups fire in registration
// order — the first registered receiver sees the report before later ones.
func TestRuntimeWakeupsInRegistrationOrder(t *testing.T) {
	rt, _ := newTestRuntime(t, nil)
	var first, second []string
	rt.AddWakeup(func(reason string) error { first = append(first, reason); return nil })
	rt.AddWakeup(func(reason string) error { second = append(second, reason); return nil })
	if err := rt.Start(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusAssigned}, team.Member{ID: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Complete("t1", "done"); err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 || !strings.Contains(first[0], "reported") || first[0] != second[0] {
		t.Fatalf("wakeups first=%v second=%v, want one report notice each, same reason", first, second)
	}
}

// TestRuntimeCompleteRefusesPersistFailure: the report is written before the
// wake fires — a refused durable save aborts the completion, so a crashed
// report can never leave the store behind the blackboard notice.
func TestRuntimeCompleteRefusesPersistFailure(t *testing.T) {
	rt, _ := newTestRuntime(t, nil)
	if err := rt.Start(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusAssigned}, team.Member{ID: "alpha"}); err != nil {
		t.Fatal(err)
	}
	var wakes []string
	rt.AddWakeup(func(reason string) error { wakes = append(wakes, reason); return nil })
	rt.SetTaskStore(failingStore{})
	err := rt.Complete("t1", "done")
	if err == nil {
		t.Fatal("a refused save must surface")
	}
	if len(wakes) != 0 {
		t.Fatalf("wakeups = %v, want none after a refused save", wakes)
	}
}
