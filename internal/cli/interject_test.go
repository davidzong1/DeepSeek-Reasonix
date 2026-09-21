package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/sessioninbox"
)

type busyInboxController struct {
	control.SessionAPI
}

func (c *busyInboxController) Running() bool { return true }

func (c *busyInboxController) TryEnqueueFollowup(req control.InboxRequest) (sessioninbox.InboxReceipt, error) {
	return c.EnqueueInbox(req)
}

func TestInterjectQueuesWhileRunningWithoutOverwrite(t *testing.T) {
	m := newInboxTestChatTUI(t)
	m.state = tuiRunning

	m.input.SetValue("first")
	m0, _ := m.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m0.(chatTUI)

	m.input.SetValue("second")
	m0, _ = m.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m0.(chatTUI)

	m.input.SetValue("")
	m0, _ = m.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m0.(chatTUI)

	got := m.inboxBodies()
	if want := []string{"first", "second"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("inbox = %v, want %v (second must not overwrite first; empty must not queue)", got, want)
	}
	if m.state != tuiRunning {
		t.Fatalf("queuing input must not change state; got %v", m.state)
	}
}

func TestInterjectLeavesQueueOnTurnDoneForControllerDispatch(t *testing.T) {
	r := &blockingTurnRunner{started: make(chan struct{})}
	dir := t.TempDir()
	ctrl := newOwnedTestController(t, control.Options{Runner: r, Sink: event.Discard, SessionDir: dir, Label: "test"})
	ctrl.EnsureSessionPath()
	m := newChatTUI(ctrl, "", make(chan event.Event, 8), 80)
	m.ctrl = &busyInboxController{SessionAPI: ctrl}
	m.state = tuiRunning
	m.seedInbox("first", "second")

	// CLI no longer dequeues on TurnDone; durable items stay until the controller
	// admits and acks them. Pause so dispatch does not immediately consume.
	_ = m.ctrl.SetInboxPaused(true)
	m0, _ := m.update(agentEventMsg(event.Event{Kind: event.TurnDone}))
	m = m0.(chatTUI)

	got := m.inboxBodies()
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("TurnDone must not drop durable inbox items; got %v", got)
	}
}

func newInboxTestChatTUI(t *testing.T) chatTUI {
	t.Helper()
	dir := t.TempDir()
	ctrl := newOwnedTestController(t, control.Options{SessionDir: dir, Label: "test", Sink: event.Discard})
	ctrl.EnsureSessionPath()
	m := newTestChatTUI()
	m.ctrl = &busyInboxController{SessionAPI: ctrl}
	return m
}

// TestQueuedRowsStayInsideTheFrameBudget pins the frame against the durable
// queue rows. They render above the composer, and while the height budget did
// not carry them every queued follow-up pushed the frame one row past the
// terminal — which is what drops the last status row (Git + telemetry: ctx,
// cache, jobs) off-screen while work is queued, exactly the row an operator
// watches with items waiting.
func TestQueuedRowsStayInsideTheFrameBudget(t *testing.T) {
	for _, running := range []bool{false, true} {
		m := newInboxTestChatTUI(t)
		next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		m = next.(chatTUI)
		if running {
			m.state = tuiRunning
		}
		m.seedInbox("queued one", "queued two", "queued three")
		// Any update recomputes the reserved height, as production does.
		next, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		m = next.(chatTUI)

		queued := ansi.Strip(m.renderQueueIndicator())
		if queued == "" {
			t.Fatal("the fixture must actually render queue rows, or this test asserts nothing")
		}
		rows := strings.Count(queued, "\n") + 1
		if got := len(strings.Split(ansi.Strip(m.View().Content), "\n")); got != m.height {
			t.Fatalf("running=%v: View() rendered %d lines with %d queued row(s), want %d — the last status row must stay on screen",
				running, got, rows, m.height)
		}
	}
}
