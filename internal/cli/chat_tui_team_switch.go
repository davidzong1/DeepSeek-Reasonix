package cli

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/boot"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/team"
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
// events render into the transcript exactly as the ambient session's do, while
// another member's are buffered so switching to it shows the turn it is running
// right now — History() only carries committed messages, so without the buffer
// an in-flight turn looked like an idle member. The pump re-arms either way.
func (m *chatTUI) handleMemberEvent(msg memberEventMsg) tea.Cmd {
	if msg.member == m.boundMember() {
		m.noteWatchdogHeartbeat(watchdogAgentSource(msg.ev.Kind))
		m.ingestEvent(msg.ev)
	} else if m.teamPick == nil {
		m.noteOrphanedMemberPrompt(msg.member, msg.ev)
	} else {
		m.bufferMemberEvent(msg.member, msg.ev)
		if memberEventNeedsAttention(msg.ev.Kind) {
			m.recordMemberPrompt(msg.member, msg.ev)
			m.markMemberUnread(msg.member)
		}
	}
	return waitForMemberEvent(m.memberEvents)
}

// noteOrphanedMemberPrompt surfaces a member that blocked on a decision after the
// user left the team. Its backend stays assembled and its run goroutine waits on
// an answer, but with no overlay there is no inbox to record into and no roster to
// badge — the event was simply dropped, so the member hung with nothing on screen.
// The prompt itself is not lost: ReplayPendingPrompts re-raises it on the next
// bind, which is exactly what the notice tells the user to do.
func (m *chatTUI) noteOrphanedMemberPrompt(member string, ev event.Event) {
	switch ev.Kind {
	case event.ApprovalRequest, event.AskRequest:
		m.notice("team member " + member + " is waiting for a decision — open [ TEAM ] and switch to it")
	}
}

// memberLiveEventCap bounds one unbound member's buffered turn. Streaming deltas
// dominate the count, so the cap is what keeps a long background turn from
// growing without limit; the oldest events are dropped first, which degrades to
// "you see the tail of what it is doing" rather than to nothing.
const memberLiveEventCap = 512

// bufferMemberEvent keeps one unbound member's in-flight turn so a switch can
// replay it. A finished turn clears the buffer: its content is in the member's
// own History() from then on, and replaying both would double it. Prompt events
// are excluded because ReplayPendingPrompts owns re-emitting those on bind —
// buffering them too would raise the same card twice.
func (m *chatTUI) bufferMemberEvent(member string, ev event.Event) {
	if m.teamPick == nil || m.teamPick.session.live == nil {
		return
	}
	switch ev.Kind {
	case event.TurnDone:
		delete(m.teamPick.session.live, member)
		return
	case event.ApprovalRequest, event.AskRequest:
		return
	}
	buffered := append(m.teamPick.session.live[member], ev)
	if over := len(buffered) - memberLiveEventCap; over > 0 {
		buffered = buffered[over:]
	}
	m.teamPick.session.live[member] = buffered
}

// replayMemberLiveEvents renders the turn a member started while the window was
// elsewhere. It runs after the history replay committed, so the buffered events
// land on top of the member's committed transcript in the order they arrived —
// the same order the bound path ingested them in.
func (m *chatTUI) replayMemberLiveEvents(member string) {
	if m.teamPick == nil || m.teamPick.session.live == nil {
		return
	}
	for _, ev := range m.teamPick.session.live[member] {
		m.ingestEvent(ev)
	}
}

// recordMemberPrompt keeps one non-current member's pending approval/ask on the
// session — the inbox that lets the leader answer by keybinding instead of
// switching. An approval/ask registers its id; a finished turn clears the
// record, because the turn settling means the prompt resolved there.
func (m *chatTUI) recordMemberPrompt(member string, ev event.Event) {
	if m.teamPick == nil || m.teamPick.session.prompts == nil {
		return
	}
	switch ev.Kind {
	case event.TurnDone:
		delete(m.teamPick.session.prompts, member)
	case event.ApprovalRequest:
		m.teamPick.session.prompts[member] = memberPrompt{kind: promptApproval, id: ev.Approval.ID}
	case event.AskRequest:
		m.teamPick.session.prompts[member] = memberPrompt{kind: promptAsk, id: ev.Ask.ID}
	}
}

