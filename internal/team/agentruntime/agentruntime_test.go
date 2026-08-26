package agentruntime

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/team"
)

// stubAgent records the narrow AgentAPI surface the runtime drives.
type stubAgent struct {
	submitted []string
	canceled  bool
}

func (s *stubAgent) Submit(input string)                  { s.submitted = append(s.submitted, input) }
func (s *stubAgent) SubmitUserTurn(input, display string) { s.submitted = append(s.submitted, input) }
func (s *stubAgent) Cancel()                              { s.canceled = true }
func (s *stubAgent) Running() bool                        { return s.canceled == false && len(s.submitted) > 0 }
func (s *stubAgent) Turn() int                            { return len(s.submitted) }
func (s *stubAgent) Compose(text string) string           { return text }
func (s *stubAgent) Close()                               {}

func newTestBoard(t *testing.T) team.BoardStore {
	t.Helper()
	store, err := team.NewSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// newTestRuntime wires a runtime over a fresh board with one stub agent per
// member; the blackboard identity is stamped from the member id.
func newTestRuntime(t *testing.T, board team.BoardStore) (*Runtime, map[string]*stubAgent) {
	t.Helper()
	agents := map[string]*stubAgent{}
	rt := NewRuntime(func(memberID string) (AgentAPI, error) {
		if _, ok := agents[memberID]; !ok {
			agents[memberID] = &stubAgent{}
		}
		return agents[memberID], nil
	}, board, "team:T", func(memberID string) team.Identity {
		return team.Identity{MemberID: memberID, Generation: 1}
	})
	return rt, agents
}

// TestInjectTaskChainOrder pins the §7 assembly: task context, then inbox
// commands, then board view, each with structured task/generation labels.
func TestInjectTaskChainOrder(t *testing.T) {
	ac := InjectTask(team.Task{ID: "t1", Desc: "do it", Expected: "green", ContextRef: "ctx/t1"},
		[]InboxItem{{TaskID: "t2", Summary: "follow up", Generation: 3, Seq: 7}},
		"board summary")
	if !strings.HasPrefix(ac.Text, "[task: t1]\ndo it\n[expected] green") {
		t.Fatalf("task block first, got %q", ac.Text)
	}
	ti := strings.Index(ac.Text, "[command inbox] (generation 3)")
	bi := strings.Index(ac.Text, "[board view]")
	if ti < 0 || bi < 0 || ti > bi {
		t.Fatalf("inbox must precede board view, got %q", ac.Text)
	}
	if len(ac.Links) != 3 || ac.Links[1].Name != "inbox" {
		t.Fatalf("links = %+v, want task/inbox/board", ac.Links)
	}
	if ac.Links[1].Ref != "board:seq:7-7" {
		t.Fatalf("inbox ref = %q, want the seq range", ac.Links[1].Ref)
	}
}

// TestRuntimeStartSubmitsInjected pins the real execution contract: the
// injected task context reaches the member's agent and the blackboard
// records the move to running.
func TestRuntimeStartSubmitsInjected(t *testing.T) {
	board := newTestBoard(t)
	rt, agents := newTestRuntime(t, board)
	member := team.Member{ID: "alpha", Role: team.RoleCoder, State: team.MemberStateIdle}
	if err := rt.Start(context.Background(), team.Task{ID: "t1", Desc: "build it", Status: team.TaskStatusAssigned}, member); err != nil {
		t.Fatal(err)
	}
	if len(agents["alpha"].submitted) != 1 || !strings.Contains(agents["alpha"].submitted[0], "[task: t1]") {
		t.Fatalf("agent input = %q, want the injected task context", agents["alpha"].submitted)
	}
	page, err := board.ReadAfter(context.Background(), "team:T", 0, team.Filter{Stamped: team.Identity{MemberID: "alpha", Generation: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].Kind != team.EventAssignment || !strings.Contains(page.Events[0].Summary, "running") {
		t.Fatalf("board events = %+v, want one running assignment", page.Events)
	}
}

func TestRuntimeStartBusyMember(t *testing.T) {
	rt, _ := newTestRuntime(t, nil)
	member := team.Member{ID: "alpha"}
	ok := team.Task{ID: "t1", Status: team.TaskStatusAssigned}
	if err := rt.Start(context.Background(), ok, member); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background(), team.Task{ID: "t2", Status: team.TaskStatusAssigned}, member); !errors.Is(err, ErrMemberBusy) {
		t.Fatalf("second start on alpha = %v, want ErrMemberBusy", err)
	}
}

// TestRuntimeStartRefusesIllegalState: a task that already left the runnable
// states cannot be started — the migration map is the gate.
func TestRuntimeStartRefusesIllegalState(t *testing.T) {
	rt, _ := newTestRuntime(t, nil)
	err := rt.Start(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusReported}, team.Member{ID: "alpha"})
	if err == nil || !strings.Contains(err.Error(), "invalid task transition") {
		t.Fatalf("err = %v, want invalid transition", err)
	}
}

// TestRuntimeCancelStopsAgentAndWakes pins cancel: the agent stops, the
// blackboard records canceled, and the leader wakeup fires.
func TestRuntimeCancelStopsAgentAndWakes(t *testing.T) {
	board := newTestBoard(t)
	rt, agents := newTestRuntime(t, board)
	var wakes []string
	rt.AddWakeup(func(reason string) error { wakes = append(wakes, reason); return nil })
	member := team.Member{ID: "alpha"}
	if err := rt.Start(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusAssigned}, member); err != nil {
		t.Fatal(err)
	}
	if err := rt.Cancel("t1"); err != nil {
		t.Fatal(err)
	}
	if !agents["alpha"].canceled {
		t.Fatal("agent backend must be canceled")
	}
	if len(wakes) != 1 || !strings.Contains(wakes[0], "canceled") {
		t.Fatalf("wakeups = %v, want one cancel notice", wakes)
	}
	if err := rt.Cancel("t1"); !errors.Is(err, ErrTaskUnknown) {
		t.Fatalf("re-cancel = %v, want ErrTaskUnknown (entry dropped)", err)
	}
}

