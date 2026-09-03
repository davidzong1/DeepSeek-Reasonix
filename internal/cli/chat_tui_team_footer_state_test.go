package cli

// Footer state isolation tests for team member switching. These tests pin the
// contract that the footer "working" status (m.turnPhase → runningWorkingLine)
// is scoped per member and properly cleared/reset on switch.
//
// bindBackend (chat_tui_team_switch.go) already clears the outgoing member's
// footer state (state=tuiIdle, turnPhase="", elapsed, turnTokens) on every
// switch, and switchTeamMember replays the incoming member's live buffer after
// binding, so a running member's phase is restored from its own events — never
// from the previous member's. All assertions below exercise current behavior.
//
// Tests check m.turnPhase directly (the internal state that drives the footer
// rendering) rather than runningWorkingLine, which requires a fully wired
// backend implementing InboxSnapshot. turnPhase is the source of truth:
// runningWorkingLine renders it through turnPhaseStatusLabel.

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// TestTurnPhaseClearedWhenSwitchingToIdleMember: switching from a working member
// to an idle member must drop the previous member's footer phase and idle the
// window — bindBackend clears state/turnPhase on every bind.
func TestTurnPhaseClearedWhenSwitchingToIdleMember(t *testing.T) {
	m := overlayWithBackends(t, map[string][]provider.Message{
		"lead":  {userMessage("lead-history")},
		"alice": {userMessage("alice-history")},
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)

	// Bind the leader and simulate a running turn.
	m.switchTeamMember("lead")
	m.state = tuiRunning
	m.turnPhase = "working"

	// Switch to an idle member who has no in-flight turn.
	m.switchTeamMember("alice")
	if m.turnPhase != "" {
		t.Errorf("switching to an idle member must clear the footer phase, got %q", m.turnPhase)
	}
	if m.state != tuiIdle {
		t.Errorf("switching must idle the window, state = %v", m.state)
	}
}

// TestTurnPhaseRestoredWhenSwitchingBackToRunningMember: a member whose turn is
// still in flight when the window switches away keeps emitting into its live
// buffer; switching back replays those events and restores the "working" footer.
func TestTurnPhaseRestoredWhenSwitchingBackToRunningMember(t *testing.T) {
	m := overlayWithBackends(t, map[string][]provider.Message{
		"lead":  {userMessage("lead-history")},
		"alice": {userMessage("alice-history")},
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)

	// Leader is running.
	m.switchTeamMember("lead")
	m.state = tuiRunning
	m.turnPhase = "working"

	// Switch away to alice (idle). bindBackend clears the leader's phase.
	m.switchTeamMember("alice")
	if m.turnPhase != "" {
		t.Fatalf("switching away must clear the leader's phase, got %q", m.turnPhase)
	}

	// Lead keeps working in the background — its TurnPhase lands in the live
	// buffer now that alice is bound.
	m.handleMemberEvent(memberEventMsg{
		member: "lead",
		ev:     event.Event{Kind: event.TurnPhase, PhaseName: "working", Text: "lead still working"},
	})
	if got := len(m.teamPick.session.live["lead"]); got == 0 {
		t.Fatal("lead's in-flight TurnPhase must buffer while the window shows alice")
	}

	// Switch back to lead — the replay must restore the working phase.
	m.switchTeamMember("lead")
	m.state = tuiRunning
	if m.turnPhase != "working" {
		t.Errorf("switching back to a working member must restore turnPhase=working, got %q", m.turnPhase)
	}
}

// TestTwoMembersDistinctTurnPhases: two members running concurrently each have
// their own turn phase. The footer must show the bound member's phase, not the
// other's, and switching restores the incoming member's own buffered phase.
func TestTwoMembersDistinctTurnPhases(t *testing.T) {
	m := overlayWithBackends(t, map[string][]provider.Message{
		"lead":  {userMessage("lead-history")},
		"alice": {userMessage("alice-history")},
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)

	// Lead is "checking", alice is "working".
	m.switchTeamMember("lead")
	m.state = tuiRunning
	m.turnPhase = "checking"

	// Simulate alice's TurnPhase arriving while lead is bound.
	m.handleMemberEvent(memberEventMsg{
		member: "alice",
		ev:     event.Event{Kind: event.TurnPhase, PhaseName: "working", Text: "alice working"},
	})
	// The bound member's turnPhase must still be "checking" — alice's TurnPhase
	// went to the buffer.
	if m.turnPhase != "checking" {
		t.Errorf("bound member's turnPhase must stay %q, got %q", "checking", m.turnPhase)
	}

	// Switch to alice — the live buffer replay must set "working".
	m.switchTeamMember("alice")
	m.state = tuiRunning
	if m.turnPhase != "working" {
		t.Errorf("after switching to alice, turnPhase should be working (from her buffered events), got %q", m.turnPhase)
	}
}

// TestBackgroundTurnStartedNotLeakToFooter: TurnStarted/TurnDone events from a
// non-bound member must not affect the bound member's footer state, and on
// switch they must not leak a stale phase into the newly bound member.
func TestBackgroundTurnStartedNotLeakToFooter(t *testing.T) {
	m := overlayWithBackends(t, map[string][]provider.Message{
		"lead":  {userMessage("lead-history")},
		"alice": {userMessage("alice-history")},
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)

	m.switchTeamMember("lead")
	m.state = tuiRunning
	m.turnPhase = "working"

	// A background member starts and finishes a turn. These must not touch the
	// bound member's footer.
	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{Kind: event.TurnStarted}})
	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{Kind: event.TurnDone}})

	if m.turnPhase != "working" {
		t.Errorf("background TurnStarted/TurnDone must not clear the bound member's turnPhase, got %q", m.turnPhase)
	}

	// Switch to alice. Her finished turn was dropped from the buffer (TurnDone
	// clears it), and bindBackend cleared the leader's phase — so the footer
	// must not show any stale "working".
	m.switchTeamMember("alice")
	m.state = tuiRunning
	if m.turnPhase != "" {
		t.Errorf("a finished background turn must not leak a phase into the next member, got %q", m.turnPhase)
	}
}

