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
}

func (t *leaderMemberTool) Name() string            { return t.name }
func (t *leaderMemberTool) Description() string     { return t.desc }
func (t *leaderMemberTool) Schema() json.RawMessage { return t.schema }
func (t *leaderMemberTool) ReadOnly() bool          { return false }
func (t *leaderMemberTool) PlanModeSafe() bool      { return false }
func (t *leaderMemberTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", fmt.Errorf("%s: unsupported operation", t.name)
}

func newLeaderMemberTools(store *team.TeamStore, sessions *team.TeamSessionStore, teamName, leaderID string) []tool.Tool {
	if store == nil || strings.TrimSpace(teamName) == "" || strings.TrimSpace(leaderID) == "" {
		return nil
	}
	base := func(name, desc, schema string) *leaderMemberTool {
		return &leaderMemberTool{name: name, desc: desc, schema: json.RawMessage(schema), store: store, sessions: sessions, teamName: teamName, leaderID: leaderID}
	}
	return []tool.Tool{
		&leaderAddMemberTool{leaderMemberTool: base("leader_add_member", "Add a member slot to this team. Leader-only.", `{"type":"object","properties":{"member_id":{"type":"string"},"role":{"type":"string"},"agent_user_ref":{"type":"string"}},"required":["member_id"]}`)},
		&leaderRemoveMemberTool{leaderMemberTool: base("leader_remove_member", "Remove a member slot from this team. Leader-only.", `{"type":"object","properties":{"member_id":{"type":"string"}},"required":["member_id"]}`)},
		&leaderSetRoleTool{leaderMemberTool: base("leader_set_member_role", "Change a team member's role. Leader-only.", `{"type":"object","properties":{"member_id":{"type":"string"},"role":{"type":"string"}},"required":["member_id","role"]}`)},
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
	if t.sessions != nil {
		if err := t.sessions.ClearMember(t.teamName, id); err != nil {
			return "", fmt.Errorf("role changed but clearing member context failed: %w", err)
		}
	}
	return fmt.Sprintf("member %q role updated to %q", id, role), nil
}
