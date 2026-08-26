package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/command"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/plugin"
	"reasonix/internal/provider"
	"reasonix/internal/skill"
	"reasonix/internal/team"
)

// twoMemberTeam is a leader plus one other member, both bound to a pool entry.
func twoMemberTeam() team.Team {
	return team.Team{Name: "alpha", Template: []team.MemberSlot{
		{MemberID: "lead", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive},
		{MemberID: "alice", Role: team.RoleTester, Status: team.MemberStatusActive},
	}}
}

// stubBackend is a control.SessionAPI carrying one member's history. Only the
// methods bindBackend reads are implemented; the rest stay nil so a call this
// test does not expect fails loudly instead of passing silently.
type stubBackend struct {
	control.SessionAPI
	label   string
	history []provider.Message
	closed  *int                  // nil when the test does not care about teardown
	status  control.RuntimeStatus // injected busy state; zero = idle
	replays *int                  // nil when the test does not assert prompt replay
}

func (s stubBackend) Label() string               { return s.label }
func (s stubBackend) ModelRef() string            { return s.label + "/model" }
func (s stubBackend) History() []provider.Message { return s.history }
func (s stubBackend) Commands() []command.Command { return nil }
func (s stubBackend) SlashSkills() []skill.Skill  { return nil }
func (s stubBackend) Host() *plugin.Host          { return nil }
func (s stubBackend) SessionPath() string         { return "" }

// ReplayPendingPrompts is what switchTeamMember calls after binding to resurface
// a member's pending approval/ask card (§4.5). Tests count the call; a real
// controller re-emits the blocked prompt onto its sink here.
func (s stubBackend) ReplayPendingPrompts() {
	if s.replays != nil {
		*s.replays++
	}
}

// Close is what retiring a backend calls: it releases the member's session lease
// in production, so a test that retires one must be able to observe it.
func (s stubBackend) Close() {
	if s.closed != nil {
		*s.closed++
	}
}

// RuntimeStatus is read by runtimeSwitchBusy on the bound backend, so a switch
// away from a member consults the member it is leaving. The zero value is idle:
// nothing in flight. Tests inject Running/PendingPrompt/BackgroundJobs to pin
// the switch gate's per-condition behavior.
func (s stubBackend) RuntimeStatus() control.RuntimeStatus { return s.status }

// overlayWithBackends opens the team overlay and wires a backend registry whose
// builder hands out a backend carrying that member's own history.
func overlayWithBackends(t *testing.T, history map[string][]provider.Message) chatTUI {
	t.Helper()
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	m.memberEvents = make(chan memberEvent, 8)
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return stubBackend{label: b.MemberID, history: history[b.MemberID]}, nil
	}, 4)
	return m
}

func userMessage(text string) provider.Message {
	return provider.Message{Role: provider.RoleUser, Content: text}
}

// TestSwitchTeamMemberRebindsBackendAndTranscript pins R2.1's core: binding a
// member swaps the window's backend and rebuilds the transcript from that
// member's own history — the previous member's context does not linger.
func TestSwitchTeamMemberRebindsBackendAndTranscript(t *testing.T) {
	m := overlayWithBackends(t, map[string][]provider.Message{
		"lead":  {userMessage("LEAD-HISTORY")},
		"alice": {userMessage("ALICE-HISTORY")},
	})
	// Width is needed before the transcript commits anything.
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)

	before := m.ctrl.Label()
	if cmd := m.switchTeamMember("lead"); cmd == nil {
		t.Fatal("a successful switch must arm the member event pump")
	}
	if got := m.ctrl.Label(); got == before || got != "lead" {
		t.Fatalf("switching must rebind the window's backend, label = %q", got)
	}
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("current member = %q", got)
	}
	if joined := strings.Join(m.transcript, "\n"); !strings.Contains(joined, "LEAD-HISTORY") {
		t.Fatalf("the transcript must replay the bound member's history:\n%s", joined)
	}

	m.switchTeamMember("alice")
	joined := strings.Join(m.transcript, "\n")
	if !strings.Contains(joined, "ALICE-HISTORY") {
		t.Fatalf("switching must replay the incoming member's history:\n%s", joined)
	}
	if strings.Contains(joined, "LEAD-HISTORY") {
		t.Fatalf("the outgoing member's transcript must not linger:\n%s", joined)
	}
}

