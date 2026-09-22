package cli

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"reasonix/internal/billing"
	"reasonix/internal/boot"
	"reasonix/internal/command"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/i18n"
	"reasonix/internal/memory"
	"reasonix/internal/migration"
	"reasonix/internal/outputstyle"
	"reasonix/internal/plugin"
	"reasonix/internal/provider"
	"reasonix/internal/sessioninbox"
	"reasonix/internal/skill"
)

// chatTUI is a bubbletea Model that normally owns the terminal with an
// alt-screen transcript viewport. Termux is the exception: it stays in the
// normal buffer and commits finalized output to native scrollback via
// tea.Println so taps can still focus the soft keyboard.
type chatTUI struct {
	ctrl control.SessionAPI
	// teamPick is the team roster overlay opened by the TEAM button (nil when closed).
	teamPick *teamPicker

	// teamBackends holds one assembled Agent backend per team member; binding a
	// member swaps m.ctrl to its backend. memberEvents is its event pump.
	teamBackends *teamBackends

	// teamEscalations is the decider for a member's out-of-scope write: it queues
	// the request for the leader agent and settles the blocked member. Window-
	// scoped, because member backends outlive the overlay.
	teamEscalations *writeAccessEscalations
	memberEvents    *memberEventPump
	// replay is the bound backend's replay bundle and its pending render.
	replay replayWindow
	// memberBackendBase yields the boot options a member backend inherits from
	// this session's launch wiring; the member builder overrides model and sink.
	memberBackendBase func() boot.Options

	// ambient is the chat's own backend, saved when the window first binds a team
	// member so leaving the team session hands the window back to it.
	ambient control.SessionAPI

	shutdownErr error // final save's failure; reported after terminal release
	label       string
	missing     string // missing-key warning surfaced once in the banner, "" when ready
	// turnSettingsIntent holds a queued turn waiting on a model/settings
	// decision; it is resumed once the switch settles.
	turnSettingsIntent *controllerTurnIntent
	webHandoffState
	// diagnostics is the process-owned TUI log/watchdog started before terminal
	// takeover. Nil in unit tests that construct chatTUI without chatREPL.
	diagnostics      *tuiDiagnostics
	firstFrameLogged bool

	width  int
	height int
	// themeSweep freezes the frame while a /theme switch wipes across it.
	themeSweep *themeSweep
	// nativeScrollback keeps Termux out of alt-screen mode so taps still focus
	// the textarea and raise the soft keyboard.
	nativeScrollback bool
	// mouseCaptureOff releases mouse ownership back to the terminal (View() sets
	// tea.MouseModeNone instead of MouseModeCellMotion) so its native
	// click-drag selection and right-click context menu work again. Toggled by
	// "/mouse" or REASONIX_DISABLE_MOUSE at startup; trades away in-app
	// drag-select, the transcript scrollbar, and wheel-scroll while it's on,
	// since the terminal no longer forwards those events to Reasonix.
	mouseCaptureOff bool

	input       textarea.Model
	composerSel composerSelection
	composerMap composerLayoutCache
	// composerScrollOffset is an independent view offset used after the user
	// wheels inside an overflowing composer. The textarea keeps ownership of the
	// real insertion cursor; a subsequent edit or cursor key reattaches the view
	// to that cursor without the wheel having moved it.
	composerScrollOffset   int
	composerScrollDetached bool
	spinner                spinner.Model

	submittedInputs      []string
	submittedInputCursor int
	submittedInputDraft  string
	pastedBlocks         []pastedBlock
	nextPasteID          int
	usedPasteIDs         map[int]struct{}

	state tuiState
	// maintenance is the active controller-owned compaction lifecycle. It is
	// deliberately separate from state: maintenance keeps the composer usable
	// for durable queueing and must not start ordinary turn timers or metrics.
	maintenance                 *event.SessionOperationInfo
	maintenanceTranscriptID     string
	maintenanceTranscriptIdx    int
	maintenanceTerminal         map[string]struct{}
	maintenanceLatest           map[string]event.SessionOperationInfo
	compactCompatibilityPending bool
	compactLifecycleObserved    bool
	runStart                    time.Time
	elapsed                     int
	elapsedTickGeneration       uint64
	// rosterTickGen names the team overlay's poll chain. Opening a session bumps
	// it, so a chain armed before that is dropped instead of re-arming: the tick
	// message carries it, and only a tick whose generation is current continues
	// the chain (see rosterTick).
	rosterTickGen uint64
	// Recovery state is cleared by progress or completion.
	retryAttempt int
	retryMax     int
	recovery     *event.RecoveryStatus
	// Host turn phase, cleared on TurnDone.
	turnPhase string
	readStatusState
	// turnTokens accumulates this turn's output tokens (summed from per-step Usage
	// events) for the live "↓N" readout in the running status line.
	turnTokens int
	// showTurnUsage controls whether completed per-request token/cost receipts are
	// retained in transcript scrollback. Usage accounting remains active either way.
	showTurnUsage bool
	// sessionCostQuote is the incrementally aggregated canonical quote seen on
	// Usage events. It powers the persistent footer without re-running pricing.
	sessionCostQuote *billing.CostQuote

	// balance is the last-fetched wallet-balance readout (e.g. "¥110.00"), "" when
	// the provider declares no balance_url or a fetch failed. Refreshed async on
	// startup and after each turn so the status line stays roughly current without
	// blocking the event loop.
	balance string

	// todo is the pinned task panel's mounted state, owned by the session that
	// produced it (see todoView). event.Todo carries no owner of its own, so the
	// ownership lives here and is re-checked at every write.
	todo todoView
	// todoArgs is the latest todo_write call's raw args; it drives the task list
	// pinned just above the input (see renderTodoPanel). "" when there's no list.
	// Persists across turns until the work completes or a new session starts.
	todoArgs      string
	searchSources []provider.ServerSearchHit // post-answer footnotes; cleared when the turn settles

	// marker rides in outgoing user messages so the cache-stable prompt prefix is
	// left untouched.
	planMode bool
	// yoloRestoreToolApprovalMode remembers the safe permission preset that
	// Ctrl+Y should restore after toggling the canonical danger-full-access
	// preset under the user-facing YOLO label.
	yoloRestoreToolApprovalMode string
	// legacyScrollClear keeps the per-offset ClearScreen workaround only for Warp.
	legacyScrollClear bool
	// sessionSwitch suppresses that workaround during a transcript rebuild (#5441).
	sessionSwitch bool
	// inboxSelectedID is the currently highlighted durable inbox item while
	// browsing the queue in tuiRunning. Empty means "not browsing". Full bodies
	// are never cached here — only the selected ID and the snapshot metadata.
	inboxSelectedID string
	// queueEditCursor tracks which queued message the user is currently
	// browsing/editing via ↑/↓ during tuiRunning. -1 means "not browsing".
	queueEditCursor int
	// queueEditDraft saves the in-progress input text when the user first
	// presses ↑ to browse the queue, so it can be restored when the cursor
	// moves past the end.
	queueEditDraft string
	// queueConfirmDelete, when true, the next 'd' confirms deletion of the
	// selected inbox item.
	queueConfirmDelete bool

	// history is a resumed session's messages, committed to scrollback once on
	// the first WindowSizeMsg so a reopened chat shows its prior transcript.
	history []provider.Message

	// reasoning accumulates the in-progress thinking stream (dim); pending
	// accumulates the in-progress answer (raw markdown). They are committed to
	// scrollback (reasoning collapsed by default, answer markdown-rendered) when they
	// finalize — at a tool/usage boundary or turn end — not previewed live, so
	// the bottom region stays a stable height. pendingCommit queues finalized
	// lines so a single Update emits exactly one ordered tea.Println.
	reasoning     *strings.Builder
	pending       *strings.Builder
	pendingCommit *[]string
	showReasoning bool // Ctrl+O / /verbose: show raw thinking text in the CLI
	cfg           *config.Config
	// reasoningLineIdx is the transcript index of the live "▎ thinking…" marker
	// while a reasoning block streams; it's rewritten to "▎ thought for Ns" when
	// the block closes. -1 when no block is open. transcriptDirty forces a
	// viewport re-feed after that in-place rewrite (length is unchanged).
	reasoningLineIdx int
	// reasoningTextIdx is the transcript index of the live reasoning text block
	// (the block right after the marker), streamed in as the model thinks and
	// removed when the block collapses (kept only in verbose mode). -1 when none.
	reasoningTextIdx int
	// reasoningView is a bounded trailing window (≤ reasoningViewMax bytes) of the
	// streaming thought, rendered live; the full text stays in reasoning for verbose.
	reasoningView []byte
	// reasoningNative is the Termux/native-scrollback path: reasoning is buffered
	// without a live transcript block, then appended once as a final summary.
	reasoningNative bool
	thinkStart      time.Time
	// answerIdx is the transcript index of the streaming answer block (rewritten in
	// place as completed paragraphs arrive); -1 when none is open. answerFlushed is
	// how many bytes of pending have already been rendered into it, so a Text packet
	// that doesn't close a new paragraph re-renders nothing.
	answerIdx     int
	answerFlushed int
	// toolStreamIdx is the transcript index of a running tool's live-output block
	// (streamed via ToolProgress under the tool card); -1 when none. toolStreamID
	// is the call ID it belongs to. Only a bounded tail is kept — the last few
	// complete lines (toolTail) plus the in-progress one (toolPartial) — so a
	// high-output command can't balloon memory or cost O(n²) re-splitting;
	// toolLineCount feeds the collapse summary.
	toolStreamIdx int
	toolStreamID  string
	toolTail      []string
	toolPartial   string
	toolLineCount int
	// shellOutputs stores the full accumulated output of each shell command
	// (tool IDs with "shell-" prefix), so the first 10 lines can be shown after
	// collapse and Ctrl+B can toggle the complete output.
	shellOutputs  map[string]string
	shellExpanded map[string]bool
	// shellTranscriptIdx maps a shell tool ID to the transcript index of its
	// collapsed output block, so Ctrl+B can rewrite it in place.
	shellTranscriptIdx map[string]int
	// toolLineCountByID keeps a switched-away tool's last line count so a late
	// ToolResult can still render "⎿ N lines" (shellOutputs only tracks "shell-" ids).
	toolLineCountByID map[string]int
	// toolStreamStart / toolStreamFrame drive the "⎿ working · Ns" line shown
	// under a dispatched tool that hasn't produced output yet, so a slow tool
	// reads as making progress rather than frozen.
	toolStreamStart time.Time
	toolStreamFrame int
	// Sub-agent progress previews (reserved ToolProgress channels) render per
	// child into their own fixed transcript slot, keyed by the namespaced call
	// ID — independent of the single live toolStreamID. subagentProgress keeps
	// the bounded live state (phase, elapsed, recent activity, verbose tails).
	subagentProgressIdx map[string]int
	subagentProgress    map[string]*cliSubagentProgress
	transcriptDirty     bool
	// forceGotoBottom is set by replayActiveBranch and resetFreshContextView to
	// pin the viewport to the bottom after a session / branch / clear switch
	// regardless of the previous wasAtBottom state (#4584).
	forceGotoBottom bool
	// scrollMode is the explicit followTail / userScrolled state machine.
	// Prefer this over a raw wasAtBottom snapshot so modal height changes
	// (approval, chooser, pickers) never silently disable tail-follow (#6430).
	scrollMode scrollFollowMode
	eventCh    chan event.Event
	started    bool // banner + resumed history committed once

	// transcript holds every finalized line commitLine emits; the viewport
	// renders a scrollable window of it (alt-screen owns the grid, so there's no
	// native terminal scrollback). sel is the live left-drag text selection.
	transcript []string
	// transcriptSources runs parallel to transcript and retains raw, semantic
	// content for blocks whose layout depends on terminal width. Fixed blocks
	// keep their already-rendered text; markdown, user bubbles, reasoning, tool
	// cards, and replay bundles are regenerated after a resize. The wrap cache
	// fields beside it keep wrappedLines incremental (wrap_cache.go).
	transcriptSources []transcriptSource
	wrappedLines      []string
	wrapBlockLines    [][]string
	wrapBlockOffsets  []int
	wrapWidth         int
	wrapBlockCount    int
	wrapDirty         wrapSpan
	// lastMouseReenable rate-limits ConPTY mouse re-enable sequences (#7583).
	// mouseReenablePending + timer cover trailing-edge fires after a resize storm.
	lastMouseReenable       time.Time
	mouseReenablePending    bool
	mouseReenableTimerArmed bool
	// wantMouseReenable is set by TurnDone (and similar settle points) and
	// consumed once in Update so the raw enable sequence is batched with the
	// frame that paints the settled state.
	wantMouseReenable bool
	viewport          viewport.Model
	sel               selection
	// autoScroll drives edge-drag scrolling: -1 up, +1 down, 0 off. dragX is the
	// column the drag is held at, so the ticker can extend the selection head.
	autoScroll int
	dragX      int
	// scrollbarDrag owns left-button drags that start on the transcript scrollbar
	// column. It is separate from text selection so the visual thumb is not a
	// dead target and dragging it never leaves a transcript selection behind.
	scrollbarDrag       bool
	scrollbarGrabOffset int
	// copyNoticeText is a transient "copied to clipboard" hint shown on the status
	// line after a mouse-drag, right-click, or Ctrl+C selection copy; "" when none
	// is showing. copyNoticeSeq guards its expiry tick so an older copy's timer
	// can't clear a newer notice — each copy bumps the sequence and only a tick
	// carrying the current sequence clears the text.
	copyNoticeText string
	copyNoticeSeq  int
	// clipboardImagePending keeps the footer honest while the platform clipboard
	// is being decoded. clipboardImageRequests counts shortcuts coalesced into the
	// probe: an image attaches once, while a text fallback preserves every press.
	clipboardImagePending  bool
	clipboardImageRequests int

	// terminalPasteSeq counts bracketed pastes delivered by the terminal.
	// clipboardImageTerminalPasteSeq snapshots it when an image probe starts, so a
	// terminal that pastes text itself is never pasted into twice.
	terminalPasteSeq               uint64
	clipboardImageTerminalPasteSeq uint64

	// The user bubble is echoed to scrollback immediately on Enter (bubbleStartIdx
	// marks where in the transcript it landed). It stays "un-sendable" until the
	// first response packet arrives: pressing Esc/Ctrl+C before then pops those
	// lines back off the transcript and restores the text to the input box, leaving
	// no trace. bubblePending is true from startTurn until the first packet confirms
	// the send or it's un-sent; turnDiscarded then swallows the turn's
	// already-buffered events until its TurnDone settles.
	pendingRestore string
	pendingPastes  []string
	bubbleStartIdx int
	bubblePending  bool
	turnDiscarded  bool

	// pendingApproval holds the tool-call approval currently shown in the banner
	// (nil when none). While set, the controller's run goroutine is blocked
	// awaiting ctrl.Approve and key input is captured to answer it.
	pendingApproval   *event.Approval
	approvalSelection int

	// chooser holds the `ask` tool's question card (nil when none). While set, the
	// run goroutine is blocked awaiting ctrl.AnswerQuestion and keys drive the card.
	chooser *chooser
	// elicit holds the pending MCP elicitation card (nil when none).
	elicit *elicitCard

	// rewind holds the Esc-Esc / "/rewind" picker (nil when closed); while set,
	// keys drive it and it renders as an overlay. lastEsc times the double-Esc
	// gesture that opens it on an empty composer.
	rewind *rewindPicker
	// resumePick is the interactive "/resume" session picker overlay. Non-nil
	// while the user browses saved sessions with ↑/↓ and confirms with Enter.
	resumePick *resumePicker
	// reclaimState groups the flags a remote take-back sets and clears together.
	reclaimState
	// pendingTakeoverPath remembers the last /resume target refused because a
	// resident serve on this machine holds its lease; "/takeover" force-takes
	// that session back.
	pendingTakeoverPath string
	// quickPick owns searchable single-choice overlays such as /model and
	// /provider. It never invokes a raw-mode prompt inside Bubble Tea.
	quickPick *quickPicker
	setup     *connectionSetup
	copyPick  *copyPicker
	lastEsc   time.Time

	// mcp is the interactive "/mcp" manager overlay. mcpDisabled tracks servers
	// turned off only for this chat session, matching the desktop connector
	// toggle's non-persistent semantics.
	mcp         *mcpManager
	mcpDisabled map[string]bool

	// clearConfirm is the destructive "/clear" confirmation overlay. It is separate
	// from /new because /clear discards the current transcript instead of saving it.
	clearConfirm *clearConfirm

	// lastCtrlCAt records when Ctrl+C was pressed while idle on an empty
	// composer, enabling a "press again to quit" confirmation pattern (1.5s
	// window). Reset when Ctrl+C clears non-empty input instead.
	lastCtrlCAt time.Time

	// mcpImport holds the interactive cc-switch MCP import picker (nil when
	// closed). It writes selected servers to config and hot-connects the ones that
	// can start successfully.
	mcpImport *mcpImportPicker

	// host is the running MCP servers (nil when no plugins). The TUI reads
	// prompts (slash commands), resources (@-references), and server status
	// (/mcp) from it.
	host *plugin.Host

	// commands are custom slash commands loaded from .reasonix/commands; each renders
	// its template with the typed args and sends the result as a turn.
	commands []command.Command

	// skills are the discoverable skills (built-in + user/project); each is offered
	// in the slash menu as "/<name>" and managed via /skills.
	skills []skill.Skill

	// slashCache holds the immutable slash catalog and the arg-completion data
	// snapshot, rebuilt only on explicit invalidation — never on keystrokes
	// (#6417, #7090, #9503).
	slashCache *slashCompletionCache

	// skillPick is the interactive skill picker overlay for /skills. nil when closed.
	skillPick *skillPicker

	// buildController builds a fresh controller for a model choice, carrying
	// prior history across and pinning auto-save to resumePath so the continued
	// conversation stays in one file (set by chatREPL; it must NOT touch this
	// model — the swap happens on the running copy). nil disables runtime
	// rebuild commands. modelRef is the active "provider/model" ref, marked
	// current in the picker. oldCtrl is the
	// outgoing controller, passed through so the replacement can carry forward
	// same-session tool grants and Plan-mode read-only command trust that
	// don't travel through carry/resumePath (see Controller.RestoreSessionAuthorizations).
	buildController func(spec controllerBuildSpec, carry []provider.Message, resumePath string, oldCtrl control.SessionAPI) (*control.Controller, error)
	// rebuildRuntime builds the /reload replacement through boot.Rebuild:
	// same model/profile/effort, but tools, skills, commands, hooks, MCP
	// servers, and providers are discovered fresh and the session state
	// migrates inside the boot layer. Set by chatREPL (it must NOT touch
	// this model — the swap happens on the running copy); nil disables
	// /reload.
	rebuildRuntime  runtimeRebuilder
	lastBuildResult *boot.BuildResult
	// pendingReload coalesces /reload requests made while a turn or a runtime
	// switch is in flight; the TurnDone drain runs it once the TUI is idle.
	pendingReload bool
	modelRef      string
	effortLevel   string // "" when the current provider/model has no configurable effort

	// leases owns the session lease guarding the TUI's active session file (set
	// by chatREPL; nil in tests and when persistence is disabled). Every in-TUI
	// operation that rebinds the controller to another session file must move
	// the lease first — see rebindSessionLease / followSessionLease.
	leases *control.SessionLeaseKeeper
	// takeover mirrors a session acquired from a resident Serve and blocks
	// admission while that Serve is reclaiming it.
	takeover *cliTakeoverManager

	// outputStyle is the active output-style name (config agent.output_style),
	// shown as the current entry in the /output-style listing. "" = default.
	outputStyle string

	// diffMaxLines controls the max lines shown in a diff view. 0 = show all;
	// non-zero = fold at that many lines. Toggled by /diff-fold.
	diffMaxLines int

	// statuslineCmd is the user's custom status-line command (config
	// [statusline].command); "" disables it. statuslineOut caches its latest
	// one-line stdout, refreshed at startup and after each turn and rendered in
	// place of the built-in data row.
	statuslineCmd string
	statuslineOut string
	gitStatus     gitStatus

	// statusLineCount is the number of terminal rows the status block occupies
	// (wrapped working line + wrapped status line + wrapped data line). Updated
	// each frame via computeStatusLineCount so bottomRows can reserve the correct
	// height; starts at 2 (unwrapped) until first render.
	statusLineCount int

	// modelSwitchPending is true while any async controller rebuild is in flight.
	modelSwitchPending bool
	// pendingModelSwitch holds the tea.Cmd that triggers the async build. The
	// historical field name is retained because model, effort, skill refresh,
	// and work-mode changes all share the same atomic swap path.
	pendingModelSwitch tea.Cmd
	// oldControllers accumulates controllers retired by runtime switches.
	// They cannot be closed during the switch (Close runs SessionEnd hooks
	// and kills plugin subprocesses, both of which corrupt the terminal's
	// raw mode). Instead they are closed at process exit when the terminal
	// is already being restored.
	oldControllers []control.SessionAPI

	// completion is the live autocomplete menu (slash commands; @-refs later).
	completion completion
	// fileSearchCache memoizes fileref.Search by query so the bounded walk runs
	// once per @token fragment, not on every keystroke that re-renders the menu.
	fileSearchCache map[string][]string
}

