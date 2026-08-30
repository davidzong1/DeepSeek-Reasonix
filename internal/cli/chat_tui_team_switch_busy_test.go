package cli

// Regression tests for the thinking-state switch contract (§4.5): Running
// members switch freely (events badge unread, transcripts rebuild on return),
// while pending prompts refuse and replay via ReplayPendingPrompts.

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// overlayBusy opens the overlay with per-member busy state and replay counters:
// the bound backend's RuntimeStatus feeds the switch gate, so a test can pin
// exactly which condition lets a switch through and which refuses it, and the
// counters observe how often binding a member asked it to replay its prompts.
func overlayBusy(t *testing.T, status map[string]control.RuntimeStatus) (chatTUI, map[string]*int) {
	t.Helper()
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	m.memberEvents = make(chan memberEvent, 8)
	replays := map[string]*int{}
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		n := 0
		replays[b.MemberID] = &n
		return stubBackend{label: b.MemberID, status: status[b.MemberID], replays: &n}, nil
	}, 4)
	return m, replays
}

func sized(t *testing.T, m chatTUI) chatTUI {
	t.Helper()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return next.(chatTUI)
}

// TestSwitchTeamMemberWhileRunningSucceeds pins the core of the fix: a bound
// member whose turn is in flight (Running) no longer refuses the switch — the
// window moves on, and the turn keeps running on the member's own backend.
func TestSwitchTeamMemberWhileRunningSucceeds(t *testing.T) {
	m, _ := overlayBusy(t, map[string]control.RuntimeStatus{"lead": {Running: true}})
	m = sized(t, m)
	m.switchTeamMember("lead") // bind the running member
	if cmd := m.switchTeamMember("alice"); cmd == nil {
		t.Fatal("a running member must not refuse the switch")
	}
	if got := m.teamPick.session.current; got != "alice" {
		t.Fatalf("current member = %q, want alice", got)
	}
	if m.teamPick.session.errMsg != "" {
		t.Fatalf("a running switch must not refuse, got %q", m.teamPick.session.errMsg)
	}
}

// TestStepSessionWhileRunningSwitches pins the keyboard path: ctrl+down from a
// running member switches the session, exactly as it does from an idle one.
func TestStepSessionWhileRunningSwitches(t *testing.T) {
	m, _ := overlayBusy(t, map[string]control.RuntimeStatus{"lead": {Running: true}})
	m = sized(t, m)
	m.switchTeamMember("lead")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl})
	if got := m.teamPick.session.current; got != "alice" {
		t.Fatalf("ctrl+down from a running member should switch to alice, got %q", got)
	}
	if m.teamPick.session.errMsg != "" {
		t.Fatalf("the switch must not refuse, got %q", m.teamPick.session.errMsg)
	}
}

// TestSwitchTeamMemberBackgroundJobsAllowed pins BackgroundJobs beside Running:
// a background job is not a foreground turn, so it does not block the switch.
func TestSwitchTeamMemberBackgroundJobsAllowed(t *testing.T) {
	m, _ := overlayBusy(t, map[string]control.RuntimeStatus{"lead": {BackgroundJobs: 2}})
	m = sized(t, m)
	m.switchTeamMember("lead")
	if cmd := m.switchTeamMember("alice"); cmd == nil {
		t.Fatal("background jobs must not refuse the switch")
	}
	if got := m.teamPick.session.current; got != "alice" {
		t.Fatalf("current member = %q, want alice", got)
	}
}

// TestRunningMemberBackgroundEventsBadgeUnread pins the unread contract for a
// switched-away running member: its finished turn badges, its streaming deltas
// never badge, and binding it back consumes the badge — the transcript never
// shows another member's events.
func TestRunningMemberBackgroundEventsBadgeUnread(t *testing.T) {
	m, _ := overlayBusy(t, map[string]control.RuntimeStatus{"lead": {Running: true}})
	m = sized(t, m)
	m.switchTeamMember("lead")
	m.switchTeamMember("alice")
	before := len(m.transcript)

	if cmd := m.handleMemberEvent(memberEventMsg{
		member: "lead",
		ev:     event.Event{Kind: event.TurnDone},
	}); cmd == nil {
		t.Error("the pump must re-arm after the running member's turn ends")
	}
	if len(m.transcript) != before {
		t.Error("the running member's TurnDone must not touch the alice transcript")
	}
	if got := m.teamPick.session.unread["lead"]; got != 1 {
		t.Errorf("unread[lead] = %d, want 1", got)
	}
	m.handleMemberEvent(memberEventMsg{member: "lead", ev: event.Event{Kind: event.Text, Text: "x"}})
	if got := m.teamPick.session.unread["lead"]; got != 1 {
		t.Errorf("a streaming delta must not badge, unread[lead] = %d", got)
	}

	m.switchTeamMember("lead")
	if got := m.teamPick.session.unread["lead"]; got != 0 {
		t.Errorf("binding the running member back must clear its badge, got %d", got)
	}
}

