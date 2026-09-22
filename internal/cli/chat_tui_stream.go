package cli

import (
	"fmt"
	"reasonix/internal/event"
	"reasonix/internal/i18n"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// finalize drains the committed-line queue and batches the turn's commands. In
// the default alt-screen path the queue is already mirrored in m.transcript. In
// Termux finalized lines are also emitted into the terminal's native scrollback.
func finalize(m chatTUI, cmds []tea.Cmd) tea.Cmd {
	if m.nativeScrollback && len(*m.pendingCommit) > 0 {
		out := strings.TrimRight(clampWidth(strings.Join(*m.pendingCommit, "\n"), m.width), "\n")
		*m.pendingCommit = (*m.pendingCommit)[:0]
		var prints []tea.Cmd
		for _, chunk := range chunkLines(out, m.scrollChunkHeight()) {
			prints = append(prints, tea.Println(chunk))
		}
		cmds = append(cmds, tea.Sequence(prints...))
		return tea.Batch(cmds...)
	}
	*m.pendingCommit = (*m.pendingCommit)[:0]
	return tea.Batch(cmds...)
}

func (m *chatTUI) clearTranscriptDisplay() {
	if m.pendingCommit != nil {
		*m.pendingCommit = (*m.pendingCommit)[:0]
	}
	m.transcript = nil
	m.transcriptSources = nil
	m.replay.reset() // the replay block goes with the transcript
	m.clearWrapCache()
	m.viewport.SetContent("")
	m.shellOutputs = make(map[string]string)
	m.shellExpanded = make(map[string]bool)
	m.shellTranscriptIdx = make(map[string]int)
	m.toolLineCountByID = make(map[string]int)
	m.subagentProgressIdx = make(map[string]int)
	m.subagentProgress = make(map[string]*cliSubagentProgress)
	m.toolStreamID = ""
	m.toolStreamIdx = -1
	m.toolTail = nil
	m.toolPartial = ""
	m.toolLineCount = 0
}

// scrollChunkHeight is the largest block (in lines) finalize prints at once in
// native-scrollback mode, leaving room for the pinned bottom frame.
func (m chatTUI) scrollChunkHeight() int {
	if m.height <= 0 {
		return 100
	}
	if n := m.height - m.bottomRows(); n > 1 {
		return n
	}
	return 1
}

// chunkLines splits s into blocks of at most n lines each, preserving order and
// line content. A single block is returned when it already fits.
func chunkLines(s string, n int) []string {
	if n < 1 {
		n = 1
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return []string{s}
	}
	var out []string
	for i := 0; i < len(lines); i += n {
		end := min(i+n, len(lines))
		out = append(out, strings.Join(lines[i:end], "\n"))
	}
	return out
}

// clampWidth hard-breaks any line wider than width so no scrollback line wraps
// in the terminal. bubbletea's inline renderer estimates how far to scroll for
// each printed block from each line's width (insertAbove: offset += width/w); an
// over-wide line that the terminal wraps throws that estimate off and drifts the
// pinned input box off-screen. Lines already within width are left byte-for-byte
// untouched (chunkByWidth preserves content and ANSI), so rendered tables and the
// wrapped answer — which the markdown renderer already fit to width — are safe;
// only stray long lines (tool-dispatch args, unwrapped code) get broken.
func clampWidth(s string, width int) string {
	if width <= 0 {
		return s
	}
	// ansi.Hardwrap breaks any line over `width` visible cols on grapheme
	// boundaries, preserving ANSI and counting wide chars — exactly what we want,
	// and lines already within width pass through unchanged.
	return ansi.Hardwrap(s, width, false)
}

// commitLine queues one finalized block for the next scrollback flush.
func (m *chatTUI) commitLine(s string) {
	*m.pendingCommit = append(*m.pendingCommit, s)
	m.appendTranscriptBlock(s, transcriptSource{kind: transcriptSourceFixed})
}

// commitSpacer separates the next block (a thinking marker or a tool line) from
// the previous one with a single blank line, skipping it at the top of the
// transcript or when a blank already trails so spacers never double up.
func (m *chatTUI) commitSpacer() {
	if n := len(m.transcript); n > 0 && strings.TrimSpace(m.transcript[n-1]) != "" {
		m.commitLine("")
	}
}

// hideComposer is the single ownership gate for the bottom composer.
//
// Rule for new CLI panels:
//   - If a panel is modal and keystrokes navigate/confirm/cancel the panel, hide
//     the composer so users do not see an inactive chat input.
//   - If a panel is input-owned (autocomplete, or chooser free-text mode), keep
//     the composer visible because the textarea is the active control.
//
// Whenever a new slash-command overlay or approval-style prompt is added, update
// this function and the modal layout tests together. Otherwise the panel may
// reserve rows for a composer that cannot receive input, leaving a confusing
// blank/bordered area at the bottom of the TUI.
func (m chatTUI) hideComposer() bool {
	if m.mcp != nil || m.clearConfirm != nil || m.mcpImport != nil || m.skillPick != nil || m.resumePick != nil || m.quickPick != nil || m.setup != nil || m.copyPick != nil || m.teamOverlayModal() || m.rewind != nil || m.pendingApproval != nil {
		return true
	}
	return (m.chooser != nil && !m.chooser.typing) || (m.elicit != nil && !m.elicit.typing)
}

// transcriptHeight is the row budget left for the transcript viewport once the
// pinned bottom region is accounted for (at least one row).
func (m chatTUI) transcriptHeight() int {
	if h := m.height - m.bottomRows(); h > 1 {
		return h
	}
	return 1
}

// reasoningViewMax bounds the live thinking buffer the streamed block renders
// from. Re-rendering the full chain of thought on every delta was O(n²) (a 2k-
// token thought churned ~4.7GB); rendering only the trailing window keeps each
// delta O(1). The full text still lives in m.reasoning for verbose mode.
const reasoningViewMax = 4096

// reasoningTailLines caps how many trailing visual lines the live block shows.
const reasoningTailLines = 12

// streamReasoning appends a chunk and rewrites the live reasoning block from a
// bounded trailing view (mirrors streamToolOutput), so the chain of thought is
// visible while the model works without re-rendering the whole thing per token.
func (m *chatTUI) streamReasoning(chunk string) {
	m.reasoning.WriteString(chunk) // full text retained for verbose mode
	if m.reasoningTextIdx < 0 {
		return
	}
	m.reasoningView = append(m.reasoningView, chunk...)
	if len(m.reasoningView) > reasoningViewMax {
		drop := len(m.reasoningView) - reasoningViewMax
		for drop < len(m.reasoningView) && !utf8.RuneStart(m.reasoningView[drop]) {
			drop++
		}
		m.reasoningView = m.reasoningView[:copy(m.reasoningView, m.reasoningView[drop:])]
	}
	raw := string(m.reasoningView)
	m.setTranscriptBlock(m.reasoningTextIdx, reasoningBlock(raw, m.width, reasoningTailLines), transcriptSource{
		kind: transcriptSourceReasoning, raw: raw, maxLines: reasoningTailLines,
	})
}

// reasoningBlock renders raw thinking text as dim, width-wrapped lines under a
// "⎿" connector that ties the block to the "▎ thinking…" marker above it. A
// positive maxLines keeps only the trailing visual lines (the live view); 0
// renders all (verbose collapse).
func reasoningBlock(raw string, width, maxLines int) string {
	return connectorBlock(reasoningBlockLines(raw, width, maxLines))
}

// toolStreamTailLines caps how many trailing output lines a running tool shows;
// the live block scrolls within this window so a chatty build doesn't flood.
const toolStreamTailLines = 8

// shellPreviewLines is how many lines of shell output to show by default after
// the command finishes. Ctrl+B toggles the full output.
const shellPreviewLines = 10

// shellExpandMaxLines caps how many lines Ctrl+B shows in expanded mode, so a
// very large output (e.g. thousands of lines) doesn't hang the TUI or push the
// input box off-screen.
const shellExpandMaxLines = 200

// pushToolLine appends a completed output line to the bounded tail, dropping the
// oldest when it exceeds the window (the backing array stays ≤ window+1).
func (m *chatTUI) pushToolLine(line string) {
	m.toolLineCount++
	m.toolTail = append(m.toolTail, line)
	if len(m.toolTail) > toolStreamTailLines {
		copy(m.toolTail, m.toolTail[1:])
		m.toolTail = m.toolTail[:toolStreamTailLines]
	}
}

// subagentPreviewMax bounds each child's retained reasoning/text preview tail
// (verbose mode renders from it); the notice tail is smaller.
const (
	subagentPreviewMax = 4096
	subagentNoticeMax  = 2048
	// subagentPreviewTailLines caps the trailing visual lines of a preview.
	subagentPreviewTailLines = 12
)

// cliSubagentProgress is the per-child live state backing one fixed transcript
// slot. Everything here is in-memory only: the persisted sub-agent transcript
// remains the source of truth after a restart.
type cliSubagentProgress struct {
	phase            string
	startedAt        time.Time
	lastActive       time.Time
	reasoning        string // bounded ≤ subagentPreviewMax, UTF-8-safe tail
	text             string
	notice           string
	truncated        bool
	durationMs       int64
	terminal         bool
	lastPrintedPhase string // native-scrollback dedupe
	verboseLastPrint time.Time
}

func subagentPhaseTerminal(phase string) bool {
	switch phase {
	case "completed", "failed", "cancelled":
		return true
	}
	return false
}

// cliPreviewTail keeps the most recent maxBytes of s at a rune boundary.
func cliPreviewTail(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	s = s[len(s)-maxBytes:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

// streamSubagentProgress routes reserved ToolProgress channels into per-child
// progress state instead of the single live tool stream: each child keeps its
// own phase, elapsed, recent activity, and (verbose-only) preview tails.
func (m *chatTUI) streamSubagentProgress(t event.Tool) {
	if t.ID == "" {
		return
	}
	sp := m.subagentProgress[t.ID]
	if sp == nil {
		sp = &cliSubagentProgress{startedAt: time.Now()}
		m.subagentProgress[t.ID] = sp
	}
	sp.lastActive = time.Now()
	switch t.Name {
	case event.SubagentProgressStatusName:
		sp.phase = t.Output
		if t.DurationMs > 0 {
			sp.durationMs = t.DurationMs
		}
		sp.terminal = subagentPhaseTerminal(t.Output)
		m.renderSubagentProgress(t.ID)
	case event.SubagentProgressReasoningName:
		sp.reasoning = cliPreviewTail(sp.reasoning+t.Output, subagentPreviewMax)
		sp.truncated = sp.truncated || t.Truncated
		if m.showReasoning {
			m.renderSubagentProgress(t.ID)
		}
	case event.SubagentProgressTextName:
		sp.text = cliPreviewTail(sp.text+t.Output, subagentPreviewMax)
		sp.truncated = sp.truncated || t.Truncated
		if m.showReasoning {
			m.renderSubagentProgress(t.ID)
		}
	case event.SubagentProgressNoticeName:
		sp.notice = cliPreviewTail(sp.notice+t.Output, subagentNoticeMax)
		sp.truncated = sp.truncated || t.Truncated
		if m.showReasoning {
			m.renderSubagentProgress(t.ID)
		}
	}
}

// renderSubagentProgress redraws a child's progress block. Alt-screen TUIs
// rewrite the fixed transcript slot in place (created on first sight under the
// current transcript end); native-scrollback terminals print a status line
// only on phase changes and terminal, since printed output cannot be rewritten.
func (m *chatTUI) renderSubagentProgress(id string) {
	sp := m.subagentProgress[id]
	if sp == nil || sp.phase == "" {
		return
	}
	if m.nativeScrollback {
		m.printSubagentProgressScrollback(id, sp)
		return
	}
	idx, ok := m.subagentProgressIdx[id]
	if !ok {
		idx = len(m.transcript)
		m.subagentProgressIdx[id] = idx
		m.commitLine(m.subagentProgressBlock(id, sp))
		return
	}
	m.setTranscriptBlock(idx, m.subagentProgressBlock(id, sp), transcriptSource{kind: transcriptSourceSubagentProgress, raw: id})
}

// tickSubagentProgress refreshes the elapsed / recent-activity fields of live
// progress blocks once a second (mirrors tickToolRunning), so a child that
// produces no events still reads as alive.
func (m *chatTUI) tickSubagentProgress() {
	if m.nativeScrollback {
		return
	}
	for id, sp := range m.subagentProgress {
		if sp.terminal || sp.phase == "" {
			continue
		}
		idx, ok := m.subagentProgressIdx[id]
		if !ok {
			continue
		}
		m.setTranscriptBlock(idx, m.subagentProgressBlock(id, sp), transcriptSource{kind: transcriptSourceSubagentProgress, raw: id})
	}
}

// subagentProgressBlock renders one child's progress block. The default line
// shows phase, running elapsed, and recent activity; verbose mode adds the
// bounded reasoning/text/notice tails above it. Terminal children collapse to
// a one-line summary (the preview survives in memory for verbose re-render).
func (m *chatTUI) subagentProgressBlock(id string, sp *cliSubagentProgress) string {
	var lines []string
	if m.showReasoning {
		if sp.reasoning != "" {
			lines = append(lines, subagentPreviewBlock(i18n.M.ChatSubagentPreviewLabel, sp.reasoning, m.width, subagentPreviewTailLines))
		}
		if sp.text != "" {
			lines = append(lines, subagentPreviewBlock("✎", sp.text, m.width, subagentPreviewTailLines))
		}
		if sp.notice != "" {
			lines = append(lines, subagentPreviewBlock("!", sp.notice, m.width, subagentPreviewTailLines))
		}
		if sp.truncated {
			lines = append(lines, dim("… preview truncated"))
		}
	}
	label := subagentPhaseLabel(sp.phase)
	switch sp.phase {
	case "completed":
		label = green(label + " ✓")
	case "failed":
		label = red(label + " ✗")
	case "cancelled":
		label = dim(label + " ⊘")
	case "retrying":
		label = yellow(label)
	}
	if sp.terminal {
		secs := sp.durationMs / 1000
		if secs <= 0 && !sp.startedAt.IsZero() {
			secs = int64(time.Since(sp.startedAt).Seconds())
		}
		lines = append(lines, fmt.Sprintf(i18n.M.ChatSubagentProgressDoneFmt, label, secs))
	} else {
		elapsed := int64(0)
		idle := int64(0)
		if !sp.startedAt.IsZero() {
			elapsed = int64(time.Since(sp.startedAt).Seconds())
		}
		if !sp.lastActive.IsZero() {
			idle = int64(time.Since(sp.lastActive).Seconds())
		}
		lines = append(lines, fmt.Sprintf(i18n.M.ChatSubagentProgressFmt, label, elapsed, idle))
	}
	return connectorBlock(lines)
}

// subagentPreviewBlock renders a bounded trailing window of a preview channel
// as dim, width-wrapped lines carrying a small glyph marker.
func subagentPreviewBlock(glyph, raw string, width, maxLines int) string {
	w := max(width-len([]rune(connector)), 8)
	var lines []string
	first := true
	for ln := range strings.SplitSeq(strings.TrimRight(raw, "\n"), "\n") {
		if first {
			ln = glyph + " " + ln
			first = false
		}
		for wl := range strings.SplitSeq(ansi.Wrap(expandTabs(ln), w, ""), "\n") {
			lines = append(lines, dim(wl))
		}
	}
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

// subagentPhaseLabel maps a reserved status value to its localized label.
func subagentPhaseLabel(phase string) string {
	switch phase {
	case "queued":
		return i18n.M.ChatSubagentPhaseQueued
	case "running":
		return i18n.M.ChatSubagentPhaseRunning
	case "reasoning":
		return i18n.M.ChatSubagentPhaseReasoning
	case "responding":
		return i18n.M.ChatSubagentPhaseResponding
	case "tool":
		return i18n.M.ChatSubagentPhaseTool
	case "retrying":
		return i18n.M.ChatSubagentPhaseRetrying
	case "completed":
		return i18n.M.ChatSubagentPhaseCompleted
	case "failed":
		return i18n.M.ChatSubagentPhaseFailed
	case "cancelled":
		return i18n.M.ChatSubagentPhaseCancelled
	}
	return phase
}

// printSubagentProgressScrollback queues a status line for native-scrollback
// terminals, which cannot rewrite printed output (finalized blocks drain via
// pendingCommit like every other scrollback commit): status lines appear on
// phase changes and terminal only, and verbose previews are throttled to at
// most one print per 2s per child.
func (m *chatTUI) printSubagentProgressScrollback(id string, sp *cliSubagentProgress) {
	if m.pendingCommit == nil {
		return
	}
	block := m.subagentProgressBlock(id, sp)
	if sp.terminal {
		*m.pendingCommit = append(*m.pendingCommit, block)
		sp.lastPrintedPhase = sp.phase
		sp.verboseLastPrint = time.Now()
		return
	}
	if sp.phase != sp.lastPrintedPhase {
		*m.pendingCommit = append(*m.pendingCommit, block)
		sp.lastPrintedPhase = sp.phase
		sp.verboseLastPrint = time.Now()
		return
	}
	if m.showReasoning && time.Since(sp.verboseLastPrint) >= 2*time.Second && (sp.reasoning != "" || sp.text != "" || sp.notice != "") {
		*m.pendingCommit = append(*m.pendingCommit, block)
		sp.verboseLastPrint = time.Now()
	}
}

// collapseToolOutput replaces a finished tool's live block with a dim
// "⎿ N lines" summary, so the scrollback keeps a marker of the run without the
// full output (which the model already received). For shell commands ("shell-"
// prefix), it shows the first shellPreviewLines with a Ctrl+B hint instead.
// No-op when id isn't streaming. resultOutput (the ToolResult's final output)
// is the last-resort line-count source when the live state was already reset.
func (m *chatTUI) collapseToolOutput(id, resultOutput string) {
	if m.nativeScrollback {
		if id == "" || m.toolStreamID != id {
			return
		}
		n := m.toolLineCount
		if m.toolPartial != "" {
			n++
		}
		if n > 0 {
			if full, ok := m.shellOutputs[id]; ok {
				lines := strings.Split(strings.TrimRight(full, "\n"), "\n")
				total := len(lines)
				if total > shellPreviewLines {
					preview := make([]string, shellPreviewLines+1)
					for i := range shellPreviewLines {
						preview[i] = dim(clampPlain(lines[i], m.width-len([]rune(connector))))
					}
					preview[shellPreviewLines] = dim(fmt.Sprintf("… %d more lines (Ctrl+B)", total-shellPreviewLines))
					m.commitConnectorBlock(preview)
				} else {
					rendered := make([]string, total)
					for i, ln := range lines {
						rendered[i] = dim(clampPlain(ln, m.width-len([]rune(connector))))
					}
					m.commitConnectorBlock(rendered)
				}
				m.shellTranscriptIdx[id] = len(m.transcript) - 1
			} else {
				m.commitConnectorBlock([]string{dim(fmt.Sprintf("%d lines", n))})
			}
		}
		m.toolStreamIdx = -1
		m.toolStreamID = ""
		m.toolTail = m.toolTail[:0]
		m.toolPartial = ""
		m.toolLineCount = 0
		return
	}
	if m.toolStreamIdx < 0 || id == "" || m.toolStreamID != id {
		// Slot no longer active (another tool took over, or this id never
		// streamed). If beginToolRunning recorded a transcript index, collapse
		// in place so a late ToolResult doesn't leave raw streamed text behind.
		if idx, ok := m.shellTranscriptIdx[id]; ok && idx >= 0 && idx < len(m.transcript) {
			m.collapseShellSlot(id, idx, resultOutput)
		}
		return
	}
	m.collapseShellSlot(id, m.toolStreamIdx, resultOutput)
	m.toolStreamIdx = -1
	m.toolStreamID = ""
	m.toolTail = m.toolTail[:0]
	m.toolPartial = ""
	m.toolLineCount = 0
}

// collapseShellSlot finalises a tool's live block at idx. Used both by the
// active-tool path (idx == toolStreamIdx, streaming state intact) and the
// late-result path (idx recorded in shellTranscriptIdx at dispatch). Line-count
// sources, in order: live streaming state, shellOutputs ("shell-" ids only),
// the per-id count stashed by streamToolOutput, then the ToolResult's output.
func (m *chatTUI) collapseShellSlot(id string, idx int, resultOutput string) {
	m.transcriptDirty = true
	n := -1
	if id == m.toolStreamID {
		// Prefer the larger of the live count and resultOutput: resultOutput
		// is the authoritative end-state, the live state may lag behind it.
		n = m.toolLineCount
		if m.toolPartial != "" {
			n++
		}
		if resultOutput != "" {
			fromResult := len(strings.Split(strings.TrimRight(resultOutput, "\n"), "\n"))
			if fromResult > n {
				n = fromResult
			}
		}
	}
	if n < 0 {
		if full, ok := m.shellOutputs[id]; ok {
			n = len(strings.Split(strings.TrimRight(full, "\n"), "\n"))
		} else if c, ok := m.toolLineCountByID[id]; ok {
			n = c
		} else if resultOutput != "" {
			n = len(strings.Split(strings.TrimRight(resultOutput, "\n"), "\n"))
		}
	}
	if n < 0 {
		// Nothing applies (e.g. a late result for a non-"shell-" id that never
		// streamed): treat as zero rather than fabricate a "-1 lines" count.
		n = 0
	}
	if n == 0 {
		// Tool finished with no output: clear the "working…" placeholder but
		// keep the slot (shellTranscriptIdx still points here for late progress).
		m.rewriteConnectorBlock(idx, nil)
		return
	}
	if full, ok := m.shellOutputs[id]; ok {
		// Shell command: show first N lines + hint.
		lines := strings.Split(strings.TrimRight(full, "\n"), "\n")
		total := len(lines)
		if total > shellPreviewLines {
			preview := make([]string, shellPreviewLines+1)
			for i := range shellPreviewLines {
				preview[i] = dim(clampPlain(lines[i], m.width-len([]rune(connector))))
			}
			preview[shellPreviewLines] = dim(fmt.Sprintf("… %d more lines (Ctrl+B)", total-shellPreviewLines))
			m.rewriteConnectorBlock(idx, preview)
		} else {
			rendered := make([]string, total)
			for i, ln := range lines {
				rendered[i] = dim(clampPlain(ln, m.width-len([]rune(connector))))
			}
			m.rewriteConnectorBlock(idx, rendered)
		}
	} else {
		m.rewriteConnectorBlock(idx, []string{dim(fmt.Sprintf("%d lines", n))})
	}
	m.shellTranscriptIdx[id] = idx
}

// toggleShellOutput expands or collapses the output of the most recent shell
// command. When expanded, up to shellExpandMaxLines lines are shown; when
// collapsed, only the first shellPreviewLines are shown. Called on Ctrl+B.
func (m *chatTUI) toggleShellOutput() {
	// Find the most recent shell output that has a transcript entry.
	var lastID string
	lastIdx := -1
	for id, idx := range m.shellTranscriptIdx {
		if idx >= 0 && idx < len(m.transcript) && idx > lastIdx {
			lastID = id
			lastIdx = idx
		}
	}
	if lastID == "" {
		return
	}
	full, ok := m.shellOutputs[lastID]
	if !ok {
		return
	}
	lines := strings.Split(strings.TrimRight(full, "\n"), "\n")
	total := len(lines)
	innerW := m.width - len([]rune(connector))
	if innerW < 10 {
		innerW = 80
	}

	if m.shellExpanded[lastID] {
		// Collapse back to preview.
		m.shellExpanded[lastID] = false
		if total > shellPreviewLines {
			preview := make([]string, shellPreviewLines+1)
			for i := range shellPreviewLines {
				preview[i] = dim(clampPlain(lines[i], innerW))
			}
			preview[shellPreviewLines] = dim(fmt.Sprintf("… %d more lines (Ctrl+B)", total-shellPreviewLines))
			m.rewriteConnectorBlock(lastIdx, preview)
		}
	} else {
		// Expand: show up to shellExpandMaxLines lines.
		m.shellExpanded[lastID] = true
		show := min(total, shellExpandMaxLines)
		rendered := make([]string, show)
		for i := range show {
			rendered[i] = dim(clampPlain(lines[i], innerW))
		}
		if total > shellExpandMaxLines {
			rendered = append(rendered, dim(fmt.Sprintf("… %d more lines", total-shellExpandMaxLines)))
		}
		m.rewriteConnectorBlock(lastIdx, rendered)
	}
	if m.nativeScrollback {
		m.commitLine(m.transcript[lastIdx])
	}
}

// toolWorkingFrames is the braille spinner cycled once per second on the
// "⎿ working · Ns" line of a tool that hasn't streamed output yet.
var toolWorkingFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// tickToolRunning re-renders the working line of a tool that's dispatched but
// hasn't produced output yet. A no-op once output streams in or no tool runs.
func (m *chatTUI) tickToolRunning() {
	if m.nativeScrollback {
		return
	}
	if m.toolStreamIdx < 0 || m.toolLineCount != 0 || m.toolPartial != "" {
		return
	}
	m.toolStreamFrame++
	frame := toolWorkingFrames[m.toolStreamFrame%len(toolWorkingFrames)]
	secs := int(time.Since(m.toolStreamStart).Seconds())
	m.rewriteConnectorBlock(m.toolStreamIdx, []string{dim(fmt.Sprintf(i18n.M.ChatToolWorkingFmt, frame, secs))})
}

// commitReasoning closes the live thinking block: the "▎ thinking…" marker is
// rewritten to a dim "▎ thought for Ns" summary and the streamed text below it is
// removed (collapsed) — kept only in verbose mode. The viewport re-wraps from
// m.transcript, so the change is flagged via transcriptDirty.
func (m *chatTUI) commitReasoning() {
	if m.reasoningNative {
		if strings.TrimSpace(m.reasoning.String()) != "" || !m.thinkStart.IsZero() {
			secs := int(time.Since(m.thinkStart).Seconds())
			m.commitSpacer()
			m.commitLine(dim(fmt.Sprintf("  ▎ "+i18n.M.ChatThoughtForFmt, secs)))
			if m.showReasoning && strings.TrimSpace(m.reasoning.String()) != "" {
				m.commitLine(reasoningBlock(m.reasoning.String(), m.width, 0))
			}
		}
		m.reasoning.Reset()
		m.reasoningView = m.reasoningView[:0]
		m.reasoningNative = false
		m.thinkStart = time.Time{}
		return
	}
	if m.reasoningLineIdx < 0 {
		return
	}
	secs := int(time.Since(m.thinkStart).Seconds())
	m.setTranscriptBlock(m.reasoningLineIdx, dim(fmt.Sprintf("  ▎ "+i18n.M.ChatThoughtForFmt, secs)), transcriptSource{kind: transcriptSourceFixed})
	if m.reasoningTextIdx >= 0 {
		if m.showReasoning && strings.TrimSpace(m.reasoning.String()) != "" {
			raw := m.reasoning.String()
			m.setTranscriptBlock(m.reasoningTextIdx, reasoningBlock(raw, m.width, 0), transcriptSource{
				kind: transcriptSourceReasoning, raw: raw,
			})
		} else {
			m.removeTranscriptBlock(m.reasoningTextIdx)
		}
	}
	m.transcriptDirty = true
	m.reasoning.Reset()
	m.reasoningView = m.reasoningView[:0]
	m.reasoningLineIdx = -1
	m.reasoningTextIdx = -1
}

// commitReasoningBeforeAnswer closes a real reasoning block and leaves exactly
// one blank transcript row before the assistant answer. Answers that start
// without reasoning keep their existing compact placement.
func (m *chatTUI) commitReasoningBeforeAnswer() {
	hadReasoning := m.reasoningNative || m.reasoningLineIdx >= 0
	m.commitReasoning()
	if hadReasoning {
		m.commitSpacer()
	}
}

// streamAnswer renders the answer streamed so far up to its last completed
// paragraph (flushableMarkdownPrefix) and writes it as one transcript block,
// rewritten in place as later paragraphs land — so a long reply appears chunk by
// chunk instead of all at once on turn end. The trailing, still-streaming block
// stays buffered (a half-written fence/list never renders early), and it only
// re-renders when a new paragraph actually closes.
func (m *chatTUI) streamAnswer() {
	if m.nativeScrollback {
		return
	}
	prefix := flushableMarkdownPrefix(m.pending.String())
	if len(prefix) <= m.answerFlushed {
		return
	}
	source := transcriptSource{kind: transcriptSourceMarkdown, raw: prefix}
	m.answerFlushed = len(prefix)
	if m.answerIdx < 0 {
		m.answerIdx = len(m.transcript)
		m.commitTranscriptSource(source)
	} else {
		// setTranscriptBlock invalidates the wrap suffix from answerIdx so the
		// next Update only re-wraps the live answer block — not the full history.
		block := m.renderTranscriptSource(source, m.width)
		m.setTranscriptBlock(m.answerIdx, block, source)
	}
}

// commitPending freezes the full accumulated answer as markdown — overwriting the
// streamed block if one is open (streamAnswer), else committing fresh. Joining
// commitReasoning then commitPending puts the answer on its own line, restoring
// the thinking→answer break the renderer strips.
func (m *chatTUI) commitPending() {
	if m.pending.Len() == 0 {
		m.answerIdx = -1
		m.answerFlushed = 0
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
	m.answerIdx = -1
	m.answerFlushed = 0
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

// finalizeStreamed freezes any in-progress reasoning + answer into scrollback so
// a following event line lands after them, preserving chronological order.
func (m *chatTUI) finalizeStreamed() {
	m.collapseToolOutput(m.toolStreamID, "")
	m.commitReasoning()
	m.commitPending()
}
