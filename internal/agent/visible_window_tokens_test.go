package agent

import (
	"testing"

	"reasonix/internal/event"
)

// VisibleWindowTokens must default to the 16% recent-tail budget at zero, and
// cap the budget when set below the default. The compact trigger and hard input
// ceiling are unaffected.
func TestVisibleWindowTokensCapsRecentTailBudget(t *testing.T) {
	// Zero value: unchanged 16% behavior.
	a := &Agent{agentConfig: agentConfig{contextWindow: 1_000_000, compactRatio: defaultCompactRatio}}
	wantDefault := 160_000
	if got := a.recentTailBudget(); got != wantDefault {
		t.Fatalf("zero VisibleWindowTokens: recentTailBudget = %d, want %d (16%% of 1M)", got, wantDefault)
	}

	// VisibleWindowTokens > 0 and below the default: cap at the configured value.
	a.visibleWindowTokens = 80_000
	if got := a.recentTailBudget(); got != 80_000 {
		t.Fatalf("VisibleWindowTokens=80000: recentTailBudget = %d, want 80000", got)
	}

	// VisibleWindowTokens > 0 but above the default: no effect (16% is smaller).
	a.visibleWindowTokens = 200_000
	if got := a.recentTailBudget(); got != wantDefault {
		t.Fatalf("VisibleWindowTokens=200000 (above 16%%): recentTailBudget = %d, want %d", got, wantDefault)
	}

	// Compact trigger must be unchanged regardless of VisibleWindowTokens.
	trigger := a.compactTrigger()
	wantTrigger := int(float64(1_000_000) * defaultCompactRatio)
	if trigger != wantTrigger {
		t.Fatalf("compactTrigger = %d, want %d (unchanged by VisibleWindowTokens)", trigger, wantTrigger)
	}

	// Hard input ceiling must be unchanged.
	hard := a.hardInputCeiling()
	wantHard := 1_000_000 - protocolReserveTokens
	if hard != wantHard {
		t.Fatalf("hardInputCeiling = %d, want %d (unchanged by VisibleWindowTokens)", hard, wantHard)
	}
}

// TestVisibleWindowTokensReachesTheAgentFromOptions is the consumed boundary the
// test above cannot see. Setting the private field proves the cap is computed
// correctly; only this proves the configured value ever arrives, because the
// host-facing option key is otherwise silently inert.
func TestVisibleWindowTokensReachesTheAgentFromOptions(t *testing.T) {
	a := New(nil, nil, NewSession("sys"), Options{
		ContextWindow:       1_000_000,
		VisibleWindowTokens: 80_000,
	}, event.Discard)
	if got := a.recentTailBudget(); got != 80_000 {
		t.Fatalf("Options.VisibleWindowTokens did not reach the agent: recentTailBudget = %d, want 80000", got)
	}
}
