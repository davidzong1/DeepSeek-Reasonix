package cli

import (
	tea "charm.land/bubbletea/v2"

	"reasonix/internal/team"
	"reasonix/internal/team/tui"
)

// bindKey owns every key while a bind cycle is active (§6.2): up/down cycle
// the candidate pool entries, enter binds the focused member to the current
// candidate, esc unbinds (when bound) or cancels the cycle.
func bindKey(p *teamPicker, msg tea.KeyPressMsg) bool {
	if p.kind != teamInputBind {
		return false
	}
	switch msg.String() {
	case "up":
		stepBind(p, -1)
	case "down", "j":
		stepBind(p, +1)
	case "enter":
		if len(p.binds) > 0 {
			p.confirm(func() error { return p.bindTo(p.binds[p.bind]) })
		}
	case "esc", "ctrl+c":
		member, ok := p.model.Focused()
		bound := false
		if ok {
			if slot, found := p.slotOf(member.ID); found {
				bound = slot.AgentUserRef != ""
			}
		}
		p.kind = teamInputNone
		p.binds = nil
		if bound {
			p.confirm(p.unbindFrom)
		}
	}
	return true
}

// startBindKey arms the bind cycle on the focused member, listing every pool
// entry as a candidate. An empty pool renders its hint inside the cycle
// instead of failing.
func startBindKey(p *teamPicker, view *tui.Model) {
	if p.kind != teamInputNone || p.errMsg != "" {
		return
	}
	if _, ok := view.Focused(); !ok {
		return
	}
	users, err := p.store.ListAgentUsers()
	if err != nil {
		p.errMsg = pickerErrMsg(err)
		return
	}
	p.binds = make([]string, 0, len(users))
	for _, u := range users {
		p.binds = append(p.binds, u.UserID)
	}
	p.bind = 0
	p.kind = teamInputBind
}

// stepBind cycles the candidate cursor, wrapping so every candidate stays
// reachable.
func stepBind(p *teamPicker, d int) {
	if len(p.binds) == 0 {
		return
	}
	p.bind = (p.bind + d + len(p.binds)) % len(p.binds)
}

// bindTo points the focused member at a pool entry and re-reads; the store
// refuses a reference that no longer exists.
func (p *teamPicker) bindTo(ref string) error {
	member, ok := p.model.Focused()
	if !ok {
		return nil
	}
	if err := p.store.BindAgentUser(p.model.Name(), member.ID, ref); err != nil {
		return err
	}
	return p.reload("")
}

// unbindFrom clears the focused member's pool reference back to the team
// default.
func (p *teamPicker) unbindFrom() error {
	member, ok := p.model.Focused()
	if !ok {
		return nil
	}
	if err := p.store.UnbindAgentUser(p.model.Name(), member.ID); err != nil {
		return err
	}
	return p.reload("")
}

// cycleProxy advances the focused member's proxy override around
// inherit → force-on → force-off → inherit and publishes it (§7.4).
func (p *teamPicker) cycleProxy() error {
	member, ok := p.model.Focused()
	if !ok {
		return nil
	}
	slot, ok := p.slotOf(member.ID)
	if !ok {
		return nil
	}
	var next *bool
	switch {
	case slot.ProxyEnabled == nil:
		on := true
		next = &on
	case *slot.ProxyEnabled:
		off := false
		next = &off
	default:
		next = nil
	}
	if err := p.store.SetMemberProxyOverride(p.model.Name(), member.ID, next); err != nil {
		return err
	}
	return p.reload("")
}

// toggleRosterLeader flips the focused member's standalone leader property
// from the compact roster, publishing through the store's CAS setter (§5). The
// t gate reads the same registry field, so a just-assigned leader can open the
// team session; the editor's Leader row stays the full-edit path.
func (p *teamPicker) toggleRosterLeader() {
	member, ok := p.model.Focused()
	if !ok {
		return
	}
	slot, ok := p.slotOf(member.ID)
	if !ok {
		return
	}
	if err := p.store.SetMemberLeader(p.model.Name(), member.ID, !slot.IsLeader()); err != nil {
		p.errMsg = pickerErrMsg(err)
		return
	}
	if err := p.reload(""); err != nil {
		p.errMsg = pickerErrMsg(err)
	}
}

// toggleLeader switches leader mode, which gates member and pool create and
// delete through the store's MemberWritePolicy: Open is the default, LeaderOnly
// refuses those operations until l presses it on again.
func (p *teamPicker) toggleLeader() {
	p.leader = !p.leader
	policy := team.MemberWriteOpen
	if p.leader {
		policy = team.MemberWriteLeaderOnly
	}
	_ = p.store.SetMemberWritePolicy(policy)
	p.errMsg = "" // a mode switch is an explicit retry intent; clear the refusal
}
