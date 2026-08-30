package cli

import (
	"errors"
	"slices"
	"sync"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/team"
)

// errBackendStopped reports a bind whose assembly was racing a teardown: the
// member's backend was released, evicted, or closed while its build was still
// running, so the finished controller must not be registered. The caller sees
// the same refusal a released member already reports, and the backend is closed.
var errBackendStopped = errors.New("cli: team member backend stopped while assembling")

// memberEvent tags one agent event with the member whose backend emitted it.
// Every member backend writes into one shared channel, so the TUI keeps its
// single waitForAgentEvent goroutine and still knows whose turn produced the
// event: the bound member's events render into the transcript, another member's
// terminal events only count as unread.
type memberEvent struct {
	member string
	ev     event.Event
}

// memberSink adapts one member's backend onto the shared tagged channel. The
// send blocks like the main path's eventSink does — a generously buffered
// channel is what keeps a streaming burst from backpressuring the agent
// goroutine, not a drop policy that would lose turn-final events.
func memberSink(member string, ch chan<- memberEvent) event.Sink {
	return event.FuncSink(func(e event.Event) { ch <- memberEvent{member: member, ev: e} })
}

// backendKey identifies one member instance. Team and member id are validated
// upstream (team.MemberSessionFile), so the pair is unambiguous.
func backendKey(teamName, memberID string) string { return teamName + "\x00" + memberID }

// defaultMaxTeamBackends bounds how many member backends stay assembled at
// once. Each one owns a controller with its own plugin/MCP subprocesses and a
// session-file lease, so the set is capped and the least recently bound member
// is retired; its history is on disk, so binding it again rebuilds it.
const defaultMaxTeamBackends = 4

// teamBackends holds one assembled Agent backend per member: the registry the
// TUI binds against so switching members swaps the backend behind one window
// instead of rendering a second, smaller chat. It owns only the lifetime of
// those backends — assembly itself is the injected build function, which keeps
// provider resolution and boot wiring out of this file.
type teamBackends struct {
	mu          sync.Mutex
	build       func(team.MemberBinding) (control.SessionAPI, error)
	fingerprint func(team.MemberBinding) (string, error)
	max         int
	live        map[string]control.SessionAPI
	fps         map[string]string // key → fingerprint at assembly; only when set
	order       []string          // least recently bound first; the eviction order
	// building tracks in-flight assembly per key: a second bind for a member
	// whose backend is already being assembled waits on that same call instead
	// of assembling a duplicate. The call is removed once its build settles.
	building map[string]*buildCall
}

// buildCall is one in-flight assembly: the result the leading bind produces,
// and the channel that releases waiters when the build settles. abandoned is
// set when the key is retired (release/evict/close) while the build is still
// running — the finished backend must then be closed, never registered.
type buildCall struct {
	done      chan struct{}
	backend   control.SessionAPI
	err       error
	abandoned bool
}

// newTeamBackends returns an empty registry. max <= 0 takes the default cap.
func newTeamBackends(build func(team.MemberBinding) (control.SessionAPI, error), max int) *teamBackends {
	if max <= 0 {
		max = defaultMaxTeamBackends
	}
	return &teamBackends{
		build: build, max: max,
		live:     map[string]control.SessionAPI{},
		fps:      map[string]string{},
		building: map[string]*buildCall{},
	}
}

// setFingerprint installs the binding-fingerprint function. Once installed,
// bind refuses to reuse a member's assembled backend when the fingerprint —
// agent-user ref, provider, model, base url or API key — changed since
// assembly, and re-assembles it instead. Without it the registry keeps the
// historical behavior: same member always reuses its backend.
func (r *teamBackends) setFingerprint(f func(team.MemberBinding) (string, error)) {
	r.fingerprint = f
}

// bind returns the member's backend, assembling it on first use. A cached
// backend is reused only when its fingerprint is unchanged (or no fingerprint
// is installed); a changed credential/provider identity assembles a fresh one
// before retiring the stale, so a rebind never keeps serving the old provider
// and never kills a running turn to do it. A backend whose fingerprint cannot
// be evaluated (or that is busy) also keeps serving until an idle, resolvable
// bind rebuilds it. The returned backend becomes the most recently bound, so
// it is never the eviction victim; a failed assembly leaves the previous
// backend serving so the caller can retry.
//
// Assembly runs outside the registry lock behind a per-key in-flight guard:
// two binds for the same member share one build instead of double-assembling,
// while binds for distinct members build in parallel — a slow member boot never
// serializes the fleet behind it (§P1 concurrency review). The registry maps
// are still guarded for concurrent bind/evict/release.
func (r *teamBackends) bind(b team.MemberBinding) (control.SessionAPI, error) {
	key := backendKey(b.Team, b.MemberID)
	r.mu.Lock()
	fp, fpErr := r.currentFingerprint(b)
	live, ok := r.live[key]
	if ok && fpErr == nil && r.fps[key] == fp {
		r.touch(key)
		r.mu.Unlock()
		return live, nil
	}
	// A changed identity must not be reused, but retiring it first kills a
	// running turn (§4.5) or strands a pending prompt on a failed rebuild —
	// a busy backend keeps serving until an idle bind rebuilds it.
	if ok {
		if st := live.RuntimeStatus(); st.Running || st.PendingPrompt || st.BackgroundJobs > 0 || fpErr != nil {
			r.touch(key)
			r.mu.Unlock()
			return live, nil
		}
	}
	if r.build == nil {
		r.mu.Unlock()
		return nil, team.ErrMemberNotFound
	}
	// Join an in-flight assembly for the same member instead of starting a
	// duplicate build. The wait happens outside the lock, so it never holds
	// other members' binds.
	if call, inflight := r.building[key]; inflight {
		r.mu.Unlock()
		<-call.done
		if call.abandoned {
			return nil, errBackendStopped
		}
		return call.backend, call.err
	}
	call := &buildCall{done: make(chan struct{})}
	r.building[key] = call
	r.mu.Unlock()

	// Assemble outside the lock: boot.Build is the slow step, and only this
	// member's slot is taken, so the rest of the fleet binds independently.
	backend, err := r.build(b)

	r.mu.Lock()
	// A release/eviction/close raced the build: the member was retired while
	// its backend was still assembling, so the finished controller must not be
	// registered — close it and fail the bind like any teardown does.
	if call.abandoned {
		if backend != nil {
			backend.Close()
		}
		delete(r.building, key)
		call.backend, call.err = nil, errBackendStopped
		close(call.done)
		r.mu.Unlock()
		return nil, errBackendStopped
	}
	if err != nil {
		delete(r.building, key)
		if ok {
			r.touch(key)
		}
		call.backend, call.err = live, err
		close(call.done)
		r.mu.Unlock()
		if ok {
			return live, err
		}
		return nil, err
	}
	// Publish while the in-flight guard is still held, so a concurrent bind for
	// the same member joins this build instead of starting a duplicate.
	if ok {
		r.drop(key)
	}
	r.live[key] = backend
	if fpErr == nil {
		r.fps[key] = fp
	}
	r.touch(key)
	r.evictOverCap(key)
	delete(r.building, key)
	call.backend, call.err = backend, nil
	close(call.done)
	r.mu.Unlock()
	return backend, nil
}

