package cli

// Durable command chain tests (route §5.1/§7): the bound member's unread
// leader commands ride the next turn and are acknowledged, stale
// generations never inject, unbound members pass through, wakeups once.

import (
	"context"
	"strings"
	"testing"

	"reasonix/internal/control"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// captureBackend records the model inputs the window submits, so the inbox
// injection is assertable at the final boundary.
type captureBackend struct {
	stubBackend
	sent []string
	raw  []string
}

func (b *captureBackend) SendWithRaw(sent, raw string) {
	b.sent = append(b.sent, sent)
	b.raw = append(b.raw, raw)
}

func closeBoardOn(t *testing.T, m chatTUI) {
	t.Helper()
	t.Cleanup(func() {
		if m.teamPick != nil && m.teamPick.board != nil {
			m.teamPick.board.close()
		}
	})
}

// seedCommand writes one leader command for a member and its persisted
// binding, so the member has a durable inbox to drain.
func seedCommand(t *testing.T, m chatTUI, member string, generation uint64, taskID, summary string) {
	t.Helper()
	ctx := context.Background()
	w := m.teamPick.board
	if err := w.board.SaveBinding(ctx, team.BindRecord{
		MemberID: member, LeaderID: member, Generation: generation, Status: team.BindStatusBound,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.board.Append(ctx, team.AppendInput{
		BoardID: team.BoardShared,
		EventID: "cmd-" + taskID,
		Kind:    team.EventCommand,
		TaskID:  team.TaskID(taskID),
		Summary: summary,
		Stamped: team.Identity{MemberID: member, Generation: generation},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestTeamInboxInjectsBoundMemberCommands pins the turn-tail injection: the
// [TEAM] entry lands on the leader session, its unread commands ride the
// next model input in the §7 order, and the acknowledged batch never
// injects twice.
func TestTeamInboxInjectsBoundMemberCommands(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	closeBoardOn(t, m)
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("the click must open the leader session, got %q", got)
	}
	seedCommand(t, m, "lead", 2, "t1", "build the widget")

	got := m.injectTeamTurn("hi")
	if !strings.Contains(got, "[command inbox] (generation 2)") {
		t.Errorf("the injected block must carry the generation, got:\n%s", got)
	}
	if !strings.Contains(got, "[task: t1] build the widget") {
		t.Errorf("the injected block must carry the command, got:\n%s", got)
	}
	if !strings.HasSuffix(got, "\nhi") {
		t.Errorf("the user text must stay the tail of the turn, got:\n%s", got)
	}
	if again := m.injectTeamTurn("hi"); again != "hi" {
		t.Errorf("an acknowledged batch must not inject twice, got:\n%s", again)
	}
}

// TestTeamInboxSkipsStaleGeneration pins the generation gate (§4.1): a
// command stamped with an older generation than the window's binding is
// skipped, never injected into the current window's turn.
func TestTeamInboxSkipsStaleGeneration(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	closeBoardOn(t, m)
	seedCommand(t, m, "lead", 2, "t2", "current work")
	ctx := context.Background()
	w := m.teamPick.board
	// A stale-generation command directly on the board (bypassing the
	// service layer, which would refuse it): the inbox must skip it.
	if _, err := w.board.Append(ctx, team.AppendInput{
		BoardID: team.BoardShared, EventID: "cmd-stale", Kind: team.EventCommand,
		TaskID: "t0", Summary: "old window work",
		Stamped: team.Identity{MemberID: "lead", Generation: 1},
	}); err != nil {
		t.Fatal(err)
	}
	got := m.injectTeamTurn("hi")
	if strings.Contains(got, "old window work") {
		t.Errorf("a stale generation must never inject, got:\n%s", got)
	}
	if !strings.Contains(got, "current work") {
		t.Errorf("the current generation's command must inject, got:\n%s", got)
	}
}

// TestTeamInboxUnboundMemberPassesThrough pins the gate for a member with
// no persisted binding: the window has no generation to answer for, so the
// turn passes through untouched — no fabrication, no drain.
func TestTeamInboxUnboundMemberPassesThrough(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	closeBoardOn(t, m)
	if got := m.injectTeamTurn("hi"); got != "hi" {
		t.Errorf("an unbound member must pass through unchanged, got:\n%s", got)
	}
}

// TestTeamWakeupConsumeOnce pins the wakeup consumption (§5.1): the first
// read establishes the leader's cursor quietly, a wakeup surfaces once, and
// the advanced cursor keeps it from surfacing again.
func TestTeamWakeupConsumeOnce(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	closeBoardOn(t, m)
	w := m.teamPick.board
	if reasons := w.consumeWakeups("lead"); len(reasons) != 0 {
		t.Fatalf("the first read must be quiet, got %v", reasons)
	}
	ctx := context.Background()
	if _, err := w.board.Append(ctx, team.AppendInput{
		BoardID: team.BoardShared, EventID: "w1", Kind: team.EventWakeup,
		Summary: "task t1 reported", Stamped: team.Identity{MemberID: "lead"},
	}); err != nil {
		t.Fatal(err)
	}
	if reasons := w.consumeWakeups("lead"); len(reasons) != 1 || reasons[0] != "task t1 reported" {
		t.Fatalf("the wakeup must surface once, got %v", reasons)
	}
	if reasons := w.consumeWakeups("lead"); len(reasons) != 0 {
		t.Fatalf("a consumed wakeup must not surface again, got %v", reasons)
	}
}

// TestTeamTurnInjectsInboxAtSubmit pins the wiring end to end: the submit
// path feeds the injected text to the controller while the composer text,
// display bubble and restore stay the user's own.
func TestTeamTurnInjectsInboxAtSubmit(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	closeBoardOn(t, m)
	m.memberEvents = make(chan memberEvent, 8)
	var cb *captureBackend
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		cb = &captureBackend{stubBackend: stubBackend{
			label: b.MemberID, history: []provider.Message{userMessage("H")},
		}}
		return cb, nil
	}, 4)
	if cmd := m.switchTeamMember("lead"); cmd == nil {
		t.Fatal("switching to the leader must arm the event pump")
	}
	seedCommand(t, m, "lead", 2, "t3", "review the plan")

	cmd := m.startTurnWithRaw("hi", "hi", "hi", "hi")
	cmd()
	if len(cb.sent) != 1 {
		t.Fatalf("the submit path must reach the controller once, sent=%d", len(cb.sent))
	}
	if !strings.Contains(cb.sent[0], "[task: t3] review the plan") || !strings.HasSuffix(cb.sent[0], "hi") {
		t.Errorf("the controller must receive the injected turn, got:\n%s", cb.sent[0])
	}
	if cb.raw[0] != "hi" {
		t.Errorf("the raw prompt must stay the user's own, got %q", cb.raw[0])
	}
	// A second turn is quiet: the batch was acknowledged at the first submit.
	cmd = m.startTurnWithRaw("hi", "hi", "hi", "hi")
	cmd()
	if n := len(cb.sent); n != 2 || cb.sent[1] != "hi" {
		t.Errorf("the acknowledged batch must not inject twice, sent=%d", n)
	}
}
