package cli

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
)

// The theme-sweep frame is the second place View() decides MouseMode, and the
// second hunk the Team-agent/upstream merge dropped: the sweep branch kept
// upstream's inline `mouseCaptureOff` test, which cannot express the overlay's
// modality — so while the modal roster page was up, the sweep animation asked
// the terminal to keep delivering clicks the roster's own design hands back to
// it. The main frame's regression test cannot see this branch (a sweep frame
// renders instead of the full frame), so this pins the sweep's half.
func TestThemeSweepFrameMouseModeFollowsOverlayModality(t *testing.T) {
	defer restoreThemeForTest(activeColorProfile, activeCLITheme)
	configureCLIThemeWithStyle("dark", "graphite")
	activeColorProfile = colorprofile.ANSI256
	from := resolveCLIThemeWithStyle("dark", "graphite")
	to := resolveCLIThemeWithStyle("light", "sandstone")

	for _, tt := range []struct {
		name string
		open func(*testing.T) chatTUI
		want tea.MouseMode
	}{
		{name: "bound member session keeps capture", open: openTeamOverlay, want: tea.MouseModeCellMotion},
		{name: "modal roster releases capture", open: openRoster, want: tea.MouseModeNone},
	} {
		t.Run(tt.name, func(t *testing.T) {
			writeTeamFixture(t, twoMemberTeam())
			m := tt.open(t)
			if got := m.teamOverlayModal(); got != (tt.want == tea.MouseModeNone) {
				t.Fatalf("precondition: teamOverlayModal()=%v with want=%v", got, tt.want)
			}
			// The sweep frame carries a MouseMode only when the view is not
			// committed to native scrollback — the branch the hunk lived in.
			m.nativeScrollback = false
			// Every precondition is pinned above (colour profile, width, two
			// different themes), so a sweep that refuses to start would leave this
			// guard asserting nothing — a skip here is the defect, not the fixture.
			if m.startThemeSweep(from, to) == nil || m.themeSweep == nil {
				t.Fatal("the sweep must start under an explicit colour profile, width " +
					"80, and two different themes")
			}
			view := m.View()
			if view.MouseMode != tt.want {
				t.Fatalf("sweep frame MouseMode=%v, want %v", view.MouseMode, tt.want)
			}
			if got, want := view.MouseMode, m.overlayMouseMode(); got != want {
				t.Fatalf("sweep frame disagrees with the modality helper: %v vs %v", got, want)
			}
		})
	}
}
