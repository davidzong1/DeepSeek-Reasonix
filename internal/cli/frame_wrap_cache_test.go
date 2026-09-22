// F-B2 acceptance for TEAM_FRAME_PATH_COST_ROUTE.md §5.4: the wrap cache keeps
// its flat line list incrementally from per-block offsets instead of rebuilding
// it, and every operation that used to rebuild still leaves the list identical
// to a full rebuild.
package cli

import (
	"slices"
	"strings"
	"testing"
)

// wrapCacheModel is a sized model with `lines` committed blocks, its wrap cache
// built at the model's own content width.
func wrapCacheModel(t *testing.T, lines int) (chatTUI, int) {
	t.Helper()
	m := frameCostModel(t, frameCostMembers, lines)
	cw := transcriptContentWidth(m.width, m.nativeScrollback)
	return m, cw
}

// referenceFlatten rebuilds every block's wrap and concatenates them: the
// oracle the incremental list is compared against.
func referenceFlatten(m chatTUI, cw int) []string {
	blocks := make([][]string, len(m.transcript))
	for i, block := range m.transcript {
		blocks[i] = wrapBlockLines(block, cw)
	}
	return flattenBlockWraps(blocks)
}

// requireCacheMatchesRebuild fails with the first divergence, naming the step
// that produced it so a broken operation is not reported as a broken cache.
func requireCacheMatchesRebuild(t *testing.T, m chatTUI, cw int, step string) {
	t.Helper()
	want := referenceFlatten(m, cw)
	if len(m.wrappedLines) != len(want) {
		t.Fatalf("%s: wrappedLines has %d lines, a full rebuild has %d", step, len(m.wrappedLines), len(want))
	}
	for i := range want {
		if m.wrappedLines[i] != want[i] {
			t.Fatalf("%s: line %d = %q, rebuild says %q", step, i, m.wrappedLines[i], want[i])
		}
	}
	if m.wrapBlockCount != len(m.transcript) {
		t.Fatalf("%s: wrapBlockCount = %d, transcript has %d blocks", step, m.wrapBlockCount, len(m.transcript))
	}
	if len(m.wrapBlockOffsets) != m.wrapBlockCount {
		t.Fatalf("%s: %d offsets for %d blocks", step, len(m.wrapBlockOffsets), m.wrapBlockCount)
	}
}

// countWrapCalls swaps the wrap primitive for a counting one, so a test can say
// how much of the transcript one operation re-wrapped.
func countWrapCalls(t *testing.T) *int {
	t.Helper()
	calls := new(int)
	original := wrapTranscriptFn
	wrapTranscriptFn = func(s string, width int) string {
		*calls++
		return original(s, width)
	}
	t.Cleanup(func() { wrapTranscriptFn = original })
	return calls
}

// TestWrapCacheSuffixSyncMatchesFullRebuild is the node's core gate: append,
// early-block rewrite, mid-transcript invalidation, removal, truncation and an
// off-loop block adoption each leave the incremental list element-wise equal to
// a full rebuild. It separates "cheaper" from "different" — the probes below
// measure the cost, this one pins the meaning.
func TestWrapCacheSuffixSyncMatchesFullRebuild(t *testing.T) {
	m, cw := wrapCacheModel(t, 60)

	for i := range 4 {
		m.commitLine("appended block with enough words to wrap at this width")
		if !m.syncWrappedLines(cw, false) {
			t.Fatalf("append %d left the cache behind the transcript", i)
		}
		requireCacheMatchesRebuild(t, m, cw, "append")
	}

	m.setTranscriptBlock(2, strings.Repeat("rewritten early block ", 12), transcriptSource{kind: transcriptSourceFixed})
	m.syncWrappedLines(cw, false)
	requireCacheMatchesRebuild(t, m, cw, "early rewrite")

	m.invalidateWrapFrom(20)
	m.syncWrappedLines(cw, false)
	requireCacheMatchesRebuild(t, m, cw, "invalidate from 20")

	m.removeTranscriptBlock(5)
	m.syncWrappedLines(cw, false)
	requireCacheMatchesRebuild(t, m, cw, "remove block 5")

	m.truncateTranscriptBlocks(40)
	m.syncWrappedLines(cw, false)
	requireCacheMatchesRebuild(t, m, cw, "truncate to 40")

	m.appendTranscriptBlock("replay bundle body", transcriptSource{kind: transcriptSourceFixed})
	m.installWrappedBlock(len(m.transcript)-1, replayPaint{
		wrapped: wrapBlockLines("replay bundle body", cw), contentW: cw,
	})
	requireCacheMatchesRebuild(t, m, cw, "off-loop adoption")

	// A width change is the one case that still rebuilds everything.
	m.width += 7
	wide := transcriptContentWidth(m.width, m.nativeScrollback)
	m.syncWrappedLines(wide, true)
	requireCacheMatchesRebuild(t, m, wide, "width change")
}

