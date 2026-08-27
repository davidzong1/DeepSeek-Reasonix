package agent

import "fmt"

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
func contextualToolGateMessage(name string) string {
	switch name {
	case "update_goal":
		return "blocked: update_goal is only available while a goal is running, and no goal is active — no goal state was changed. End this turn with finish instead."
	case "complete_step":
		return "blocked: complete_step is only available after plan approval. While planning, keep task state with todo_write and present the plan for user approval."
	case "bash_output", "wait", "kill_shell":
		return "background jobs are not available in this context"
	default:
		return fmt.Sprintf("blocked: tool %q is unavailable in the current workflow context", name)
	}
}
