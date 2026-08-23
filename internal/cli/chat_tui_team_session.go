package cli

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/team"
	"reasonix/internal/team/agentruntime"
)

// sessionState is the team session window (§5/§11.4): the current member's
// agent window with the full roster beside it for switching. Switching changes
// only the display target — contexts are never copied or merged. The selected
// member persists through the TeamSessionStore seam (route §4.2); the UI never
// writes session files itself. The composer is the overlay's own input: it
// focuses on Enter, sends on Enter, and never touches the hidden chat
// composer.
type sessionState struct {
	active   bool
	teamName string
	current  string
	members  []string
	focus    int
	input    bool                       // session composer focused (§11.4)
	buf      string                     // composer draft; multiline
	errMsg   string                     // session-scoped error, separate from the roster's errMsg
	sub      *agentruntime.Subscription // live runtime subscription (§11.5); nil when none
	unread   map[string]int             // non-current members' terminal events, per member
}

// enterTeamSession opens the session window on the focused member, gated on
// the leader property read from the registry (control layer, §5 — never a UI
// marker): a non-leader is refused with a message. The window opens on the
// leader, the only member t can enter from, starts its runtime lazily, and
// subscribes its event stream (route §11.4/§11.5).
func (p *teamPicker) enterTeamSession() tea.Cmd {
	member, ok := p.model.Focused()
	if !ok {
		return nil
	}
	slot, ok := p.slotOf(member.ID)
	if !ok {
		return nil
	}
	if !slot.IsLeader() {
		p.errMsg = "Only the leader can start a team session"
		return nil
	}
	p.errMsg = ""
	session := sessionState{active: true, teamName: p.model.Name(), current: member.ID,
		unread: map[string]int{}}
	for i, m := range p.model.Members() {
		session.members = append(session.members, m.ID)
		if m.ID == member.ID {
			session.focus = i
		}
	}
	p.session = session
	p.startSessionTarget()
	p.persistSessionSelection()
	return p.bindSessionSubscription()
}

// persistSessionSelection writes the current member window through the
// session store (§4.2): the only persisted session data is the selection —
// histories live in the member context directories.
func (p *teamPicker) persistSessionSelection() {
	if p.sessions == nil {
		return
	}
	_ = p.sessions.WriteSelection(p.session.teamName, team.SessionSelection{
		Team:     p.session.teamName,
		MemberID: p.session.current,
	})
}

// sessionSpec assembles the member instance for the session window (route
// §11.4): the composite key and the slot's free-text role. The config snapshot
// stays empty until P4.2 wires provider resolution from the AgentUserRef.
func (p *teamPicker) sessionSpec(key agentruntime.InstanceKey) agentruntime.Spec {
	role := ""
	if slot, ok := p.slotOf(key.MemberID); ok {
		role = string(slot.Role)
	}
	return agentruntime.Spec{Key: key, Role: role}
}

// startSessionTarget starts the current member's runtime idempotently (route
// §11.4: t enters with the leader, switching lazily starts the target). A
// start failure is silent here — the send path retries and surfaces it.
func (p *teamPicker) startSessionTarget() {
	if p.runtime == nil {
		return
	}
	key := agentruntime.InstanceKey{Team: p.session.teamName, MemberID: p.session.current}
	if _, err := p.runtime.Start(context.Background(), p.sessionSpec(key)); err != nil {
		return
	}
}

// handleSessionKey routes a keypress inside the session window and reports
// whether it consumed the key: the composer owns every key while focused; in
// the browsing state up/down/j switch the current member's agent window
// (re-subscribing the event stream), enter focuses the composer, esc returns
// to the roster keeping every member's context (§5).
func (m *chatTUI) handleSessionKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	p := m.teamPick
	if p.session.input {
		return m.sessionInputKey(msg)
	}
	switch msg.String() {
	case "up":
		return true, m.stepSession(-1)
	case "down", "j":
		return true, m.stepSession(+1)
	case "enter":
		p.session.input = true
	case "r":
		return true, p.restartSessionTarget()
	case "esc", "ctrl+c":
		m.closeSession()
	}
	return true, nil
}

