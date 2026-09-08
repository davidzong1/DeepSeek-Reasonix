package team

import "fmt"

// MemberBinding is the pure-data description of one member's Agent backend:
// which member it is, how it thinks, and which pool entry configures it. It
// carries no runtime handle and no controller reference — assembly belongs to
// the frontend, because only frontends may import internal/control.
type MemberBinding struct {
	Team         string
	MemberID     string
	Role         RoleID
	Leader       bool
	AgentUserRef string      // already resolved: the member's override, else the effective pool head
	Pool         []string    // effective ordered pool: the member's custom pool, else the team's (failover walks it)
	AgentType    string      // launch-type override; empty = inherit the team default
	Proxy        ProxyConfig // already resolved by ProxyFor: member override > team default > off
	SessionFile  string      // the member's session-file base name (MemberSessionFile)
}

// SlotEffectivePool resolves a slot's ordered agent-user pool: a custom slot
// walks its own entries, an inheriting slot the team's effective pool. An empty
// custom pool is unreachable through the write path (SetMemberPool refuses it),
// so only a hand-edited document reaches it — the team pool then serves
// defensively instead of leaving the member nothing to walk, while the stored
// mode still says custom, never inherit. Read-only, like Team.EffectivePool.
func SlotEffectivePool(slot MemberSlot, t Team) []string {
	if slot.IsCustomPool() && len(slot.AgentUserPool) > 0 {
		return slot.AgentUserPool
	}
	return t.EffectivePool()
}

// SlotNominal returns the entry a slot's binding resolves to: a custom slot's
// own pool head, a pinned slot's legacy override, else the team's effective
// head. Read-only — a legacy pin folds away on an explicit write (custom pool,
// g reset), never on a read.
func SlotNominal(slot MemberSlot, t Team) string {
	if slot.IsCustomPool() {
		if len(slot.AgentUserPool) > 0 {
			return slot.AgentUserPool[0]
		}
		return t.DefaultRef() // the empty-custom fallback, mirroring SlotEffectivePool
	}
	if slot.AgentUserRef != "" {
		return slot.AgentUserRef
	}
	return t.DefaultRef()
}

// MemberSessionFile is a member's stable session-file base name. A member's
// history is one ordinary Reasonix session file — so checkpoints, rewind, fork
// and compact all work on it — and the frontend joins this name onto the
// session directory it owns. Both keys are validated, so a member can never
// name a file outside the flat session namespace.
func MemberSessionFile(teamName, memberID string) (string, error) {
	if err := validateSessionKey(teamName); err != nil {
		return "", err
	}
	if err := validateSessionKey(memberID); err != nil {
		return "", err
	}
	return fmt.Sprintf("team-%s-%s.json", teamName, memberID), nil
}

// Bindings returns one binding per member slot of the named team, in template
// order. An unbound member inherits the team's default agent-user reference, so
// the caller resolves one reference rather than reimplementing the fallback.
// An unknown team is ErrTeamNotFound; a slot whose keys cannot form a session
// file name is refused rather than silently skipped.
func (s *TeamStore) Bindings(teamName string) ([]MemberBinding, error) {
	doc, _, err := s.Load()
	if err != nil {
		return nil, err
	}
	for _, t := range doc.Teams {
		if t.Name != teamName {
			continue
		}
		out := make([]MemberBinding, 0, len(t.Template))
		for _, slot := range t.Template {
			file, err := MemberSessionFile(teamName, slot.MemberID)
			if err != nil {
				return nil, err
			}
			pool := SlotEffectivePool(slot, t)
			ref := SlotNominal(slot, t)
			agentType := slot.AgentType
			if agentType == "" {
				agentType = t.AgentType
			}
			proxy, _ := ProxyFor(t.Proxy, slot.ProxyEnabled)
			out = append(out, MemberBinding{
				Team: teamName, MemberID: slot.MemberID, Role: slot.Role,
				Leader: slot.IsLeader(), AgentUserRef: ref, Pool: pool,
				AgentType: agentType, Proxy: proxy, SessionFile: file,
			})
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrTeamNotFound, teamName)
}

// Binding returns one member's binding. A member that is not on the team's
// template is ErrMemberNotFound.
func (s *TeamStore) Binding(teamName, memberID string) (MemberBinding, error) {
	all, err := s.Bindings(teamName)
	if err != nil {
		return MemberBinding{}, err
	}
	for _, b := range all {
		if b.MemberID == memberID {
			return b, nil
		}
	}
	return MemberBinding{}, fmt.Errorf("%w: %q", ErrMemberNotFound, memberID)
}

// MemberPool resolves the member's effective agent-user pool and the nominal
// entry its binding points at, folding the read cases in one place: a custom
// slot walks its own ordered pool (head = nominal), a pinned slot keeps its
// legacy override as nominal while still walking the team's pool, and an
// unbound slot inherits the team pool head. Read-only.
func (s *TeamStore) MemberPool(teamName, memberID string) (pool []string, nominal string, err error) {
	doc, _, err := s.Load()
	if err != nil {
		return nil, "", err
	}
	for i := range doc.Teams {
		if doc.Teams[i].Name != teamName {
			continue
		}
		for j := range doc.Teams[i].Template {
			if doc.Teams[i].Template[j].MemberID == memberID {
				slot := doc.Teams[i].Template[j]
				return SlotEffectivePool(slot, doc.Teams[i]), SlotNominal(slot, doc.Teams[i]), nil
			}
		}
		return nil, "", fmt.Errorf("%w: %q", ErrMemberNotFound, memberID)
	}
	return nil, "", fmt.Errorf("%w: %q", ErrTeamNotFound, teamName)
}
