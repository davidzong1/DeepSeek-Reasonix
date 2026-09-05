package cli

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/team"
	"reasonix/internal/team/tui"
)

// memberEditKind is the cli-owned write state of the member property editor
// (§5): the field-list navigator or one open field's edit. The editor
// replaces the read-only member detail — every property is a row that edits
// in place, and only s publishes; Esc returns with zero writes.
type memberEditKind int

const (
	memberEditNone      memberEditKind = iota
	memberEditFieldList                // field cursor navigation; s saves, esc exits
	memberEditFieldEdit                // editing one field; enter confirms back to the list
)

// memberEditFields is the member property editor's editable field list. Leader
// remains a separate assignment/step-down flow; Role is free text validated
// and persisted through the same guarded save path as the closed choices.
var memberEditFields = []string{"status", "proxy", "agent", "role"}

// memberEditState is the member property editor: the draft slot seeded from
// the focused member, the field cursor, the open field's option list, and the
// refusal message. The draft publishes field by field on s, so an untouched
// row never writes.
type memberEditState struct {
	kind   memberEditKind
	draft  team.MemberSlot
	edit   int // field cursor into memberEditFields
	list   optionList
	errMsg string
	buf    string
	cur    int // rune cursor into buf while the role field is being typed
}

// handleMemberEditNavKey routes the member editor's own keys on the detail
// screen — field cursor, open field, save, session, step-down — and reports
// whether it consumed the keypress. The editor is a cli-owned overlay on the
// detail mode, so its keys never reach the tui model; add/delete/bind/leader-
// mode stay in the shared dispatch below.
func handleMemberEditNavKey(p *teamPicker, view *tui.Model, msg tea.KeyPressMsg) (handled bool) {
	if view.Mode() != tui.ModeContext || p.memberEdit.kind != memberEditFieldList || p.kind != teamInputNone {
		return false
	}
	switch msg.String() {
	case "up":
		moveMemberEditCursor(p, -1)
	case "down", "j":
		moveMemberEditCursor(p, +1)
	case "enter", "space":
		p.openMemberEditField()
	case "s":
		p.saveMemberEdit()
	case "t":
		return true // handled by the chatTUI-level key router (enterTeamSession)
	case "k":
		p.startLeaderReset()
	default:
		return false
	}
	return true
}

// armMemberEdit seeds the editor from the focused member's persisted slot,
// once. Every entry path (e, Enter, Space on the roster) calls it, so the
// editor always opens on the document, never on a stale draft.
func (p *teamPicker) armMemberEdit() {
	if p.memberEdit.kind != memberEditNone {
		return
	}
	member, ok := p.model.Focused()
	if !ok {
		return
	}
	slot, ok := p.slotOf(member.ID)
	if !ok {
		return
	}
	p.memberEdit = memberEditState{kind: memberEditFieldList, draft: slot}
}

// moveMemberEditCursor shifts the field cursor, clamped.
func moveMemberEditCursor(p *teamPicker, d int) {
	p.memberEdit.edit = min(max(p.memberEdit.edit+d, 0), len(memberEditFields)-1)
}

// openMemberEditField opens the focused field into its option list, preselected
// on the current value. Nothing is written until s.
func (p *teamPicker) openMemberEditField() {
	me := &p.memberEdit
	field := memberEditFields[me.edit]
	me.kind = memberEditFieldEdit
	if field == "role" {
		me.buf = string(me.draft.Role)
		me.cur = fieldRuneCount(me.buf)
		return
	}
	me.list.setOptions(optionSingle, p.memberPickerOptions(field), memberPickerInitialID(field, me.draft))
}

// handleMemberFieldKey routes a keypress inside one open field: the role field
// is free text — runes insert at the cursor, left/right/home/end move it,
// backspace/delete remove around it, and enter confirms — while the remaining
// fields are option lists that move with up/down and confirm with enter, zero
// writes until s. The field owns every key while it is open, so "s"/"t" are
// ordinary letters here.
func handleMemberFieldKey(p *teamPicker, msg tea.KeyPressMsg) bool {
	me := &p.memberEdit
	if memberEditFields[me.edit] == "role" {
		switch msg.String() {
		case "enter":
			me.draft.Role = team.RoleID(strings.TrimSpace(me.buf))
			me.kind, me.buf, me.cur = memberEditFieldList, "", 0
		case "esc", "ctrl+c":
			me.kind, me.buf, me.cur = memberEditFieldList, "", 0
		case "backspace":
			me.buf, me.cur = fieldBackspace(me.buf, me.cur)
		case "delete":
			me.buf, me.cur = fieldDelete(me.buf, me.cur)
		case "left":
			me.cur = fieldMove(me.buf, me.cur, -1)
		case "right":
			me.cur = fieldMove(me.buf, me.cur, +1)
		case "home":
			me.cur = 0
		case "end":
			me.cur = fieldRuneCount(me.buf)
		default:
			if msg.String() == "space" {
				me.buf, me.cur = fieldInsert(me.buf, me.cur, " ")
			} else if printableKey(msg.String()) {
				me.buf, me.cur = fieldInsert(me.buf, me.cur, msg.String())
			}
		}
		return true
	}
	_, action := me.list.handleKey(msg)
	switch action {
	case optionListCommit:
		p.commitMemberField()
	case optionListCancel:
		me.kind = memberEditFieldList
		me.list = optionList{}
		me.errMsg = ""
	}
	return true
}

