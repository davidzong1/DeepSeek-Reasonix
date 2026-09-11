package cli

import (
	"errors"
	"io/fs"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/boot"
	"reasonix/internal/team"
	"reasonix/internal/team/tui"
)

// teamButtonText is deliberately plain ASCII so its terminal hit box is stable
// across fonts and locales. Styling is added only when the button is rendered.
const teamButtonText = "[ TEAM ]"

// appendTeamButton adds the team entry point to the interaction status line,
// followed by one button per member of the open team so a click binds that
// member's Agent. The bound member is highlighted; the rest are dim. A terminal
// TUI has no native button widget, so labels and hit-testing are explicit.
func (m chatTUI) appendTeamButton(status string) string {
	var row strings.Builder
	row.WriteString(accent(teamButtonText))
	for _, id := range m.statusMemberIDs() {
		label := memberButtonText(id)
		if id == m.boundMember() {
			label = accent(label)
		} else {
			label = dim(label)
		}
		row.WriteString(" " + label)
	}
	if strings.TrimSpace(status) == "" {
		return row.String()
	}
	return status + " · " + row.String()
}

// memberButtonText brackets a member id so its status-line hit box is
// unambiguous, matching the [ TEAM ] entry's shape.
func memberButtonText(id string) string { return "[ " + id + " ]" }

// statusMemberIDs is the member row the status line offers: the bound session's
// roster, empty otherwise. Only a bound session can act on a click — the
// management page is modal and hands the mouse back to the terminal — so
// rendering the buttons there would show a row that cannot respond. Bounded by
// statusMemberButtonLimit so a large team cannot push the rest of the status line
// off a narrow terminal.
func (m chatTUI) statusMemberIDs() []string {
	if !m.teamSessionBound() {
		return nil
	}
	ids := m.teamPick.session.members
	if len(ids) > statusMemberButtonLimit {
		ids = ids[:statusMemberButtonLimit]
	}
	return ids
}

// statusMemberButtonLimit caps the member button row. Beyond it the session's own
// roster column is the way to reach a member, so the status line stays readable.
const statusMemberButtonLimit = 6

// teamStatusButtonHit reports which status-line button a click landed on: a
// member id, or the team entry itself. It inspects the final frame because the
// responsive status layout may move the row onto another line on narrow
// terminals.
func (m chatTUI) teamStatusButtonHit(x, y int) (member string, teamEntry bool) {
	if x < 0 || y < 0 || m.width <= 0 || m.height <= 0 {
		return "", false
	}
	lines := strings.Split(m.View().Content, "\n")
	if y >= len(lines) {
		return "", false
	}
	line := ansi.Strip(lines[y])
	for _, id := range m.statusMemberIDs() {
		if labelHit(line, memberButtonText(id), x) {
			return id, false
		}
	}
	return "", labelHit(line, teamButtonText, x)
}

// labelHit reports whether column x falls inside label's first occurrence on a
// stripped status line. Width is measured in terminal cells, so CJK member ids
// and styled neighbours do not shift the hit box.
func labelHit(line, label string, x int) bool {
	before, _, found := strings.Cut(line, label)
	if !found {
		return false
	}
	start := visibleWidth(before)
	return x >= start && x < start+visibleWidth(label)
}

// teamInputKind is the cli-owned write state of the team overlay (§3.4). The
// tui model stays display-only, so add/delete live here as transient input
// states that publish through team.TeamStore only on confirm.
type teamInputKind int

const (
	teamInputNone         teamInputKind = iota
	teamInputAdd                        // typing a new team name
	teamInputDelete                     // confirming deletion of the focused team
	teamInputAddMember                  // typing a new member id
	teamInputDeleteMember               // confirming deletion of the focused member
	teamInputBind                       // cycling candidate pool entries to bind
)

