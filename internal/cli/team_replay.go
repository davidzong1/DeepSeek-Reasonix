package cli

import (
	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/provider"
)

// Member transcript replay, off the Update goroutine (see commitBackendReplay).

// replayInlineMessages is the longest history painted in one synchronous pass,
// and the window a longer history shows while the rest renders.
const replayInlineMessages = 24

// replayPaint is one rendered replay bundle together with the wrap the viewport
// will consume, so installing it costs no rendering at all.
type replayPaint struct {
	rendered string
	wrapped  []string
	contentW int
	// raw and history are what the rendered text was built from.
	raw     string
	history []provider.Message
	// sourceHistory is the history the block's source keeps, set only by a windowed
	// paint: the block shows the newest window while the source stays the member's
	// whole history, so a reflow stays bounded and a copy yields all of it.
	sourceHistory []provider.Message
	// windowed marks a paint of the bounded member bind: a frame paints the newest
	// window of sourceHistory, not all of it. Both paints of that machinery carry it.
	windowed bool
}

// replayPaintFor renders and wraps one history as a replay bundle. It is a
// function of its arguments alone so it can run off the Update goroutine, and it
// reads the active palette, so it holds the theme materialization read lock for
// its duration and a palette change cannot rewrite it mid-render (theme.go).
func replayPaintFor(label, raw string, history []provider.Message, terminalWidth int, nativeScrollback bool) replayPaint {
	contentW := transcriptContentWidth(terminalWidth, nativeScrollback)
	var rendered string
	var wrapped []string
	withThemeMaterialized(func() {
		rendered = renderReplayBundleBlock(label, raw, history, contentW, renderAssistantMarkdown, reasoningBlock)
		wrapped = wrapBlockLines(rendered, contentW)
	})
	return replayPaint{rendered: rendered, wrapped: wrapped, contentW: contentW, raw: raw, history: history}
}

// replayLoad is the full-bundle render still in flight for the bound member.
// gen identifies it: a later bind, or a second render of the same history at a
// new width, supersedes the one before it.
type replayLoad struct {
	gen     uint64
	raw     string
	history []provider.Message
	// pin asks the install to keep the frame on the newest output, which is where
	// a just-bound member's transcript belongs. A resize does not pin: the terminal
	// changed width under a user who may have scrolled up to read the window.
	pin bool
	// windowed says the block paints a window of history and re-arms a full render
	// on a resize (replayLoad.history is then the whole history).
	windowed bool
	// reading marks a load whose history has not been read yet: the bind handed the
	// read to a command, and nothing is painted until it lands (handleReplayReadReady).
	// history is nil while it is set.
	reading bool
}

// replayWindow is the window's member-replay state: where the bound backend's
// replay bundle sits in the transcript, and the full render still in flight for
// it. Zero value means "no bundle tracked, nothing pending".
type replayWindow struct {
	block int
	load  *replayLoad
	gen   uint64
}

// reset forgets the tracked replay block and any render in flight for it: the
// transcript they belong to is going away (clearTranscriptDisplay). The
// generation keeps counting, so a render already in flight cannot land in the
// next display's block.
func (w *replayWindow) reset() {
	w.block = -1
	w.load = nil
}

// teamReplayReadyMsg carries one off-goroutine replay render back to the loop.
type teamReplayReadyMsg struct {
	gen   uint64
	width int
	paint replayPaint
}

// replayMode selects how a backend bind paints the incoming transcript: bounded
// renders the rest of a long history off the loop, inline does not.
type replayMode uint8

const (
	// replayInline renders the whole history before the bind returns, which is
	// what a path that cannot hand a command back to the Update loop must use.
	replayInline replayMode = iota
	// replayBounded paints the newest messages inline and renders the rest off
	// the Update goroutine. The read stays inline: a bind must show the incoming
	// member's transcript before it returns, so it cannot clear the window and
	// wait for a read to fill it.
	replayBounded
	// replayDeferred reads the backend's history off the Update goroutine and
	// paints only when it lands. This is the refresh path — the window already
	// shows this member, so it repaints in place instead of blanking, and the
	// read is what the frame must not pay: a follower re-reads its durable source
	// on every call, and a peer's append reaches this path through the tick.
	replayDeferred
)

