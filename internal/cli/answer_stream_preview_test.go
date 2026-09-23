package cli

import (
	"strings"
	"testing"
	"time"

	"reasonix/internal/event"
)

// TestStreamedMarkdownPreview pins what a streaming answer may paint before the
// turn ends: everything except a trailing line that is mid-math, because the
// renderer reads math one line at a time and would paint the half-arrived `$…`
// as raw LaTeX first.
func TestStreamedMarkdownPreview(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
	}{
		{"empty", "", ""},
		{"no newline yet", "writing the first line", "writing the first line"},
		{"trailing newline", "first line\n", "first line\n"},
		{"open paragraph", "first para\n\nsecond para ", "first para\n\nsecond para "},
		{"open fence", "```go\ncode\n", "```go\ncode\n"},
		{"mid inline math", "Euler is $e^{i\\", "Euler is "},
		{"mid display math", "sum:\n$$\\sum_{n=1}", "sum:\n"},
		{"closed math stays", "see $x$ here\n", "see $x$ here\n"},
		{"unmatched closer waits too", "costs $5 per call", "costs "},
		{"text after the open span waits", "a $x$ and $y", "a $x$ and "},
	} {
		if got := streamedMarkdownPreview(tc.raw); got != tc.want {
			t.Errorf("%s: streamedMarkdownPreview(%q) = %q, want %q", tc.name, tc.raw, got, tc.want)
		}
	}
}

// TestStreamAnswerPaintsTheOpenParagraph is the regression the live preview
// exists for: an answer that has not closed a paragraph yet must still be on
// screen. Withholding it until the first blank line left the window frozen for
// the whole paragraph and then dumped it in one jump.
func TestStreamAnswerPaintsTheOpenParagraph(t *testing.T) {
	m := newTestChatTUI()

	m.ingestEvent(event.Event{Kind: event.Text, Text: "a paragraph with no"})
	if m.answerIdx < 0 {
		t.Fatal("the first chunk should open the live answer block")
	}
	if joined := strings.Join(m.transcript, "\n"); !strings.Contains(joined, "a paragraph with no") {
		t.Fatalf("the open paragraph should be visible as it streams, transcript=%v", m.transcript)
	}

	m.ingestEvent(event.Event{Kind: event.Text, Text: " blank line yet"})
	// One block, rewritten in place: the preview must not append a block per chunk.
	if len(m.transcript) != 1 {
		t.Fatalf("the live answer should stay one block, transcript=%v", m.transcript)
	}
	// The paint is rate-bounded, so the chunk above lands on the next paint; the
	// block never shows less than what it was opened with.
	if joined := strings.Join(m.transcript, "\n"); !strings.Contains(joined, "a paragraph with no") {
		t.Fatalf("the block should keep the text it was opened with, transcript=%v", m.transcript)
	}
}

// TestStreamAnswerThrottlesTheOpenBlock pins the rate bound: while the block is
// still open, a chunk that arrives inside the paint interval does not repaint,
// and one that arrives after it does. The clock is rewound by hand rather than
// slept on, the way the other interval tests in this package do it.
func TestStreamAnswerThrottlesTheOpenBlock(t *testing.T) {
	m := newTestChatTUI()

	m.ingestEvent(event.Event{Kind: event.Text, Text: "first chunk"})
	painted := m.transcript[m.answerIdx]

	m.ingestEvent(event.Event{Kind: event.Text, Text: " + second chunk"})
	if m.transcript[m.answerIdx] != painted {
		t.Fatal("a chunk inside the paint interval should not repaint the open block")
	}

	m.answerPaintedAt = time.Now().Add(-2 * answerPaintInterval)
	m.ingestEvent(event.Event{Kind: event.Text, Text: " + third chunk"})
	if m.transcript[m.answerIdx] == painted {
		t.Fatal("a chunk after the paint interval should repaint the open block")
	}
	if joined := strings.Join(m.transcript, "\n"); !strings.Contains(joined, "third chunk") {
		t.Fatalf("the repaint should carry the new text, transcript=%v", m.transcript)
	}
}

// TestStreamAnswerPaintsAClosedBlockImmediately keeps the pre-preview paint
// points intact: a chunk that closes a paragraph is the moment the text becomes
// stable, so it repaints without waiting for the interval.
func TestStreamAnswerPaintsAClosedBlockImmediately(t *testing.T) {
	m := newTestChatTUI()

	m.ingestEvent(event.Event{Kind: event.Text, Text: "first paragraph.\n\nsecond"})
	m.ingestEvent(event.Event{Kind: event.Text, Text: " paragraph closes.\n\n"})
	if joined := strings.Join(m.transcript, "\n"); !strings.Contains(joined, "second paragraph closes.") {
		t.Fatalf("a closed paragraph should paint at once, transcript=%v", m.transcript)
	}
}

// TestClearTranscriptDisplayClosesTheAnswerBlock guards the stale-index defect:
// a clear that leaves answerIdx pointing into a transcript it emptied makes the
// next chunk paint the answer over whatever block now sits at that index.
func TestClearTranscriptDisplayClosesTheAnswerBlock(t *testing.T) {
	m := newTestChatTUI()

	m.ingestEvent(event.Event{Kind: event.Text, Text: "an answer that is streaming"})
	if m.answerIdx != 0 {
		t.Fatalf("answerIdx = %d, want the live block at 0", m.answerIdx)
	}
	m.clearTranscriptDisplay()
	if m.answerIdx != -1 || m.answerFlushed != 0 || m.answerPainted != 0 {
		t.Fatalf("clear left the answer stream open: idx=%d flushed=%d painted=%d",
			m.answerIdx, m.answerFlushed, m.answerPainted)
	}
	// The next chunk reopens the block in the new transcript rather than writing
	// into the index the cleared one used to hold.
	m.commitLine("a line the replay installed")
	m.ingestEvent(event.Event{Kind: event.Text, Text: "resumed"})
	if m.answerIdx != len(m.transcript)-1 {
		t.Fatalf("answerIdx = %d, want the new block at %d", m.answerIdx, len(m.transcript)-1)
	}
	if line := m.transcript[0]; !strings.Contains(line, "a line the replay installed") {
		t.Fatalf("the cleared block was overwritten by the answer: %v", m.transcript)
	}
}
