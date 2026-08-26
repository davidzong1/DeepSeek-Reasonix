package agentruntime

// Effect tests at the durable boundaries (route §5/§7): leader offline never
// wedges completion, the inbox replays at-least-once across a store reopen,
// and a canceled task frees its member for the next one.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/team"
)

// reopenBoard returns a fresh board whose database path survives the close,
// so a second store on the same path simulates a kill/reopen window.
func reopenBoard(t *testing.T) (string, *team.SQLiteStore) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "board.db")
	s, err := team.NewSQLiteStore(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return path, s
}

// TestWakeupFailureDoesNotWedgeCompletion pins the offline leader: a wakeup
// that fails (leader window gone) must not stop the report path — the
// completion lands on the blackboard, which is the durable notice the next
// wake delivers.
func TestWakeupFailureDoesNotWedgeCompletion(t *testing.T) {
	_, board := reopenBoard(t)
	rt, _ := newTestRuntime(t, board)
	rt.AddWakeup(func(reason string) error { return errors.New("leader offline") })
	if err := rt.Start(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusAssigned}, team.Member{ID: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Complete("t1", "done despite offline"); err != nil {
		t.Fatalf("a failing wakeup must not wedge completion: %v", err)
	}
	page, err := board.ReadAfter(context.Background(), "team:T", 0, team.Filter{Stamped: team.Identity{MemberID: "alpha", Generation: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || !strings.Contains(page.Events[1].Summary, "reported") {
		t.Fatalf("the durable report must land despite the wakeup failure, got %+v", page.Events)
	}
}

// TestDrainAtLeastOnceAcrossReopen pins the durable-inbox contract at the
// process boundary: a batch acked before a store reopen never replays, and a
// batch that failed (handler error) replays on the reopened store — exactly
// once in total, never twice, never lost.
func TestDrainAtLeastOnceAcrossReopen(t *testing.T) {
	path, store := reopenBoard(t)
	appendCmd := func(s *team.SQLiteStore, msgID string) {
		t.Helper()
		if _, err := s.Append(context.Background(), team.AppendInput{
			BoardID: "team:T", EventID: msgID, ClientMsgID: msgID, Kind: team.EventCommand,
			TaskID: "t9", Summary: "cmd " + msgID, Stamped: team.Identity{MemberID: "alpha", Generation: 1},
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendCmd(store, "c1")
	appendCmd(store, "c2")

	// First window: a clean drain acks both, then the store closes.
	rt, _ := newTestRuntime(t, store)
	inbox := NewBoardInbox(store, "team:T", "alpha", 1)
	processed := 0
	if _, err := rt.Drain(context.Background(), inbox, 10, func(InboxItem) error { processed++; return nil }); err != nil {
		t.Fatal(err)
	}
	if processed != 2 {
		t.Fatalf("first window processed %d, want 2", processed)
	}
	store.Close()

	// Second window on the same database: the acked batch does not replay.
	store2, err := team.NewSQLiteStore(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store2.Close() })
	rt2, _ := newTestRuntime(t, store2)
	inbox2 := NewBoardInbox(store2, "team:T", "alpha", 1)
	processed2 := 0
	if _, err := rt2.Drain(context.Background(), inbox2, 10, func(InboxItem) error { processed2++; return nil }); err != nil {
		t.Fatal(err)
	}
	if processed2 != 0 {
		t.Fatalf("acked commands must not replay after reopen, got %d", processed2)
	}

	// A third command that fails its handler in window two replays in window
	// three, exactly once.
	appendCmd(store2, "c3")
	if _, err := rt2.Drain(context.Background(), inbox2, 10, func(InboxItem) error { return errors.New("boom") }); err == nil {
		t.Fatal("the failing handler must surface")
	}
	store2.Close()

	store3, err := team.NewSQLiteStore(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store3.Close() })
	rt3, _ := newTestRuntime(t, store3)
	inbox3 := NewBoardInbox(store3, "team:T", "alpha", 1)
	processed3 := 0
	if _, err := rt3.Drain(context.Background(), inbox3, 10, func(InboxItem) error { processed3++; return nil }); err != nil {
		t.Fatal(err)
	}
	if processed3 != 1 {
		t.Fatalf("the failed batch must replay exactly once, got %d", processed3)
	}
}

// TestCancelFreesMemberSlot pins cancel's side effect on the member map: the
// canceled task's member becomes startable again, so a retry does not bounce
// off ErrMemberBusy — cancel is both a stop and a slot release.
func TestCancelFreesMemberSlot(t *testing.T) {
	_, board := reopenBoard(t)
	rt, agents := newTestRuntime(t, board)
	member := team.Member{ID: "alpha"}
	if err := rt.Start(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusAssigned}, member); err != nil {
		t.Fatal(err)
	}
	if err := rt.Cancel("t1"); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background(), team.Task{ID: "t2", Status: team.TaskStatusAssigned}, member); err != nil {
		t.Fatalf("the member slot must be free after cancel: %v", err)
	}
	if len(agents["alpha"].submitted) != 2 {
		t.Fatalf("the second start must submit again, got %d submissions", len(agents["alpha"].submitted))
	}
}