// memberEventNeedsAttention reports whether an unbound member's event is worth
// a badge: a finished turn, a failure, or a pending approval/ask the window is
// not showing. Streaming deltas never count — they would make the counter climb
// for every token. ApprovalRequest is included because a background member's
// unanswered prompt is exactly the "member waiting" state the leader must see.
func memberEventNeedsAttention(kind event.Kind) bool {
	switch kind {
	case event.TurnDone, event.Message, event.ApprovalRequest, event.AskRequest:
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

// memberSwitchBusy is the narrow gate for switching members (§4.5): only an
// unanswered prompt (approval/ask) blocks the window move — its run goroutine
// waits on a decision only the window can surface. A running turn or background
// job does NOT block: the backend survives the swap, its events keep flowing on
// the shared pump, and switching back replays the transcript from history.
// PendingPrompt is kept as a race guard: between the backend registering the
// prompt and the TUI ingesting its event, the user's keypress can arrive while
// m.pendingApproval is still nil. The shared runtimeSwitchBusy stays full —
// model/effort/skill/web switches rebuild or tear down the backend, so Running
// still refuses those.
func (m *chatTUI) memberSwitchBusy() bool {
	if m == nil || m.ctrl == nil {
		return false
	}
	status := m.ctrl.RuntimeStatus()
	return status.PendingPrompt || m.pendingApproval != nil || m.chooser != nil
}

// switchTeamMember binds the window to one member's Agent backend: the same
// hot-swap the model switch performs, but pointed at another member instead of
// another model. The transcript is rebuilt from the incoming backend's own
// history, so the window shows that member's context and nothing of the
// previous one. The switch never interrupts a turn (§4.5); only an unanswered
// prompt refuses it, and pending prompts replay on re-entry.
func (m *chatTUI) switchTeamMember(memberID string) tea.Cmd {
	p := m.teamPick
	if p == nil || p.store == nil || m.teamBackends == nil {
		return nil
	}
	if m.memberSwitchBusy() {
		return m.refuseTeamSession("Answer the pending approval before switching member")
	}
	binding, err := p.store.Binding(p.sessionTeamName(), memberID)
	if err != nil {
		return m.refuseTeamSession(pickerErrMsg(err))
	}
	backend, err := m.teamBackends.bind(binding)
	if err != nil {
		return m.refuseTeamSession("member unavailable: " + err.Error())
	}

	p.session.current = memberID
	p.session.errMsg = ""
	p.hub.setTeam(p.sessionTeamName())
	delete(p.session.unread, memberID) // showing a member is consuming it
	// The record is the roster inbox's, for members the window is not showing.
	// Keeping it for the member being bound leaves a badge and an armed
	// Ctrl+A hint on a prompt its own modal is about to own.
	delete(p.session.prompts, memberID)
	if m.ambient == nil {
		m.ambient = m.ctrl // first member bind: remember the chat's own backend
	}
	m.bindBackend(backend)
	// The turn this member started while the window was elsewhere: bindBackend
	// replayed its committed history, and these are the events that turn has
	// produced since, which no History() snapshot can carry yet.
	m.replayMemberLiveEvents(memberID)
	// Replay the member's pending approval/ask card: the window was elsewhere
	// when the prompt registered. The event flows through the member's own sink
	// onto the shared pump, ingested once the member is bound. No prompt, none.
	backend.ReplayPendingPrompts()
	return waitForMemberEvent(m.memberEvents)
}

// sessionTeamName is the team a member switch resolves against: the bound
// session's own team, not the roster's focused one. They are normally equal, but
// the picker's focus is free to move, and resolving a binding against it would
// bind a same-named member of a different team.
func (p *teamPicker) sessionTeamName() string {
	if p.session.active && p.session.teamName != "" {
		return p.session.teamName
	}
	return p.model.Name()
}

// refuseTeamSession records a session-scoped refusal where the user can
// actually read it. session.errMsg renders in exactly one place — the detail
// panel — and R7 made that panel opt-in, so a panel-only refusal is invisible in
// the default layout: a failed bind then looks like "the team opened but shows
// the wrong model". The transcript is always on screen, so the reason goes there
// too. Returns nil so callers stay one-line.
func (m *chatTUI) refuseTeamSession(msg string) tea.Cmd {
	if m.teamPick == nil {
		return nil
	}
	m.teamPick.session.errMsg = msg
	m.notice(msg)
	return nil
}

// unbindTeamMember hands the window back to the chat's own backend when the
// team session closes. Member backends stay assembled in the registry — their
// histories and leases are untouched, so re-entering the team resumes them.
// closeSession has already dropped the active flag, so bindBackend restores
// the ambient session lease (a member bind never does).
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
	// Only the ambient restore follows the ambient session lease; a member
	// bind never does — the member owns its own lease and authority.
	if !m.teamSessionBound() {
		m.followSessionLease()
	}
	m.refreshEffortStatus()

	// A composer draft was addressed to the outgoing backend; carrying it over
	// would silently submit it to a different member.
	m.input.SetValue("")
	m.finalizeStreamed()
	m.pending.Reset()
	m.reasoning.Reset()
	m.todoArgs = ""
	m.chooser = nil
	m.pendingApproval = nil
	m.bubblePending = false
	m.turnDiscarded = false
	// The footer working line reads m.state/turnPhase/elapsed/turnTokens of the
	// previously bound member; without clearing them a switch to an idle member
	// would keep showing the outgoing member's "working" status. The incoming
	// member's own events restore its real phase on replay.
	m.state = tuiIdle
	m.turnPhase = ""
	m.elapsed = 0
	m.turnTokens = 0
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
	// A registry built before this commit pinned a board-less task service (D1);
	// drop it rather than re-create the registry around a stale service. The
	// registry itself is rebuilt from the shared board on the next overlay open.
	if m.teamBackends != nil {
		m.teamBackends.closeAll()
		m.teamBackends = nil
	}
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
	bind := func(b team.MemberBinding) (control.SessionAPI, error) {
		if m.teamBackends == nil {
			return nil, team.ErrMemberNotFound
		}
		return m.teamBackends.bind(b)
	}
	// The board is opened before this call (see onTeamButtonClick), so the task
	// service — and with it every leader task tool — gets its durable store on
	// the very first overlay, not on some later open.
	// The first call to bindTeamBackends happens on the first overlay open,
	// when m.ctrl is still the chat's own backend — the ambient source a
	// leader's first member session continues from. It is captured by reference
	// (nil when the seam races differently), so the carrier reads the chat at
	// the moment that member is actually assembled.
	var ambientCtrl control.SessionAPI = m.ctrl
	var ambientCarrier func() []provider.Message
	if ambientCtrl != nil {
		ambientCarrier = func() []provider.Message { return ambientCtrl.History() }
	}
	tasks := newTeamTaskService(m.teamPick.store, m.teamPick.boardStore(), "", bind)
	memberDeps := memberBackendDeps{
		ctx: context.Background(), users: users, store: m.teamPick.store, sessions: m.teamPick.sessions,
		tasks:  tasks,
		events: m.memberEvents, base: m.memberBackendBase,
		ambient: ambientCarrier,
		release: func(teamName, memberID string) {
			if m.teamBackends != nil {
				m.teamBackends.release(teamName, memberID)
			}
		},
	}
	m.teamBackends = newTeamBackends(newMemberBackendBuilder(memberDeps), 0)
	m.teamBackends.setTasks(tasks)
	// Invalidate a member's assembled backend when its pool entry (ref,
	// provider, model, base url or API key) changed, so a rebind never keeps
	// serving the previous provider/credential.
	m.teamBackends.setFingerprint(newMemberBackendFingerprint(memberDeps))
}

// teamSessionBound reports whether the window is showing a team member's Agent.
// While bound, the main composer is visible and owns typing: submitting reaches
// that member because m.ctrl IS its backend. The roster and its transient states
// stay modal — only this one state shares the keyboard.
func (m *chatTUI) teamSessionBound() bool {
	return m.teamPick != nil && m.teamPick.session.active
}

// teamOpenKey opens the team overlay from the plain chat by keyboard. On SSH
// the TUI deliberately runs without application mouse capture (the terminal's
// native selection and right-click menu stay usable), so no MouseClickMsg ever
// reaches the [ TEAM ] button — the key is the reliable entry point there.
// Alt is preferred over Ctrl+Shift on purpose: several common ssh clients and
// meta modes collapse Ctrl+Shift+T to Ctrl+T, which would collide with the
// team exit key.
const teamOpenKey = "alt+t"

// handleTeamKey routes a keypress while the team overlay is open and reports
// whether the overlay consumed it. The roster and its transient states consume
// everything. A bound member session consumes only the reserved keys — member
// switch and esc — so every other key falls through to the composer, which is
// the whole point: one composer, one transcript, whichever backend is bound.
// teamExitKey is reserved on every screen: it leaves the team outright. With no
// overlay open, teamOpenKey opens the overlay and nothing else is consumed, so
// the keyboard is never claimed by a team UI that is not there.
func (m chatTUI) handleTeamKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.teamPick == nil {
		if msg.String() == teamOpenKey {
			return m, m.onTeamButtonClick(), true
		}
		return m, nil, false
	}
	// Checked before every state owner, so no depth — an open field, an armed
	// confirmation — can swallow the one key that leaves.
	if msg.String() == teamExitKey {
		m.leaveTeamDeliberately()
		return m, nil, true
	}
	if !m.teamSessionBound() {
		next, cmd := m.handleTeamPickerKey(msg)
		return next, cmd, true
	}
	switch msg.String() {
	case "ctrl+up":
		return m, m.stepSession(-1), true
	case "ctrl+down":
		return m, m.stepSession(+1), true
	case "ctrl+a", "ctrl+x":
		// A non-current member's pending approval answers by keybinding without a
		// switch; no pending prompt for the focused member leaves the key to the
		// composer (select-all / cut), so it is only consumed when one exists.
		if m.answerMemberPrompt(msg.String() == "ctrl+a") {
			return m, nil, true
		}
		return m, nil, false
	case "esc":
		// The panel is a layer over the session, so esc dismisses it before the
		// session itself: leaving the team is one esc further out.
		if m.teamPick.session.panel {
			m.setSessionPanel(false)
			return m, nil, true
		}
		m.closeSession()
		return m, nil, true
	}
	return m, nil, false
}

