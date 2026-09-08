package team

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ErrInvalidAgent reports an agent launch type the store refuses to persist:
// the claude/codex whitelist is caller-enforced (§7.5), but control
// characters and surrounding whitespace are unsafe in any launch type.
var ErrInvalidAgent = errors.New("team: invalid agent launch type")

// Member-pool write errors. The empty refusal keeps a custom mode from silently
// degrading into inherit; the pin refusal keeps the legacy one-ref pin from
// coexisting with a custom pool on the same slot.
var (
	ErrInvalidMemberPool  = errors.New("team: invalid member pool mode")
	ErrMemberPoolEmpty    = errors.New("team: a custom member pool must name at least one agent user")
	ErrMemberPoolNonEmpty = errors.New("team: an inheriting member cannot carry a custom pool")
	ErrMemberPoolPin      = errors.New("team: unbind the member's pinned agent user before assigning a custom pool")
	ErrMemberPoolBind     = errors.New("team: reset the member's custom pool before binding an agent user")
)

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

// SetTeamDefaultAgentUser points the team at a pool entry used by members
// without an explicit override. An empty ref clears the default; non-empty
// refs are validated against the agent-user pool before publishing.
func (s *TeamStore) SetTeamDefaultAgentUser(teamName, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return s.update(func(doc *TeamDoc) error {
			i := teamIndex(doc, teamName)
			if i < 0 {
				return ErrTeamNotFound
			}
			doc.Teams[i].DefaultAgentUserRef = ""
			return nil
		})
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
		doc.Teams[i].DefaultAgentUserRef = ref
		return nil
	})
}

// SetTeamAgentUserPool writes the team's ordered agent-user pool: the entries
// members inherit from, head first. Every reference is validated against the
// registry, the order is preserved, and duplicates collapse. Publishing a pool
// makes it the team default authority, so the legacy DefaultAgentUserRef is
// cleared — the fold happens only on this explicit write, never on a read. An
// empty pool clears the default entirely, gating sessions until a pool is set.
func (s *TeamStore) SetTeamAgentUserPool(teamName string, pool []string) error {
	ids := make([]string, 0, len(pool))
	seen := make(map[string]bool, len(pool))
	for _, ref := range pool {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		ids = append(ids, ref)
	}
	for _, id := range ids {
		if _, ok, err := s.agentUsers.GetAgentUser(id); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("%w: %q", ErrAgentUserNotFound, id)
		}
	}
	return s.update(func(doc *TeamDoc) error {
		i := teamIndex(doc, teamName)
		if i < 0 {
			return ErrTeamNotFound
		}
		doc.Teams[i].AgentUserPool = ids
		doc.Teams[i].DefaultAgentUserRef = "" // the pool head is now the default
		return nil
	})
}

// EffectiveTeamPool returns the team's ordered pool entries as configured — the
// explicit pool, or the legacy default as a one-entry pool. It seeds the roster
// pool editor and feeds the session gate. Read-only: a call never rewrites the
// legacy field.
func (s *TeamStore) EffectiveTeamPool(teamName string) ([]string, error) {
	doc, _, err := s.Load()
	if err != nil {
		return nil, err
	}
	for _, t := range doc.Teams {
		if t.Name == teamName {
			return t.EffectivePool(), nil
		}
	}
	return nil, fmt.Errorf("%w: %q", ErrTeamNotFound, teamName)
}

// SetMemberPool writes a member's pool mode and ordered custom entries. A
// custom write validates every reference against the registry — a missing one
// refuses the whole write by name — collapses duplicates keeping first order,
// and refuses an empty pool: custom is an explicit choice, never a silent
// fallback to inherit. Inherit is the default mode: it demands an empty pool
// argument, leaves any existing custom entries on the slot untouched (unread
// until the mode flips back, so a round trip loses nothing), and canonicalizes
// the stored mode to empty. A legacy AgentUserRef pin refuses a custom write —
// the roster g reset folds the pin first — so the two never coexist on one
// slot. The write never touches runtime failover state, mirroring the team
// pool editor.
func (s *TeamStore) SetMemberPool(teamName, memberID, mode string, pool []string) error {
	mode = strings.TrimSpace(mode)
	if mode != "" && mode != MemberPoolCustom {
		return ErrInvalidMemberPool
	}
	if mode != MemberPoolCustom {
		ids := cleanPoolRefs(pool)
		if len(ids) > 0 {
			return ErrMemberPoolNonEmpty
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
			slot.PoolMode = ""
			return nil
		})
	}
	ids := cleanPoolRefs(pool)
	if len(ids) == 0 {
		return ErrMemberPoolEmpty
	}
	for _, id := range ids {
		if _, ok, err := s.agentUsers.GetAgentUser(id); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("%w: %q", ErrAgentUserNotFound, id)
		}
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
		if slot.AgentUserRef != "" {
			return ErrMemberPoolPin
		}
		slot.PoolMode = MemberPoolCustom
		slot.AgentUserPool = ids
		return nil
	})
}

// cleanPoolRefs trims the refs and collapses duplicates keeping first order,
// dropping blanks — the same normalization SetTeamAgentUserPool applies.
func cleanPoolRefs(pool []string) []string {
	ids := make([]string, 0, len(pool))
	seen := make(map[string]bool, len(pool))
	for _, ref := range pool {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		ids = append(ids, ref)
	}
	return ids
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
// clearing back to the team default is UnbindAgentUser's job. A custom pool
// already binds its own head, so the pin write is refused (ErrMemberPoolBind),
// the mirror of SetMemberPool's pin refusal: the two never coexist on a slot.
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
		if slot.IsCustomPool() {
			return ErrMemberPoolBind
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
