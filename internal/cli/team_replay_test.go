package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/provider"
)

// replayHistory builds one member history of n user messages, each carrying an
// index the assertions can look for.
func replayHistory(n int) []provider.Message {
	history := make([]provider.Message, 0, n)
	for i := range n {
		history = append(history, userMessage(fmt.Sprintf("HISTORY-MSG-%02d", i)))
	}
	return history
}

// pendingReplayCmd runs the off-goroutine render the window is waiting on. The
// switch's own command is a batch with a pump read that never returns, so a test
// takes the replay command from the window's state instead.
func pendingReplayCmd(t *testing.T, m chatTUI) tea.Cmd {
	t.Helper()
	if m.replay.load == nil {
		t.Fatal("a long history must leave a full render pending")
	}
	return m.renderFullReplayCmd(*m.replay.load)
}

// replayReady runs the off-goroutine render the window handed back and unwraps
// it. The render must arrive as the roster tick's own message (see
// teamRosterRefreshMsg): the update loop has one branch for the tick's results,
// so a bare message type would be dropped without a trace.
func replayReady(t *testing.T, cmd tea.Cmd) teamReplayReadyMsg {
	t.Helper()
	tick, ok := cmd().(teamRosterRefreshMsg)
	if !ok || tick.replay == nil {
		t.Fatalf("the render command must deliver its paint through the roster tick, got %T", cmd())
	}
	return *tick.replay
}

// replayTick delivers one render result the way the loop does.
func replayTick(t *testing.T, ready teamReplayReadyMsg) tea.Msg {
	t.Helper()
	return teamRosterRefreshMsg{replay: &ready}
}

// frameReplayReady runs a command the frame handed back and returns the replay
// render it carries. Taking the render from the frame's own command — rather than
// rebuilding it from window state — is what pins the wiring: a render the frame
// forgets to hand back leaves the window on its tail forever, silently.
func frameReplayReady(t *testing.T, cmd tea.Cmd) teamReplayReadyMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("the frame must hand the replay render back to the loop")
	}
	collect := func(msg tea.Msg) (teamReplayReadyMsg, bool) {
		tick, ok := msg.(teamRosterRefreshMsg)
		if !ok || tick.replay == nil {
			return teamReplayReadyMsg{}, false
		}
		return *tick.replay, true
	}
	if ready, ok := collect(cmd()); ok {
		return ready
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, child := range batch {
			if child == nil {
				continue
			}
			if ready, ok := collect(child()); ok {
				return ready
			}
		}
	}
	t.Fatal("the frame's command carries no replay render")
	return teamReplayReadyMsg{}
}

// TestMemberReplayPaintsShortHistoryInline pins the boundary: a history that fits
// the inline window is rendered and wrapped before the switch returns, exactly as
// it always was, and leaves nothing in flight.
func TestMemberReplayPaintsShortHistoryInline(t *testing.T) {
	m := overlayWithBackends(t, map[string][]provider.Message{
		"lead":  replayHistory(replayInlineMessages),
		"alice": {userMessage("ALICE")},
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)

	if cmd := m.switchTeamMember("lead"); cmd == nil {
		t.Fatal("a successful switch must arm the member event pump")
	}
	if m.replay.load != nil {
		t.Fatal("a history that fits the inline window must not leave a render pending")
	}
	joined := strings.Join(m.transcript, "\n")
	for _, want := range []string{"HISTORY-MSG-00", "HISTORY-MSG-23"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the inline render must carry the whole history, missing %s:\n%s", want, joined)
		}
	}
	// The wrap is installed with the paint, so the frame after a switch re-wraps
	// nothing at all.
	contentW := transcriptContentWidth(80, m.nativeScrollback)
	if m.wrapWidth != contentW || m.wrapBlockCount != len(m.transcript) {
		t.Fatalf("wrap cache = (width %d, blocks %d), want (%d, %d)",
			m.wrapWidth, m.wrapBlockCount, contentW, len(m.transcript))
	}
}

