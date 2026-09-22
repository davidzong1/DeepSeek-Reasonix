package cli

import (
	"context"
	"log/slog"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/event"
	"reasonix/internal/team"
)

// teamRosterRefreshMsg invalidates the in-memory roster while the team
// overlay remains open. Leader tools mutate team.json through their own store,
// so the TUI needs a small polling loop to observe those cross-session writes.
//
// sync carries the result of the tick's off-goroutine durable-history read back
// through the same message: the read is scheduled by the tick, so its result
// rides the tick instead of needing a second update branch (see syncBoundHistory).
// replay does the same for a member's off-goroutine transcript render
// (team_replay.go), and super for the members' cockpit reports
// (team_member_cockpit.go) — neither is a tick of its own.
type teamRosterRefreshMsg struct {
	sync   *historySyncDone
	replay *teamReplayReadyMsg
	super  []cockpitResult
}

const teamRosterRefreshInterval = time.Second

func teamRosterRefresh() tea.Cmd {
	return tea.Tick(teamRosterRefreshInterval, func(time.Time) tea.Msg { return teamRosterRefreshMsg{} })
}

// refreshTeamRoster re-reads the registry and keeps the active session's
// member list aligned with disk. A removed current member falls back to the
// first remaining leader; a new member becomes immediately switchable.
//
// The same 1s tick also polls the bound member's history identity, so a second
// window that appended, cleared or branched the same canonical owner is noticed
// without a re-enter or a rebind (see syncBoundHistory). The poll runs after the
// roster has settled: a member removed remotely must be rebound before its
// owner is read, or the window reads the fingerprint of a member it is about to
// leave and reports its own rebind as a remote clear.
func (m *chatTUI) refreshTeamRoster(msg teamRosterRefreshMsg) tea.Cmd {
	if m == nil {
		return nil
	}
	if len(msg.super) > 0 {
		// A member's cockpit report: the tick that armed this collection is still
		// pending, so this path applies the report and arms no second tick.
		m.applyCockpitResults(msg.super)
		return nil
	}
	if m.teamPick == nil || m.teamPick.store == nil {
		return nil
	}
	var replayCmd tea.Cmd
	if msg.replay != nil {
		replayCmd = m.handleTeamReplayReady(*msg.replay)
	}
	if msg.sync != nil {
		replayCmd = batchCmds(replayCmd, m.handleHistorySyncDone(*msg.sync))
	}
	m.syncAmbientOwnerUsage()
	next := m.refreshTeamRosterView()
	return batchCmds(replayCmd, m.syncBoundHistory(), m.refreshBoundMemberUsage(), m.collectCockpitResults(), next)
}

// refreshBoundMemberUsage hands the bound member's published usage to the tick's
// off-goroutine read, so the status band stops reading that document on the frame
// path (see refreshUsage). A follower is the only backend whose usage lives on
// disk: when the window owns the writer's own controller, its numbers are already
// in memory and there is nothing to refresh.
//
// The read answers no message. The tick that armed it is already in flight, and a
// second result would arm a second tick — the same reason the tick carries the
// results that do need delivering (see refreshTeamRoster).
func (m *chatTUI) refreshBoundMemberUsage() tea.Cmd {
	follower, ok := m.ctrl.(*memberFollowerBackend)
	if !ok || follower == nil {
		return nil
	}
	return func() tea.Msg {
		follower.refreshUsage(context.Background())
		return nil
	}
}

