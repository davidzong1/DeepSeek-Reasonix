package cli

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/team"
	"reasonix/internal/team/tui"

	tea "charm.land/bubbletea/v2"
)

// renderTeamPicker renders the team overlay: the team list, a team's member
// roster with lifecycle status, the focused member's context view, or one of the
// transient input/confirmation states. A bound member session renders nothing
// until [ TEAM ] reveals its panel, so the member's own history owns the frame.
// An unreadable registry renders its message instead of a list, and an empty one
// the create hint.
func (m chatTUI) renderTeamPicker() string {
	p := m.teamPick
	if p == nil {
		return ""
	}
	if p.pool.active {
		return p.renderTeamPool(m.width, optionListHeight(m.height))
	}
	if p.session.active {
		if p.sessionPanelHidden() {
			return ""
		}
		return p.renderTeamSession(m.width)
	}
	if p.reset.kind != leaderResetNone {
		return p.renderLeaderReset(m.width)
	}
	view := p.model
	w := max(m.width, 10)
	var b strings.Builder
	b.WriteString(accent(teamPickerTitle(view)) + "\n")
	// A refusal is a banner over a live page — the leader gate's message
	// without the data-unavailable dead end errMsg renders instead.
	if p.refusal != "" {
		b.WriteString(p.refusal + "\n")
	}
	if p.errMsg != "" {
		b.WriteString(p.errMsg + "\n")
		b.WriteString(dim("Esc close"))
		return choicePanelStyle.Width(w).Render(b.String())
	}
	if card, ok := p.renderInputState(view, &b); ok {
		return choicePanelStyle.Width(w).Render(card)
	}
	switch view.Mode() {
	case tui.ModeTeams:
		p.renderTeamList(view, &b)
	case tui.ModeContext:
		p.renderMemberEdit(view, &b, w, optionListHeight(m.height))
	case tui.ModeQuit:
		b.WriteString(dim("Leave team view? Esc cancels · Enter or q confirms"))
	default:
		p.renderRoster(view, &b, w)
	}
	return choicePanelStyle.Width(w).Render(b.String())
}

// teamPickerTitle names the screen: the team list header, else the focused
// team's name.
func teamPickerTitle(view *tui.Model) string {
	if view.Mode() == tui.ModeTeams {
		return "Teams"
	}
	if name := view.Name(); name != "" {
		return name
	}
	return "Teams"
}

// renderTeamPool renders the agent-user pool screen (§6.2): every entry with
// its provider and model, the add/delete prompts, or the empty hint. An
// unreadable pool renders its message instead of a list.
func (p *teamPicker) renderTeamPool(width, listH int) string {
	w := max(width, 10)
	var b strings.Builder
	b.WriteString(accent("Agent users") + "\n")
	// The editor renders its own refusals inside the editor; a bare list error
	// (corrupt pool, failed delete) replaces the list instead.
	switch p.pool.kind {
	case poolInputDelete:
		id := p.pool.users[p.pool.focus].UserID
		b.WriteString(dim(`  Delete agent user "`) + accent(id) + dim(`"? `))
		b.WriteString(dim("It is removed from agent_users.json.\n"))
		b.WriteString(dim("Enter delete · Esc/q cancel"))
		return choicePanelStyle.Width(w).Render(b.String())
	case poolInputEdit, poolInputEditField:
		return p.renderPoolEdit(w, listH)
	}
	if p.pool.errMsg != "" {
		b.WriteString(p.pool.errMsg + "\n")
		b.WriteString(dim("Esc close"))
		return choicePanelStyle.Width(w).Render(b.String())
	}
	users := p.pool.users
	if len(users) == 0 {
		b.WriteString(dim("No agent users yet") + "\n")
		b.WriteString(dim("a add user · Esc back"))
		return choicePanelStyle.Width(w).Render(b.String())
	}
	if p.pool.detail {
		return p.renderPoolDetail(w, users[p.pool.focus])
	}
	for i, u := range users {
		label := u.UserID + " " + dim("("+providerModel(u)+")")
		b.WriteString(rowLine(i == p.pool.focus, i+1, "", label, false) + "\n")
	}
	b.WriteString(dim("↑/↓ navigate · Enter detail · a add user · e edit user · d delete user · Esc back"))
	return choicePanelStyle.Width(w).Render(b.String())
}