// TestMemberReplayPaintsTailWindowThenRendersTheRestOffThread is the core of the
// off-thread replay: the switch paints the newest window and hands back a render
// for the rest, and the delivered bundle replaces that window with the whole
// history plus a wrap the frame can consume without wrapping anything.
func TestMemberReplayPaintsTailWindowThenRendersTheRestOffThread(t *testing.T) {
	const total = 60
	m := overlayWithBackends(t, map[string][]provider.Message{"lead": replayHistory(total)})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)

	if cmd := m.switchTeamMember("lead"); cmd == nil {
		t.Fatal("a successful switch must arm the member event pump")
	}
	windowed := strings.Join(m.transcript, "\n")
	if !strings.Contains(windowed, "HISTORY-MSG-59") {
		t.Fatalf("the window must show the newest history:\n%s", windowed)
	}
	if strings.Contains(windowed, "HISTORY-MSG-00") {
		t.Fatal("a long history must not be rendered synchronously in full")
	}
	contentW := transcriptContentWidth(80, m.nativeScrollback)
	if len(m.transcript) != 1 || m.wrapBlockCount != 1 || m.wrapWidth != contentW {
		t.Fatalf("windowed paint state = (blocks %d, wrapBlocks %d, wrapWidth %d), want (1, 1, %d)",
			len(m.transcript), m.wrapBlockCount, m.wrapWidth, contentW)
	}
	windowedLines := len(m.wrappedLines)

	// update (not Update) is the loop's msg switch: a stub backend answers only
	// what a bind reads, so the frame path is not available here.
	next, _ = m.update(replayTick(t, replayReady(t, pendingReplayCmd(t, m))))
	m = next.(chatTUI)

	full := strings.Join(m.transcript, "\n")
	for _, want := range []string{"HISTORY-MSG-00", "HISTORY-MSG-59"} {
		if !strings.Contains(full, want) {
			t.Fatalf("the installed bundle must carry the whole history, missing %s", want)
		}
	}
	if len(m.transcript) != 1 {
		t.Fatalf("the bundle replaces the window in place, got %d blocks", len(m.transcript))
	}
	if m.replay.load != nil {
		t.Fatal("the delivered bundle must clear the pending render")
	}
	if len(m.wrappedLines) <= windowedLines {
		t.Fatalf("the delivered bundle must carry the rest of the history's lines, got %d from %d",
			len(m.wrappedLines), windowedLines)
	}
	// The wrap the frame would have rebuilt lazily is the one that was installed:
	// no syncWrappedLines pass is needed for the frame to be correct.
	want := wrapBlockLines(m.transcript[0], contentW)
	if len(want) != len(m.wrappedLines) {
		t.Fatalf("installed wrap = %d lines, the lazy rebuild would produce %d", len(m.wrappedLines), len(want))
	}
	for i := range want {
		if m.wrappedLines[i] != want[i] {
			t.Fatalf("installed wrap line %d = %q, want %q", i, m.wrappedLines[i], want[i])
		}
	}
}