// TestWrapCacheSuffixSyncRewrapsOnlyTheNewBlocks is the probe the equivalence
// gate cannot give: identical output, proportional cost. Committing a handful of
// new blocks must wrap exactly those, never the history behind them.
func TestWrapCacheSuffixSyncRewrapsOnlyTheNewBlocks(t *testing.T) {
	m, cw := wrapCacheModel(t, 300)
	calls := countWrapCalls(t)

	const added = 5
	for range added {
		m.commitLine("new streamed block")
	}
	if !m.syncWrappedLines(cw, false) {
		t.Fatal("the appends must leave the cache behind the transcript")
	}
	if *calls != added {
		t.Fatalf("one suffix sync wrapped %d blocks, want only the %d new ones", *calls, added)
	}
	// The same probe for the position §2.5 was about: an early rewrite must wrap
	// the block it touched, not the tail behind it.
	m.setTranscriptBlock(2, "rewritten early block body", transcriptSource{kind: transcriptSourceFixed})
	before := *calls
	m.syncWrappedLines(cw, false)
	if *calls-before > 1 {
		t.Fatalf("an early rewrite re-wrapped %d blocks, want 1", *calls-before)
	}
}

// TestWrapCacheInvalidateDefersAndKeepsThePrefix pins the other half: invalidating
// from the middle wraps nothing (the next sync does the work) and keeps exactly
// the prefix lines a rebuild of the surviving blocks would produce.
func TestWrapCacheInvalidateDefersAndKeepsThePrefix(t *testing.T) {
	m, _ := wrapCacheModel(t, 200)
	calls := countWrapCalls(t)

	m.invalidateWrapFrom(10)
	if *calls != 0 {
		t.Fatalf("invalidate wrapped %d blocks; the sync is what wraps", *calls)
	}
	prefix := flattenBlockWraps(m.wrapBlockLines[:10])
	if len(prefix) != len(m.wrappedLines) {
		t.Fatalf("prefix has %d lines, the cache kept %d", len(prefix), len(m.wrappedLines))
	}
	for i := range prefix {
		if prefix[i] != m.wrappedLines[i] {
			t.Fatalf("line %d = %q, want %q", i, m.wrappedLines[i], prefix[i])
		}
	}
	if m.wrapBlockCount != 10 || len(m.wrapBlockOffsets) != 10 {
		t.Fatalf("after invalidate: blocks=%d offsets=%d, want 10", m.wrapBlockCount, len(m.wrapBlockOffsets))
	}
}

// TestWrapCacheSetWrappedBlockAdoptsAndTruncates pins the seam the replay path
// now goes through: adopting a block at an index drops that block and everything
// after it from the cache (they are re-wrapped by the next sync) and leaves the
// prefix untouched. The adopted wrap is the block's own — which is what the
// off-loop caller passes — so a later sync agrees with a full rebuild.
func TestWrapCacheSetWrappedBlockAdoptsAndTruncates(t *testing.T) {
	m, cw := wrapCacheModel(t, 30)
	const at = 5
	adopted := wrapBlockLines(m.transcript[at], cw)
	m.setWrappedBlock(at, adopted, cw)

	if m.wrapBlockCount != at+1 {
		t.Fatalf("wrapBlockCount = %d, want the adopted block plus its prefix", m.wrapBlockCount)
	}
	if got := m.wrappedLines[len(m.wrappedLines)-len(adopted):]; !slices.Equal(got, adopted) {
		t.Fatalf("adopted tail = %v, want the block's own lines %v", got, adopted)
	}
	// The prefix is the same bytes a rebuild would have produced for it.
	prefix := flattenBlockWraps(m.wrapBlockLines[:at])
	if len(prefix) != len(m.wrappedLines)-len(adopted) {
		t.Fatalf("prefix has %d lines, the cache kept %d before the adopted block",
			len(prefix), len(m.wrappedLines)-len(adopted))
	}
	for i := range prefix {
		if prefix[i] != m.wrappedLines[i] {
			t.Fatalf("prefix line %d = %q, want %q", i, m.wrappedLines[i], prefix[i])
		}
	}
	// And a later sync re-wraps the dropped tail back into agreement.
	m.syncWrappedLines(cw, false)
	requireCacheMatchesRebuild(t, m, cw, "sync after adoption")
}

