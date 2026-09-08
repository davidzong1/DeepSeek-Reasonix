package cli

import (
	"strconv"
	"strings"

	"reasonix/internal/team"
	"reasonix/internal/team/tui"
)

// renderMemberEdit renders the member property editor (§5): the member id
// header with its runtime state, the Role/Leader rows, and the
// editable field list with its cursor on the left and a preview column of the
// same draft on the right. Only s persists; esc returns with zero writes. The
// agent fields stay backend-only — the editor's rows are the persisted
// template properties, never launch configuration.
func (p *teamPicker) renderMemberEdit(view *tui.Model, b *strings.Builder, w, listH int) {
	member, ok := view.Focused()
	if !ok {
		return
	}
	me := &p.memberEdit
	if me.kind == memberEditPoolSel {
		p.renderMemberPoolSel(b, w)
		return
	}
	b.WriteString("  " + accent(member.ID) + "\n")
	b.WriteString(dim("  State: ") + string(member.State) + "\n")
	// Leader remains a separate assignment/step-down flow; Role is editable below.
	role := "-"
	if me.draft.Role != "" {
		role = string(me.draft.Role)
	}
	leader := "off"
	if me.draft.IsLeader() {
		leader = "on"
	}
	b.WriteString(dim("  Role: ") + role + dim("   Leader: ") + leader + "\n")
	if me.errMsg != "" {
		b.WriteString(me.errMsg + "\n")
	}
	col := max((w-8)/2, 12)
	preview := make([]string, len(memberEditFields))
	for i, f := range memberEditFields {
		preview[i] = memberFieldLabel(f) + ": " + memberFieldValue(me.draft, i)
	}
	for i, f := range memberEditFields {
		val := memberFieldValue(me.draft, i)
		if me.kind == memberEditFieldEdit && i == me.edit {
			if f == "role" {
				val = fieldCursorView(me.buf, me.cur)
			} else {
				val = me.list.currentLabel() + " ▏"
			}
		}
		mark := "  "
		if i == me.edit {
			mark = "> "
		}
		left := mark + memberFieldLabel(f) + ": " + truncateCells(val, col-6)
		b.WriteString(padColumn(left, col) + dim("│ "+truncateCells(preview[i], col)) + "\n")
	}
	if me.kind == memberEditFieldEdit {
		if memberEditFields[me.edit] == "role" {
			b.WriteString(dim("Type role · Enter confirm · Esc cancel"))
		} else {
			me.list.resize(listH - 3)
			b.WriteString(me.list.view(w, listH))
		}
	} else {
		b.WriteString(dim("↑/↓ field · Enter/Space edit · s save · 🌟 t Enter_session · a/d member") + "\n")
		b.WriteString(dim("b bind · l leader-mode · Esc back · " + teamExitHint + " · q quit"))
	}
}

// renderMemberPoolSel renders the pool row's ordered multi-select: candidates
// marked with their pool position, a live order line, and the hint. Only the
// editor's s persists — Enter merges, Esc restores the row's opening state.
func (p *teamPicker) renderMemberPoolSel(b *strings.Builder, w int) {
	st := &p.memberEdit.pool
	member, _ := p.model.Focused()
	b.WriteString(accent(member.ID+" · agent pool") + "\n")
	if st.errMsg != "" {
		b.WriteString(st.errMsg + "\n")
	}
	if len(st.users) == 0 {
		b.WriteString(dim("No agent users yet — u on the Teams list adds them") + "\n")
		b.WriteString(dim("Esc cancel"))
		return
	}
	pos := make(map[string]int, len(st.sel))
	for i, id := range st.sel {
		pos[id] = i + 1
	}
	for i, u := range st.users {
		label := u.UserID + " " + dim("("+providerModel(u)+")")
		if n, on := pos[u.UserID]; on {
			label = accent(u.UserID) + " " + accent("#"+strconv.Itoa(n)) + " " + dim("("+providerModel(u)+")")
		}
		b.WriteString(rowLine(i == st.focus, i+1, "", label, false) + "\n")
	}
	order := "(none — a custom pool needs at least one entry)"
	if len(st.sel) > 0 {
		order = strings.Join(st.sel, " → ")
	}
	b.WriteString(dim("Order: ") + accent(order) + "\n")
	b.WriteString(dim("↑/↓ move · Space toggle in click order · Enter confirm · Esc cancel"))
}

// memberFieldLabel names a member property for the editor rows.
func memberFieldLabel(f string) string {
	switch f {
	case "role":
		return "Role"
	case "leader":
		return "Leader"
	case "status":
		return "Status"
	case "proxy":
		return "Proxy"
	case "pool":
		return "Agent pool"
	default:
		return "Agent"
	}
}

// memberFieldValue reads a member property from the editor draft by field id;
// the pool row renders its mode plus, for custom, the ordered entries.
func memberFieldValue(slot team.MemberSlot, i int) string {
	switch memberEditFields[i] {
	case "role":
		if slot.Role == "" {
			return "-"
		}
		return string(slot.Role)
	case "leader":
		if slot.IsLeader() {
			return "on"
		}
		return "off"
	case "status":
		return string(slot.Status)
	case "proxy":
		return memberProxyLabel(slot.ProxyEnabled)
	case "pool":
		if !slot.IsCustomPool() {
			return "inherit"
		}
		if len(slot.AgentUserPool) == 0 {
			return "custom"
		}
		return "custom · " + strings.Join(slot.AgentUserPool, " → ")
	default:
		if slot.AgentUserRef == "" {
			return "team default"
		}
		return slot.AgentUserRef
	}
}

// memberProxyLabel names a member's proxy override state.
func memberProxyLabel(e *bool) string {
	if e == nil {
		return "inherit"
	}
	if *e {
		return "on"
	}
	return "off"
}
