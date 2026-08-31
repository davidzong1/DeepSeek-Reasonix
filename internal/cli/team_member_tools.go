package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/team"
	"reasonix/internal/tool"
)

// leaderMemberTool is an Agent-facing management command bound to one leader
// backend. Binding the store and leader identity at assembly time means a
// regular member never receives a tool that could be used to mutate the roster.
type leaderMemberTool struct {
	name     string
	desc     string
	schema   json.RawMessage
	store    *team.TeamStore
	sessions *team.TeamSessionStore
	teamName string
	leaderID string
	// release retires the affected member's assembled backend, as the TUI's member
	// editor already does. Without it a member the leader removed or retagged kept
	// a live controller writing the context that was just cleared.
	release func(teamName, memberID string)
}

// retire releases the member's backend when a registry is wired. A nil release
// is the test/non-interactive shape, not an error.
func (t *leaderMemberTool) retire(memberID string) {
	if t.release != nil {
		t.release(t.teamName, memberID)
	}
}

func (t *leaderMemberTool) Name() string            { return t.name }
func (t *leaderMemberTool) Description() string     { return t.desc }
func (t *leaderMemberTool) Schema() json.RawMessage { return t.schema }
func (t *leaderMemberTool) ReadOnly() bool          { return false }
func (t *leaderMemberTool) PlanModeSafe() bool      { return false }
func (t *leaderMemberTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", fmt.Errorf("%s: unsupported operation", t.name)
}

func newLeaderMemberTools(store *team.TeamStore, sessions *team.TeamSessionStore, teamName, leaderID string, release func(teamName, memberID string)) []tool.Tool {
	if store == nil || strings.TrimSpace(teamName) == "" || strings.TrimSpace(leaderID) == "" {
		return nil
	}
	base := func(name, desc, schema string) *leaderMemberTool {
		return &leaderMemberTool{name: name, desc: desc, schema: json.RawMessage(schema), store: store, sessions: sessions, teamName: teamName, leaderID: leaderID, release: release}
	}
	return []tool.Tool{
		&leaderAddMemberTool{leaderMemberTool: base("leader_add_member", "Add a member slot to this team. Leader-only.", `{"type":"object","properties":{"member_id":{"type":"string"},"role":{"type":"string"},"agent_user_ref":{"type":"string"}},"required":["member_id"]}`)},
		&leaderRemoveMemberTool{leaderMemberTool: base("leader_remove_member", "Remove a member slot from this team. Leader-only.", `{"type":"object","properties":{"member_id":{"type":"string"}},"required":["member_id"]}`)},
		&leaderSetRoleTool{leaderMemberTool: base("leader_set_member_role", "Change a team member's role. Leader-only.", `{"type":"object","properties":{"member_id":{"type":"string"},"role":{"type":"string"}},"required":["member_id","role"]}`)},
	}
}

// newLeaderTaskTools exposes only the task orchestration surface to a leader
// backend. The service owns authorization and persistence; these thin tools
// keep the provider contract stable and easy to inspect.
func newLeaderTaskTools(service *teamTaskService, teamName, leaderID string) []tool.Tool {
	if service == nil {
		return nil
	}
	base := func(name, desc, schema string) *teamTaskTool {
		return &teamTaskTool{name: name, desc: desc, schema: json.RawMessage(schema), service: service, teamName: teamName, memberID: leaderID, leader: true}
	}
	return []tool.Tool{
		base("leader_list_team", "List the current team roster before dispatching work.", `{"type":"object","properties":{},"additionalProperties":false}`),
		base("leader_select_task_members", "Select non-leader members by task and role before assignment.", `{"type":"object","properties":{"task":{"type":"string"},"required_roles":{"type":"string"},"create_missing":{"type":"boolean"}},"required":["task"]}`),
		base("leader_assign_subtask", "Assign a persisted subtask to one non-leader member and start its backend.", `{"type":"object","properties":{"member_name":{"type":"string"},"subtask":{"type":"string"},"context":{"type":"string"}},"required":["member_name","subtask"]}`),
		base("leader_assign_task_to_relevant", "Select relevant non-leader members and assign the task to each.", `{"type":"object","properties":{"task":{"type":"string"},"subtask":{"type":"string"},"required_roles":{"type":"string"},"create_missing":{"type":"boolean"}},"required":["task"]}`),
		base("leader_check_member_status", "Read durable task status for team members without terminal polling.", `{"type":"object","properties":{"member_name":{"type":"string"}}}`),
	}
}

func newMemberTaskTools(service *teamTaskService, teamName, memberID string) []tool.Tool {
	if service == nil {
		return nil
	}
	base := func(name, desc, schema string) *teamTaskTool {
		return &teamTaskTool{name: name, desc: desc, schema: json.RawMessage(schema), service: service, teamName: teamName, memberID: memberID}
	}
	return []tool.Tool{
		base("member_get_my_task", "Read this member's unfinished assigned task.", `{"type":"object","properties":{},"additionalProperties":false}`),
		base("member_report_result", "Report this member's completed task result to the leader. Pass task_id when more than one task is assigned.", `{"type":"object","properties":{"result":{"type":"string"},"task_id":{"type":"string"}},"required":["result"],"additionalProperties":false}`),
	}
}

type teamTaskTool struct {
	name     string
	desc     string
	schema   json.RawMessage
	service  *teamTaskService
	teamName string
	memberID string
	leader   bool
}

