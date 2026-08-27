package agent

import (
	"strings"
	"testing"
)

// TestContextualGateMessagesNameAnAlternative pins the contract that turned a
// wasted round trip into a productive one: a tool that was offered and then
// declines must tell the model what to do instead. Without that, the model has
// nothing left to do and ends the turn, and the finish protocol reports the
// missing terminal call — the shape observed with a goal-less update_goal call.
func TestContextualGateMessagesNameAnAlternative(t *testing.T) {
	for _, tc := range []struct {
		name        string
		alternative string
	}{
		{"update_goal", "finish"},
		{"complete_step", "todo_write"},
	} {
		msg := contextualToolGateMessage(tc.name)
		if !strings.Contains(msg, tc.alternative) {
			t.Errorf("%s refusal must point at %q, got: %s", tc.name, tc.alternative, msg)
		}
		if !strings.Contains(msg, tc.name) {
			t.Errorf("%s refusal must name the tool it declined, got: %s", tc.name, msg)
		}
	}
	// update_goal's refusal also has to state the precondition, so the model can
	// tell this from a malformed-arguments rejection and not simply retry.
	goal := contextualToolGateMessage("update_goal")
	if !strings.Contains(goal, "only available while a goal is running") {
		t.Errorf("update_goal refusal must state the precondition, got: %s", goal)
	}
	if !strings.Contains(goal, "no goal state was changed") {
		t.Errorf("update_goal refusal must say nothing was written, got: %s", goal)
	}
	// An unknown contextual tool still gets a message rather than an empty result.
	if got := contextualToolGateMessage("some_future_tool"); !strings.Contains(got, "some_future_tool") {
		t.Errorf("the fallback must name the tool, got: %s", got)
	}
}
