package tui

import (
	"sort"

	"reasonix/internal/team"
)

// Mode is the view model's screen (§3.2). The team list is the entry screen:
// team lifecycle acts there, member lifecycle inside a team's roster.
type Mode string

const (
	ModeTeams   Mode = "teams"
	ModeList    Mode = "list"
	ModeContext Mode = "context"
	ModeQuit    Mode = "quit"
)

// Event is one normalized interaction; the frontend maps keypresses onto it.
type Event string

const (
	EventUp     Event = "up"
	EventDown   Event = "down"
	EventSelect Event = "select"
	EventBack   Event = "back"
	EventQuit   Event = "quit"
)

// TeamView is one team as the view consumes it: the team name plus its roster
// in domain form. The frontend converts registry slots into members.
type TeamView struct {
	Name    string
	Members []team.Member
}

// Model is the team management view model: the registry's teams, the focused
// team and member, and the current screen. Data arrives from the domain layer;
// the model owns ordering, focus, and navigation, never lifecycle.
type Model struct {
	teams  []TeamView
	team   int
	member int
	mode   Mode
	resume Mode // screen a quit confirmation was entered from
}

// New returns a Model on the team list, each team's roster in state-priority
// order, focused on the first team.
func New(teams []TeamView) *Model {
	m := &Model{mode: ModeTeams, resume: ModeTeams, teams: make([]TeamView, 0, len(teams))}
	for _, t := range teams {
		m.teams = append(m.teams, TeamView{Name: t.Name, Members: sortedMembers(t.Members)})
	}
	return m
}

// Reload replaces the registry data, keeping the focused team, member, and
// screen where they still resolve, so a write that re-reads from disk does not
// throw the user back to the team list. A vanished team or member steps out to
// the nearest screen that still resolves.
func (m *Model) Reload(teams []TeamView) {
	prevTeam, prevMode := m.Name(), m.mode
	prevMember := ""
	if focused, ok := m.Focused(); ok {
		prevMember = focused.ID
	}
	next := New(teams)
	if next.SelectTeam(prevTeam) {
		next.selectMember(prevMember)
		switch prevMode {
		case ModeList:
			next.mode = ModeList
		case ModeContext:
			next.mode = ModeContext
			if _, ok := next.Focused(); !ok {
				next.mode = ModeList
			}
		}
		next.resume = next.mode
	}
	*m = *next
}

// SelectTeam focuses the named team and resets member focus, reporting whether
// the name is registered; an unknown name leaves focus unchanged.
func (m *Model) SelectTeam(name string) bool {
	for i := range m.teams {
		if m.teams[i].Name == name {
			m.team, m.member = i, 0
			return true
		}
	}
	return false
}

// selectMember focuses the member with id inside the focused team, reporting
// whether it is present.
func (m *Model) selectMember(id string) bool {
	members := m.Members()
	for i := range members {
		if members[i].ID == id {
			m.member = i
			return true
		}
	}
	return false
}

// Mode returns the current screen.
func (m *Model) Mode() Mode { return m.mode }

// Teams returns the teams in registry order; callers may mutate the copy.
func (m *Model) Teams() []TeamView {
	out := make([]TeamView, 0, len(m.teams))
	for _, t := range m.teams {
		out = append(out, TeamView{Name: t.Name, Members: append([]team.Member(nil), t.Members...)})
	}
	return out
}

// TeamIndex returns the focused team's index; 0 when no team is registered.
func (m *Model) TeamIndex() int { return m.team }

// FocusedTeam returns the focused team, or false when no team is registered.
func (m *Model) FocusedTeam() (TeamView, bool) {
	if m.team >= len(m.teams) {
		return TeamView{}, false
	}
	t := m.teams[m.team]
	return TeamView{Name: t.Name, Members: append([]team.Member(nil), t.Members...)}, true
}

// Name returns the focused team's name, empty when no team is registered.
func (m *Model) Name() string {
	t, ok := m.FocusedTeam()
	if !ok {
		return ""
	}
	return t.Name
}