// teamOverlayModal reports whether the team overlay is hiding the composer: the
// roster and its transient states do, a bound member session does not — there
// the composer is the session's input.
func (m chatTUI) teamOverlayModal() bool {
	return m.teamPick != nil && !m.teamPick.session.active
}

// answerMemberPrompt approves or denies a background member's pending approval
// through the hub, so the decision reaches that member's own backend without
// switching the window. It reports whether it consumed the key: a waiting member
// does, no waiting member leaves Ctrl+A/Ctrl+X to the composer. The bound
// member's own prompt is never here — that stays on the pendingApproval modal the
// ordinary keys answer.
func (m *chatTUI) answerMemberPrompt(allow bool) bool {
	p := m.teamPick
	if p == nil || p.hub == nil {
		return false
	}
	member, prompt, ok := p.pendingApprovalMember()
	if !ok {
		return false
	}
	if err := p.hub.Approve(p.session.teamName, member, prompt.id, allow, false, false); err != nil {
		m.refuseTeamSession("could not answer " + member + ": " + err.Error())
		return true
	}
	delete(p.session.prompts, member)
	if n := p.session.unread[member]; n > 0 {
		p.session.unread[member] = n - 1
	}
	verb := "approved"
	if !allow {
		verb = "denied"
	}
	m.notice(verb + " " + member + "'s pending approval")
	return true
}

