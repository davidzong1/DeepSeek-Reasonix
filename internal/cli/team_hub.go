package cli

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/team"
)

// teamHub is the P2 routing seam: a thin host-layer abstraction that resolves
// one member id onto that member's assembled backend and drives a turn on it,
// without touching how the runtime persists tasks or how the TUI renders. The
// bound-controller path stays the compatibility layer — the hub routes by id,
// it never swaps the window's backend.
type teamHub struct {
	mu       sync.Mutex
	submit   func(control.SessionAPI, string) error
	store    *team.TeamStore
	backends *teamBackends
	teamName string
}

// setTeam re-points the hub at the team the session is now on. The name is part
// of every routing decision, so a hub frozen at construction would resolve a
// member against the team the overlay opened with, not the one it is showing.
func (h *teamHub) setTeam(teamName string) {
	if h == nil {
		return
	}
	if name := strings.TrimSpace(teamName); name != "" {
		h.mu.Lock()
		h.teamName = name
		h.mu.Unlock()
	}
}

// team is the hub's current team name.
func (h *teamHub) team() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.teamName
}

// newTeamHub returns a routing hub over the member-backend registry. The
// default turn driver is the explicit submission the runtime's task-driving
// path already uses, so a refused turn surfaces instead of silently dropping.
func newTeamHub(store *team.TeamStore, backends *teamBackends, teamName string) *teamHub {
	h := &teamHub{store: store, backends: backends, teamName: strings.TrimSpace(teamName)}
	h.submit = func(b control.SessionAPI, text string) error {
		return b.SubmitUserTurnOrError(text, text)
	}
	return h
}

// setSubmit installs the turn driver. Tests replace it to observe routing
// without a controller; a host may route into its own composer path later.
func (h *teamHub) setSubmit(fn func(control.SessionAPI, string) error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if fn != nil {
		h.submit = fn
	}
}

// Submit drives text on member's backend, assembling it on first use through
// the registry. An unknown team/member or a refused admission is an error. No
// hub-level lock is held across the backend call, so concurrent submits to
// distinct members run their turns in parallel — the property that keeps a
// leader and a reporting member going at the hub layer.
func (h *teamHub) Submit(memberID, text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("text is required")
	}
	backend, err := h.Target(h.team(), memberID)
	if err != nil {
		return err
	}
	submit := h.submitFor()
	return submit(backend, text)
}

// Target resolves member's assembled backend through the registry without
// starting a turn. Hosts use it to make a composer submit's target explicit
// by member id — the reliable routing when the TUI's ambient controller may
// no longer be that member's backend. An explicit team name keeps it usable
// as the session switches teams within one overlay.
func (h *teamHub) Target(teamName, memberID string) (control.SessionAPI, error) {
	if h == nil {
		return nil, errors.New("team hub unavailable")
	}
	if strings.TrimSpace(memberID) == "" || strings.TrimSpace(teamName) == "" {
		return nil, errors.New("member_id and team name are required")
	}
	if h.store == nil || h.backends == nil {
		return nil, errors.New("team hub: store or member backends unavailable")
	}
	b, err := h.store.Binding(teamName, memberID)
	if err != nil {
		return nil, err
	}
	return h.backends.bind(b)
}

// submitFor returns the installed turn driver. The mutex only guards the
// driver swap; the backend call itself runs outside every lock.
func (h *teamHub) submitFor() func(control.SessionAPI, string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.submit
}

// assembled resolves a member's ALREADY-running backend. A decision must never
// assemble one: the pending prompt lives in a specific controller's run
// goroutine, so building a fresh controller would spin up subprocesses and a
// session lease only to answer an id that does not exist there — and report
// success. A retired or never-started member is an error the caller can show.
func (h *teamHub) assembled(teamName, memberID string) (control.SessionAPI, error) {
	if h == nil {
		return nil, errors.New("team hub unavailable")
	}
	if strings.TrimSpace(memberID) == "" || strings.TrimSpace(teamName) == "" {
		return nil, errors.New("member_id and team name are required")
	}
	if h.backends == nil {
		return nil, errors.New("team hub: member backends unavailable")
	}
	backend, ok := h.backends.bound(teamName, memberID)
	if !ok {
		return nil, fmt.Errorf("member %q has no live session, so its prompt is gone — switch to it to see the current state", memberID)
	}
	return backend, nil
}

// Approve answers a pending approval/ask on a named member's backend without
// switching the window: the decision routes to whichever backend holds the
// blocked prompt (the controller's run goroutine unblocks there). The bound
// window's own pendingApproval/chooser are never touched — a background
// member's prompt can be resolved while another member is bound.
func (h *teamHub) Approve(teamName, memberID, id string, allow, session, persist bool) error {
	backend, err := h.assembled(teamName, memberID)
	if err != nil {
		return err
	}
	backend.Approve(id, allow, session, persist)
	return nil
}

// Answer resolves an `ask` question card on a named member's backend, mirroring
// Approve: the decision goes to the member's own controller, never the bound
// window's.
func (h *teamHub) Answer(teamName, memberID, id string, answers []event.AskAnswer) error {
	backend, err := h.assembled(teamName, memberID)
	if err != nil {
		return err
	}
	backend.AnswerQuestion(id, answers)
	return nil
}
