package team

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// ErrInvalidAgent reports an agent launch type the store refuses to persist:
// the claude/codex whitelist is caller-enforced (§7.5), but control
// characters and surrounding whitespace are unsafe in any launch type.
var ErrInvalidAgent = errors.New("team: invalid agent launch type")

// SetTeamAgentType sets the team-default launch type; empty clears back to
// legacy behavior. A type with control characters or surrounding whitespace
// is refused.
func (s *TeamStore) SetTeamAgentType(teamName string, t string) error {
	if err := validateAgentType(t); err != nil {
		return err
	}
	return s.update(func(doc *TeamDoc) error {
		i := teamIndex(doc, teamName)
		if i < 0 {
			return ErrTeamNotFound
		}
		doc.Teams[i].AgentType = t
		return nil
	})
}

// SetMemberAgentType sets a member's launch-type override; empty clears back
// to the team default. Validation and refusals mirror SetTeamAgentType.
func (s *TeamStore) SetMemberAgentType(teamName, memberID, t string) error {
	if err := validateAgentType(t); err != nil {
		return err
	}
	return s.update(func(doc *TeamDoc) error {
		i := teamIndex(doc, teamName)
		if i < 0 {
			return ErrTeamNotFound
		}
		slot, err := memberSlot(doc, i, memberID)
		if err != nil {
			return err
		}
		slot.AgentType = t
		return nil
	})
}

// SetMemberLeader sets the member's standalone leader property; false clears
// it back to a regular member, also dropping a legacy "leader" role value so
// leader status really ends (IsLeader reads both encodings). New writes never
// use the role encoding; the flag never rewrites a business role.
func (s *TeamStore) SetMemberLeader(teamName, memberID string, leader bool) error {
	return s.update(func(doc *TeamDoc) error {
		i := teamIndex(doc, teamName)
		if i < 0 {
			return ErrTeamNotFound
		}
		slot, err := memberSlot(doc, i, memberID)
		if err != nil {
			return err
		}
		slot.Leader = leader
		if !leader && slot.Role == RoleLeader {
			slot.Role = ""
		}
		return nil
	})
}

// BindAgentUser points a member's slot at a pool entry, verifying the
// reference exists at write time (§5) — an empty ref is refused because
// clearing back to the team default is UnbindAgentUser's job.
func (s *TeamStore) BindAgentUser(teamName, memberID, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ErrAgentUserNotFound
	}
	if _, ok, err := s.agentUsers.GetAgentUser(ref); err != nil {
		return err
	} else if !ok {
		return ErrAgentUserNotFound
	}
	return s.update(func(doc *TeamDoc) error {
		i := teamIndex(doc, teamName)
		if i < 0 {
			return ErrTeamNotFound
		}
		slot, err := memberSlot(doc, i, memberID)
		if err != nil {
			return err
		}
		slot.AgentUserRef = ref
		return nil
	})
}

// UnbindAgentUser clears a member's pool reference back to the team default.
func (s *TeamStore) UnbindAgentUser(teamName, memberID string) error {
	return s.update(func(doc *TeamDoc) error {
		i := teamIndex(doc, teamName)
		if i < 0 {
			return ErrTeamNotFound
		}
		slot, err := memberSlot(doc, i, memberID)
		if err != nil {
			return err
		}
		slot.AgentUserRef = ""
		return nil
	})
}

// memberSlot returns the named team's template slot for editing; the caller
// holds the working doc, so changes publish through the CAS loop.
func memberSlot(doc *TeamDoc, teamIdx int, memberID string) (*MemberSlot, error) {
	for j := range doc.Teams[teamIdx].Template {
		if doc.Teams[teamIdx].Template[j].MemberID == memberID {
			return &doc.Teams[teamIdx].Template[j], nil
		}
	}
	return nil, ErrMemberNotFound
}

// AgentTypeClaude and AgentTypeCodex are the two launch types that pass without
// review. Anything else must be a plain command word (§7.5).
const (
	AgentTypeClaude = "claude"
	AgentTypeCodex  = "codex"
)

// agentTypeMaxLen bounds a custom launch type so a pasted command line cannot
// become one.
const agentTypeMaxLen = 32

// validateAgentType enforces the §7.5 whitelist: empty inherits, claude and
// codex pass, and anything else must be one plain command word — no whitespace,
// path separators, or shell metacharacters, so a launch type can never carry an
// argument list or a redirection into whatever eventually spawns it.
func validateAgentType(t string) error {
	switch t {
	case "", AgentTypeClaude, AgentTypeCodex:
		return nil
	}
	if len(t) > agentTypeMaxLen || !utf8.ValidString(t) {
		return ErrInvalidAgent
	}
	for _, r := range t {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return ErrInvalidAgent
		}
	}
	return nil
}
