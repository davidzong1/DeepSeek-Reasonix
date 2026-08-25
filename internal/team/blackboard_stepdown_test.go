package team

// Step-down contract (route §4.3): unbinding ends the binding, never the
// identity — binding records and cursors stay readable with the member's
// identity intact.

import (
	"context"
	"testing"
)

// TestStepDownKeepsBindingIdentityReadable: after unbind the record still
// reads back with the full member identity — member, leader, generation,
// task — and the unbound status. Step-down ends the binding, not the
// identity the blackboard carries.
func TestStepDownKeepsBindingIdentityReadable(t *testing.T) {
	dir := t.TempDir()
	s := newTestBoardAt(t, dir)
	defer s.Close()
	r := NewBindingRegistryWithPersister(s)
	identity := testIdentity("leader-a", 2)
	if _, err := r.Bind("m1", identity, TaskID("t1")); err != nil {
		t.Fatal(err)
	}
	h := Handoff{TaskID: "t1", Digest: "done", ArtifactRefs: []ArtifactRef{{Name: "r", Path: "artifacts/r.md"}}}
	if _, err := r.Unbind("m1", identity, h); err != nil {
		t.Fatal(err)
	}
	rec, ok := r.GetBind("m1")
	if !ok {
		t.Fatal("the record must stay readable after step-down")
	}
	if rec.Status != BindStatusUnbound {
		t.Fatalf("step-down must leave status unbound, got %+v", rec)
	}
	if rec.MemberID != "m1" || rec.LeaderID != "leader-a" || rec.Generation != 2 || rec.TaskID != "t1" {
		t.Fatalf("step-down must keep the identity readable: %+v", rec)
	}
}

// TestStepDownKeepsCursorReadable: a step-down clears no cursors — the
// member's consumer row still reads back with the same position, because
// consumer identity is board data, not leader context.
func TestStepDownKeepsCursorReadable(t *testing.T) {
	dir := t.TempDir()
	s := newTestBoardAt(t, dir)
	defer s.Close()
	r := NewBindingRegistryWithPersister(s)
	if _, err := r.Bind("m1", testIdentity("leader-a", 1), TaskID("t1")); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceCursor(context.Background(), CursorUpdate{
		BoardID: BoardShared, ConsumerID: "m1", Generation: 1, LastSeq: 7,
	}); err != nil {
		t.Fatal(err)
	}
	h := Handoff{TaskID: "t1", Digest: "done", ArtifactRefs: []ArtifactRef{{Name: "r", Path: "artifacts/r.md"}}}
	if _, err := r.Unbind("m1", testIdentity("leader-a", 1), h); err != nil {
		t.Fatal(err)
	}
	cur, err := s.GetCursor(context.Background(), BoardShared, "m1")
	if err != nil {
		t.Fatalf("the member cursor must survive step-down: %v", err)
	}
	if cur.ConsumerID != "m1" || cur.LastSeq != 7 {
		t.Fatalf("cursor identity or position lost after step-down: %+v", cur)
	}
}
