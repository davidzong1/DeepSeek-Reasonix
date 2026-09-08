package cli

import (
	"fmt"
	"slices"

	"reasonix/internal/control"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// failoverQuotaTurn is the P2 agent-user failover trigger, invoked after a
// bound member's turn ended in a provider quota refusal (TurnDone.Err). The
// failed pool entry is marked saturated for that member, the next usable pool
// entry is assembled on the member's own session file (history, skills and
// system identity preserved by the resume path), and the window is rebound to
// it. The user is asked to continue/retry; the original request is never
// re-sent. A member whose whole pool is saturated stays put with an explicit
// warning that gates further composer submits until the roster g reset.
func (m *chatTUI) failoverQuotaTurn(member string, err error) {
	if err == nil || !provider.QuotaExhausted(err) {
		return
	}
	p := m.teamPick
	if p == nil || p.store == nil || p.sessions == nil || m.teamBackends == nil {
		return
	}
	teamName := p.sessionTeamName()
	if teamName == "" {
		return
	}
	pool, active, st, err := p.failoverSnapshot(teamName, member)
	if err != nil {
		m.notice("quota failover: " + err.Error())
		return
	}
	if active == "" || !slices.Contains(pool, active) {
		return // an out-of-pool pin is an explicit choice, not a pool to walk
	}
	next := team.AdvanceFailover(st, pool, active)
	if next.Exhausted {
		if err := p.sessions.WriteMemberFailover(teamName, member, next); err != nil {
			m.notice("quota failover: " + err.Error())
			return
		}
		m.refuseTeamSession(m.teamMemberPoolBlocked())
		return
	}
	// Land the rebuild before persisting: a busy or unresolvable switch keeps
	// serving, so a failed advance never consumes a pool entry.
	var bindErr error
	if member == m.boundMember() {
		_, bindErr = m.rebindMemberToRef(teamName, member, next.ActiveRef)
	} else {
		// Background members must fail over without stealing the visible TUI
		// window. Their backend remains registered in the shared fleet and the
		// next roster switch resolves the durable ActiveRef normally.
		bindErr = p.rebindBackgroundMemberToRef(teamName, member, next.ActiveRef)
	}
	if bindErr != nil {
		pending := next
		pending.ActiveRef = active
		pending.Generation = st.Generation
		pending.Switches = st.Switches
		_ = p.sessions.WriteMemberFailover(teamName, member, pending) // best effort
		m.notice(fmt.Sprintf("%s hit its quota for member %s; switching to %s is pending: %s", active, member, next.ActiveRef, bindErr))
		return
	}
	if err := p.sessions.WriteMemberFailover(teamName, member, next); err != nil {
		m.notice("quota failover: " + err.Error())
		return
	}
	m.notice(fmt.Sprintf("%s hit its quota for member %s; switched to %s — send continue or retry the last message", active, member, next.ActiveRef))
}

// rebindBackgroundMemberToRef rebuilds an unbound member's backend on the
// requested pool entry without changing the visible TUI controller. It uses
// the same fingerprint proof as the bound path so a busy or failed rebuild
// never records a switch that did not actually land.
func (p *teamPicker) rebindBackgroundMemberToRef(teamName, member, ref string) error {
	if p == nil || p.store == nil || p.backends == nil {
		return fmt.Errorf("member %s backend is unavailable", member)
	}
	binding, err := p.store.Binding(teamName, member)
	if err != nil {
		return err
	}
	binding.AgentUserRef = ref
	already, err := p.backends.onRef(binding)
	if err != nil {
		return err
	}
	if _, err := p.backends.bind(binding); err != nil {
		return err
	}
	if already {
		return nil
	}
	on, err := p.backends.onRef(binding)
	if err != nil {
		return err
	}
	if !on {
		return fmt.Errorf("member %s is busy; the pool switch to %s applies when idle", member, ref)
	}
	return nil
}

// failoverSnapshot reconciles a member's persisted failover state with the
// member's current effective pool and its nominal binding — the custom pool
// head, else the pinned override, else the team pool head, all folded by
// MemberPool. active is the pool entry the member's backend currently points
// at: the persisted ActiveRef when it is still a pool member, else nominal.
func (p *teamPicker) failoverSnapshot(teamName, memberID string) (pool []string, active string, st team.MemberFailover, err error) {
	st, err = p.sessions.ReadMemberFailover(teamName, memberID)
	if err != nil {
		return nil, "", st, err
	}
	pool, nominal, err := p.store.MemberPool(teamName, memberID)
	if err != nil {
		return nil, "", st, err
	}
	active = nominal
	if st.ActiveRef != "" && slices.Contains(pool, st.ActiveRef) {
		active = st.ActiveRef
	}
	return pool, active, st, nil
}

// onRef reports whether the member's assembled backend already matches the
// binding's fingerprint — i.e. a bind for binding would reuse it rather than
// rebuild. It is the failover switch's proof that a rebuild landed: after bind,
// a requested ref change that still does not match means a busy backend kept
// serving the old entry.
func (r *teamBackends) onRef(b team.MemberBinding) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.live[backendKey(b.Team, b.MemberID)]; !ok {
		return false, nil
	}
	fp, err := r.currentFingerprint(b)
	if err != nil {
		return false, err
	}
	return r.fps[backendKey(b.Team, b.MemberID)] == fp, nil
}