// TestSwitchTeamMemberRefusesUnknownMember pins the refusal path: a member that
// is not on the team is reported in the session window, and the window keeps
// showing whoever was bound.
func TestSwitchTeamMemberRefusesUnknownMember(t *testing.T) {
	m := overlayWithBackends(t, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.switchTeamMember("lead")

	bound := m.ctrl.Label()
	if cmd := m.switchTeamMember("nobody"); cmd != nil {
		t.Error("a refused switch must not arm the pump")
	}
	if got := m.ctrl.Label(); got != bound {
		t.Errorf("a refused switch must keep the bound backend, got %q want %q", got, bound)
	}
	if m.teamPick.session.errMsg == "" {
		t.Error("a refused switch must say why")
	}
	if got := m.teamPick.session.current; got != "lead" {
		t.Errorf("current member = %q, want the still-bound lead", got)
	}
}

// TestMemberEventRoutingKeepsTranscriptToTheBoundMember pins the attribution
// contract: only the bound member's events reach the transcript; another
// member's finished turn becomes an unread badge instead.
func TestMemberEventRoutingKeepsTranscriptToTheBoundMember(t *testing.T) {
	m := overlayWithBackends(t, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.switchTeamMember("lead")
	before := len(m.transcript)

	if cmd := m.handleMemberEvent(memberEventMsg{
		member: "alice",
		ev:     event.Event{Kind: event.TurnDone},
	}); cmd == nil {
		t.Error("the pump must re-arm after an unbound member's event")
	}
	if len(m.transcript) != before {
		t.Error("an unbound member's event must not touch the transcript")
	}
	if got := m.teamPick.session.unread["alice"]; got != 1 {
		t.Errorf("unread[alice] = %d, want 1", got)
	}

	// Streaming deltas from an unbound member never badge.
	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{Kind: event.Text, Text: "x"}})
	if got := m.teamPick.session.unread["alice"]; got != 1 {
		t.Errorf("a delta must not badge, unread[alice] = %d", got)
	}

	// Binding a member consumes its badge.
	m.switchTeamMember("alice")
	if got := m.teamPick.session.unread["alice"]; got != 0 {
		t.Errorf("binding a member must clear its badge, got %d", got)
	}
}

// TestTeamBackendSeamInstallsChannelAndOptions pins R2.1b's wiring: the seam
// provides the tagged channel and the options a member inherits, and the
// registry is created from the overlay's own store.
func TestTeamBackendSeamInstallsChannelAndOptions(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	if m.teamBackends != nil {
		t.Fatal("no registry before the seam is installed")
	}
	// Without the seam, creating the registry is a no-op rather than a panic:
	// non-interactive hosts and tests never install it.
	m.bindTeamBackends(m.teamPick.store)
	if m.teamBackends != nil {
		t.Fatal("bindTeamBackends must be inert without the seam")
	}

	m.bindTeamBackendSeam(20, cliBuildOverrides{})
	if m.memberEvents == nil || cap(m.memberEvents) != memberEventBuffer {
		t.Fatalf("seam must install a buffered channel, cap = %d", cap(m.memberEvents))
	}
	if m.memberBackendBase == nil {
		t.Fatal("seam must install the options template")
	}
	if got := m.memberBackendBase().MaxSteps; got != 20 {
		t.Errorf("member options must inherit the session's max steps, got %d", got)
	}
	m.bindTeamBackends(m.teamPick.store)
	if m.teamBackends == nil {
		t.Fatal("the registry must be created once the seam is installed")
	}
}

