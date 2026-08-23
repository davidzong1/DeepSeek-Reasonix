package cli

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/boot"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// waitForMemberEvent turns one blocking read on the shared tagged channel into
// a tea.Msg. One goroutine serves every member backend, so member switching
// never re-arms a second event pump (§11.5 replacement: no second event bus).
func waitForMemberEvent(ch chan memberEvent) tea.Cmd {
	return func() tea.Msg { return memberEventMsg(<-ch) }
}

// memberEventMsg is one member backend's event inside the update loop.
type memberEventMsg memberEvent

// handleMemberEvent routes one member backend's event: the bound member's
// events render into the transcript exactly as the ambient session's do, and
// another member's events only mark that member unread — never the transcript,
// which belongs to whoever is bound. The pump re-arms either way.
func (m *chatTUI) handleMemberEvent(msg memberEventMsg) tea.Cmd {
	if msg.member == m.boundMember() {
		m.noteWatchdogHeartbeat(watchdogAgentSource(msg.ev.Kind))
		m.ingestEvent(msg.ev)
	} else if memberEventIsTerminal(msg.ev.Kind) {
		m.markMemberUnread(msg.member)
	}
	return waitForMemberEvent(m.memberEvents)
}

// memberEventIsTerminal reports whether an unbound member's event is worth a
// badge: a finished turn or a failure, never streaming deltas — those would
// make the counter climb for every token.
func memberEventIsTerminal(kind event.Kind) bool {
	switch kind {
	case event.TurnDone, event.Message:
		return true
	}
	return false
}

// boundMember is the member whose backend m.ctrl currently is, or "" when the
// ambient session is bound (no team member selected).
func (m *chatTUI) boundMember() string {
	if m.teamPick == nil || !m.teamPick.session.active {
		return ""
	}
	return m.teamPick.session.current
}

// markMemberUnread counts one finished turn for a member the window is not
// showing, so the roster can badge it.
func (m *chatTUI) markMemberUnread(member string) {
	if m.teamPick == nil || m.teamPick.session.unread == nil {
		return
	}
	m.teamPick.session.unread[member]++
}

// switchTeamMember binds the window to one member's Agent backend: the same
// hot-swap the model switch performs, but pointed at another member instead of
// another model. The transcript is rebuilt from the incoming backend's own
// history, so the window shows that member's context and nothing of the
// previous one.
//
// A turn in flight refuses the switch: swapping m.ctrl under a running turn
// would leave its events arriving for a backend the window no longer shows.
func (m *chatTUI) switchTeamMember(memberID string) tea.Cmd {
	p := m.teamPick
	if p == nil || p.store == nil || m.teamBackends == nil {
		return nil
	}
	if m.runtimeSwitchBusy() {
		p.session.errMsg = "Finish or stop the current turn before switching member"
		return nil
	}
	binding, err := p.store.Binding(p.model.Name(), memberID)
	if err != nil {
		p.session.errMsg = pickerErrMsg(err)
		return nil
	}
	backend, err := m.teamBackends.bind(binding)
	if err != nil {
		p.session.errMsg = "member unavailable: " + err.Error()
		return nil
	}

	p.session.current = memberID
	p.session.errMsg = ""
	delete(p.session.unread, memberID) // showing a member is consuming it
	if m.ambient == nil {
		m.ambient = m.ctrl // first member bind: remember the chat's own backend
	}
	m.bindBackend(backend)
	return waitForMemberEvent(m.memberEvents)
}

// unbindTeamMember hands the window back to the chat's own backend when the team
// session closes. Member backends stay assembled in the registry — their
// histories and leases are untouched, so re-entering the team resumes them.
func (m *chatTUI) unbindTeamMember() {
	if m.ambient == nil {
		return // never bound a member
	}
	ambient := m.ambient
	m.ambient = nil
	m.bindBackend(ambient)
}

// bindBackend swaps the window's backend and rebuilds everything derived from
// it: the label and model line, the slash catalog, the session lease, and the
// transcript. It mirrors the model switch's own post-swap sync (chat_tui.go's
// modelSwitchMsg branch) so a member switch cannot drift from it.
func (m *chatTUI) bindBackend(backend control.SessionAPI) {
	m.ctrl = backend
	m.label = backend.Label()
	m.modelRef = backend.ModelRef()
	m.commands = backend.Commands()
	m.skills = backend.SlashSkills()
	m.setHostAndInvalidateSlashCatalog(backend.Host())
	m.updateWatchdogStatusProvider()
	m.followSessionLease()
	m.refreshEffortStatus()

	m.finalizeStreamed()
	m.pending.Reset()
	m.reasoning.Reset()
	m.todoArgs = ""
	m.chooser = nil
	m.pendingApproval = nil
	m.bubblePending = false
	m.turnDiscarded = false
	m.sessionSwitch = true
	// Discard the outgoing member's transcript: without this the viewport
	// accumulates every member ever bound, and the scroll offset lands inside
	// merged content (the same reason branch replay clears it).
	m.clearTranscriptDisplay()
	m.transcriptDirty = true
	m.forceGotoBottom = true
	m.commitTranscriptSource(transcriptSource{
		kind:    transcriptSourceReplayBundle,
		history: append([]provider.Message(nil), backend.History()...),
	})
}

// bindTeamBackendSeam installs the team seam onto the TUI: the one tagged event
// channel every member backend emits into, and the boot options a member backend
// inherits from this session's launch wiring (permissions, additional dirs,
// workspace root, session directory). The registry itself is created when the
// overlay opens, where the team store already exists — the model and the sink in
// these options are placeholders the member builder overrides per member.
func (m *chatTUI) bindTeamBackendSeam(maxSteps int, overrides cliBuildOverrides) {
	m.memberEvents = make(chan memberEvent, memberEventBuffer)
	m.memberBackendBase = func() boot.Options {
		return cliProfileBuildOptions("", maxSteps, false, event.Discard, overrides)
	}
}

// memberEventBuffer matches the ambient session's event channel: buffered
// generously so a streaming burst never backpressures a member's agent loop.
const memberEventBuffer = 1024

// bindTeamBackends creates the member-backend registry for an opened overlay.
// The pool lookup is the overlay's own store, so a member resolves its agent
// user from the same document the pool screen edits. Reopening the overlay keeps
// the existing registry: its backends hold session leases and in-flight state,
// and discarding them would orphan both.
func (m *chatTUI) bindTeamBackends(users memberPoolLookup) {
	if m.teamBackends != nil {
		return
	}
	if m.memberBackendBase == nil || m.memberEvents == nil || users == nil {
		return // seam not installed (tests, non-interactive hosts): no member backends
	}
	m.teamBackends = newTeamBackends(newMemberBackendBuilder(memberBackendDeps{
		ctx: context.Background(), users: users, events: m.memberEvents, base: m.memberBackendBase,
	}), 0)
}