// TestRuntimeCompleteReportsAndWakes pins the report path's single
// migration point: running -> reported on the shared map, a blackboard
// report event, and a leader wakeup.
func TestRuntimeCompleteReportsAndWakes(t *testing.T) {
	board := newTestBoard(t)
	rt, _ := newTestRuntime(t, board)
	var wakes []string
	rt.AddWakeup(func(reason string) error { wakes = append(wakes, reason); return nil })
	if err := rt.Start(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusAssigned}, team.Member{ID: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Complete("t1", "done green"); err != nil {
		t.Fatal(err)
	}
	if len(wakes) != 1 || !strings.Contains(wakes[0], "reported") {
		t.Fatalf("wakeups = %v, want one report notice", wakes)
	}
	page, err := board.ReadAfter(context.Background(), "team:T", 0, team.Filter{Stamped: team.Identity{MemberID: "alpha", Generation: 1}})
	if err != nil {
		t.Fatal(err)
	}
	last := page.Events[len(page.Events)-1]
	if !strings.Contains(last.Summary, "reported: done green") {
		t.Fatalf("last event = %q, want the report", last.Summary)
	}
}

// TestRuntimeResumeSubmitsResumed: the recovery path submits the task again
// with a resume marker.
func TestRuntimeResumeSubmitsResumed(t *testing.T) {
	rt, agents := newTestRuntime(t, nil)
	task := team.Task{ID: "t1", Desc: "finish it", Status: team.TaskStatusRunning, AssignedMember: "alpha"}
	if err := rt.Resume(context.Background(), task, team.Member{ID: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if len(agents["alpha"].submitted) != 1 || !strings.Contains(agents["alpha"].submitted[0], "[resumed]") {
		t.Fatalf("agent input = %q, want a resumed marker", agents["alpha"].submitted)
	}
}

// TestDrainAcksOnlyAfterProcessing pins write-before-commit on the durable
// inbox: a failed handler leaves the watermark behind so the batch replays;
// a clean batch advances it exactly once.
func TestDrainAcksOnlyAfterProcessing(t *testing.T) {
	board := newTestBoard(t)
	_, err := board.Append(context.Background(), team.AppendInput{
		BoardID: "team:T", EventID: "cmd-1", ClientMsgID: "cmd-1", Kind: team.EventCommand,
		TaskID: "t9", Summary: "follow up", Stamped: team.Identity{MemberID: "alpha", Generation: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt, _ := newTestRuntime(t, board)
	inbox := NewBoardInbox(board, "team:T", "alpha", 1)
	processed := 0
	_, err = rt.Drain(context.Background(), inbox, 10, func(InboxItem) error {
		processed++
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("a failing handler must surface")
	}
	pos, err := inbox.watermark.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pos != 0 {
		t.Fatalf("watermark = %d, want 0 after a failed batch", pos)
	}
	if _, err = rt.Drain(context.Background(), inbox, 10, func(InboxItem) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want 1 (replayed after the failure)", processed)
	}
	pos, err = inbox.watermark.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pos == 0 {
		t.Fatal("watermark must advance after a clean batch")
	}
}

// TestBoardInboxSkipsOtherGeneration: commands stamped for another window
// are never drained by a stale consumer.
func TestBoardInboxSkipsOtherGeneration(t *testing.T) {
	board := newTestBoard(t)
	for _, g := range []uint64{1, 2} {
		_, err := board.Append(context.Background(), team.AppendInput{
			BoardID: "team:T", EventID: "cmd-" + string(rune('a'+g)), ClientMsgID: "cmd-" + string(rune('a'+g)),
			Kind: team.EventCommand, TaskID: "t9", Summary: "gen " + string(rune('0'+g)),
			Stamped: team.Identity{MemberID: "alpha", Generation: g},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	inbox := NewBoardInbox(board, "team:T", "alpha", 2)
	items, _, err := inbox.Fetch(context.Background(), -1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Generation != 2 {
		t.Fatalf("items = %+v, want only generation 2", items)
	}
}

// TestCursorWatermarkFirstReadZero: a consumer without a persisted row reads
// from zero, then the commit persists.
func TestCursorWatermarkFirstReadZero(t *testing.T) {
	board := newTestBoard(t)
	w := NewCursorWatermark(board, "team:T", "alpha", 1)
	pos, err := w.Load(context.Background())
	if err != nil || pos != 0 {
		t.Fatalf("first load = %d, %v; want 0, nil", pos, err)
	}
	if err := w.Commit(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	pos, err = w.Load(context.Background())
	if err != nil || pos != 42 {
		t.Fatalf("after commit = %d, %v; want 42, nil", pos, err)
	}
}

// TestBoardInboxIsolatesMembers: one member's inbox never sees another
// member's commands — the member filter is a hard boundary, not a hint.
func TestBoardInboxIsolatesMembers(t *testing.T) {
	board := newTestBoard(t)
	for _, m := range []string{"alpha", "beta"} {
		_, err := board.Append(context.Background(), team.AppendInput{
			BoardID: "team:T", EventID: "cmd-" + m, ClientMsgID: "cmd-" + m, Kind: team.EventCommand,
			TaskID: "t9", Summary: "for " + m, Stamped: team.Identity{MemberID: m, Generation: 1},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []string{"alpha", "beta"} {
		inbox := NewBoardInbox(board, "team:T", m, 1)
		items, _, err := inbox.Fetch(context.Background(), -1, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || !strings.Contains(items[0].Summary, "for "+m) {
			t.Fatalf("%s inbox = %+v, want only its own command", m, items)
		}
	}
}

// TestBoardInboxFetchRespectsLimit: a limited fetch stops at the limit and
// the watermark stays put; the next fetch from the acked position returns the
// remainder — batching never drops a command and never double-delivers.
func TestBoardInboxFetchRespectsLimit(t *testing.T) {
	board := newTestBoard(t)
	for i := range 3 {
		id := string(rune('a' + i))
		_, err := board.Append(context.Background(), team.AppendInput{
			BoardID: "team:T", EventID: "cmd-" + id, ClientMsgID: "cmd-" + id, Kind: team.EventCommand,
			TaskID: "t9", Summary: "cmd " + id, Stamped: team.Identity{MemberID: "alpha", Generation: 1},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	inbox := NewBoardInbox(board, "team:T", "alpha", 1)
	first, next, err := inbox.Fetch(context.Background(), -1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("first batch = %d, want the limit 2", len(first))
	}
	if err := inbox.Ack(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	rest, _, err := inbox.Fetch(context.Background(), -1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || rest[0].Summary != "cmd c" {
		t.Fatalf("second batch = %+v, want the remaining cmd c", rest)
	}
}
