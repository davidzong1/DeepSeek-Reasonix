package cli

import "strings"

// wrapCache keeps the viewport's wrapped line list in sync with transcript
// blocks without re-wrapping the entire history on every streaming update.
//
// Contract:
//   - wrapWidth is the content width used for the cached lines.
//   - wrapBlockCount is how many leading transcript blocks are fully reflected
//     in wrapBlockLines / wrapBlockOffsets and in the matching prefix of
//     wrappedLines; wrapBlockOffsets[i] is where block i starts in that list.
//   - A block rewritten in place is recorded in wrapDirty, so the next sync
//     re-wraps only it and splices its lines back in.
//   - invalidateWrapFrom(i) drops the cache from block i onward, for a removal
//     or a truncation where the later blocks are different blocks.
//   - Full rebuild is reserved for width change, history shrink, or an empty
//     cache — never for a routine transcriptDirty flag alone.
func (m *chatTUI) clearWrapCache() {
	m.wrappedLines = nil
	m.wrapWidth = 0
	m.wrapBlockCount = 0
	m.wrapBlockLines = nil
	m.wrapBlockOffsets = nil
	m.wrapDirty = wrapSpan{}
}

// wrapSpan is the half-open block range whose content changed in place since the
// last sync. A rewrite keeps the blocks after it — their text did not change, so
// their wraps did not either; only their offsets shift. That distinction is what
// keeps an early rewrite from re-wrapping the whole tail (F-B2).
type wrapSpan struct{ from, to int }

// dirty reports whether any block's content changed in place.
func (s wrapSpan) dirty() bool { return s.to > s.from }

// markWrapDirty widens the span to cover one rewritten block. It is what
// setTranscriptBlock records instead of dropping the cache from that block on.
func (m *chatTUI) markWrapDirty(index int) {
	if index < 0 || index >= m.wrapBlockCount {
		return
	}
	if !m.wrapDirty.dirty() {
		m.wrapDirty = wrapSpan{from: index, to: index + 1}
		return
	}
	m.wrapDirty.from = min(m.wrapDirty.from, index)
	m.wrapDirty.to = max(m.wrapDirty.to, index+1)
}

// syncWrappedLines ensures wrapBlockLines/wrappedLines match m.transcript at
// contentW. forceFull rebuilds every block; otherwise only the blocks whose
// content changed — the dirty span, then everything from wrapBlockCount onward —
// are wrapped, and their lines are spliced into the flat list in place.
// Returns true when the flat line list changed and the viewport must be fed.
func (m *chatTUI) syncWrappedLines(contentW int, forceFull bool) bool {
	if contentW <= 0 {
		contentW = 1
	}
	n := len(m.transcript)
	if forceFull || contentW != m.wrapWidth || n < m.wrapBlockCount {
		return m.rebuildWrappedLinesFull(contentW)
	}
	// Heal a desynced block slice (should not happen if invalidate is used).
	if len(m.wrapBlockLines) != m.wrapBlockCount || len(m.wrapBlockOffsets) != m.wrapBlockCount {
		return m.rebuildWrappedLinesFull(contentW)
	}
	changed := m.rewrapDirtyBlocks(contentW)
	if n == m.wrapBlockCount {
		return changed
	}
	// Suffix-only: re-wrap mutated/new blocks from wrapBlockCount..n and append
	// their lines to the prefix the cache already holds.
	for i := m.wrapBlockCount; i < n; i++ {
		blockLines := wrapBlockLines(m.transcript[i], contentW)
		m.wrapBlockOffsets = append(m.wrapBlockOffsets, len(m.wrappedLines))
		m.wrapBlockLines = append(m.wrapBlockLines, blockLines)
		m.wrappedLines = append(m.wrappedLines, blockLines...)
	}
	m.wrapBlockCount = n
	m.wrapWidth = contentW
	m.wrapDirty = wrapSpan{}
	return true
}

// rewrapDirtyBlocks re-wraps the blocks whose content changed in place and
// splices their lines back into the flat list, shifting the offsets of the
// blocks after them by the line delta. The blocks outside the span keep the
// wraps they already have, which is the whole saving: an early rewrite costs the
// blocks it touched, never the tail behind them.
func (m *chatTUI) rewrapDirtyBlocks(contentW int) bool {
	if !m.wrapDirty.dirty() {
		return false
	}
	from, to := m.wrapDirty.from, min(m.wrapDirty.to, m.wrapBlockCount)
	m.wrapDirty = wrapSpan{}
	if to <= from {
		return false
	}
	start, oldEnd := m.wrapBlockOffsets[from], m.wrapBlockOffsets[to-1]+len(m.wrapBlockLines[to-1])
	replacement := make([]string, 0, oldEnd-start)
	for i := from; i < to; i++ {
		m.wrapBlockLines[i] = wrapBlockLines(m.transcript[i], contentW)
		m.wrapBlockOffsets[i] = start + len(replacement)
		replacement = append(replacement, m.wrapBlockLines[i]...)
	}
	tail := append([]string(nil), m.wrappedLines[oldEnd:]...)
	m.wrappedLines = append(m.wrappedLines[:start], replacement...)
	m.wrappedLines = append(m.wrappedLines, tail...)
	delta := len(replacement) - (oldEnd - start)
	for i := to; i < m.wrapBlockCount; i++ {
		m.wrapBlockOffsets[i] += delta
	}
	m.wrapWidth = contentW
	return true
}

