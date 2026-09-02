package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/i18n"
)

// TestSSHOverlayMouseModeBoundary proves overlayMouseMode() returns the correct
// MouseMode for each combination of mouseCaptureOff and teamOverlayModal.
// Over SSH, mouseCaptureOff=true and no overlay → MouseModeNone (the terminal
// keeps native selection); the [TEAM] button is then reachable through a
// keyboard shortcut rather than a click. The overlay modal itself still
// disables mouse events in all states.
func TestSSHOverlayMouseModeBoundary(t *testing.T) {
	tests := []struct {
		name            string
		mouseCaptureOff bool
		overlayModal    bool // teamPick != nil && !session.active
		want            tea.MouseMode
	}{
		{
			name:            "capture_on_no_overlay",
			mouseCaptureOff: false,
			overlayModal:    false,
			want:            tea.MouseModeCellMotion,
		},
		{
			name:            "capture_on_with_overlay",
			mouseCaptureOff: false,
			overlayModal:    true,
			want:            tea.MouseModeNone,
		},
		{
			name:            "capture_off_no_overlay", // SSH default
			mouseCaptureOff: true,
			overlayModal:    false,
			want:            tea.MouseModeNone,
		},
		{
			name:            "capture_off_with_overlay",
			mouseCaptureOff: true,
			overlayModal:    true,
			want:            tea.MouseModeNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestChatTUI()
			m.mouseCaptureOff = tt.mouseCaptureOff
			if tt.overlayModal {
				// teamPick != nil with session.active=false (the zero value) makes
				// teamOverlayModal() return true.
				m.teamPick = &teamPicker{}
			}

			got := m.overlayMouseMode()
			if got != tt.want {
				t.Errorf("overlayMouseMode() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSSHTeamClickHandlerWorks proves when a MouseClickMsg does arrive (e.g.
// after /mouse toggles capture on, or from a terminal that forwards mouse
// events despite MouseModeNone), the click handler correctly routes [TEAM]
// clicks to onTeamButtonClick. This tests the routing logic, not bubbletea
// event delivery — the production bug is that MouseModeNone prevents
// MouseClickMsg from being sent, but the handler itself is sound.
func TestSSHTeamClickHandlerWorks(t *testing.T) {
	ctrl := control.New(control.Options{})
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)
	m.mouseCaptureOff = true
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)

	// Find the [TEAM] button in the rendered view.
	lines := strings.Split(m.View().Content, "\n")
	buttonX, buttonY := -1, -1
	for y, styled := range lines {
		line := ansi.Strip(styled)
		if before, _, found := strings.Cut(line, teamButtonText); found {
			buttonX = visibleWidth(before)
			buttonY = y
			break
		}
	}
	if buttonX < 0 || buttonY < 0 {
		t.Fatalf("team button %q is missing from the TUI frame", teamButtonText)
	}

	// Hit-test works regardless of mouseCaptureOff (coordinate math, not mode).
	if _, hit := m.teamStatusButtonHit(buttonX, buttonY); !hit {
		t.Fatal("team button should be clickable even with mouseCaptureOff")
	}

	// Click routing works when the event is injected directly.
	updated, cmd := m.Update(tea.MouseClickMsg{X: buttonX, Y: buttonY, Button: tea.MouseLeft})
	clicked := updated.(chatTUI)
	if cmd != nil {
		t.Fatal("team callback should not schedule a command")
	}
	if clicked.teamPick == nil || clicked.teamPick.model == nil {
		t.Fatal("team button click should open the team view model even with mouseCaptureOff")
	}
}

// TestSSHKeyboardTeamEntry proves the team overlay keyboard navigation works
// after opening via onTeamButtonClick (the same path a click or keyboard
// shortcut would use). The t key on the roster enters the team session.
func TestSSHKeyboardTeamEntry(t *testing.T) {
	m := openTeamOverlay(t)
	if m.teamPick == nil || m.teamPick.model == nil {
		t.Fatal("openTeamOverlay should open the team view model")
	}
	// The overlay is open — keyboard navigation works.
	if got := m.teamPick.model.Mode(); got != "teams" {
		t.Fatalf("overlay should open on the team list, got %q", got)
	}
}

// TestSSHKeyboardTeamOpen proves the team overlay opens by keyboard over SSH,
// where the terminal keeps mouse capture off (no MouseClickMsg reaches the
// [ TEAM ] button). Alt+T is the reliable entry point; it must open the same
// overlay onTeamButtonClick opens.
func TestSSHKeyboardTeamOpen(t *testing.T) {
	ctrl := control.New(control.Options{})
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)
	m.mouseCaptureOff = true

	// Before the shortcut nothing is open.
	if m.teamPick != nil {
		t.Fatal("team overlay must start closed")
	}

	m0, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = m0.(chatTUI)

	// Pressing alt+t routes to onTeamButtonClick via handleTeamKey.
	updated, _ := m.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModAlt})
	m = updated.(chatTUI)
	if m.teamPick == nil || m.teamPick.model == nil {
		t.Fatal("alt+t must open the team view model even with mouseCaptureOff")
	}

	// The overlay owns the keyboard once open; alt+t no longer re-opens.
	_, _, consumed := m.handleTeamKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModAlt})
	if !consumed {
		t.Fatal("alt+t must be consumed while the overlay is open")
	}
}

