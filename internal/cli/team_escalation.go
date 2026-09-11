package cli

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/sandbox"
	"reasonix/internal/team"
)

// escalationTimeout bounds how long a member waits on the leader before the
// request stops being the leader's to answer. The member stays blocked either
// way — its prompt was always the operator's fallback — but the leader must not
// be handed a request nobody is waiting for any more.
const escalationTimeout = 2 * time.Minute

// pendingEscalation is one member's out-of-scope write offered to the leader.
type pendingEscalation struct {
	RequestID, Team, Member, ApprovalID string
	Tool, Subject                       string
	Dirs, DisplayDirs                   []string
	Justification                       string
	BroadHome, OrdinaryNeeded           bool
	Created                             time.Time
}

// writeAccessEscalations routes a member's out-of-scope write to the leader
// agent: it queues the request, wakes the leader with it, and settles the blocked
// member through the hub. It lives on the chat window rather than the team
// overlay because member backends keep running after the overlay closes — an
// overlay-scoped registry would drop every request the moment the user left it.
type writeAccessEscalations struct {
	mu      sync.Mutex
	store   *team.TeamStore
	hub     *teamHub
	pending map[string]pendingEscalation
	waking  map[string]bool
}

func newWriteAccessEscalations(store *team.TeamStore) *writeAccessEscalations {
	return &writeAccessEscalations{
		store:   store,
		pending: map[string]pendingEscalation{}, waking: map[string]bool{},
	}
}

// setHub late-binds the routing seam, the same way tasks and the inbox are
// bound once the overlay has built them.
func (s *writeAccessEscalations) setHub(h *teamHub) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.hub = h
	s.mu.Unlock()
}

// requestID keys one member's prompt. Approval ids are per-controller counters,
// so two members routinely hold the same id; the member segment is what makes
// the leader's handle unambiguous.
func escalationRequestID(member, approvalID string) string {
	return member + ":" + approvalID
}

// memberEscalator binds one member's identity to the shared registry, so the
// controller seam — which knows only its own prompt — routes to the right member.
type memberEscalator struct {
	svc    *writeAccessEscalations
	team   string
	member string
}

func (m memberEscalator) BeginWriteAccessEscalation(esc control.WriteAccessEscalation) func() {
	return m.svc.begin(m.team, m.member, esc)
}

// memberWriteAccessEscalator returns the decider for one member backend, or nil
// when there is nothing to escalate to. A leader never escalates to itself: its
// own write scope is the whole filesystem, so it raises no such card at all.
func memberWriteAccessEscalator(svc *writeAccessEscalations, teamName, memberID string, leader bool) control.WriteAccessEscalator {
	if svc == nil || leader || strings.TrimSpace(teamName) == "" || strings.TrimSpace(memberID) == "" {
		return nil
	}
	return memberEscalator{svc: svc, team: teamName, member: memberID}
}

// begin queues one request and starts its wake. It runs on the blocked member's
// turn goroutine with the prompt lock held, so it must not block: the wake and
// the expiry both run on their own goroutines. The returned release runs exactly
// once when the prompt settles — answered by anyone, timed out, or torn down —
// and is what keeps a human answer from leaving a stale entry behind for the
// leader to "decide".
func (s *writeAccessEscalations) begin(teamName, memberID string, esc control.WriteAccessEscalation) func() {
	if s == nil || s.store == nil || strings.TrimSpace(esc.ApprovalID) == "" {
		return nil
	}
	leader, ok := s.leaderOf(teamName)
	if !ok {
		return nil // nobody to ask: the operator's card is the only decider
	}
	entry := pendingEscalation{
		RequestID: escalationRequestID(memberID, esc.ApprovalID),
		Team:      teamName, Member: memberID, ApprovalID: esc.ApprovalID,
		Tool: esc.Tool, Subject: esc.Subject,
		Dirs:          append([]string(nil), esc.Directories...),
		DisplayDirs:   append([]string(nil), esc.DisplayDirectories...),
		Justification: esc.Justification,
		BroadHome:     esc.BroadHomeAccess, OrdinaryNeeded: esc.OrdinaryPermissionNeeded,
		Created: time.Now(),
	}
	s.mu.Lock()
	s.pending[entry.RequestID] = entry
	s.mu.Unlock()

	go s.wake(teamName, leader)
	go s.expire(entry.RequestID)

	return func() {
		s.mu.Lock()
		delete(s.pending, entry.RequestID)
		s.mu.Unlock()
	}
}

// expire retires a request the leader never answered, so a leader that stopped
// running cannot be handed a decision nobody is waiting on.
func (s *writeAccessEscalations) expire(requestID string) {
	timer := time.NewTimer(escalationTimeout)
	defer timer.Stop()
	<-timer.C
	s.mu.Lock()
	delete(s.pending, requestID)
	s.mu.Unlock()
}

