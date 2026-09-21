package cli

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"strings"
)

func (m chatTUI) maintenanceCancellable() bool {
	if m.maintenance == nil || m.ctrl == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(m.maintenance.Activity)) {
	case "", "starting", "running":
		return true
	default:
		return false
	}
}

func (m *chatTUI) runCompactCommand(input, typedCmd string) tea.Cmd {
	m.echoLocalCommand(input)
	// Register maintenance before returning so Esc and new input see its owner.
	// The worker runs in the background; trailing text remains summary guidance.
	if submitter, ok := m.ctrl.(interface {
		SubmitDisplayWithResult(display, input string) control.SubmitResult
	}); ok {
		result := submitter.SubmitDisplayWithResult(input, input)
		if result.OperationID != "" {
			op := &event.SessionOperationInfo{
				OperationID: result.OperationID,
				Kind:        "compact",
				Activity:    "running",
				Status:      "running",
			}
			m.maintenance = op
			m.renderSessionOperation(op)
		}
		return nil
	}

	// Compatibility path for older SessionAPI implementations. Keep an
	// explicit maintenance placeholder so Esc preserves the draft while the
	// blocking Compact call runs as a Bubble Tea command.
	focus := strings.TrimSpace(strings.TrimPrefix(input, typedCmd))
	m.maintenance = &event.SessionOperationInfo{Kind: "compact", Activity: "starting", Status: "running"}
	m.compactCompatibilityPending = true
	m.compactLifecycleObserved = false
	return func() tea.Msg { return compactDoneMsg{err: m.ctrl.Compact(context.Background(), focus)} }
}

func (m *chatTUI) stopMaintenance() {
	m.ctrl.Cancel()
	updated := *m.maintenance
	updated.Activity = "cancelling"
	updated.Status = "cancelling"
	m.maintenance = &updated
	m.renderSessionOperation(&updated)
}
