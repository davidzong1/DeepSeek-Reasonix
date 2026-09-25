package cli

import (
	"context"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/team"
)

// Cross-window history observation: the session window polls the bound
// member's canonical owner identity on the existing roster tick and adopts a
// change another runtime made, without a rebind and without a writer lease.

// historySyncTimeout bounds one durable-history read. The read pages the
// session's history index, which may rebuild itself over the whole log, so the
// bound is what keeps a wedged or huge store from leaving the refresh pending
// forever.
const historySyncTimeout = 5 * time.Second

// historySyncDone is the result of a durable-history read that ran off the UI
// goroutine. It rides the roster tick's own message so the update loop needs no
// second branch for it (see refreshTeamRoster).
type historySyncDone struct {
	member   string
	stamp    string
	reloaded bool
	err      error
}

// syncBoundHistory is the cross-window history poll: it compares the bound
// member's canonical owner identity with the one this window last rendered and
// adopts a change. It runs on the existing 1s roster tick, so no second timer
// exists and no keypress is needed.
//
// The change signal is the member's owner metadata — its stem plus its
// monotonic generation, which every history mutation bumps — because that is
// the one identity a second process can read without a lease. The content
// source stays the bound backend's own durable store, so the reload never takes
// or moves a writer lease.
//
// It never rebinds: the backend, its lease and the session selection are left
// exactly as they are — only the rendered transcript is rebuilt. A busy window
// (a running turn, a pending prompt) records the change and retries on a later
// tick, so local input and the in-flight turn are never dropped.
func (m *chatTUI) syncBoundHistory(fingerprint team.OwnerFingerprint, ok bool) tea.Cmd {
	if m == nil || !m.teamSessionBound() || m.ctrl == nil {
		return nil
	}
	session := &m.teamPick.session
	if !ok {
		return nil
	}
	// Fail closed: an owner document that cannot be read is not an unnamed
	// owner. Adopting it would render "stem:0" as if it were a real identity,
	// and the change that is actually on disk would never be seen.
	if fingerprint.Corrupt {
		m.reportHistorySyncFailure("corrupt:"+session.current,
			"team member "+session.current+"'s history metadata is unreadable — not refreshing it")
		return nil
	}
	stamp := ownerHistoryStamp(fingerprint)
	if session.syncStamp == "" {
		// First observation after a bind: the transcript was just replayed, so
		// the identity is adopted rather than acted on.
		session.syncStamp, session.syncPending, session.syncInFlight = stamp, false, ""
		return nil
	}
	if session.syncStamp == stamp && !session.syncPending {
		return nil
	}
	if !fingerprint.Present {
		// The canonical owner directory is gone — the team was cleared, or the
		// leader stepped down. There is nothing left to read, so the window goes
		// to the empty state instead of keeping a history that no longer exists.
		session.syncStamp, session.syncPending, session.syncInFlight = stamp, false, ""
		replayCmd := m.replayBoundHistory()
		m.notice("team member " + session.current + "'s history was cleared elsewhere")
		return replayCmd
	}
	if session.syncInFlight == stamp {
		return nil // a read for this change is already running
	}
	if status := m.ctrl.RuntimeStatus(); status.Running || status.PendingPrompt || status.BackgroundJobs > 0 {
		// Busy: the in-memory transcript is the authoritative one and adopting
		// the durable view underneath a live turn would drop local input. The
		// change stays pending and the next idle tick retries it.
		session.syncPending = true
		return nil
	}
	session.syncPending, session.syncInFlight = true, stamp
	return m.loadBoundHistoryCmd(m.ctrl, session.current, stamp)
}

// loadBoundHistoryCmd runs the durable-history read off the UI goroutine. The
// read reaches the session's history index, which can rebuild itself over the
// whole log; on the Update goroutine that rebuild freezes the frame for as long
// as it takes. The read is context-bounded (see historySyncTimeout) so a slow
// store surfaces as a visible failure rather than a read that never comes back.
func (m *chatTUI) loadBoundHistoryCmd(syncer control.HistorySync, member, stamp string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), historySyncTimeout)
		defer cancel()
		reloaded, err := syncer.ReloadHistoryIfChanged(ctx, stamp)
		return teamRosterRefreshMsg{sync: &historySyncDone{
			member: member, stamp: stamp, reloaded: reloaded, err: err,
		}}
	}
}