// teamPicker is the team management overlay opened by the TEAM button. It owns
// the transport-agnostic view model from internal/team/tui; the CLI maps
// keypresses onto tui events and renders the model state. The registry is read
// from the real .reasonix/team/team.json on open: an absent one is an empty team
// list the first team can be created in, while a corrupt one renders errMsg
// instead of placeholder data. store is the storage seam — every mutation goes
// through team.TeamStore, so a legacy teams.json migrates on first write.
type teamPicker struct {
	model         *tui.Model
	errMsg        string                 // unreadable registry; "" when healthy
	refusal       string                 // transient operation refusal; the page stays up
	store         *team.TeamStore        // storage seam; nil when no team data root opened
	sessions      *team.TeamSessionStore // session/context seam; nil when no team data root opened
	dataDir       string                 // team data dir backing store and board; "" when unknown
	board         *teamInboxWire         // durable command chain (§5.1); nil when the board is unavailable
	backends      *teamBackends          // member Agent backends; nil when the seam is unavailable
	hub           *teamHub               // member-prompt routing; nil when the store/backends are unavailable
	sessionDir    string                 // where member session files live; "" when unknown
	workspaceRoot string                 // project root used for skills and project-scoped context
	doc           team.TeamDoc           // registry as last loaded, for lifecycle lookups
	kind          teamInputKind          // transient write state; teamInputNone when idle
	buf           string                 // team, member, or field value being typed
	pool          poolState              // agent-user pool screen; active replaces the team list
	teamPool      teamPoolSelState       // roster team-pool editor; active owns the roster keys
	binds         []string               // bind candidates (pool user ids), for teamInputBind
	bind          int                    // candidate cursor for teamInputBind
	leader        bool                   // leader mode; gates member and pool create/delete
	memberEdit    memberEditState        // member property editor; owns the detail screen
	proxyEdit     teamProxyState         // team proxy settings editor; opens from the roster p
	session       sessionState           // team session window; active replaces the roster
	reset         leaderResetState       // k step-down confirmation; owns every key while active
}

// onTeamButtonClick opens the team overlay on the focused team's leader
// session — the [TEAM] click target state (§11.4). The registry is loaded
// from disk on open, so a stale document surfaces as a message, never as
// fabricated members. The session start returns the subscription command that
// arms its runtime event stream (§11.5).
func (m *chatTUI) onTeamButtonClick() tea.Cmd {
	cwd, err := os.Getwd()
	if err == nil {
		workspaceRoot, projectRoot := teamLaunchRoots(cwd, controllerWorkspaceRoot(m.ctrl))
		// Member controllers keep the ambient workspace root — where the user
		// launched Reasonix — for file references, sandboxing and status. Role
		// playbooks are user-global; only legacy project team data follows the repository root.
		roots, err := openTeamDataRoots(projectRoot)
		if err == nil {
			if roots.note != "" {
				m.notice("team: " + roots.note)
			}
			// The board is the task service's dependency, and the registry — with
			// its task service — is assembled inside bindTeamBackends; opening it
			// after that froze the service on a nil board (D1).
			p := &teamPicker{model: tui.New(nil), store: roots.store, sessions: roots.sessions, dataDir: roots.dataDir, workspaceRoot: workspaceRoot}
			if p.board = m.teamBackends.inbox(); p.board == nil {
				p.board = openTeamInbox(roots.dataDir)
			}
			m.teamPick = p
			m.bindTeamBackends(roots.store)
			// The registry owns the board, so install it there once; a reopen
			// keeps it. Only the per-member inbox cache is overlay-scoped, because
			// each inbox pins that member's BindRecord generation.
			m.teamBackends.setInbox(p.board)
			p.backends = m.teamBackends
			p.board.resetInboxes()
			if m.ctrl != nil {
				p.sessionDir = m.ctrl.SessionDir()
			}
			if err := p.reload(""); err != nil {
				p.errMsg = pickerErrMsg(err)
				return nil
			}
			p.hub = newTeamHub(roots.store, m.teamBackends, p.model.Name())
			// Late-bound for the same reason tasks are: the escalations service is
			// built with the registry, and the hub is built with the overlay.
			m.teamEscalations.setHub(p.hub)
			// Leader wakeups land as notices; a leader without a cursor yet is
			// quiet — history before the first open does not replay (§5.1).
			for _, reason := range p.board.consumeWakeups(p.firstLeader()) {
				m.notice("wakeup: " + reason)
			}
			member, suspended := p.restoreSession()
			if member != "" {
				return tea.Batch(m.switchTeamMember(member), teamRosterRefresh())
			}
			if !suspended && p.refusal == "" && p.firstLeader() == "" {
				// A session needs a leader; a leaderless team parks on the
				// management page with a refusal the page stays usable past —
				// l appoints one, and the reload clears it (§11.4).
				p.refusal = "Only the leader can start a team session"
			}
			return nil
		}
	}
	m.teamPick = &teamPicker{model: tui.New(nil), errMsg: "Team data unavailable: " + err.Error()}
	return nil
}

