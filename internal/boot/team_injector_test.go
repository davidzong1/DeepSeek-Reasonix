package boot

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/team"
	"reasonix/internal/team/agentruntime"
)

// stubMemberAgent records the submitted inputs so the tests can prove the §7
// chain really reaches the member's backend.
type stubMemberAgent struct{ submitted []string }

func (a *stubMemberAgent) Submit(string) {}
func (a *stubMemberAgent) SubmitUserTurn(input, display string) {
	a.submitted = append(a.submitted, input)
}
func (a *stubMemberAgent) Cancel()                    {}
func (a *stubMemberAgent) Running() bool              { return false }
func (a *stubMemberAgent) Turn() int                  { return 0 }
func (a *stubMemberAgent) Compose(text string) string { return text }
func (a *stubMemberAgent) Close()                     {}

// newInjectorBoard wires a runtime over a real store with the full §7
// injector installed — the host assembly under test.
func newInjectorBoard(t *testing.T) (*team.SQLiteStore, *agentruntime.Runtime, *stubMemberAgent) {
	t.Helper()
	store, err := team.NewSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	agent := &stubMemberAgent{}
	rt := agentruntime.NewRuntime(func(string) (agentruntime.AgentAPI, error) { return agent, nil },
		store, team.BoardShared, func(memberID string) team.Identity {
			return team.Identity{MemberID: memberID, Generation: 1}
		})
	rt.SetTaskStore(store)
	WireTeamInjector(rt, store)
	return store, rt, agent
}

