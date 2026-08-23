package cli

import (
	"slices"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/team"
)

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
	build func(team.MemberBinding) (control.SessionAPI, error)
	max   int
	live  map[string]control.SessionAPI
	order []string // least recently bound first; the eviction order
}

// newTeamBackends returns an empty registry. max <= 0 takes the default cap.
func newTeamBackends(build func(team.MemberBinding) (control.SessionAPI, error), max int) *teamBackends {
	if max <= 0 {
		max = defaultMaxTeamBackends
	}
	return &teamBackends{build: build, max: max, live: map[string]control.SessionAPI{}}
}

// bind returns the member's backend, assembling it on first use. The returned
// backend becomes the most recently bound, so it is never the eviction victim;
// a failed assembly leaves the registry untouched so the caller can retry.
func (r *teamBackends) bind(b team.MemberBinding) (control.SessionAPI, error) {
	key := backendKey(b.Team, b.MemberID)
	if live, ok := r.live[key]; ok {
		r.touch(key)
		return live, nil
	}
	if r.build == nil {
		return nil, team.ErrMemberNotFound
	}
	backend, err := r.build(b)
	if err != nil {
		return nil, err
	}
	r.live[key] = backend
	r.touch(key)
	r.evictOverCap(key)
	return backend, nil
}

// bound reports the member's assembled backend without building one.
func (r *teamBackends) bound(teamName, memberID string) (control.SessionAPI, bool) {
	live, ok := r.live[backendKey(teamName, memberID)]
	return live, ok
}

// touch moves key to the most-recently-bound end of the eviction order.
func (r *teamBackends) touch(key string) {
	if i := slices.Index(r.order, key); i >= 0 {
		r.order = slices.Delete(r.order, i, i+1)
	}
	r.order = append(r.order, key)
}

// evictOverCap retires least-recently-bound backends until the live set fits
// the cap, never touching keep — the member the caller just bound.
func (r *teamBackends) evictOverCap(keep string) {
	for len(r.live) > r.max && len(r.order) > 0 {
		victim := r.order[0]
		if victim == keep {
			if len(r.order) == 1 {
				return
			}
			victim = r.order[1]
		}
		r.retire(victim)
	}
}

// retire closes one backend and forgets it, releasing its session lease and
// plugin subprocesses. The member's history stays on disk.
func (r *teamBackends) retire(key string) {
	if live, ok := r.live[key]; ok {
		live.Close()
		delete(r.live, key)
	}
	if i := slices.Index(r.order, key); i >= 0 {
		r.order = slices.Delete(r.order, i, i+1)
	}
}

// release retires one member's backend if it is assembled; unknown members are
// a no-op. Used by the destructive paths (member/team delete, leader step-down)
// so nothing keeps writing a context that is about to be cleared.
func (r *teamBackends) release(teamName, memberID string) {
	r.retire(backendKey(teamName, memberID))
}

// releaseTeam retires every assembled backend of one team.
func (r *teamBackends) releaseTeam(teamName string) {
	prefix := teamName + "\x00"
	for _, key := range slices.Clone(r.order) {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			r.retire(key)
		}
	}
}

// closeAll retires every backend; the registry is reusable afterwards.
func (r *teamBackends) closeAll() {
	for _, key := range slices.Clone(r.order) {
		r.retire(key)
	}
}
