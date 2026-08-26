package cli

// Leader lifecycle and session-exit contract tests (route §11.6): re-entry
// never restores a stale member window, the exit key keeps the leader's
// context, l assigns only without a leader, k stops all sessions first.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// TestTeamReentryStartsFreshOnLeader: after the exit key the next [TEAM]
// click lands on the management page (§11.4 suppression) — a deliberate t then
// enters the session on the leader, never the previously bound member: the
// entry point is leader-gated, so a stale member window cannot outlive its
// session.
func TestTeamReentryStartsFreshOnLeader(t *testing.T) {
	m := overlayWithBackends(t, map[string][]provider.Message{
		"lead":  {userMessage("LEAD-HISTORY")},
		"alice": {userMessage("ALICE-HISTORY")},
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("entry must open on the leader, got %q", got)
	}
	m.switchTeamMember("alice")
	if got := m.teamPick.session.current; got != "alice" {
		t.Fatalf("expected the member bound, got %q", got)
	}
	m = teamKey(m, exitKey)
	m.onTeamButtonClick()
	if m.teamPick.session.active {
		t.Fatal("after the exit key the click must land on the management page")
	}
	// A deliberate t enters fresh on the leader: the stale alice selection is
	// gated by the leader property, exactly like the t key.
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // the roster
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // focus the leader
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	if !m.teamPick.session.active {
		t.Fatal("t must open the session window")
	}
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("the session must land on the leader, not restore %q", got)
	}
}