type tuiState int

func (m chatTUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Confirm booting → idle on the first Update. User input (keys/mouse/focus)
	// must NOT refresh the active-turn heartbeat; only elapsedTick and work
	// events do, so interaction cannot mask a stuck event loop (#7809).
	if m.diagnostics != nil {
		m.diagnostics.NoteBooted()
	}
	logFirstFrame := false
	if m.diagnostics != nil && !m.firstFrameLogged {
		if _, ok := msg.(tea.WindowSizeMsg); ok {
			logFirstFrame = true
		}
	}
	// Prefer explicit scrollMode over a raw AtBottom snapshot so opening an
	// approval/chooser (height-only change) does not disable tail-follow (#6430).
	followTail := m.shouldFollowTail()
	prevLines := len(m.transcript)
	prevWidth := m.width
	prevHeight := m.height
	prevYOff := m.viewport.YOffset()
	var resizeAnchor transcriptResizeAnchor
	if size, ok := msg.(tea.WindowSizeMsg); ok && size.Width != m.width && !followTail {
		resizeAnchor = captureTranscriptResizeAnchor(m.transcript, m.viewport.Width(), prevYOff)
	}

	next, cmd := m.update(msg)
	cm := next.(chatTUI)
	if logFirstFrame {
		cm.firstFrameLogged = true
		if cm.diagnostics != nil {
			cm.diagnostics.Milestone("first_frame")
		}
	}

	contentW := transcriptContentWidth(cm.width, cm.nativeScrollback)
	cm.viewport.SetWidth(contentW)
	// Recompute the wrapped status-line count so bottomRows reserves the right
	// height for the viewport. Use cm.width (same as boxW in View()) so the
	// wrapping width matches what View() actually renders.
	cm.statusLineCount = cm.computeStatusLineCount(cm.width)
	// Keep the composer proportional to the live terminal instead of letting its
	// absolute row cap crowd the transcript and fixed status rows on short
	// windows. Textarea remains the owner of the scroll offset and caret reveal.
	cm.syncInputHeightLimit()
	cm.viewport.SetHeight(cm.transcriptHeight())
	widthChanged := cm.width != prevWidth
	var replayCmd tea.Cmd
	if widthChanged {
		cm.reflowTranscript(cm.width)
		// Selection coordinates are visual-line based and cannot survive a
		// semantic reflow without selecting unrelated text.
		cm.sel = selection{}
		// A member's replay block reflows through its newest window (bounded, like a
		// bind); the rest re-renders off the loop, so a resize never re-renders a
		// long member history on this goroutine (reflowReplayBundle).
		replayCmd = cm.reflowReplayBundle()
	}
	// Wrap sync: full rebuild only on width change or history shrink. Streaming
	// answer/tool rewrites use invalidateWrapFrom → suffix-only re-wrap; the
	// transcriptDirty flag alone must never force a full-history rebuild (#6978).
	forceFullWrap := widthChanged || len(cm.transcript) < prevLines
	wrapBehind := cm.wrapWidth != contentW || cm.wrapBlockCount != len(cm.transcript)
	if forceFullWrap || wrapBehind || len(cm.transcript) != prevLines {
		if cm.syncWrappedLines(contentW, forceFullWrap) {
			cm.feedViewportContent()
		}
		if followTail || cm.shouldFollowTail() {
			cm.viewport.GotoBottom() // tail-follow: stay pinned to newest output
			cm.markFollowTail()
		} else if widthChanged && resizeAnchor.valid {
			cm.viewport.SetYOffset(resizeAnchor.yOffset(cm.transcript, contentW))
		}
	} else if followTail && (cm.forceGotoBottom || cm.height != prevHeight) {
		// Height-only change (modal open/close, status wrap) must still pin
		// when we are in followTail — without waiting for new transcript.
		cm.viewport.GotoBottom()
	}
	if cm.forceGotoBottom {
		cm.viewport.GotoBottom()
		cm.markFollowTail()
		cm.forceGotoBottom = false
	}
	cm.transcriptDirty = false

	// Rate-limited mouse re-enable after real resize, focus regain, or turn
	// settle so Windows ConPTY keeps wheel → MouseWheelMsg (#7583). Trailing
	// timer msgs are handled here too. Same-size WindowSizeMsg (session-switch
	// rebuilds) must not force a spurious Raw cmd.
	var mouseCmd tea.Cmd
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		if cm.width != prevWidth || cm.height != prevHeight {
			mouseCmd = cm.maybeReenableMouse()
		}
	case tea.FocusMsg:
		mouseCmd = cm.maybeReenableMouse()
	case mouseReenableMsg:
		mouseCmd = cm.handleMouseReenableMsg(v)
	}
	if cm.wantMouseReenable {
		cm.wantMouseReenable = false
		if c := cm.maybeReenableMouse(); c != nil {
			mouseCmd = batchCmds(mouseCmd, c)
		}
	}

	// Keep the legacy full redraw only where Warp's scroll optimization can
	// strand stale rows. Every other terminal relies on Bubble Tea's renderer.
	if cm.legacyScrollClear && cm.viewport.YOffset() != prevYOff && !cm.nativeScrollback && !cm.sessionSwitch {
		cm.sessionSwitch = false
		return cm, batchCmds(tea.ClearScreen, mouseCmd, cmd, replayCmd)
	}
	cm.sessionSwitch = false
	return cm, batchCmds(mouseCmd, cmd, replayCmd)
}

