package cli

// P4 regression locks: session close keeps member backends assembled, a full
// registry close re-assembles on the next switch, and session nav clears badges
// and persists the selection.

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// TestP4CloseSessionKeepsBackendsAndResetsWindow pins the close semantics a
// cleanup must preserve: closing the session zeroes the window's own state
// (badges included) but the member backends stay assembled and untouched — the
// registry survives teardown, so re-entering resumes them instead of reaping.
func TestP4CloseSessionKeepsBackendsAndResetsWindow(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	m.memberEvents = make(chan memberEvent, 8)
	closed := map[string]*int{}
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		n := 0
		closed[b.MemberID] = &n
		return stubBackend{label: b.MemberID, closed: &n}, nil
	}, 4)
	m = sized(t, m)
	m.switchTeamMember("lead")
	m.switchTeamMember("alice")
	m.handleMemberEvent(memberEventMsg{member: "lead", ev: event.Event{Kind: event.TurnDone}})
	if got := m.teamPick.session.unread["lead"]; got != 1 {
		t.Fatalf("unread[lead] = %d, want 1", got)
	}

	m.closeSession()
	if m.teamPick.session.unread != nil {
		t.Fatal("close must zero the window's session state, badges included")
	}
	for _, member := range []string{"lead", "alice"} {
		if _, ok := m.teamBackends.bound("alpha", member); !ok {
			t.Fatalf("close must keep member %q assembled in the registry", member)
		}
		if got := *closed[member]; got != 0 {
			t.Fatalf("close must not retire member %q's backend, Close called %d times", member, got)
		}
	}
}

// TestP4FullRegistryCloseRebindReassembles pins the resilience contract of the
// registry teardown path: after a full close, the next switch re-assembles a
// fresh backend for the member instead of stranding the window on a closed one.
func TestP4FullRegistryCloseRebindReassembles(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	m.memberEvents = make(chan memberEvent, 8)
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return stubBackend{label: b.MemberID, history: map[string][]provider.Message{
			"lead":  {userMessage("LEAD-HISTORY")},
			"alice": {userMessage("ALICE-HISTORY")},
		}[b.MemberID]}, nil
	}, 4)
	m = sized(t, m)
	m.switchTeamMember("lead")
	m.teamBackends.closeAll()
	if cmd := m.switchTeamMember("lead"); cmd == nil {
		t.Fatal("a switch after a full registry close must re-assemble the backend")
	}
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("bound member = %q, want lead", got)
	}
	if got := m.ctrl.Label(); got != "lead" {
		t.Fatalf("the window must serve a freshly assembled backend, label = %q", got)
	}
}

// TestP4SessionNavClearsBadgeAndPersistsSelection pins the keyboard switch path:
// ctrl+down back to a badged member consumes its badge like a direct switch does
// and persists the selection to disk, so a cleanup touching stepSession cannot
// drop the accounting silently.
func TestP4SessionNavClearsBadgeAndPersistsSelection(t *testing.T) {
	m, _ := overlayBusy(t, nil)
	m = sized(t, m)
	m.switchTeamMember("lead")                                           // opens on focus 0
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl}) // ctrl+down → alice
	if got := m.teamPick.session.current; got != "alice" {
		t.Fatalf("the nav must reach the second member, got %q", got)
	}
	for _, kind := range []event.Kind{event.TurnDone, event.Message} {
		m.handleMemberEvent(memberEventMsg{member: "lead", ev: event.Event{Kind: kind}})
	}
	if got := m.teamPick.session.unread["lead"]; got != 2 {
		t.Fatalf("unread[lead] = %d, want 2", got)
	}

	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl}) // wraps back to lead
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("ctrl+down must wrap to the leader, got %q", got)
	}
	if got := m.teamPick.session.unread["lead"]; got != 0 {
		t.Fatalf("navigating back must clear the member's badge, got %d", got)
	}
	sel, err := m.teamPick.sessions.ReadSelection("alpha")
	if err != nil || sel.MemberID != "lead" {
		t.Fatalf("the nav must persist the selection, got %+v, %v", sel, err)
	}
}