// handleHistorySyncDone applies one off-goroutine read's result. A result whose
// stamp is no longer the one in flight belongs to a window that has moved on —
// a rebind, a newer change, a closed session — so it is dropped rather than
// rendered over whatever the window shows now. It returns the command carrying
// the rebuilt transcript's pending work, if the reload has any.
func (m *chatTUI) handleHistorySyncDone(msg historySyncDone) tea.Cmd {
	if m == nil || !m.teamSessionBound() {
		return nil
	}
	session := &m.teamPick.session
	if session.current != msg.member || session.syncInFlight != msg.stamp {
		return nil
	}
	session.syncInFlight = ""
	if msg.err != nil {
		// The stamp stays unrecorded, so the change is still visible to the next
		// tick: a read failure is retried, never adopted as if it had landed.
		session.syncPending = true
		m.reportHistorySyncFailure("read:"+msg.member,
			"team member "+msg.member+"'s history could not be read: "+pickerErrMsg(msg.err))
		return nil
	}
	session.syncErrKey = ""
	if !msg.reloaded {
		// Nothing to render: the controller had already adopted this stamp.
		session.syncPending = false
		return nil
	}
	session.syncStamp, session.syncPending = msg.stamp, false
	return m.replayBoundHistory()
}

// reportHistorySyncFailure records a cross-window failure where the user can
// read it. It is said once per distinct cause: the poll runs every second and
// the publication runs on every settled turn, so a persistent failure would
// otherwise bury the transcript under a line a second. key names the cause; a
// later success clears it.
func (m *chatTUI) reportHistorySyncFailure(key, msg string) {
	if m == nil || m.teamPick == nil {
		return
	}
	session := &m.teamPick.session
	if session.syncErrKey == key {
		return
	}
	session.syncErrKey = key
	// The detail panel renders session.errMsg, and R7 made that panel opt-in, so
	// the transcript line is what makes the failure actually visible.
	session.errMsg = msg
	m.notice(msg)
}

// ownerFingerprintReadHook observes one owner-fingerprint disk read. Nil in
// production; a test installs it to pin how many reads a tick performs and which
// member they name — the shared read above is only safe while there is exactly
// one per tick, after the roster settled.
var ownerFingerprintReadHook func()

// boundOwnerFingerprint reads the bound member's canonical owner fingerprint.
// ok is false when there is nothing to poll — no owner store, no bound member,
// or a member id that cannot form an owner key. A read error is a failure to
// poll, not a missing owner, so it is reported rather than folded into the
// absent state: "no owner" and "cannot tell" drive different actions.
func (m *chatTUI) boundOwnerFingerprint() (team.OwnerFingerprint, bool) {
	p := m.teamPick
	if p == nil || p.owners == nil || p.session.current == "" {
		return team.OwnerFingerprint{}, false
	}
	if ownerFingerprintReadHook != nil {
		ownerFingerprintReadHook()
	}
	key := team.OwnerKey{TeamID: p.sessionTeamName(), MemberID: p.session.current}
	fingerprint, err := p.owners.Fingerprint(key)
	if err != nil {
		m.reportHistorySyncFailure("fingerprint:"+p.session.current,
			"team member "+p.session.current+"'s history identity is unavailable: "+pickerErrMsg(err))
		return team.OwnerFingerprint{}, false
	}
	return fingerprint, true
}

// ownerHistoryStamp renders one owner fingerprint as the opaque change signal
// the session window compares across ticks. An absent owner is its own stamp,
// so a cleared history is a change rather than a missing read. A corrupt owner
// is the "absent" stamp too: the reader refuses it before comparing, and it
// must never render as a generation the peer did not publish.
func ownerHistoryStamp(f team.OwnerFingerprint) string {
	if !f.Present || f.Corrupt {
		return "absent"
	}
	return f.Stem + ":" + strconv.FormatUint(f.Generation, 10)
}

// publishBoundOwnerHistory advances the bound member's owner generation after a
// history mutation this window performed — /clear and /branch replace the live
// session, so a peer window watching the same owner must be able to see it.
// The mutation already happened, so a failure is not a refusal; it is said out
// loud instead of dropped, because a peer that never learns of the change keeps
// rendering the transcript this window replaced.
func (m *chatTUI) publishBoundOwnerHistory() {
	if m == nil || !m.teamSessionBound() {
		return
	}
	p := m.teamPick
	if p == nil || p.owners == nil || p.store == nil {
		return
	}
	member := p.session.current
	binding, err := p.store.Binding(p.sessionTeamName(), member)
	if err != nil {
		m.reportHistorySyncFailure("publish-binding:"+member,
			"team member "+member+"'s history change was not published: "+pickerErrMsg(err))
		return
	}
	m.publishOwnerHistory(cockpitCommand{
		owners: p.owners, binding: binding, stamper: m.ctrl, bump: true, errKey: "publish:" + member,
	})
}

