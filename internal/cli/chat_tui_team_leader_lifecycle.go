package cli

import (
	"fmt"

	"reasonix/internal/team"
)

// assignFocusedLeader sets the focused member as the team's leader — the l
// key's assign-only contract. A team that already has a leader refuses with
// the holder's id: leaders step down through k, never through a toggle. The t
// session gate reads the same registry field, so a just-assigned leader can
// open the team session. Setting is idempotent and reloads the registry.
func (p *teamPicker) assignFocusedLeader() error {
	member, ok := p.model.Focused()
	if !ok {
		return team.ErrMemberNotFound
	}
	if current := p.firstLeader(); current != "" && current != member.ID {
		return fmt.Errorf("team: %q already has a leader %q — it steps down through k", p.model.Name(), current)
	}
	if slot, ok := p.slotOf(member.ID); ok && slot.IsLeader() {
		return nil // already the leader: assign is a no-op
	}
	if err := p.store.SetMemberLeader(p.model.Name(), member.ID, true); err != nil {
		return err
	}
	return p.reload("")
}

// stepDownLeader runs k's destructive half in strict order: the team's
// assembled backends stop first, then the leader's private session files and
// the team's shared context tree are cleared, and only then is the leader
// marker published off (write-before-commit: a stop or clear failure aborts
// before anything is written). The blackboard — its SQLite events, bindings
// and cursors, and the member identities they carry — is shared data, not
// leader context: nothing here touches it.
func (p *teamPicker) stepDownLeader(teamName, memberID string) error {
	slot, ok := p.slotOf(memberID)
	if !ok {
		return team.ErrMemberNotFound
	}
	if !slot.IsLeader() { // verify: the flag may have changed since the UI armed
		return fmt.Errorf("team: %q is not the leader", memberID)
	}
	if err := p.stopTeamBackends(teamName); err != nil {
		return err
	}
	if err := p.clearTeamHistories(teamName); err != nil {
		return err
	}
	if err := p.store.SetMemberLeader(teamName, memberID, false); err != nil {
		return err
	}
	if p.sessions != nil {
		_ = p.sessions.WriteSelection(teamName, team.SessionSelection{Team: teamName})
	}
	if err := p.reload(""); err != nil {
		return err
	}
	p.closeTeamSessions()
	return nil
}

// stopTeamBackends retires every assembled backend of the team and refuses to
// continue while any survived the close: k must not clear contexts a backend
// could still be writing. Close is synchronous and errorless today, so the
// survival check is the failure surface — it stays for the day Close can fail.
func (p *teamPicker) stopTeamBackends(teamName string) error {
	if p.backends == nil {
		return nil
	}
	p.backends.releaseTeam(teamName)
	if n := p.backends.liveTeamCount(teamName); n > 0 {
		return fmt.Errorf("team: %d backend(s) of %q survived the stop", n, teamName)
	}
	return nil
}

// closeTeamSessions closes the session window from the picker side, so the
// roster renders again. Member backends and the leader marker are untouched:
// re-entering the team resumes the members' histories. The m-level unbind —
// handing the window back to the chat's own backend — is the TUI's job, since
// the stopped backends can no longer back the window.
func (p *teamPicker) closeTeamSessions() {
	p.session = sessionState{}
}
