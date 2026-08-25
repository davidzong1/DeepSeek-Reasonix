package team

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestBlackboardConcurrentReadWriteIsolation matches route §2.4: readers
// never observe a torn event, a duplicate, or a seq outside the total
// write range — every event served is a committed fact.
func TestBlackboardConcurrentReadWriteIsolation(t *testing.T) {
	s := newTestBoard(t)
	const writers = 10
	const perWriter = 50
	const total = writers * perWriter

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if _, err := boardAppend(s, fmt.Sprintf("w%d-%d", w, i), fmt.Sprintf("m%d", w), 1); err != nil {
					t.Errorf("append: %v", err)
					return
				}
			}
		}(w)
	}

	// Read-after-read: a page is monotonic, every event is fully stamped,
	// and seqs stay within the committed range [1, total].
	var wgR sync.WaitGroup
	wgR.Add(1)
	go func() {
		defer wgR.Done()
		after := int64(0)
		for {
			select {
			case <-stop:
				return
			default:
			}
			page, err := s.ReadAfter(context.Background(), BoardShared, after,
				Filter{Stamped: Identity{MemberID: "reader", Generation: 1}})
			if err != nil {
				t.Errorf("read: %v", err)
				return
			}
			prev := after
			for _, ev := range page.Events {
				if ev.MemberID == "" || ev.Digest == "" || ev.Seq == 0 {
					t.Errorf("torn event: %+v", ev)
				}
				if ev.Seq <= prev {
					t.Errorf("page not monotonic: %d after %d", ev.Seq, prev)
				}
				if ev.Seq > total {
					t.Errorf("seq %d beyond total write range %d", ev.Seq, total)
				}
				prev = ev.Seq
			}
			if len(page.Events) > 0 {
				after = page.Events[len(page.Events)-1].Seq
			}
		}
	}()

	wg.Wait()
	close(stop)
	wgR.Wait()
}

// TestBlackboardConcurrentSameMsgIDSingleWinner: concurrent replays of one
// client_msg_id store exactly one event — every caller gets the original
// back, no duplicate row is ever written (route §2.2).
func TestBlackboardConcurrentSameMsgIDSingleWinner(t *testing.T) {
	s := newTestBoard(t)
	const n = 20
	var wg sync.WaitGroup
	seqs := make(chan int64, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ev, err := s.Append(context.Background(), AppendInput{
				BoardID: BoardShared, ClientMsgID: "same-id", Kind: EventReport,
				TaskID: "t", CreatedAt: time.Now().UTC(), Summary: "s",
				Stamped: Identity{MemberID: "m", Generation: 1},
			})
			if err != nil {
				t.Errorf("append: %v", err)
				return
			}
			seqs <- ev.Seq
		}()
	}
	wg.Wait()
	close(seqs)
	unique := map[int64]bool{}
	for seq := range seqs {
		unique[seq] = true
	}
	if len(unique) != 1 {
		t.Fatalf("replays returned %d distinct seqs, want 1", len(unique))
	}
	page, err := s.ReadAfter(context.Background(), BoardShared, 0,
		Filter{Stamped: Identity{MemberID: "m", Generation: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("stored %d events, want 1", len(page.Events))
	}
}

// TestBlackboardConcurrentCursorsIndependent: five consumers advance their
// own cursors concurrently without cross-talk (route §2.2).
func TestBlackboardConcurrentCursorsIndependent(t *testing.T) {
	s := newTestBoard(t)
	for i := 0; i < 50; i++ {
		if _, err := boardAppend(s, fmt.Sprintf("seed-%d", i), "m", 1); err != nil {
			t.Fatal(err)
		}
	}
	const consumers = 5
	var wg sync.WaitGroup
	for c := 0; c < consumers; c++ {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			id := fmt.Sprintf("consumer-%d", c)
			last := int64(0)
			for i := 0; i < 20; i++ {
				last += int64(i%5 + 1)
				if err := s.AdvanceCursor(context.Background(), CursorUpdate{
					BoardID: BoardShared, ConsumerID: id, Generation: 1, LastSeq: last,
				}); err != nil {
					t.Errorf("cursor %s advance to %d: %v", id, last, err)
					return
				}
			}
		}(c)
	}
	wg.Wait()
}

// TestBlackboardConcurrentSameCursorMonotonic: racing advances on one
// cursor may lose with ErrCursorBackwards but never corrupt it: the final
// state must accept the max position and reject a step below it.
func TestBlackboardConcurrentSameCursorMonotonic(t *testing.T) {
	s := newTestBoard(t)
	for i := 0; i < 10; i++ {
		if _, err := boardAppend(s, fmt.Sprintf("seed-%d", i), "m", 1); err != nil {
			t.Fatal(err)
		}
	}
	advance := func(seq int64) error {
		return s.AdvanceCursor(context.Background(), CursorUpdate{
			BoardID: BoardShared, ConsumerID: "one", Generation: 1, LastSeq: seq})
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				_ = advance(int64(g*10 + i + 1)) // intermediate losses are fine
			}
		}(g)
	}
	wg.Wait()
	if err := advance(80); err != nil {
		t.Fatalf("final advance to max failed: %v", err)
	}
	if err := advance(40); !errors.Is(err, ErrCursorBackwards) {
		t.Fatalf("backwards advance: got %v, want ErrCursorBackwards", err)
	}
}

// TestBlackboardConcurrentSupersedeWithAppend: supersede racing with new
// appends keeps seq unique and the audit chain intact — superseded events
// stay readable and every revision is one additional event.
func TestBlackboardConcurrentSupersedeWithAppend(t *testing.T) {
	s := newTestBoard(t)
	var target int64
	for i := 0; i < 5; i++ {
		ev, err := boardAppend(s, fmt.Sprintf("seed-%d", i), "m", 1)
		if err != nil {
			t.Fatal(err)
		}
		target = ev.Seq
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			if _, err := boardAppend(s, fmt.Sprintf("app-%d", i), "m", 1); err != nil {
				t.Errorf("append: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_, err := s.Supersede(context.Background(), BoardShared, []int64{target}, AppendInput{
				ClientMsgID: fmt.Sprintf("sup-%d", i), Kind: EventSupersede, TaskID: "t",
				CreatedAt: time.Now().UTC(), Summary: "revised",
				Stamped: Identity{MemberID: "m", Generation: 1},
			})
			if err != nil {
				t.Errorf("supersede: %v", err)
				return
			}
		}
	}()
	wg.Wait()

	page, err := s.ReadAfter(context.Background(), BoardShared, 0,
		Filter{Limit: 1000, Stamped: Identity{MemberID: "m", Generation: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 5+20+20 {
		t.Fatalf("stored %d events, want 45", len(page.Events))
	}
	seen := make(map[int64]bool, len(page.Events))
	audit := false
	for _, ev := range page.Events {
		if seen[ev.Seq] {
			t.Fatalf("duplicate seq %d", ev.Seq)
		}
		seen[ev.Seq] = true
		if ev.Seq == target {
			audit = true // superseded event must still be readable
		}
	}
	if !audit {
		t.Fatal("superseded target event missing from audit history")
	}
}