// teamLaunchRoots keeps the process/controller workspace (the directory the
// user opened Reasonix in) separate from the repository owning the team assets
// a checkout provides: the team/skills source `make install-team-skills` copies
// from, and the project's legacy .reasonix/team that adoption reads. Member
// sessions retain the launch workspace for file references, sandboxing, and
// status display; the installed role playbooks live in the user state root, so
// neither root can steer them.
func teamLaunchRoots(cwd, controllerRoot string) (workspaceRoot, projectRoot string) {
	workspaceRoot = strings.TrimSpace(controllerRoot)
	if workspaceRoot == "" {
		workspaceRoot = strings.TrimSpace(cwd)
	}
	if workspaceRoot == "" {
		return "", ""
	}
	return workspaceRoot, boot.ResolveTeamProjectRoot(workspaceRoot)
}

// firstLeader returns the focused team's first leader slot id, or "".
func (p *teamPicker) firstLeader() string {
	name := p.model.Name()
	for _, t := range p.doc.Teams {
		if t.Name != name {
			continue
		}
		for _, slot := range t.Template {
			if slot.IsLeader() {
				return slot.MemberID
			}
		}
	}
	return ""
}

// pickerErrMsg maps a load or mutation error onto the overlay message, keeping
// the refusals readable and distinct from anything else (corrupt document,
// schema mismatch, I/O), which reads as "unavailable".
func pickerErrMsg(err error) string {
	switch {
	case errors.Is(err, team.ErrTeamExists):
		return "A team with that name already exists"
	case errors.Is(err, team.ErrMemberExists):
		return "A member with that id already exists"
	case errors.Is(err, team.ErrAgentUserNotFound):
		return "No such agent user"
	case errors.Is(err, team.ErrInvalidAgent):
		return "Invalid agent type — claude, codex, or a plain command word"
	case errors.Is(err, team.ErrInvalidRole):
		return "Invalid role — free text, at most 128 bytes, no control characters"
	case errors.Is(err, team.ErrInvalidProxy):
		return "Invalid proxy configuration"
	case errors.Is(err, team.ErrLeaderOnly):
		return "Leader-only operation — press l to enable leader mode"
	case errors.Is(err, team.ErrInvalidMemberPool):
		return "Unknown agent pool mode"
	case errors.Is(err, team.ErrMemberPoolEmpty):
		return "A custom member pool needs at least one entry — toggle entries on with Space"
	case errors.Is(err, team.ErrMemberPoolNonEmpty):
		return "An inheriting member carries no pool entries — switch to custom first"
	case errors.Is(err, team.ErrMemberPoolPin):
		return "Unbind this member's pinned agent user first (Agent row → team default, or press g)"
	case errors.Is(err, team.ErrMemberPoolBind):
		return "Set this member's pool row to inherit first — a custom pool binds its own head"
	default:
		return "Team data unavailable: " + err.Error()
	}
}

// reload re-reads the registry into the view model, which keeps the focused
// team, member, and screen. An absent registry is an empty team list rather
// than an error, so the first team can be created from the overlay; a non-empty
// focus moves onto that team (a freshly added one). Every write path calls it
// afterwards, so the view shows persisted state, never an invented one
// (write-then-read-back, §8.3).
func (p *teamPicker) reload(focus string) error {
	doc, _, err := p.store.Load()
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		doc = team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}}
	}
	p.doc = doc
	views := make([]tui.TeamView, 0, len(doc.Teams))
	for _, t := range doc.Teams {
		views = append(views, tui.TeamView{Name: t.Name, Members: p.rosterMembers(t)})
	}
	p.model.Reload(views)
	if focus != "" {
		p.model.SelectTeam(focus)
	}
	p.errMsg = ""
	p.refusal = ""
	return nil
}

// rosterMembers converts a team's template slots into view members with the
// runtime working state, keeping every slot: the roster manages lifecycle
// status, so a disabled or archived slot must stay visible to edit. Disk holds
// no runtime observation; the bound backends do — a member whose backend is
// Running is working, anything else is idle, so a switch between members never
// lets one member inherit the other's "working" footer.
func (p *teamPicker) rosterMembers(t team.Team) []team.Member {
	members := slotsToMembers(t.Template)
	for i := range members {
		if p.backends == nil {
			continue
		}
		if b, ok := p.backends.bound(t.Name, members[i].ID); ok && b.RuntimeStatus().Running {
			members[i].State = team.MemberStateWorking
		}
	}
	return members
}

