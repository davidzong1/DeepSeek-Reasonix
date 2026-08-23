package cli

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/team"
	"reasonix/internal/team/agentruntime"
	"reasonix/internal/team/tui"
)

// teamButtonText is deliberately plain ASCII so its terminal hit box is stable
// across fonts and locales. Styling is added only when the button is rendered.
const teamButtonText = "[ TEAM ]"

// appendTeamButton adds the team entry point to the interaction status line.
// A terminal TUI has no native button widget, so the visible label and mouse
// hit-testing are implemented explicitly.
func (m chatTUI) appendTeamButton(status string) string {
	button := accent(teamButtonText)
	if strings.TrimSpace(status) == "" {
		return button
	}
	return status + " · " + button
}

// teamButtonHit reports whether a terminal mouse coordinate falls inside the
// rendered team button. It inspects the final frame because the responsive
// status layout may move the button onto another row on narrow terminals.
func (m chatTUI) teamButtonHit(x, y int) bool {
	if x < 0 || y < 0 || m.width <= 0 || m.height <= 0 {
		return false
	}

	lines := strings.Split(m.View().Content, "\n")
	if y >= len(lines) {
		return false
	}

	line := ansi.Strip(lines[y])
	before, _, found := strings.Cut(line, teamButtonText)
	if !found {
		return false
	}
	start := visibleWidth(before)
	return x >= start && x < start+visibleWidth(teamButtonText)
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
	model      *tui.Model
	errMsg     string                 // unreadable registry; "" when healthy
	store      *team.TeamStore        // storage seam; nil when the project root is unusable
	sessions   *team.TeamSessionStore // session/context seam; nil when the project root is unusable
	runtime    *agentruntime.Registry // member runtime seam (§11.3); nil when the seam is unavailable
	doc        team.TeamDoc           // registry as last loaded, for lifecycle lookups
	kind       teamInputKind          // transient write state; teamInputNone when idle
	buf        string                 // team, member, or field value being typed
	pool       poolState              // agent-user pool screen; active replaces the team list
	binds      []string               // bind candidates (pool user ids), for teamInputBind
	bind       int                    // candidate cursor for teamInputBind
	leader     bool                   // leader mode; gates member and pool create/delete
	memberEdit memberEditState        // member property editor; owns the detail screen
	session    sessionState           // team session window; active replaces the roster
	reset      leaderResetState       // k step-down confirmation; owns every key while active
}

// onTeamButtonClick opens the team overlay on the focused team's leader
// session — the [TEAM] click target state (§11.4). The registry is loaded
// from disk on open, so a stale document surfaces as a message, never as
// fabricated members. The session start returns the subscription command that
// arms its runtime event stream (§11.5).
func (m *chatTUI) onTeamButtonClick() tea.Cmd {
	cwd, err := os.Getwd()
	if err == nil {
		var store *team.TeamStore
		var sessions *team.TeamSessionStore
		store, err = team.NewTeamStore(cwd)
		if err == nil {
			sessions, err = team.NewTeamSessionStore(cwd)
		}
		if err == nil {
			p := &teamPicker{model: tui.New(nil), store: store, sessions: sessions,
				runtime: agentruntime.NewRegistry(sessions)}
			m.teamPick = p
			m.bindTeamBackends(store)
			if err := p.reload(""); err != nil {
				p.errMsg = pickerErrMsg(err)
				return nil
			}
			if leader := p.openLeaderSession(); leader != "" {
				return m.switchTeamMember(leader)
			}
			return nil
		}
	}
	m.teamPick = &teamPicker{model: tui.New(nil), errMsg: "Team data unavailable: " + err.Error()}
	return nil
}

// openLeaderSession puts the overlay on the focused team's leader window and
// reports the leader to bind — session active, leader current, the chat composer
// hidden, the roster beside it for switching (§11.4). A team with no leader
// returns "" and stays on the management page: the leader marker is the gate,
// mirroring the t key.
func (p *teamPicker) openLeaderSession() string {
	if p.sessions == nil {
		return ""
	}
	teamName := p.model.Name()
	if teamName == "" {
		return ""
	}
	current := p.firstLeader()
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
	case errors.Is(err, team.ErrLastTeam):
		return "Cannot delete the last team — at least one team must remain"
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
		views = append(views, tui.TeamView{Name: t.Name, Members: slotsToMembers(t.Template)})
	}
	p.model.Reload(views)
	if focus != "" {
		p.model.SelectTeam(focus)
	}
	p.errMsg = ""
	return nil
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