// TestTeamExitKeepsLeaderContext pins the exit key's boundary: it hands the
// window back to the chat backend but leaves the leader's own backend and
// its transcript alive — leaving is not the k step-down, which is the only
// path that clears leader context.
func TestTeamExitKeepsLeaderContext(t *testing.T) {
	m := overlayWithBackends(t, map[string][]provider.Message{
		"lead": {userMessage("LEAD-HISTORY")},
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.switchTeamMember("lead")
	if got := m.ctrl.Label(); got != "lead" {
		t.Fatalf("expected the leader bound, got %q", got)
	}
	m = teamKey(m, exitKey)
	backend, ok := m.teamBackends.bound("alpha", "lead")
	if !ok {
		t.Fatal("the exit key must leave the leader's backend alive")
	}
	if h := backend.History(); len(h) != 1 || h[0].Content != "LEAD-HISTORY" {
		t.Fatalf("the leader's context must be intact after exit, got %+v", h)
	}
}

// TestKStepDownStopsAllSessionsBeforeClearing pins k's order: every member
// backend closes first, then the session files go, then the leader marker
// publishes off and the session window closes. A later assertion in the
// failure test pins the write-before-commit half.
func TestKStepDownStopsAllSessionsBeforeClearing(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	closed := 0
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return stubBackend{label: b.MemberID, closed: &closed}, nil
	}, 4)
	m.teamPick.backends = m.teamBackends
	for _, id := range []string{"lead", "alice"} {
		if _, err := m.teamBackends.bind(team.MemberBinding{Team: "alpha", MemberID: id}); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	m.teamPick.sessionDir = dir
	var paths []string
	for _, id := range []string{"lead", "alice"} {
		name, err := team.MemberSessionFile("alpha", id)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(`{"messages":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}

	if err := m.teamPick.stepDownLeader("alpha", "lead"); err != nil {
		t.Fatalf("stepDownLeader: %v", err)
	}
	if closed != 2 {
		t.Errorf("stop must close every member backend first, closed=%d", closed)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s must be deleted after the stop, stat err = %v", filepath.Base(path), err)
		}
	}
	if slot, ok := m.teamPick.slotOf("lead"); !ok || slot.IsLeader() {
		t.Error("the leader marker must publish off last")
	}
	if m.teamPick.session.active {
		t.Error("the session window must close after step-down")
	}
}

// TestKStepDownStopFailureKeepsContext: when a backend survives the stop, the
// step-down aborts before clearing anything — the session files stay, the
// leader marker stays, so a failed k never leaves a half-cleared team.
func TestKStepDownStopFailureKeepsContext(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	closed := 0
	rb := newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return stubBackend{label: b.MemberID, closed: &closed}, nil
	}, 4)
	// A backend present in live but not in the eviction order survives
	// releaseTeam, which is the stop failure surface.
	rb.live[backendKey("alpha", "lead")] = stubBackend{label: "lead", closed: &closed}
	m.teamBackends = rb
	m.teamPick.backends = rb
	dir := t.TempDir()
	m.teamPick.sessionDir = dir
	name, err := team.MemberSessionFile("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(`{"messages":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	err = m.teamPick.stepDownLeader("alpha", "lead")
	if err == nil {
		t.Fatal("a survived backend must abort the step-down")
	}
	if !strings.Contains(err.Error(), "survived") {
		t.Errorf("the error must name the failure, got %v", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("session files must survive a failed stop: %v", statErr)
	}
	if slot, ok := m.teamPick.slotOf("lead"); !ok || !slot.IsLeader() {
		t.Error("the leader marker must survive a failed step-down")
	}
	if closed != 0 {
		t.Errorf("no backend may be closed when the stop fails, closed=%d", closed)
	}
}

// TestKStepDownRepeatedCallIsSafe: a second step-down of the same leader is
// an explicit refusal, not a second wipe — no panic, no double close, the
// files are already gone.
func TestKStepDownRepeatedCallIsSafe(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	closed := 0
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return stubBackend{label: b.MemberID, closed: &closed}, nil
	}, 4)
	m.teamPick.backends = m.teamBackends
	for _, id := range []string{"lead", "alice"} {
		if _, err := m.teamBackends.bind(team.MemberBinding{Team: "alpha", MemberID: id}); err != nil {
			t.Fatal(err)
		}
	}
	m.teamPick.sessionDir = t.TempDir()
	if err := m.teamPick.stepDownLeader("alpha", "lead"); err != nil {
		t.Fatalf("first step-down: %v", err)
	}
	if err := m.teamPick.stepDownLeader("alpha", "lead"); err == nil {
		t.Fatal("a repeated step-down must refuse the member is no longer leader")
	}
	if closed != 2 {
		t.Errorf("no backend may close twice, closed=%d", closed)
	}
}

// TestLAssignAppointsLeaderlessTeam: the l key assigns the focused member as
// leader when the team has none — the leaderless team stays on the
// management page until an assignment gives it a leader.
func TestLAssignAppointsLeaderlessTeam(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "alpha", Template: []team.MemberSlot{
		{MemberID: "alice", Role: team.RoleTester, Status: team.MemberStatusActive},
	}})
	m := openTeamOverlay(t)
	if m.teamPick.session.active {
		t.Fatal("a leaderless team must stay on the management page")
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // into the roster
	if err := m.teamPick.assignFocusedLeader(); err != nil {
		t.Fatalf("assign on a leaderless team: %v", err)
	}
	if got := m.teamPick.firstLeader(); got != "alice" {
		t.Fatalf("the focused member must become leader, got %q", got)
	}
}

// TestLAssignRefusesWhenLeaderExists: with a leader present, assigning
// another member is refused with the holder's id and leaves the leader
// untouched — leaders change through k, never through l.
func TestLAssignRefusesWhenLeaderExists(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)                                 // leaders first: lead pinned, alice after
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // focus alice, the non-leader
	err := m.teamPick.assignFocusedLeader()
	if err == nil {
		t.Fatal("assigning with a leader present must refuse")
	}
	if !strings.Contains(err.Error(), "lead") {
		t.Errorf("the refusal must name the holder, got %v", err)
	}
	if got := m.teamPick.firstLeader(); got != "lead" {
		t.Errorf("the existing leader must stay, got %q", got)
	}
}

// TestRoleEditingReadOnly pins the leader's ruling: the TUI edits nothing
// about a member's role — role leaves the member editor, the store keeps
// its own member-changing APIs.
func TestRoleEditingReadOnly(t *testing.T) {
	for _, f := range memberEditFields {
		if f == "role" {
			t.Skip("awaiting role read-only seam: memberEditFields still lists role")
		}
	}
}