// memberPickerOptions returns the closed choice set of a picker field. The
// agent field's options are the pool entries as loaded plus "team default"
// for unbind; the rest are fixed.
func (p *teamPicker) memberPickerOptions(field string) []option {
	switch field {
	case "status":
		return []option{
			{id: string(team.MemberStatusActive)},
			{id: string(team.MemberStatusDisabled)},
			{id: string(team.MemberStatusArchived)},
		}
	case "proxy":
		return []option{{id: "inherit"}, {id: "on"}, {id: "off"}}
	default: // agent
		opts := []option{{id: "", label: "team default"}}
		users, err := p.store.ListAgentUsers()
		if err == nil {
			for _, u := range users {
				opts = append(opts, option{id: u.UserID})
			}
		}
		return opts
	}
}

// memberPickerInitialID maps the slot's current value onto the option id the
// picker opens with: the persisted value, or "team default" when a referenced
// pool entry no longer exists (the picker then lands on the unbind row).
func memberPickerInitialID(field string, slot team.MemberSlot) string {
	switch field {
	case "status":
		return string(slot.Status)
	case "proxy":
		if slot.ProxyEnabled == nil {
			return "inherit"
		}
		if *slot.ProxyEnabled {
			return "on"
		}
		return "off"
	default: // agent
		return slot.AgentUserRef
	}
}

// commitMemberField validates and merges the open field into the draft, then
// returns to the field list. The option list merges its committed id.
func (p *teamPicker) commitMemberField() {
	me := &p.memberEdit
	field := memberEditFields[me.edit]
	id, _ := me.list.choice()
	switch field {
	case "status":
		me.draft.Status = team.MemberStatus(id)
	case "proxy":
		switch id {
		case "on":
			on := true
			me.draft.ProxyEnabled = &on
		case "off":
			off := false
			me.draft.ProxyEnabled = &off
		default:
			me.draft.ProxyEnabled = nil
		}
	default: // agent
		me.draft.AgentUserRef = id
	}
	me.kind = memberEditFieldList
	me.list = optionList{}
	me.buf = ""
	me.cur = 0
	me.errMsg = ""
}

// saveMemberEdit is the s key: every changed property publishes through its
// TeamStore setter (one CAS each), then the view re-reads so the editor shows
// persisted state (§8.3). A refusal lands on the field and keeps the editor
// open; untouched fields never write.
func (p *teamPicker) saveMemberEdit() {
	me := &p.memberEdit
	member, ok := p.model.Focused()
	if !ok {
		return
	}
	slot, ok := p.slotOf(member.ID)
	if !ok {
		return
	}
	name := p.model.Name()
	for i, f := range memberEditFields {
		if memberFieldEqual(f, slot, me.draft) {
			continue
		}
		if err := p.applyMemberField(name, member.ID, f, me.draft); err != nil {
			me.errMsg, me.edit = pickerErrMsg(err), i
			return
		}
	}
	if err := p.reload(""); err != nil {
		me.errMsg = pickerErrMsg(err)
		return
	}
	me.kind = memberEditFieldList
	me.list = optionList{}
	me.errMsg = ""
	if fresh, ok := p.slotOf(member.ID); ok {
		me.draft = fresh
	}
}

// memberFieldEqual reports whether the field is unchanged between the
// persisted slot and the draft, so an untouched row never publishes.
func memberFieldEqual(field string, old, new team.MemberSlot) bool {
	switch field {
	case "role":
		return old.Role == new.Role
	case "leader":
		return old.Leader == new.Leader
	case "status":
		return old.Status == new.Status
	case "proxy":
		return sameBoolPtr(old.ProxyEnabled, new.ProxyEnabled)
	default:
		return old.AgentUserRef == new.AgentUserRef
	}
}

// applyMemberField publishes one draft field through its TeamStore setter.
func (p *teamPicker) applyMemberField(teamName, memberID, field string, draft team.MemberSlot) error {
	switch field {
	case "role":
		if err := team.ValidateRole(string(draft.Role)); err != nil {
			return err
		}
		if p.backends != nil {
			if backend, ok := p.backends.bound(teamName, memberID); ok {
				status := backend.RuntimeStatus()
				if status.Running || status.PendingPrompt || status.BackgroundJobs > 0 {
					return fmt.Errorf("team: finish or stop member %q before changing its role", memberID)
				}
			}
		}
		if err := p.store.SetMemberRole(teamName, memberID, draft.Role); err != nil {
			return err
		}
		if p.sessions != nil {
			if err := p.sessions.ClearMember(teamName, memberID); err != nil {
				return err
			}
		}
		if p.backends != nil {
			p.backends.release(teamName, memberID)
		}
		return nil
	case "leader":
		return p.store.SetMemberLeader(teamName, memberID, draft.Leader)
	case "status":
		return p.store.SetMemberStatus(teamName, memberID, draft.Status)
	case "proxy":
		return p.store.SetMemberProxyOverride(teamName, memberID, draft.ProxyEnabled)
	default:
		if draft.AgentUserRef == "" {
			return p.store.UnbindAgentUser(teamName, memberID)
		}
		return p.store.BindAgentUser(teamName, memberID, draft.AgentUserRef)
	}
}

// sameBoolPtr compares two proxy override pointers by value, nil included.
func sameBoolPtr(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