func (t *teamTaskTool) Name() string            { return t.name }
func (t *teamTaskTool) Description() string     { return t.desc }
func (t *teamTaskTool) Schema() json.RawMessage { return t.schema }
func (t *teamTaskTool) ReadOnly() bool {
	return t.name == "leader_list_team" || t.name == "leader_select_task_members" || t.name == "leader_check_member_status" || t.name == "member_get_my_task"
}
func (t *teamTaskTool) PlanModeSafe() bool { return t.ReadOnly() }

func (t *teamTaskTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		MemberName    string `json:"member_name"`
		Subtask       string `json:"subtask"`
		Context       string `json:"context"`
		Task          string `json:"task"`
		RequiredRoles string `json:"required_roles"`
		CreateMissing bool   `json:"create_missing"`
		Result        string `json:"result"`
		TaskID        string `json:"task_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("%s: invalid arguments: %w", t.name, err)
	}
	switch t.name {
	case "leader_list_team":
		return t.service.listTeam()
	case "leader_select_task_members":
		selected, roles, err := t.service.selectMembers(p.Task, p.RequiredRoles)
		if err != nil {
			return "", err
		}
		out := fmt.Sprintf("task roles: %s\nselected members: %s", strings.Join(roles, ", "), strings.Join(selected, ", "))
		if len(selected) == 0 && p.CreateMissing {
			out += "\nno matching member; create a member explicitly with leader_add_member"
		}
		return out, nil
	case "leader_assign_subtask":
		assignment, err := t.service.assignSubtask(ctx, p.MemberName, p.Subtask, p.Context)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("task %s assigned to %s (status=%s)", assignment.TaskID, assignment.MemberID, assignment.Status), nil
	case "leader_assign_task_to_relevant":
		selected, roles, err := t.service.selectMembers(p.Task, p.RequiredRoles)
		if err != nil {
			return "", err
		}
		if len(selected) == 0 {
			if len(roles) > 0 {
				return "", fmt.Errorf("no active member matches required roles %s; add or retag a member before assigning", strings.Join(roles, ", "))
			}
			return "", fmt.Errorf("no active non-leader members available for task; add a member before assigning")
		}
		payload := strings.TrimSpace(p.Subtask)
		if payload == "" {
			payload = p.Task
		}
		assigned := make([]string, 0, len(selected))
		for _, member := range selected {
			if _, err := t.service.assignSubtask(ctx, member, payload, "leader task: "+p.Task); err != nil {
				return "", fmt.Errorf("assign %s: %w", member, err)
			}
			assigned = append(assigned, member)
		}
		return fmt.Sprintf("task assigned to %s (roles=%s)", strings.Join(assigned, ", "), strings.Join(roles, ", ")), nil
	case "leader_check_member_status":
		return t.service.checkStatus(strings.TrimSpace(p.MemberName))
	case "member_get_my_task":
		return t.service.memberTask(t.memberID)
	case "member_report_result":
		return t.service.report(t.memberID, p.TaskID, p.Result)
	default:
		return "", fmt.Errorf("%s: unsupported operation", t.name)
	}
}

type leaderAddMemberTool struct{ *leaderMemberTool }

func (t *leaderAddMemberTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		MemberID     string `json:"member_id"`
		Role         string `json:"role"`
		AgentUserRef string `json:"agent_user_ref"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("leader_add_member: invalid arguments: %w", err)
	}
	err := t.store.LeaderAddMember(t.teamName, t.leaderID, team.MemberSlot{MemberID: strings.TrimSpace(p.MemberID), Role: team.RoleID(strings.TrimSpace(p.Role)), AgentUserRef: strings.TrimSpace(p.AgentUserRef), Status: team.MemberStatusActive})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("member %q added to team %q", strings.TrimSpace(p.MemberID), t.teamName), nil
}

type leaderRemoveMemberTool struct{ *leaderMemberTool }

func (t *leaderRemoveMemberTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		MemberID string `json:"member_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("leader_remove_member: invalid arguments: %w", err)
	}
	id := strings.TrimSpace(p.MemberID)
	if err := t.store.LeaderRemoveMember(t.teamName, t.leaderID, id); err != nil {
		return "", err
	}
	t.retire(id) // stop the backend before its context is cleared
	if t.sessions != nil {
		if err := t.sessions.ClearMember(t.teamName, id); err != nil {
			return "", fmt.Errorf("member removed but clearing context failed: %w", err)
		}
	}
	return fmt.Sprintf("member %q removed from team %q", id, t.teamName), nil
}

type leaderSetRoleTool struct{ *leaderMemberTool }

func (t *leaderSetRoleTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		MemberID string `json:"member_id"`
		Role     string `json:"role"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("leader_set_member_role: invalid arguments: %w", err)
	}
	id, role := strings.TrimSpace(p.MemberID), strings.TrimSpace(p.Role)
	if err := t.store.LeaderSetMemberRole(t.teamName, t.leaderID, id, team.RoleID(role)); err != nil {
		return "", err
	}
	// A role change is a context boundary: the role is baked into the member's
	// cache-stable prompt at assembly, so the backend must be retired for the
	// next bind to rebuild it. Mirrors the TUI's member editor.
	t.retire(id)
	if t.sessions != nil {
		if err := t.sessions.ClearMember(t.teamName, id); err != nil {
			return "", fmt.Errorf("role changed but clearing member context failed: %w", err)
		}
	}
	return fmt.Sprintf("member %q role updated to %q", id, role), nil
}
