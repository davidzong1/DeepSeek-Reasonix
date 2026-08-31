package cli

import (
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/event"
	"reasonix/internal/team"
)

// teamRosterRefreshMsg invalidates the in-memory roster while the team
// overlay remains open. Leader tools mutate team.json through their own store,
// so the TUI needs a small polling loop to observe those cross-session writes.
type teamRosterRefreshMsg struct{}

const teamRosterRefreshInterval = time.Second

func teamRosterRefresh() tea.Cmd {
	return tea.Tick(teamRosterRefreshInterval, func(time.Time) tea.Msg { return teamRosterRefreshMsg{} })
}

// refreshTeamRoster re-reads the registry and keeps the active session's
// member list aligned with disk. A removed current member falls back to the
// first remaining leader; a new member becomes immediately switchable.
func (m *chatTUI) refreshTeamRoster() tea.Cmd {
	if m == nil || m.teamPick == nil || m.teamPick.store == nil {
		return nil
	}
	p := m.teamPick
	teamName := p.model.Name()
	if err := p.reload(teamName); err != nil {
		p.errMsg = pickerErrMsg(err)
		return teamRosterRefresh()
	}
	if !p.session.active {
		return teamRosterRefresh()
	}
	ids := make([]string, 0, len(p.model.Members()))
	for _, member := range p.model.Members() {
		ids = append(ids, member.ID)
	}
	oldCurrent := p.session.current
	p.session.members = ids
	if i := slices.Index(ids, oldCurrent); i >= 0 {
		p.session.focus = i
		return teamRosterRefresh()
	}
	// The bound member was removed remotely. Rebind to the current leader when
	// possible, preserving the team session instead of forcing a reopen.
	leader := p.firstLeader()
	if leader == "" {
		m.closeSession()
		return teamRosterRefresh()
	}
	p.session.current = leader
	p.session.focus = slices.Index(ids, leader)
	if cmd := m.switchTeamMember(leader); cmd != nil {
		return tea.Batch(cmd, teamRosterRefresh())
	}
	return teamRosterRefresh()
}

// sessionState is the team session window (§5/§11.4): which member's Agent the
// window is bound to, and the roster it switches across. Switching changes only
// the bound backend — contexts are never copied or merged. The selected member
// persists through the TeamSessionStore seam (route §4.2); the UI never writes
// session files itself. Typing goes to the main composer, which submits to
// whichever member is bound.
// memberPrompt records one non-current member's pending approval/ask, so the
// leader can answer an approval by keybinding without switching members and the
// roster can mark a question card that still needs switching. kind is the
// decision surface; id correlates with the member's own backend's reply.
type memberPrompt struct {
	kind string // promptApproval | promptAsk
	id   string
}

// The prompt surfaces: an approval answers by keybinding through the hub, while
// a question card needs its structured card, so it keeps the switch path but
// stays recorded (and marked) until it is answered.
const (
	promptApproval = "approval"
	promptAsk      = "ask"
)

type sessionState struct {
	active   bool
	panel    bool // detail panel; off by default so the member's history owns the frame
	teamName string
	current  string
	members  []string
	focus    int
	errMsg   string                   // session-scoped error, separate from the roster's errMsg
	unread   map[string]int           // non-current members' terminal events, per member
	prompts  map[string]memberPrompt  // non-current members' pending approval/ask, per member
	live     map[string][]event.Event // non-current members' in-flight turn, replayed on switch
}

