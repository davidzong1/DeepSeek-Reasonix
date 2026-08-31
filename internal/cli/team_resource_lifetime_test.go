package cli

// Team resource lifetime (D1/D8): the registry owns the board because their
// holders — the task service, each member's tools — share its lifetime. An
// overlay reopen must not swap it; process exit must close it.

import (
	"context"
	"testing"

	"reasonix/internal/control"
	"reasonix/internal/team"
)

// TestReopeningTheOverlayKeepsTheTaskServiceBoardUsable pins D1: the [TEAM]
// click used to close the board and open a fresh one, while the task service —
// built once, behind the backend registry's build closure — kept the first
// handle. One reopen therefore broke every durable task call with
// "sql: database is closed", permanently, because the registry is never rebuilt.
func TestReopeningTheOverlayKeepsTheTaskServiceBoardUsable(t *testing.T) {
	m := overlayWithWiredRegistry(t)
	first := m.teamPick.boardStore()
	if first == nil {
		t.Skip("no board store in this environment")
	}
	svc := newTeamTaskService(m.teamPick.store, first, "alpha",
		func(b team.MemberBinding) (control.SessionAPI, error) {
			return stubBackend{label: b.MemberID}, nil
		})
	if _, err := svc.board.LoadLiveTasks(context.Background()); err != nil {
		t.Fatalf("baseline read: %v", err)
	}

	m.onTeamButtonClick() // the ordinary reopen: esc out, click [TEAM] again

	if second := m.teamPick.boardStore(); second != first {
		t.Fatal("a reopen must reuse the process-scoped board, not swap it under its holders")
	}
	if _, err := svc.board.LoadLiveTasks(context.Background()); err != nil {
		t.Fatalf("the task service must still reach its board after a reopen: %v", err)
	}
}

// TestExitTeamKeepsTeamResourcesAlive: leaving the team is an overlay teardown,
// not a resource teardown. A member may still be running a turn, and the task
// service still holds the board — closing either here is what made the durable
// chain fail after one exit-and-return.
func TestExitTeamKeepsTeamResourcesAlive(t *testing.T) {
	m := overlayWithWiredRegistry(t)
	board := m.teamPick.boardStore()
	if board == nil {
		t.Skip("no board store in this environment")
	}
	m.exitTeam()
	if m.teamPick != nil {
		t.Fatal("exitTeam must close the overlay")
	}
	if m.teamBackends.inbox() == nil {
		t.Fatal("the board must outlive the overlay")
	}
	if _, err := board.LoadLiveTasks(context.Background()); err != nil {
		t.Fatalf("the board must still be open after exitTeam: %v", err)
	}
}

// TestCloseTeamResourcesReleasesEverything pins D8's other half: nothing closed
// the member backends at all, so every assembled member kept its session lease
// and plugin subprocesses to process exit. It must also hand the window's
// controller identity back first, or the caller's own ctrl.Close() lands a second
// time on a member controller this just closed.
func TestCloseTeamResourcesReleasesEverything(t *testing.T) {
	closed := 0
	m := overlayWithWiredRegistry(t, &closed)
	// The overlay bound the leader, so ambient holds the chat's own backend and
	// m.ctrl is the member's. (stubBackend carries a slice, so it is uncomparable
	// as an interface value — identity is asserted on the ambient controller.)
	ambient := m.ambient
	if ambient == nil {
		t.Fatal("binding a member must save the chat's own backend")
	}

	m.closeTeamResources()

	if closed == 0 {
		t.Fatal("assembled member backends must be closed")
	}
	if m.ctrl != ambient {
		t.Fatal("the window's controller must be handed back to the chat's own backend")
	}
	if m.ambient != nil {
		t.Fatal("the ambient slot must be cleared once the window owns it again")
	}
	if m.teamBackends != nil {
		t.Fatal("team resources must be released, not left dangling")
	}
}

// overlayWithWiredRegistry opens the overlay the way a launched TUI does: the
// member-backend registry exists, so the board is installed into it and shared
// across opens. Without the registry there is no task service to outlive the
// overlay, and the per-overlay board stays correct — so the sharing contract can
// only be exercised with the seam wired.
func overlayWithWiredRegistry(t *testing.T, closed ...*int) chatTUI {
	t.Helper()
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	var counter *int
	if len(closed) > 0 {
		counter = closed[0]
	}
	m.memberEvents = make(chan memberEvent, 8)
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return stubBackend{label: b.MemberID, closed: counter}, nil
	}, 4)
	m.onTeamButtonClick() // the first open with a registry installs the shared board
	return m
}
