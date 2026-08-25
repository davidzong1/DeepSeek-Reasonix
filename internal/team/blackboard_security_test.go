package team

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestBlackboardPrivateBoardAccessDenied matches route §6.2: a non-owner
// can neither read nor write another member's private board; the attempt
// leaks nothing (owner still sees exactly their own events).
func TestBlackboardPrivateBoardAccessDenied(t *testing.T) {
	s := newTestBoard(t)
	owner := Identity{MemberID: "alice", Generation: 1}
	other := Identity{MemberID: "bob", Generation: 1}
	board := BoardPrivatePrefix + "alice"

	if _, err := s.Append(context.Background(), AppendInput{
		BoardID: board, ClientMsgID: "a1", Kind: EventReport, TaskID: "t",
		CreatedAt: time.Now().UTC(), Summary: "secret", Stamped: owner,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(context.Background(), AppendInput{
		BoardID: board, ClientMsgID: "a2", Kind: EventReport, TaskID: "t",
		CreatedAt: time.Now().UTC(), Summary: "intrusion", Stamped: other,
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-member append: got %v, want ErrForbidden", err)
	}
	if _, err := s.ReadAfter(context.Background(), board, 0, Filter{Stamped: other}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-member read: got %v, want ErrForbidden", err)
	}
	page, err := s.ReadAfter(context.Background(), board, 0, Filter{Stamped: owner})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("owner sees %d events after intrusion attempt, want 1", len(page.Events))
	}
}

// TestBlackboardNonOwnerSupersedeDenied: supersede on a private board is
// owner-gated like every other write (route §6.2).
func TestBlackboardNonOwnerSupersedeDenied(t *testing.T) {
	s := newTestBoard(t)
	owner := Identity{MemberID: "alice", Generation: 1}
	board := BoardPrivatePrefix + "alice"
	ev, err := s.Append(context.Background(), AppendInput{
		BoardID: board, ClientMsgID: "a1", Kind: EventReport, TaskID: "t",
		CreatedAt: time.Now().UTC(), Summary: "s", Stamped: owner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Supersede(context.Background(), board, []int64{ev.Seq}, AppendInput{
		ClientMsgID: "s1", Kind: EventSupersede, TaskID: "t", CreatedAt: time.Now().UTC(),
		Summary: "x", Stamped: Identity{MemberID: "bob", Generation: 1},
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-member supersede: got %v, want ErrForbidden", err)
	}
}

// TestBlackboardNonOwnerArchiveDenied: archiving is a destructive write
// gated by the management channel (route §6.2): a non-leader is rejected
// with ErrForbidden.
func TestBlackboardNonOwnerArchiveDenied(t *testing.T) {
	s := newTestBoard(t)
	owner := Identity{MemberID: "alice", Generation: 1}
	board := BoardPrivatePrefix + "alice"
	if _, err := s.Append(context.Background(), AppendInput{
		BoardID: board, ClientMsgID: "a1", Kind: EventReport, TaskID: "t",
		CreatedAt: time.Now().UTC(), Summary: "s", Stamped: owner,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveBefore(context.Background(), board, 1000, Identity{MemberID: "bob", Generation: 1}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-member archive: got %v, want ErrForbidden", err)
	}
}

// TestBlackboardSharedBoardRequiresStampedIdentity: even a shared board
// rejects unauthenticated writes — fail-closed (route §6.2).
func TestBlackboardSharedBoardRequiresStampedIdentity(t *testing.T) {
	s := newTestBoard(t)
	if _, err := s.Append(context.Background(), AppendInput{
		BoardID: BoardShared, ClientMsgID: "anon", Kind: EventReport, TaskID: "t",
		CreatedAt: time.Now().UTC(), Summary: "s",
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("anonymous append: got %v, want ErrForbidden", err)
	}
}

// TestPrivateBoardHelper: the prefix helper is the single classification
// point; malformed prefixes must not classify as private.
func TestPrivateBoardHelper(t *testing.T) {
	for _, tc := range []struct {
		board  string
		owner  string
		isPriv bool
	}{
		{"private/alice", "alice", true},
		{"private/", "", false},
		{"private", "", false},
		{"shared", "", false},
	} {
		owner, ok := PrivateBoard(tc.board)
		if ok != tc.isPriv || owner != tc.owner {
			t.Fatalf("PrivateBoard(%q) = (%q,%v), want (%q,%v)", tc.board, owner, ok, tc.owner, tc.isPriv)
		}
	}
}
