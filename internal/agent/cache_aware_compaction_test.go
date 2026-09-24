package agent

import (
	"context"
	"testing"

	"reasonix/internal/ablation"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
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

// The tests below drive the real ContextManager over a foldable transcript, so
// each branch is asserted at the boundary the switch governs: whether a
// projection was installed for that request.

// The fold window these tests run in. It is deliberately small: the transcript
// has to exceed 16% of the window (the recent verbatim tail budget) before there
// is anything to fold, and the thresholds are reached by stating the request
// size rather than by building a million-token transcript.
const (
	cacheAwareWindow   = 100_000
	cacheAwareRatio    = 80_000 // window * defaultCompactRatio
	cacheAwareCeiling  = cacheAwareWindow - protocolReserveTokens
	cacheAwareOverTail = 85_000 // above the ratio, below the ceiling
)

// cacheAwareFixture is the one transcript shape every arm below runs on. The
// control and variant arms must differ in the switch alone, so they share this
// builder rather than each assembling their own Options.
func cacheAwareFixture(t *testing.T, opts Options) *Agent {
	t.Helper()
	if opts.ArchiveDir == "" {
		opts.ArchiveDir = t.TempDir()
	}
	return New(&fakeProvider{reply: "durable digest"}, tool.NewRegistry(), foldableSessionOverForce(40), opts, event.Discard)
}

// cacheAwarePipeline builds the switch-on arm. warm seeds the cache receipt the
// deferral reads; ablated turns compaction off underneath it, which is the
// combination the guard must refuse.
func cacheAwarePipeline(t *testing.T, warm, ablated bool) *Agent {
	t.Helper()
	opts := Options{
		ContextWindow:        cacheAwareWindow,
		CompactRatio:         defaultCompactRatio,
		CacheAwareCompaction: true,
		RecentKeep:           2,
	}
	if ablated {
		opts.Ablation = ablation.New(ablation.Compaction)
	}
	a := cacheAwareFixture(t, opts)
	if warm {
		setLastUsage(a, 900_000, 100_000)
	}
	return a
}

// TestCacheAwareCompactionControlArmFoldsAtTheRatio is the control the warm
// tests are read against. They assert that a warm request above compact_ratio
// does not fold; without this arm that assertion is unfalsifiable, because it
// could always be the fixture rather than the switch suppressing the fold. Same
// transcript, same warm receipt, same request size — only the switch differs.
func TestCacheAwareCompactionControlArmFoldsAtTheRatio(t *testing.T) {
	control := cacheAwareFixture(t, Options{
		ContextWindow: cacheAwareWindow,
		CompactRatio:  defaultCompactRatio,
		RecentKeep:    2,
	})
	setLastUsage(control, 900_000, 100_000)
	if control.cacheAwareCompaction {
		t.Fatal("the control arm must run with the switch off")
	}
	if got := control.compactTrigger(); got != cacheAwareRatio {
		t.Fatalf("control trigger = %d, want compact_ratio %d (a warm cache must not move it)", got, cacheAwareRatio)
	}
	prepareAt(t, control, cacheAwareOverTail)
	if !foldInstalled(control) {
		t.Fatalf("the control arm must fold at %d with the switch off; the warm arm's silence is then attributable to the switch", cacheAwareOverTail)
	}

	// The variant arm on the same fixture and the same request size must not.
	variant := cacheAwarePipeline(t, true, false)
	prepareAt(t, variant, cacheAwareOverTail)
	if foldInstalled(variant) {
		t.Fatalf("the switch-on arm folded at %d, so the deferral is not in effect", cacheAwareOverTail)
	}
}

// prepareAt drives one automatic maintenance pass with the request size the
// caller states, so a threshold is reached without building a transcript that
// actually costs that many tokens.
func prepareAt(t *testing.T, a *Agent, observed int) {
	t.Helper()
	_, err := a.contextManager().Prepare(context.Background(), ContextPreparePolicy{
		Trigger: CompactionTriggerPressure, ObservedInputTokens: observed,
	})
	if err != nil {
		t.Fatalf("prepare at %d observed tokens: %v", observed, err)
	}
}

func foldInstalled(a *Agent) bool {
	return a.sess.compactionState.LastReceipt != nil || a.currentProjectionVersion() != 0
}

// TestCacheAwareCompactionDefersToTheCeilingButStillMaintains is B3's second
// branch: while warm, a request past compact_ratio must not fold, but the
// request that reaches the hard ceiling must — the deferral buys headroom, it
// does not disable maintenance. Both halves matter: without the first the
// switch is a no-op, without the second it is a way to cross the ceiling.
func TestCacheAwareCompactionDefersToTheCeilingButStillMaintains(t *testing.T) {
	a := cacheAwarePipeline(t, true, false)
	if got := a.compactTrigger(); got != a.hardInputCeiling() {
		t.Fatalf("warm trigger = %d, want the hard ceiling %d", got, a.hardInputCeiling())
	}

	prepareAt(t, a, cacheAwareOverTail)
	if foldInstalled(a) {
		t.Fatalf("a warm request at %d tokens folded despite a trigger of %d", cacheAwareOverTail, a.compactTrigger())
	}

	// The same session one request later, now at the ceiling, must maintain.
	prepareAt(t, a, a.hardInputCeiling())
	if !foldInstalled(a) {
		t.Fatal("a warm request at the hard ceiling must still fold")
	}
	if got := projectionTokens(a); got >= a.hardInputCeiling() {
		t.Fatalf("the installed projection is %d tokens, at or above the ceiling %d", got, a.hardInputCeiling())
	}
}

// TestCacheAwareCompactionReturnsToTheRatioAfterTTLCooldown is the second half
// of B3's third branch: a TTL cooldown arrives as an ordinary miss-heavy
// receipt, and the trigger must fall back to compact_ratio rather than keep the
// deferral the stale warm receipt earned.
func TestCacheAwareCompactionReturnsToTheRatioAfterTTLCooldown(t *testing.T) {
	a := cacheAwarePipeline(t, true, false)
	prepareAt(t, a, cacheAwareOverTail)
	if foldInstalled(a) {
		t.Fatal("the warm arm must not fold below the ceiling")
	}

	// The provider cache expired: the next receipt is mostly miss.
	setLastUsage(a, 100_000, 900_000)
	if a.warmCache() {
		t.Fatal("a miss-heavy receipt must not read as warm")
	}
	if got := a.compactTrigger(); got != cacheAwareRatio {
		t.Fatalf("post-cooldown trigger = %d, want compact_ratio %d", got, cacheAwareRatio)
	}
	prepareAt(t, a, cacheAwareOverTail)
	if !foldInstalled(a) {
		t.Fatal("after a TTL cooldown the ordinary ratio trigger must fold again")
	}
}

// TestCacheAwareCompactionCannotBypassCompactionAblation is B3's fourth branch.
// Ablation lowers the trigger to 50% so folds fire earlier; the deferral must
// not read that as a reason to jump to the ceiling, which would put a warm
// ablated benchmark arm far above its own threshold.
func TestCacheAwareCompactionCannotBypassCompactionAblation(t *testing.T) {
	a := cacheAwarePipeline(t, true, true)
	if !a.warmCache() {
		t.Fatal("the fixture must be warm for this to test the guard")
	}
	const ablatedTrigger = cacheAwareWindow / 2
	if got := a.compactTrigger(); got != ablatedTrigger {
		t.Fatalf("ablated warm trigger = %d, want %d (ablation wins over the deferral)", got, ablatedTrigger)
	}
	if got := a.compactTrigger(); got >= a.hardInputCeiling() {
		t.Fatalf("the ablated trigger %d must not reach the ceiling %d", got, a.hardInputCeiling())
	}

	prepareAt(t, a, cacheAwareOverTail)
	if !foldInstalled(a) {
		t.Fatal("an ablated warm session above its threshold must still fold")
	}
}

// TestCacheAwareCompactionLeavesManualCompactAlone pins the other side of the
// deferral: it governs automatic maintenance only. An explicit /compact is a
// user instruction, so a warm prefix must not silently turn it into a no-op.
func TestCacheAwareCompactionLeavesManualCompactAlone(t *testing.T) {
	a := cacheAwarePipeline(t, true, false)
	if err := a.CompactNow(context.Background(), ""); err != nil {
		t.Fatalf("manual compact on a warm session: %v", err)
	}
	if !foldInstalled(a) {
		t.Fatal("a warm session must still honour an explicit compact")
	}
}
