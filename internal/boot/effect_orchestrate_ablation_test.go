package boot

// A2 at the boot boundary: the subagent ablation removes orchestrate
// structurally rather than gating a call, so no dispatch path — tool:,
// workflow: or task: — can reach it.

import (
	"testing"

	"reasonix/internal/ablation"
	"reasonix/internal/tool"
)

// A2: the sub-agent ablation removes the tool structurally rather than gating a
// call, so no dispatch path — tool:, workflow: or task: — can reach it.
func TestEffectOrchestrateIsAbsentUnderSubagentAblation(t *testing.T) {
	effectOrchestrateStage(t)
	ctrl := effectOrchestrateBuild(t, nil, ablation.New(ablation.Subagent))
	for _, entry := range ctrl.AllToolContractEntries() {
		if entry.Name == tool.HostOrchestrate {
			t.Fatal("an ablated build still registers orchestrate")
		}
	}
}
