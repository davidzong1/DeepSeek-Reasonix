package cli

// Registry-seam tests for TEAM_LEADER_WAIT_ROUTE.md §5.3: the bus a waiting
// leader subscribes to is the registry's, and both producers reach it through
// the registry's own late-bound seams.

import (
	"testing"

	"reasonix/internal/team/agentruntime"
)

// TestRegistryHandsItsBusToTheTaskService pins the service seam: the registry's
// setTasks installs its own bus on the service (and on every per-team child), so
// the report the runtime's attention hook raises lands on the bus a leader
// subscribes to.
func TestRegistryHandsItsBusToTheTaskService(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	closeBoardOn(t, m)
	registry := newTeamBackends(nil, 0)
	svc := newTeamTaskService(m.teamPick.store, m.teamPick.board.board, "alpha", nil)
	registry.setTasks(svc)
	waiter := registry.signals().Subscribe("alpha")
	defer waiter.Close()

	// What the runtime's registered attention hook calls on a member's report.
	svc.attention(agentruntime.AttentionReport, "t1", "task t1 reported")
	got := receiveWaitEvent(t, waiter)
	if got.Kind != "report" || got.ID != "t1" || got.Team != "alpha" {
		t.Fatalf("event = %+v, want the report the runtime raises", got)
	}
	child := svc.forTeam("beta")
	if child == svc {
		t.Fatal("precondition: a second team gets its own service child")
	}
	childWaiter := registry.signals().Subscribe("beta")
	defer childWaiter.Close()
	child.attention("cancel", "t2", "task t2 canceled")
	if ev := receiveWaitEvent(t, childWaiter); ev.Team != "beta" || ev.ID != "t2" {
		t.Fatalf("child event = %+v, want the per-team child to share the registry's bus", ev)
	}
}

// TestRegistryHandsItsBusToTheBoardDispatcher pins the board seam: setInbox
// attaches the registry's bus to the board's dispatcher, so a wakeup drained off
// the board reaches the same waiter a member's report does — one bus, two
// delivery paths.
func TestRegistryHandsItsBusToTheBoardDispatcher(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	closeBoardOn(t, m)
	registry := newTeamBackends(nil, 0)
	registry.setInbox(m.teamPick.board)
	waiter := registry.signals().Subscribe("alpha")
	defer waiter.Close()
	quiet(t, m.teamPick.board)

	appendWakeup(t, m.teamPick.board, "w1", "lead", "task t1 reported")
	if got := m.teamPick.board.wakes.drain("alpha", "lead"); len(got) != 1 {
		t.Fatalf("drain = %+v, want the leader's wakeup", got)
	}
	if ev := receiveWaitEvent(t, waiter); ev.Summary != "task t1 reported" || ev.Kind != waitKindWakeup {
		t.Fatalf("waiter = %+v, want the drained wakeup on the registry bus", ev)
	}
}
