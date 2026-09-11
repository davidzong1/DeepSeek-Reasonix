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
// interpolates into them — so a caller may fold them into the cache-stable
// prefix once at assembly and the prefix still stays byte-stable across turns.
const (
	// leaderCollaborationDiscipline applies to slots whose Leader property is
	// set: split, assign, then track durable task state. Execution is a
	// member's job — an unbounded "simple tasks may stay with the leader"
	// escape hatch let the solo-coding base prompt win and the leader did the
	// work itself, so the boundary is drawn at write access instead.
	leaderCollaborationDiscipline = `Team collaboration discipline (leader):
1. Your job is to split work, assign it, track progress, decide authorizations and keep the granularity aligned — not to implement it yourself; members execute.
2. Before assigning, call leader_list_team to see the members, then leader_select_task_members to choose who takes part; never treat the leader's own record as an assignable target.
3. Once a task arrives, split it into independently deliverable subtasks and create each one durably with leader_assign_task_to_relevant or leader_assign_subtask; if no member fits, call leader_add_member first rather than doing the work yourself.
4. Only the read-only inspection your split and acceptance need (reading files, checking status) is yours to do; writing files, changing code and running implementation commands must be assigned to a member.
5. After assigning, track task state with leader_check_member_status instead of polling terminals; integrate and close out once the reports arrive.
6. Authorization decisions are yours to make — do not hand them to the user: a member's out-of-scope write blocks until you decide, so call leader_list_member_approvals to see the pending requests and leader_resolve_member_approval to answer them; when the same directory keeps coming back, grant scope "session" rather than blocking the member again and again.

`
	// memberCollaborationDiscipline applies to regular slots: read the durable
	// task first and formally report completion.
	memberCollaborationDiscipline = `Team collaboration discipline (member):
1. Read the durable task (member_get_my_task) before you start; never invent your own scope.
2. Your first action when you finish is a formal member_report_result; prose or an inferred monitor state does not replace the formal report.

`
	// sharedCollaborationDiscipline binds both sides: team-dispatch tools vs
	// the local task capability, no empty-prompt retries, and split-and-rerun
	// instead of blind batch retry.
	sharedCollaborationDiscipline = `Shared discipline:
1. Keep the team-dispatch tools apart from the local task capability: dispatched work goes through the team tools, never a local task/use_capability stand-in.
2. Never retry a failed task call with an empty prompt; fix the arguments as the error indicates, then resend.
3. When a command batch hits a dependency skip or permission deny, split it and rerun — do not blind-retry the whole batch.
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
	b.WriteString("You are member " + memberID + " of team " + teamName + ".\n")
	if role == "" {
		b.WriteString("You have no team role configured; plan your own responsibilities from the team's delivery needs.\n")
	} else {
		b.WriteString("Your team role is: " + string(role) + ".\n")
		b.WriteString("Take part in the work from that role and specialty.\n")
	}
	if isLeader {
		b.WriteString(leaderCollaborationDiscipline)
	} else {
		b.WriteString(memberCollaborationDiscipline)
	}
	b.WriteString(sharedCollaborationDiscipline)
	return b.String()
}