// renderPoolDetail renders the focused entry's fields and the members bound
// to it. API key is intentionally shown in plaintext per the user's request.
func (p *teamPicker) renderPoolDetail(w int, u team.AgentUser) string {
	var b strings.Builder
	b.WriteString("  " + accent(u.UserID) + "\n")
	b.WriteString(dim("  Provider: ") + u.Provider + "\n")
	b.WriteString(dim("  Base URL: ") + u.BaseURL + "\n")
	b.WriteString(dim("  Model: ") + u.Model + "\n")
	b.WriteString(dim("  Effort: ") + u.Effort + "\n")
	if u.Identity != "" {
		b.WriteString(dim("  Identity: ") + u.Identity + "\n")
	}
	b.WriteString(dim("  Key: ") + u.APIKey + "\n")
	if u.SecretRef.StoreID != "" {
		b.WriteString(dim("  Secret store: ") + u.SecretRef.StoreID + "\n")
	}
	b.WriteString(dim("  Bound by:") + "\n")
	members := p.boundMembers(u.UserID)
	if len(members) == 0 {
		b.WriteString(dim("    (no member is bound to this entry)") + "\n")
	}
	for _, m := range members {
		b.WriteString("    " + m + "\n")
	}
	b.WriteString(dim("Esc back · e edit fields · d delete"))
	return choicePanelStyle.Width(w).Render(b.String())
}

// renderPoolEdit renders the entry field editor: the field list with its
// cursor on the left — the id first, editable only while adding — and a
// preview column of the same draft on the right. The api key renders in
// plaintext — the user's chosen contract overrides the default
// mask-everything policy on this screen — so a key edit is visible while
// typing. The draft renders, unsaved; only s persists it.
func (p *teamPicker) renderPoolEdit(w, listH int) string {
	var b strings.Builder
	u := p.pool.draft
	if p.pool.adding {
		b.WriteString(accent("Adding agent user") + "\n")
	} else {
		b.WriteString(dim("  editing ") + accent(u.UserID) + "\n")
	}
	if p.pool.errMsg != "" {
		b.WriteString(p.pool.errMsg + "\n")
	}
	col := max((w-8)/2, 12)
	preview := make([]string, len(poolEditFields))
	for i, f := range poolEditFields {
		preview[i] = poolFieldLabel(f) + ": " + poolFieldValue(u, i)
	}
	for i, f := range poolEditFields {
		val := poolFieldValue(u, i)
		if p.pool.kind == poolInputEditField && i == p.pool.edit {
			if f == team.AgentUserFieldProvider {
				val = p.pool.list.currentLabel() + " ▏"
			} else {
				val = p.pool.buf + "▏"
			}
		}
		mark := "  "
		if i == p.pool.edit {
			mark = "> "
		}
		left := mark + f + ": " + truncateCells(val, col-6)
		if !p.pool.adding && i == 0 {
			left = dim(left) // a published id is immutable
		}
		b.WriteString(padColumn(left, col) + dim("│ "+truncateCells(preview[i], col)) + "\n")
	}
	if p.pool.kind == poolInputEditField {
		if poolEditFields[p.pool.edit] == team.AgentUserFieldProvider {
			p.pool.list.resize(listH - 3)
			b.WriteString(p.pool.list.view(w, listH))
		} else {
			b.WriteString(dim("Enter confirm · Esc cancel"))
		}
	} else {
		b.WriteString(dim("↑/↓ field · Enter edit · s save · Esc cancel"))
	}
	return choicePanelStyle.Width(w).Render(b.String())
}

// truncateCells clips s to at most n terminal cells, ellipsizing the tail so a
// field value can never widen the column it renders in. Cells, not runes: CJK
// and emoji occupy two columns each, and ANSI SGR codes occupy none.
func truncateCells(s string, n int) string {
	if n <= 0 {
		return ""
	}
	return ansi.Truncate(s, n, "…")
}