// commitBackendReplay rebuilds the transcript for a backend the window is
// binding, keeping the synchronous part bounded.
//
// The replay bundle is rendered — then wrapped — as one document, so rendering
// it on the Update goroutine freezes the frame for as long as a long-running
// member's whole history takes; that is why switching across a team of members
// felt stuck (decoupling plan §36). Let the bound be per frame instead: a
// history that fits replayInlineMessages renders inline exactly as before, and
// a longer one commits its newest window and hands back a render of the whole
// bundle, done on a goroutine that also wraps it at the same content width.
// installWrappedBlock adopts that wrap, so neither the markdown pass nor the
// wrap ever runs on the Update goroutine. The window is real history, not a
// placeholder, so a paint whose bundle never arrives still shows a transcript.
//
// The read is the remaining half (replayDeferred): bounding the paint still left
// backend.History() on this goroutine, and for a follower that read is O(history)
// off disk, so the frame still stalled — on every refresh, not just a bind. The
// refresh path therefore reads off the loop; a bind reads inline, because it
// cannot show a stale transcript and must not blank the window to wait.
func (m *chatTUI) commitBackendReplay(backend control.SessionAPI, mode replayMode) tea.Cmd {
	label, raw := m.label, ""
	if mode == replayDeferred {
		m.replay.gen++
		load := replayLoad{gen: m.replay.gen, raw: raw, pin: true, windowed: true, reading: true}
		m.replay.load = &load
		return m.readReplayCmd(backend, load.gen)
	}
	history := backend.History()
	if mode == replayInline || len(history) <= replayInlineMessages {
		m.replay.load = nil
		m.installReplayPaint(replayPaintFor(label, raw, history, m.width, m.nativeScrollback))
		return nil
	}
	// History order is oldest first, so the window is what the member is doing
	// now — the part the user reads while the rest renders above it.
	tail := history[len(history)-replayInlineMessages:]
	paint := replayPaintFor(label, raw, tail, m.width, m.nativeScrollback)
	paint.sourceHistory = history
	paint.windowed = true
	m.installReplayPaint(paint)
	m.replay.gen++
	load := &replayLoad{gen: m.replay.gen, raw: raw, history: history, pin: true, windowed: true}
	m.replay.load = load
	return m.renderFullReplayCmd(*load)
}

// replayReadReady carries one off-goroutine transcript read back to the loop.
// The history is width-independent, so no width rides along: the paint that
// follows is rendered at whatever width the window has when it lands, which is
// the width it will be shown at.
type replayReadReady struct {
	gen     uint64
	history []provider.Message
}

// readReplayCmd reads the backend's history off the Update goroutine and hands it
// back through the roster tick's own message, the way the full render does.
func (m *chatTUI) readReplayCmd(backend control.SessionAPI, gen uint64) tea.Cmd {
	return func() tea.Msg {
		return teamRosterRefreshMsg{read: &replayReadReady{gen: gen, history: backend.History()}}
	}
}

// handleReplayReadReady paints a transcript whose history the frame did not read.
// The clear and the install happen together here, so a refreshed transcript never
// shows an empty frame between them, and a read whose refresh the window has moved
// past is dropped rather than painted over the bound member.
func (m *chatTUI) handleReplayReadReady(msg replayReadReady) tea.Cmd {
	load := m.replay.load
	if load == nil || !load.reading || load.gen != msg.gen {
		return nil // a later bind owns the transcript now
	}
	history := msg.history
	label, raw := m.label, load.raw
	// The stream state is reset with the install, not only when the read was
	// armed: a delta that arrived while the read was in flight is superseded by
	// the durable history this paints, and leaving it pending would render it on
	// top of the replayed transcript as a duplicate.
	m.pending.Reset()
	m.reasoning.Reset()
	m.clearTranscriptDisplay()
	m.sessionSwitch = true
	m.transcriptDirty = true
	m.forceGotoBottom = true
	if len(history) <= replayInlineMessages {
		m.installReplayPaint(replayPaintFor(label, raw, history, m.width, m.nativeScrollback))
		return nil
	}
	// History order is oldest first, so the window is what the member is doing
	// now — the part the user reads while the rest renders above it.
	tail := history[len(history)-replayInlineMessages:]
	paint := replayPaintFor(label, raw, tail, m.width, m.nativeScrollback)
	paint.sourceHistory = history
	paint.windowed = true
	m.installReplayPaint(paint)
	m.replay.gen++
	next := &replayLoad{gen: m.replay.gen, raw: raw, history: history, pin: true, windowed: true}
	m.replay.load = next
	return m.renderFullReplayCmd(*next)
}

// reflowReplayBundle re-arms the off-loop render after the terminal changed
// width, which is the other half of the bounded bind: reflowTranscript repaints
// the block from its newest window, so the frame never re-renders a long member
// history inline on a resize either, and the whole bundle follows as a paint the
// way it does after a bind. A history that already fits the window is complete
// and arms nothing.
//
// The render it arms lands on a later message, by which point the frame's own
// wrap sync has moved the cache to the new width — so the wrap the paint adopts
// is the one that frame would otherwise have rebuilt itself.
func (m *chatTUI) reflowReplayBundle() tea.Cmd {
	idx := m.replay.block
	if idx < 0 || idx >= len(m.transcript) || idx >= len(m.transcriptSources) {
		return nil
	}
	source := m.transcriptSources[idx]
	if source.kind != transcriptSourceReplayBundle || !source.windowed || len(source.history) <= replayInlineMessages {
		return nil
	}
	m.replay.gen++
	load := replayLoad{gen: m.replay.gen, raw: source.raw, history: source.history, windowed: true}
	m.replay.load = &load
	return m.renderFullReplayCmd(load)
}

