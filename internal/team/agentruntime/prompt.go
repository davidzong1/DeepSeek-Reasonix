package agentruntime

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxRoleLen caps the injected free-text role so a role can never blow up
// the system prompt; longer roles are truncated to the cap.
const MaxRoleLen = 200

// ComposeSystemPrompt assembles a member instance's system prompt (route
// §2.2): the base prompt plus an identity block naming the team, the member,
// and the free-text role. The role is treated strictly as data — it is
// placed inside the identity block verbatim and never interpreted as
// instructions — so role text that happens to look like a directive cannot
// restructure the prompt. An empty role renders an explicit unconfigured
// note instead of omitting the identity block.
func ComposeSystemPrompt(base string, key InstanceKey, role string) string {
	if role == "" {
		return fmt.Sprintf("%s\n你是团队 %s 的成员 %s。\n你的团队角色尚未配置。\n请以团队成员身份参与任务。",
			strings.TrimRight(base, "\n"), key.Team, key.MemberID)
	}
	role = truncateRole(role)
	return fmt.Sprintf("%s\n你是团队 %s 的成员 %s。\n你的团队角色是：%s。\n请以该角色和专精方向参与任务。",
		strings.TrimRight(base, "\n"), key.Team, key.MemberID, role)
}

// truncateRole clips the role to MaxRoleLen runes so a hostile or accidental
// long role cannot flood the prompt beyond the cap.
func truncateRole(role string) string {
	if n := utf8.RuneCountInString(role); n <= MaxRoleLen {
		return role
	}
	return string([]rune(role)[:MaxRoleLen])
}
