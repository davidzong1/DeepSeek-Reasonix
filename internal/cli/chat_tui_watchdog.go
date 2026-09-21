package cli

import (
	"fmt"
	"time"

	"reasonix/internal/event"
)

func (m *chatTUI) noteWatchdogRunning() {
	if m == nil || m.diagnostics == nil {
		return
	}
	ctrl := m.ctrl
	m.diagnostics.NoteRunning(func() {
		if ctrl != nil {
			ctrl.Cancel()
		}
	})
}

func (m *chatTUI) noteWatchdogIdle() {
	if m == nil || m.diagnostics == nil {
		return
	}
	m.diagnostics.NoteIdle()
}

// elapsedTickLive reports whether the elapsed-tick chain must keep running. With
// a watchdog installed the chain follows its armed generation — the thing the
// ticks exist to keep fed — rather than any one footer flag. A member switch
// resets m.state without ending the turn this window still services
// (bindBackend), and a chain that died there left the watchdog with no liveness
// proof at all: the next quiet stretch — a slow model or a long tool call emits
// no agent events for ten seconds — then read as a stall and cancelled a healthy
// turn out from under the user. A host with no watchdog (a test, an embedded
// frontend) keeps the historical rule: run while a turn is running.
func (m *chatTUI) elapsedTickLive() bool {
	if m == nil {
		return false
	}
	if m.diagnostics != nil {
		return m.diagnostics.Running()
	}
	return m.state == tuiRunning
}

// elapsedTickProgress refreshes the elapsed readout, the running-tool rows and
// the subagent progress. Those belong to the footer, so they follow m.state even
// though the tick chain above outlives it.
func (m *chatTUI) elapsedTickProgress() {
	if m == nil || m.state != tuiRunning {
		return
	}
	m.elapsed = int(time.Since(m.runStart).Seconds())
	m.tickToolRunning()
	m.tickSubagentProgress()
}

// noteTerminalSize publishes the geometry this frame is laid out for, so the
// watchdog can compare it with the real terminal and tell a frame sized for the
// wrong terminal from a healthy one.
//
// Team-agent: main-v2 has no terminal-size recovery; keep this and its caller on
// merge.
func (m *chatTUI) noteTerminalSize() {
	if m == nil || m.diagnostics == nil {
		return
	}
	m.diagnostics.NoteTerminalSize(m.width, m.height)
}

// noteWatchdogHeartbeat records work progress for the stall watchdog.
func (m *chatTUI) noteWatchdogHeartbeat(source string) {
	if m == nil || m.diagnostics == nil {
		return
	}
	m.diagnostics.NoteActiveHeartbeat(source)
}

func watchdogAgentSource(kind event.Kind) string {
	return fmt.Sprintf("agent:%d", kind)
}

func (m *chatTUI) updateWatchdogStatusProvider() {
	if m == nil || m.diagnostics == nil || m.ctrl == nil {
		return
	}
	ctrl := m.ctrl
	m.diagnostics.SetStatusProvider(func() string {
		st := ctrl.RuntimeStatus()
		return fmt.Sprintf("running=%v pending_prompt=%v cancel_requested=%v cancellable=%v background_jobs=%d",
			st.Running, st.PendingPrompt, st.CancelRequested, st.Cancellable, st.BackgroundJobs)
	})
}
