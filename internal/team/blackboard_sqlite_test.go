package team

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestBoard(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func boardAppend(s *SQLiteStore, msgID, member string, gen uint64) (BoardEvent, error) {
	return s.Append(context.Background(), AppendInput{
		BoardID:     BoardShared,
		ClientMsgID: msgID,
		Kind:        EventReport,
		TaskID:      TaskID("t1"),
		CreatedAt:   time.Now().UTC(),
		Summary:     "summary " + msgID,
		Stamped:     Identity{MemberID: member, Role: "coder", Agent: "claude", Generation: gen},
	})
}

// TestBlackboardConcurrentAppendNoTears matches route §2.4: 10 writers ×
// 100 events produce seq 1..1000 with no gaps, no duplicates, and fully
// stamped identity.
func TestBlackboardConcurrentAppendNoTears(t *testing.T) {
	s := newTestBoard(t)
	const writers, per = 10, 100
	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				_, err := s.Append(context.Background(), AppendInput{
					BoardID: BoardShared, ClientMsgID: fmt.Sprintf("w%d-%d", w, i),
					Kind: EventReport, TaskID: "t1", Summary: "s",
					Stamped: Identity{MemberID: fmt.Sprintf("m%d", w), Generation: 1},
				})
				if err != nil {
					errCh <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	page, err := s.ReadAfter(context.Background(), BoardShared, 0, Filter{
		Limit:   2000,
		Stamped: Identity{MemberID: "m0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != writers*per {
		t.Fatalf("want %d events, got %d", writers*per, len(page.Events))
	}
	seen := make(map[string]bool)
	owner := make(map[string]string)
	for w := 0; w < writers; w++ {
		for i := 0; i < per; i++ {
			owner[fmt.Sprintf("w%d-%d", w, i)] = fmt.Sprintf("m%d", w)
		}
	}
	for i, ev := range page.Events {
		if ev.Seq != int64(i+1) {
			t.Fatalf("seq tear at %d: got %d", i, ev.Seq)
		}
		if seen[ev.ClientMsgID] {
			t.Fatalf("duplicate event %s", ev.ClientMsgID)
		}
		seen[ev.ClientMsgID] = true
		if ev.MemberID != owner[ev.ClientMsgID] || ev.Generation != 1 {
			t.Fatalf("identity not stamped on %s: %+v", ev.EventID, ev)
		}
		if ev.SchemaVersion != SchemaVersion || len(ev.Digest) != 32 {
			t.Fatalf("envelope incomplete on %s", ev.EventID)
		}
	}
}

// TestBlackboardPaging verifies limit + has_more + next_seq continuation.
func TestBlackboardPaging(t *testing.T) {
	s := newTestBoard(t)
	for i := 0; i < 250; i++ {
		if _, err := boardAppend(s, fmt.Sprintf("p%d", i), "m1", 1); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ReadAfter(context.Background(), BoardShared, 0, Filter{
		Limit: 100, Stamped: Identity{MemberID: "m1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 100 || !page.HasMore || page.NextSeq != 100 {
		t.Fatalf("bad first page: n=%d more=%v next=%d", len(page.Events), page.HasMore, page.NextSeq)
	}
	next, err := s.ReadAfter(context.Background(), BoardShared, page.NextSeq, Filter{
		Limit: 200, Stamped: Identity{MemberID: "m1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Events) != 150 || next.HasMore || next.NextSeq != 250 {
		t.Fatalf("bad last page: n=%d more=%v next=%d", len(next.Events), next.HasMore, next.NextSeq)
	}
}

// TestBlackboardIdempotentReplay matches route §2.4: N replays of one
// client_msg_id produce exactly one event with a stable seq.
func TestBlackboardIdempotentReplay(t *testing.T) {
	s := newTestBoard(t)
	first, err := boardAppend(s, "dup-1", "m1", 1)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := boardAppend(s, "dup-1", "m1", 1)
		if err != nil {
			t.Fatal(err)
		}
		if again.Seq != first.Seq || again.EventID != first.EventID || again.Digest != first.Digest {
			t.Fatalf("replay diverged: %+v vs %+v", again, first)
		}
	}
	page, _ := s.ReadAfter(context.Background(), BoardShared, 0, Filter{Stamped: Identity{MemberID: "m1"}})
	if len(page.Events) != 1 {
		t.Fatalf("want 1 event after replays, got %d", len(page.Events))
	}
}

// TestBlackboardConclusionCASMatchesRoute matches route §2.2/§2.4:
// concurrent revisions of one topic have exactly one winner; losers get
// ErrConflict carrying the current epoch.
func TestBlackboardConclusionCASMatchesRoute(t *testing.T) {
	s := newTestBoard(t)
	const racers = 10
	var wg sync.WaitGroup
	wins, conflicts := make(chan int, racers), make(chan error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Append(context.Background(), AppendInput{
				BoardID: BoardShared, ClientMsgID: fmt.Sprintf("cas-%d", i),
				Kind: EventConclusion, TaskID: "t1", Summary: "c",
				Stamped:    Identity{MemberID: "m1", Generation: 1},
				Conclusion: &ConclusionUpdate{Topic: "topic-a", BaseEpoch: 0, Summary: fmt.Sprintf("rev %d", i)},
			})
			if err == nil {
				wins <- i
				return
			}
			conflicts <- err
		}(i)
	}
	wg.Wait()
	close(wins)
	close(conflicts)
	if got := len(wins); got != 1 {
		t.Fatalf("want exactly 1 CAS winner, got %d", got)
	}
	for err := range conflicts {
		var ce *ErrConflict
		if !errors.As(err, &ce) {
			t.Fatalf("loser got %v, want ErrConflict", err)
		}
		if ce.Topic != "topic-a" || ce.CurrentEpoch != 1 || ce.CurrentSeq == 0 {
			t.Fatalf("conflict info wrong: %+v", ce)
		}
	}
}

// TestBlackboardConclusionRevisionEpoch verifies base_epoch matching:
// a revision against the current epoch succeeds and bumps it, a stale
// base_epoch conflicts.
func TestBlackboardConclusionRevisionEpoch(t *testing.T) {
	s := newTestBoard(t)
	rev := func(base uint64, msg string) error {
		_, err := s.Append(context.Background(), AppendInput{
			BoardID: BoardShared, ClientMsgID: "rev-" + msg,
			Kind: EventConclusion, TaskID: "t1", Summary: "c",
			Stamped:    Identity{MemberID: "m1", Generation: 1},
			Conclusion: &ConclusionUpdate{Topic: "topic-b", BaseEpoch: base, Summary: msg},
		})
		return err
	}
	if err := rev(0, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := rev(1, "v2"); err != nil {
		t.Fatal(err)
	}
	err := rev(1, "v3-stale")
	var ce *ErrConflict
	if !errors.As(err, &ce) || ce.CurrentEpoch != 2 {
		t.Fatalf("stale revision want ErrConflict(epoch 2), got %v", err)
	}
	view, err := s.ReadView(context.Background(), BoardShared, ViewSpec{TaskID: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Conclusions) != 1 || view.Conclusions[0].Epoch != 2 || view.Conclusions[0].Summary != "v2" {
		t.Fatalf("view wrong: %+v", view)
	}
}

// TestBlackboardCursorIsolationAndMonotonic matches route §2.4: 10
// consumers advance independent cursors without covering each other;
// backwards advance and stale generation are rejected.
func TestBlackboardCursorIsolationAndMonotonic(t *testing.T) {
	s := newTestBoard(t)
	const consumers = 10
	var wg sync.WaitGroup
	errCh := make(chan error, consumers)
	for i := 0; i < consumers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if err := s.AdvanceCursor(context.Background(), CursorUpdate{
					BoardID: BoardShared, ConsumerID: fmt.Sprintf("c%d", i),
					Generation: 1, LastSeq: int64(j*3 + 2),
				}); err != nil {
					errCh <- err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	if err := s.AdvanceCursor(context.Background(), CursorUpdate{
		BoardID: BoardShared, ConsumerID: "c0", Generation: 1, LastSeq: 1,
	}); !errors.Is(err, ErrCursorBackwards) {
		t.Fatalf("backwards advance want ErrCursorBackwards, got %v", err)
	}
	if err := s.AdvanceCursor(context.Background(), CursorUpdate{
		BoardID: BoardShared, ConsumerID: "c0", Generation: 0, LastSeq: 99,
	}); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale generation want ErrStaleGeneration, got %v", err)
	}
}

// TestBlackboardWALRecovery matches route §2.4: committed events survive a
// close/reopen; a rolled-back transaction never appears.
func TestBlackboardWALRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.db")
	s1, err := NewSQLiteStore(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := boardAppend(s1, fmt.Sprintf("r%d", i), "m1", 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := NewSQLiteStore(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	page, err := s2.ReadAfter(context.Background(), BoardShared, 0, Filter{Stamped: Identity{MemberID: "m1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 3 {
		t.Fatalf("committed events lost after reopen: got %d", len(page.Events))
	}

	// A failed transaction must leave no trace.
	err = s2.inTx(context.Background(), func(conn *sql.Conn) error { return errors.New("boom") })
	if err == nil {
		t.Fatal("want rollback error")
	}
	page, err = s2.ReadAfter(context.Background(), BoardShared, 0, Filter{Stamped: Identity{MemberID: "m1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 3 {
		t.Fatalf("rolled-back write leaked: got %d", len(page.Events))
	}
}

// TestBlackboardPrivateAccess matches route §6.2: a private board accepts
// only its owner; cross-member access is forbidden and reveals nothing.
func TestBlackboardPrivateAccess(t *testing.T) {
	s := newTestBoard(t)
	alice := Identity{MemberID: "alice", Role: "coder", Agent: "claude", Generation: 1}
	if _, err := s.Append(context.Background(), AppendInput{
		BoardID: BoardPrivatePrefix + "alice", ClientMsgID: "a1",
		Kind: EventCheckpoint, TaskID: "t1", Summary: "private",
		Stamped: alice,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(context.Background(), AppendInput{
		BoardID: BoardPrivatePrefix + "alice", ClientMsgID: "b1",
		Kind: EventReport, TaskID: "t1", Summary: "sneak",
		Stamped: Identity{MemberID: "bob", Generation: 1},
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bob writing alice's board want ErrForbidden, got %v", err)
	}
	if _, err := s.ReadAfter(context.Background(), BoardPrivatePrefix+"alice", 0, Filter{
		Stamped: Identity{MemberID: "bob"},
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bob reading alice's board want ErrForbidden, got %v", err)
	}
	page, err := s.ReadAfter(context.Background(), BoardPrivatePrefix+"alice", 0, Filter{Stamped: alice})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].MemberID != "alice" {
		t.Fatalf("owner read wrong: %+v", page.Events)
	}
}

// TestBlackboardArchiveResync matches route §2.3: after archiving, a
// cursor inside the hole gets NeedResync; logical seqs are not renumbered.
func TestBlackboardArchiveResync(t *testing.T) {
	s := newTestBoard(t)
	for i := 0; i < 10; i++ {
		if _, err := boardAppend(s, fmt.Sprintf("a%d", i), "m1", 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ArchiveBefore(context.Background(), BoardShared, 5, Identity{MemberID: "leader", Role: "leader", Generation: 1}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ReadAfter(context.Background(), BoardShared, 5, Filter{Stamped: Identity{MemberID: "m1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !page.NeedResync {
		t.Fatal("cursor in archived hole must request resync")
	}
	if len(page.Events) != 5 || page.Events[0].Seq != 6 {
		t.Fatalf("archived events leaked into read: %+v", page.Events)
	}
	fresh, err := s.ReadAfter(context.Background(), BoardShared, 0, Filter{Stamped: Identity{MemberID: "m1"}})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.NeedResync {
		t.Fatal("fresh cursor must not resync")
	}
	if len(fresh.Events) != 5 {
		t.Fatalf("want 5 live events, got %d", len(fresh.Events))
	}
}

// TestBlackboardSupersedeChainsAudit verifies supersede writes a
// replacement chaining the target seqs while the originals stay readable.
func TestBlackboardSupersedeChainsAudit(t *testing.T) {
	s := newTestBoard(t)
	for i := 0; i < 2; i++ {
		if _, err := boardAppend(s, fmt.Sprintf("s%d", i), "m1", 1); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := s.Supersede(context.Background(), BoardShared, []int64{1, 2}, AppendInput{
		ClientMsgID: "sup-1", Kind: EventSupersede, TaskID: "t1", Summary: "replacement",
		Stamped: Identity{MemberID: "m1", Generation: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Supersedes) != 2 || rep.Supersedes[0] != 1 || rep.Supersedes[1] != 2 {
		t.Fatalf("replacement did not chain targets: %+v", rep)
	}
	page, _ := s.ReadAfter(context.Background(), BoardShared, 0, Filter{Stamped: Identity{MemberID: "m1"}})
	if len(page.Events) != 3 {
		t.Fatalf("audit history lost: want 3, got %d", len(page.Events))
	}
	if _, err := s.Supersede(context.Background(), BoardShared, []int64{99}, AppendInput{
		ClientMsgID: "sup-2", Kind: EventSupersede, TaskID: "t1", Summary: "x",
		Stamped: Identity{MemberID: "m1", Generation: 1},
	}); err == nil {
		t.Fatal("superseding a missing seq must fail")
	}
}

// TestBlackboardCheckGeneration gates writes from older windows: the
// member's max persisted generation (binding or event history) rejects
// stale claims; unbound members with no history pass.
func TestBlackboardCheckGeneration(t *testing.T) {
	s := newTestBoard(t)
	if err := s.CheckGeneration(context.Background(), BoardShared, "m1", 1); err != nil {
		t.Fatalf("first write must pass: %v", err)
	}
	if _, err := boardAppend(s, "g1", "m1", 2); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckGeneration(context.Background(), BoardShared, "m1", 1); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale claim want ErrStaleGeneration, got %v", err)
	}
	if err := s.CheckGeneration(context.Background(), BoardShared, "m1", 2); err != nil {
		t.Fatalf("current generation must pass: %v", err)
	}
	if err := s.SaveBinding(context.Background(), BindRecord{
		MemberID: "m2", LeaderID: "leader", Generation: 5, Status: BindStatusBound, TaskID: "t1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckGeneration(context.Background(), BoardShared, "m2", 4); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("binding generation must gate, got %v", err)
	}
	if err := s.CheckGeneration(context.Background(), BoardShared, "m2", 5); err != nil {
		t.Fatalf("binding generation 5 must pass: %v", err)
	}
}

// TestBlackboardBatchRollback verifies a CAS conflict inside AppendBatch
// rolls the whole batch back, leaving no orphan events.
func TestBlackboardBatchRollback(t *testing.T) {
	s := newTestBoard(t)
	_, err := s.AppendBatch(context.Background(), []AppendInput{
		{
			BoardID: BoardShared, ClientMsgID: "b1", Kind: EventReport, TaskID: "t1",
			Summary: "1", Stamped: Identity{MemberID: "m1", Generation: 1},
			Conclusion: &ConclusionUpdate{Topic: "t", BaseEpoch: 0, Summary: "v1"},
		},
		{
			BoardID: BoardShared, ClientMsgID: "b2", Kind: EventReport, TaskID: "t1",
			Summary: "2", Stamped: Identity{MemberID: "m1", Generation: 1},
			Conclusion: &ConclusionUpdate{Topic: "t", BaseEpoch: 0, Summary: "v2"},
		},
	})
	var ce *ErrConflict
	if !errors.As(err, &ce) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	page, _ := s.ReadAfter(context.Background(), BoardShared, 0, Filter{Stamped: Identity{MemberID: "m1"}})
	if len(page.Events) != 0 {
		t.Fatalf("failed batch leaked events: %d", len(page.Events))
	}
}
