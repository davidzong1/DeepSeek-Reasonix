package team

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestBlackboardCursorStaleGenerationRejected matches route §2.2: a cursor
// advance from an older window is rejected; a higher generation takes over.
func TestBlackboardCursorStaleGenerationRejected(t *testing.T) {
	s := newTestBoard(t)
	if err := s.AdvanceCursor(context.Background(), CursorUpdate{
		BoardID: BoardShared, ConsumerID: "m", Generation: 5, LastSeq: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceCursor(context.Background(), CursorUpdate{
		BoardID: BoardShared, ConsumerID: "m", Generation: 3, LastSeq: 11,
	}); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale advance: got %v, want ErrStaleGeneration", err)
	}
	if err := s.AdvanceCursor(context.Background(), CursorUpdate{
		BoardID: BoardShared, ConsumerID: "m", Generation: 6, LastSeq: 12,
	}); err != nil {
		t.Fatalf("higher generation rejected: %v", err)
	}
}

// TestBlackboardAppendStampsIdentityUnchanged: the store persists the
// stamped identity verbatim and durably — the server side stamps, the
// store never re-derives or drops it (route §1.2).
func TestBlackboardAppendStampsIdentityUnchanged(t *testing.T) {
	s := newTestBoard(t)
	ev, err := s.Append(context.Background(), AppendInput{
		BoardID: BoardShared, ClientMsgID: "id-1", Kind: EventReport, TaskID: "t",
		CreatedAt: time.Now().UTC(), Summary: "s",
		Stamped: Identity{MemberID: "member-x", Role: "tester", Agent: "claude", Generation: 7},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ev.MemberID != "member-x" || ev.Role != "tester" || ev.Agent != "claude" || ev.Generation != 7 {
		t.Fatalf("stamped identity changed in response: %+v", ev)
	}
	page, err := s.ReadAfter(context.Background(), BoardShared, 0,
		Filter{Stamped: Identity{MemberID: "member-x", Generation: 7}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("want 1 event, got %d", len(page.Events))
	}
	got := page.Events[0]
	if got.MemberID != "member-x" || got.Role != "tester" || got.Agent != "claude" || got.Generation != 7 {
		t.Fatalf("identity not durable: %+v", got)
	}
}
