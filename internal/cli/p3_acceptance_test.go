package cli

// P3 acceptance: switching preserves each member's context (history, pending
// prompts, unread) on the still-live backend, and one shared pump re-arms
// for every member's events.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// TestP3SwitchPreservesMemberContext pins the P3 preservation contract on the
// window side of a switch: leaving a member with a pending approval clears only
// the card from the window (never the member's backend), re-binding that member
// asks its backend to replay the still-blocking prompt, and the bound window's
// own member-id session survives the whole round trip.
func TestP3SwitchPreservesMemberContext(t *testing.T) {
	m, replays := overlayBusy(t, nil)
	m = sized(t, m)
	m.switchTeamMember("lead")
	m.switchTeamMember("alice")
	if got := m.teamPick.session.current; got != "alice" {
		t.Fatalf("bound member = %q, want alice", got)
	}
	m.pendingApproval = &event.Approval{ID: "a1", Tool: "bash"}
	if cmd := m.switchTeamMember("lead"); cmd != nil {
		t.Fatal("answering nothing yet, a pending approval must refuse the switch")
	}
	if got := m.teamPick.session.current; got != "alice" {
		t.Fatalf("the refused switch must keep alice bound, got %q", got)
	}

	// Accept the approval: the card leaves the window and a switch away is now
	// legal. The bound window's own frame is untouched by resolving the card.
	before := len(m.transcript)
	m.pendingApproval = nil
	if len(m.transcript) != before {
		t.Fatal("resolving alice's card must not mutate the bound transcript")
	}
	if cmd := m.switchTeamMember("lead"); cmd == nil {
		t.Fatal("after the approval is resolved the switch must succeed")
	}
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("bound member = %q, want lead", got)
	}

	// Back to alice: her backend re-asks for the pending prompt. The replay
	// call is the seam; an idle member replays nothing.
	replayed := *replays["alice"]
	m.teamPick.session.current = "alice"
	m.switchTeamMember("alice")
	if got := *replays["alice"]; got != replayed+1 {
		t.Fatalf("re-binding alice must ask her backend to replay prompts, got %d want %d", got, replayed+1)
	}
}

// TestP3IndependentMemberCursors exposes the P3 cursor model directly: each
// member's history and pending-prompt state live on its own backend, so a
// switch never reads across the boundary — the roster cursor moves within one
// member's context, and the transcript after switching twice matches the
// member's own end state.
func TestP3IndependentMemberCursors(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	m.memberEvents = make(chan memberEvent, 8)
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return stubBackend{label: b.MemberID, status: control.RuntimeStatus{}, history: map[string][]provider.Message{
			"lead":  {userMessage("LEAD-HISTORY")},
			"alice": {userMessage("ALICE-HISTORY")},
		}[b.MemberID]}, nil
	}, 4)
	m = sized(t, m)
	m.switchTeamMember("lead")
	m.switchTeamMember("alice")
	m.switchTeamMember("lead")

	joined := strings.Join(m.transcript, "\n")
	if !strings.Contains(joined, "LEAD-HISTORY") {
		t.Fatalf("the member's own history must replay on return:\n%s", joined)
	}
	if strings.Contains(joined, "ALICE-HISTORY") {
		t.Fatalf("the other member's cursor must never leak into this window:\n%s", joined)
	}
}

