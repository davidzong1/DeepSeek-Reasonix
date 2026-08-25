package team

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func testIdentity(memberID string, gen uint64) Identity {
	return Identity{MemberID: memberID, Generation: gen}
}

func TestBindBindsMember(t *testing.T) {
	r := NewBindingRegistry()
	rec, err := r.Bind("m1", testIdentity("leader-a", 1), TaskID("t1"))
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if rec.Status != BindStatusBound || rec.LeaderID != "leader-a" ||
		rec.Generation != 1 || rec.TaskID != "t1" || rec.MemberID != "m1" {
		t.Fatalf("unexpected record: %+v", rec)
	}
	if rec.BoundAt.IsZero() {
		t.Fatal("BoundAt not stamped")
	}
}

func TestBindRejectsEmptyTask(t *testing.T) {
	r := NewBindingRegistry()
	if _, err := r.Bind("m1", testIdentity("leader-a", 1), ""); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("want ErrInvalidTask, got %v", err)
	}
}

func TestBindIdempotentReplay(t *testing.T) {
	r := NewBindingRegistry()
	first, err := r.Bind("m1", testIdentity("leader-a", 1), TaskID("t1"))
	if err != nil {
		t.Fatalf("first Bind: %v", err)
	}
	second, err := r.Bind("m1", testIdentity("leader-a", 1), TaskID("t1"))
	if err != nil {
		t.Fatalf("replay Bind: %v", err)
	}
	if first != second {
		t.Fatalf("replay must return the original record: %+v vs %+v", first, second)
	}
}

func TestBindStaleGenerationGated(t *testing.T) {
	r := NewBindingRegistry()
	if _, err := r.Bind("m1", testIdentity("leader-a", 2), TaskID("t1")); err != nil {
		t.Fatalf("Bind gen2: %v", err)
	}
	if _, err := r.Bind("m1", testIdentity("leader-b", 1), TaskID("t1")); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("want ErrStaleGeneration, got %v", err)
	}
}

func TestBindSameGenerationDifferentLeaderConflict(t *testing.T) {
	r := NewBindingRegistry()
	if _, err := r.Bind("m1", testIdentity("leader-a", 1), TaskID("t1")); err != nil {
		t.Fatalf("Bind a: %v", err)
	}
	if _, err := r.Bind("m1", testIdentity("leader-b", 1), TaskID("t1")); !errors.Is(err, ErrBindConflict) {
		t.Fatalf("want ErrBindConflict, got %v", err)
	}
}

func TestBindHigherGenerationTakesOver(t *testing.T) {
	r := NewBindingRegistry()
	if _, err := r.Bind("m1", testIdentity("leader-a", 1), TaskID("t1")); err != nil {
		t.Fatalf("Bind a: %v", err)
	}
	rec, err := r.Bind("m1", testIdentity("leader-b", 2), TaskID("t2"))
	if err != nil {
		t.Fatalf("take-over Bind: %v", err)
	}
	if rec.LeaderID != "leader-b" || rec.TaskID != "t2" || rec.Generation != 2 {
		t.Fatalf("take-over must replace the record: %+v", rec)
	}
}

func TestUnbindRequiresMatchingHandoff(t *testing.T) {
	r := NewBindingRegistry()
	if _, err := r.Bind("m1", testIdentity("leader-a", 1), TaskID("t1")); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	// Wrong task id: rejected, member stays bound.
	if _, err := r.Unbind("m1", testIdentity("leader-a", 1), Handoff{TaskID: "t2"}); !errors.Is(err, ErrInvalidHandoff) {
		t.Fatalf("want ErrInvalidHandoff, got %v", err)
	}
	rec, ok := r.GetBind("m1")
	if !ok || rec.Status != BindStatusBound {
		t.Fatalf("failed unbind must leave the member bound: %+v ok=%v", rec, ok)
	}
	// Oversized digest: rejected.
	tooLong := ""
	for i := 0; i < 201; i++ {
		tooLong += "字"
	}
	if _, err := r.Unbind("m1", testIdentity("leader-a", 1), Handoff{TaskID: "t1", Digest: tooLong}); !errors.Is(err, ErrInvalidHandoff) {
		t.Fatalf("want ErrInvalidHandoff for long digest, got %v", err)
	}
	// Artifact pointer without a path: rejected.
	if _, err := r.Unbind("m1", testIdentity("leader-a", 1), Handoff{TaskID: "t1", ArtifactRefs: []ArtifactRef{{Name: "x"}}}); !errors.Is(err, ErrInvalidHandoff) {
		t.Fatalf("want ErrInvalidHandoff for empty path, got %v", err)
	}
}