// currentFingerprint evaluates the installed fingerprint for a binding. With
// no fingerprint installed it returns an empty match so every cached backend
// is reusable (the legacy contract).
func (r *teamBackends) currentFingerprint(b team.MemberBinding) (string, error) {
	if r.fingerprint == nil {
		return "", nil
	}
	return r.fingerprint(b)
}

// bound reports the member's assembled backend without building one.
func (r *teamBackends) bound(teamName, memberID string) (control.SessionAPI, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	live, ok := r.live[backendKey(teamName, memberID)]
	return live, ok
}

// touch moves key to the most-recently-bound end of the eviction order.
// Caller holds the registry lock.
func (r *teamBackends) touch(key string) {
	if i := slices.Index(r.order, key); i >= 0 {
		r.order = slices.Delete(r.order, i, i+1)
	}
	r.order = append(r.order, key)
}

// evictOverCap retires least-recently-bound backends until the live set fits
// the cap, never touching keep. A running or pending-prompt backend is never
// an eviction victim — closing it would kill a live turn or strand a prompt
// the window cannot answer (§P1). Caller holds the registry lock; the sweep
// is bounded: each busy member is rotated to the MRU end once, and an
// all-busy over-cap set returns instead of spinning.
func (r *teamBackends) evictOverCap(keep string) {
	total := len(r.live)
	scanned := 0
	for len(r.live) > r.max && len(r.order) > 0 {
		victim := r.order[0]
		if victim == keep {
			if len(r.order) == 1 {
				return
			}
			victim = r.order[1]
		}
		if st := r.live[victim].RuntimeStatus(); st.Running || st.PendingPrompt || st.BackgroundJobs > 0 {
			r.touch(victim)
			scanned++
			if scanned >= total {
				return // every candidate busy: keep the over-cap set, never kill a turn
			}
			continue
		}
		scanned = 0
		r.retire(victim)
	}
}

// drop closes one backend and forgets it, releasing its session lease and
// plugin subprocesses. The member's history stays on disk. Unlike retire it
// leaves any in-flight assembly alone — the bind path drops the stale backend
// while its replacement build is still this member's own live guard. Caller
// holds the registry lock.
func (r *teamBackends) drop(key string) {
	if live, ok := r.live[key]; ok {
		live.Close()
		delete(r.live, key)
	}
	delete(r.fps, key)
	if i := slices.Index(r.order, key); i >= 0 {
		r.order = slices.Delete(r.order, i, i+1)
	}
}

// abandon marks any in-flight assembly for key as abandoned: the member was
// retired (release/evict/close) while its backend was still building, so the
// finished controller must be closed, never registered. Caller holds the lock.
func (r *teamBackends) abandon(key string) {
	if call, ok := r.building[key]; ok {
		call.abandoned = true
	}
}

// retire drops one member's backend and abandons its in-flight assembly. The
// member's history stays on disk. Caller holds the registry lock.
func (r *teamBackends) retire(key string) {
	r.abandon(key)
	r.drop(key)
}

// release retires one member's backend if it is assembled; unknown members are
// a no-op. Used by the destructive paths (member/team delete, leader step-down)
// so nothing keeps writing a context that is about to be cleared.
func (r *teamBackends) release(teamName, memberID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retire(backendKey(teamName, memberID))
}

// releaseTeam retires every assembled backend of one team and abandons any
// in-flight assembly of its members: a member released mid-build must not
// re-register its finished controller after the team was torn down.
func (r *teamBackends) releaseTeam(teamName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prefix := teamName + "\x00"
	for _, key := range slices.Clone(r.order) {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			r.retire(key)
		}
	}
	for key := range r.building {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			r.abandon(key)
		}
	}
}

// liveTeamCount counts the team's assembled backends; the leader step-down
// uses it to prove the stop completed before clearing anything.
func (r *teamBackends) liveTeamCount(teamName string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	prefix := teamName + "\x00"
	n := 0
	for key := range r.live {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

// closeAll retires every backend and abandons every in-flight assembly; the
// registry is reusable afterwards.
func (r *teamBackends) closeAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, key := range slices.Clone(r.order) {
		r.retire(key)
	}
	for key := range r.building {
		r.abandon(key)
	}
}