// TestSwitchBackWhileRunningRebuildsTranscript pins the transcript contract on
// return: switching back to the running member rebuilds the window from that
// member's own history, and the switched-away member's context does not linger.
func TestSwitchBackWhileRunningRebuildsTranscript(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	m.memberEvents = make(chan memberEvent, 8)
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		st := control.RuntimeStatus{}
		if b.MemberID == "lead" {
			st.Running = true
		}
		return stubBackend{label: b.MemberID, status: st, history: map[string][]provider.Message{
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
		t.Fatalf("the running member's transcript must replay on return:\n%s", joined)
	}
	if strings.Contains(joined, "ALICE-HISTORY") {
		t.Fatalf("the switched-away member's context must not linger:\n%s", joined)
	}
}

// TestSwitchTeamMemberPendingPromptRefused pins the first half of the pending
// gate: a member blocked on a user prompt (PendingPrompt) still refuses the
// switch — leaving would strand the turn without its input.
func TestSwitchTeamMemberPendingPromptRefused(t *testing.T) {
	m, _ := overlayBusy(t, map[string]control.RuntimeStatus{"lead": {PendingPrompt: true}})
	m = sized(t, m)
	m.switchTeamMember("lead")
	if cmd := m.switchTeamMember("alice"); cmd != nil {
		t.Fatal("a pending prompt must refuse the switch")
	}
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("the refused switch must keep lead bound, got %q", got)
	}
	if m.teamPick.session.errMsg == "" {
		t.Error("the refusal must say why")
	}
}

// TestSwitchTeamMemberPendingApprovalRefused pins the approval half of the
// pending gate: a tool approval on screen refuses the switch, so the user's
// answer cannot be separated from the turn waiting for it.
func TestSwitchTeamMemberPendingApprovalRefused(t *testing.T) {
	m, _ := overlayBusy(t, nil)
	m = sized(t, m)
	m.switchTeamMember("lead")
	m.pendingApproval = &event.Approval{ID: "a1", Tool: "bash"}
	if cmd := m.switchTeamMember("alice"); cmd != nil {
		t.Fatal("a pending approval must refuse the switch")
	}
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("the refused switch must keep lead bound, got %q", got)
	}
	if m.teamPick.session.errMsg == "" {
		t.Error("the refusal must say why")
	}
}

// TestSwitchTeamMemberChooserRefused pins the chooser half of the pending gate:
// an open ask-card refuses the switch — its answer belongs to the member whose
// turn is paused on it.
func TestSwitchTeamMemberChooserRefused(t *testing.T) {
	m, _ := overlayBusy(t, nil)
	m = sized(t, m)
	m.switchTeamMember("lead")
	m.chooser = newChooser(event.Ask{ID: "q1"})
	if cmd := m.switchTeamMember("alice"); cmd != nil {
		t.Fatal("an open chooser must refuse the switch")
	}
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("the refused switch must keep lead bound, got %q", got)
	}
}

