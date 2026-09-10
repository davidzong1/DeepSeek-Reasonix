package cli

import (
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/team"
	"reasonix/internal/team/tui"
)

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
// write state or descends, esc cancels or steps back, q enters the quit
// confirmation, ctrl+c closes from the team list and confirms a pending quit,
// backspace edits the name buffer — and reports whether the overlay closed.
func handleTeamSharedKey(p *teamPicker, view *tui.Model, msg tea.KeyPressMsg) (closed bool) {
	switch msg.String() {
	case "enter":
		return enterTeamKey(p, view)
	case "esc":
		return escTeamKey(p, view)
	case "q":
		return quitTeamKey(p, view)
	case "ctrl+c":
		return ctrlCTeamKey(p, view)
	case "backspace":
		backspaceTeamKey(p)
	}
	return false
}

// configTeamKey routes the member-config keys to their editors and reports
// whether the key was consumed: u opens the agent-user pool from the team list
// or the ordered team-pool editor from the roster, e descends from the compact
// roster into the member editor, b arms the bind cycle, p opens the team proxy
// settings from the roster (the member editor owns each member's proxy override
// field), and l assigns the focused member as leader on the roster — refused
// with the holder's id when the team already has one, leaders step down through
// k — or toggles leader mode on the detail screen.
func configTeamKey(p *teamPicker, view *tui.Model, key string) bool {
	switch key {
	case "u":
		switch view.Mode() {
		case tui.ModeTeams:
			p.enterTeamPool()
		case tui.ModeList:
			p.openTeamPoolSel()
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
			p.armTeamProxy()
		}
	case "l":
		switch view.Mode() {
		case tui.ModeList: // the roster's leader assign — never a toggle
			if err := p.assignFocusedLeader(); err != nil {
				p.errMsg = err.Error()
			}
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
// buffers, the pool field editor's non-provider row, the step-down's exact-id
// stage, the team proxy address field, or the member editor's free-text role
// field. nil = no text input (picker rows).
func teamPasteTarget(p *teamPicker) *string {
	switch p.kind {
	case teamInputAdd, teamInputAddMember:
		return &p.buf
	}
	if p.pool.active && p.pool.kind == poolInputEditField &&
		poolEditFields[p.pool.edit] != team.AgentUserFieldProvider {
		return &p.pool.buf
	}
	if p.reset.kind == leaderResetID {
		return &p.reset.buf
	}
	if p.proxyEdit.kind == teamProxyField && teamProxyFields[p.proxyEdit.edit] == "address" {
		return &p.proxyEdit.buf
	}
	if p.memberEdit.kind == memberEditFieldEdit && len(memberEditFields) > p.memberEdit.edit && memberEditFields[p.memberEdit.edit] == "role" {
		return &p.memberEdit.buf
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
	if view.Mode() == tui.ModeContext && p.memberEdit.kind == memberEditPoolSel {
		p.cancelMemberPoolSel() // the row's Esc restores the draft it opened on
		return false
	}
	if view.Mode() == tui.ModeContext && p.memberEdit.kind == memberEditFieldEdit {
		p.memberEdit.kind = memberEditFieldList
		p.memberEdit.list = optionList{}
		p.memberEdit.errMsg = ""
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

// ctrlCTeamKey cancels a write state (a hard exit would drop typed input),
// closes from the team list and the quit confirmation — ctrl+c is the
// terminal's abort chord, so on the list it behaves like Esc — or enters the
// confirmation from a roster or a member view.
func ctrlCTeamKey(p *teamPicker, view *tui.Model) (closed bool) {
	if p.kind != teamInputNone {
		p.kind = teamInputNone
		p.buf = ""
		return false
	}
	if view.Mode() == tui.ModeTeams || view.Mode() == tui.ModeQuit {
		return true
	}
	view.Handle(tui.EventQuit)
	return false
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
