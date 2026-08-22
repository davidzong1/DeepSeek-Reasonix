package team

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// ErrInvalidRole reports a member role the store refuses. Empty is legal — a
// role-less member renders an unconfigured hint at prompt assembly — but a
// non-empty role over the length ceiling or carrying control characters is
// refused: control bytes could corrupt rendering and inject a second line
// into the member's system prompt (§2.2).
var ErrInvalidRole = errors.New("team: role must not contain control characters and must be at most 128 bytes")

// memberRoleLimit bounds a free-text role against accidental bloat. The value
// is generous: roles are open-ended, only bounded.
const memberRoleLimit = 128

// ValidateRole checks a member's free-text role. Empty is valid. A non-empty
// role must be valid UTF-8, carry no control characters (newline would break
// the prompt line), and stay under the length ceiling. The character set is
// otherwise unrestricted — roles are free text, not a fixed vocabulary.
func ValidateRole(role string) error {
	if role == "" {
		return nil
	}
	if !utf8.ValidString(role) || len(role) > memberRoleLimit {
		return ErrInvalidRole
	}
	for _, r := range role {
		if r < 0x20 || r == 0x7f {
			return ErrInvalidRole
		}
	}
	return nil
}

// SetMemberRole sets a member's free-text role; empty clears it. Validation
// mirrors AddMember — the same ValidateRole gate — so a refused value never
// lands on disk.
func (s *TeamStore) SetMemberRole(teamName, memberID string, role RoleID) error {
	if err := ValidateRole(string(role)); err != nil {
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
		slot.Role = role
		return nil
	})
}

// SystemPromptForRole assembles the member-identity fragment of a team
// session's system prompt (§2.2): team name, member id, and the free-text
// role. Callers inject it into the turn tail, never the stable prefix — the
// prefix must stay byte-stable for cache-first. An empty role renders the
// unconfigured hint instead.
func SystemPromptForRole(teamName, memberID string, role RoleID) string {
	var b strings.Builder
	b.WriteString("你是团队 " + teamName + " 的成员 " + memberID + "。\n")
	if role == "" {
		b.WriteString("你尚未配置团队角色，请根据团队交付需求自行规划职责。")
		return b.String()
	}
	b.WriteString("你的团队角色是：" + string(role) + "。\n")
	b.WriteString("请以该角色和专精方向参与任务。")
	return b.String()
}