// publishOwnerHistory hands one publication to the member's cockpit worker, so
// the store write happens off the Update goroutine (team_member_cockpit.go). A
// window with no registry has no worker and runs it here, which is the historical
// inline path — tests and non-interactive hosts take it, and they observe the
// same result synchronously.
func (m *chatTUI) publishOwnerHistory(cmd cockpitCommand) {
	if m != nil && m.teamBackends != nil && m.teamBackends.cockpit().submit(cmd) {
		return
	}
	m.applyCockpitResults([]cockpitResult{runOwnerHistoryPublication(context.Background(), cmd)})
}

// applyCockpitResults folds the members' finished publications into the window,
// on the Update goroutine: a failure is reported once per cause, a success clears
// the failure it superseded.
func (m *chatTUI) applyCockpitResults(results []cockpitResult) {
	if m == nil {
		return
	}
	for _, res := range results {
		if res.errMsg != "" {
			m.reportHistorySyncFailure(res.errKey, res.errMsg)
			continue
		}
		if m.teamPick != nil {
			m.teamPick.session.syncErrKey = ""
		}
	}
}

// collectCockpitResults drains the members' finished publications so the roster
// tick can apply them. It is non-blocking by design: nothing ready means no
// message and no new command, and the next tick collects whatever landed since.
func (m *chatTUI) collectCockpitResults() tea.Cmd {
	if m == nil || m.teamBackends == nil {
		return nil
	}
	cockpit := m.teamBackends.cockpit()
	return func() tea.Msg {
		results := cockpit.drain()
		if len(results) == 0 {
			return nil
		}
		return teamRosterRefreshMsg{super: results}
	}
}

// publishTurnOwnerHistory advances the generation of whichever member's turn
// just settled. A background member's turn commits to the same canonical owner
// as the bound one, so a second window watching that owner must be able to
// notice the append without polling the transcript file itself — the bound
// member is only the one whose transcript this window happens to be showing.
//
// The bound member keeps its own path (m.ctrl, one bump per settled turn); any
// other member resolves through the registry's assembled backend, which is the
// only place a never-bound member's live identity exists. A member with no
// assembled backend, or one whose backend has no readable history identity yet,
// is skipped: publishing a stem this window made up would read to a peer
// exactly like a real change. A publication that fails is reported — the turn
// that settled is real either way, but a peer stuck on a stale transcript is
// exactly what the failure would otherwise hide.
func (m *chatTUI) publishTurnOwnerHistory(member string) {
	if m == nil || member == "" {
		return
	}
	if member == m.boundMember() {
		m.publishBoundOwnerHistory()
		return
	}
	p := m.teamPick
	if p == nil || p.owners == nil || p.store == nil || m.teamBackends == nil {
		return
	}
	backend, ok := m.teamBackends.bound(p.sessionTeamName(), member)
	if !ok {
		return
	}
	stamper, ok := backend.(memberHistoryStamper)
	if !ok {
		return
	}
	binding, err := p.store.Binding(p.sessionTeamName(), member)
	if err != nil {
		m.reportHistorySyncFailure("publish-binding:"+member,
			"team member "+member+"'s history change was not published: "+pickerErrMsg(err))
		return
	}
	// The identity read and the advance both happen on the member's cockpit
	// worker: requireIdentity is what keeps a member that cannot name its own
	// history from being published under a made-up one.
	m.publishOwnerHistory(cockpitCommand{
		owners: p.owners, binding: binding, stamper: stamper, bump: true,
		requireIdentity: true, errKey: "publish:" + member,
	})
}

// replayBoundHistory rebuilds the displayed transcript from the bound backend's
// history after a cross-window change. It is the display half of a reload only:
// nothing about the binding, the lease, the roster or the session selection is
// touched, which is what distinguishes this from switchTeamMember.
//
// It paints through the deferred path, so the frame does not read the history
// either: this runs whenever a peer appended to the member this window shows,
// and a follower's History() is a durable re-read, so a synchronous read here
// stalled the frame on every append rather than once per switch. The clear, the
// sessionSwitch arming and the install all happen together when the read lands
// (handleReplayReadReady) — clearing now would blank the transcript for the
// length of that read, which is the stall this path exists to avoid.
func (m *chatTUI) replayBoundHistory() tea.Cmd {
	if m == nil || m.ctrl == nil {
		return nil
	}
	m.finalizeStreamed()
	m.pending.Reset()
	m.reasoning.Reset()
	m.transcriptDirty = true
	m.forceGotoBottom = true
	return m.commitBackendReplay(m.ctrl, replayDeferred)
}
