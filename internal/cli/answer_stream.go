package cli

import (
	"strings"
	"time"
)

// The live answer stream: the answer is painted into one transcript block as it
// arrives. This file holds the parts of that block which are not the renderer:
// when it may be repainted, and how much of a half-arrived stream is safe.

// answerPaintInterval bounds how often an answer whose block has not closed is
// repainted. A repaint costs O(answer), so painting per chunk is quadratic in a
// long reply; one repaint per interval keeps the stream visibly live at a fixed
// share of the frame loop.
const answerPaintInterval = 50 * time.Millisecond

// streamAnswer paints the answer streamed so far into one transcript block,
// rewritten in place as text arrives, so a long reply appears chunk by chunk
// instead of all at once on turn end.
//
// The paint carries the whole streamed text (streamedMarkdownPreview), not only
// the completed blocks: waiting for a blank line left a paragraph, a list, or a
// long fence invisible for its whole duration and then dumped it in one jump. A
// block that has not closed repaints at most every answerPaintInterval.
func (m *chatTUI) streamAnswer() {
	if m.nativeScrollback {
		return
	}
	raw := m.pending.String()
	preview := streamedMarkdownPreview(raw)
	if len(preview) <= m.answerPainted {
		return
	}
	flushed := flushableMarkdownPrefix(raw)
	// A closed block repaints at once: that is where the text becomes stable, and
	// it keeps the pre-preview paint points intact.
	if len(flushed) <= m.answerFlushed && time.Since(m.answerPaintedAt) < answerPaintInterval {
		return
	}
	source := transcriptSource{kind: transcriptSourceMarkdown, raw: preview}
	m.answerFlushed = len(flushed)
	m.answerPainted = len(preview)
	m.answerPaintedAt = time.Now()
	if m.answerIdx < 0 {
		m.answerIdx = len(m.transcript)
		m.commitTranscriptSource(source)
		return
	}
	// setTranscriptBlock invalidates the wrap suffix from answerIdx so the next
	// Update only re-wraps the live answer block — not the full history.
	block := m.renderTranscriptSource(source, m.width)
	m.setTranscriptBlock(m.answerIdx, block, source)
}

// commitPending freezes the full accumulated answer as markdown — overwriting the
// streamed block if one is open (streamAnswer), else committing fresh. Joining
// commitReasoning then commitPending puts the answer on its own line, restoring
// the thinking→answer break the renderer strips.
func (m *chatTUI) commitPending() {
	if m.pending.Len() == 0 {
		m.resetAnswerStream()
		return
	}
	raw := m.pending.String()
	source := transcriptSource{kind: transcriptSourceMarkdown, raw: raw}
	if m.answerIdx < 0 {
		m.commitTranscriptSource(source)
	} else {
		block := m.renderTranscriptSource(source, m.width)
		m.setTranscriptBlock(m.answerIdx, block, source)
	}
	m.pending.Reset()
	m.resetAnswerStream()
}

// resetAnswerStream closes the live answer block. Every path that empties the
// answer buffer goes through here, so a stale index can never point into a
// transcript the block was not written to.
func (m *chatTUI) resetAnswerStream() {
	m.answerIdx = -1
	m.answerFlushed = 0
	m.answerPainted = 0
	m.answerPaintedAt = time.Time{}
}

// flushableMarkdownPrefix returns the longest prefix of buf made of complete
// markdown blocks — text up to the last blank line outside any open fenced code
// block. A blank line inside a ``` / ~~~ fence isn't a boundary, so a half-written
// code block stays buffered until it closes.
func flushableMarkdownPrefix(buf string) string {
	lines := strings.Split(buf, "\n")
	inFence := false
	boundary := -1
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
			continue
		}
		if !inFence && t == "" {
			boundary = i
		}
	}
	if boundary <= 0 {
		return ""
	}
	return strings.Join(lines[:boundary], "\n")
}

// streamedMarkdownPreview returns the prefix of a streaming answer that is safe
// to paint before the turn ends: the whole text, minus a trailing math span that
// has not closed. The renderer reads math one line at a time, so a half-arrived
// `$…` would paint as raw LaTeX and then swap to the rendered form; everything
// before it renders as it will stay.
func streamedMarkdownPreview(raw string) string {
	if raw == "" || strings.HasSuffix(raw, "\n") {
		return raw
	}
	line := raw[strings.LastIndexByte(raw, '\n')+1:]
	cut, open := openMathAt(line)
	if !open {
		return raw
	}
	return raw[:len(raw)-len(line)+cut]
}

// openMathAt reports where a line's math span is left open — the byte offset of
// an unmatched $ or $$ opener. A stray $ counts as an opener: holding its line's
// tail back until the meaning settles beats painting it twice.
func openMathAt(line string) (int, bool) {
	for i := 0; i < len(line); {
		if line[i] != '$' {
			i++
			continue
		}
		delim := "$"
		if i+1 < len(line) && line[i+1] == '$' {
			delim = "$$"
		}
		closeAt := strings.Index(line[i+len(delim):], delim)
		if closeAt < 0 {
			return i, true
		}
		i += len(delim) + closeAt + len(delim)
	}
	return 0, false
}