// syncAmbientOwnerUsage keeps the usage channel of the member this window's own
// chat writes. The leader's chat is a member's canonical session — the team
// store records that member's history stem as this session's identity — so the
// window that shows that member shows a read-only follower whose writer is this
// very process, and without this its gauges would stay empty forever.
//
// The condition is the published identity: while the bound member's owner stem
// is this window's ambient stamp, the ambient chat is that member's writer. The
// publisher owns its own cadence, so it survives the team overlay being left
// (the member backends do too); the condition is re-evaluated every tick and
// the publisher is stopped the moment it stops holding.
func (m *chatTUI) syncAmbientOwnerUsage() {
	if m == nil {
		return
	}
	p := m.teamPick
	if p == nil || p.owners == nil {
		return
	}
	fingerprint, ok := m.boundOwnerFingerprint()
	if m.ambient == nil || !ok || !fingerprint.Present || fingerprint.Corrupt ||
		!sameOwnerIdentity(fingerprint.Stem, m.ambient.HistoryStamp()) {
		m.stopAmbientOwnerUsage()
		return
	}
	if p.ambientUsage != nil {
		return
	}
	p.ambientUsage = newMemberUsagePublisher(p.owners,
		team.OwnerKey{TeamID: p.sessionTeamName(), MemberID: p.session.current}, m.ambient)
	p.ambientUsage.Start()
	slog.Info("team ambient usage publisher started", "team", p.sessionTeamName(), "member", p.session.current)
}

// sameOwnerIdentity reports whether two history stamps name the same session.
// The generation in a stamp is per-writer bookkeeping — the owner document's is
// bumped by publishes, the controller's by its own history mutations — so the
// two counters agree only by accident. What identifies the writer is the
// identity in front of the colon, which is exactly what a follower follows.
func sameOwnerIdentity(ownerStem, writerStamp string) bool {
	ownerID, ownerOK := followerStemIdentity(ownerStem)
	writerID, writerOK := followerStemIdentity(writerStamp)
	return ownerOK && writerOK && ownerID == writerID
}

// stopAmbientOwnerUsage stops the ambient writer's publisher, if any: the
// window no longer writes the member whose channel it was keeping fresh.
func (m *chatTUI) stopAmbientOwnerUsage() {
	if m == nil || m.teamPick == nil || m.teamPick.ambientUsage == nil {
		return
	}
	m.teamPick.ambientUsage.Close()
	m.teamPick.ambientUsage = nil
	slog.Info("team ambient usage publisher stopped", "team", m.teamPick.sessionTeamName(), "member", m.teamPick.session.current)
}

// refreshTeamRosterView is the roster half of the tick: reload the registry and
// align the session's member list with it.
func (m *chatTUI) refreshTeamRosterView() tea.Cmd {
	p := m.teamPick
	teamName := p.model.Name()
	if err := p.reload(teamName); err != nil {
		p.errMsg = pickerErrMsg(err)
		return teamRosterRefresh()
	}
	if !p.session.active {
		return teamRosterRefresh()
	}
	ids := make([]string, 0, len(p.model.Members()))
	for _, member := range p.model.Members() {
		ids = append(ids, member.ID)
	}
	oldCurrent := p.session.current
	p.session.members = ids
	if i := slices.Index(ids, oldCurrent); i >= 0 {
		p.session.focus = i
		return teamRosterRefresh()
	}
	// The bound member was removed remotely. Rebind to the current leader when
	// possible, preserving the team session instead of forcing a reopen.
	leader := p.firstLeader()
	if leader == "" {
		m.closeSession()
		return teamRosterRefresh()
	}
	p.session.current = leader
	p.session.focus = slices.Index(ids, leader)
	if cmd := m.switchTeamMember(leader); cmd != nil {
		return tea.Batch(cmd, teamRosterRefresh())
	}
	return teamRosterRefresh()
}

// sessionState is the team session window (§5/§11.4): which member's Agent the
// window is bound to, and the roster it switches across. Switching changes only
// the bound backend — contexts are never copied or merged. The selected member
// persists through the TeamSessionStore seam (route §4.2); the UI never writes
// session files itself. Typing goes to the main composer, which submits to
// whichever member is bound.
// memberPrompt records one non-current member's pending approval/ask, so the
// leader can answer an approval by keybinding without switching members and the
// roster can mark a question card that still needs switching. kind is the
// decision surface; id correlates with the member's own backend's reply.
type memberPrompt struct {
	kind    string // promptApproval | promptAsk
	id      string
	tool    string // approval subject, for the authorization log
	subject string
}

