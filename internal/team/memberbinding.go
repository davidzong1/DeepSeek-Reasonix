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
	AgentUserRef string      // already resolved: the member's override, else the team default
	AgentType    string      // launch-type override; empty = inherit the team default
	Proxy        ProxyConfig // already resolved by ProxyFor: member override > team default > off
	SessionFile  string      // the member's session-file base name (MemberSessionFile)
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
			ref := slot.AgentUserRef
			if ref == "" {
				ref = t.DefaultAgentUserRef
			}
			agentType := slot.AgentType
			if agentType == "" {
				agentType = t.AgentType
			}
			proxy, _ := ProxyFor(t.Proxy, slot.ProxyEnabled)
			out = append(out, MemberBinding{
				Team: teamName, MemberID: slot.MemberID, Role: slot.Role,
				Leader: slot.IsLeader(), AgentUserRef: ref,
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
