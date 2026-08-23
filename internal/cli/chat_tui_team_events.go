package cli

import (
	"slices"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/team/agentruntime"
)

// teamRuntimeEventMsg is one member-runtime event delivered into the TUI
// update loop (§11.5). It carries the live subscription so the next read
// re-arms on the same stream; a closed stream arrives as a zero event.
type teamRuntimeEventMsg struct {
	sub   agentruntime.Subscription
	event agentruntime.RuntimeEvent
}

// subscribeTeamRuntime converts one blocking read on the subscription into a
// tea message; the command re-arms itself after every delivered event.
func subscribeTeamRuntime(sub agentruntime.Subscription) tea.Cmd {
	return func() tea.Msg {
		return teamRuntimeEventMsg{sub: sub, event: <-sub.C}
	}
}

// handleTeamRuntimeEvent routes one runtime event (§11.5): current-member
// events refresh the window (render re-reads the store on every frame),
// non-current-member terminal events count as unread on the roster column,
// and events from a closed or foreign stream are dropped. The subscription
// re-arms while the session window stays open.
func (m chatTUI) handleTeamRuntimeEvent(msg teamRuntimeEventMsg) tea.Cmd {
	p := m.teamPick
	if p == nil || !p.session.active {
		return nil // a stale delivery after the window closed
	}
	ev := msg.event
	if ev.Team != p.session.teamName || ev.MemberID == "" || ev.Kind == "" {
		return nil // closed stream or a foreign instance
	}
	if ev.MemberID == p.session.current {
		switch ev.Kind {
		case agentruntime.EventError:
			p.session.errMsg = ev.Text
		case agentruntime.EventMessage, agentruntime.EventDone:
			p.session.errMsg = ""
		}
	} else if sessionEventIsTerminal(ev.Kind) && p.sessionHasMember(ev.MemberID) {
		p.session.unread[ev.MemberID]++
	}
	return subscribeTeamRuntime(msg.sub)
}

// sessionHasMember reports whether the id is on the session window's roster;
// events from members outside it (never switched to, never started) are
// dropped instead of counting unread on a row that does not exist.
func (p *teamPicker) sessionHasMember(id string) bool {
	return slices.Contains(p.session.members, id)
}

// sessionEventIsTerminal reports whether the event kind must reach the UI:
// terminal events (final message, done, error, stopped) count as unread for
// non-current members; started/delta never do, or the counter would explode.
func sessionEventIsTerminal(kind agentruntime.RuntimeEventKind) bool {
	switch kind {
	case agentruntime.EventMessage, agentruntime.EventDone, agentruntime.EventError, agentruntime.EventStopped:
		return true
	}
	return false
}

// bindSessionSubscription (re)subscribes the current member's runtime event
// stream (§11.5): switching members cancels the old subscription and arms the
// target's. An unassembled runtime leaves the session event-less, not broken
// — the send path still works and events simply arrive later.
func (p *teamPicker) bindSessionSubscription() tea.Cmd {
	if p.runtime == nil {
		return nil
	}
	p.cancelSessionSubscription()
	key := agentruntime.InstanceKey{Team: p.session.teamName, MemberID: p.session.current}
	sub, err := p.runtime.Subscribe(key)
	if err != nil {
		p.session.sub = nil
		return nil // unassembled: no event stream yet, sends still work
	}
	p.session.sub = &sub
	return subscribeTeamRuntime(sub)
}

// cancelSessionSubscription stops the live subscription, if any: the stream
// stops after the window closes or the member switches, so no goroutine or
// message leaks past the session (§11.5).
func (p *teamPicker) cancelSessionSubscription() {
	if p.session.sub != nil {
		p.session.sub.Cancel()
		p.session.sub = nil
	}
}