// list reports the team's live requests, oldest first — the order they blocked.
func (s *writeAccessEscalations) list(teamName string) []pendingEscalation {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	out := make([]pendingEscalation, 0, len(s.pending))
	for _, entry := range s.pending {
		if entry.Team == teamName {
			out = append(out, entry)
		}
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// resolve settles one request with the leader's decision. It is the only writer
// of a leader_agent ledger row, and it writes one only when the answer actually
// landed: the request must still be pending here, which the member's own release
// clears the moment any other route settles the prompt.
func (s *writeAccessEscalations) resolve(teamName, requestID string, allow bool, scope sandbox.ApprovalScope) (string, error) {
	if scope != sandbox.ApprovalScopeOnce && scope != sandbox.ApprovalScopeSession {
		return "", fmt.Errorf("scope %q is not available to a leader; use once or session (project writes the workspace config and needs the operator)", scope)
	}
	s.mu.Lock()
	entry, ok := s.pending[requestID]
	if ok && entry.Team == teamName {
		delete(s.pending, requestID)
	} else {
		ok = false
	}
	hub := s.hub
	s.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("no pending request %q for this team — it was already answered, expired, or belongs to another team", requestID)
	}
	if hub == nil {
		return "", errors.New("team hub unavailable")
	}
	if err := hub.ResolveApproval(entry.Team, entry.Member, entry.ApprovalID, allow, scope); err != nil {
		return "", err
	}
	if s.store != nil {
		_ = s.store.AppendAuthz(entry.Team, team.AuthzEntry{
			TS: time.Now().Format(time.RFC3339), Member: entry.Member,
			Source: team.AuthzSourceLeaderAgent, Allow: allow,
			ID: entry.ApprovalID, Tool: entry.Tool, Subject: entry.Subject,
			RequestID: requestID, Kind: team.AuthzKindWriteAccess,
			Dirs: entry.Dirs, Scope: string(scope),
		})
	}
	verdict := "denied"
	if allow {
		verdict = "allowed"
	}
	return fmt.Sprintf("%s %s for member %q (%s, scope=%s)", verdict, requestID, entry.Member, entry.Tool, scope), nil
}

// wake brings the leader's turn up so it can answer the queue. A leader already
// mid-turn refuses a new turn, so the request rides its next step as guidance
// instead. One wake is in flight per team; a request that arrives while one is
// running is picked up by the same turn, whose text lists the whole queue.
func (s *writeAccessEscalations) wake(teamName, leaderID string) {
	s.mu.Lock()
	if s.waking[teamName] {
		s.mu.Unlock()
		return
	}
	s.waking[teamName] = true
	hub := s.hub
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.waking, teamName)
		s.mu.Unlock()
	}()
	if hub == nil {
		return
	}
	text := s.wakeText(teamName)
	if text == "" {
		return
	}
	if err := hub.Submit(leaderID, text); err == nil {
		return
	}
	// A refused submission means the leader is mid-turn (or draining). Guidance
	// lands at its next step, which is the same decision point.
	if backend, err := hub.Target(teamName, leaderID); err == nil {
		backend.Steer(text)
	}
}

// wakeText is the leader-facing turn body. It names every request rather than
// just the new one, because a coalesced wake must not hide a second member.
func (s *writeAccessEscalations) wakeText(teamName string) string {
	pending := s.list(teamName)
	if len(pending) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[team authorization request — your decision is required]\n")
	fmt.Fprintf(&b, "%d request(s) are blocking team members. A blocked member cannot continue until you answer.\n", len(pending))
	for _, e := range pending {
		fmt.Fprintf(&b, "- request_id %s | member %s | tool %s | dirs %s | ordinary_permission_needed: %t",
			e.RequestID, e.Member, e.Tool, strings.Join(e.Dirs, ", "), e.OrdinaryNeeded)
		if e.BroadHome {
			b.WriteString(" | WARNING: broad home access")
		}
		if e.Justification != "" {
			b.WriteString(" | why: " + e.Justification)
		}
		b.WriteString("\n")
	}
	b.WriteString("Answer with leader_resolve_member_approval {request_id, allow, scope}. ")
	b.WriteString("scope once grants this single call; session also covers later writes to the same directories for that member. ")
	b.WriteString("Persisting to the project config is not available to you; ask the operator if it is needed.")
	return b.String()
}

// leaderOf resolves the team's leader member. A team with no leader slot has
// nobody to escalate to, and the request stays on the operator's card.
func (s *writeAccessEscalations) leaderOf(teamName string) (string, bool) {
	if s == nil || s.store == nil {
		return "", false
	}
	bindings, err := s.store.Bindings(teamName)
	if err != nil {
		return "", false
	}
	for _, b := range bindings {
		if b.Leader {
			return b.MemberID, true
		}
	}
	return "", false
}

// clear drops every queued request. Backends are torn down separately; each
// blocked waiter's release then finds its entry already gone.
func (s *writeAccessEscalations) clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	clear(s.pending)
	s.mu.Unlock()
}
