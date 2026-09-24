// Part B (TEAM_MEMBER_CACHE_ROOTCAUSE_3AGENT_EXECUTION_PLAN.zh-CN.md) §3 Agent B
// item 6: the two cache-shaping knobs a Team member backend carries must survive
// the whole chain instead of silently falling back to the defaults.
//
// The failure this pins happened once: agent.New assembled its agentConfig
// without copying visibleWindowTokens or cacheAwareCompaction, so both config
// keys were dead while the boots kept passing them, and the default build stayed
// byte-identical either way, so the guard reaches the consumer, not the field.
package agent

import (
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/tool"
)

// TestSubagentInheritsTheCacheShapingKnobs covers the path a member's own child
// agents take: a task spawned by the member must maintain its context under the
// same two knobs, or the member's prefix stability ends at the first subagent.
func TestSubagentInheritsTheCacheShapingKnobs(t *testing.T) {
	const window, capped = 1_000_000, 80_000

	task := NewTaskToolWithOptions(TaskToolOptions{
		Provider:             &mockProvider{name: "member"},
		ParentRegistry:       tool.NewRegistry(),
		ContextWindow:        window,
		VisibleWindowTokens:  capped,
		CacheAwareCompaction: true,
	})
	child := task.subagentOptions(t.Context(), 10, nil, window, 1, "", nil)
	if child.VisibleWindowTokens != capped {
		t.Errorf("subagent VisibleWindowTokens = %d, want %d", child.VisibleWindowTokens, capped)
	}
	if !child.CacheAwareCompaction {
		t.Error("subagent must inherit cache_aware_compaction")
	}

	// The fields are only half the contract: the child's agent has to act on
	// them. recentTailBudget is the consumed form of the window cap — it is what
	// a fold keeps verbatim, and therefore where a dropped option shows up.
	built := New(&mockProvider{name: "child"}, tool.NewRegistry(), NewSession("sys"), child, event.Discard)
	if got := built.recentTailBudget(); got != capped {
		t.Errorf("child recentTailBudget = %d, want %d (the configured cap)", got, capped)
	}

	// Zero must mean the documented defaults, not "inherited": no cap beyond
	// 16% of the window, and no warm-cache deferral.
	plain := NewTaskToolWithOptions(TaskToolOptions{
		Provider:       &mockProvider{name: "member"},
		ParentRegistry: tool.NewRegistry(),
		ContextWindow:  window,
	})
	inherited := plain.subagentOptions(t.Context(), 10, nil, window, 1, "", nil)
	if inherited.VisibleWindowTokens != 0 || inherited.CacheAwareCompaction {
		t.Errorf("a member without the knobs must not invent them: %+v", inherited.VisibleWindowTokens)
	}
	defaulted := New(&mockProvider{name: "child"}, tool.NewRegistry(), NewSession("sys"), inherited, event.Discard)
	if got, want := defaulted.recentTailBudget(), int(float64(window)*recentTailBudgetRatio); got != want {
		t.Errorf("default recentTailBudget = %d, want %d (16%% of the window)", got, want)
	}
}