// TestBoundMemberTurnPhaseChangesUpdateFooter: the bound member's own TurnPhase
// events must update the footer in real time, not be buffered.
func TestBoundMemberTurnPhaseChangesUpdateFooter(t *testing.T) {
	m := overlayWithBackends(t, map[string][]provider.Message{
		"lead": {userMessage("lead-history")},
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)

	m.switchTeamMember("lead")
	m.state = tuiRunning

	// The bound member's TurnPhase must update the footer immediately.
	m.handleMemberEvent(memberEventMsg{
		member: "lead",
		ev:     event.Event{Kind: event.TurnPhase, PhaseName: "working"},
	})
	if m.turnPhase != "working" {
		t.Errorf("bound member's TurnPhase must set turnPhase, got %q", m.turnPhase)
	}

	// Subsequent TurnPhase overwrites.
	m.handleMemberEvent(memberEventMsg{
		member: "lead",
		ev:     event.Event{Kind: event.TurnPhase, PhaseName: "checking"},
	})
	if m.turnPhase != "checking" {
		t.Errorf("bound member's subsequent TurnPhase must overwrite, got %q", m.turnPhase)
	}

	// TurnDone must clear the phase.
	m.handleMemberEvent(memberEventMsg{member: "lead", ev: event.Event{Kind: event.TurnDone}})
	if m.turnPhase != "" {
		t.Errorf("TurnDone must clear turnPhase, got %q", m.turnPhase)
	}
}

// TestTurnPhaseClearedWhenOverlayOpens: verifying the turnPhase is empty when
// the team overlay first opens (no stale phase from the ambient session).
func TestTurnPhaseClearedWhenOverlayOpens(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	if m.turnPhase != "" {
		t.Errorf("a fresh overlay must start with empty turnPhase, got %q", m.turnPhase)
	}
}

// TestRosterStateMatchesBoundBackend pins the roster half of isolation:
// rosterMembers derives each member's state from its own assembled backend's
// RuntimeStatus, so the roster agrees with the current backend — one running
// member shows working while another stays idle, and switching never blends
// one member's working state into another's row.
func TestRosterStateMatchesBoundBackend(t *testing.T) {
	p := &teamPicker{}
	p.backends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		status := control.RuntimeStatus{}
		if b.MemberID == "lead" {
			status = control.RuntimeStatus{Running: true}
		}
		return stubBackend{label: b.MemberID, status: status}, nil
	}, 4)
	for _, b := range []team.MemberBinding{
		{Team: "alpha", MemberID: "lead"},
		{Team: "alpha", MemberID: "alice"},
	} {
		if _, err := p.backends.bind(b); err != nil {
			t.Fatal(err)
		}
	}

	members := p.rosterMembers(twoMemberTeam())
	got := map[string]team.MemberState{}
	for _, mem := range members {
		got[mem.ID] = mem.State
	}
	if got["lead"] != team.MemberStateWorking {
		t.Errorf("the running member must show working in the roster, got %q", got["lead"])
	}
	if got["alice"] != team.MemberStateIdle {
		t.Errorf("the idle member must stay idle in the roster, got %q", got["alice"])
	}
}
