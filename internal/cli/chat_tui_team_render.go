package cli

import (
	"strconv"
	"strings"

	"reasonix/internal/team"
	"reasonix/internal/team/tui"
)

// renderTeamPicker renders the team overlay: the team list, a team's member
// roster with lifecycle status, the focused member's context view, or one of the
// transient input/confirmation states. An unreadable registry renders its
// message instead of a list, and an empty one the create hint.
func (m chatTUI) renderTeamPicker() string {
	p := m.teamPick
	if p == nil {
		return ""
	}
	view := p.model
	w := max(m.width, 10)
	var b strings.Builder
	b.WriteString(accent(teamPickerTitle(view)) + "\n")
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
		p.renderMemberContext(view, &b)
	case tui.ModeQuit:
		b.WriteString(dim("Leave team view? Esc cancels · Enter or q confirms"))
	default:
		p.renderRoster(view, &b)
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
		b.WriteString(dim("a add team · Esc close"))
		return
	}
	for i, t := range teams {
		label := t.Name + " " + dim("("+rosterSize(len(t.Members))+")")
		b.WriteString(rowLine(i == view.TeamIndex(), i+1, "", label, false) + "\n")
	}
	b.WriteString(dim("↑/↓ navigate · Enter/Space open · a add team · d delete team · Esc close · q quit"))
}

// renderRoster renders one team's member slots with their lifecycle status.
func (p *teamPicker) renderRoster(view *tui.Model, b *strings.Builder) {
	members := view.Members()
	if len(members) == 0 {
		b.WriteString(dim("No team members yet") + "\n")
		b.WriteString(dim("a add member · Esc back"))
		return
	}
	for i, member := range members {
		label := member.ID + " " + dim("("+p.statusOf(member.ID)+")")
		b.WriteString(rowLine(i == view.FocusIndex(), i+1, "", label, member.State == team.MemberStateWorking) + "\n")
		b.WriteString(dim("  Role: "+string(member.Role)) + "\n")
	}
	b.WriteString(dim("↑/↓ navigate · Enter/Space view · a add member · d delete member · s status · Esc back · q quit"))
}

// renderMemberContext renders the focused member's detail view, showing only
// the pointers the document actually carries.
func (p *teamPicker) renderMemberContext(view *tui.Model, b *strings.Builder) {
	member, ok := view.Focused()
	if !ok {
		return
	}
	b.WriteString("  " + accent(member.ID) + "\n")
	b.WriteString(dim("  Role: ") + string(member.Role) + "\n")
	b.WriteString(dim("  State: ") + string(member.State) + "\n")
	if slot, ok := p.slotOf(member.ID); ok {
		b.WriteString(dim("  Status: ") + string(slot.Status) + "\n")
	}
	if member.TaskRef != "" {
		b.WriteString(dim("  Task: ") + string(member.TaskRef) + "\n")
	}
	if member.ContextRef != "" {
		b.WriteString(dim("  Context: ") + member.ContextRef + "\n")
	}
	if member.AgentUserRef != "" {
		b.WriteString(dim("  Agent user: ") + member.AgentUserRef + "\n")
	}
	b.WriteString(dim("↑/↓ switch member · a add · d delete · s status · Esc back · q quit"))
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
