package cli

import (
	tea "charm.land/bubbletea/v2"

	"reasonix/internal/team"
)

// sessionState is the team session window (§5/§11.4): which member's Agent the
// window is bound to, and the roster it switches across. Switching changes only
// the bound backend — contexts are never copied or merged. The selected member
// persists through the TeamSessionStore seam (route §4.2); the UI never writes
// session files itself. Typing goes to the main composer, which submits to
// whichever member is bound.
type sessionState struct {
	active   bool
	panel    bool // detail panel; off by default so the member's history owns the frame
	teamName string
	current  string
	members  []string
	focus    int
	errMsg   string         // session-scoped error, separate from the roster's errMsg
	unread   map[string]int // non-current members' terminal events, per member
}

// setSessionPanel shows or hides the bound session's detail panel. Its rows come
// out of the transcript's height, so the tail is re-pinned: keeping the old
// offset would leave the newest output off-screen as the frame grows.
func (m *chatTUI) setSessionPanel(show bool) {
	if !m.teamSessionBound() || m.teamPick.session.panel == show {
		return
	}
	m.teamPick.session.panel = show
	m.forceGotoBottom = true
}

// sessionPanelHidden reports whether a bound session is rendering no panel at
// all — the default, so the member's own history fills the frame. The status
// line's member buttons stay the switch affordance either way.
func (p *teamPicker) sessionPanelHidden() bool {
	return p.session.active && !p.session.panel
}

// enterTeamSession opens the session window on the focused member and binds its
// Agent backend, gated on the leader property read from the registry (control
// layer, §5 — never a UI marker): a non-leader is refused with a message. The
// window opens on the leader, the only member t can enter from.
func (m *chatTUI) enterTeamSession() tea.Cmd {
	p := m.teamPick
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
	for i, sm := range p.model.Members() {
		session.members = append(session.members, sm.ID)
		if sm.ID == member.ID {
			session.focus = i
		}
	}
	p.session = session
	p.persistSessionSelection()
	return m.switchTeamMember(member.ID)
}

// restoreSession resumes the persisted member window when its member is still
// the roster's leader — the session gate is the leader property, mirroring the
// t key — and falls back to the focused team's leader session otherwise. An
// absent, unreadable, or stale selection is a fallback, never an error: the
// [TEAM] click opens the management page's leader window as it always has.
func (p *teamPicker) restoreSession() string {
	if p.sessions != nil {
		if teamName := p.model.Name(); teamName != "" {
			if sel, err := p.sessions.ReadSelection(teamName); err == nil && sel.MemberID != "" {
				if slot, ok := p.slotOf(sel.MemberID); ok && slot.IsLeader() {
					return p.openSession(sel.MemberID)
				}
			}
		}
	}
	return p.openSession("")
}

// openSession puts the overlay on the focused team's given member's window and
// reports the member to bind — session active, member current, the chat composer
// hidden, the roster beside it for switching (§11.4). An empty initial opens the
// first leader; a team with no leader returns "" and stays on the management
// page: the leader marker is the gate, mirroring the t key.
func (p *teamPicker) openSession(initial string) string {
	if p.sessions == nil {
		return ""
	}
	teamName := p.model.Name()
	if teamName == "" {
		return ""
	}
	current := initial
	if current == "" {
		current = p.firstLeader()
	}
	if current == "" {
		return ""
	}
	session := sessionState{active: true, teamName: teamName, current: current,
		unread: map[string]int{}}
	for i, m := range p.model.Members() {
		session.members = append(session.members, m.ID)
		if m.ID == current {
			session.focus = i
		}
	}
	p.session = session
	return current
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
	p.persistSessionSelection()
	delete(p.session.unread, target)
	return cmd
}

// closeSession tears the session window down and hands the window back to the
// chat's own backend. Member backends stay assembled in the registry: their
// histories, leases and in-flight state are untouched, so re-entering the team
// resumes them instead of rebuilding.
func (m *chatTUI) closeSession() {
	m.teamPick.session = sessionState{}
	m.unbindTeamMember()
}

// teamExitKey leaves the team from any overlay screen; teamExitHint names it in
// the help lines, so key and label cannot drift apart. Ctrl+T shadows the
// composer's transpose binding, but only while the overlay is up — a one-key way
// out of the team is worth more there.
const (
	teamExitKey  = "ctrl+t"
	teamExitHint = "Ctrl+T exit team"
)

// exitTeam leaves the whole team UI in one step, from any depth: the bound member
// is unbound too, so the window is back on the chat's own backend and its
// history. Esc still unwinds one layer at a time; this is the way straight out.
// Member backends survive, as they do across any overlay close.
//
// Like the x key, Ctrl+T drops the persisted selection and arms the suppression
// flag: the next [TEAM] click lands on the management page, and a deliberate t
// restores the auto-session (§11.4). The two exits differ only in that x parks
// on the management page while Ctrl+T closes the overlay outright.
func (m *chatTUI) exitTeam() {
	if m.teamPick == nil {
		return
	}
	m.closeSession()
	if m.quickPick != nil && m.quickPick.kind == quickPickerMemberAgentUser {
		m.quickPick = nil // it lists the bound member's models: it leaves with the team
	}
	m.teamPick.closeTeamOverlay()
	if m.teamPick.sessions != nil {
		_ = m.teamPick.sessions.WriteSelection(m.teamPick.model.Name(), team.SessionSelection{Team: m.teamPick.model.Name()})
	}
	m.teamSuppressAutoSession = true
	m.teamPick = nil
	// The overlay's rows go back to the transcript, so the tail is re-pinned:
	// keeping the old offset would leave the newest output off-screen.
	m.forceGotoBottom = true
}

// sessionSlot returns the current member's persisted slot for the session
// window's info lines.
func (p *teamPicker) sessionSlot() (team.MemberSlot, bool) {
	return p.slotOf(p.session.current)
}