// deleteTeam removes the focused team. Deleting the last team is refused by the
// store (ErrLastTeam), which the overlay renders as a readable message. Member
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

// stopTeamBeforeClear stops every member runtime of the team before a
// destructive operation — k step-down, member deletion, team cleanup (§11.6):
// no loop may keep writing context while the team's contexts are being
// cleared. A stop failure refuses the operation with a message; half-cleanup
// is worse than a refusal.
func (p *teamPicker) stopTeamBeforeClear(teamName string) bool {
	if p.runtime == nil {
		return true
	}
	if err := p.runtime.StopTeam(teamName); err != nil {
		p.errMsg = "Cannot clear — member runtimes still active: " + err.Error()
		return false
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
// states. Esc closes from the team list and steps back from a roster or the
// member editor; q enters the quit confirmation; a/d act on the screen's own
// subject; s saves the member editor's draft; t opens the team session from
// the roster (leader only); k arms the leader step-down. Write states feed
// every key first, so Enter confirms and q cancels a delete, never
// accelerates it.
func (m chatTUI) handleTeamPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.teamPick
	if p == nil {
		return m, nil
	}
	if teamPasteKey(p, msg) {
		return m, pasteClipboardText()
	}
	// The pool screen, the session window, and an armed step-down confirmation
	// each own every key while active (§5, §6) — their keys never reach the
	// team-list handler.
	if p.pool.active && handlePoolKey(p, msg) {
		return m, nil
	}
	if p.session.active {
		if handled, cmd := m.handleSessionKey(msg); handled {
			return m, cmd
		}
	}
	if p.reset.kind != leaderResetNone && handleLeaderResetKey(p, msg) {
		return m, nil
	}
	view := p.model
	// An open member field owns every key while it is being edited (§5): "s"
	// and "t" are letters there. The field-list screen owns its own keys —
	// cursor, open, save, session, step-down.
	if view.Mode() == tui.ModeContext && p.memberEdit.kind == memberEditFieldEdit &&
		handleMemberFieldKey(p, msg) {
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
	case "t":
		if memberListKeyAllowed(view, p) {
			cmd = p.enterTeamSession()
		}
	case "k":
		if memberListKeyAllowed(view, p) {
			p.startLeaderReset()
		}
	default:
		if configTeamKey(p, view, msg.String()) {
			return m, nil
		}
	}
	if closed := handleTeamSharedKey(p, view, msg); closed {
		p.closeTeamOverlay()
		m.teamPick = nil
	}
	return m, cmd
}

// closeTeamOverlay tears the registry down when the overlay closes: every
// remaining member instance — sessions of other teams included — stops and
// its event source closes, so no loop or subscriber outlives the window
// (§11.6). Session Esc already stopped the current team; this is the last
// line for anything else the overlay started.
func (p *teamPicker) closeTeamOverlay() {
	if p.runtime != nil {
		_ = p.runtime.Close()
	}
}

// teamPasteKey reports whether the keypress pastes into the overlay's active
// text buffer — the composer's Ctrl+V / shift+insert binding, for terminals
// that forward the key instead of bracketed-pasting. Inert elsewhere.
func teamPasteKey(p *teamPicker, msg tea.KeyPressMsg) bool {
	return teamPasteTarget(p) != nil && (msg.String() == "ctrl+v" || msg.String() == "shift+insert")
}

// spaceTeamKey selects on the team list and roster, seeding the member editor
// when the roster descends into the detail screen.
func spaceTeamKey(p *teamPicker, view *tui.Model) {
	if view.Mode() == tui.ModeList {
		p.armMemberEdit()
	}
	view.Handle(tui.EventSelect)
}

// handleTeamSharedKey routes the screen-agnostic keys — enter confirms a
// write state or descends, esc cancels or steps back, q and ctrl+c manage the
// quit confirmation, backspace edits the name buffer — and reports whether
// the overlay closed.
func handleTeamSharedKey(p *teamPicker, view *tui.Model, msg tea.KeyPressMsg) (closed bool) {
	switch msg.String() {
	case "enter":
		return enterTeamKey(p, view)
	case "esc":
		return escTeamKey(p, view)
	case "q":
		return quitTeamKey(p, view)
	case "ctrl+c":
		ctrlCTeamKey(p, view)
	case "backspace":
		backspaceTeamKey(p)
	}
	return false
}

// configTeamKey routes the member-config keys to their editors and reports
// whether the key was consumed: u opens the agent-user pool from the team
// list, e descends from the compact roster into the member editor, b arms the
// bind cycle, p cycles the proxy override on the roster (the member editor
// owns the proxy field), and l assigns/clears the focused member's leader on
// the roster or toggles leader mode on the detail screen.
func configTeamKey(p *teamPicker, view *tui.Model, key string) bool {
	switch key {
	case "u":
		if view.Mode() == tui.ModeTeams {
			p.enterTeamPool()
		}
	case "e":
		if view.Mode() == tui.ModeList {
			p.armMemberEdit()
			view.Handle(tui.EventSelect) // the compact roster descends into the member editor
		}
	case "b":
		if view.Mode() == tui.ModeContext {
			startBindKey(p, view)
		}
	case "p":
		if view.Mode() == tui.ModeList {
			if err := p.cycleProxy(); err != nil {
				p.errMsg = pickerErrMsg(err)
			}
		}
	case "l":
		switch view.Mode() {
		case tui.ModeList: // the roster's leader shortcut
			p.toggleRosterLeader()
		case tui.ModeContext:
			p.toggleLeader()
		}
	default:
		return false
	}
	return true
}

// typeIntoTeamBuffer feeds a printable key into the active name buffer, where
// "a", "d", "s", and "q" are ordinary letters. It reports whether the key was
// consumed by the input.
func typeIntoTeamBuffer(p *teamPicker, msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "enter", "esc", "backspace", "ctrl+c":
		return false
	}
	if msg.String() == "space" {
		p.buf += " "
		return true
	}
	if printableKey(msg.String()) {
		p.buf += msg.String()
		return true
	}
	return false
}