// update runs the model's message handling. Update wraps it to keep the
// transcript viewport sized, fed, and tail-following after every message.
func (m chatTUI) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var inputBeforeSelection string

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.followComposerCursor()
		m.width = msg.Width
		m.height = msg.Height
		m.noteTerminalSize()
		m.input.SetWidth(max(msg.Width-4, 1))
		// Commit the banner — and a resumed session's transcript — once, now
		// that the width is known.
		if !m.started {
			m.started = true
			history := append([]provider.Message(nil), m.history...)
			m.commitTranscriptSource(transcriptSource{
				kind: transcriptSourceReplayBundle, raw: m.missing, history: history,
			})
			m.history = nil
		}

	case tea.FocusMsg:
		// Terminal regained focus — ConPTY may have dropped mouse tracking
		// while the pane was unfocused (#7583). Re-enable is issued from Update.
		return m, nil

	case tea.MouseWheelMsg:
		if m.teamPick != nil && m.teamPick.handleWheel(msg.Button) {
			return m, nil
		}
		if m.mouseOverComposer(msg.X, msg.Y) {
			delta := 0
			switch msg.Button {
			case tea.MouseWheelUp:
				delta = -composerWheelRows
			case tea.MouseWheelDown:
				delta = composerWheelRows
			}
			if delta != 0 && m.scrollComposer(delta) {
				return m, nil
			}
		}
		// Outside the composer, or once its internal viewport has reached the
		// requested edge, continue the gesture in the transcript. This mirrors
		// ordinary nested-scroll behavior and avoids a dead wheel at boundaries.
		switch msg.Button {
		case tea.MouseWheelUp:
			m.viewport.ScrollUp(3)
		case tea.MouseWheelDown:
			m.viewport.ScrollDown(3)
		}
		m.syncScrollModeAfterGesture()
		return m, nil

	case tea.MouseClickMsg:
		if cmd, hit := m.teamStatusClick(msg); hit {
			return m, cmd
		}
		// Match the complete terminal right-click convention while Reasonix owns
		// the mouse: copy an active selection, otherwise paste clipboard text into
		// the visible composer. Left-press begins a selection unless it lands on
		// the transcript scrollbar or a shell-output hint line.
		// Middle-click pastes tmux's current buffer when tmux owns the pane;
		// otherwise it follows the X11/Wayland PRIMARY-selection convention.
		if msg.Button == tea.MouseMiddle {
			if m.hideComposer() {
				return m, nil
			}
			cmds = append(cmds, pasteMiddleClick())
			return m, finalize(m, cmds)
		}
		if msg.Button == tea.MouseRight && m.validComposerSelection() && !m.composerSel.empty() {
			cmds = append(cmds, m.copySelectionWithNotice(m.selectedComposerText()))
			return m, finalize(m, cmds)
		}
		if msg.Button == tea.MouseRight && m.sel.active && !m.sel.empty() {
			text := m.selectedText()
			m.sel = selection{}
			cmds = append(cmds, m.copySelectionWithNotice(text))
			return m, finalize(m, cmds)
		}
		if msg.Button == tea.MouseRight && !m.hideComposer() {
			cmds = append(cmds, pasteClipboardText())
			return m, finalize(m, cmds)
		}
		if msg.Button == tea.MouseLeft {
			if at, ok := m.composerCaretAt(msg.X, msg.Y, false); ok {
				m.sel = selection{}
				m.autoScroll = 0
				m.setComposerCursor(at.offset)
				m.composerSel = composerSelection{
					active: true, anchor: at.offset, head: at.offset, value: m.input.Value(),
				}
				return m, nil
			}
			m.composerSel = composerSelection{}
		}
		if msg.Button == tea.MouseLeft && m.inScrollbar(msg.X, msg.Y) {
			m.sel = selection{}
			m.autoScroll = 0
			m.scrollbarDrag = true
			m.scrollbarGrabOffset = m.scrollbarGrabRowOffset(msg.Y)
			m.dragScrollbar(msg.Y)
			return m, nil
		}
		if msg.Button == tea.MouseLeft && msg.Y < m.viewport.Height() {
			// Check if the clicked line is a shell-output hint.
			lineIdx := m.viewport.YOffset() + msg.Y
			if lineIdx >= 0 && lineIdx < len(m.wrappedLines) {
				clicked := m.wrappedLines[lineIdx]
				if strings.Contains(clicked, "more lines") && strings.Contains(clicked, "Ctrl+B") {
					m.toggleShellOutput()
					return m, finalize(m, cmds)
				}
			}
			at := m.transcriptCaret(msg.X, msg.Y)
			m.sel = selection{active: true, anchor: at, head: at}
			m.autoScroll = 0
		}
		return m, nil

	case tea.MouseMotionMsg:
		if m.validComposerSelection() {
			if at, ok := m.composerCaretAt(msg.X, msg.Y, true); ok {
				m.composerSel.head = at.offset
			}
			return m, nil
		}
		if m.scrollbarDrag {
			m.dragScrollbar(msg.Y)
			return m, nil
		}
		// Drag extends the live selection (CellMotion only reports motion while
		// a button is held, so this is a drag). A drag held against the top or
		// bottom edge starts an auto-scroll ticker so the selection can run past
		// the visible window.
		if m.sel.active {
			m.sel.head = m.transcriptCaret(msg.X, msg.Y)
			m.dragX = msg.X
			prev := m.autoScroll
			m.autoScroll = edgeScrollDir(msg.Y, m.viewport.Height())
			if m.autoScroll != 0 && prev == 0 {
				return m, autoScrollTick()
			}
		}
		return m, nil

	case autoScrollMsg:
		// One edge-scroll step: scroll a single line, drag the selection head to
		// the edge row, and keep ticking until the drag ends, leaves the edge, or
		// the viewport can't scroll further (so it can't run away to the end).
		if !m.sel.active || m.autoScroll == 0 {
			return m, nil
		}
		edgeY := 0
		if m.autoScroll > 0 {
			m.viewport.ScrollDown(1)
			edgeY = m.viewport.Height() - 1
		} else {
			m.viewport.ScrollUp(1)
		}
		m.syncScrollModeAfterGesture()
		m.sel.head = m.transcriptCaret(m.dragX, edgeY)
		// Stop at the boundary so a held edge can't run away to the very end.
		if (m.autoScroll > 0 && m.viewport.AtBottom()) || (m.autoScroll < 0 && m.viewport.AtTop()) {
			m.autoScroll = 0
			return m, nil
		}
		return m, autoScrollTick()

	case tea.MouseReleaseMsg:
		if msg.Button == tea.MouseLeft && m.validComposerSelection() {
			if at, ok := m.composerCaretAt(msg.X, msg.Y, true); ok {
				m.composerSel.head = at.offset
				m.setComposerCursor(at.offset)
			}
			if m.composerSel.empty() {
				m.composerSel = composerSelection{}
				return m, nil
			}
			// The terminal cannot see Reasonix's application-owned highlight, and
			// macOS commonly consumes Cmd+C before it reaches the TUI. Copy on drag
			// release just like transcript selection so the visible selection always
			// has a usable clipboard result.
			cmds = append(cmds, m.copySelectionWithNotice(m.selectedComposerText()))
			return m, finalize(m, cmds)
		}
		// Release finalizes the selection: a real drag auto-copies it (native
		// terminal convention), while the highlight stays on as the visual
		// "what's selected" cue and a right-click can still re-copy it. A plain
		// click (no drag) clears any prior selection.
		if m.scrollbarDrag {
			m.dragScrollbar(msg.Y) // already syncs scrollMode
			m.scrollbarDrag = false
			m.scrollbarGrabOffset = 0
			return m, nil
		}
		m.autoScroll = 0 // stop edge auto-scroll
		if msg.Button == tea.MouseLeft && m.sel.active {
			if m.sel.empty() {
				m.sel = selection{}
			} else {
				cmds = append(cmds, m.copySelectionWithNotice(m.selectedText()))
			}
		}
		return m, finalize(m, cmds)

	case tea.PasteMsg:
		return m.applyComposerPaste(msg, true)

	case tea.KeyPressMsg:
		// Any keystroke dismisses a finished selection (copy is a right-click),
		// with a few exceptions: Ctrl/Super/Meta+C and Ctrl+Insert copy the
		// selection, the paste shortcuts keep it so the async clipboard result
		// can replace it, and Left/Right collapse it to its ordered start/end.
		sel := m.sel
		m.sel = selection{}
		if m.validComposerSelection() && !m.composerSel.empty() {
			switch {
			case msg.String() == "ctrl+c" || msg.String() == "super+c" || msg.String() == "meta+c" || msg.String() == "ctrl+insert":
				cmds = append(cmds, m.copySelectionWithNotice(m.selectedComposerText()))
				return m, finalize(m, cmds)
			case imagePasteShortcut(msg.String(), runtime.GOOS):
				// The asynchronous image result replaces the still-active
				// selection. Terminal text paste arrives separately as PasteMsg.
			case msg.String() == "left":
				start, _ := m.composerSel.ordered()
				m.composerSel = composerSelection{}
				m.setComposerCursor(start)
				return m, finalize(m, cmds)
			case msg.String() == "right":
				_, end := m.composerSel.ordered()
				m.composerSel = composerSelection{}
				m.setComposerCursor(end)
				return m, finalize(m, cmds)
			default:
				inputBeforeSelection = m.input.Value()
				if composerSelectionDeletes(msg, m.input.KeyMap) {
					m.deleteComposerSelection()
					m.growInputToFit()
					m.updateCompletion()
					if shouldClearWideInputChange(inputBeforeSelection, m.input.Value()) {
						cmds = append(cmds, tea.ClearScreen)
					}
					return m, finalize(m, cmds)
				}
				if composerSelectionReplaces(msg, m.input.KeyMap) {
					m.deleteComposerSelection()
				} else {
					m.composerSel = composerSelection{}
				}
			}
		}
		// Transcript scroll keys work in any state (PgUp/PgDn are never text).
		switch msg.String() {
		case "pgup":
			m.viewport.PageUp()
			m.syncScrollModeAfterGesture()
			return m, finalize(m, cmds)
		case "pgdown":
			m.viewport.PageDown()
			m.syncScrollModeAfterGesture()
			return m, finalize(m, cmds)
		case "ctrl+home":
			m.viewport.GotoTop()
			m.markUserScrolled()
			return m, finalize(m, cmds)
		case "ctrl+end":
			m.viewport.GotoBottom()
			m.markFollowTail()
			return m, finalize(m, cmds)
		case "ctrl+z":
			return m, suspendWithMouseReset()
		}
		// From this point on the key belongs to the active control rather than
		// transcript navigation. Editing or moving the insertion cursor restores
		// the textarea's normal caret-following viewport.
		m.followComposerCursor()
		// A question card is modal: keys drive it. In its free-text ("Type
		// something") mode, the keystroke goes to the textarea — Enter confirms the
		// custom answer, Esc backs out of typing — so input/IME work as usual.
		if m.elicit != nil {
			if model, cmd, handled := m.elicitKey(msg, cmds); handled {
				return model, cmd
			}
		}
		if m.chooser != nil {
			if m.chooser.typing {
				switch msg.String() {
				case "enter":
					val := strings.TrimSpace(m.input.Value())
					m.resetComposerInput()
					m.chooser.typing = false
					m.refreshInputPlaceholder()
					if val == "" {
						return m, finalize(m, cmds)
					}
					m.chooser.custom[m.chooser.tab] = val
					m.chooser.sel[m.chooser.tab] = map[int]bool{}
					return m.chooserAdvance()
				case "esc":
					m.chooser.typing = false
					m.resetComposerInput()
					m.refreshInputPlaceholder()
					return m, finalize(m, cmds)
				}
				beforeInput := m.input.Value()
				var ic tea.Cmd
				m.input, ic = m.input.Update(msg)
				cmds = append(cmds, ic)
				m.growInputToFit()
				if shouldClearWideInputChange(beforeInput, m.input.Value()) {
					cmds = append(cmds, tea.ClearScreen)
				}
				return m, finalize(m, cmds)
			}
			return m.handleChooserKey(msg)
		}
		// The rewind picker is modal while open: keys navigate it.
		if m.rewind != nil {
			return m.handleRewindKey(msg)
		}
		// The MCP import picker is modal while open: keys select candidates.
		if m.mcpImport != nil {
			return m.handleMCPImportKey(msg)
		}
		// Copy picker is modal while open.
		if m.copyPick != nil {
			return m.handleCopyPickerKey(msg)
		}
		// The team overlay is modal; a bound session's composer owns typing.
		if next, cmd, consumed := m.handleTeamKey(msg); consumed {
			return next, cmd
		}
		// The resume picker is modal while open: keys navigate it.
		if m.resumePick != nil {
			return m.handleResumePickerKey(msg)
		}
		// Searchable command pickers are modal while open.
		if m.quickPick != nil {
			return m.handleQuickPickerKey(msg)
		}
		if m.setup != nil {
			return m.handleConnectionSetupKey(msg)
		}
		// The MCP manager is modal while open: keys navigate it.
		if m.mcp != nil {
			return m.handleMCPManagerKey(msg)
		}
		// The destructive /clear confirmation is modal while open.
		if m.clearConfirm != nil {
			return m.handleClearConfirmKey(msg)
		}
		// The skill picker is modal while open: keys navigate it.
		if m.skillPick != nil {
			return m.handleSkillPickerKey(msg)
		}
		// A pending tool approval is modal: keystrokes answer it (y/a/n, Enter,
		// Esc) rather than reaching the input.
		if m.pendingApproval != nil {
			return m.handleApprovalKey(msg)
		}
		// While the autocomplete menu is open it captures navigation/accept keys
		// (↑/↓ move, Tab/Enter accept, Esc close); everything else falls through
		// to the textarea and re-filters the menu at the end of Update.
		if m.completion.active {
			switch msg.String() {
			case "up", "ctrl+p":
				m.moveCompletion(-1)
				return m, nil
			case "down", "ctrl+n":
				m.moveCompletion(1)
				return m, nil
			case "tab", "enter":
				if msg.String() == "enter" && (m.completionExactLabel() || m.completionBareOverlayCommand()) {
					m.dismissCompletion()
					break // fall through to regular Enter and submit the command
				}
				// When Enter is pressed and the selected completion is already fully
				// present in the input, close the menu and submit instead of accepting
				// the same item again (/resume 1 still has /resume 10 as a prefix match).
				if msg.String() == "enter" && m.completionSelectedInsertPresent() {
					m.dismissCompletion()
					break // fall through to regular Enter
				}
				m.acceptCompletion()
				return m, nil
			case "esc":
				m.dismissCompletion()
				if m.state == tuiRunning {
					break // a turn is running — also cancel it via the main Esc handler
				}
				return m, nil
			}
		}
		switch msg.String() {
		case "up":
			if m.state == tuiRunning {
				if m.navigateQueue(-1) {
					return m, nil
				}
			} else if m.recallSubmittedInput(-1) {
				return m, nil
			}
		case "down":
			if m.state == tuiRunning {
				if m.navigateQueue(1) {
					return m, nil
				}
			} else if m.recallSubmittedInput(1) {
				return m, nil
			}
		case "alt+up", "meta+up":
			if m.handleQueueReorder(-1) {
				return m, finalize(m, cmds)
			}
		case "alt+down", "meta+down":
			if m.handleQueueReorder(1) {
				return m, finalize(m, cmds)
			}
		case " ":
			if m.inboxQueuedCount() > 0 && m.input.Value() == "" {
				m.toggleInboxPaused()
				return m, finalize(m, cmds)
			}
		case "r":
			if m.queueEditCursor >= 0 && m.inboxSelectedID != "" {
				if err := m.ctrl.RetryInboxItem(m.inboxSelectedID); err != nil {
					m.notice("retry: " + err.Error())
				} else {
					m.notice("retry queued #" + shortID(m.inboxSelectedID))
				}
				return m, finalize(m, cmds)
			}
		case "d":
			if m.queueEditCursor >= 0 && m.inboxSelectedID != "" {
				if !m.queueConfirmDelete {
					m.queueConfirmDelete = true
					m.notice("press d again to delete #" + shortID(m.inboxSelectedID))
					return m, finalize(m, cmds)
				}
				if err := m.ctrl.DeleteInboxItem(m.inboxSelectedID); err != nil {
					m.notice("delete: " + err.Error())
				} else {
					m.notice("deleted #" + shortID(m.inboxSelectedID))
				}
				m.resetQueueNavigation()
				return m, finalize(m, cmds)
			}
		case "enter":
			// Don't reset queue navigation — the Enter handler below needs
			// queueEditCursor to decide whether to save an edit or enqueue.
		default:
			m.resetSubmittedInputRecall()
			// Preserve queue navigation while the user is editing a queued
			// item — only reset when they're not browsing the queue, so that
			// typing replacement text keeps queueEditCursor alive for the
			// Enter handler to save the edit in-place. (#4877)
			if m.queueEditCursor < 0 {
				m.resetQueueNavigation()
			} else {
				m.queueConfirmDelete = false
			}
		}
		if imagePasteShortcut(msg.String(), runtime.GOOS) {
			if m.state == tuiRunning {
				return m, nil
			}
			if cmd := m.beginClipboardImagePaste(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return m, finalize(m, cmds)
		}
		// Shift+Insert is the classic terminal paste key. Most terminals
		// intercept it themselves and deliver the clipboard text as bracketed
		// paste (tea.PasteMsg); some forward the key sequence instead (e.g. via
		// the kitty keyboard protocol). Bind it explicitly so paste works
		// either way — same native-clipboard read path as right-click, so SSH
		// sessions get the same remote hint and never read the remote host's
		// clipboard.
		if msg.String() == "shift+insert" {
			cmds = append(cmds, pasteClipboardText())
			return m, finalize(m, cmds)
		}
		// Mode shortcuts share one dispatcher so terminal-specific Shift+Tab
		// encodings and Ctrl+Y stay consistent without duplicating state logic.
		if m.handleModeShortcut(msg.String()) {
			return m, nil
		}
		switch m.endSlashArgSnapshotForKey(msg.String()) {
		case "esc":
			// "Back out" of the most specific in-progress state: un-send a just-sent
			// turn (server not yet replied), cancel a streaming turn, or clear
			// typed-but-unsent input. Mode switches (normal/plan/YOLO) are
			// exclusively driven by Shift+Tab — Esc must not silently flip a
			// session from plan or YOLO back to a less-permissive mode. PR #3051
			// removed the YOLO half of this; plan mode was missed and is fixed
			// here. Scrollback is the terminal's now, so there's no viewport to
			// dismiss.
			switch {
			case m.maintenanceCancellable():
				m.stopMaintenance()
			case m.maintenance != nil:
				// A projection already being saved cannot be rolled back. Keep
				// the draft intact while the authoritative operation settles.
			case m.state == tuiRunning && m.bubblePending:
				m.unsendPending()
			case m.state == tuiRunning:
				m.ctrl.Cancel()
				// Defensive: if the controller is no longer running (cancel
				// completed synchronously, e.g. for shell commands), transition
				// to idle immediately instead of waiting for TurnDone.
				if !m.ctrl.Running() {
					m.state = tuiIdle
					m.confirmBubbleSent()
					m.noteWatchdogIdle()
				}
			default:
				// Idle (any mode): a double-Esc on an empty composer opens the
				// rewind picker (Claude Code's gesture); a first Esc just arms
				// it. Non-empty input clears as before.
				if strings.TrimSpace(m.input.Value()) == "" {
					if !m.lastEsc.IsZero() && time.Since(m.lastEsc) < 600*time.Millisecond {
						m.lastEsc = time.Time{}
						m.openRewind()
					} else {
						m.lastEsc = time.Now()
					}
				} else {
					m.resetComposerInput()
					m.pastedBlocks = nil
				}
			}
			return m, nil
		case "ctrl+insert":
			// Terminal-convention copy without Ctrl+C's destructive side
			// effects: copy an active selection if there is one, otherwise do
			// nothing (no clear-input, no cancel, no quit). The selection lives
			// in-app because Reasonix owns the mouse, so the terminal's own
			// Ctrl+Insert (which copies the terminal selection) would see an
			// empty one.
			if sel.active && !sel.empty() {
				m.sel = sel // restore so selectedText() can read it
				text := m.selectedText()
				m.sel = selection{}
				cmds = append(cmds, m.copySelectionWithNotice(text))
				return m, finalize(m, cmds)
			}
			return m, nil
		case "ctrl+c", "super+c", "meta+c":
			if m.state == tuiRunning {
				// Selection takes precedence: copy instead of cancel, same as idle.
				if sel.active && !sel.empty() {
					m.sel = sel
					text := m.selectedText()
					m.sel = selection{}
					cmds = append(cmds, m.copySelectionWithNotice(text))
					return m, finalize(m, cmds)
				}
				if m.bubblePending {
					m.unsendPending() // server not yet replied — restore text, leave no trace
				} else if m.cancelRequested() {
					m.ctrl.Cancel()
					return m, shutdownNow
				} else {
					m.ctrl.Cancel()
				}
				return m, nil
			}
			// Idle: an active text selection takes precedence over the
			// composer-clear / double-press-quit gestures. Standard terminal
			// convention is "Ctrl+C copies the selection" — the user can still
			// clear the input with a second Ctrl+C once the selection is gone.
			// Hoisting this branch above the clear branch also stops the
			// previous behaviour where Ctrl+C would dismiss a selection AND
			// wipe any draft text the user was typing — felt like the
			// selection was being silently lost.
			if sel.active && !sel.empty() {
				m.sel = sel // restore so selectedText() can read it
				text := m.selectedText()
				m.sel = selection{}
				cmds = append(cmds, m.copySelectionWithNotice(text))
				return m, finalize(m, cmds)
			}
			// No selection: if the composer has text, a single press clears it
			// (like Esc); on an empty composer a double-press within 1.5s quits.
			if strings.TrimSpace(m.input.Value()) != "" {
				m.resetComposerInput()
				m.pastedBlocks = nil
				m.lastCtrlCAt = time.Time{}
				return m, nil
			}
			if !m.lastCtrlCAt.IsZero() && time.Since(m.lastCtrlCAt) < 1500*time.Millisecond {
				return m, shutdownNow
			}
			m.lastCtrlCAt = time.Now()
			m.notice(i18n.M.CtrlCQuitHint)
			return m, finalize(m, nil)
		case "ctrl+d":
			// Compatible Ctrl+D: forward-delete when the composer has any
			// raw content (including whitespace-only); only quit when idle
			// with a truly empty composer (bash/readline-style EOF).
			if m.input.Value() != "" {
				// Delegate to textarea DeleteCharacterForward (bound to
				// ctrl+d by default) so mid-line forward delete works.
				var ic tea.Cmd
				m.input, ic = m.input.Update(msg)
				if ic != nil {
					cmds = append(cmds, ic)
				}
				m.growInputToFit()
				m.updateCompletion()
				return m, finalize(m, cmds)
			}
			if m.state == tuiIdle {
				return m, shutdownNow
			}
			return m, nil
		case "ctrl+l":
			if m.state != tuiRunning {
				m.finalizeStreamed()
				m.clearTranscriptDisplay()
				m.commitTranscriptSource(transcriptSource{kind: transcriptSourceBanner})
				m.transcriptDirty = true
				m.forceGotoBottom = true
				m.notice(i18n.M.SlashClsDone)
			}
			return m, finalize(m, cmds)
		case "ctrl+o":
			m.toggleVerboseReasoning(m.state != tuiRunning)
			return m, finalize(m, cmds)
		case "ctrl+b":
			m.toggleShellOutput()
			return m, finalize(m, cmds)
		case "ctrl+enter":
			// Durable mid-turn steer (terminals without modified Enter use /steer).
			if m.state == tuiRunning {
				line := strings.TrimSpace(m.input.Value())
				if line == "" {
					return m, nil
				}
				// Local /queue always, even while running.
				if handled, msg := m.handleQueueSlash(line); handled {
					m.notice(msg)
					m.resetComposerInput()
					m.pastedBlocks = nil
					return m, finalize(m, cmds)
				}
				body := m.expandPastedBlocks(line)
				rec, err := m.enqueueSteer(body, body)
				if err != nil {
					m.notice("steer: " + err.Error())
					// Keep composer text on durable failure.
					return m, finalize(m, cmds)
				}
				switch rec.Disposition {
				case sessioninbox.DispositionSteerAccepted:
					m.notice(fmt.Sprintf("steer accepted #%s", shortID(rec.ItemID)))
				case sessioninbox.DispositionQueuedFollowup:
					m.notice(fmt.Sprintf("steer rejected — durable follow-up #%s", shortID(rec.ItemID)))
				default:
					m.notice(fmt.Sprintf("queued #%s", shortID(rec.ItemID)))
				}
				m.signalTeamInput(body)
				m.resetComposerInput()
				m.pastedBlocks = nil
				m.resetQueueNavigation()
				return m, finalize(m, cmds)
			}
		case "enter":
			if m.state == tuiRunning {
				line := strings.TrimSpace(m.input.Value())
				if line == "" {
					m.viewport.GotoBottom()
					m.markFollowTail()
					return m, nil
				}
				// /queue and /steer are local commands even mid-turn.
				if handled, msg := m.handleQueueSlash(line); handled {
					m.notice(msg)
					m.resetComposerInput()
					m.pastedBlocks = nil
					return m, finalize(m, cmds)
				}
				body := m.expandPastedBlocks(line)
				items := m.inboxPreviews()
				if m.queueEditCursor >= 0 && m.queueEditCursor < len(items) {
					id := items[m.queueEditCursor].ID
					if _, err := m.ctrl.UpdateInboxItem(id, body, body, body); err != nil {
						m.notice("queue update: " + err.Error())
						return m, finalize(m, cmds)
					}
					m.notice(fmt.Sprintf("queue [%d] updated", m.queueEditCursor+1))
					m.resetQueueNavigation()
				} else {
					rec, err := m.enqueueFollowup(body, body)
					if err != nil {
						m.notice("queue: " + err.Error())
						// Keep composer text on durable failure / capacity.
						return m, finalize(m, cmds)
					}
					m.notice(fmt.Sprintf("durable follow-up queued #%s — will run when idle", shortID(rec.ItemID)))
					m.signalTeamInput(body)
					m.resetQueueNavigation()
				}
				m.resetComposerInput()
				m.pastedBlocks = nil
				return m, finalize(m, cmds)
			}
			if m.modelSwitchPending {
				return m, nil // ignore Enter while /model switch is building
			}
			line := strings.TrimSpace(m.input.Value())

			if line == "" {
				m.viewport.GotoBottom()
				m.markFollowTail()
				return m, nil
			}
			if line == "exit" || line == "quit" || line == ":q" {
				return m, shutdownNow
			}
			if m.reclaimBlocksInput(line) {
				return m, finalize(m, cmds)
			}
			// /queue and /steer are local even when idle (never model-prompted).
			if handled, msg := m.handleQueueSlash(line); handled {
				m.notice(msg)
				m.resetComposerInput()
				m.pastedBlocks = nil
				return m, finalize(m, cmds)
			}
			m.rememberSubmittedInput(line)

			// "# <note>" quick-adds a memory line locally, no model turn. The
			// space keeps "#7" / "#issue" prompts from being swallowed.
			if note, ok := control.MemoryQuickAddNote(line); ok {
				m.resetComposerInput()
				m.pastedBlocks = nil
				if note == "" {
					m.notice(i18n.M.QuickRememberEmpty)
				} else if path, err := m.ctrl.QuickAdd(memory.ScopeProject, note); err != nil {
					m.notice("memory: " + err.Error())
				} else {
					m.notice(fmt.Sprintf(i18n.M.QuickRememberDoneFmt, path))
				}
				return m, finalize(m, cmds)
			}

			// "!<cmd>" runs a shell command directly, bypassing the model.
			if after, ok := strings.CutPrefix(line, "!"); ok {
				cmd := after
				if strings.TrimSpace(cmd) == "" {
					m.resetComposerInput()
					m.pastedBlocks = nil
					m.notice(i18n.M.ShellExecEmpty)
					return m, finalize(m, cmds)
				}
				m.resetComposerInput()
				m.pastedBlocks = nil
				m.state = tuiRunning
				m.runStart = time.Now()
				m.elapsed = 0
				m.turnTokens = 0
				m.pendingRestore = line
				m.bubbleStartIdx = len(m.transcript)
				m.commitLine("")
				m.commitTranscriptSource(transcriptSource{
					kind: transcriptSourceUser, raw: line, planMode: m.planMode,
				})
				m.bubblePending = true
				m.turnDiscarded = false
				m.confirmBubbleSent() // shell events arrive instantly
				m.noteWatchdogRunning()
				m.ctrl.RunShell(cmd)
				return m, m.startRunningTicks()
			}

			// Slash commands run locally without going through the model. A
			// '/'-leading line that's actually a dragged file path is an attachment,
			// not a command, so it's rewritten to an @reference instead.
			if control.SlashCodeCommentLine(line) {
				// Slash-prefixed code comments are prompt text, not commands.
				// Not a command. Fall through to normal message path.
			} else if strings.HasPrefix(line, "/") {
				if ref, ok := control.FileRefLine(line); ok {
					line = ref
				} else {
					m.resetComposerInput()
					m.pastedBlocks = nil
					cmds = append(cmds, m.runSlashCommand(line))
					return m, finalize(m, cmds)
				}
			}

			sentLine := m.expandPastedBlocks(line)
			m.resetComposerInput()

			// @references (local files / MCP resources, including inline image
			// attachments) are resolved off the event loop by the controller; the turn
			// starts when they resolve (refsResolvedMsg).
			if m.ctrl.HasRefs(sentLine) {
				cmds = append(cmds, m.resolveRefs(sentLine, sentLine, line))
				return m, finalize(m, cmds)
			}

			// Keep the expanded paste content as the raw turn, not the folded label,
			// so downstream consumers never see just the placeholder label.
			cmds = append(cmds, m.startTurnWithRaw(sentLine, sentLine, line, sentLine))
			return m, finalize(m, cmds)
		}

	case agentEventMsg:
		e := event.Event(msg)
		drained := m.drainAgentEvents(e)
		cmds = append(cmds, waitForAgentEvent(m.eventCh))
		cmds = append(cmds, drained.cmds...)
		// A turn just spent tokens (and money) — refresh the balance readout and
		// the custom status line (its context/cost inputs just changed).
		if drained.turnDone {
			cmds = append(cmds, fetchBalance(m.ctrl))
			if c := m.runStatusline(); c != nil {
				cmds = append(cmds, c)
			}
			// Durable inbox dispatch is owned by the controller after TurnDone.
			// Reset local queue navigation when the snapshot changes.
			m.resetQueueNavigation()
			// A /reload typed while the turn ran fires now that the TUI may be
			// idle; the drain re-checks busy state (an inbox admission above or a
			// background job keeps it queued).
			if c := m.drainQueuedRuntimeReload(); c != nil {
				cmds = append(cmds, c)
			}
		}
		if drained.turnDone || drained.gitMaybeChanged {
			if c := m.refreshGitStatus(); c != nil {
				cmds = append(cmds, c)
			}
		}

	case balanceMsg:
		m.balance = msg.text

	case statuslineMsg:
		m.statuslineOut = msg.out

	case gitStatusMsg:
		m.gitStatus = msg.status

	case compactDoneMsg:
		if m.maintenance != nil && m.maintenance.OperationID == "" {
			m.maintenance = nil
		}
		if msg.err != nil && !m.compactLifecycleObserved {
			m.notice(fmt.Sprintf("%s: %v", i18n.M.SlashCompactFailed, msg.err))
		} else if msg.err == nil {
			m.followSessionLease()
		}
		m.compactLifecycleObserved = false
		m.compactCompatibilityPending = false

	case tuiShutdownMsg:
		return m.shutdownAndQuit(msg)

	case tuiSessionReclaimedMsg:
		return m.completeSessionReclaim()

	case turnModelSettingsMsg:
		return m, m.handleTurnModelSettings(msg)
	case modelSwitchMsg:
		cmds = append(cmds, m.handleModelSwitch(msg)...)

	case connectionCredentialSavedMsg:
		return m, m.handleConnectionCredentialSaved(msg)
	case connectionCredentialTestedMsg:
		m.handleConnectionCredentialTested(msg)
		return m, nil

	case promptResolvedMsg:
		switch {
		case msg.err != nil:
			m.commitLine(wrapForViewport(i18n.M.ErrorPrefix+" "+msg.err.Error(), m.width, activeCLITheme.warn))
		case strings.TrimSpace(msg.sent) == "":
			m.notice(i18n.M.SlashPromptEmpty)
		default:
			cmds = append(cmds, m.startTurn(msg.sent, msg.display, msg.display))
		}

	case extensionActionMsg:
		switch {
		case msg.err != nil:
			m.commitLine(wrapForViewport(i18n.M.ErrorPrefix+" "+msg.err.Error(), m.width, activeCLITheme.warn))
		case strings.TrimSpace(msg.message) != "":
			m.notice(msg.message)
		}

	case mcpExternalDoneMsg:
		m.handleMCPExternalDone(msg)

	case refsResolvedMsg:
		for _, e := range msg.errs {
			m.notice(e) // surface a fetch failure but still send the turn
		}
		sent := msg.sent
		if msg.block != "" {
			sent = "Referenced context:\n\n" + msg.block + "\n\n" + msg.sent
		}
		// raw = msg.display (the expanded paste content, without resolved @-ref
		// payloads) — NOT msg.restore (the folded label). See the non-refs branch
		// above for why raw needs the expansion.
		cmds = append(cmds, m.startTurnWithRaw(sent, msg.display, msg.restore, msg.display))

	case clipboardImageMsg:
		requests := max(m.clipboardImageRequests, 1)
		m.clipboardImagePending = false
		m.clipboardImageRequests = 0
		if msg.err != nil {
			// An empty image clipboard is the normal case for a text paste on
			// terminals that hand Ctrl+V to the application instead of pasting
			// themselves. Fall through to text rather than blocking the paste.
			if errors.Is(msg.err, control.ErrNoClipboardImage) {
				// Skip the fallback when the terminal already delivered a
				// bracketed paste for this key press; it owns the paste and
				// pasting again would duplicate the text.
				pending := pendingClipboardTextPastes(requests, m.clipboardImageTerminalPasteSeq, m.terminalPasteSeq)
				if pending > 0 {
					cmds = append(cmds, pasteClipboardTextGuarded(m.terminalPasteSeq, pending, msg.err))
				}
				break
			}
			m.notice(fmt.Sprintf(i18n.M.ClipboardImagePasteFailedFmt, sanitizeExternalDisplayText(msg.err.Error())))
			break
		}
		if m.teamOverlayModal() {
			break
		}
		imageBefore := m.input.Value()
		m.insertImageRef(msg.path)
		if shouldClearWideInputChange(imageBefore, m.input.Value()) {
			cmds = append(cmds, tea.ClearScreen)
		}

	case clipboardTextPasteMsg:
		return m.handleClipboardTextPaste(msg)

	case memberEventMsg:
		return m, m.handleMemberEvent(msg)
	case teamRosterRefreshMsg:
		return m, m.refreshTeamRoster(msg)
	case clipboardCopyMsg:
		if msg.statusHint && msg.seq != m.copyNoticeSeq {
			break
		}
		label := i18n.M.MouseCopiedHint
		if !msg.statusHint {
			label = i18n.M.SlashCopyDone
		}
		if msg.osc52 || msg.err != nil {
			label = i18n.M.ClipboardCopyOSC52Hint
			if msg.err != nil {
				label = i18n.M.ClipboardCopyFallbackHint
			}
			cmds = append(cmds, tea.SetClipboard(msg.text))
		}
		if msg.statusHint {
			m.copyNoticeText = label
			cmds = append(cmds, copyNoticeExpire(msg.seq))
		} else {
			m.notice(label)
		}

	case copyNoticeExpireMsg:
		if msg.seq == m.copyNoticeSeq {
			m.copyNoticeText = ""
		}

	case themeSweepTickMsg:
		if m.themeSweep != nil {
			if m.themeSweep.advance() {
				cmds = append(cmds, themeSweepTick())
			} else {
				m.themeSweep = nil
			}
		}

	case elapsedTickMsg:
		// The chain follows the armed generation, not the footer's state: see
		// elapsedTickLive for why a member switch must not stop the heartbeat.
		if msg.generation == m.elapsedTickGeneration && m.elapsedTickLive() {
			m.noteWatchdogHeartbeat("elapsed_tick")
			m.elapsedTickProgress()
			cmds = append(cmds, elapsedTick(msg.generation))
		}

	case spinner.TickMsg:
		if m.state == tuiRunning {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}
	}

	beforeInput := m.input.Value()
	if inputBeforeSelection != "" {
		beforeInput = inputBeforeSelection
	}
	var ic tea.Cmd
	m.input, ic = m.input.Update(msg)
	cmds = append(cmds, ic)
	m.growInputToFit()
	// Re-filter the autocomplete menu against the freshly-edited input.
	if _, ok := msg.(tea.KeyPressMsg); ok {
		m.updateCompletion()
	}
	if shouldClearWideInputChange(beforeInput, m.input.Value()) {
		cmds = append(cmds, tea.ClearScreen)
	}

	return m, finalize(m, cmds)
}