// pendingApprovalMember is the background member Ctrl+A/Ctrl+X answers: the first
// in roster order still waiting on an approval. A bound session moves focus and
// current together, so there is no independent cursor — reading focus is why
// these keys could never fire, since focus always indexed the bound member and
// the "not the bound member" guard then rejected every press. A question card is
// deliberately not offered: it needs its own structured surface, so it keeps the
// switch path and its own roster badge.
func (p *teamPicker) pendingApprovalMember() (string, memberPrompt, bool) {
	if p == nil || len(p.session.prompts) == 0 {
		return "", memberPrompt{}, false
	}
	for _, id := range p.session.members {
		if id == p.session.current {
			continue
		}
		if prompt, ok := p.session.prompts[id]; ok && prompt.kind == promptApproval {
			return id, prompt, true
		}
	}
	return "", memberPrompt{}, false
}

// runMemberModelSubcommand is /model while a team member is bound: a member's
// model is whatever its pool entry configures, so choosing here rebinds the
// member to another agent-user entry and reassembles its backend from it —
// never the chat's own model (§8-5 拍板). It reports whether it handled the
// command; an unbound window falls through to the ordinary /model path.
func (m *chatTUI) runMemberModelSubcommand(input string) bool {
	if !m.teamSessionBound() {
		return false
	}
	p := m.teamPick
	if p.store == nil {
		m.refuseTeamSession("member model unavailable: no team store")
		return true
	}
	args := tokenizeArgs(input) // args[0] == "/model"
	if len(args) < 2 {
		m.openMemberModelPicker()
		return true
	}
	m.rebindMemberAgentUser(args[1])
	return true
}