// slotsToMembers converts a team's template slots into view members, keeping
// every slot: the roster manages lifecycle status, so a disabled or archived
// slot must stay visible to edit. Disk holds no runtime observation, so state
// starts idle (§2.2).
func slotsToMembers(slots []team.MemberSlot) []team.Member {
	var members []team.Member
	for _, slot := range slots {
		members = append(members, team.Member{
			ID:           slot.MemberID,
			AgentUserRef: slot.AgentUserRef,
			Role:         slot.Role,
			Leader:       slot.IsLeader(), // both the explicit flag and the legacy role encoding
			State:        team.MemberStateIdle,
		})
	}
	return members
}

// addTeam appends a new team and moves focus onto it, so the freshly created
// team is what the user sees next.
func (p *teamPicker) addTeam(name string) error {
	if err := p.store.AddTeam(team.Team{Name: name}); err != nil {
		return err
	}
	return p.reload(name)
}

// deleteTeam removes the focused team, last one included — the registry may end
// empty, which reload renders as the empty state with the create hint. Member
// runtimes stop before the destructive op (§11.6), so none can keep writing
// context while the team's contexts disappear.
func (p *teamPicker) deleteTeam() error {
	name := p.model.Name()
	if !p.stopTeamBeforeClear(name) {
		return nil
	}
	if err := p.store.DeleteTeam(name); err != nil {
		return err
	}
	return p.reload("")
}

// addMember appends an active member slot to the focused team.
func (p *teamPicker) addMember(id string) error {
	if err := p.store.AddMember(p.model.Name(), team.MemberSlot{
		MemberID: id,
		Status:   team.MemberStatusActive, // a new member joins the active roster
	}); err != nil {
		return err
	}
	return p.reload("")
}

// deleteMember removes the focused member slot from the focused team. The
// team's member runtimes stop before the removal (§11.6), so no loop can keep
// writing the member's context while the slot disappears.
func (p *teamPicker) deleteMember() error {
	member, ok := p.model.Focused()
	if !ok {
		return nil
	}
	if !p.stopTeamBeforeClear(p.model.Name()) {
		return nil
	}
	if err := p.store.DeleteMember(p.model.Name(), member.ID); err != nil {
		return err
	}
	return p.reload("")
}

// stopTeamBeforeClear retires every assembled member backend of the team before
// a destructive operation — k step-down, member deletion, team cleanup (§11.6):
// no member may keep writing history while the team's contexts are being
// cleared. Retiring closes each backend, which releases its session lease, so
// there is no failure mode left to refuse on.
func (p *teamPicker) stopTeamBeforeClear(teamName string) bool {
	if p.backends != nil {
		p.backends.releaseTeam(teamName)
	}
	return true
}

// slotOf returns the focused team's template slot for a member id, the source
// of the lifecycle status the roster and context view display.
func (p *teamPicker) slotOf(id string) (team.MemberSlot, bool) {
	name := p.model.Name()
	for _, t := range p.doc.Teams {
		if t.Name != name {
			continue
		}
		for _, slot := range t.Template {
			if slot.MemberID == id {
				return slot, true
			}
		}
	}
	return team.MemberSlot{}, false
}