// TestWrapCacheColdTierCostRecordsTheDrop is F-B2's benchmark. §2.5 measured the
// cold tier — an early block rewritten, so the sync had to re-wrap everything
// after it — at 2,519,584 ns/op against a warm 45,777. Three arms are measured
// here so the drop is attributable rather than asserted:
//
//   - the suffix path on an early rewrite, and the same rewrite at the tail: the
//     positions must now cost the same, which is the node's claim;
//   - the pre-F-B2 rebuild (test-only, below) on the same rewrite: the "before";
//   - the per-sync viewport feed, measured alone: an O(transcript) copy this node
//     does not change, so it is the floor under both production arms.
func TestWrapCacheColdTierCostRecordsTheDrop(t *testing.T) {
	m, cw := wrapCacheModel(t, frameCostLines)
	last := len(m.transcript) - 1
	rewrite := func(cur chatTUI, at int) {
		cur.setTranscriptBlock(at, "rewritten block body", transcriptSource{kind: transcriptSourceFixed})
		cur.syncWrappedLines(cw, false)
	}

	cold := testing.Benchmark(func(b *testing.B) {
		cur := m
		for range b.N {
			rewrite(cur, 2)
			cur.feedViewportContent()
		}
	})
	frameCostReport(t, "early-block rewrite + sync", cold)

	warm := testing.Benchmark(func(b *testing.B) {
		cur := m
		for range b.N {
			rewrite(cur, last)
			cur.feedViewportContent()
		}
	})
	frameCostReport(t, "tail rewrite + sync (warm tier)", warm)

	wrapOnly := testing.Benchmark(func(b *testing.B) {
		cur := m
		for range b.N {
			rewrite(cur, 2)
		}
	})
	frameCostReport(t, "early-block rewrite + sync (no feed)", wrapOnly)

	feedOnly := testing.Benchmark(func(b *testing.B) {
		for range b.N {
			m.feedViewportContent()
		}
	})
	frameCostReport(t, "feedViewportContent alone", feedOnly)

	legacy := testing.Benchmark(func(b *testing.B) {
		cur := m
		for range b.N {
			cur.setTranscriptBlock(2, "rewritten block body", transcriptSource{kind: transcriptSourceFixed})
			cur.rebuildWrapFromLegacy(2, cw)
		}
	})
	frameCostReport(t, "early-block rewrite + legacy rebuild (no feed)", legacy)

	// The legacy arm must reproduce the tier §2.5 measured, or this reference is
	// not measuring the thing the node removed.
	if legacy.NsPerOp() < wrapOnly.NsPerOp()*10 {
		t.Errorf("the legacy rebuild measured %d ns/op against the suffix path's %d: this reference is not reproducing the cold tier §2.5 measured",
			legacy.NsPerOp(), wrapOnly.NsPerOp())
	}
	// An early rewrite costs the blocks it touched and the flat-list splice, not
	// the tail's re-wrap: the two positions must now cost the same.
	if cold.NsPerOp() > warm.NsPerOp()*4 {
		t.Errorf("cold tier %d ns/op vs warm %d ns/op: an early rewrite still rebuilds the tail",
			cold.NsPerOp(), warm.NsPerOp())
	}
}

// rebuildWrapFromLegacy is the pre-F-B2 wrap path, kept as the benchmark's "before"
// arm: drop the cache from index, re-wrap every block from there to the end, and
// flatten the whole table into a fresh slice. It touches only the cache fields the
// old implementation owned, so it cannot be mistaken for production code.
func (m *chatTUI) rebuildWrapFromLegacy(index, contentW int) {
	m.wrapBlockLines = m.wrapBlockLines[:index]
	for i := index; i < len(m.transcript); i++ {
		m.wrapBlockLines = append(m.wrapBlockLines, wrapBlockLines(m.transcript[i], contentW))
	}
	m.wrapBlockCount = len(m.transcript)
	m.wrapWidth = contentW
	m.wrappedLines = flattenBlockWraps(m.wrapBlockLines)
}
