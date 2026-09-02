package agent

import (
	"testing"

	"reasonix/internal/provider"
)

// cacheAwareCompaction is off by default and only defers an automatic fold to
// the hard input ceiling while the last completed request was served mostly
// from the provider cache. Cold/no-receipt sessions and a warm prefix with the
// switch off must keep the exact default compactTrigger.
func TestCacheAwareCompactionDefersFoldWhenWarm(t *testing.T) {
	const window = 1_000_000
	baseTrigger := int(float64(window) * defaultCompactRatio) // 800_000
	hard := window - protocolReserveTokens                    // 999_744

	// Default (switch off): warm cache has no effect.
	a := &Agent{agentConfig: agentConfig{contextWindow: window, compactRatio: defaultCompactRatio}}
	setLastUsage(a, 900_000, 100_000) // 90% warm
	if got := a.compactTrigger(); got != baseTrigger {
		t.Fatalf("default: compactTrigger = %d, want %d (switch off, warm cache ignored)", got, baseTrigger)
	}

	// Switch on but no receipt yet: cold, unchanged trigger.
	w := &Agent{agentConfig: agentConfig{contextWindow: window, compactRatio: defaultCompactRatio, cacheAwareCompaction: true}}
	if got := w.compactTrigger(); got != baseTrigger {
		t.Fatalf("no receipt: compactTrigger = %d, want %d", got, baseTrigger)
	}

	// Switch on, warm receipt: fold defers to the hard ceiling, never past it.
	setLastUsage(w, 900_000, 100_000)
	if got := w.compactTrigger(); got != hard {
		t.Fatalf("warm: compactTrigger = %d, want %d (hardInputCeiling)", got, hard)
	}

	// Warm but below the 90% threshold: cold, unchanged trigger.
	setLastUsage(w, 800_000, 200_000) // 80% warm
	if got := w.compactTrigger(); got != baseTrigger {
		t.Fatalf("80%% warm: compactTrigger = %d, want %d", got, baseTrigger)
	}

	// Receipt without a cache split (no hit/miss): treated as cold.
	setLastUsage(w, 0, 0)
	if got := w.compactTrigger(); got != baseTrigger {
		t.Fatalf("no cache split: compactTrigger = %d, want %d", got, baseTrigger)
	}

	// hardInputCeiling stays the hard upper bound even when warm.
	if got := w.hardInputCeiling(); got != hard {
		t.Fatalf("hardInputCeiling = %d, want %d", got, hard)
	}
	if got := w.compactTrigger(); got > w.hardInputCeiling() {
		t.Fatalf("compactTrigger = %d exceeds hardInputCeiling = %d", got, w.hardInputCeiling())
	}
}

func TestWarmCacheEdgeThreshold(t *testing.T) {
	cases := []struct {
		name string
		hit  int
		miss int
		want bool
	}{
		{"exactly at 90%", 900, 100, true},
		{"above 90%", 950, 50, true},
		{"just below 90%", 890, 110, false},
		{"all miss", 0, 100, false},
		{"zero total", 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &Agent{}
			setLastUsage(a, c.hit, c.miss)
			if got := a.warmCache(); got != c.want {
				t.Fatalf("warmCache(hit=%d, miss=%d) = %v, want %v", c.hit, c.miss, got, c.want)
			}
		})
	}
}

func setLastUsage(a *Agent, hit, miss int) {
	clone := &provider.Usage{CacheHitTokens: hit, CacheMissTokens: miss}
	a.sess.output.lastUsage.Store(clone)
}