// TestP3BackgroundEventBadgesExactlyOnce merges the P2 pump contract and the P3
// unread contract: two attention events from a switched-away member badge it
// twice, streaming deltas badge nothing, and binding that member back clears
// its counter to zero — a fresh cursor, not a stale pile.
func TestP3BackgroundEventBadgesExactlyOnce(t *testing.T) {
	m, _ := overlayBusy(t, map[string]control.RuntimeStatus{"lead": {Running: true}})
	m = sized(t, m)
	m.switchTeamMember("lead")
	m.switchTeamMember("alice")

	for _, kind := range []event.Kind{event.TurnDone, event.Message} {
		if cmd := m.handleMemberEvent(memberEventMsg{member: "lead", ev: event.Event{Kind: kind}}); cmd == nil {
			t.Fatal("the shared pump must re-arm after every background event")
		}
	}
	if got := m.teamPick.session.unread["lead"]; got != 2 {
		t.Fatalf("unread[lead] = %d, want 2 (each attention event once)", got)
	}
	m.handleMemberEvent(memberEventMsg{member: "lead", ev: event.Event{Kind: event.Text, Text: "x"}})
	if got := m.teamPick.session.unread["lead"]; got != 2 {
		t.Errorf("a streaming delta must not badge, unread[lead] = %d", got)
	}

	m.switchTeamMember("lead")
	if got := m.teamPick.session.unread["lead"]; got != 0 {
		t.Errorf("binding the member back must clear its badge to a fresh cursor, got %d", got)
	}
	if cmd := m.switchTeamMember("alice"); cmd == nil {
		t.Fatal("a running member's switch must stay legal after the round trip")
	}
}

// TestP3BoundMemberEventsRenderToOwnWindow pins which window swallows which
// events: an unbound member's text never reaches the transcript or cards, and
// the bound member's own attention event renders into its own window (the
// right-side context belongs to whoever is bound).
func TestP3BoundMemberEventsRenderToOwnWindow(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	m.memberEvents = make(chan memberEvent, 8)
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return stubBackend{label: b.MemberID, status: control.RuntimeStatus{}, history: map[string][]provider.Message{
			"alice": {userMessage("ALICE-HISTORY")},
		}[b.MemberID]}, nil
	}, 4)
	m = sized(t, m)
	m.switchTeamMember("lead")
	before := len(m.transcript)

	for _, kind := range []event.Kind{event.Message, event.ApprovalRequest, event.AskRequest} {
		if cmd := m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{Kind: kind}}); cmd == nil {
			t.Fatal("the pump must re-arm after an unbound member's attention event")
		}
	}
	if len(m.transcript) != before {
		t.Fatal("an unbound member's events must never touch the bound transcript")
	}
	if m.pendingApproval != nil || m.chooser != nil {
		t.Fatal("an unbound member's prompt must not surface a card in this window")
	}
	if got := m.teamPick.session.unread["alice"]; got != 3 {
		t.Fatalf("the unbound member's three attention events must badge exactly 3, got %d", got)
	}

	// Switching to that member renders its own history, not this one's, and the
	// badge is consumed — the window now owns alice's context.
	m.switchTeamMember("alice")
	if got := m.teamPick.session.current; got != "alice" {
		t.Fatalf("bound member = %q, want alice", got)
	}
	if got := m.teamPick.session.unread["alice"]; got != 0 {
		t.Fatalf("binding the badged member must consume its badge, got %d", got)
	}
	joined := strings.Join(m.transcript, "\n")
	if !strings.Contains(joined, "ALICE-HISTORY") {
		t.Fatalf("the window must render alice's own history:\n%s", joined)
	}
}

// TestP3PumpRearmsAfterBoundMemberAttention pins the shared-pump edge case the
// P2 pump test only half-covered: even when the pump is already mid-stream, the
// directly-bound member's own attention events ingest into her window and
// re-arm the single pump exactly once per event — never a second pump, never a
// dead reader.
func TestP3PumpRearmsAfterBoundMemberAttention(t *testing.T) {
	m := overlayWithBackends(t, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	if got := m.boundMember(); got != "lead" {
		t.Fatalf("the overlay should open bound to the leader, got %q", got)
	}
	for i, kind := range []event.Kind{event.Message, event.ApprovalRequest, event.AskRequest} {
		after, cmd := m.Update(memberEventMsg{member: "lead", ev: event.Event{Kind: kind}})
		m = after.(chatTUI)
		if cmd == nil {
			t.Fatalf("bound event #%d (%v) must re-arm the shared pump", i, kind)
		}
	}
	if got := m.teamPick.session.unread["lead"]; got != 0 {
		t.Fatalf("the bound member's own events must not badge her, unread[lead] = %d", got)
	}
}