// bottomRows is the terminal-row height of the pinned bottom region: any open
// bottom panels (todo / approval / chooser / rewind / completion), the composer
// when visible, and the two fixed status rows. Full-screen managers such as MCP
// and skills normally render inside the main transcript area; in native
// scrollback mode they join the bottom rail because there is no main viewport.
func (m chatTUI) bottomRows() int {
	rows := 0
	for _, s := range []string{
		m.renderTodoPanel(),
		m.renderApprovalBanner(),
		m.renderChooser(),
		m.renderElicit(),
		m.renderRewind(),
		m.renderMCPImport(),
		m.renderResumePicker(),
		m.renderQuickPicker(),
		m.renderConnectionSetup(),
		m.renderCopyPicker(),
		m.renderTeamPicker(),
		m.renderCompletion(),
	} {
		if s != "" {
			rows += strings.Count(s, "\n") + 1
		}
	}
	// Remove the hardcoded working-line increment — it is counted inside
	// statusLineCount via computeStatusLineCount, which also accounts for
	// wrapping. The fallback to 2 (unwrapped) covers the initial frame and
	// tests that don't call Update first.
	if m.nativeScrollback {
		if main := m.renderMainManager(); main != "" {
			rows += strings.Count(main, "\n") + 1
		}
	}
	if footer := m.renderMainManagerFooter(); footer != "" {
		rows += strings.Count(footer, "\n") + 1
	}
	if !m.hideComposer() {
		rows += m.input.Height() + 2 + m.queueIndicatorRows()
	}
	if m.statusLineCount > 0 {
		return rows + m.statusLineCount
	}
	return rows + 2 // fallback for tests that don't set statusLineCount
}