// TestEscUnderApprovalClearsCardReplayRestores pins the preservation contract:
// esc out from under a pending approval clears the card from the chat window,
// but re-entering that member asks its backend to replay the still-blocking
// prompt — the replayed event reaches the bound window and the card is back,
// exactly where the user left it.
func TestEscUnderApprovalClearsCardReplayRestores(t *testing.T) {
	m, replays := overlayBusy(t, nil)
	m = sized(t, m)
	m.switchTeamMember("lead")
	m.switchTeamMember("alice")
	m.pendingApproval = &event.Approval{ID: "a1", Tool: "bash"}

	m.closeSession() // esc: the window returns to the chat backend
	if got := m.teamPick.session.active; got {
		t.Fatal("closing the session must leave the session window")
	}
	if m.pendingApproval != nil {
		t.Fatal("the window must not carry alice's approval into the chat backend")
	}

	// Re-enter and switch back: binding alice asks her backend to replay the
	// prompt, and the replayed event restores the card in the bound window.
	m.teamPick.session = sessionState{active: true, teamName: "alpha", current: "lead",
		members: []string{"lead", "alice"}, unread: map[string]int{}}
	before := *replays["alice"]
	m.switchTeamMember("alice")
	if got := *replays["alice"]; got != before+1 {
		t.Fatalf("binding alice must ask her backend to replay prompts, got %d want %d", got, before+1)
	}
	// Route the replayed event through the member handler, not a full Update
	// frame: the status line reads sub-ports a stub backend does not implement.
	m.handleMemberEvent(memberEventMsg{member: "alice",
		ev: event.Event{Kind: event.ApprovalRequest, Approval: event.Approval{ID: "a1", Tool: "bash"}}})
	if m.pendingApproval == nil || m.pendingApproval.ID != "a1" {
		t.Fatalf("the replayed approval must restore the card, got %+v", m.pendingApproval)
	}
}

// TestReplayWithNoPendingShowsNoCard pins the idempotent tail of the replay:
// binding an idle member re-asks (the call is unconditional), but nothing
// re-emits, so no card can appear out of nowhere.
func TestReplayWithNoPendingShowsNoCard(t *testing.T) {
	m, replays := overlayBusy(t, nil)
	m = sized(t, m)
	m.switchTeamMember("lead")
	m.switchTeamMember("alice")
	if got := *replays["alice"]; got < 1 {
		t.Fatalf("binding must ask the backend to replay prompts, got %d", got)
	}
	if m.pendingApproval != nil || m.chooser != nil {
		t.Fatal("an idle member's replay must not surface a card")
	}
}

// TestUnboundMemberApprovalIgnoredNotRendered pins the pending events of a
// switched-away member: its ApprovalRequest is not ingested into the bound
// window (no transcript line, no card) — the switch-back replay is what
// resurfaces it, never the bound member's window. Since P1, an unanswered
// background approval DOES badge the member (§P1: the "member waiting" state
// the leader must see), so the old "must not badge" assertion is inverted.
func TestUnboundMemberApprovalIgnoredNotRendered(t *testing.T) {
	m, _ := overlayBusy(t, nil)
	m = sized(t, m)
	m.switchTeamMember("lead")
	before := len(m.transcript)

	if cmd := m.handleMemberEvent(memberEventMsg{
		member: "alice",
		ev:     event.Event{Kind: event.ApprovalRequest, Approval: event.Approval{ID: "a1", Tool: "bash"}},
	}); cmd == nil {
		t.Error("the pump must re-arm after an unbound member's approval")
	}
	if len(m.transcript) != before {
		t.Error("an unbound member's approval must not touch the transcript")
	}
	if got := m.teamPick.session.unread["alice"]; got != 1 {
		t.Errorf("a background approval must badge the member, unread[alice] = %d", got)
	}
	if m.pendingApproval != nil {
		t.Fatal("an unbound member's approval must not surface a card")
	}
}

// TestUnboundMemberAskIgnoredNotRendered pins the ask half of the ignore rule:
// an unbound member's ask-card never reaches the bound window.
func TestUnboundMemberAskIgnoredNotRendered(t *testing.T) {
	m, _ := overlayBusy(t, nil)
	m = sized(t, m)
	m.switchTeamMember("lead")
	before := len(m.transcript)

	if cmd := m.handleMemberEvent(memberEventMsg{
		member: "alice",
		ev:     event.Event{Kind: event.AskRequest, Ask: event.Ask{ID: "q1"}},
	}); cmd == nil {
		t.Error("the pump must re-arm after an unbound member's ask")
	}
	if len(m.transcript) != before {
		t.Error("an unbound member's ask must not touch the transcript")
	}
	if m.chooser != nil {
		t.Fatal("an unbound member's ask must not surface a card")
	}
}

