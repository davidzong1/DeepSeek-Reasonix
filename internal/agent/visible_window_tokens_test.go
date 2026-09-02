package agent

import "testing"

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