// The prompt surfaces: an approval answers by keybinding through the hub, while
// a question card needs its structured card, so it keeps the switch path but
// stays recorded (and marked) until it is answered.
const (
	promptApproval = "approval"
	promptAsk      = "ask"
)

// poolSessionRefusal is the empty-pool session gate: a session window dials a
// team pool entry, so entering one on a team with no configured agent-user
// pool parks here until the roster u editor sets one. The gate reads the
// team's own pool — a member's pin or custom pool never substitutes — and
// every session entry shares the hint so it cannot drift between enter,
// restore, and open.
const poolSessionRefusal = "No agent pool configured for this team — press u on the roster to configure one before opening a session"

// sessionSelectionRefusal is the unreadable-selection gate: the persisted
// member window exists but cannot be read, so opening the fallback would show a
// member the user did not choose and then persist that replacement. The file is
// named in the hint because it is the only thing the user can act on.
const sessionSelectionRefusal = "Team session preference is unreadable — fix or remove it, then reopen the team"

// teamPoolConfigured reports whether the focused team has at least one
// agent-user pool entry — its own entries or the legacy default reference.
// That is the session gate's subject: entry requires the team-level
// configuration, so a member-only binding can never unlock a session on an
// unconfigured team.
func (p *teamPicker) teamPoolConfigured() bool {
	name := p.model.Name()
	for _, t := range p.doc.Teams {
		if t.Name == name {
			return t.DefaultRef() != ""
		}
	}
	return false
}

type sessionState struct {
	active   bool
	panel    bool // detail panel; off by default so the member's history owns the frame
	teamName string
	current  string
	members  []string
	focus    int
	errMsg   string                   // session-scoped error, separate from the roster's errMsg
	unread   map[string]int           // non-current members' terminal events, per member
	prompts  map[string]memberPrompt  // non-current members' pending approval/ask, per member
	live     map[string][]event.Event // non-current members' in-flight turn, replayed on switch
	// syncStamp is the durable history identity the window last rendered for
	// the bound member. The 1s tick compares it to the member's current stamp,
	// so a history another window changed is adopted without a rebind.
	syncStamp string
	// syncPending records that a history change was observed while the window
	// was busy and could not be adopted yet. The next idle tick retries, so a
	// change that arrives mid-turn is not silently dropped.
	syncPending bool
	// syncInFlight is the stamp of a durable-history read running off the UI
	// goroutine: it suppresses a duplicate read and matches a late result, which
	// is dropped when the window has moved on.
	syncInFlight string
	// syncErrKey is the last failure this window reported, so a failure that
	// repeats on every tick or every turn is said once instead of burying the
	// transcript.
	syncErrKey string
}

// newSessionState arms one team session window. The per-member maps are created
// together because every one of them is read unconditionally on the event path:
// a nil map there is a silently dropped member event, which is exactly how an
// in-flight turn became invisible.
func newSessionState(teamName, current string) sessionState {
	return sessionState{
		active: true, teamName: teamName, current: current,
		unread:  map[string]int{},
		prompts: map[string]memberPrompt{},
		live:    map[string][]event.Event{},
	}
}

// setSessionPanel shows or hides the bound session's detail panel. Its rows come
// out of the transcript's height, so the tail is re-pinned: keeping the old
// offset would leave the newest output off-screen as the frame grows.
func (m *chatTUI) setSessionPanel(show bool) {
	if !m.teamSessionBound() || m.teamPick.session.panel == show {
		return
	}
	m.teamPick.session.panel = show
	m.forceGotoBottom = true
}

// sessionPanelHidden reports whether a bound session is rendering no panel at
// all — the default, so the member's own history fills the frame. The status
// line's member buttons stay the switch affordance either way.
func (p *teamPicker) sessionPanelHidden() bool {
	return p.session.active && !p.session.panel
}