func TestUnbindSuccessAndReplay(t *testing.T) {
	r := NewBindingRegistry()
	if _, err := r.Bind("m1", testIdentity("leader-a", 1), TaskID("t1")); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	h := Handoff{TaskID: "t1", Digest: "done", ArtifactRefs: []ArtifactRef{{Name: "r", Path: "artifacts/r.md"}}}
	rec, err := r.Unbind("m1", testIdentity("leader-a", 1), h)
	if err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if rec.Status != BindStatusUnbound {
		t.Fatalf("want unbound, got %+v", rec)
	}
	// Idempotent replay of the same handoff.
	if _, err := r.Unbind("m1", testIdentity("leader-a", 1), h); err != nil {
		t.Fatalf("replay Unbind: %v", err)
	}
	// Rebinding the same task after unbind succeeds.
	rec, err = r.Bind("m1", testIdentity("leader-a", 1), TaskID("t1"))
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if rec.Status != BindStatusBound {
		t.Fatalf("rebind must rebind: %+v", rec)
	}
}

func TestUnbindNotBound(t *testing.T) {
	r := NewBindingRegistry()
	if _, err := r.Unbind("ghost", testIdentity("leader-a", 1), Handoff{TaskID: "t1"}); !errors.Is(err, ErrNotBound) {
		t.Fatalf("want ErrNotBound, got %v", err)
	}
}

func TestExpiredTransitionRollsBack(t *testing.T) {
	r := NewBindingRegistry()
	prev := BindRecord{MemberID: "m1", LeaderID: "leader-a", Generation: 1,
		Status: BindStatusBound, TaskID: "t1", BoundAt: time.Now()}
	// A transition that started a second ago and never finished.
	r.records["m1"] = bindingState{
		rec: BindRecord{MemberID: "m1", LeaderID: "leader-b", Generation: 2,
			Status: BindStatusTransitioning, TaskID: "t2"},
		pending: &pendingTransition{prev: &prev, deadline: time.Now().Add(-time.Second)},
	}
	rec, ok := r.GetBind("m1")
	if !ok {
		t.Fatal("member missing after rollback")
	}
	if rec.Status != BindStatusBound || rec.LeaderID != "leader-a" || rec.TaskID != "t1" {
		t.Fatalf("expired transition must roll back to prev: %+v", rec)
	}
	if got, ok := r.records["m1"]; !ok || got.pending != nil {
		t.Fatalf("rollback must clear the pending transition: %+v", got)
	}
}

func TestExpiredTransitionDoesNotRollbackBeforeDeadline(t *testing.T) {
	r := NewBindingRegistry()
	prev := BindRecord{MemberID: "m1", LeaderID: "leader-a", Generation: 1, Status: BindStatusBound}
	r.records["m1"] = bindingState{
		rec: BindRecord{MemberID: "m1", LeaderID: "leader-b", Generation: 2,
			Status: BindStatusTransitioning},
		pending: &pendingTransition{prev: &prev, deadline: time.Now().Add(time.Second)},
	}
	rec, ok := r.GetBind("m1")
	if !ok {
		t.Fatal("member missing")
	}
	if rec.Status != BindStatusTransitioning {
		t.Fatalf("in-flight transition must survive: %+v", rec)
	}
}

func TestConcurrentBindSingleWinner(t *testing.T) {
	r := NewBindingRegistry()
	const n = 8
	var wg sync.WaitGroup
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := testIdentity(fmt.Sprintf("leader-%d", i), 1)
			_, err := r.Bind("m1", id, TaskID("t1"))
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ErrBindConflict):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("exactly one concurrent Bind must win, got %d (conflicts %d)", wins, conflicts)
	}
	if wins+conflicts != n {
		t.Fatalf("every contender must win or conflict: %d+%d != %d", wins, conflicts, n)
	}
}

func TestConcurrentBindSameLeaderIdempotent(t *testing.T) {
	r := NewBindingRegistry()
	const n = 8
	var wg sync.WaitGroup
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Bind("m1", testIdentity("leader-a", 1), TaskID("t1"))
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("same-leader replay must not conflict: %v", err)
		}
	}
}

func TestGetBindUnknownMember(t *testing.T) {
	r := NewBindingRegistry()
	if _, ok := r.GetBind("ghost"); ok {
		t.Fatal("unknown member must not be bound")
	}
}

func TestAllListsEveryRecord(t *testing.T) {
	r := NewBindingRegistry()
	if _, err := r.Bind("m1", testIdentity("leader-a", 1), TaskID("t1")); err != nil {
		t.Fatalf("Bind m1: %v", err)
	}
	if _, err := r.Bind("m2", testIdentity("leader-a", 1), TaskID("t2")); err != nil {
		t.Fatalf("Bind m2: %v", err)
	}
	if got := r.All(); len(got) != 2 {
		t.Fatalf("want 2 records, got %d", len(got))
	}
}
