package cli

import (
	tea "charm.land/bubbletea/v2"

	"reasonix/internal/team"
)

// sessionState is the team session window (§5): the current member's agent
// window with the full roster beside it for switching. Switching changes only
// the display target — contexts are never copied or merged. The selected
// member persists through the TeamSessionStore seam (route §4.2) once the P2
// domain interface lands; the UI never writes session files itself.
type sessionState struct {
	active   bool
	teamName string
	current  string
	members  []string
	focus    int
}

// enterTeamSession opens the session window on the focused member, gated on
// the leader property read from the registry (control layer, §5 — never a UI
// marker): a non-leader is refused with a message. The window opens on the
// leader, the only member t can enter from.
func (p *teamPicker) enterTeamSession() {
	member, ok := p.model.Focused()
	if !ok {
		return
	}
	slot, ok := p.slotOf(member.ID)
	if !ok {
		return
	}
	if !slot.IsLeader() {
		p.errMsg = "Only the leader can start a team session"
		return
	}
	p.errMsg = ""
	session := sessionState{active: true, teamName: p.model.Name(), current: member.ID}
	for i, m := range p.model.Members() {
		session.members = append(session.members, m.ID)
		if m.ID == member.ID {
			session.focus = i
		}
	}
	p.session = session
	p.persistSessionSelection()
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

// handleSessionKey routes a keypress inside the session window and reports
// whether it consumed the key: up/down switch the current member's agent
// window, esc returns to the roster keeping every member's context (§5).
func handleSessionKey(p *teamPicker, msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "up":
		stepSession(p, -1)
	case "down", "j":
		stepSession(p, +1)
	case "esc", "ctrl+c":
		p.session = sessionState{}
	}
	return true
}

// stepSession moves the session focus around the roster, changing only which
// member's window is displayed, and persists the new selection.
func stepSession(p *teamPicker, d int) {
	if n := len(p.session.members); n > 0 {
		p.session.focus = (p.session.focus + d + n) % n
		p.session.current = p.session.members[p.session.focus]
		p.persistSessionSelection()
	}
}

// sessionSlot returns the current member's persisted slot for the session
// window's info lines.
func (p *teamPicker) sessionSlot() (team.MemberSlot, bool) {
	return p.slotOf(p.session.current)
}