// streamToolOutput appends a chunk of a running tool's output and re-renders its
// live block (the last toolStreamTailLines lines) under the tool card, opening
// the block on the first chunk. Mirrors streamReasoning.
func (m *chatTUI) streamToolOutput(id, chunk string) {
	if id == "" {
		return
	}
	if m.toolStreamID != id {
		// Switching to a different id means either:
		//   (a) the previous tool finished and a new one is starting — collapse
		//       the current id's live block, then append a fresh slot at the
		//       end of the transcript.
		//   (b) late ToolProgress for an earlier (already dispatched and
		//       possibly collapsed) tool — reuse the slot beginToolRunning
		//       already wrote for that id, so the live block stays directly
		//       under the earlier tool's card rather than stacking at the end.
		if existingIdx, ok := m.shellTranscriptIdx[id]; ok && existingIdx >= 0 && existingIdx < len(m.transcript) {
			// Stash the switched-away id's live count before resetting it;
			// its late ToolResult reads it back via toolLineCountByID.
			if m.toolStreamID != "" && m.toolStreamID != id {
				n := m.toolLineCount
				if m.toolPartial != "" {
					n++
				}
				if n > 0 {
					m.toolLineCountByID[m.toolStreamID] = n
				}
			}
			m.toolStreamID = id
			m.toolStreamIdx = existingIdx
			m.toolTail = m.toolTail[:0]
			m.toolPartial = ""
			m.toolLineCount = 0
		} else {
			// Unknown id: collapse the active stream (its live count is intact).
			m.collapseToolOutput(m.toolStreamID, "")
			m.toolStreamID = id
			m.toolTail = m.toolTail[:0]
			m.toolPartial = ""
			m.toolLineCount = 0
			if m.nativeScrollback {
				m.toolStreamIdx = -1
			} else {
				m.toolStreamIdx = len(m.transcript)
				m.commitConnectorBlock(nil)
			}
		}
	}
	// Accumulate full output for shell commands so Ctrl+B can expand it.
	if strings.HasPrefix(id, "shell-") {
		m.shellOutputs[id] += chunk
	}
	// Fold completed lines into the bounded tail; keep the trailing partial.
	data := m.toolPartial + chunk
	for {
		i := strings.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		m.pushToolLine(strings.TrimRight(data[:i], "\r"))
		data = data[i+1:]
	}
	m.toolPartial = data

	vis := m.toolTail
	if m.toolPartial != "" {
		vis = append(append([]string{}, m.toolTail...), m.toolPartial)
	}
	if m.nativeScrollback {
		return
	}
	lines := make([]string, len(vis))
	for i, ln := range vis {
		lines[i] = dim(clampPlain(ln, m.width-len([]rune(connector))))
	}
	m.rewriteConnectorBlock(m.toolStreamIdx, lines)
}

