package cli

import (
	"strings"
	"testing"

	"reasonix/internal/event"
)

// TestMemberInFlightTurnVisibleAfterSwitch pins the user-visible half of the
// concurrency story: while the window shows another member, a working member
// writes nothing to the transcript, and switching to it must then show the turn
// it is running right now. History() carries only committed messages, so before
// the live buffer a member mid-turn looked idle — which is what made "is it
// actually thinking?" unanswerable during a concurrency smoke test.
func TestMemberInFlightTurnVisibleAfterSwitch(t *testing.T) {
	m := promptTestTUI(t, nil) // binds "lead"
	// The real shape of a member producing output: streamed deltas, then the
	// canonical message. The turn has not settled, so none of it is in History().
	for _, ev := range []event.Event{
		{Kind: event.Text, Text: "ALICE-IN-FLIGHT"},
		{Kind: event.Message, Text: "ALICE-IN-FLIGHT"},
	} {
		m.handleMemberEvent(memberEventMsg{member: "alice", ev: ev})
	}

	if joined := strings.Join(m.transcript, "\n"); strings.Contains(joined, "ALICE-IN-FLIGHT") {
		t.Fatalf("an unbound member must not write the bound member's transcript:\n%s", joined)
	}
	if got := len(m.teamPick.session.live["alice"]); got != 2 {
		t.Fatalf("live buffer = %d events, want the in-flight turn kept for the switch", got)
	}

	if cmd := m.switchTeamMember("alice"); cmd == nil {
		t.Fatal("switching to alice must bind its backend")
	}
	if joined := strings.Join(m.transcript, "\n"); !strings.Contains(joined, "ALICE-IN-FLIGHT") {
		t.Fatalf("the switch must replay the member's in-flight turn:\n%s", joined)
	}
}

// TestMemberLiveBufferClearsOnTurnDone: once the turn settles its content is in
// the member's own History(), so the buffer must drop it — replaying both would
// show the turn twice on the next switch.
func TestMemberLiveBufferClearsOnTurnDone(t *testing.T) {
	m := promptTestTUI(t, nil)
	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{Kind: event.Message, Text: "done work"}})
	if len(m.teamPick.session.live["alice"]) == 0 {
		t.Fatal("the in-flight event must buffer")
	}
	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{Kind: event.TurnDone}})
	if _, ok := m.teamPick.session.live["alice"]; ok {
		t.Fatal("a finished turn must clear the buffer; its content is in History() now")
	}
}

// TestMemberLiveBufferIsBounded: a long background turn must not grow the buffer
// without limit. The oldest events go first, so a switch shows the tail of what
// the member is doing rather than nothing.
func TestMemberLiveBufferIsBounded(t *testing.T) {
	m := promptTestTUI(t, nil)
	for i := range memberLiveEventCap + 50 {
		text := "keep"
		if i == 0 {
			text = "OLDEST"
		}
		m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{Kind: event.Text, Text: text}})
	}
	buffered := m.teamPick.session.live["alice"]
	if len(buffered) != memberLiveEventCap {
		t.Fatalf("buffer = %d events, want the cap %d", len(buffered), memberLiveEventCap)
	}
	for _, ev := range buffered {
		if ev.Text == "OLDEST" {
			t.Fatal("the cap must drop the oldest events, not the newest")
		}
	}
}

// TestMemberPromptEventsStayOutOfLiveBuffer: ReplayPendingPrompts owns
// re-emitting an approval/ask on bind, so buffering them too would raise the
// same decision card twice on one switch.
func TestMemberPromptEventsStayOutOfLiveBuffer(t *testing.T) {
	m := promptTestTUI(t, nil)
	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{
		Kind: event.ApprovalRequest, Approval: event.Approval{ID: "a1"}}})
	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{
		Kind: event.AskRequest, Ask: event.Ask{ID: "q1"}}})
	if got := len(m.teamPick.session.live["alice"]); got != 0 {
		t.Fatalf("live buffer = %d events, want prompts left to ReplayPendingPrompts", got)
	}
}

// TestBoundSessionFocusTracksCurrent pins the invariant that made a focus-based
// prompt selector dead code: in a bound session every path moves focus and
// current together, so there is no independent cursor to read. A future
// "answer the focused member" must not come back — the keys answer whichever
// member is waiting instead.
func TestBoundSessionFocusTracksCurrent(t *testing.T) {
	m := promptTestTUI(t, nil)
	s := m.teamPick.session
	if len(s.members) == 0 {
		t.Fatal("a bound session must carry its roster")
	}
	if got := s.members[s.focus]; got != s.current {
		t.Fatalf("focus indexes %q but current is %q", got, s.current)
	}
	if cmd := m.stepSession(+1); cmd == nil {
		t.Skip("no second member to step to")
	}
	s = m.teamPick.session
	if got := s.members[s.focus]; got != s.current {
		t.Fatalf("after a step, focus indexes %q but current is %q", got, s.current)
	}
}
