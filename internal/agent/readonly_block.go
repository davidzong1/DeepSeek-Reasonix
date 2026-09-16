package agent

import (
	"strings"

	"reasonix/internal/tool"
)

// readOnlyExecutionUnwritableReason returns the read-only boundary's block
// reason for a statically writable tool, or "" when the tool must stay
// reachable. A TeamLifecycleStateWriter passes: its writes are confined to team
// coordination state, so a member under a read-only/plan task can still report.
func readOnlyExecutionUnwritableReason(t tool.Tool) string {
	if tc, ok := t.(tool.TeamLifecycleStateWriter); ok && tc.TeamLifecycleStateWriter() {
		return ""
	}
	if reasoner, ok := t.(tool.ReadOnlyExecutionBlockReason); ok && strings.TrimSpace(reasoner.ReadOnlyExecutionBlockReason()) != "" {
		return reasoner.ReadOnlyExecutionBlockReason()
	}
	return "execute a state-changing tool"
}
