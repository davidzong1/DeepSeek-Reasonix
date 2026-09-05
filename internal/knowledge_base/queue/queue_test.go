package queue

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// reopen simulates a process restart: close then Open the same dir again.
func reopen(t *testing.T, dir string) *Queue {
	t.Helper()
	q, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	return q
}

func TestOpenCreatesLogNoCursor(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "q")
	q := reopen(t, dir)
	defer q.Close()
	if got := q.Committed(); got != 0 {
		t.Errorf("initial committed = %d, want 0", got)
	}
	if err := q.Replay(0, func(Event) error { t.Error("unexpected event"); return nil }); err != nil {
		t.Fatalf("empty replay: %v", err)
	}
}

func TestAppendCommitSurvivesRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "q")
	q := reopen(t, dir)
	seqs := make([]uint64, 3)
	for k := 0; k < 3; k++ {
		s, err := q.Append("ingest", []byte(`{"n":`+string(rune('0'+k))+`}`))
		if err != nil {
			t.Fatal(err)
		}
		seqs[k] = s
	}
	if seqs[0] != 1 || seqs[2] != 3 {
		t.Fatalf("seqs = %v, want 1..3", seqs)
	}
	if err := q.Commit(2); err != nil {
		t.Fatal(err)
	}
	if got := q.Committed(); got != 2 {
		t.Errorf("committed = %d, want 2", got)
	}
	q.Close()

	// Restart: cursor 2 survives, event 3 is uncommitted but still present.
	q2 := reopen(t, dir)
	defer q2.Close()
	if got := q2.Committed(); got != 2 {
		t.Errorf("after restart committed = %d, want 2", got)
	}
	var seen []uint64
	if err := q2.Replay(2, func(ev Event) error { seen = append(seen, ev.Seq); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != 3 {
		t.Errorf("replay after committed = %v, want [3]", seen)
	}
	if err := q2.Commit(3); err != nil {
		t.Fatal(err)
	}
	if err := q2.Replay(3, func(ev Event) error { t.Error("expected no replay after full commit"); return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestCommitNeverRegresses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "q")
	q := reopen(t, dir)
	defer q.Close()
	for k := 0; k < 3; k++ {
		if _, err := q.Append("x", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Commit(2); err != nil {
		t.Fatal(err)
	}
	if err := q.Commit(2); err != nil {
		t.Errorf("idempotent re-commit failed: %v", err)
	}
	if err := q.Commit(1); err != nil {
		t.Errorf("commit below cursor should be a no-op, got %v", err)
	}
	if got := q.Committed(); got != 2 {
		t.Errorf("cursor regressed to %d", got)
	}
}

func TestCommitAheadRejected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "q")
	q := reopen(t, dir)
	defer q.Close()
	if _, err := q.Append("x", nil); err != nil {
		t.Fatal(err)
	}
	if err := q.Commit(5); !errors.Is(err, ErrCursorAhead) {
		t.Errorf("commit 5 past max = %v, want ErrCursorAhead", err)
	}
}

func TestUncommittedEventsSurviveCrash(t *testing.T) {
	// Simulates kill -9 in the window "store committed, cursor not yet advanced":
	// uncommitted events must still be replayable after restart (at-least-once).
	dir := filepath.Join(t.TempDir(), "q")
	q := reopen(t, dir)
	if _, err := q.Append("ingest", []byte(`{"first":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := q.Commit(1); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Append("ingest", []byte(`{"second":true}`)); err != nil {
		t.Fatal(err)
	}
	q.Close() // no Commit(2): crash window

	q2 := reopen(t, dir)
	defer q2.Close()
	if got := q2.Committed(); got != 1 {
		t.Errorf("committed = %d, want 1", got)
	}
	var got []Event
	if err := q2.Replay(1, func(ev Event) error { got = append(got, ev); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Seq != 2 || string(got[0].Payload) != `{"second":true}` {
		t.Errorf("replayed = %+v, want exactly event 2", got)
	}
}

func TestConcurrentAppendSequencesUnique(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "q")
	q := reopen(t, dir)
	defer q.Close()
	const n = 24
	var wg sync.WaitGroup
	seqs := make([]uint64, n)
	for k := 0; k < n; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			s, err := q.Append("x", []byte(`{}`))
			if err != nil {
				t.Errorf("append: %v", err)
				return
			}
			seqs[k] = s
		}(k)
	}
	wg.Wait()
	uniq := map[uint64]bool{}
	for _, s := range seqs {
		if s == 0 || uniq[s] {
			t.Errorf("duplicate/zero seq %d", s)
		}
		uniq[s] = true
	}
	if err := q.Replay(0, func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

// TestCloseThenReopenContinuesWatermark pins the old-handle lifecycle behind
// ClearTeam's swap: once a handle is Closed, a fresh Open of the same dir must
// reload committed + maxSeq from disk and keep appending from where the closed
// handle left off (no lost watermark, no seq collision).
func TestCloseThenReopenContinuesWatermark(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "q")
	q := reopen(t, dir)
	for k := 0; k < 2; k++ {
		if _, err := q.Append("ingest", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Commit(2); err != nil {
		t.Fatal(err)
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}

	q2 := reopen(t, dir)
	defer q2.Close()
	if got := q2.Committed(); got != 2 {
		t.Fatalf("reopened committed = %d, want 2", got)
	}
	s, err := q2.Append("ingest", []byte(`{"after":"reopen"}`))
	if err != nil {
		t.Fatal(err)
	}
	if s != 3 {
		t.Fatalf("reopened append seq = %d, want 3", s)
	}
	if err := q2.Commit(3); err != nil {
		t.Fatal(err)
	}
}

// TestEventsLogMissingFailsClosed pins the events.log recovery contract: the
// log is the durable truth, so Open must refuse to come up when the log is gone
// but a committed cursor remains (fail closed rather than silently forget that
// committed events existed).
func TestEventsLogMissingFailsClosed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "q")
	q := reopen(t, dir)
	if _, err := q.Append("ingest", nil); err != nil {
		t.Fatal(err)
	}
	if err := q.Commit(1); err != nil {
		t.Fatal(err)
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, eventsFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("Open after events.log deleted with a committed cursor must fail closed")
	}
}