// padColumn pads s to col terminal cells, always leaving at least one space
// before the divider so a full-width value never abuts it. Measuring in cells
// keeps the divider in one column: counting runes instead let ANSI SGR codes
// and wide characters shift it per row.
func padColumn(s string, col int) string {
	return s + strings.Repeat(" ", max(col-visibleWidth(s), 1))
}

// poolFieldLabel names a pool-form field for the preview column, distinct from
// the raw field name the editable column shows.
func poolFieldLabel(field string) string {
	switch field {
	case team.AgentUserFieldID:
		return "Id"
	case team.AgentUserFieldIdentity:
		return "Identity"
	case team.AgentUserFieldProvider:
		return "Provider"
	case team.AgentUserFieldBaseURL:
		return "Base URL"
	case team.AgentUserFieldModel:
		return "Model"
	case team.AgentUserFieldEffort:
		return "Effort"
	default:
		return "API key"
	}
}

// providerModel labels a pool row with the entry's provider and model, or
// "unconfigured" when the entry carries neither.
func providerModel(u team.AgentUser) string {
	switch {
	case u.Provider == "" && u.Model == "":
		return "unconfigured"
	case u.Provider == "":
		return u.Model
	case u.Model == "":
		return u.Provider
	default:
		return u.Provider + " / " + u.Model
	}
}

// renderInputState renders the add/delete prompts and reports whether one was
// active, since those replace the screen underneath.
func (p *teamPicker) renderInputState(view *tui.Model, b *strings.Builder) (string, bool) {
	switch p.kind {
	case teamInputAdd:
		b.WriteString(dim("  New team name: ") + p.buf + "▏\n")
		b.WriteString(dim("Enter confirm · Esc cancel"))
	case teamInputAddMember:
		b.WriteString(dim("  New member id: ") + p.buf + "▏\n")
		b.WriteString(dim("Enter confirm · Esc cancel"))
	case teamInputDelete:
		b.WriteString(dim(`  Delete team "`) + accent(view.Name()) + dim(`"? `))
		b.WriteString(dim("It is removed from team.json.\n"))
		b.WriteString(dim("Enter delete · Esc/q cancel"))
	case teamInputDeleteMember:
		member, ok := view.Focused()
		if !ok {
			return b.String(), true
		}
		b.WriteString(dim(`  Delete member "`) + accent(member.ID) + dim(`"? `))
		b.WriteString(dim("It is removed from team.json.\n"))
		b.WriteString(dim("Enter delete · Esc/q cancel"))
	case teamInputBind:
		member, ok := view.Focused()
		if !ok {
			return b.String(), true
		}
		b.WriteString(dim(`  Bind "`) + accent(member.ID) + dim(`" to:`) + "\n")
		if len(p.binds) == 0 {
			b.WriteString(dim("    (no agent users yet — a add on the pool screen)"))
		} else {
			for i, id := range p.binds {
				mark := "    "
				if i == p.bind {
					mark = "  > "
				}
				b.WriteString(dim(mark+id) + "\n")
			}
		}
		b.WriteString(dim("↑/↓ candidate · Enter bind · Esc unbind/cancel"))
	case teamInputDefaultAgent:
		b.WriteString(dim("  Team default agent user:\n"))
		if len(p.binds) == 0 {
			b.WriteString(dim("    (no agent users yet — u opens the pool)\n"))
		} else {
			for i, id := range p.binds {
				mark := "    "
				if i == p.bind {
					mark = "  > "
				}
				label := id
				if label == "" {
					label = "(none - sessions disabled)"
				}
				b.WriteString(mark + label + "\n")
			}
		}
		b.WriteString(dim("↑/↓ candidate · Enter set default · Esc clear/cancel"))
	default:
		return "", false
	}
	return b.String(), true
}

// renderTeamList renders the entry screen: every registered team with its
// roster size. An empty registry shows how to create the first team instead of
// an error, so a fresh project is not a dead end.
func (p *teamPicker) renderTeamList(view *tui.Model, b *strings.Builder) {
	teams := view.Teams()
	if len(teams) == 0 {
		b.WriteString(dim("No team yet") + "\n")
		b.WriteString(dim("a add team · u agent users · Esc close"))
		return
	}
	for i, t := range teams {
		label := t.Name + " " + dim("("+rosterSize(len(t.Members))+")")
		b.WriteString(rowLine(i == view.TeamIndex(), i+1, "", label, false) + "\n")
	}
	b.WriteString(dim("↑/↓ navigate · Enter/Space open · a add team · d delete team · u agent users · Esc close · q quit"))
}