// enterTeamSession opens the session window on the focused member and binds its
// Agent backend. A non-leader entry auto-corrects to the team's leader — the
// session belongs to the leader (§11.4); only a team with no leader at all
// refuses. The leader property is read from the registry (control layer, §5 —
// never a UI marker).
func (m *chatTUI) enterTeamSession() tea.Cmd {
	p := m.teamPick
	member, ok := p.model.Focused()
	if !ok {
		return nil
	}
	if slot, ok := p.slotOf(member.ID); !ok || !slot.IsLeader() {
		leader := p.firstLeader()
		if leader == "" {
			p.refusal = "Only the leader can start a team session"
			return nil
		}
		p.model.FocusMember(leader) // the roster highlights the session's member
		member, ok = p.model.Focused()
		if !ok {
			return nil
		}
	}
	if !p.teamPoolConfigured() {
		p.refusal = poolSessionRefusal
		return nil
	}
	p.errMsg = ""
	p.refusal = ""
	session := newSessionState(p.model.Name(), member.ID)
	for i, sm := range p.model.Members() {
		session.members = append(session.members, sm.ID)
		if sm.ID == member.ID {
			session.focus = i
		}
	}
	p.session = session
	if err := p.persistSessionSelection(); err != nil {
		// The window still opens on the member; only the restart preference
		// failed to land, and the refusal banner is where the page says so.
		p.refusal = "Selection not saved: " + pickerErrMsg(err)
	}
	return tea.Batch(m.switchTeamMember(member.ID), teamRosterRefresh())
}

// restoreSession resumes the persisted member window when its member is still
// the roster's leader — the session gate is the leader property, mirroring the
// t key — and falls back to the focused team's leader session otherwise. The
// second result reports a deliberate leave: suspended selections park on the
// management page, never a refusal. An absent or stale selection is a fallback,
// never an error: the [TEAM] click opens the management page's leader window as
// it always has.
//
// A selection that exists but cannot be read is not an absent one. It is refused
// rather than fallen back from: the fallback would open a different member's
// window over the operator's recorded choice, and the next deliberate switch
// persists that replacement, destroying the only record of where they were. The
// refusal leaves the file untouched for the operator to fix or remove.
func (p *teamPicker) restoreSession() (string, bool) {
	sel, selErr := p.readSelection()
	if selErr != nil {
		p.refusal = sessionSelectionRefusal
		return "", false
	}
	if sel.Suspended {
		return "", true
	}
	if p.firstLeader() == "" {
		p.refusal = "Only the leader can start a team session"
		return "", false
	}
	if slot, ok := p.slotOf(sel.MemberID); ok && slot.IsLeader() {
		// The session will open on this member: the team's configured pool —
		// never the member's own binding — is the gate.
		if !p.teamPoolConfigured() {
			p.refusal = poolSessionRefusal
			return "", false
		}
		return p.openSession(sel.MemberID), false
	}
	if !p.teamPoolConfigured() {
		p.refusal = poolSessionRefusal
		return "", false
	}
	return p.openSession(""), false
}

// readSelection reads the focused team's persisted session selection. No store,
// no focused team, or no file yet is an empty selection and a nil error, so the
// caller only has to distinguish "nothing recorded" from "recorded but broken".
func (p *teamPicker) readSelection() (team.SessionSelection, error) {
	if p.sessions == nil {
		return team.SessionSelection{}, nil
	}
	teamName := p.model.Name()
	if teamName == "" {
		return team.SessionSelection{}, nil
	}
	return p.sessions.ReadSelection(teamName)
}