// TestMemberReplayDropsAPaintTheWindowMovedOnFrom pins the supersession guard: a
// render that lands after the window bound somebody else must not replace that
// member's transcript with the first one's history.
func TestMemberReplayDropsAPaintTheWindowMovedOnFrom(t *testing.T) {
	m := overlayWithBackends(t, map[string][]provider.Message{
		"lead":  replayHistory(60),
		"alice": {userMessage("ALICE")},
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.switchTeamMember("lead")
	lead := replayReady(t, pendingReplayCmd(t, m))

	m.switchTeamMember("alice")
	before := strings.Join(m.transcript, "\n")

	next, _ = m.update(replayTick(t, lead))
	m = next.(chatTUI)
	after := strings.Join(m.transcript, "\n")
	if after != before {
		t.Fatalf("a superseded paint must be dropped:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if strings.Contains(after, "HISTORY-MSG-00") {
		t.Fatal("the previous member's history must not be mounted on the bound one")
	}
}

// TestMemberReplayReissuesARenderTheResizeInvalidated pins the resize path: a
// bundle rendered for the old width is not installed, and the window re-renders
// at the current one instead of staying on the tail window forever.
func TestMemberReplayReissuesARenderTheResizeInvalidated(t *testing.T) {
	m := overlayWithBackends(t, map[string][]provider.Message{"lead": replayHistory(60)})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.switchTeamMember("lead")
	rendered := replayReady(t, pendingReplayCmd(t, m))

	m.width = 100 // the terminal resized while the bundle rendered
	windowed := strings.Join(m.transcript, "\n")

	cmd := m.handleTeamReplayReady(rendered)
	if cmd == nil {
		t.Fatal("a bundle rendered for a width the window left must be re-issued")
	}
	if joined := strings.Join(m.transcript, "\n"); joined != windowed {
		t.Fatal("a bundle rendered for the old width must not be installed")
	}

	next, _ = m.update(cmd())
	m = next.(chatTUI)
	joined := strings.Join(m.transcript, "\n")
	if !strings.Contains(joined, "HISTORY-MSG-00") || !strings.Contains(joined, "HISTORY-MSG-59") {
		t.Fatalf("the re-issued render must install the whole history:\n%s", joined)
	}
}

// TestMemberReplayReflowsAResizeOnTheWindow pins the resize half of the bounded
// replay: a width change repaints the block from its newest window like a bind
// does — never the whole member history inline — and re-arms the off-loop render
// for the rest.
func TestMemberReplayReflowsAResizeOnTheWindow(t *testing.T) {
	const total = 60
	m := overlayWithBackends(t, map[string][]provider.Message{"lead": replayHistory(total)})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.switchTeamMember("lead")

	// The bind's own bundle lands first, so the block holds the whole history.
	next, _ = m.update(replayTick(t, replayReady(t, pendingReplayCmd(t, m))))
	m = next.(chatTUI)
	if !strings.Contains(strings.Join(m.transcript, "\n"), "HISTORY-MSG-00") {
		t.Fatal("precondition: the delivered bind bundle carries the whole history")
	}
	if m.replay.load != nil {
		t.Fatal("precondition: the bind's render is done, so a pending load below is the resize's own")
	}

	// Native scrollback keeps the frame's own command to the replay render: the
	// mouse-re-enable timer is what a resize arms otherwise, and running it here
	// would only sleep for the rate-limit window.
	m.nativeScrollback = true
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = next.(chatTUI)
	joined := strings.Join(m.transcript, "\n")
	if !strings.Contains(joined, "HISTORY-MSG-59") {
		t.Fatalf("a resize must keep the newest history on screen:\n%s", joined)
	}
	if strings.Contains(joined, "HISTORY-MSG-00") {
		t.Fatal("a resize must reflow the block through its window, not re-render the whole history inline")
	}
	if m.replay.load == nil {
		t.Fatal("a resize must re-arm the off-loop render for the rest of the history")
	}

	next, _ = m.update(replayTick(t, frameReplayReady(t, cmd)))
	m = next.(chatTUI)
	joined = strings.Join(m.transcript, "\n")
	for _, want := range []string{"HISTORY-MSG-00", "HISTORY-MSG-59"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the re-rendered bundle must carry the whole history at the new width, missing %s", want)
		}
	}
	if len(m.transcript) != 1 || m.replay.load != nil {
		t.Fatalf("the bundle replaces the window in place and clears the load, got blocks=%d load=%v",
			len(m.transcript), m.replay.load != nil)
	}
	contentW := transcriptContentWidth(100, m.nativeScrollback)
	want := wrapBlockLines(m.transcript[0], contentW)
	if len(want) != len(m.wrappedLines) {
		t.Fatalf("installed wrap = %d lines, the lazy rebuild would produce %d", len(m.wrappedLines), len(want))
	}
	for i := range want {
		if m.wrappedLines[i] != want[i] {
			t.Fatalf("installed wrap line %d = %q, want %q", i, m.wrappedLines[i], want[i])
		}
	}
}

// TestMemberReplayResizeStillRendersTheWholeHistory pins where a windowed block's
// history lives: the paint renders the window, but the block's source keeps the
// member's entire history, so a resize that arrives before the bundle does still
// re-renders all of it instead of freezing the tail window as the transcript.
func TestMemberReplayResizeStillRendersTheWholeHistory(t *testing.T) {
	const total = 60
	m := overlayWithBackends(t, map[string][]provider.Message{"lead": replayHistory(total)})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.switchTeamMember("lead") // window painted, whole-bundle render in flight

	next, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = next.(chatTUI)
	load := m.replay.load
	if load == nil {
		t.Fatal("a resize must leave a full render pending")
	}
	if len(load.history) != total {
		t.Fatalf("the re-armed render carries %d of %d messages: the tail window became the transcript",
			len(load.history), total)
	}

	next, _ = m.update(replayTick(t, replayReady(t, pendingReplayCmd(t, m))))
	m = next.(chatTUI)
	joined := strings.Join(m.transcript, "\n")
	for _, want := range []string{"HISTORY-MSG-00", "HISTORY-MSG-59"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the whole history must arrive after the resize, missing %s:\n%s", want, joined)
		}
	}
}

// TestMemberReplayPinsTheTailOnABindButNotAResize pins the difference between the
// two installs: a bind lands at the newest output, while a resize must leave a
// user who scrolled up into the window exactly where they are.
func TestMemberReplayPinsTheTailOnABindButNotAResize(t *testing.T) {
	m := overlayWithBackends(t, map[string][]provider.Message{"lead": replayHistory(60)})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.switchTeamMember("lead")
	next, _ = m.update(replayTick(t, replayReady(t, pendingReplayCmd(t, m))))
	m = next.(chatTUI)
	if !m.forceGotoBottom {
		t.Fatal("a bind must keep the frame on the member's newest output")
	}

	m.forceGotoBottom = false
	next, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = next.(chatTUI)
	next, _ = m.update(replayTick(t, replayReady(t, pendingReplayCmd(t, m))))
	m = next.(chatTUI)
	if m.forceGotoBottom {
		t.Fatal("a resize must not yank a frame that may be scrolled up into the window")
	}
}

// TestSessionReplayBlockStaysWhole pins the other kind of replay bundle: the chat
// session's own /resume, /rewind and /branch commit a block that is not windowed,
// so it commits and reflows whole — and arms no off-loop render, because there is
// nothing left to render. The bounded window is a member bind's business; applying
// it here would silently drop everything but the newest turns of a resumed session.
func TestSessionReplayBlockStaysWhole(t *testing.T) {
	m := overlayWithBackends(t, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)

	const total = 60
	m.commitTranscriptSource(transcriptSource{kind: transcriptSourceReplayBundle, history: replayHistory(total)})
	if joined := strings.Join(m.transcript, "\n"); !strings.Contains(joined, "HISTORY-MSG-00") {
		t.Fatalf("a session replay must commit its whole history:\n%s", joined)
	}
	if cmd := m.reflowReplayBundle(); cmd != nil {
		t.Fatal("a complete session replay has nothing to re-render off the loop")
	}

	m.reflowTranscript(100)
	joined := strings.Join(m.transcript, "\n")
	for _, want := range []string{"HISTORY-MSG-00", "HISTORY-MSG-59"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("a resize must not window a session replay, missing %s:\n%s", want, joined)
		}
	}
}

// TestThemeFrameSkipsWhileABackgroundRenderOwnsThePalette pins the guard that
// makes the off-thread render safe: a per-frame palette swap (the theme sweep)
// does not wait for a render in flight, and the render's read side still excludes
// a palette change.
func TestThemeFrameSkipsWhileABackgroundRenderOwnsThePalette(t *testing.T) {
	previous := activeCLITheme
	t.Cleanup(func() { activeCLITheme = previous })

	m := overlayWithBackends(t, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)

	rendering := make(chan struct{})
	release := make(chan struct{})
	go func() {
		withThemeMaterialized(func() {
			close(rendering)
			<-release
		})
	}()
	<-rendering

	done := make(chan string, 1)
	go func() { done <- m.frameWithTheme(cliLightTheme) }()
	select {
	case frame := <-done:
		if frame == "" {
			t.Fatal("a frame that loses the palette must still render")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a per-frame palette swap must not wait on a background render")
	}
	close(release)

	// A palette change taken while no render is running still lands.
	materializeTheme(func() { activeCLITheme = cliLightTheme })
	if activeCLITheme.name != cliLightTheme.name {
		t.Fatalf("active palette = %q, want %q", activeCLITheme.name, cliLightTheme.name)
	}
}
