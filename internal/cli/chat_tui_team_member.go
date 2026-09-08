package cli

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/team"
	"reasonix/internal/team/tui"
)

// memberEditKind is the cli-owned write state of the member property editor
// (§5): the field-list navigator, one open field's edit, or the member pool's
// ordered multi-select sub-editor (the pool row's second level). The editor
// replaces the read-only member detail — every property is a row that edits
// in place, and only s publishes; Esc returns with zero writes.
type memberEditKind int

const (
	memberEditNone      memberEditKind = iota
	memberEditFieldList                // field cursor navigation; s saves, esc exits
	memberEditFieldEdit                // editing one field; enter confirms back to the list
	memberEditPoolSel                  // ordered multi-select of the member's custom pool
)

// memberEditFields is the member property editor's editable field list. Leader
// remains a separate assignment/step-down flow; Role is free text validated
// and persisted through the same guarded save path as the closed choices. The
// pool row opens the agent-pool mode picker (inherit/custom) and, for custom,
// descends into the ordered multi-select that owns the pool entries.
var memberEditFields = []string{"status", "proxy", "agent", "role", "pool"}

// memberPoolSelState is the pool row's ordered multi-select: one row per
// registry entry, with the chosen entries selected in the order the user
// toggled them on — the saved pool order is the click order, never the registry
// order, mirroring the team-pool editor. prevMode/prevPool snapshot the draft
// when the row opened — the mode still original, since "custom" lands on enter
// — so Esc restores the exact prior state.
type memberPoolSelState struct {
	users    []team.AgentUser // candidate rows, registry order
	sel      []string         // ordered selected ids — the custom pool being edited
	focus    int              // row cursor into users
	errMsg   string
	prevMode string   // draft pool mode when the row opened (restore target)
	prevPool []string // draft pool entries when the row opened (restore target)
}

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
	pool   memberPoolSelState
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

// memberEditOwnsKey reports whether an open member sub-editor consumed the key:
// the pool row's ordered multi-select or one field's edit, both of which own
// every key while they are up.
func memberEditOwnsKey(p *teamPicker, msg tea.KeyPressMsg) bool {
	switch p.memberEdit.kind {
	case memberEditPoolSel:
		return handleMemberPoolSelKey(p, msg)
	case memberEditFieldEdit:
		return handleMemberFieldKey(p, msg)
	}
	return false
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
// for unbind; the pool field's are the two pool modes, with the team default
// labeled by its current head so inherit reads as what it inherits; the rest
// are fixed.
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
	case "pool":
		return []option{
			{id: "", label: p.poolInheritLabel()},
			{id: team.MemberPoolCustom, label: "custom — this member's own pool"},
		}
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
	case "pool":
		if slot.IsCustomPool() {
			return team.MemberPoolCustom
		}
		return ""
	default: // agent
		return slot.AgentUserRef
	}
}

// poolInheritLabel names what inherit resolves to for the focused member — a
// pin stays pinned, an unbound member takes the team pool head — so the pool
// row's mode picker opens labeled by the actual target.
func (p *teamPicker) poolInheritLabel() string {
	member, ok := p.model.Focused()
	if !ok {
		return "inherit"
	}
	if slot, ok := p.slotOf(member.ID); ok && !slot.IsCustomPool() && slot.AgentUserRef != "" {
		return "inherit — pinned to " + slot.AgentUserRef
	}
	if pool := p.teamEffectivePool(); len(pool) > 0 {
		return "inherit — team pool head " + pool[0]
	}
	return "inherit — no team agent pool configured"
}

// commitMemberField validates and merges the open field into the draft, then
// returns to the field list. The option list merges its committed id; a custom
// pool-mode commit descends into the ordered multi-select instead, whose Esc
// restores the draft the row opened on.
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
	case "pool":
		me.kind = memberEditFieldList
		me.list = optionList{}
		if id == team.MemberPoolCustom {
			p.armMemberPoolSel() // the custom commit descends into the entries
			return
		}
		me.draft.PoolMode = "" // inherit; the retained entries stay inert on the slot
	default: // agent
		me.draft.AgentUserRef = id
	}
	me.kind = memberEditFieldList
	me.list = optionList{}
	me.buf = ""
	me.cur = 0
	me.errMsg = ""
}

// armMemberPoolSel opens the pool row's ordered multi-select on a custom
// commit: candidates come from the registry, the selection seeds from the
// draft's entries (dangling refs drop out — the next save rewrites reality),
// and prevMode/prevPool snapshot the row's opening state for Esc to restore.
func (p *teamPicker) armMemberPoolSel() {
	me := &p.memberEdit
	st := &me.pool
	st.prevMode = me.draft.PoolMode
	st.prevPool = append([]string(nil), me.draft.AgentUserPool...)
	users, err := p.store.ListAgentUsers()
	if err != nil {
		me.kind = memberEditFieldList
		me.errMsg = pickerErrMsg(err)
		return
	}
	st.users = users
	present := make(map[string]bool, len(users))
	for _, u := range users {
		present[u.UserID] = true
	}
	st.sel = nil
	for _, id := range me.draft.AgentUserPool {
		if present[id] {
			st.sel = append(st.sel, id)
		}
	}
	st.focus, st.errMsg = 0, ""
	me.kind = memberEditPoolSel
}