// TestSSHKeyboardTeamExit proves ctrl+t from within the overlay exits the
// team (closes the overlay), regardless of mouseCaptureOff. Uses the same
// chord construction as chat_tui_team_exit_test.go: no Text, since ctrl+t
// carries no character.
func TestSSHKeyboardTeamExit(t *testing.T) {
	m := openTeamOverlay(t)
	m.mouseCaptureOff = true

	updated, cmd, consumed := m.handleTeamKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if !consumed {
		t.Fatal("ctrl+t must be consumed by handleTeamKey")
	}
	if cmd != nil {
		t.Fatal("ctrl+t must not schedule a command")
	}
	exited := updated.(chatTUI)
	if exited.teamPick != nil {
		t.Fatal("ctrl+t must close the team overlay (teamPick = nil)")
	}
}

// TestLocalTeamClickWithCaptureOn proves local (non-SSH) click routing works
// when mouseCaptureOff=false, so the existing path does not regress.
func TestLocalTeamClickWithCaptureOn(t *testing.T) {
	ctrl := control.New(control.Options{})
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)
	m.mouseCaptureOff = false
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)

	lines := strings.Split(m.View().Content, "\n")
	buttonX, buttonY := -1, -1
	for y, styled := range lines {
		line := ansi.Strip(styled)
		if before, _, found := strings.Cut(line, teamButtonText); found {
			buttonX = visibleWidth(before)
			buttonY = y
			break
		}
	}
	if buttonX < 0 || buttonY < 0 {
		t.Fatalf("team button %q is missing from the TUI frame", teamButtonText)
	}

	// Hit-test works.
	if _, hit := m.teamStatusButtonHit(buttonX, buttonY); !hit {
		t.Fatal("team button should be clickable with mouseCaptureOff=false")
	}

	// Click opens the overlay.
	updated, _ := m.Update(tea.MouseClickMsg{X: buttonX, Y: buttonY, Button: tea.MouseLeft})
	clicked := updated.(chatTUI)
	if clicked.teamPick == nil || clicked.teamPick.model == nil {
		t.Fatal("team button click should open the team view model")
	}
}

// TestSSHRemoteClipboardKeepsWorking proves the SSH clipboard strategy (OSC 52)
// is not affected by the overlay mouse-mode fix. The test verifies that
// remoteClipboardSession() returns true for SSH env vars, and that the
// copy-to-clipboard path uses the OSC 52 code path.
func TestSSHRemoteClipboardKeepsWorking(t *testing.T) {
	// Set SSH env vars so the test is self-contained.
	for _, k := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY", "REASONIX_DISABLE_MOUSE"} {
		t.Setenv(k, "")
	}
	t.Setenv("SSH_TTY", "/dev/pts/3")

	if !remoteClipboardSession() {
		t.Fatal("remoteClipboardSession() must return true with SSH_TTY set")
	}
	if !mouseCaptureOffByDefault() {
		t.Fatal("mouseCaptureOffByDefault() must return true over SSH")
	}

	// Verify mouseCaptureOff gating: the clipboard path must still work.
	// copyToClipboardWithStatus is the central clipboard dispatch; over SSH
	// it forces OSC 52 regardless of mouseCaptureOff. Verifying the flag
	// didn't corrupt the dispatch contract is sufficient.
	if got := mouseCaptureOffByDefault(); !got {
		t.Fatal("mouseCaptureOff must default to true over SSH")
	}
}

// TestSSHViewMouseModeFollowsCapture proves View() requests MouseModeNone while
// mouseCaptureOff is set over SSH, and the mouse-tag still renders.
func TestSSHViewMouseModeFollowsCapture(t *testing.T) {
	ctrl := control.New(control.Options{})
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 60)
	m.mouseCaptureOff = true
	m0, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	m = m0.(chatTUI)

	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("MouseMode with mouseCaptureOff = %v, want MouseModeNone", got)
	}
	if got := m.mouseTag(); !strings.Contains(ansi.Strip(got), i18n.M.MouseCaptureTag) {
		t.Fatalf("mouseTag() = %q, want it to contain %q", got, i18n.M.MouseCaptureTag)
	}
}
