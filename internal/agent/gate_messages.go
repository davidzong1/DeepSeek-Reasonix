package agent

import (
	"fmt"
	"strings"
)

// contextualToolGateMessage is what the model reads when a tool it was offered
// refuses in the current workflow context. The provider-visible surface is a
// static allowlist kept byte-stable for the prompt cache, so a ContextualTool is
// offered on every turn and can only decline at execution — which makes this
// string the whole explanation the model gets.
//
// Each one names an alternative. A bare refusal is a dead end: the model reached
// for that tool to make progress, and with nothing to do instead it ends the
// turn, which the finish protocol then reports as a missing terminal call. That
// is exactly how a goal-less update_goal call used to cost a repair round.
//
// reason is the tool's own ContextualUnavailableReason when it declares one. It
// only supplies the fallback for tools this allowlist does not name: a named
// tool keeps its actionable alternative, and every other tool gets to explain
// itself instead of a generic refusal.
func contextualToolGateMessage(name string, reason ...string) string {
	switch name {
	case "update_goal":
		return "blocked: update_goal is only available while a goal is running, and no goal is active — no goal state was changed. End this turn with finish instead."
	case "get_goal", "create_goal":
		return "blocked: goal tools require the current top-level host-attested goal context — no goal state was changed"
	case "complete_step":
		return "blocked: complete_step is only available after plan approval. While planning, keep task state with todo_write and present the plan for user approval."
	case "bash_output", "wait", "kill_shell", "job_output", "job_kill":
		return "background jobs are not available in this context"
	default:
		if len(reason) > 0 {
			if detail := strings.TrimSpace(reason[0]); detail != "" {
				return "blocked: " + strings.TrimPrefix(detail, "blocked: ")
			}
		}
		return fmt.Sprintf("blocked: tool %q is unavailable in the current workflow context", name)
	}
}
