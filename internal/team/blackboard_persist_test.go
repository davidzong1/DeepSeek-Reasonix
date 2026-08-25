package team

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// newTestBoardAt opens the board database at dir without auto-close: the
// test owns the lifecycle so it can Close and reopen the same file.
func newTestBoardAt(t *testing.T, dir string) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(context.Background(), filepath.Join(dir, "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestBindingSurvivesReopen matches route §4.3: a server restart restores
// the bound record with the same generation — the restart does not bump
// generations, so live windows survive unchanged.
func TestBindingSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s := newTestBoardAt(t, dir)
	r := NewBindingRegistryWithPersister(s)
	if _, err := r.Bind("m1", testIdentity("leader-a", 7), TaskID("t1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2 := newTestBoardAt(t, dir)
	defer s2.Close()
	r2 := NewBindingRegistryWithPersister(s2)
	recs, err := s2.LoadBindings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].MemberID != "m1" {
		t.Fatalf("want one persisted record, got %+v", recs)
	}
	r2.Restore(recs)
	rec, ok := r2.GetBind("m1")
	if !ok {
		t.Fatal("bound member must survive the restart")
	}
	if rec.Generation != 7 || rec.LeaderID != "leader-a" || rec.TaskID != "t1" {
		t.Fatalf("restart must restore the record unchanged: %+v", rec)
	}
	if rec.Status != BindStatusBound {
		t.Fatalf("restored record must stay bound: %+v", rec)
	}
}

// TestUnboundSurvivesReopen: an unbound record persists too, so a
// restarted server replays the unbind instead of reporting ErrNotBound.
func TestUnboundSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s := newTestBoardAt(t, dir)
	r := NewBindingRegistryWithPersister(s)
	if _, err := r.Bind("m1", testIdentity("leader-a", 1), TaskID("t1")); err != nil {
		t.Fatal(err)
	}
	h := Handoff{TaskID: "t1", Digest: "done", ArtifactRefs: []ArtifactRef{{Name: "r", Path: "artifacts/r.md"}}}
	if _, err := r.Unbind("m1", testIdentity("leader-a", 1), h); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2 := newTestBoardAt(t, dir)
	defer s2.Close()
	recs, err := s2.LoadBindings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r2 := NewBindingRegistryWithPersister(s2)
	r2.Restore(recs)
	rec, ok := r2.GetBind("m1")
	if !ok || rec.Status != BindStatusUnbound {
		t.Fatalf("restart must restore the unbound state: %+v ok=%v", rec, ok)
	}
	if _, err := r2.Unbind("m1", testIdentity("leader-a", 1), h); err != nil {
		t.Fatalf("unbind replay across restart: %v", err)
	}
}

// TestBindPersistFailureLeavesMemoryUntouched: a failed durable write
// leaves the member where it was — the record never half-exists.
func TestBindPersistFailureLeavesMemoryUntouched(t *testing.T) {
	boom := errors.New("persist boom")
	r := NewBindingRegistry()
	r.persist = func(BindRecord) error { return boom }
	if _, err := r.Bind("m1", testIdentity("leader-a", 1), TaskID("t1")); !errors.Is(err, boom) {
		t.Fatalf("want persist error, got %v", err)
	}
	if _, ok := r.GetBind("m1"); ok {
		t.Fatal("failed persist must not leave a bound record")
	}
}

// TestCursorSurvivesReopen matches route §2.3/§4.3: after a server
// restart the member continues from the persisted position — events read
// before the restart are never re-rendered.
func TestCursorSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s := newTestBoardAt(t, dir)
	for i := 0; i < 10; i++ {
		boardAppend(s, fmt.Sprintf("e%d", i), "m1", 1)
	}
	cursors := NewSQLiteCursorStore(s, 1)
	a := newAssembler(s, "m1", 1, cursors, NewViewCache())
	as, err := a.Advance(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if as.Cursor.LastSeq != 10 {
		t.Fatalf("cursor must reach 10, got %d", as.Cursor.LastSeq)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2 := newTestBoardAt(t, dir)
	defer s2.Close()
	cursors2 := NewSQLiteCursorStore(s2, 1)
	cur, err := cursors2.LoadCursor(BoardShared, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if cur.LastSeq != 10 {
		t.Fatalf("restart must restore cursor 10, got %d", cur.LastSeq)
	}
	boardAppend(s2, "e10", "m1", 1)
	boardAppend(s2, "e11", "m1", 1)
	a2 := newAssembler(s2, "m1", 1, cursors2, NewViewCache())
	as2, err := a2.Advance(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if n := deltaItems(as2.L1); n != 2 {
		t.Fatalf("after restart only the 2 new events must render, got %d", n)
	}
	if as2.Cursor.LastSeq != 12 {
		t.Fatalf("cursor must advance to 12 after restart, got %d", as2.Cursor.LastSeq)
	}
}

// TestCursorGenerationMismatchResets: a window change bumps the
// generation, so the member starts over instead of continuing the old
// window's cursor (route §4.3).
func TestCursorGenerationMismatchResets(t *testing.T) {
	dir := t.TempDir()
	s := newTestBoardAt(t, dir)
	defer s.Close()
	if err := s.AdvanceCursor(context.Background(), CursorUpdate{
		BoardID: BoardShared, ConsumerID: "m1", Generation: 1, LastSeq: 42,
	}); err != nil {
		t.Fatal(err)
	}
	cur, err := NewSQLiteCursorStore(s, 2).LoadCursor(BoardShared, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if cur.LastSeq != 0 {
		t.Fatalf("older-generation cursor must read as first read, got %d", cur.LastSeq)
	}
}

// TestCursorNoRowIsFirstRead: a member with no persisted position starts
// from zero.
func TestCursorNoRowIsFirstRead(t *testing.T) {
	s := newTestBoard(t)
	cur, err := NewSQLiteCursorStore(s, 1).LoadCursor(BoardShared, "ghost")
	if err != nil {
		t.Fatal(err)
	}
	if cur.LastSeq != 0 {
		t.Fatalf("no row must read as zero cursor, got %d", cur.LastSeq)
	}
}

// TestCursorSaveToleratesStaleMirror: a cursor write behind the persisted
// position is tolerated — the read source is unaffected (mirror
// semantics, §2.3).
func TestCursorSaveToleratesStaleMirror(t *testing.T) {
	s := newTestBoard(t)
	cs := NewSQLiteCursorStore(s, 1)
	if err := s.AdvanceCursor(context.Background(), CursorUpdate{
		BoardID: BoardShared, ConsumerID: "m1", Generation: 1, LastSeq: 50,
	}); err != nil {
		t.Fatal(err)
	}
	if err := cs.SaveCursor(BoardCursor{BoardID: BoardShared, ConsumerID: "m1", LastSeq: 5, Epoch: 1}); err != nil {
		t.Fatalf("backwards mirror save must be tolerated, got %v", err)
	}
	cur, err := NewSQLiteCursorStore(s, 1).LoadCursor(BoardShared, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if cur.LastSeq != 50 {
		t.Fatalf("persisted position must stay 50, got %d", cur.LastSeq)
	}
}