// TestRebindMemberAgentUserKeepsFullGate pins the rebuild gate beside the
// switch gate: /model rebind tears the member backend down, so a running turn
// still refuses it — Running is only freed for the window move, never for a
// rebuild that would kill the turn.
func TestRebindMemberAgentUserKeepsFullGate(t *testing.T) {
	m, _ := overlayBusy(t, map[string]control.RuntimeStatus{"lead": {Running: true}})
	m = sized(t, m)
	m.switchTeamMember("lead")
	m.rebindMemberAgentUser("u1")
	if m.teamPick.session.errMsg == "" {
		t.Fatal("rebinding a running member's model must refuse")
	}
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("the refused rebind must keep lead bound, got %q", got)
	}
}

// TestRebindMemberAgentUserRebuildsAndRebindsImmediately pins the fix: after
// persisting the new agent-user ref, the old backend is retired and a freshly
// assembled one is bound onto the window right away — the window never keeps
// serving a stale (retired) backend whose provider/credential were baked in at
// the previous assembly.
func TestRebindMemberAgentUserRebuildsAndRebindsImmediately(t *testing.T) {
	m, _ := overlayBusy(t, map[string]control.RuntimeStatus{})
	store, err := team.NewTeamStore(".")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddAgentUser(team.AgentUser{
		UserID: "u1", Provider: "openai", Model: "gpt-5.6",
		BaseURL: "https://x.example.com/v1", APIKey: "sk",
	}); err != nil {
		t.Fatal(err)
	}
	builds := 0
	closed := 0
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		builds++
		return stubBackend{label: b.AgentUserRef, closed: &closed}, nil
	}, 4)
	m.teamBackends.setFingerprint(newMemberBackendFingerprint(memberBackendDeps{users: store}))
	m = sized(t, m)
	m.switchTeamMember("lead")
	if builds != 1 {
		t.Fatalf("builds = %d, want 1 before the rebind", builds)
	}
	oldModel := m.ctrl.ModelRef()
	m.rebindMemberAgentUser("u1")
	if errMsg := m.teamPick.session.errMsg; errMsg != "" {
		t.Fatalf("an idle rebind must succeed, got %q", errMsg)
	}
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("the rebind must keep lead bound, got %q", got)
	}
	if builds != 2 {
		t.Errorf("builds = %d, want 2 (rebind must reassemble the backend)", builds)
	}
	if closed != 1 {
		t.Errorf("closed = %d, want 1 (the old backend must be retired)", closed)
	}
	if m.ctrl.ModelRef() == oldModel {
		t.Error("the window must be bound to the freshly assembled backend")
	}
}

// TestRebindMemberAgentUserFailedBuildKeepsServing pins the no-stranding
// contract: when the reassembly fails, the previous backend stays assembled
// and keeps serving the window — the rebind reports the error instead of
// leaving the window bound to a closed controller.
func TestRebindMemberAgentUserFailedBuildKeepsServing(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	if err := m.teamPick.store.AddAgentUser(team.AgentUser{
		UserID: "u1", Provider: "openai", Model: "gpt-5.6",
		BaseURL: "https://x.example.com/v1", APIKey: "sk",
	}); err != nil {
		t.Fatal(err)
	}
	closed := 0
	fail := false
	m.memberEvents = make(chan memberEvent, 4)
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		if fail {
			return nil, errors.New("no credential")
		}
		return stubBackend{label: b.AgentUserRef, closed: &closed}, nil
	}, 4)
	m.teamBackends.setFingerprint(newMemberBackendFingerprint(memberBackendDeps{users: m.teamPick.store}))
	m.teamPick.backends = m.teamBackends
	m.switchTeamMember("lead")

	fail = true
	m.rebindMemberAgentUser("u1")
	if m.teamPick.session.errMsg == "" {
		t.Fatal("a failed rebuild must surface the error")
	}
	if closed != 0 {
		t.Errorf("the previous backend must not be retired on a failed rebuild, closed = %d", closed)
	}
	if _, ok := m.teamBackends.bound("alpha", "lead"); !ok {
		t.Error("the previous backend must stay assembled")
	}
	if m.ctrl == nil {
		t.Fatal("the window must keep a serving backend")
	}
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("the member must stay bound after a failed rebind, got %q", got)
	}
}
