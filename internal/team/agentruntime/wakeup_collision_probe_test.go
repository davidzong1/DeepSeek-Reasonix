package agentruntime

import (
	"context"
	"strconv"
	"sync"
	"testing"

	"reasonix/internal/team"
)

// TestBoardWakeAppendDoesNotDropConcurrentWakes proves a delivered report
// wake never silently flattens at the board: two Complete wakes — including
// one clock tick — must land as distinct events, never deduped into one.
func TestBoardWakeAppendDoesNotDropConcurrentWakes(t *testing.T) {
	board := newTestBoard(t)
	w := &boardWake{store: board, boardID: "team:T", identity: team.Identity{MemberID: "the-team", Generation: 1}}

	// Two wakes sharing one clock tick must stay distinct events: the runtime
	// wake appends a monotonic sequence to each event id, so a same-tick pair
	// never collides on the board's client-msg-id dedup.
	emitting := []wakeWithTimeCall{
		{now: 1788028130, reason: "task A reported"},
		{now: 1788028130, reason: "task B reported"},
	}
	for _, call := range emitting {
		if err := w.wakeWithTime(call.now, call.reason); err != nil {
			t.Fatal(err)
		}
	}
	page, err := board.ReadAfter(context.Background(), "team:T", 0, team.Filter{
		Kind:    team.EventWakeup,
		Stamped: team.Identity{MemberID: "the-team", Generation: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("wakeup events = %d (%v), want 2 — a same-nanosecond wakeup was dropped", len(page.Events), page.Events)
	}
}

// wakeWithTimeCall is one deterministic wake emission under test, naming the
// wall-clock tick explicitly so a same-tick collision is reproducible.
type wakeWithTimeCall struct {
	now    int64
	reason string
}

// wakeWithTime is boardWake.wake with the clock replaced: the event id derives
// from now plus the monotonic sequence — the same collision-avoidance surface
// the real wake runs.
func (w *boardWake) wakeWithTime(now int64, reason string) error {
	if w.store == nil {
		return nil
	}
	id := "wakeup-" + strconv.FormatInt(now, 10) +
		"-" + strconv.FormatUint(w.calls.Add(1), 10)
	_, err := w.store.Append(context.Background(), team.AppendInput{
		BoardID:     w.boardID,
		EventID:     id,
		ClientMsgID: id,
		Kind:        team.EventWakeup,
		Summary:     reason,
		Stamped:     w.identity,
	})
	return err
}

// TestBoardWakeConcurrentCompleteSurfacesTwoReportWakes is the P2 acceptance
// case end to end: two members reporting in parallel through Runtime.Complete
// must land two distinct leader wakes — never one flattened notice.
func TestBoardWakeConcurrentCompleteSurfacesTwoReportWakes(t *testing.T) {
	board := newTestBoard(t)
	rt, _ := newTestRuntime(t, board)
	var wakes sync.Map
	rt.AddWakeup(NewBoardWake(board, "team:T", team.Identity{MemberID: "the-team", Generation: 1}))
	rt.AddWakeup(func(reason string) error {
		wakes.Store(reason, true)
		return nil
	})
	if board, ok := board.(*team.SQLiteStore); ok {
		defer board.Close()
	}

	for _, m := range []string{"alpha", "beta"} {
		if err := rt.Start(context.Background(), team.Task{ID: team.TaskID(m), Status: team.TaskStatusAssigned}, team.Member{ID: m}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for _, m := range []string{"alpha", "beta"} {
		wg.Add(1)
		go func(m string) {
			defer wg.Done()
			if err := rt.Complete(team.TaskID(m), "done "+m); err != nil {
				t.Errorf("complete %s: %v", m, err)
			}
		}(m)
	}
	wg.Wait()

	if _, ok := wakes.Load("task alpha reported"); !ok {
		t.Error("alpha's report wake was lost")
	}
	if _, ok := wakes.Load("task beta reported"); !ok {
		t.Error("beta's report wake was lost")
	}
	page, err := board.ReadAfter(context.Background(), "team:T", 0, team.Filter{
		Kind:    team.EventWakeup,
		Stamped: team.Identity{MemberID: "the-team", Generation: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("leader wakeups = %d (%v), want 2 — concurrent report wakes flattened", len(page.Events), page.Events)
	}
}