// beginToolRunning opens an empty live block under a just-dispatched tool card,
// keyed by the call id. tickToolRunning fills it with a "working · Ns" line each
// second; if the tool later streams output, streamToolOutput reuses the same
// block; collapseToolOutput closes it on the result.
func (m *chatTUI) beginToolRunning(id string) {
	if id == "" {
		return
	}
	m.toolStreamID = id
	m.toolTail = m.toolTail[:0]
	m.toolPartial = ""
	m.toolLineCount = 0
	// Clear accumulated output for this tool ID so a re-run (e.g. repeated
	// !pwd with the same "shell-pwd" id) doesn't append to old output.
	delete(m.shellOutputs, id)
	m.toolStreamStart = time.Now()
	m.toolStreamFrame = 0
	if m.nativeScrollback {
		m.toolStreamIdx = -1
		return
	}
	m.toolStreamIdx = len(m.transcript)
	m.commitConnectorBlock([]string{dim(fmt.Sprintf(i18n.M.ChatToolWorkingFmt, toolWorkingFrames[0], 0))})
	// Remember the transcript slot for this id so a late ToolProgress for a
	// previously dispatched (and possibly already collapsed) tool can reuse
	// it instead of appending a fresh slot at the end of the transcript. For
	// back-to-back tool calls this keeps each tool's live block directly
	// under its own card.
	m.shellTranscriptIdx[id] = m.toolStreamIdx
}

