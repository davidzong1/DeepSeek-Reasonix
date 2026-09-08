package cli

import (
	"strings"

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

// restoreMemberToPoolHead is the roster g key: it rewinds the focused member's
// agent-user binding to its pool default and clears its durable failover state,
// leaving every other member untouched. The member's own pool wins — a custom
// member keeps its pool configuration and g only resets the runtime back to
// that pool's head, never touching the team document. An inheriting member's
// legacy pin folds away through UnbindAgentUser (clearing the pin falls back to
// the pool head — the same terminal state BindAgentUser(head) produced, without
// leaving a redundant pin on the document). With no pool configured the action
// is refused, so the roster u editor is where the team's defaults are set. The
// write is P1 — no runtime seam re-points an assembled mid-turn backend from the
// management page, so a busy member keeps its in-flight entry and the
// fingerprint-aware bind rebuilds it once idle. P2 adds the runtime half: g also
// clears the member's durable failover state and retires an idle backend moved
// by a quota switch, so the next bind walks the pool head again
// (resetMemberFailover).
func restoreMemberToPoolHead(p *teamPicker, view *tui.Model) {
	if p.kind != teamInputNone || p.errMsg != "" || view.Mode() != tui.ModeList {
		return
	}
	member, ok := view.Focused()
	if !ok {
		return
	}
	name := p.model.Name()
	slot, ok := p.slotOf(member.ID)
	if !ok {
		return
	}
	if slot.IsCustomPool() {
		// A custom member owns its pool: g only rewinds the runtime to its head,
		// never the document — the member editor's pool row edits the config.
		if msg := p.resetMemberFailover(name, member.ID); msg != "" {
			p.errMsg = msg
		}
		return
	}
	eff, err := p.store.EffectiveTeamPool(name)
	if err != nil {
		p.errMsg = pickerErrMsg(err)
		return
	}
	if len(eff) == 0 {
		p.refusal = poolSessionRefusal
		return
	}
	if msg := p.resetMemberFailover(name, member.ID); msg != "" {
		p.errMsg = msg
		return
	}
	if slot.AgentUserRef == "" {
		return // already inheriting the pool head — nothing to restore
	}
	if err := p.store.UnbindAgentUser(name, member.ID); err != nil {
		p.errMsg = pickerErrMsg(err)
		return
	}
	if err := p.reload(""); err != nil {
		p.errMsg = pickerErrMsg(err)
	}
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

// teamProxyKind is the cli-owned write state of the team proxy settings editor
// (§7.4): the field-list navigator or one open field's edit.
type teamProxyKind int

const (
	teamProxyNone teamProxyKind = iota
	teamProxyList
	teamProxyField // editing one field of the team proxy config
)

// teamProxyFields are the editor's rows: the enabled switch members inherit and
// the address (IP:port) the store validates as one field.
var teamProxyFields = []string{"enabled", "address"}

// teamProxyState is the team proxy settings editor: the draft config seeded
// from the focused team, the field cursor, and the open field's value. Only s
// publishes; Esc returns with zero writes, mirroring the member editor.
type teamProxyState struct {
	kind   teamProxyKind
	on     bool
	addr   string
	edit   int
	list   optionList
	buf    string
	cur    int // rune cursor into buf while the address field is being typed
	errMsg string
}

// armTeamProxy seeds the editor from the focused team's persisted proxy: a team
// without one opens off with the default address, so enabling just saves it.
func (p *teamPicker) armTeamProxy() {
	if p.kind != teamInputNone || p.errMsg != "" {
		return
	}
	st := teamProxyState{kind: teamProxyList, addr: team.DefaultProxyAddress}
	for _, t := range p.doc.Teams {
		if t.Name != p.model.Name() || t.Proxy == nil {
			continue
		}
		st.on = t.Proxy.Enabled
		if t.Proxy.Address != "" {
			st.addr = t.Proxy.Address
		}
	}
	p.proxyEdit = st
}

// handleTeamProxyKey owns every key while the team proxy editor is active.
func (p *teamPicker) handleTeamProxyKey(msg tea.KeyPressMsg) bool {
	if p.proxyEdit.kind == teamProxyNone {
		return false
	}
	if p.proxyEdit.kind == teamProxyList {
		switch msg.String() {
		case "up", "k":
			p.proxyEdit.edit = (p.proxyEdit.edit + len(teamProxyFields) - 1) % len(teamProxyFields)
		case "down", "j":
			p.proxyEdit.edit = (p.proxyEdit.edit + 1) % len(teamProxyFields)
		case "enter", "space":
			p.openTeamProxyField()
		case "s":
			p.saveTeamProxy()
		case "esc", "ctrl+c":
			p.proxyEdit = teamProxyState{}
		}
		return true
	}
	return p.handleTeamProxyFieldKey(msg)
}

// openTeamProxyField opens the focused row: enabled is an on/off pick, address
// is free text. Nothing is written until s.
func (p *teamPicker) openTeamProxyField() {
	if teamProxyFields[p.proxyEdit.edit] == "enabled" {
		p.proxyEdit.kind = teamProxyField
		initial := "off"
		if p.proxyEdit.on {
			initial = "on"
		}
		p.proxyEdit.list.setOptions(optionSingle, []option{{id: "on"}, {id: "off"}}, initial)
		return
	}
	p.proxyEdit.kind = teamProxyField
	p.proxyEdit.buf = p.proxyEdit.addr
	p.proxyEdit.cur = fieldRuneCount(p.proxyEdit.buf)
}

// handleTeamProxyFieldKey routes a keypress inside one open field: the enabled
// row is an on/off picker, the address row free text edited like the member
// role field. Enter confirms back to the list; Esc cancels the field edit.
func (p *teamPicker) handleTeamProxyFieldKey(msg tea.KeyPressMsg) bool {
	if teamProxyFields[p.proxyEdit.edit] == "enabled" {
		_, action := p.proxyEdit.list.handleKey(msg)
		switch action {
		case optionListCommit:
			p.commitTeamProxyField()
		case optionListCancel:
			p.proxyEdit.kind = teamProxyList
			p.proxyEdit.list = optionList{}
			p.proxyEdit.errMsg = ""
		}
		return true
	}
	switch msg.String() {
	case "enter":
		p.proxyEdit.addr = strings.TrimSpace(p.proxyEdit.buf)
		p.proxyEdit.kind, p.proxyEdit.buf, p.proxyEdit.cur = teamProxyList, "", 0
	case "esc", "ctrl+c":
		p.proxyEdit.kind, p.proxyEdit.buf, p.proxyEdit.cur = teamProxyList, "", 0
	case "backspace":
		p.proxyEdit.buf, p.proxyEdit.cur = fieldBackspace(p.proxyEdit.buf, p.proxyEdit.cur)
	case "delete":
		p.proxyEdit.buf, p.proxyEdit.cur = fieldDelete(p.proxyEdit.buf, p.proxyEdit.cur)
	case "left":
		p.proxyEdit.cur = fieldMove(p.proxyEdit.buf, p.proxyEdit.cur, -1)
	case "right":
		p.proxyEdit.cur = fieldMove(p.proxyEdit.buf, p.proxyEdit.cur, +1)
	case "home":
		p.proxyEdit.cur = 0
	case "end":
		p.proxyEdit.cur = fieldRuneCount(p.proxyEdit.buf)
	default:
		if msg.String() == "space" {
			p.proxyEdit.buf, p.proxyEdit.cur = fieldInsert(p.proxyEdit.buf, p.proxyEdit.cur, " ")
		} else if printableKey(msg.String()) {
			p.proxyEdit.buf, p.proxyEdit.cur = fieldInsert(p.proxyEdit.buf, p.proxyEdit.cur, msg.String())
		}
	}
	return true
}

// commitTeamProxyField merges the open field into the draft and returns to the
// field list; the whole config validates at s through SetTeamProxy.
func (p *teamPicker) commitTeamProxyField() {
	if teamProxyFields[p.proxyEdit.edit] == "enabled" {
		id, _ := p.proxyEdit.list.choice()
		p.proxyEdit.on = id == "on"
	}
	p.proxyEdit.kind = teamProxyList
	p.proxyEdit.list = optionList{}
	p.proxyEdit.errMsg = ""
}

// saveTeamProxy is the s key: the one store write of the editor. The draft
// publishes through SetTeamProxy — which validates the address (literal IP and
// port) and refuses a bad one — then the roster re-reads (§8.3).
func (p *teamPicker) saveTeamProxy() {
	name := p.model.Name()
	cfg := team.ProxyConfig{Enabled: p.proxyEdit.on, Address: strings.TrimSpace(p.proxyEdit.addr)}
	if err := p.store.SetTeamProxy(name, cfg); err != nil {
		p.proxyEdit.errMsg = pickerErrMsg(err)
		return
	}
	p.proxyEdit = teamProxyState{}
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
