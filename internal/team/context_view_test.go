package team

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func boardAppendKind(t *testing.T, s *SQLiteStore, msgID, member string, gen uint64, kind EventKind, task TaskID, summary string) {
	t.Helper()
	_, err := s.Append(context.Background(), AppendInput{
		BoardID: BoardShared, ClientMsgID: msgID, Kind: kind, TaskID: task,
		CreatedAt: time.Now().UTC(), Summary: summary,
		Stamped: Identity{MemberID: member, Role: "coder", Agent: "claude", Generation: gen},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func newAssembler(s *SQLiteStore, member string, epoch uint64, cursors CursorStore, cache *ViewCache) *Assembler {
	return NewAssembler(BoardShared, Identity{MemberID: member, Role: "coder", Agent: "claude", Generation: 1},
		s, cursors, cache, NewViewBuilder(BoardShared, epoch))
}

func deltaItems(v BoardView) int {
	return strings.Count(v.Content, "\n- ")
}

// TestCursorThreeStates matches route §2.3/§3.2: an empty cursor serves the
// first full delta, a normal cursor serves only what is new, and a cursor
// inside an archived hole resyncs from the start instead of silently
// skipping the gap.
func TestCursorThreeStates(t *testing.T) {
	s := newTestBoard(t)
	for i := 0; i < 10; i++ {
		boardAppendKind(t, s, fmt.Sprintf("e%d", i), "m1", 1, EventReport, "t1", "summary")
	}
	cursors := NewMemoryCursorStore()
	cache := NewViewCache()
	a := newAssembler(s, "m1", 1, cursors, cache)

	// 空游标:首次读取 = 全量 delta。
	as, err := a.Advance(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if n := deltaItems(as.L1); n != 10 {
		t.Fatalf("first read must serve the full delta of 10, got %d", n)
	}
	if as.Cursor.LastSeq != 10 {
		t.Fatalf("cursor must advance to 10, got %d", as.Cursor.LastSeq)
	}

	// 正常游标:只读增量。
	boardAppendKind(t, s, "e10", "m1", 1, EventReport, "t1", "summary")
	boardAppendKind(t, s, "e11", "m1", 1, EventReport, "t2", "summary")
	as, err = a.Advance(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if n := deltaItems(as.L1); n != 2 {
		t.Fatalf("delta read must serve 2 new events, got %d", n)
	}
	if as.Cursor.LastSeq != 12 {
		t.Fatalf("cursor must advance to 12, got %d", as.Cursor.LastSeq)
	}

	// 越界游标:落在归档洞内 -> need_resync,从起点重建。
	if err := cursors.SaveCursor(BoardCursor{BoardID: BoardShared, ConsumerID: "m2", LastSeq: 5}); err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveBefore(context.Background(), BoardShared, 7, Identity{MemberID: "leader", Role: "leader", Generation: 1}); err != nil {
		t.Fatal(err)
	}
	a2 := newAssembler(s, "m2", 1, cursors, cache)
	as2, err := a2.Advance(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !as2.NeedResync {
		t.Fatal("cursor inside an archived hole must set NeedResync")
	}
	if n := deltaItems(as2.L1); n != 5 {
		t.Fatalf("resync must serve the surviving 5 events, got %d", n)
	}
	if as2.Cursor.LastSeq != 12 {
		t.Fatalf("resync cursor must advance to 12, got %d", as2.Cursor.LastSeq)
	}
}

// TestNoDoubleConsumption matches route §3.3/§3.4: an event is rendered
// into exactly one turn; a second read with no new events renders nothing.
func TestNoDoubleConsumption(t *testing.T) {
	s := newTestBoard(t)
	for i := 0; i < 3; i++ {
		boardAppendKind(t, s, fmt.Sprintf("e%d", i), "m1", 1, EventConclusion, "t1", "summary")
	}
	a := newAssembler(s, "m1", 1, NewMemoryCursorStore(), NewViewCache())
	as1, err := a.Advance(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	as2, err := a.Advance(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if n := deltaItems(as2.L1); n != 0 {
		t.Fatalf("second read must render no events, got %d", n)
	}
	if as2.Cursor.LastSeq != as1.Cursor.LastSeq {
		t.Fatalf("cursor must not move without new events: %d -> %d", as1.Cursor.LastSeq, as2.Cursor.LastSeq)
	}
	if as2.L0.Content != as1.L0.Content || as2.L0.Digest != as1.L0.Digest {
		t.Fatal("L0 must be byte-stable across turns with no new events (cache-first)")
	}
}

// TestStaleFallbackNeverEmpty matches route §3.2: a read failure returns
// the last-known views marked Stale; with no prior render it returns the
// error and nothing pretending to be fresh.
func TestStaleFallbackNeverEmpty(t *testing.T) {
	s := newTestBoard(t)
	boardAppendKind(t, s, "e0", "m1", 1, EventReport, "t1", "summary")
	boardAppendKind(t, s, "e1", "m1", 1, EventConclusion, "t1", "conclusion")
	cursors := NewMemoryCursorStore()
	cache := NewViewCache()
	a := newAssembler(s, "m1", 1, cursors, cache)
	if _, err := a.Advance(context.Background(), false); err != nil {
		t.Fatal(err)
	}

	fail := &failStore{BoardStore: s}
	ab := NewAssembler(BoardShared, Identity{MemberID: "m1", Role: "coder", Agent: "claude", Generation: 1},
		fail, cursors, cache, NewViewBuilder(BoardShared, 1))
	as, err := ab.Advance(context.Background(), false)
	if err == nil {
		t.Fatal("failing source must surface the read error")
	}
	if !as.L0.Stale || as.L0.Content == "" {
		t.Fatalf("stale fallback must return the last-known non-empty view, got Stale=%v Content=%q", as.L0.Stale, as.L0.Content)
	}
	if !as.L1.Stale || as.L1.Content == "" {
		t.Fatal("L1 stale fallback must be marked and non-empty")
	}

	fresh := NewViewCache()
	af := NewAssembler(BoardShared, Identity{MemberID: "m1", Role: "coder", Agent: "claude", Generation: 1},
		fail, NewMemoryCursorStore(), fresh, NewViewBuilder(BoardShared, 1))
	asf, err := af.Advance(context.Background(), false)
	if err == nil || asf.L0.Content != "" {
		t.Fatal("a fresh failing reader must return the error and no content")
	}
}

type failStore struct{ BoardStore }

func (f *failStore) ReadAfter(context.Context, string, int64, Filter) (Page, error) {
	return Page{}, errors.New("read boom")
}

// TestL2OnDemand: L2 is only rendered when requested, so a turn without it
// pays no detail cost.
func TestL2OnDemand(t *testing.T) {
	s := newTestBoard(t)
	boardAppendKind(t, s, "e0", "m1", 1, EventAssignment, "t1", "assign")
	boardAppendKind(t, s, "e1", "m1", 1, EventConclusion, "t2", "conclusion")
	a := newAssembler(s, "m1", 1, NewMemoryCursorStore(), NewViewCache())
	as, err := a.Advance(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if as.L2.Content != "" {
		t.Fatal("L2 must be empty when not requested")
	}
	boardAppendKind(t, s, "e2", "m1", 1, EventAssignment, "t3", "assign2")
	as2, err := a.Advance(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if as2.L2.Content == "" || !strings.Contains(as2.L2.Content, "assignments:") {
		t.Fatalf("L2 must render the assignments section on request:\n%s", as2.L2.Content)
	}
}

// TestEpochInvalidationWholeKey matches route §3.2: an epoch change drops
// every cached entry of that epoch at once — the L0 index restarts instead
// of reusing accumulated counts across epochs.
func TestEpochInvalidationWholeKey(t *testing.T) {
	s := newTestBoard(t)
	for i := 0; i < 5; i++ {
		boardAppendKind(t, s, fmt.Sprintf("e%d", i), "m1", 1, EventReport, "t1", "summary")
	}
	cursors := NewMemoryCursorStore()
	cache := NewViewCache()
	a := newAssembler(s, "m1", 1, cursors, cache)
	as, err := a.Advance(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(as.L0.Content, "task t1: 5 items") {
		t.Fatalf("L0 must index the accumulated t1 counts:\n%s", as.L0.Content)
	}

	cache.InvalidateEpoch(BoardShared, "m1", 1)
	boardAppendKind(t, s, "e5", "m1", 1, EventReport, "t2", "new task")
	as2, err := a.Advance(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(as2.L0.Content, "task t2: 1 items") || strings.Contains(as2.L0.Content, "task t1:") {
		t.Fatalf("epoch invalidation must restart the index, no partial reuse:\n%s", as2.L0.Content)
	}
}

// TestConcurrentConsumersRace matches route §2.4: independent consumers
// advance their own cursors over a shared store without interference;
// -race guards the shared cursor store.
func TestConcurrentConsumersRace(t *testing.T) {
	s := newTestBoard(t)
	for i := 0; i < 50; i++ {
		boardAppendKind(t, s, fmt.Sprintf("e%d", i), "m1", 1, EventReport, "t1", "summary")
	}
	cursors := NewMemoryCursorStore()
	const consumers = 4
	var wg sync.WaitGroup
	errCh := make(chan error, consumers)
	for c := 0; c < consumers; c++ {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			a := newAssembler(s, fmt.Sprintf("m%d", c), 1, cursors, NewViewCache())
			for i := 0; i < 10; i++ {
				as, err := a.Advance(context.Background(), i%2 == 0)
				if err != nil {
					errCh <- err
					return
				}
				if as.Cursor.LastSeq > 50 {
					errCh <- fmt.Errorf("consumer %d cursor %d beyond board 50", c, as.Cursor.LastSeq)
					return
				}
			}
		}(c)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	for c := 0; c < consumers; c++ {
		cur, err := cursors.LoadCursor(BoardShared, fmt.Sprintf("m%d", c))
		if err != nil {
			t.Fatal(err)
		}
		if cur.LastSeq != 50 {
			t.Fatalf("consumer m%d cursor must reach 50, got %d", c, cur.LastSeq)
		}
	}
}
