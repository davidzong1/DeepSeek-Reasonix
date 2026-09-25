package cli

import (
	"fmt"
	"strings"

	"reasonix/internal/team"
)

// This file holds the task-selection and role-matching helpers of the team task
// service: pure functions over task rows and strings, with no store in reach.
// They live apart from teamTaskService so that file stays within its ceiling.

func taskIDs(tasks []team.Task) string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, string(task.ID))
	}
	return strings.Join(ids, ", ")
}

// undrivenTaskError refuses a report whose target no runtime is executing. The
// member cannot have completed work nothing drove; the durable status alone
// (assigned after a refused dispatch, running after a dead runtime) would have
// sent the completion into claimTerminal's unknown-task refusal. The fix is
// the leader's: retry the dispatch or reassign.
func undrivenTaskError(task *team.Task) error {
	return fmt.Errorf("task %s is not executing (recorded %s, nothing is driving it): ask the leader to retry or reassign it, then report", task.ID, task.Status)
}

// undrivenTasksError is the multi-row form of undrivenTaskError: every owned
// task is listed with its durable status so the member can tell the leader
// which one to retry, instead of guessing between rows nothing drives.
func undrivenTasksError(memberID string, owned []team.Task) error {
	states := make([]string, 0, len(owned))
	for _, task := range owned {
		states = append(states, fmt.Sprintf("%s (recorded %s)", task.ID, task.Status))
	}
	return fmt.Errorf("member %q has %d unfinished tasks but none is executing (%s): ask the leader to retry or reassign one before reporting",
		memberID, len(owned), strings.Join(states, ", "))
}

func parseRoles(raw string) []string {
	seen := map[string]bool{}
	var out []string
	for part := range strings.SplitSeq(raw, ",") {
		role := strings.TrimSpace(strings.ToLower(part))
		if role != "" && !seen[role] {
			seen[role] = true
			out = append(out, role)
		}
	}
	return out
}

func inferRoles(task string) []string {
	t := strings.ToLower(task)
	roles := make([]string, 0, 1)
	for _, pair := range []struct {
		role string
		keys []string
	}{
		{string(team.RoleTester), []string{"test", "测试", "验证"}},
		{string(team.RoleReviewer), []string{"review", "审查", "评审"}},
		{string(team.RoleArchitectureAnalyst), []string{"architect", "architecture", "design", "架构", "设计"}},
		{string(team.RolePluginEngineer), []string{"plugin", "mcp", "插件"}},
		{string(team.RoleCoder), []string{"code", "implement", "fix", "实现", "修复", "编码"}},
	} {
		for _, key := range pair.keys {
			if strings.Contains(t, key) {
				roles = append(roles, pair.role)
				break
			}
		}
	}
	return roles
}

func containsRole(roles []string, role team.RoleID) bool {
	for _, want := range roles {
		if strings.EqualFold(want, string(role)) {
			return true
		}
	}
	return false
}