// openSession puts the overlay on the focused team's given member's window and
// reports the member to bind — session active, member current, the chat composer
// hidden, the roster beside it for switching (§11.4). An empty initial opens the
// first leader; a team with no leader returns "" and stays on the management
// page: the leader marker is the gate, mirroring the t key.
func (p *teamPicker) openSession(initial string) string {
	if p.sessions == nil {
		return ""
	}
	if p.firstLeader() == "" {
		p.refusal = "Only the leader can start a team session"
		return ""
	}
	teamName := p.model.Name()
	if teamName == "" {
		return ""
	}
	current := initial
	if current == "" {
		current = p.firstLeader()
	}
	if current == "" {
		return ""
	}
	// The team's configured pool is the dialing gate: a session belongs to the
	// team's pool, so no member-level binding opens a window on an unconfigured
	// team.
	if !p.teamPoolConfigured() {
		p.refusal = poolSessionRefusal
		return ""
	}
	session := newSessionState(teamName, current)
	for i, m := range p.model.Members() {
		session.members = append(session.members, m.ID)
		if m.ID == current {
			session.focus = i
		}
	}
	p.session = session
	return current
}

// persistSessionSelection writes the current member window through the
// session store (§4.2): the only persisted session data is the selection —
// histories live in the member context directories.
//
// Persisting a member clears the suspension: the window is open on someone, so
// the user is not parked on the management page, and a stale suspension would
// make the next [TEAM] click ignore the member this call just stored. The write
// is a locked read-modify-write rather than a whole-document replace, so it
// cannot reset a field another process changed in between. The error is
// returned rather than dropped: a selection that did not persist is what a
// relaunch resumes, so the caller has to be able to say so.
func (p *teamPicker) persistSessionSelection() error {
	if p.sessions == nil {
		return nil
	}
	_, err := p.sessions.UpdateSelection(p.session.teamName, 0, func(sel *team.SessionSelection) error {
		sel.MemberID = p.session.current
		sel.Suspended = false
		return nil
	})
	return err
}

func (m *chatTUI) stepSession(d int) tea.Cmd {
	p := m.teamPick
	n := len(p.session.members)
	if n == 0 {
		return nil
	}
	next := (p.session.focus + d + n) % n
	target := p.session.members[next]
	var cmd tea.Cmd
	// Only a wired registry can refuse: a missing one means no member backends
	// exist at all (tests, non-interactive hosts), which must not block display
	// navigation. A wired one returning no command is a real refusal.
	if m.teamBackends != nil {
		if cmd = m.switchTeamMember(target); cmd == nil {
			return nil // refused: keep showing whoever is bound
		}
	}
	p.session.focus = next
	p.session.current = target
	if err := p.persistSessionSelection(); err != nil {
		p.refusal = "Selection not saved: " + pickerErrMsg(err)
	}
	delete(p.session.unread, target)
	return cmd
}

// closeSession tears the session window down and hands the window back to the
// chat's own backend. Member backends stay assembled in the registry: their
// histories, leases and in-flight state are untouched, so re-entering the team
// resumes them instead of rebuilding.
func (m *chatTUI) closeSession() {
	m.teamPick.session = sessionState{}
	m.unbindTeamMember()
}

// teamExitKey leaves the team from any overlay screen; teamExitHint names it in
// the help lines, so key and label cannot drift apart. Ctrl+T shadows the
// composer's transpose binding, but only while the overlay is up — a one-key way
// out of the team is worth more there.
const (
	teamExitKey  = "ctrl+t"
	teamExitHint = "Ctrl+T exit team"
)

// exitTeam tears the whole team UI down in one step, from any depth: the bound
// member is unbound too, so the window is back on the chat's own backend and its
// history. Esc still unwinds one layer at a time; this is the way straight out.
// Member backends survive, as they do across any overlay close.
//
// Teardown only. Whether the next entry reopens a session is a separate
// decision, persisted by suspendAutoSession — this function runs on every close,
// including a plain esc out of the team list, which must not change it.
func (m *chatTUI) exitTeam() {
	if m.teamPick == nil {
		return
	}
	m.stopAmbientOwnerUsage()
	m.closeSession()
	if m.quickPick != nil && m.quickPick.kind == quickPickerMemberAgentUser {
		m.quickPick = nil // it lists the bound member's models: it leaves with the team
	}
	// The member-backend registry and its board deliberately survive: an assembled
	// member keeps running its turn. closeTeamResources ends them once, after the
	// TUI releases the terminal.
	m.teamPick = nil
	// The overlay's rows go back to the transcript, so the tail is re-pinned:
	// keeping the old offset would leave the newest output off-screen.
	m.forceGotoBottom = true
}