// handleTeamPickerKey maps keypresses onto tui events and the cli write
// states. Esc and ctrl+c close from the team list (esc also steps back from a
// roster or the member editor); q enters the quit confirmation; a/d act on
// the screen's own subject; s saves the member editor's draft; t opens the
// team session from the roster (leader only); k arms the leader step-down; x
// exits every team session and parks the next [TEAM] click on the management
// page. Write states feed every key first, so Enter confirms and q cancels a
// delete, never accelerates it.
func (m chatTUI) handleTeamPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.teamPick
	if p == nil {
		return m, nil
	}
	if teamPasteKey(p, msg) {
		return m, pasteClipboardText()
	}
	// The pool screen, the roster's team-pool editor, the session window, and an
	// armed step-down confirmation each own every key while active (§5, §6) —
	// their keys never reach the team-list handler.
	if teamSubscreenKey(p, msg) {
		return m, nil
	}
	if p.reset.kind != leaderResetNone && handleLeaderResetKey(p, msg) {
		return m, nil
	}
	if p.proxyEdit.kind != teamProxyNone {
		p.handleTeamProxyKey(msg)
		return m, nil
	}
	view := p.model
	// An open member sub-editor owns every key while it is up: the pool row's
	// ordered multi-select and the field edit, where "s"/"t" are letters.
	if view.Mode() == tui.ModeContext && memberEditOwnsKey(p, msg) {
		return m, nil
	}
	if handleMemberEditNavKey(p, view, msg) {
		return m, nil
	}
	// The bind cycle owns its keys (up/down candidates, enter bind, esc
	// unbind-or-cancel); the field editor owns provider/baseURL/model/effort.
	if bindKey(p, msg) {
		return m, nil
	}
	if (p.kind == teamInputAdd || p.kind == teamInputAddMember) && typeIntoTeamBuffer(p, msg) {
		return m, nil
	}
	var cmd tea.Cmd
	switch msg.String() {
	case "up":
		view.Handle(tui.EventUp)
	case "down", "j":
		view.Handle(tui.EventDown)
	case "space":
		spaceTeamKey(p, view)
	case "a", "d":
		startTeamKey(p, view, msg.String())
	case "g":
		restoreMemberToPoolHead(p, view)
	case "t":
		if memberListKeyAllowed(view, p) {
			cmd = m.enterTeamSession()
		}
	case "k":
		if memberListKeyAllowed(view, p) {
			p.startLeaderReset()
		}
	case teamExitAllKey:
		if memberListKeyAllowed(view, p) {
			m.exitAllTeamSessions()
		}
	default:
		if configTeamKey(p, view, msg.String()) {
			return m, nil
		}
	}
	if closed := handleTeamSharedKey(p, view, msg); closed {
		m.exitTeam()
	}
	return m, cmd
}

// teamExitAllKey exits every team session from the roster; teamExitAllHint
// names it in the help lines, so key and label cannot drift apart. It is not a
// typing key inside a bound session — the composer owns those — so the exit is
// reached from the management page, one esc away from any session.
const (
	teamExitAllKey  = "x"
	teamExitAllHint = "x exit all"
)

// exitAllTeamSessions is the x key: the session window closes and the
// auto-session is suspended, so the next entry parks on the management page
// until a deliberate t (§11.4). Members' backends stay assembled — histories are
// not retired — and the leader property is untouched: this exits sessions, it
// does not step anyone down. Unlike Ctrl+T it keeps the overlay open, which is
// its only reason to exist; it says so in the transcript, because on the
// management page there is no session left on screen for the change to show in.
func (m *chatTUI) exitAllTeamSessions() {
	p := m.teamPick
	if p == nil {
		return
	}
	p.suspendAutoSession()
	m.closeSession()
	m.notice("team " + p.model.Name() + ": auto-session off — [ TEAM ] parks here until t")
	m.forceGotoBottom = true
}

// teamPasteKey reports whether the keypress pastes into the overlay's active
// text buffer — the composer's Ctrl+V / shift+insert binding, for terminals
// that forward the key instead of bracketed-pasting. Inert elsewhere.
func (p *teamPicker) confirm(fn func() error) {
	p.kind = teamInputNone
	p.buf = ""
	if err := fn(); err != nil {
		p.errMsg = pickerErrMsg(err)
	}
}

// handleTeamStatusClick routes a left click on the status-line button row: a
// member button binds that member's Agent (opening the overlay first if the
// click arrived from the plain chat), and the team entry opens the overlay, or
// toggles the detail panel when a member is already bound — there the button is
// the panel's own switch. It reports whether the click was on the row at all.
func (m *chatTUI) handleTeamStatusClick(x, y int) (tea.Cmd, bool) {
	member, teamEntry := m.teamStatusButtonHit(x, y)
	switch {
	case member != "":
		return m.switchTeamMember(member), true
	case teamEntry && m.teamSessionBound():
		m.setSessionPanel(!m.teamPick.session.panel)
		return nil, true
	case teamEntry:
		return m.onTeamButtonClick(), true
	}
	return nil, false
}

// teamStatusClick gates the status-row click routing to left clicks, keeping
// the button handling out of the update switch.
func (m *chatTUI) teamStatusClick(msg tea.MouseClickMsg) (tea.Cmd, bool) {
	if msg.Button != tea.MouseLeft {
		return nil, false
	}
	return m.handleTeamStatusClick(msg.X, msg.Y)
}
