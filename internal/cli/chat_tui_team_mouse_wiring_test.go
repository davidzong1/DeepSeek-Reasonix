package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The frame's MouseMode is the only thing that decides whether bubbletea
// delivers a click at all. Deciding it from "the team overlay object exists"
// (m.teamPick != nil) instead of the overlay's modality turned capture off for
// a *bound* member session too, so [ TEAM ] and the status-line member buttons
// rendered but never received a MouseClickMsg — the click routes below were
// sound, they just never ran. These tests pin the wiring, not the helper: the
// boundary test next door calls overlayMouseMode() directly, which stayed green
// for as long as View() stopped calling it.
func TestTeamSessionViewKeepsMouseCaptured(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	if !m.teamSessionBound() {
		t.Fatalf("precondition: the fixture must land in a bound member session")
	}
	if m.teamOverlayModal() {
		t.Fatalf("precondition: a bound member session is not modal")
	}
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("a bound member session must keep the mouse captured so the "+
			"[ TEAM ] and member buttons stay clickable, got MouseMode=%v", got)
	}
}

// TestTeamModalPageReleasesMouse pins the other half: the modal management page
// still hands the mouse back to the terminal for native selection.
func TestTeamModalPageReleasesMouse(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	if m.teamSessionBound() {
		m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	}
	if !m.teamOverlayModal() {
		t.Fatalf("precondition: the roster page must be modal")
	}
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("the modal roster must release the mouse, got MouseMode=%v", got)
	}
}

// TestTeamSessionViewMouseModeTracksOverlayState is the invariant itself, so a
// future edit to either side of the wiring fails here rather than shipping: the
// frame and the modality helper cannot disagree.
func TestTeamSessionViewMouseModeTracksOverlayState(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	if got, want := m.View().MouseMode, m.overlayMouseMode(); got != want {
		t.Errorf("bound session: View().MouseMode=%v, overlayMouseMode()=%v", got, want)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got, want := m.View().MouseMode, m.overlayMouseMode(); got != want {
		t.Errorf("modal page: View().MouseMode=%v, overlayMouseMode()=%v", got, want)
	}
}

// TestTeamButtonClickStillRoutesWhileBound drives a real left click through the
// update loop at the rendered [ TEAM ] hit box, proving the status row answers
// once the frame delivers clicks. It complements the MouseMode assertions above:
// those cover delivery, this covers the route the delivered click takes.
func TestTeamButtonClickStillRoutesWhileBound(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	if !m.teamSessionBound() {
		t.Fatalf("precondition: the fixture must land in a bound member session")
	}
	x, y, ok := teamButtonHitBox(m)
	if !ok {
		t.Fatalf("[ TEAM ] must be rendered on the status row while bound")
	}
	before := m.teamPick.session.panel
	next, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if got := next.(chatTUI).teamPick.session.panel; got == before {
		t.Fatalf("a click on [ TEAM ] must be routed while bound (panel stayed %v)", before)
	}
}

// teamButtonHitBox locates the rendered [ TEAM ] label in the final frame, the
// same way teamStatusButtonHit measures it: on the stripped line, in cells.
func teamButtonHitBox(m chatTUI) (int, int, bool) {
	for y, raw := range strings.Split(m.View().Content, "\n") {
		line := ansi.Strip(raw)
		before, _, found := strings.Cut(line, teamButtonText)
		if !found {
			continue
		}
		return visibleWidth(before) + 1, y, true
	}
	return 0, 0, false
}
