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
)

// teamPicker is the team management overlay opened by the TEAM button. It owns
// the transport-agnostic view model from internal/team/tui; the CLI maps
// keypresses onto tui events and renders the model state. The registry is read
// from the real .reasonix/team/team.json on open: an absent one is an empty team
// list the first team can be created in, while a corrupt one renders errMsg
// instead of placeholder data. store is the storage seam — every mutation goes
// through team.TeamStore, so a legacy teams.json migrates on first write.
type teamPicker struct {
	model  *tui.Model
	errMsg string          // unreadable registry; "" when healthy
	store  *team.TeamStore // storage seam; nil when the project root is unusable
	doc    team.TeamDoc    // registry as last loaded, for lifecycle lookups
	kind   teamInputKind   // transient write state; teamInputNone when idle
	buf    string          // team or member name being typed
}

// onTeamButtonClick opens the team management view in its own overlay. The
// registry is loaded from disk on open, so a stale document surfaces as a
// message, never as fabricated members.
func (m *chatTUI) onTeamButtonClick() {
	cwd, err := os.Getwd()
	if err == nil {
		var store *team.TeamStore
		store, err = team.NewTeamStore(cwd)
		if err == nil {
			p := &teamPicker{model: tui.New(nil), store: store}
			m.teamPick = p
			if err := p.reload(""); err != nil {
				p.errMsg = pickerErrMsg(err)
			}
			return
		}
	}
	m.teamPick = &teamPicker{model: tui.New(nil), errMsg: "Team data unavailable: " + err.Error()}
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
// store (ErrLastTeam), which the overlay renders as a readable message.
func (p *teamPicker) deleteTeam() error {
	if err := p.store.DeleteTeam(p.model.Name()); err != nil {
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

// deleteMember removes the focused member slot from the focused team.
func (p *teamPicker) deleteMember() error {
	member, ok := p.model.Focused()
	if !ok {
		return nil
	}
	if err := p.store.DeleteMember(p.model.Name(), member.ID); err != nil {
		return err
	}
	return p.reload("")
}

// cycleStatus advances the focused member's lifecycle status. The member stays
// on screen throughout, so the cycle is reversible and needs no confirmation.
func (p *teamPicker) cycleStatus() error {
	member, ok := p.model.Focused()
	if !ok {
		return nil
	}
	slot, ok := p.slotOf(member.ID)
	if !ok {
		return nil
	}
	if err := p.store.SetMemberStatus(p.model.Name(), member.ID, nextStatus(slot.Status)); err != nil {
		return err
	}
	return p.reload("")
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

// nextStatus advances the lifecycle status: active → disabled → archived →
// active. The overlay renders the new status on the member row.
func nextStatus(st team.MemberStatus) team.MemberStatus {
	switch st {
	case team.MemberStatusActive:
		return team.MemberStatusDisabled
	case team.MemberStatusDisabled:
		return team.MemberStatusArchived
	default:
		return team.MemberStatusActive
	}
}

// handleTeamPickerKey maps keypresses onto tui events and the cli write states.
// Esc closes the overlay from the team list and steps back one screen from a
// roster or context view; q (or ctrl+c, which the modal chain already
// intercepts) enters the quit confirmation, and a second q, Enter, or
// Esc-cancelled confirmation closes. a/d act on the screen's own subject —
// teams on the team list, members inside a roster — and s cycles the focused
// member's lifecycle status. In the add/delete states every key feeds the write
// flow first: printable keys type into the new name, Enter confirms, Esc
// cancels, and q cancels a delete (never accelerates it).
func (m chatTUI) handleTeamPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.teamPick
	if p == nil {
		return m, nil
	}
	view := p.model
	if (p.kind == teamInputAdd || p.kind == teamInputAddMember) && typeIntoTeamBuffer(p, msg) {
		return m, nil
	}
	switch msg.String() {
	case "up", "k":
		view.Handle(tui.EventUp)
	case "down", "j":
		view.Handle(tui.EventDown)
	case "enter":
		if enterTeamKey(p, view) {
			m.teamPick = nil
		}
	case "esc":
		if escTeamKey(p, view) {
			m.teamPick = nil
		}
	case "q":
		if quitTeamKey(p, view) {
			m.teamPick = nil
		}
	case "ctrl+c":
		ctrlCTeamKey(p, view)
	case "space":
		view.Handle(tui.EventSelect)
	case "a", "d", "s":
		startTeamKey(p, view, msg.String())
	case "backspace":
		backspaceTeamKey(p)
	default:
		if (p.kind == teamInputAdd || p.kind == teamInputAddMember) && printableKey(msg.String()) {
			p.buf += msg.String()
		}
	}
	return m, nil
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
		if view.Mode() == tui.ModeQuit {
			return true
		}
		view.Handle(tui.EventSelect)
	}
	return false
}

// escTeamKey cancels a write state, closes the overlay from the team list, or
// steps back one screen.
func escTeamKey(p *teamPicker, view *tui.Model) (closed bool) {
	if p.kind != teamInputNone {
		p.kind = teamInputNone
		p.buf = ""
		return false
	}
	if view.Mode() == tui.ModeTeams {
		return true
	}
	view.Handle(tui.EventBack)
	return false
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
// the focused member, and s cycles its status.
func startMemberLevelKey(p *teamPicker, view *tui.Model, key string) {
	switch key {
	case "a":
		p.kind = teamInputAddMember
	case "d":
		if _, ok := view.Focused(); ok {
			p.kind = teamInputDeleteMember
		}
	case "s":
		if err := p.cycleStatus(); err != nil {
			p.errMsg = pickerErrMsg(err)
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
