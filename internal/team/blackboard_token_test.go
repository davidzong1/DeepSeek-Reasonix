package team

import (
	"context"
	"fmt"
	"testing"
)

// TestBlackboardDigestAlwaysPresent: every stored event carries a digest
// (route §1.1), so readers compare revisions without touching payloads.
func TestBlackboardDigestAlwaysPresent(t *testing.T) {
	s := newTestBoard(t)
	for i := 0; i < 10; i++ {
		ev, err := boardAppend(s, fmt.Sprintf("d-%d", i), "m", 1)
		if err != nil {
			t.Fatal(err)
		}
		if ev.Digest == "" {
			t.Fatalf("event %d has empty digest", ev.Seq)
		}
	}
}

// TestBlackboardReadAfterDeltaProportional: paging a 120-event board at
// limit 10 serves every event exactly once, no duplicates, no gaps (route
// §2.3: reads scale with the delta, not the archive).
func TestBlackboardReadAfterDeltaProportional(t *testing.T) {
	s := newTestBoard(t)
	const total = 120
	for i := 0; i < total; i++ {
		if _, err := boardAppend(s, fmt.Sprintf("p-%d", i), "m", 1); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[int64]bool{}
	after := int64(0)
	for pages := 0; pages < 30; pages++ {
		page, err := s.ReadAfter(context.Background(), BoardShared, after,
			Filter{Limit: 10, Stamped: Identity{MemberID: "m", Generation: 1}})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Events) > 10 {
			t.Fatalf("page of %d exceeds limit 10", len(page.Events))
		}
		for _, ev := range page.Events {
			if seen[ev.Seq] {
				t.Fatalf("seq %d served twice", ev.Seq)
			}
			seen[ev.Seq] = true
		}
		if !page.HasMore {
			break
		}
		after = page.NextSeq
	}
	if len(seen) != total {
		t.Fatalf("saw %d events, want %d", len(seen), total)
	}
}

// TestBlackboardAdvanceSkipsSeenEvents: after a cursor advance, nothing at
// or below the cursor is served again (route §3.2: no re-injection).
func TestBlackboardAdvanceSkipsSeenEvents(t *testing.T) {
	s := newTestBoard(t)
	for i := 0; i < 20; i++ {
		if _, err := boardAppend(s, fmt.Sprintf("c-%d", i), "m", 1); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ReadAfter(context.Background(), BoardShared, 0,
		Filter{Limit: 5, Stamped: Identity{MemberID: "m", Generation: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 5 {
		t.Fatalf("first page has %d events, want 5", len(page.Events))
	}
	last := page.Events[len(page.Events)-1].Seq
	if err := s.AdvanceCursor(context.Background(), CursorUpdate{
		BoardID: BoardShared, ConsumerID: "m", Generation: 1, LastSeq: last,
	}); err != nil {
		t.Fatal(err)
	}
	next, err := s.ReadAfter(context.Background(), BoardShared, last,
		Filter{Limit: 100, Stamped: Identity{MemberID: "m", Generation: 1}})
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range next.Events {
		if ev.Seq <= last {
			t.Fatalf("re-served seq %d at or below cursor %d", ev.Seq, last)
		}
	}
}