// sessionInputKey routes a keypress while the session composer is focused
// (§11.4): printable keys type into the draft, enter sends it, esc returns to
// the browsing state keeping the draft, shift+enter/alt+enter insert a
// newline, and ctrl+up/ctrl+down/tab switch the member window (re-subscribing
// its event stream). Plain up/down stay inert so a mid-draft arrow can never
// yank the window onto another member.
func (m *chatTUI) sessionInputKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	p := m.teamPick
	switch msg.String() {
	case "esc":
		p.session.input = false
	case "enter":
		p.sessionSend()
	case "shift+enter", "alt+enter":
		p.session.buf += "\n"
	case "ctrl+up":
		return true, m.stepSession(-1)
	case "ctrl+down":
		return true, m.stepSession(+1)
	case "tab":
		return true, m.stepSession(+1)
	case "shift+tab", "backtab":
		return true, m.stepSession(-1)
	case "backspace":
		if s := p.session.buf; s != "" {
			_, size := utf8.DecodeLastRuneInString(s)
			p.session.buf = s[:len(s)-size]
		}
	case "ctrl+c":
		// stop the current member's in-flight request — wired once P4.2's
		// runtime lands; inert now so ctrl+c never quits the overlay.
	default:
		if msg.String() == "space" {
			p.session.buf += " "
		} else if printableKey(msg.String()) {
			p.session.buf += msg.String()
		}
	}
	return true, nil
}

// sessionSend records the draft as a user message into the current member's
// context (§11.3/§11.4) and clears the composer. The runtime is started
// lazily on first use; a failed send keeps the draft and surfaces the error
// in the session window instead of dropping the text.
func (p *teamPicker) sessionSend() {
	text := p.session.buf
	if strings.TrimSpace(text) == "" {
		return
	}
	if p.runtime == nil {
		p.session.errMsg = "session runtime unavailable"
		return
	}
	key := agentruntime.InstanceKey{Team: p.session.teamName, MemberID: p.session.current}
	if _, err := p.runtime.Start(context.Background(), p.sessionSpec(key)); err != nil {
		p.session.errMsg = "session start failed: " + err.Error()
		return
	}
	if err := p.runtime.Send(key, text); err != nil {
		p.session.errMsg = "send failed: " + err.Error()
		return // the draft stays for a retry
	}
	p.session.buf = ""
	p.session.errMsg = ""
}

// stepSession moves the session window onto the next member of the roster:
// navigating IS binding, so the window's backend and the member the roster
// highlights can never disagree. A refused switch (a turn in flight) leaves the
// focus where it was and reports why.
//
// The legacy agentruntime target/subscription is still driven alongside: it is
// inert in production (no ProviderFactory is wired there) but keeps the old
// session seam consistent until R4 removes it, so nothing observes a member
// whose legacy instance was left running or subscribed.
func (m *chatTUI) stepSession(d int) tea.Cmd {
	p := m.teamPick
	n := len(p.session.members)
	if n == 0 {
		return nil
	}
	next := (p.session.focus + d + n) % n
	target := p.session.members[next]
	var cmd tea.Cmd
	// Only a wired registry can refuse: a missing one means no member backends
	// exist at all (tests, non-interactive hosts), which must not block display
	// navigation. A wired one returning no command is a real refusal.
	if m.teamBackends != nil {
		if cmd = m.switchTeamMember(target); cmd == nil {
			return nil // refused: keep showing whoever is bound
		}
	}
	p.session.focus = next
	p.session.current = target
	p.startSessionTarget()
	p.persistSessionSelection()
	delete(p.session.unread, target)
	return tea.Batch(cmd, p.bindSessionSubscription())
}

// closeSession tears the session window down and hands the window back to the
// chat's own backend. Member backends stay assembled in the registry: their
// histories, leases and in-flight state are untouched, so re-entering the team
// resumes them instead of rebuilding. The legacy runtime is stopped too, so no
// legacy instance outlives the window (R4 removes that half).
func (m *chatTUI) closeSession() {
	p := m.teamPick
	p.cancelSessionSubscription()
	if p.runtime != nil {
		_ = p.runtime.StopTeam(p.session.teamName)
	}
	p.session = sessionState{}
	m.unbindTeamMember()
}

// restartSessionTarget is the r retry entry (§11.6): it stops the current
// member's instance and starts it again, reusing the same runtime so event
// sequence and cursor continue across the restart. A never-assembled instance
// (Start failed at provider resolution) has nothing to stop — Start retries
// the assembly and, on success, re-subscribes the event stream.
func (p *teamPicker) restartSessionTarget() tea.Cmd {
	key := agentruntime.InstanceKey{Team: p.session.teamName, MemberID: p.session.current}
	if p.runtime == nil {
		p.session.errMsg = "session runtime unavailable"
		return nil
	}
	if err := p.runtime.Stop(key); err != nil && !errors.Is(err, agentruntime.ErrInstanceNotFound) {
		p.session.errMsg = "restart failed: " + err.Error()
		return nil
	}
	if _, err := p.runtime.Start(context.Background(), p.sessionSpec(key)); err != nil {
		p.session.errMsg = "restart failed: " + err.Error()
		return nil
	}
	p.session.errMsg = ""
	return p.bindSessionSubscription()
}

// sessionSlot returns the current member's persisted slot for the session
// window's info lines.
func (p *teamPicker) sessionSlot() (team.MemberSlot, bool) {
	return p.slotOf(p.session.current)
}