// TestUpdateRoutesMemberEvent pins that the update loop actually reaches the
// member-event handler: the branch is the only thing connecting a member
// backend's output to the window.
//
// The backend is left as the ambient controller on purpose. Going through
// Update renders a frame, and the status line reads sub-ports (Goal,
// ToolApprovalMode, …) a partial control.SessionAPI stand-in does not
// implement — so a test that renders needs a real controller bound.
func TestUpdateRoutesMemberEvent(t *testing.T) {
	m := overlayWithBackends(t, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	if got := m.boundMember(); got != "lead" {
		t.Fatalf("the overlay should open bound to the leader, got %q", got)
	}

	after, cmd := m.Update(memberEventMsg{member: "alice", ev: event.Event{Kind: event.TurnDone}})
	m = after.(chatTUI)
	if cmd == nil {
		t.Error("routing a member event must re-arm the pump")
	}
	if got := m.teamPick.session.unread["alice"]; got != 1 {
		t.Errorf("update must route to the member handler, unread[alice] = %d", got)
	}
}

// TestClosingSessionHandsWindowBackToChat pins the reverse of binding: leaving
// the team session must return the window to the chat's own backend. Without
// this the ordinary conversation is unreachable once a member has been bound.
// Member backends stay assembled — their histories and leases are untouched.
func TestClosingSessionHandsWindowBackToChat(t *testing.T) {
	m := overlayWithBackends(t, map[string][]provider.Message{
		"lead": {userMessage("LEAD-HISTORY")},
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	ambient := m.ctrl.Label()

	m.switchTeamMember("lead")
	if m.ctrl.Label() != "lead" {
		t.Fatalf("expected the leader bound, got %q", m.ctrl.Label())
	}
	if m.ambient == nil {
		t.Fatal("binding a member must remember the chat's own backend")
	}

	m.closeSession()
	if got := m.ctrl.Label(); got != ambient {
		t.Errorf("closing the session must restore the chat backend, got %q want %q", got, ambient)
	}
	if m.ambient != nil {
		t.Error("the saved ambient backend must be released after restoring")
	}
	if _, ok := m.teamBackends.bound("alpha", "lead"); !ok {
		t.Error("the member backend must stay assembled across a close")
	}
	// Re-entering binds the same backend again rather than rebuilding it.
	m.teamPick.session = sessionState{active: true, teamName: "alpha", unread: map[string]int{}}
	m.switchTeamMember("lead")
	if got := m.ctrl.Label(); got != "lead" {
		t.Errorf("re-entering must rebind the member, got %q", got)
	}
}

// TestDestructiveOpsRetireMemberBackends pins the §11.6 gate on its new owner:
// before a team's contexts are cleared — member deletion, leader step-down, team
// deletion — every assembled member backend of that team is retired, so nothing
// keeps writing history into a tree that is being removed. Other teams' backends
// are untouched.
func TestDestructiveOpsRetireMemberBackends(t *testing.T) {
	closed := 0
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	m.memberEvents = make(chan memberEvent, 8)
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return stubBackend{label: b.MemberID, closed: &closed}, nil
	}, 8)
	m.teamPick.backends = m.teamBackends
	for _, b := range []team.MemberBinding{
		{Team: "alpha", MemberID: "lead"},
		{Team: "alpha", MemberID: "alice"},
		{Team: "other", MemberID: "lead"},
	} {
		if _, err := m.teamBackends.bind(b); err != nil {
			t.Fatal(err)
		}
	}

	if !m.teamPick.stopTeamBeforeClear("alpha") {
		t.Fatal("the clear gate must allow the operation once backends are retired")
	}
	if closed != 2 {
		t.Errorf("retiring alpha must close both its backends, closed = %d", closed)
	}
	for _, id := range []string{"lead", "alice"} {
		if _, ok := m.teamBackends.bound("alpha", id); ok {
			t.Errorf("alpha/%s must be retired before a destructive op", id)
		}
	}
	if _, ok := m.teamBackends.bound("other", "lead"); !ok {
		t.Error("another team's backend must survive")
	}
	// A team with nothing assembled is simply allowed through.
	if !m.teamPick.stopTeamBeforeClear("nobody") {
		t.Error("an unknown team must not block the operation")
	}
}

// TestClosingOverlayKeepsMemberBackends pins the deliberate asymmetry: closing
// the overlay does NOT retire backends. They hold each member's session lease and
// any in-flight turn, so reopening [ TEAM ] resumes them instead of rebuilding —
// only the cap or a destructive op retires one.
func TestClosingOverlayKeepsMemberBackends(t *testing.T) {
	m := overlayWithBackends(t, nil)
	m.teamPick.backends = m.teamBackends
	if _, err := m.teamBackends.bind(team.MemberBinding{Team: "alpha", MemberID: "lead"}); err != nil {
		t.Fatal(err)
	}
	m.teamPick.closeTeamOverlay()
	if _, ok := m.teamBackends.bound("alpha", "lead"); !ok {
		t.Error("closing the overlay must not retire member backends")
	}
}
