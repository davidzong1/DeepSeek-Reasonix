package cli

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/team"
)

// promptKey is the ctrl+a approve chord as a terminal sends it: no Text, since
// ctrl+a carries no character (mirrors exitKey).
var promptApproveKey = tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl}

// promptDenyKey is the ctrl+x deny chord.
var promptDenyKey = tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}

// TestMemberPromptInboxRecordsAndClears pins the recording seam: a non-current
// member's approval/ask registers in the inbox by member id, a finished turn
// clears it, and the bound member's own prompt never enters — it is the modal's.
func TestMemberPromptInboxRecordsAndClears(t *testing.T) {
	m := overlayWithBackends(t, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.switchTeamMember("lead")
	if m.teamPick.session.prompts == nil {
		t.Fatal("the session must arm a prompt inbox")
	}

	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{
		Kind: event.ApprovalRequest, Approval: event.Approval{ID: "a1", Tool: "bash"}}})
	if got := m.teamPick.session.prompts["alice"]; got != (memberPrompt{kind: promptApproval, id: "a1"}) {
		t.Fatalf("recorded prompt = %+v, want approval a1", got)
	}

	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{
		Kind: event.AskRequest, Ask: event.Ask{ID: "q1"}}})
	if got := m.teamPick.session.prompts["alice"]; got != (memberPrompt{kind: promptAsk, id: "q1"}) {
		t.Fatalf("an ask replaces the record, got %+v", got)
	}

	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{Kind: event.TurnDone}})
	if _, ok := m.teamPick.session.prompts["alice"]; ok {
		t.Fatal("a finished turn must clear the recorded prompt")
	}

	m.handleMemberEvent(memberEventMsg{member: "lead", ev: event.Event{
		Kind: event.ApprovalRequest, Approval: event.Approval{ID: "a2"}}})
	if _, ok := m.teamPick.session.prompts["lead"]; ok {
		t.Fatal("the bound member's prompt must not enter the inbox")
	}
}

// promptProbeBackend is a stubBackend that also records Approve calls on a
// shared counter, so the TUI-bound path can assert hub routing reached that
// member's backend (stubBackend alone implements the bind surface but not the
// approval reply).
type promptProbeBackend struct {
	stubBackend
	approves *int
}

func (b promptProbeBackend) Approve(id string, allow, session, persist bool) {
	*b.approves++
}

// promptTestTUI opens the overlay with a registry whose backends record how many
// times Approve reached them, wires the hub, and binds the leader.
func promptTestTUI(t *testing.T, approves *int) chatTUI {
	t.Helper()
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	m.memberEvents = make(chan memberEvent, 8)
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return promptProbeBackend{stubBackend: stubBackend{label: b.MemberID}, approves: approves}, nil
	}, 4)
	m.teamPick.backends = m.teamBackends
	m.teamPick.hub = newTeamHub(m.teamPick.store, m.teamBackends, "alpha")
	// Both members are assembled: a background prompt can only exist because that
	// member's own backend raised it, and the hub answers the assembled backend
	// rather than building a fresh controller that holds no such prompt.
	for _, id := range []string{"alice", "lead"} {
		if cmd := m.switchTeamMember(id); cmd == nil {
			t.Fatalf("binding %s must arm the pump", id)
		}
	}
	return m
}

// TestMemberPromptKeyAnswersBackgroundApproval pins the whole path: ctrl+a
// approves and ctrl+x denies a non-current member's pending approval through
// the hub — the decision reaches that member's own backend without switching —
// and clears the inbox plus the prompt's own badge.
func TestMemberPromptKeyAnswersBackgroundApproval(t *testing.T) {
	approves := 0
	m := promptTestTUI(t, &approves)

	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{
		Kind: event.ApprovalRequest, Approval: event.Approval{ID: "a1", Tool: "bash", Subject: "run tests"}}})
	if got := m.teamPick.session.unread["alice"]; got != 1 {
		t.Fatalf("unread[alice] = %d, want 1", got)
	}

	next, cmd, consumed := m.handleTeamKey(promptApproveKey)
	m = next.(chatTUI)
	if !consumed {
		t.Fatal("ctrl+a must be consumed for a pending background approval")
	}
	if approves != 1 {
		t.Fatalf("approve routed = %d, want 1", approves)
	}
	if _, ok := m.teamPick.session.prompts["alice"]; ok {
		t.Fatal("answering must clear the prompt inbox")
	}
	if got := m.teamPick.session.unread["alice"]; got != 0 {
		t.Fatalf("answering must consume the prompt's badge, unread = %d", got)
	}
	if cmd != nil {
		t.Fatal("answering must not arm the event pump")
	}

	// ctrl+x denies a fresh prompt the same way.
	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{
		Kind: event.ApprovalRequest, Approval: event.Approval{ID: "a2"}}})
	if _, _, consumed := m.handleTeamKey(promptDenyKey); !consumed {
		t.Fatal("ctrl+x must be consumed for a pending background approval")
	}
	if approves != 2 {
		t.Fatalf("deny routed = %d, want 2", approves)
	}
}

// TestMemberPromptKeyFallsThroughWithoutPrompt pins the key hygiene: ctrl+a/x
// are the composer's when no background prompt is pending, the bound member's
// own prompt is the modal's, and a question card keeps the switch path while
// its record survives.
func TestMemberPromptKeyFallsThroughWithoutPrompt(t *testing.T) {
	m := promptTestTUI(t, nil)

	if _, _, consumed := m.handleTeamKey(promptApproveKey); consumed {
		t.Fatal("ctrl+a must fall through when no prompt is pending")
	}

	// The bound member's own prompt is answered by the modal's keys.
	m.pendingApproval = &event.Approval{ID: "mine", Tool: "bash"}
	if _, _, consumed := m.handleTeamKey(promptApproveKey); consumed {
		t.Fatal("the bound member's prompt must not consume the roster key")
	}
	m.pendingApproval = nil

	// A question card needs its own structured surface, so it is not offered to
	// the approve keys at all: they stay the composer's, while the ask stays
	// recorded and badged for the switch path.
	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{
		Kind: event.AskRequest, Ask: event.Ask{ID: "q1"}}})
	if _, _, consumed := m.handleTeamKey(promptApproveKey); consumed {
		t.Fatal("ctrl+a must not claim the key for a question card")
	}
	if got := m.teamPick.session.prompts["alice"]; got.id != "q1" {
		t.Fatalf("the ask must stay recorded for the switch path, got %+v", got)
	}
}