// replayWindowHistory is the newest span of a member's history that a
// synchronous paint carries, and what a reflow repaints from.
func replayWindowHistory(history []provider.Message) []provider.Message {
	if len(history) <= replayInlineMessages {
		return history
	}
	return history[len(history)-replayInlineMessages:]
}

// renderReplayBundleWindow renders the part of a member's replay block a frame
// can afford inline: the newest window, exactly as a bind paints it. This is the
// block's reflow path, so a resize is bounded the same way a bind is
// (reflowReplayBundle schedules the rest). Copying does not come through here —
// it renders the source's whole history (renderReplayBundleCopy).
func (m chatTUI) renderReplayBundleWindow(source transcriptSource, contentWidth int) string {
	return renderReplayBundleBlock(m.label, source.raw, replayWindowHistory(source.history), contentWidth, renderAssistantMarkdown, reasoningBlock)
}

// renderFullReplayCmd renders one load's whole history off the Update goroutine.
// The result rides the roster tick's own message, the way the tick's durable
// history read does: a bind result is not a tick, and the loop keeps one branch
// for both (the tick that armed this render arms the next one, so delivering a
// result must not arm a second tick).
func (m *chatTUI) renderFullReplayCmd(load replayLoad) tea.Cmd {
	label, raw, width, native := m.label, load.raw, m.width, m.nativeScrollback
	history, gen := load.history, load.gen
	return func() tea.Msg {
		ready := teamReplayReadyMsg{gen: gen, width: width, paint: replayPaintFor(label, raw, history, width, native)}
		ready.paint.windowed = load.windowed
		return teamRosterRefreshMsg{replay: &ready}
	}
}

// handleTeamReplayReady installs a bundle that was rendered off the loop, if the
// window is still the one that asked for it. Every rejection is silent: the
// transcript it would replace is a valid one either way.
func (m *chatTUI) handleTeamReplayReady(msg teamReplayReadyMsg) tea.Cmd {
	load := m.replay.load
	if load == nil || load.gen != msg.gen {
		return nil // a later bind owns the transcript now
	}
	if msg.width != m.width {
		// Resized while it rendered: the paint is the wrong width, so redo it at
		// the current one instead of installing a bundle that no longer fits.
		return m.renderFullReplayCmd(*load)
	}
	idx := m.replay.block
	if idx < 0 || idx >= len(m.transcript) || m.transcriptSources[idx].kind != transcriptSourceReplayBundle {
		return nil
	}
	m.replay.load = nil
	m.transcript[idx] = msg.paint.rendered
	m.transcriptSources[idx] = msg.paint.source()
	m.installWrappedBlock(idx, msg.paint)
	m.transcriptDirty = true
	m.forceGotoBottom = load.pin
	return nil
}

// source is the transcript source a paint's block keeps, so a resize reflows it
// from the same history and banner it was rendered from.
func (p replayPaint) source() transcriptSource {
	history := p.history
	if len(p.sourceHistory) > 0 {
		history = p.sourceHistory
	}
	return transcriptSource{kind: transcriptSourceReplayBundle, raw: p.raw, history: history, windowed: p.windowed}
}

// installReplayPaint commits one rendered bundle as the transcript's replay block
// and fills the wrap cache with it, so the next frame neither renders nor wraps
// the history.
func (m *chatTUI) installReplayPaint(paint replayPaint) {
	m.replay.block = len(m.transcript)
	*m.pendingCommit = append(*m.pendingCommit, paint.rendered)
	m.appendTranscriptBlock(paint.rendered, paint.source())
	m.installWrappedBlock(m.replay.block, paint)
}

// installWrappedBlock adopts a wrap computed off the loop for one block, which
// is what keeps the cache consistent instead of invalidating it: without this
// the next frame would re-wrap the bundle on the Update goroutine and the whole
// exercise would buy nothing. A cache that is not the prefix this block extends
// (a test host that never presented a frame, a width the cache was not built at)
// is left to the lazy path, which rebuilds from this block onward.
func (m *chatTUI) installWrappedBlock(index int, paint replayPaint) {
	m.setWrappedBlock(index, paint.wrapped, paint.contentW)
}