// rebindMemberToRef rebuilds the member's backend pointed at ref, on the same
// session file (the binding carries the member's stable SessionFile), and binds
// the window to it. It returns an error when the rebuild could not land — a
// busy backend keeps serving, an unresolvable ref keeps the old one — so the
// caller never records a switch that did not happen.
func (m *chatTUI) rebindMemberToRef(teamName, member, ref string) (control.SessionAPI, error) {
	p := m.teamPick
	if p == nil || p.store == nil || m.teamBackends == nil {
		return nil, fmt.Errorf("member %s backend is unavailable", member)
	}
	binding, err := p.store.Binding(teamName, member)
	if err != nil {
		return nil, err
	}
	binding.AgentUserRef = ref
	already, err := m.teamBackends.onRef(binding)
	if err != nil {
		return nil, err
	}
	backend, err := m.teamBackends.bind(binding)
	if err != nil {
		return nil, err
	}
	if !already {
		on, err := m.teamBackends.onRef(binding)
		if err != nil {
			return nil, err
		}
		if !on {
			return nil, fmt.Errorf("member %s is busy; the pool switch to %s applies when idle", member, ref)
		}
	}
	m.bindBackend(backend)
	backend.ReplayPendingPrompts()
	return backend, nil
}

// teamMemberPoolBlocked returns the composer gate message when the bound
// member's failover state has exhausted every pool entry — the explicit
// warning that blocks further thinking until the roster g reset clears it.
// An empty result means the composer is free.
func (m *chatTUI) teamMemberPoolBlocked() string {
	if !m.teamSessionBound() {
		return ""
	}
	p := m.teamPick
	if p == nil || p.sessions == nil {
		return ""
	}
	member := m.boundMember()
	if member == "" {
		return ""
	}
	teamName := p.sessionTeamName()
	st, err := p.sessions.ReadMemberFailover(teamName, member)
	if err != nil || !st.Exhausted {
		return ""
	}
	return fmt.Sprintf("Every agent user in %s's pool has hit its quota for member %s — top up an agent user, then press g on the roster to reset the pool and retry", teamName, member)
}

// failoverBinding folds the member's durable failover ActiveRef onto a nominal
// binding when that ref is still a member of the member's current effective
// pool, so a bind after a restart (or any natural reopen) rebuilds the backend
// on the pool entry the runtime last switched to, not the nominal head. An
// absent or stale ActiveRef leaves the nominal binding untouched.
func (p *teamPicker) failoverBinding(b team.MemberBinding) team.MemberBinding {
	if p.sessions == nil || p.store == nil || b.AgentUserRef == "" {
		return b
	}
	pool, _, err := p.store.MemberPool(b.Team, b.MemberID)
	if err != nil || len(pool) == 0 {
		return b
	}
	st, err := p.sessions.ReadMemberFailover(b.Team, b.MemberID)
	if err != nil {
		return b
	}
	if st.ActiveRef != "" && slices.Contains(pool, st.ActiveRef) {
		b.AgentUserRef = st.ActiveRef
	}
	return b
}

// resetMemberFailover is the runtime half of the roster g reset: it clears the
// focused member's durable failover state (active ref back to the nominal pool
// head, saturation and exhaustion forgotten) and, when that member has an idle
// assembled backend on a moved ref, retires it so the next bind rebuilds on the
// head. A busy backend is left in flight — the fingerprint rebuild picks the
// head up once it idles. Returns an error message, empty on success.
func (p *teamPicker) resetMemberFailover(teamName, memberID string) string {
	if p.sessions == nil {
		return ""
	}
	st, err := p.sessions.ReadMemberFailover(teamName, memberID)
	if err != nil {
		return pickerErrMsg(err)
	}
	if st.ActiveRef == "" && len(st.Saturated) == 0 && !st.Exhausted && st.Generation == 0 {
		return "" // nothing to reset
	}
	if err := p.sessions.ClearMemberFailover(teamName, memberID); err != nil {
		return pickerErrMsg(err)
	}
	if p.backends != nil {
		if b, ok := p.backends.bound(teamName, memberID); ok {
			r := b.RuntimeStatus()
			if !(r.Running || r.PendingPrompt || r.BackgroundJobs > 0) {
				p.backends.release(teamName, memberID)
			}
		}
	}
	return ""
}