// newSessionState arms one team session window. The per-member maps are created
// together because every one of them is read unconditionally on the event path:
// a nil map there is a silently dropped member event, which is exactly how an
// in-flight turn became invisible.
func newSessionState(teamName, current string) sessionState {
	return sessionState{
		active: true, teamName: teamName, current: current,
		unread:  map[string]int{},
		prompts: map[string]memberPrompt{},
		live:    map[string][]event.Event{},
	}
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
// Agent backend. A non-leader entry auto-corrects to the team's leader — the
// session belongs to the leader (§11.4); only a team with no leader at all
// refuses. The leader property is read from the registry (control layer, §5 —
// never a UI marker).
func (m *chatTUI) enterTeamSession() tea.Cmd {
	p := m.teamPick
	member, ok := p.model.Focused()
	if !ok {
		return nil
	}
	if slot, ok := p.slotOf(member.ID); !ok || !slot.IsLeader() {
		leader := p.firstLeader()
		if leader == "" {
			p.refusal = "Only the leader can start a team session"
			return nil
		}
		p.model.FocusMember(leader) // the roster highlights the session's member
		member, ok = p.model.Focused()
		if !ok {
			return nil
		}
	}
	if p.defaultAgentUser() == "" {
		p.refusal = "Set a team default agent user before starting a team session (press g)"
		return nil
	}
	p.errMsg = ""
	p.refusal = ""
	session := newSessionState(p.model.Name(), member.ID)
	for i, sm := range p.model.Members() {
		session.members = append(session.members, sm.ID)
		if sm.ID == member.ID {
			session.focus = i
		}
	}
	p.session = session
	p.persistSessionSelection()
	return tea.Batch(m.switchTeamMember(member.ID), teamRosterRefresh())
}

// restoreSession resumes the persisted member window when its member is still
// the roster's leader — the session gate is the leader property, mirroring the
// t key — and falls back to the focused team's leader session otherwise. The
// second result reports a deliberate leave: suspended selections park on the
// management page, never a refusal. An absent, unreadable, or stale selection
// is a fallback, never an error: the [TEAM] click opens the management page's
// leader window as it always has.
func (p *teamPicker) restoreSession() (string, bool) {
	if p.sessions != nil {
		if teamName := p.model.Name(); teamName != "" {
			if sel, err := p.sessions.ReadSelection(teamName); err == nil && sel.Suspended {
				return "", true
			}
		}
	}
	if p.firstLeader() == "" {
		p.refusal = "Only the leader can start a team session"
		return "", false
	}
	if p.defaultAgentUser() == "" {
		p.refusal = "Set a team default agent user before starting a team session (press g)"
		return "", false
	}
	if p.sessions != nil {
		if teamName := p.model.Name(); teamName != "" {
			if sel, err := p.sessions.ReadSelection(teamName); err == nil {
				if sel.Suspended {
					return "", true
				}
				if slot, ok := p.slotOf(sel.MemberID); ok && slot.IsLeader() {
					return p.openSession(sel.MemberID), false
				}
			}
		}
	}
	return p.openSession(""), false
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
	if p.firstLeader() == "" {
		p.refusal = "Only the leader can start a team session"
		return ""
	}
	if p.defaultAgentUser() == "" {
		p.refusal = "Set a team default agent user before starting a team session (press g)"
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
	session := newSessionState(teamName, current)
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

// exitTeam tears the whole team UI down in one step, from any depth: the bound
// member is unbound too, so the window is back on the chat's own backend and its
// history. Esc still unwinds one layer at a time; this is the way straight out.
// Member backends survive, as they do across any overlay close.
//
// Teardown only. Whether the next entry reopens a session is a separate
// decision, persisted by suspendAutoSession — this function runs on every close,
// including a plain esc out of the team list, which must not change it.
func (m *chatTUI) exitTeam() {
	if m.teamPick == nil {
		return
	}
	m.closeSession()
	if m.quickPick != nil && m.quickPick.kind == quickPickerMemberAgentUser {
		m.quickPick = nil // it lists the bound member's models: it leaves with the team
	}
	// The member-backend registry and its board deliberately survive: an assembled
	// member keeps running its turn. closeTeamResources ends them once, after the
	// TUI releases the terminal.
	m.teamPick = nil
	// The overlay's rows go back to the transcript, so the tail is re-pinned:
	// keeping the old offset would leave the newest output off-screen.
	m.forceGotoBottom = true
}

// closeTeamResources releases the member backends and the board they share, at
// the one point it is safe to: after the TUI released the terminal. Nothing did
// this before, so every assembled member kept its session lease and plugin
// subprocesses to process end.
func (m *chatTUI) closeTeamResources() {
	// Hand the controller identity back first, or the caller's own ctrl.Close()
	// lands on a member controller the registry is about to close. Narrow swap,
	// not bindBackend: no terminal is left to rebuild derived state for.
	if m.ambient != nil {
		m.ctrl = m.ambient
		m.ambient = nil
	}
	if m.teamBackends != nil {
		m.teamBackends.closeAll()
		m.teamBackends = nil
	}
}

// leaveTeamDeliberately is the Ctrl+T contract: the user said "get me out of the
// team", which is both a teardown and a preference — the next [ TEAM ] click
// parks on the management page, across restarts, until a deliberate t.
func (m *chatTUI) leaveTeamDeliberately() {
	if m.teamPick == nil {
		return
	}
	m.teamPick.suspendAutoSession()
	m.exitTeam()
}

// suspendAutoSession persists "do not reopen a session here" for the team being
// left. Disk, not a field: the flag has to outlive the process, or relaunching
// silently re-enters the session the user just left. A deliberate entry clears it
// by writing its own selection.
func (p *teamPicker) suspendAutoSession() {
	if p.sessions == nil {
		return
	}
	teamName := p.exitingTeamName()
	if teamName == "" {
		return
	}
	_ = p.sessions.WriteSelection(teamName, team.SessionSelection{Team: teamName, Suspended: true})
}

// exitingTeamName is the team a leave applies to: the bound session's team, else
// the focused one. The selection is keyed by team name, so writing the preference
// under the wrong key would suspend a team the user never left. Read it before
// closeSession — that zeroes the session, teamName included.
func (p *teamPicker) exitingTeamName() string {
	if p.session.teamName != "" {
		return p.session.teamName
	}
	return p.model.Name()
}

// clearSelectedMember drops the persisted member while leaving the auto-session
// preference alone: k removes a leader, it does not decide whether the next entry
// opens a session.
func (p *teamPicker) clearSelectedMember(teamName string) {
	if p.sessions == nil {
		return
	}
	sel, err := p.sessions.ReadSelection(teamName)
	if err != nil {
		return // unreadable: leave the file alone rather than reset the preference
	}
	_ = p.sessions.WriteSelection(teamName, team.SessionSelection{
		Team: teamName, Suspended: sel.Suspended,
	})
}

// sessionSlot returns the current member's persisted slot for the session
// window's info lines.
func (p *teamPicker) sessionSlot() (team.MemberSlot, bool) {
	return p.slotOf(p.session.current)
}