// teamPasteTarget returns the overlay's active text buffer: the add-team
// buffers, the pool field editor's non-provider row, the member editor's role
// row, the step-down's exact-id stage. nil = no text input (picker rows).
func teamPasteTarget(p *teamPicker) *string {
	switch p.kind {
	case teamInputAdd, teamInputAddMember:
		return &p.buf
	}
	if p.session.active && p.session.input {
		return &p.session.buf
	}
	if p.pool.active && p.pool.kind == poolInputEditField &&
		poolEditFields[p.pool.edit] != team.AgentUserFieldProvider {
		return &p.pool.buf
	}
	if p.memberEdit.kind == memberEditFieldEdit && memberEditFields[p.memberEdit.edit] == "role" {
		return &p.memberEdit.buf
	}
	if p.reset.kind == leaderResetID {
		return &p.reset.buf
	}
	return nil
}

// enterTeamKey confirms an active write state, closes from the quit
// confirmation, or descends one screen.
func enterTeamKey(p *teamPicker, view *tui.Model) (closed bool) {
	switch p.kind {
	case teamInputAdd:
		if name := strings.TrimSpace(p.buf); name != "" {
			p.confirm(func() error { return p.addTeam(name) })
		}
	case teamInputAddMember:
		if id := strings.TrimSpace(p.buf); id != "" {
			p.confirm(func() error { return p.addMember(id) })
		}
	case teamInputDelete:
		p.confirm(p.deleteTeam)
	case teamInputDeleteMember:
		p.confirm(p.deleteMember)
	default:
		if view.Mode() == tui.ModeList {
			p.armMemberEdit() // enter descends into the member editor
		}
		if view.Mode() == tui.ModeQuit {
			return true
		}
		view.Handle(tui.EventSelect)
	}
	return false
}

