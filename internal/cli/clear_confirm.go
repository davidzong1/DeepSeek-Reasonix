package cli

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/i18n"
)

type clearConfirm struct {
	confirm int // 0 = clear, 1 = cancel
}

func (m chatTUI) handleClearConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "down", "left", "right", "j", "k", "tab", "shift+tab":
		if m.clearConfirm.confirm == 0 {
			m.clearConfirm.confirm = 1
		} else {
			m.clearConfirm.confirm = 0
		}
	case "y", "Y":
		return m.confirmClearContext()
	case "n", "N", "esc", "ctrl+c":
		m.clearConfirm = nil
	case "enter":
		if m.clearConfirm.confirm == 0 {
			return m.confirmClearContext()
		}
		m.clearConfirm = nil
	}
	return m, nil
}

// clearScope names the session /clear acted on when the window is bound to a
// team member. m.ctrl is then the member's own backend, so the rotation is that
// member's session alone: a bare "current context" reads as if the chat's own
// session — or the other members' — had been cleared too. Empty when the window
// is not on a member backend, i.e. when the chat's own session is the target.
func (m *chatTUI) clearScope() string {
	if !m.memberBackendBound() {
		return ""
	}
	return fmt.Sprintf(" (team %s · member %s)", m.teamPick.sessionTeamName(), m.boundMember())
}

// clearScopeDetail is the confirmation's scope statement: the default wording
// for the chat's own session, and for a bound clear the team/member it is
// limited to, so the destructive dialog cannot be mistaken for a fleet-wide
// wipe.
func (m *chatTUI) clearScopeDetail() string {
	if !m.memberBackendBound() {
		return "This deletes the current transcript from local history and keeps only the system prompt."
	}
	return fmt.Sprintf("This deletes only team %s's member %s transcript from local history and keeps its system prompt; the other members and the chat's own session are untouched.",
		m.teamPick.sessionTeamName(), m.boundMember())
}

func (m *chatTUI) clearContext() tea.Cmd {
	m.clearConfirm = nil
	scope := m.clearScope()
	if err := m.ctrl.ClearSession(); err != nil {
		m.notice(fmt.Sprintf("%s%s: %v", i18n.M.SlashClearFailed, scope, err))
		return nil
	}
	// Bound to a member this is a no-op: the member's own keeper rotated with the
	// session, and the ambient keeper still guards the chat's file (see
	// followSessionLease).
	m.followSessionLease()
	m.resetFreshContextView(true)
	m.notice(i18n.M.SlashClearDone + scope)
	return tea.ClearScreen
}

func (m chatTUI) confirmClearContext() (tea.Model, tea.Cmd) {
	return m, m.clearContext()
}

// resetFreshContextView re-seeds the view for a session that just started fresh
// — /new, /clear, a replayed branch. The mounted To-do panel is the replaced
// session's, so it is cleared for the owner it belongs to: the panel's own
// owner key is kept, so a list the current session produces later still mounts.
func (m *chatTUI) resetFreshContextView(clearTranscript bool) {
	m.finalizeStreamed()
	m.pending.Reset()
	m.reasoning.Reset()
	m.todo.reset(m.sessionTodoOwner())
	m.chooser = nil
	m.pendingApproval = nil
	m.bubblePending = false
	m.turnDiscarded = false
	m.sessionCostQuote = nil
	if clearTranscript {
		m.clearTranscriptDisplay()
		m.sessionSwitch = true
	} else {
		m.commitLine("")
	}
	m.commitTranscriptSource(transcriptSource{kind: transcriptSourceBanner})
	m.transcriptDirty = true
	m.forceGotoBottom = true
}

func (m chatTUI) renderClearConfirm() string {
	if m.clearConfirm == nil {
		return ""
	}
	w := max(viewWidth(m.width), 40)
	var b strings.Builder
	b.WriteString(i18n.M.SlashClearPrompt + "\n")
	b.WriteString(viewMeta(m.clearScopeDetail()) + "\n\n")
	b.WriteString(rowLine(m.clearConfirm.confirm == 0, 1, "", "Clear", false) + "\n")
	b.WriteString(rowLine(m.clearConfirm.confirm == 1, 2, "", "Cancel", false))
	return choicePanelStyle.Width(w).Render(b.String())
}
