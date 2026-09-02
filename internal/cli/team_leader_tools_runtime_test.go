package cli

// Leader task tools must be armed on the very first overlay open, not the
// second: the task service is built with the board, so a nil-board build froze
// every leader tool on "team task runtime is unavailable".

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/team"
)

// teamOverlaySeamWired launches the way a boot does: the seam is installed
// first, then the [TEAM] click opens the overlay. The registry — and the durable
// task service behind every member tool — is assembled on that first open, so a
// stuck-on-nil-board service would surface right here.
func teamOverlaySeamWired(t *testing.T) chatTUI {
	t.Helper()
	writeTeamFixture(t, twoMemberTeam())
	ctrl := control.New(control.Options{})
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.bindTeamBackendSeam(20, cliBuildOverrides{})
	m.onTeamButtonClick()
	return m
}

// TestLeaderTaskToolsArmedOnFirstOpen pins the fix: the registry built on the
// first overlay carries a board-backed task service, and the exact leader tool
// that surfaced the defect (leader_check_member_status reaches ready()) answers
// instead of refusing.
func TestLeaderTaskToolsArmedOnFirstOpen(t *testing.T) {
	m := teamOverlaySeamWired(t)
	if m.teamBackends == nil {
		t.Fatal("the seam shared across launched TUIs: a wired open must assemble the registry")
	}
	svc := m.teamBackends.tasks
	if svc == nil {
		t.Fatal("the registry must expose a task service on the first open")
	}
	teamSvc := svc.forTeam("alpha")
	if err := teamSvc.ready(); err != nil {
		t.Fatalf("first-open task service must be board-backed: %v", err)
	}
	leader := newLeaderTaskTools(teamSvc, "alpha", "lead")
	st := leaderTool(t, leader, "leader_check_member_status")
	out, err := st.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("leader_check_member_status on a board-backed service: %v", err)
	}
	if !strings.Contains(out, "alice") {
		t.Fatalf("status read should list the roster, got %q", out)
	}
}

// TestLeaderTaskToolsSurviveReopen pins D1's other half for the service: an
// overlay reopen (esc out, [TEAM] again) must keep the same board-backed
// service, never rebuild it around a nil board.
func TestLeaderTaskToolsSurviveReopen(t *testing.T) {
	m := teamOverlaySeamWired(t)
	first := m.teamBackends.tasks
	m.exitTeam()
	if m.teamPick != nil {
		t.Fatal("exitTeam must close the overlay")
	}
	m.onTeamButtonClick()
	if got := m.teamBackends.tasks; got != first {
		t.Fatal("a reopen must keep the durable task service, not rebuild it")
	}
	if err := m.teamBackends.tasks.forTeam("alpha").ready(); err != nil {
		t.Fatalf("the kept service must stay board-backed after a reopen: %v", err)
	}
}

// TestSeamRebindDrainsStaleRegistry pins the seam's cleanup: a registry built
// before this fix pinned a board-less task service; re-seaming must retire it
// (and its assembled backends), then a fresh open rebuilds a board-backed one.
func TestSeamRebindDrainsStaleRegistry(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	closed := 0
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return stubBackend{label: b.MemberID, closed: &closed}, nil
	}, 4)
	if _, err := m.teamBackends.bind(team.MemberBinding{Team: "alpha", MemberID: "alice"}); err != nil {
		t.Fatal(err)
	}
	m.bindTeamBackendSeam(20, cliBuildOverrides{})
	if m.teamBackends != nil {
		t.Fatal("re-seaming must drain a stale registry, not let its dead task service linger")
	}
	if closed == 0 {
		t.Fatal("draining the stale registry must close its assembled backends")
	}
	m.onTeamButtonClick()
	if m.teamBackends == nil || m.teamBackends.tasks == nil {
		t.Fatal("a fresh open must rebuild the registry around a board-backed task service")
	}
	if err := m.teamBackends.tasks.forTeam("alpha").ready(); err != nil {
		t.Fatalf("rebuilt service ready: %v", err)
	}
}