// escTeamKey cancels a write state, discards an open member field's edit,
// closes the overlay from the team list, or steps back one screen.
func escTeamKey(p *teamPicker, view *tui.Model) (closed bool) {
	if p.kind != teamInputNone {
		p.kind = teamInputNone
		p.buf = ""
		return false
	}
	if view.Mode() == tui.ModeContext && p.memberEdit.kind == memberEditFieldEdit {
		p.memberEdit.kind = memberEditFieldList
		p.memberEdit.buf, p.memberEdit.opts, p.memberEdit.errMsg = "", nil, ""
		return false
	}
	if view.Mode() == tui.ModeTeams {
		return true
	}
	view.Handle(tui.EventBack)
	p.memberEdit = memberEditState{} // leaving the detail discards the editor draft
	return false
}

// memberListKeyAllowed gates the session and step-down keys to the roster's
// idle state — neither acts inside a write state.
func memberListKeyAllowed(view *tui.Model, p *teamPicker) bool {
	return view.Mode() == tui.ModeList && p.kind == teamInputNone
}

// quitTeamKey cancels a delete (q never accelerates one), closes from the quit
// confirmation, or enters it.
func quitTeamKey(p *teamPicker, view *tui.Model) (closed bool) {
	if p.kind == teamInputDelete || p.kind == teamInputDeleteMember {
		p.kind = teamInputNone
		return false
	}
	if view.Mode() == tui.ModeQuit {
		return true
	}
	view.Handle(tui.EventQuit)
	return false
}

// ctrlCTeamKey cancels a write state (hard exit would drop typed input) or
// enters the quit confirmation.
func ctrlCTeamKey(p *teamPicker, view *tui.Model) {
	if p.kind != teamInputNone {
		p.kind = teamInputNone
		p.buf = ""
		return
	}
	view.Handle(tui.EventQuit)
}

// startTeamKey arms the write state for a/d or applies the status cycle for s,
// routed to the screen's own subject. A corrupt registry (errMsg) and the quit
// confirmation block every write.
func startTeamKey(p *teamPicker, view *tui.Model, key string) {
	if p.kind != teamInputNone || p.errMsg != "" {
		return
	}
	switch view.Mode() {
	case tui.ModeTeams:
		startTeamLevelKey(p, view, key)
	case tui.ModeList, tui.ModeContext:
		startMemberLevelKey(p, view, key)
	}
}

// startTeamLevelKey arms team lifecycle on the team list: a adds, d deletes the
// focused team, and s belongs to members only.
func startTeamLevelKey(p *teamPicker, view *tui.Model, key string) {
	switch key {
	case "a":
		p.kind = teamInputAdd
	case "d":
		if _, ok := view.FocusedTeam(); ok {
			p.kind = teamInputDelete
		}
	}
}

// startMemberLevelKey arms member lifecycle inside a roster: a adds, d deletes
// the focused member. The detail screen's remaining member keys are the
// property editor's own (field nav, s save, t session, l leader-mode).
func startMemberLevelKey(p *teamPicker, view *tui.Model, key string) {
	switch key {
	case "a":
		p.kind = teamInputAddMember
	case "d":
		if _, ok := view.Focused(); ok {
			p.kind = teamInputDeleteMember
		}
	}
}

// backspaceTeamKey deletes the last rune of the name being typed.
func backspaceTeamKey(p *teamPicker) {
	if (p.kind == teamInputAdd || p.kind == teamInputAddMember) && p.buf != "" {
		p.buf = strings.TrimSuffix(p.buf, lastRune(p.buf))
	}
}

// lastRune returns the final UTF-8 rune of s, for backspace deletion that
// works on multi-byte (non-ASCII) team and member names.
func lastRune(s string) string {
	_, size := utf8.DecodeLastRuneInString(s)
	return s[len(s)-size:]
}

// printableKey reports whether a keypress is a single printable character.
func printableKey(s string) bool {
	if s == "" {
		return false
	}
	r, size := utf8.DecodeRuneInString(s)
	return size == len(s) && r >= ' '
}

// confirm runs fn (an add/delete publish); on success the input state clears
// and reload already moved the view onto persisted state, on failure the
// overlay renders the error instead of closing.
func (p *teamPicker) confirm(fn func() error) {
	p.kind = teamInputNone
	p.buf = ""
	if err := fn(); err != nil {
		p.errMsg = pickerErrMsg(err)
	}
}