// handleApprovalKey resolves a pending approval from a keystroke and re-arms the
// listener. 1/y/Enter allows once and 2/a allows the exact scope for the rest
// of the session. Fresh two-choice prompts use 2 for deny, while n/Esc and
// legacy 4 still deny. Plan prompts use 1 to execute, 2/n/Esc to keep planning, and 3 to
// reject the pending plan and leave plan mode without executing it.
// Ctrl-C cancels the whole turn via the run context. For a plan approval
// (planApprovalTool), starting execution or explicitly exiting without execution
// drops the local [plan] tag and turns plan mode off on the controller.
func (m chatTUI) handleApprovalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if isRecoveryApprovalEvent(m.pendingApproval) {
		// Historical recovery requests are display-only. Escape and n dismiss the
		// compatibility record locally; no recovery RPC or tool replay is issued.
		if msg.String() == "esc" || strings.EqualFold(msg.String(), "n") {
			m.pendingApproval = nil
		}
		return m, nil
	}
	choices := approvalChoices(m.pendingApproval)
	answer := func(choice approvalChoice) (tea.Model, tea.Cmd) {
		allow, session := choice.allow, choice.allowForSession
		if m.pendingApproval.Tool == planApprovalTool && (allow || choice.exitPlan) {
			m.planMode = false
			m.ctrl.SetPlanMode(false)
		}
		m.ctrl.Approve(m.pendingApproval.ID, allow, session, false)
		m.pendingApproval = nil
		return m, nil
	}
	switch msg.String() {
	case "ctrl+c":
		m.ctrl.Cancel()
		return answer(approvalChoice{})
	case "up", "k", "ctrl+p":
		if m.approvalSelection < 0 && len(choices) > 0 {
			m.approvalSelection = 0
		} else if m.approvalSelection > 0 {
			m.approvalSelection--
		}
		return m, nil
	case "down", "j", "ctrl+n":
		if m.approvalSelection < len(choices)-1 {
			m.approvalSelection++
		}
		return m, nil
	case "enter":
		if m.approvalSelection >= 0 && m.approvalSelection < len(choices) {
			return answer(choices[m.approvalSelection])
		}
		return m, nil
	case "esc":
		return answer(approvalChoice{})
	}
	lower := strings.ToLower(msg.String())
	if len(lower) == 1 && lower[0] >= '1' && lower[0] <= '9' {
		idx := int(lower[0] - '1')
		if idx < len(choices) {
			return answer(choices[idx])
		}
		// Legacy muscle memory: tool approvals historically numbered deny as 4.
		// Honor 4 as deny even when the current prompt shows fewer rows, matching
		// the "legacy 4 still deny" contract in this function's doc comment.
		if lower == "4" {
			return answer(approvalChoice{})
		}
		return m, nil
	}
	switch lower {
	case "y":
		if len(choices) > 0 {
			return answer(choices[0])
		}
	case "a":
		for _, choice := range choices {
			if choice.allowForSession {
				return answer(choice)
			}
		}
	case "n":
		return answer(approvalChoice{})
	}
	return m, nil
}

func (m chatTUI) View() tea.View {
	if m.themeSweep != nil {
		v := tea.NewView(m.themeSweep.render())
		if !m.nativeScrollback {
			v.AltScreen = true
			v.MouseMode = m.overlayMouseMode()
		}
		return v
	}
	boxW := max(m.width, 10)
	hideComposer := m.hideComposer()
	shellMode := strings.HasPrefix(strings.TrimSpace(m.input.Value()), "!")
	cancelRequested := m.cancelRequested()
	var box string
	if !hideComposer {
		style := inputBoxStyle.Width(boxW)
		if shellMode {
			style = withThemeBorderFG(style, statusShellColor)
		}
		box = style.Render(m.renderComposerInput())
	}

	var modeTag string
	if shellMode {
		modeTag = modeTagStyle(statusShellColor, modeTagLight).Render("Shell")
	} else {
		background := statusAutoColor
		foreground := modeTagDark
		_, _, autoApprove := m.modeReads()
		switch {
		case autoApprove:
			background = statusYoloColor
			foreground = modeTagLight
		case m.planMode:
			background = statusPlanColor
			foreground = modeTagLight
		}
		modeTag = modeTagStyle(background, foreground).Render(m.modeTagText())
	}

	primaryStatus := m.appendTeamButton(m.primaryStatusLine(modeTag, shellMode, cancelRequested))
	// The spinning "thinking…" indicator is its own line ABOVE the input box (shown
	// only while a turn runs); the status/data rows stay below. This mirrors Claude
	// Code: live progress over the composer, shortcuts + stats under it.
	working := m.runningWorkingLine(cancelRequested, true)
	// Bottom region pinned under the transcript viewport: optional panels, the
	// composer when visible, then the two status rows. Its height feeds
	// transcriptHeight so the viewport above fills exactly the rest of the screen.
	var parts []string
	rowsAboveBox := 0 // terminal rows occupied by panels/working line before the composer
	if todo := m.renderTodoPanel(); todo != "" {
		parts = append(parts, todo)
		rowsAboveBox += strings.Count(todo, "\n") + 1
	}
	if banner := m.renderApprovalBanner(); banner != "" {
		parts = append(parts, banner)
		rowsAboveBox += strings.Count(banner, "\n") + 1
	}
	if card := m.renderChooser(); card != "" {
		parts = append(parts, card)
		rowsAboveBox += strings.Count(card, "\n") + 1
	}
	if card := m.renderElicit(); card != "" {
		parts = append(parts, card)
		rowsAboveBox += strings.Count(card, "\n") + 1
	}
	if card := m.renderRewind(); card != "" {
		parts = append(parts, card)
		rowsAboveBox += strings.Count(card, "\n") + 1
	}
	if card := m.renderMCPImport(); card != "" {
		parts = append(parts, card)
		rowsAboveBox += strings.Count(card, "\n") + 1
	}
	if card := m.renderResumePicker(); card != "" {
		parts = append(parts, card)
		rowsAboveBox += strings.Count(card, "\n") + 1
	}
	if card := m.renderQuickPicker(); card != "" {
		parts = append(parts, card)
		rowsAboveBox += strings.Count(card, "\n") + 1
	}
	if card := m.renderConnectionSetup(); card != "" {
		parts = append(parts, card)
		rowsAboveBox += strings.Count(card, "\n") + 1
	}
	if card := m.renderCopyPicker(); card != "" {
		parts = append(parts, card)
		rowsAboveBox += strings.Count(card, "\n") + 1
	}
	if card := m.renderTeamPicker(); card != "" {
		parts = append(parts, card)
		rowsAboveBox += strings.Count(card, "\n") + 1
	}
	if menu := m.renderCompletion(); menu != "" {
		parts = append(parts, menu)
		rowsAboveBox += strings.Count(menu, "\n") + 1
	}
	if m.nativeScrollback {
		if card := m.renderMainManager(); card != "" {
			parts = append(parts, card)
			rowsAboveBox += strings.Count(card, "\n") + 1
		}
	}
	// Layout: the working spinner (when running), then the composer when visible,
	// then the persistent status block. Wide terminals keep two information rows
	// separated by a quiet rule: interaction + model/profile, then flexible Git
	// + fixed telemetry. Narrow
	// terminals break only between those semantic groups. Padding to full width
	// prevents stale cells.
	if working != "" {
		parts = append(parts, workingStyle.Width(boxW).MaxWidth(boxW).Render(wrapStatusLine(working, boxW)))
		rowsAboveBox++
	}
	if footer := m.renderMainManagerFooter(); footer != "" {
		parts = append(parts, footer)
		rowsAboveBox += strings.Count(footer, "\n") + 1
	}
	statusBlock := m.renderStatusBlock(primaryStatus, boxW)
	if !hideComposer {
		if qi := m.renderQueueIndicator(); qi != "" {
			parts = append(parts, qi)
			rowsAboveBox += strings.Count(qi, "\n") + 1
		}
		parts = append(parts, box)
	}
	parts = append(parts, statusBlockStyle.Width(boxW).MaxWidth(boxW).Render(statusBlock))

	if m.nativeScrollback {
		v := tea.NewView(strings.Join(parts, "\n"))
		if !hideComposer {
			if cur := m.composerCursor(); cur != nil {
				cur.X += 1
				cur.Y += rowsAboveBox + 1
				v.Cursor = clampCursorToTerminal(cur, m.width, m.height)
			}
		}
		return v
	}

	// Full-screen frame: the transcript viewport on top (it pads to exactly its
	// height), the pinned bottom region beneath. Alt-screen owns the grid, so
	// resize repaints cleanly — no scrollback reflow, no ghost borders.
	mainArea := m.renderTranscript()
	if card := m.renderMainManager(); card != "" {
		mainArea = m.renderTranscriptWithMainManager(card)
	}
	v := tea.NewView(mainArea + "\n" + strings.Join(parts, "\n"))
	v.AltScreen = true
	// Modality, not the overlay's mere existence, decides capture: a bound member
	// session keeps clicks for the [ TEAM ]/member buttons; only the modal page
	// releases the mouse to the terminal (native selection, right-click menu).
	v.MouseMode = m.overlayMouseMode()
	// Anchor the real terminal cursor at the textarea's insertion point only when
	// the composer is visible. input.Cursor() is relative to the textarea; offset
	// by the viewport height + rows above + the box's top border row (+1 column
	// for PaddingLeft). Clamp to terminal bounds so VS Code fullscreen / resize
	// storms cannot leave the caret off-grid (#6282, #7236).
	if !hideComposer {
		if cur := m.composerCursor(); cur != nil {
			cur.X += 1
			cur.Y += m.viewport.Height() + rowsAboveBox + 1
			v.Cursor = clampCursorToTerminal(cur, m.width, m.height)
		}
	}
	return v
}