// TestWireTeamInjectorChainOrder pins the §7 chain on the real submission
// path: task -> inbox -> board, in that order, reaching the member's backend.
func TestWireTeamInjectorChainOrder(t *testing.T) {
	store, rt, agent := newInjectorBoard(t)
	if err := store.SaveBinding(context.Background(), team.BindRecord{MemberID: "m1", LeaderID: "L", Generation: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), team.AppendInput{
		BoardID: team.BoardShared, EventID: "cmd-1", ClientMsgID: "cmd-1", Kind: team.EventCommand,
		TaskID: "t1", Summary: "check the gate", Stamped: team.Identity{MemberID: "m1", Generation: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), team.AppendInput{
		BoardID: team.BoardShared, EventID: "conc-1", ClientMsgID: "conc-1", Kind: team.EventEvidence,
		TaskID: "t1", Summary: "gate verified", Stamped: team.Identity{MemberID: "L", Generation: 1},
		Conclusion: &team.ConclusionUpdate{Topic: "gate", Summary: "gate verified"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusAssigned, Desc: "verify"}, team.Member{ID: "m1"}); err != nil {
		t.Fatal(err)
	}
	if len(agent.submitted) != 1 {
		t.Fatalf("submitted %d turns, want 1", len(agent.submitted))
	}
	text := agent.submitted[0]
	if i, j, k := strings.Index(text, "[task: t1]"), strings.Index(text, "[command inbox]"), strings.Index(text, "[board view]"); !(i >= 0 && j > i && k > j) {
		t.Fatalf("chain order task@%d inbox@%d board@%d: %q", i, j, k, text)
	}
	if !strings.Contains(text, "check the gate") || !strings.Contains(text, "gate verified") {
		t.Fatalf("chain content missing: %q", text)
	}
}

// TestWireTeamInjectorAckOnce pins the write-before-commit at the injector:
// the batch is acknowledged exactly once, so the next turn never re-injects
// the same command and the member's cursor lands on it.
func TestWireTeamInjectorAckOnce(t *testing.T) {
	store, rt, agent := newInjectorBoard(t)
	if err := store.SaveBinding(context.Background(), team.BindRecord{MemberID: "m1", LeaderID: "L", Generation: 1}); err != nil {
		t.Fatal(err)
	}
	cmd, err := store.Append(context.Background(), team.AppendInput{
		BoardID: team.BoardShared, EventID: "cmd-1", ClientMsgID: "cmd-1", Kind: team.EventCommand,
		TaskID: "t1", Summary: "check the gate", Stamped: team.Identity{MemberID: "m1", Generation: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusAssigned, Desc: "verify"}, team.Member{ID: "m1"}); err != nil {
		t.Fatal(err)
	}
	if len(agent.submitted) != 1 || !strings.Contains(agent.submitted[0], "check the gate") {
		t.Fatalf("first turn missing the command: %q", agent.submitted)
	}
	// A second task for the same member must not see the consumed command.
	if err := rt.Complete("t1", "done"); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background(), team.Task{ID: "t2", Status: team.TaskStatusAssigned, Desc: "verify again"}, team.Member{ID: "m1"}); err != nil {
		t.Fatal(err)
	}
	if len(agent.submitted) != 2 || strings.Contains(agent.submitted[1], "check the gate") {
		t.Fatalf("command re-injected on the second turn: %q", agent.submitted)
	}
	cursor, err := store.GetCursor(context.Background(), team.BoardShared, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if cursor.LastSeq != cmd.Seq {
		t.Fatalf("cursor seq = %d, want %d (ack landed exactly once)", cursor.LastSeq, cmd.Seq)
	}
}

// TestWireTeamInjectorFullJourney pins the whole leader->member->leader loop
// on the real assembly: leader commands inject in order, the report lands
// durably on the task store and the blackboard, and the leader is woken —
// one journey, one assertion set.
func TestWireTeamInjectorFullJourney(t *testing.T) {
	store, rt, agent := newInjectorBoard(t)
	if err := store.SaveBinding(context.Background(), team.BindRecord{MemberID: "m1", LeaderID: "L", Generation: 1}); err != nil {
		t.Fatal(err)
	}
	// Leader sends two direct commands, oldest first.
	for i, summary := range []string{"check the gate", "then sign off"} {
		if _, err := store.Append(context.Background(), team.AppendInput{
			BoardID: team.BoardShared, EventID: "cmd-" + string(rune('1'+i)), ClientMsgID: "cmd-" + string(rune('1'+i)),
			Kind: team.EventCommand, TaskID: "t1", Summary: summary,
			Stamped: team.Identity{MemberID: "m1", Generation: 1},
		}); err != nil {
			t.Fatal(err)
		}
	}
	var wakes []string
	rt.AddWakeup(func(reason string) error { wakes = append(wakes, reason); return nil })
	if err := rt.Start(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusAssigned, Desc: "verify"}, team.Member{ID: "m1"}); err != nil {
		t.Fatal(err)
	}
	text := agent.submitted[0]
	if i, j := strings.Index(text, "check the gate"), strings.Index(text, "then sign off"); !(i >= 0 && j > i) {
		t.Fatalf("commands must inject in send order, got %q", text)
	}
	if err := rt.Complete("t1", "gate passed"); err != nil {
		t.Fatal(err)
	}
	if len(wakes) != 1 || !strings.Contains(wakes[0], "reported") {
		t.Fatalf("wakeups = %v, want one report notice", wakes)
	}
	saved, err := store.LoadTask(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != team.TaskStatusReported {
		t.Fatalf("saved status = %s, want reported", saved.Status)
	}
	page, err := store.ReadAfter(context.Background(), team.BoardShared, 0, team.Filter{Stamped: team.Identity{MemberID: "m1", Generation: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page.Events[len(page.Events)-1].Summary, "reported: gate passed") {
		t.Fatalf("blackboard must carry the report, got %+v", page.Events)
	}
	// The reported task is archive-eligible: the migration map allows it.
	if err := team.TransitionTask(saved.Status, team.TaskStatusArchived); err != nil {
		t.Fatalf("reported -> archived must be legal: %v", err)
	}
}

// TestWireTeamInjectorUnboundTaskOnly: a member without a persisted binding
// gets the default task-only chain — the injector never invents a window it
// cannot answer for.
func TestWireTeamInjectorUnboundTaskOnly(t *testing.T) {
	store, rt, agent := newInjectorBoard(t)
	if _, err := store.Append(context.Background(), team.AppendInput{
		BoardID: team.BoardShared, EventID: "cmd-1", ClientMsgID: "cmd-1", Kind: team.EventCommand,
		TaskID: "t1", Summary: "check the gate", Stamped: team.Identity{MemberID: "m1", Generation: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusAssigned, Desc: "verify"}, team.Member{ID: "m1"}); err != nil {
		t.Fatal(err)
	}
	text := agent.submitted[0]
	if strings.Contains(text, "[command inbox]") || strings.Contains(text, "[board view]") {
		t.Fatalf("unbound member injected %q, want task-only", text)
	}
	if !strings.Contains(text, "[task: t1]") {
		t.Fatalf("task link missing: %q", text)
	}
}
