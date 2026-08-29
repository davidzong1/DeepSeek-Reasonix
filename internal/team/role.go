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

// Role discipline fragments codify leader/member collaboration rules (§2.2,
// AGENT_OPTIMIZATION_TECHNICAL_ROUTE.md P1.2): tool sequencing, checkpoint
// ack, file locks, formal report, sleep, task-vs-capability distinction and
// error handling. They are static text — no member or business state ever
// interpolates into them — so the assembled prompt stays byte-stable across
// turns for cache-first; callers append them to the turn tail, never the
// stable prefix.
const (
	// leaderCollaborationDiscipline applies to slots whose Leader property is
	// set: inspect, select, assign, then track durable task state.
	leaderCollaborationDiscipline = `团队协作纪律（leader）：
1. 派单前先 leader_list_team 查看成员，再用 leader_select_task_members 选择参与成员；不要把 leader 自身记录当作可分配对象。
2. 复杂任务必须使用 leader_assign_task_to_relevant 或 leader_assign_subtask 创建持久化子任务；简单任务可由 leader 自行完成。
3. 分配后用 leader_check_member_status 追踪任务状态，不轮询终端；收到回报后再整合并收口。

`
	// memberCollaborationDiscipline applies to regular slots: read the durable
	// task first and formally report completion.
	memberCollaborationDiscipline = `团队协作纪律（member）：
1. 动手前先读取持久化任务（member_get_my_task），不要自行发明任务范围。
2. 完成后第一个动作是 member_report_result 正式回报；自然语言或 monitor 推断不能代替正式回报。

`
	// sharedCollaborationDiscipline binds both sides: team-dispatch tools vs
	// the local task capability, no empty-prompt retries, and split-and-rerun
	// instead of blind batch retry.
	sharedCollaborationDiscipline = `共同纪律：
1. 区分团队派单工具与本地 task capability：派单任务走团队工具，不得用本地 task/use_capability 替身执行。
2. 禁止用空 prompt 重试失败的 task 调用；参数错误按错误提示修正 arguments 后重发。
3. 命令批次出现 dependency skip 或 permission deny 时拆分重跑，不整批盲目重试。
`
)

// CollaborationDiscipline returns the static role-specific collaboration
// rules used by frontends when assembling a member identity prompt.
func CollaborationDiscipline(isLeader bool) string {
	if isLeader {
		return leaderCollaborationDiscipline + sharedCollaborationDiscipline
	}
	return memberCollaborationDiscipline + sharedCollaborationDiscipline
}

// SystemPromptForRole assembles the member-identity fragment of a team
// session's system prompt (§2.2): team name, member id, free-text role, and
// the collaboration discipline for the slot's leader property — leader rules
// for a leader slot, member rules otherwise, and the shared rules for both.
// Callers inject it into the turn tail, never the stable prefix — the prefix
// must stay byte-stable for cache-first. An empty role renders the
// unconfigured hint instead of a role line; the discipline still applies.
func SystemPromptForRole(teamName, memberID string, role RoleID, leader ...bool) string {
	isLeader := len(leader) > 0 && leader[0]
	var b strings.Builder
	b.WriteString("你是团队 " + teamName + " 的成员 " + memberID + "。\n")
	if role == "" {
		b.WriteString("你尚未配置团队角色，请根据团队交付需求自行规划职责。\n")
	} else {
		b.WriteString("你的团队角色是：" + string(role) + "。\n")
		b.WriteString("请以该角色和专精方向参与任务。\n")
	}
	if isLeader {
		b.WriteString(leaderCollaborationDiscipline)
	} else {
		b.WriteString(memberCollaborationDiscipline)
	}
	b.WriteString(sharedCollaborationDiscipline)
	return b.String()
}