func (m *chatTUI) rebuildWrappedLinesFull(contentW int) bool {
	if contentW <= 0 {
		contentW = 1
	}
	n := len(m.transcript)
	m.wrapBlockLines = make([][]string, n)
	m.wrapBlockOffsets = make([]int, n)
	m.wrappedLines = nil
	for i := range n {
		m.wrapBlockOffsets[i] = len(m.wrappedLines)
		m.wrapBlockLines[i] = wrapBlockLines(m.transcript[i], contentW)
		m.wrappedLines = append(m.wrappedLines, m.wrapBlockLines[i]...)
	}
	m.wrapBlockCount = n
	m.wrapWidth = contentW
	m.wrapDirty = wrapSpan{}
	return true
}

// feedViewportContent pushes the wrap cache into the bubbles viewport without
// re-joining the document to a single string. The line slice is cloned so later
// cache growth cannot mutate storage the viewport still holds.
func (m *chatTUI) feedViewportContent() {
	if len(m.wrappedLines) == 0 {
		m.viewport.SetContentLines(nil)
		return
	}
	lines := make([]string, len(m.wrappedLines))
	copy(lines, m.wrappedLines)
	m.viewport.SetContentLines(lines)
}

// wrapTranscriptFn is the wrap primitive the cache calls, hoisted to a variable
// so a test can count how much of a transcript one sync re-wraps (F-B2's probe).
// Production always uses wrapTranscript.
var wrapTranscriptFn = wrapTranscript

// wrapBlockLines wraps one transcript block to width as a line slice.
func wrapBlockLines(block string, width int) []string {
	wrapped := wrapTranscriptFn(block, width)
	if wrapped == "" {
		return []string{""}
	}
	return strings.Split(wrapped, "\n")
}

// flattenBlockWraps concatenates per-block wrapped line groups. Block boundaries
// in the transcript are already forced newlines (strings.Join(blocks, "\n")), so
// concatenating independent wraps matches the joined document for our content.
// It is the reference the incremental list is checked against in tests.
func flattenBlockWraps(blocks [][]string) []string {
	if len(blocks) == 0 {
		return nil
	}
	n := 0
	for _, b := range blocks {
		n += len(b)
	}
	out := make([]string, 0, n)
	for _, b := range blocks {
		out = append(out, b...)
	}
	return out
}

// wrappedContentString returns the viewport payload for tests/debug.
func (m chatTUI) wrappedContentString() string {
	if len(m.wrappedLines) == 0 {
		return ""
	}
	return strings.Join(m.wrappedLines, "\n")
}

// invalidateWrapFrom drops the wrap cache from block index onward so the next
// syncWrappedLines only re-wraps the suffix. Used for a removal or a truncation,
// where the blocks after index are different blocks, not different text.
//
// The flat list is truncated to the prefix's last offset instead of being
// re-flattened: wrapBlockOffsets[index] is exactly where block index starts, so
// the truncation is O(1) and the prefix lines are already the right ones.
func (m *chatTUI) invalidateWrapFrom(index int) {
	if index < 0 {
		index = 0
	}
	if index >= m.wrapBlockCount {
		return
	}
	m.wrapBlockLines = m.wrapBlockLines[:index]
	m.wrapBlockOffsets = m.wrapBlockOffsets[:index]
	m.wrapBlockCount = index
	if index == 0 {
		m.wrappedLines = nil
	} else {
		m.wrappedLines = m.wrappedLines[:m.wrapBlockOffsets[index-1]+len(m.wrapBlockLines[index-1])]
	}
	m.wrapDirty = wrapSpan{}
}

// setWrappedBlock adopts one block's already-computed wrap at index, keeping the
// cache consistent for the blocks before it and dropping everything from index
// onward. It is the seam a caller that rendered a block off the loop uses
// (team_replay.go) so it never has to touch the cache's fields directly.
//
// A cache that is not the prefix this block extends — a test host that never
// presented a frame, a width the cache was not built at — is left to the lazy
// path, which rebuilds from this block onward.
func (m *chatTUI) setWrappedBlock(index int, lines []string, contentW int) {
	if index < 0 || index > len(m.transcript) {
		return
	}
	if m.wrapBlockCount == 0 && index == 0 && len(m.transcript) == 1 {
		// A bind cleared the display, so this block is the whole transcript.
		m.wrapBlockLines = [][]string{lines}
		m.wrapBlockOffsets = []int{0}
		m.wrapBlockCount = 1
		m.wrapWidth = contentW
		m.wrapDirty = wrapSpan{}
		m.wrappedLines = append(m.wrappedLines[:0], lines...)
		m.feedViewportContent()
		return
	}
	// Everything from index onward is re-wrapped by the next sync; the blocks
	// before it keep the wraps they have. The adoption fills in the block the
	// caller already rendered when the cache is still its prefix.
	if m.wrapWidth != contentW || index > m.wrapBlockCount || len(m.wrapBlockLines) != m.wrapBlockCount {
		m.invalidateWrapFrom(index)
		return
	}
	m.invalidateWrapFrom(index)
	m.wrapBlockOffsets = append(m.wrapBlockOffsets, len(m.wrappedLines))
	m.wrapBlockLines = append(m.wrapBlockLines, lines)
	m.wrapBlockCount = index + 1
	m.wrappedLines = append(m.wrappedLines, lines...)
	m.wrapWidth = contentW
	m.feedViewportContent()
}