// rosterHelp is the roster's single help block: one line while the panel is
// wide enough, word-wrapped at the edge when it is not.
const rosterHelp = "↑/↓ navigate · a add member · d delete member · g default agent · 🌟 t Enter_session · p proxy · e edit · l assign leader · " + teamExitAllHint + " · Esc back · " +
	teamExitHint + " · q quit"

// renderRoster renders the compact member list: one row per slot with the
// member id and its role, leader marker, and lifecycle status. The agent
// fields — launch type and agent-user binding — stay readable in the
// document but the UI never renders them.
// Every key shown here acts on the member row: a/d add and delete, p cycles
// the proxy override, l assigns/clears the focused member's leader, t opens
// the team session (leader only), e opens the member editor.
func (p *teamPicker) renderRoster(view *tui.Model, b *strings.Builder, w int) {
	members := view.Members()
	defaultRef := p.defaultAgentUser()
	if defaultRef == "" {
		b.WriteString(dim("  Default agent: not configured") + "\n")
	} else {
		b.WriteString(dim("  Default agent: ") + accent(defaultRef) + "\n")
	}
	if len(members) == 0 {
		b.WriteString(dim("No team members yet") + "\n")
		b.WriteString(dim("a add member · Esc back"))
		return
	}
	for i, member := range members {
		label := member.ID + " " + dim("("+compactMemberSummary(member, p.statusOf(member.ID))+")")
		b.WriteString(rowLine(i == view.FocusIndex(), i+1, "", label, member.State == team.MemberStateWorking) + "\n")
	}
	b.WriteString(dim(ansi.Wrap(rosterHelp, w-1, "")))
}

// compactMemberSummary joins a member's role, leader marker, and status for
// the compact roster row: "coder · leader · active" for a leader, "tester ·
// active" otherwise. A slot with no business role (a legacy leader import)
// simply omits the role.
func compactMemberSummary(member team.Member, status string) string {
	parts := make([]string, 0, 3)
	if role := string(member.Role); role != "" {
		parts = append(parts, role)
	}
	if member.Leader {
		parts = append(parts, "leader")
	}
	return strings.Join(append(parts, status), " · ")
}

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
				val = me.buf + " ▏"
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
	default:
		return "Agent"
	}
}

// memberFieldValue reads a member property from the editor draft by field id.
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

// renderTeamSession renders the session detail panel (§5/§11.4): the team name,
// the bound member's persisted properties, the full roster beside it for
// switching (unread terminal-event counts on non-current members), and
// session-scoped errors. It is opt-in — [ TEAM ] reveals it — because the bound
// member's history is the main transcript, which this panel only shortens.
func (p *teamPicker) renderTeamSession(w int) string {
	var b strings.Builder
	b.WriteString(accent(p.session.teamName+" · session") + "\n")
	if p.session.errMsg != "" {
		b.WriteString(p.session.errMsg + "\n")
	}
	col := max((w-8)/2, 16)
	var left, right strings.Builder
	slot, ok := p.sessionSlot()
	left.WriteString("  " + accent(p.session.current) + "\n")
	if ok {
		role := "-"
		if slot.Role != "" {
			role = string(slot.Role)
		}
		leader := "off"
		if slot.IsLeader() {
			leader = "on"
		}
		left.WriteString(dim("  Role: ") + role + "\n")
		left.WriteString(dim("  Leader: ") + leader + "\n")
		left.WriteString(dim("  Status: ") + string(slot.Status) + "\n")
	}
	for i, id := range p.session.members {
		mark := "    "
		if i == p.session.focus {
			mark = "  > "
		}
		label := mark + id
		if n := p.session.unread[id]; n > 0 && id != p.session.current {
			label += " " + accent(strconv.Itoa(n))
		}
		if pr, ok := p.session.prompts[id]; ok && id != p.session.current {
			label += " " + accent(memberPromptMarker(pr.kind))
		}
		right.WriteString(dim(label) + "\n")
	}
	ll := strings.Split(strings.TrimSuffix(left.String(), "\n"), "\n")
	rl := strings.Split(strings.TrimSuffix(right.String(), "\n"), "\n")
	n := max(len(ll), len(rl))
	for i := range n {
		var l, r string
		if i < len(ll) {
			l = ll[i]
		}
		if i < len(rl) {
			r = rl[i]
		}
		b.WriteString(padColumn(l, col) + dim("│ ") + r + "\n")
	}
	hint := "Type below to message this member · Ctrl+Up/Down switch member"
	if p.sessionHasPendingApproval() {
		hint += " · Ctrl+A approve · Ctrl+X deny"
	}
	hint += " · Esc hide panel · " + teamExitHint
	b.WriteString(dim(hint))
	return choicePanelStyle.Width(w).Render(b.String())
}