// handleMemberPoolSelKey routes the pool row's ordered multi-select keys:
// up/down move, space toggles on (appending — click order), enter confirms
// into the draft, esc restores the state the row opened on.
func handleMemberPoolSelKey(p *teamPicker, msg tea.KeyPressMsg) bool {
	st := &p.memberEdit.pool
	switch msg.String() {
	case "up", "k":
		if n := len(st.users); n > 0 {
			st.focus = (st.focus + n - 1) % n
		}
	case "down", "j":
		if n := len(st.users); n > 0 {
			st.focus = (st.focus + 1) % n
		}
	case "space":
		p.toggleMemberPoolSelRow()
	case "enter":
		p.commitMemberPoolSel()
	case "esc", "ctrl+c", "q":
		p.cancelMemberPoolSel()
	default:
		return false
	}
	return true
}

// toggleMemberPoolSelRow flips the focused entry in the member pool being
// edited: off removes it from the selection, on appends it to the selection
// tail, so the saved order is the order the user clicked entries on.
func (p *teamPicker) toggleMemberPoolSelRow() {
	st := &p.memberEdit.pool
	if st.focus >= len(st.users) {
		return
	}
	id := st.users[st.focus].UserID
	for i, sel := range st.sel {
		if sel == id {
			st.sel = append(st.sel[:i], st.sel[i+1:]...)
			return
		}
	}
	st.sel = append(st.sel, id)
}

// commitMemberPoolSel is the pool select's enter key: the ordered selection
// merges into the draft, nothing persists until s. An empty custom pool is
// refused here (the store refuses it too) — never an implicit inherit.
func (p *teamPicker) commitMemberPoolSel() {
	st := &p.memberEdit.pool
	if len(st.sel) == 0 {
		if len(st.users) == 0 {
			st.errMsg = "No agent users yet — add them on the pool screen (u)"
		} else {
			st.errMsg = "A custom pool needs at least one entry — toggle entries on with Space"
		}
		return
	}
	me := &p.memberEdit
	me.draft.PoolMode = team.MemberPoolCustom
	me.draft.AgentUserPool = append([]string(nil), st.sel...)
	me.kind = memberEditFieldList
	me.pool = memberPoolSelState{}
	me.errMsg = ""
}

// cancelMemberPoolSel is the pool select's esc key: the draft returns to the
// state the row opened on (fresh custom flips back to inherit), zero writes.
func (p *teamPicker) cancelMemberPoolSel() {
	me := &p.memberEdit
	me.draft.PoolMode = me.pool.prevMode
	me.draft.AgentUserPool = append([]string(nil), me.pool.prevPool...)
	me.kind = memberEditFieldList
	me.pool = memberPoolSelState{}
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
// persisted slot and the draft, so an untouched row never publishes. The pool
// row compares the effective mode and, when both are custom, the ordered
// entries — retained-but-inert inherit entries never count.
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
	case "pool":
		if old.IsCustomPool() != new.IsCustomPool() {
			return false
		}
		if !old.IsCustomPool() {
			return true
		}
		return slices.Equal(old.AgentUserPool, new.AgentUserPool)
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
		// A proxy change is baked into the member's provider transport: refuse
		// edits while the backend is actively running, then retire an idle
		// instance after the write so the next bind rebuilds the transport.
		if p.backends != nil {
			if backend, ok := p.backends.bound(teamName, memberID); ok {
				status := backend.RuntimeStatus()
				if status.Running || status.PendingPrompt || status.BackgroundJobs > 0 {
					return fmt.Errorf("team: finish or stop member %q before changing its proxy", memberID)
				}
			}
		}
		if err := p.store.SetMemberProxyOverride(teamName, memberID, draft.ProxyEnabled); err != nil {
			return err
		}
		if p.backends != nil {
			p.backends.release(teamName, memberID)
		}
		return nil
	case "pool":
		// The pool change re-folds the member's next bind and failover walk, so
		// no busy gate or backend retirement is needed — the team pool editor
		// sets the same precedent; the store guards the write itself.
		mode := ""
		entries := []string(nil)
		if draft.IsCustomPool() {
			mode, entries = team.MemberPoolCustom, draft.AgentUserPool
		}
		return p.store.SetMemberPool(teamName, memberID, mode, entries)
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