// closeTeamResources releases the member backends and the board they share, at
// the one point it is safe to: after the TUI released the terminal. Nothing did
// this before, so every assembled member kept its session lease and plugin
// subprocesses to process end.
func (m *chatTUI) closeTeamResources() {
	// Hand the controller identity back first, or the caller's own ctrl.Close()
	// lands on a member controller the registry is about to close. This path
	// skips bindBackend, so the mounted view state is re-owned right here.
	if m.ambient != nil {
		m.ctrl = m.ambient
		m.ambient = nil
	}
	m.todo.bind(ownerKey{})
	m.todoArgs = ""
	if m.teamPick != nil {
		m.teamPick.dropMemberEditDraft()
	}
	if m.teamBackends != nil {
		// Drop queued requests first: closeAll unblocks every blocked waiter, and
		// each release then finds its entry already gone rather than racing it.
		m.teamEscalations.clear()
		m.teamBackends.closeAll()
		m.teamBackends = nil
	}
}

// leaveTeamDeliberately is the Ctrl+T contract: the user said "get me out of the
// team", which is both a teardown and a preference — the next [ TEAM ] click
// parks on the management page, across restarts, until a deliberate t.
func (m *chatTUI) leaveTeamDeliberately() {
	if m.teamPick == nil {
		return
	}
	if err := m.teamPick.suspendAutoSession(); err != nil {
		// Ctrl+T leaves the team either way — the user asked to get out — but a
		// preference that did not persist is the one thing the key was supposed
		// to leave behind, so it is surfaced rather than dropped.
		m.notice("team auto-session preference not saved: " + pickerErrMsg(err))
	}
	m.exitTeam()
}

// suspendAutoSession persists "do not reopen a session here" for the team being
// left. Disk, not a field: the flag has to outlive the process, or relaunching
// silently re-enters the session the user just left. A deliberate entry clears it
// by writing its own selection.
//
// The write is a locked partial update and it drops the member id: a suspend is
// the user saying they are done with the window, so leaving a member id behind
// would have the next entry — a click on [TEAM], a restart — resume a session
// the user just left. Returns the store's error rather than swallowing it.
func (p *teamPicker) suspendAutoSession() error {
	if p.sessions == nil {
		return nil
	}
	teamName := p.exitingTeamName()
	if teamName == "" {
		return nil
	}
	_, err := p.sessions.UpdateSelection(teamName, 0, func(sel *team.SessionSelection) error {
		sel.MemberID = ""
		sel.Suspended = true
		return nil
	})
	return err
}

// exitingTeamName is the team a leave applies to: the bound session's team, else
// the focused one. The selection is keyed by team name, so writing the preference
// under the wrong key would suspend a team the user never left. Read it before
// closeSession — that zeroes the session, teamName included.
func (p *teamPicker) exitingTeamName() string {
	if p.session.teamName != "" {
		return p.session.teamName
	}
	return p.model.Name()
}

// clearSelectedMember drops the persisted member while leaving the auto-session
// preference alone: k removes a leader, it does not decide whether the next entry
// opens a session. It returns the store's error rather than swallowing it: a
// clear that did not persist leaves a leader id a relaunch would try to resume.
func (p *teamPicker) clearSelectedMember(teamName string) error {
	if p.sessions == nil {
		return nil
	}
	_, err := p.sessions.UpdateSelection(teamName, 0, func(sel *team.SessionSelection) error {
		sel.MemberID = ""
		return nil
	})
	return err
}

// sessionSlot returns the current member's persisted slot for the session
// window's info lines.
func (p *teamPicker) sessionSlot() (team.MemberSlot, bool) {
	return p.slotOf(p.session.current)
}
