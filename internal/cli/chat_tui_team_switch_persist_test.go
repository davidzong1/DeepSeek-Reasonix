package cli

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/team"
)

// A click on a member button and a Ctrl+Up/Down step reach the same state, so
// they must leave the same state behind. switchTeamMember used to write only
// session.current: the roster's cursor (session.focus) stayed on the member the
// window had just left, which both mis-marked the panel's "> " row and made the
// next keyboard step start from the wrong index, and the selection the store
// persists was never updated — so a restart resumed the previous member.
func TestMemberButtonClickAlignsFocusAndPersists(t *testing.T) {
	m := overlayWithClickableBackends(t)
	if cmd := m.switchTeamMember("lead"); cmd == nil {
		t.Fatal("precondition: the leader must bind")
	}
	x, y, ok := memberButtonHitBox(m, "alice")
	if !ok {
		t.Fatal("precondition: alice's status-line button must be rendered")
	}
	next, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	m = next.(chatTUI)

	if got := m.teamPick.session.current; got != "alice" {
		t.Fatalf("the click must bind alice, got %q", got)
	}
	want := slices.Index(m.teamPick.session.members, "alice")
	if got := m.teamPick.session.focus; got != want {
		t.Errorf("session.focus=%d, want %d: the cursor must follow the bound member", got, want)
	}
	// The store is the durable half: the same contract the Ctrl+Up/Down path
	// already pins in TestTeamSessionSwitchPersistsAndEsc.
	sel, err := m.teamPick.sessions.ReadSelection("alpha")
	if err != nil || sel.MemberID != "alice" {
		t.Errorf("the click must persist the selection, got %+v err=%v", sel, err)
	}
	// And what the user reads: the panel's cursor sits on the member on screen.
	m.setSessionPanel(true)
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "> alice") {
		t.Errorf("the panel's cursor must mark the bound member, got:\n%s", got)
	}
}

// TestMemberClickKeepsKeyboardStepsInSync is the same defect seen from the
// keyboard: the step index is session.focus, so a click that left focus behind
// made the next Ctrl+Down re-select the member already on screen — a keypress
// that visibly did nothing.
func TestMemberClickKeepsKeyboardStepsInSync(t *testing.T) {
	m := overlayWithClickableBackends(t)
	if cmd := m.switchTeamMember("lead"); cmd == nil {
		t.Fatal("precondition: the leader must bind")
	}
	x, y, ok := memberButtonHitBox(m, "alice")
	if !ok {
		t.Fatal("precondition: alice's status-line button must be rendered")
	}
	next, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	m = next.(chatTUI)

	// From the last member the step wraps to the first: a step that had to
	// start from alice's own index is the only way this reaches lead.
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl})
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("a step after a click must move from the clicked member, got %q", got)
	}
}

// TestTeamEntryButtonDeliversClicksInPlainChat closes the other half of the
// entry path: the unbound chat must still ask the terminal for mouse events, or
// the [ TEAM ] click the frame renders can never arrive.
func TestTeamEntryButtonDeliversClicksInPlainChat(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	ctrl := control.New(control.Options{})
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	if m.teamPick != nil {
		t.Fatal("precondition: the plain chat has no overlay open")
	}
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("the plain chat must capture the mouse so [ TEAM ] is clickable, got MouseMode=%v", got)
	}
	x, y, ok := teamButtonHitBox(m)
	if !ok {
		t.Fatal("precondition: [ TEAM ] must be rendered")
	}
	next, _ = m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if opened := next.(chatTUI); opened.teamPick == nil {
		t.Fatal("a click on [ TEAM ] must open the overlay")
	}
}

// overlayWithClickableBackends is overlayWithBackends with backends that can
// serve a frame: View() reads the bound controller, and the switch test's stub
// embeds a nil control.SessionAPI, so only a real one can render.
func overlayWithClickableBackends(t *testing.T) chatTUI {
	t.Helper()
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	m.memberEvents = newMemberEventPump()
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return control.New(control.Options{}), nil
	}, 4)
	return m
}

// memberButtonHitBox locates one member's status-line button in the final frame,
// the way teamStatusButtonHit measures it: on the stripped line, in cells.
func memberButtonHitBox(m chatTUI, id string) (int, int, bool) {
	for y, raw := range strings.Split(m.View().Content, "\n") {
		line := ansi.Strip(raw)
		before, _, found := strings.Cut(line, memberButtonText(id))
		if !found {
			continue
		}
		return visibleWidth(before) + 1, y, true
	}
	return 0, 0, false
}