// sessionHasPendingApproval reports whether any non-current member's inbox
// holds a pending approval — the condition that arms the Ctrl+A/Ctrl+X hint.
func (p *teamPicker) sessionHasPendingApproval() bool {
	if p.session.prompts == nil {
		return false
	}
	for id, pr := range p.session.prompts {
		if id != p.session.current && pr.kind == promptApproval {
			return true
		}
	}
	return false
}

// memberPromptMarker is the roster badge for a non-current member's pending
// prompt: an approval answers by keybinding (Ctrl+A/Ctrl+X), a question card
// needs its own member's window, so it reads as "answer by switching".
func memberPromptMarker(kind string) string {
	if kind == promptApproval {
		return "⌛"
	}
	return "❓"
}

// renderLeaderReset renders the k step-down confirmation (§6): the warning,
// the exact-id stage, the directory-list stage, or the finished result. The
// target team and member come from the registry, never from the buffer.
func (p *teamPicker) renderLeaderReset(w int) string {
	var b strings.Builder
	r := &p.reset
	teamName := p.model.Name()
	b.WriteString(accent(teamName+" · step down") + "\n")
	switch r.kind {
	case leaderResetWarn:
		b.WriteString(dim("  This removes the leader marker and clears every member's") + "\n")
		b.WriteString(dim("  context in this team. Other teams and normal chat history") + "\n")
		b.WriteString(dim("  are untouched.") + "\n")
	case leaderResetID:
		b.WriteString(dim(`  Type the exact leader id to confirm: `) + accent(p.resetTargetID()) + "\n")
		b.WriteString("  " + r.buf + "▏" + "\n")
	case leaderResetList:
		b.WriteString(dim("  Clearing ") + accent(teamName) + dim(" member contexts:") + "\n")
		b.WriteString("  " + dim(strconv.Itoa(p.resetDirCount(teamName))+" directories under .reasonix/team/context/") + "\n")
	case leaderResetDone:
		b.WriteString(dim("  Leader stepped down; team contexts cleared.") + "\n")
	}
	if r.errMsg != "" {
		b.WriteString(r.errMsg + "\n")
	}
	switch r.kind {
	case leaderResetWarn, leaderResetList:
		b.WriteString(dim("Enter continue · Esc cancel"))
	case leaderResetID:
		b.WriteString(dim("Enter confirm · Esc cancel"))
	case leaderResetDone:
		b.WriteString(dim("Enter/Esc close"))
	}
	return choicePanelStyle.Width(w).Render(b.String())
}

// statusOf is the member's persisted lifecycle status, defaulting to active for
// a slot the registry no longer carries.
func (p *teamPicker) statusOf(id string) string {
	if slot, ok := p.slotOf(id); ok {
		return string(slot.Status)
	}
	return string(team.MemberStatusActive)
}

// rosterSize labels a team row's member count.
func rosterSize(n int) string {
	if n == 1 {
		return "1 member"
	}
	return strconv.Itoa(n) + " members"
}

// overlayMouseMode keeps the team roster mouse-captured only while open,
// falling back to the chat's native scrollback selection otherwise.
func (m chatTUI) overlayMouseMode() tea.MouseMode {
	if m.teamOverlayModal() || m.mouseCaptureOff {
		return tea.MouseModeNone
	}
	return tea.MouseModeCellMotion
}