// ingestEvent routes one typed event from the agent. Reasoning (dim) and answer
// free-text accumulate in their live buffers; every other event first finalizes
// the reasoning and answer streamed so far, then commits its own line —
// preserving order. Switching on the event Kind replaces the old prefix-sniffing
// of a flattened byte stream: the structure is now explicit.

// runSlashCommand handles "/<cmd> <args>" input. Local commands queue their
// output to scrollback; MCP prompt / custom commands resolve to a model turn.
func (m *chatTUI) runSlashCommand(input string) tea.Cmd {
	typedCmd := strings.TrimSpace(strings.SplitN(input, " ", 2)[0])
	if notice := m.slashInputBlockedNotice(typedCmd); notice != "" {
		m.notice(notice)
		return nil
	}

	if strings.HasPrefix(typedCmd, "/mcp__") {
		return m.runMCPPrompt(input)
	}
	cmd := canonicalBuiltinSlashCommand(typedCmd)

	switch cmd {
	case control.RecoverContextCommand:
		id, guidance, _ := control.ParseProtocolRecoveryCommand(input)
		return m.startControllerTurn(input, input, func(ctrl control.SessionAPI) {
			if runner, ok := ctrl.(interface{ SubmitProtocolRecovery(string, string) }); ok {
				runner.SubmitProtocolRecovery(id, guidance)
			}
		})
	case control.ContinueChecksCommand:
		prompt, _ := control.ParseFinalReadinessRecoveryCommand(input)
		return m.startControllerTurn(input, input, func(ctrl control.SessionAPI) {
			ctrl.SubmitFinalReadinessRecovery(input, prompt)
		})
	case "/compact":
		return m.runCompactCommand(input, typedCmd)
	case "/context":
		return m.showContextReport(input)
	case "/new":
		m.echoLocalCommand(input)
		if err := m.ctrl.NewSession(); err != nil {
			m.notice(fmt.Sprintf("%s: %v", i18n.M.SlashNewFailed, err))
			return nil
		}
		m.followSessionLease()
		// Native scrollback keeps the old transcript; mark the fork with a fresh banner.
		m.resetFreshContextView(false)
		m.notice(i18n.M.SlashNewDone)
	case "/clear":
		m.echoLocalCommand(input)
		if m.ctrl.ToolApprovalMode() == control.ToolApprovalDangerFullAccess {
			// Full access is an explicit commitment to skip ordinary confirmations; /clear is
			// rarely mistyped and the damage is recoverable, so clear directly.
			return m.clearContext()
		} else {
			m.clearConfirm = &clearConfirm{confirm: 1}
		}
	case "/cls":
		m.echoLocalCommand(input)
		m.finalizeStreamed()
		m.clearTranscriptDisplay()
		m.commitLine(strings.TrimRight(
			renderTUIBanner(m.label, "", transcriptContentWidth(m.width, m.nativeScrollback)), "\n"))
		m.transcriptDirty = true
		m.forceGotoBottom = true
		m.notice(i18n.M.SlashClsDone)
	case "/resume":
		m.runResumeCommand(input)
	case "/takeover":
		m.runTakeoverCommand(input)
	case "/status":
		m.echoLocalCommand(input)
		m.showStatusDetails()
	case "/rename":
		m.runRenameCommand(input)
	case "/todo":
		m.echoLocalCommand(input)
		// Dismiss the bound session's list only — never a panel left behind by
		// another owner; a later committed write brings it back.
		m.todo.dismiss(m.sessionTodoOwner())
		m.notice(i18n.M.SlashTodoCleared)
	case "/verbose":
		m.toggleVerboseReasoning(true)
	case "/mouse":
		m.toggleMouseCapture()
	case "/sandbox":
		m.echoLocalCommand(input)
		m.showSandboxStatus()
	case "/effort":
		return m.runEffortCommand(input)
	case "/preset", "/work-mode", "/profile":
		m.echoLocalCommand(input)
		return m.runPresetCommand(input)
	case "/reasoning-language":
		m.echoLocalCommand(input)
		m.runReasoningLanguageCommand(input)
	case "/rewind":
		m.echoLocalCommand(input)
		m.openRewind()
	case "/tree":
		m.echoLocalCommand(input)
		m.showBranchTree()
	case "/branch":
		m.echoLocalCommand(input)
		m.runBranchCommand(input)
	case "/switch":
		m.echoLocalCommand(input)
		m.runSwitchCommand(input)
	case "/mcp":
		m.echoLocalCommand(input)
		m.runMCPSubcommand(input)
	case "/remote":
		m.echoLocalCommand(input)
		m.showRemoteHosts()
	case "/plugin", "/plugins":
		m.echoLocalCommand(input)
		m.runPluginSubcommand(input)
	case "/model":
		m.echoLocalCommand(input)
		m.runModelSubcommand(input)
		if m.pendingModelSwitch != nil {
			return m.pendingModelSwitch
		}
	case "/provider":
		m.echoLocalCommand(input)
		m.runProviderCommand(input)
		if m.pendingModelSwitch != nil {
			return m.pendingModelSwitch
		}
	case "/setup":
		m.echoLocalCommand(input)
		m.openConnectionSetup()
	case "/skill", "/skills":
		m.echoLocalCommand(input)
		m.runSkillSubcommand(input)
		if m.pendingModelSwitch != nil {
			return m.pendingModelSwitch
		}
	case "/hooks":
		m.echoLocalCommand(input)
		m.runHooksSubcommand(input)
	case "/reload-cmd":
		m.echoLocalCommand(input)
		if m.ctrl == nil {
			m.notice("controller not ready")
			return nil
		}
		if m.ctrl.Running() {
			m.notice("wait for the current turn to finish, then retry /reload-cmd")
			return nil
		}
		prev := len(m.commands)
		err := m.ctrl.ReloadCommands(context.Background())
		m.commands = m.ctrl.Commands()
		m.invalidateSlashCatalog()
		m.updateCompletion()
		if err != nil {
			m.notice("reload-cmd: " + err.Error())
			return nil
		}
		m.notice(fmt.Sprintf("commands reloaded: %d → %d commands", prev, len(m.commands)))

	case "/reload":
		m.echoLocalCommand(input)
		return m.runReloadCommand()

	case "/paste-image":
		return m.beginClipboardImagePaste()
	case "/output-style", "/output-styles":
		m.echoLocalCommand(input)
		styles := outputstyle.List(outputstyle.Dirs())
		if len(styles) == 0 {
			m.notice(i18n.M.OutputStyleNone)
		} else {
			m.commitLine(renderOutputStyles(m.width, styles, m.outputStyle))
		}
	case "/diff-fold":
		m.echoLocalCommand(input)
		if m.diffMaxLines == 0 {
			m.diffMaxLines = diffFoldLimit
			m.notice(fmt.Sprintf(i18n.M.DiffFoldEnabledFmt, diffFoldLimit))
		} else {
			m.diffMaxLines = 0
			m.notice(i18n.M.DiffFoldDisabled)
		}
	case "/theme":
		m.echoLocalCommand(input)
		return m.runThemeSubcommand(input)
	case "/language":
		m.echoLocalCommand(input)
		return m.runLanguageSubcommand(input)
	case "/currency":
		m.echoLocalCommand(input)
		return m.runCurrencySubcommand(input)
	case "/help", "/web":
		return m.runHelpOrWebSlash(input, typedCmd)
	case "/memory":
		m.echoLocalCommand(input)
		m.showMemory(input)
	case "/migrate", "/migration":
		m.echoLocalCommand(input)
		migration.RunLegacyRescueCommand(strings.TrimSpace(strings.TrimPrefix(input, typedCmd)), event.FuncSink(func(e event.Event) {
			if e.Kind == event.Notice {
				m.notice(e.Text)
			}
		}))
	case "/goal":
		return m.runGoalSubcommand(input)
	case "/remember":
		m.rememberNote(strings.TrimSpace(strings.TrimPrefix(input, typedCmd)))
	case "/quit", "/exit":
		return shutdownNow
	case "/copy":
		return m.runCopyCommand(input)
	case "/export":
		m.runExportCommand(input)
	case "/forget":
		m.forgetMemory(strings.TrimSpace(strings.TrimPrefix(input, typedCmd)))
	default:
		return m.runUnrecognizedSlash(input, typedCmd, cmd)
	}
	return nil
}