// Members returns the focused team's roster in display order.
func (m *Model) Members() []team.Member {
	t, ok := m.FocusedTeam()
	if !ok {
		return nil
	}
	return t.Members
}

// FocusIndex returns the focused member's index within the roster.
func (m *Model) FocusIndex() int { return m.member }

// Focused returns the focused member, or false when the roster is empty.
func (m *Model) Focused() (team.Member, bool) {
	members := m.Members()
	if m.member >= len(members) {
		return team.Member{}, false
	}
	return members[m.member], true
}

// Handle applies one event and reports whether it changed the model. On the
// team list, up/down move team focus and select opens that team's roster; in
// the roster, up/down move member focus, select opens the member's context
// view, and back returns to the team list; in the context view, up/down switch
// member and back returns to the roster. Quit opens the confirmation from any
// screen, and back there cancels it onto the screen it came from.
func (m *Model) Handle(ev Event) bool {
	switch m.mode {
	case ModeTeams:
		return m.handleTeams(ev)
	case ModeList:
		return m.handleList(ev)
	case ModeContext:
		return m.handleContext(ev)
	case ModeQuit:
		if ev == EventBack {
			m.mode = m.resume
			return true
		}
	}
	return false
}

func (m *Model) handleTeams(ev Event) bool {
	switch ev {
	case EventUp:
		return m.moveTeam(-1)
	case EventDown:
		return m.moveTeam(+1)
	case EventSelect:
		if len(m.teams) == 0 {
			return false
		}
		m.mode, m.member = ModeList, 0
		return true
	case EventQuit:
		return m.enterQuit()
	}
	return false
}

func (m *Model) handleList(ev Event) bool {
	switch ev {
	case EventUp:
		return m.moveMember(-1)
	case EventDown:
		return m.moveMember(+1)
	case EventSelect:
		if _, ok := m.Focused(); !ok {
			return false
		}
		m.mode = ModeContext
		return true
	case EventBack:
		m.mode = ModeTeams
		return true
	case EventQuit:
		return m.enterQuit()
	}
	return false
}

func (m *Model) handleContext(ev Event) bool {
	switch ev {
	case EventUp:
		return m.moveMember(-1)
	case EventDown:
		return m.moveMember(+1)
	case EventBack:
		m.mode = ModeList
		return true
	case EventQuit:
		return m.enterQuit()
	}
	return false
}

// enterQuit opens the quit confirmation, remembering the screen to return to.
func (m *Model) enterQuit() bool {
	m.resume = m.mode
	m.mode = ModeQuit
	return true
}

// moveTeam shifts team focus one step, clamped, and resets member focus so the
// roster of a newly focused team opens at its top.
func (m *Model) moveTeam(d int) bool {
	next, ok := step(m.team, d, len(m.teams))
	if !ok {
		return false
	}
	m.team, m.member = next, 0
	return true
}

// moveMember shifts member focus one step within the focused roster, clamped.
func (m *Model) moveMember(d int) bool {
	next, ok := step(m.member, d, len(m.Members()))
	if !ok {
		return false
	}
	m.member = next
	return true
}

// step clamps i+d into [0, n) and reports whether it moved.
func step(i, d, n int) (int, bool) {
	if n == 0 {
		return i, false
	}
	next := min(max(i+d, 0), n-1)
	return next, next != i
}

// sortedMembers copies members into state-priority display order, ties broken
// by member ID.
func sortedMembers(in []team.Member) []team.Member {
	out := append([]team.Member(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := stateRank(out[i].State), stateRank(out[j].State)
		if ri != rj {
			return ri < rj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// stateRank maps a member state onto display priority (§2.2): approval >
// working > quota > dead > idle. "busy" is the observation name of the domain
// working state. Unknown states sink to the bottom so a new state never
// reorders the list silently.
func stateRank(st team.MemberState) int {
	switch st {
	case team.MemberStateApproval:
		return 0
	case team.MemberStateWorking:
		return 1
	case team.MemberStateQuota:
		return 2
	case team.MemberStateDead:
		return 3
	case team.MemberStateIdle:
		return 4
	default:
		return 5
	}
}