// rebindMemberAgentUser points the bound member at another pool entry and
// reassembles it. The old backend is retired first: its provider, credential and
// role prompt were baked in at assembly, so only a rebuild picks up the change.
// The rebuild happens immediately, so the window keeps serving a live backend
// bound to the new pool entry instead of a retired one. The full shared gate
// applies — retire kills a running turn, so Running still refuses a rebind even
// though switching members now allows it (§4.5).
func (m *chatTUI) rebindMemberAgentUser(ref string) {
	if !m.teamSessionBound() {
		return // the session closed under an open picker: no member left to rebind
	}
	p := m.teamPick
	member := p.session.current
	if m.runtimeSwitchBusy() {
		m.refuseTeamSession("Finish or stop the current turn before changing the member's model")
		return
	}
	if err := p.store.BindAgentUser(p.model.Name(), member, ref); err != nil {
		m.refuseTeamSession(pickerErrMsg(err))
		return
	}
	if err := p.reload(member); err != nil {
		m.refuseTeamSession(pickerErrMsg(err))
		return
	}
	if m.teamBackends != nil {
		if err := m.rebindTeamBackend(p, member); err != nil {
			m.refuseTeamSession(pickerErrMsg(err))
			return
		}
	}
	p.session.errMsg = ""
	m.notice("member " + member + " now uses agent user " + ref)
}

// rebindTeamBackend reassembles the member's backend onto the window. The
// registry's fingerprint invalidation rebuilds it — the binding's agent-user
// ref changed, so the fingerprint differs from assembly — and a failed rebuild
// leaves the previous backend serving, so the window never strands on a closed
// controller.
func (m *chatTUI) rebindTeamBackend(p *teamPicker, member string) error {
	binding, err := p.store.Binding(p.model.Name(), member)
	if err != nil {
		return err
	}
	backend, err := m.teamBackends.bind(binding)
	if err != nil {
		// bind keeps the previous backend assembled and serving on failure.
		return err
	}
	m.bindBackend(backend)
	backend.ReplayPendingPrompts()
	return nil
}

// openMemberModelPicker lists the agent-user pool as the bound member's model
// choices: one entry is one provider/model/credential set, so the pool is the
// member's model catalog.
func (m *chatTUI) openMemberModelPicker() {
	p := m.teamPick
	users, err := p.store.ListAgentUsers()
	if err != nil {
		m.refuseTeamSession(pickerErrMsg(err))
		return
	}
	if len(users) == 0 {
		m.refuseTeamSession("No agent users yet — Esc to the roster, then u to add one")
		return
	}
	slot, _ := p.slotOf(p.session.current)
	items := make([]quickPickerItem, 0, len(users))
	selected := 0
	for _, u := range users {
		status := ""
		if u.UserID == slot.AgentUserRef {
			status = "active"
			selected = len(items)
		}
		items = append(items, quickPickerItem{
			ID: u.UserID, Label: u.UserID,
			Description: providerModel(u), Status: status,
		})
	}
	m.quickPick = &quickPicker{
		kind: quickPickerMemberAgentUser, title: "Agent user for member " + p.session.current,
		items: items, selected: selected,
	}
}
