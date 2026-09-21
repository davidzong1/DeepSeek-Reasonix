package cli

import (
	"context"
	"encoding/json"
	"os"
	"reasonix/internal/command"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/hook"
	"reasonix/internal/plugin"
	"reasonix/internal/skill"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	tuiIdle tuiState = iota
	tuiRunning
)

type controllerBuildSpec struct {
	ModelRef         string
	ToolApprovalMode string
	PlanMode         bool
	EffortOverride   *string
}

func (m *chatTUI) runtimeSwitchBusy() bool {
	if m == nil || m.ctrl == nil {
		return false
	}
	status := m.ctrl.RuntimeStatus()
	return status.Running || status.PendingPrompt || status.BackgroundJobs > 0 || m.pendingApproval != nil || m.chooser != nil
}

// agentEventMsg is one typed event from the agent's run loop.
type agentEventMsg event.Event

// maxEventDrain caps how many buffered events one Update coalesces before
// yielding to render, so a sustained output flood still shows live progress.
const maxEventDrain = 512

const resetMouseTracking = ansi.ResetModeMouseX10 +
	ansi.ResetModeMouseNormal +
	ansi.ResetModeMouseHighlight +
	ansi.ResetModeMouseButtonEvent +
	ansi.ResetModeMouseAnyEvent +
	ansi.ResetModeMouseExtSgr +
	ansi.ResetModeMouseExtUtf8 +
	ansi.ResetModeMouseExtUrxvt +
	ansi.ResetModeMouseExtSgrPixel

// compactDoneMsg is the compatibility completion for controllers that do not
// support registered management submissions. SessionOperation owns the normal
// lifecycle and persistence path.
type compactDoneMsg struct{ err error }

// tuiShutdownMsg asks the live TUI model to persist its current controller and
// quit. It is injected from the signal handler so shutdown does not snapshot a
// stale controller captured before an in-TUI rebuild.
type tuiShutdownMsg struct {
	completion    *tuiShutdownCompletion
	userInitiated bool
}

// shutdownNow is the tea.Cmd every in-TUI quit gesture returns instead of
// tea.Quit. Routing through tuiShutdownMsg gives all exits the same
// finalization (Snapshot + lease follow); quitting directly would drop
// whatever the controller holds beyond the last snapshot (#5879).
func shutdownNow() tea.Msg { return tuiShutdownMsg{userInitiated: true} }

// elapsedTickMsg fires once a second while a turn runs, driving the "thinking
// Ns" counter in the status line. generation rejects a prior turn's timer.
type elapsedTickMsg struct{ generation uint64 }

// balanceMsg carries the result of an async wallet-balance fetch; text is the
// formatted readout ("" when none/failed).
type balanceMsg struct{ text string }

// statuslineMsg carries the latest custom status-line output (one line, ""
// when none/failed).
type statuslineMsg struct{ out string }

// gitStatusMsg carries the latest lightweight git readout for the built-in
// status line. Empty means "not a git worktree" or "git unavailable".
type gitStatusMsg struct{ status gitStatus }

// runStatusline runs the user's custom status-line command off the event loop,
// feeding it a small JSON context on stdin and returning its first stdout line.
// A no-op (nil) when no command is configured. Tight timeout so a slow script
// can't stall the UI; failures collapse to an empty line rather than an error.
func (m chatTUI) runStatusline() tea.Cmd {
	cmd := m.statuslineCmd
	if cmd == "" {
		return nil
	}
	used, window := m.ctrl.ContextSnapshot()
	cwd, _ := os.Getwd()
	payload, _ := json.Marshal(map[string]any{
		"model":         m.label,
		"contextUsed":   used,
		"contextWindow": window,
		"cwd":           cwd,
	})
	return func() tea.Msg { return statuslineMsg{out: runStatuslineCmd(cmd, string(payload))} }
}

const statuslineCommandTimeout = 2 * time.Second

// runStatuslineCmd runs a status-line command with the JSON context on stdin and
// returns its first stdout line (status lines are a single row). A tight timeout
// keeps a slow script from stalling the UI; any failure collapses to "".
func runStatuslineCmd(cmd, stdinPayload string) string {
	return runStatuslineCmdWithTimeout(cmd, stdinPayload, statuslineCommandTimeout)
}

func runStatuslineCmdWithTimeout(cmd, stdinPayload string, timeout time.Duration) string {
	res := hook.DefaultSpawner(context.Background(), hook.SpawnInput{
		Command: cmd,
		Stdin:   stdinPayload + "\n",
		Timeout: timeout,
	})
	out := strings.TrimSpace(res.Stdout)
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		out = strings.TrimSpace(out[:i])
	}
	return out
}

func (m chatTUI) refreshGitStatus() tea.Cmd {
	if m.statuslineCmd != "" {
		return nil
	}
	return fetchGitStatus()
}

// modelSwitchMsg carries the result of an async /model switch. A nil err means
// the new controller is ready in ctrl; label/commands/skills/host mirror the
// fields that runModelSubcommand used to set synchronously. oldCtrl is the
// previous controller that must be closed after the switch — its cleanup
// (SessionEnd hooks, plugin subprocess kill) is deferred to a tea.Cmd so it
// runs after the render completes, avoiding corruption of the terminal's raw
// mode that would occur if Close() were called from the build goroutine.
type modelSwitchMsg struct {
	resumeTurn    *controllerTurnIntent
	ref           string
	ctrl          control.SessionAPI
	oldCtrl       control.SessionAPI
	label         string
	commands      []command.Command
	skills        []skill.Skill
	host          *plugin.Host
	failurePrefix string
	successNotice string
	err           error
}

// fetchBalance queries the provider's wallet balance off the event loop. It's a
// no-op readout ("") when the provider declares no balance_url or the fetch
// fails, so the status line stays quiet rather than surfacing an error.
// Wallets are displayed in their original currencies; no conversion or sum is
// attempted when more than one currency is returned.
func fetchBalance(ctrl control.Status) tea.Cmd {
	return func() tea.Msg {
		b, err := ctrl.Balance(context.Background())
		if err != nil || b == nil {
			return balanceMsg{}
		}
		displayCurrency := ""
		if cfg, err := config.LoadForRootReadOnly("."); err == nil && cfg != nil {
			displayCurrency = cfg.ExplicitDisplayCurrency()
		}
		return balanceMsg{text: b.DisplayForCurrency(displayCurrency)}
	}
}

// promptResolvedMsg carries the result of fetching an MCP prompt (an async
// prompts/get). display is the command line echoed as the user bubble; sent is
// the rendered prompt text that becomes the model turn.
type promptResolvedMsg struct {
	display string
	sent    string
	err     error
}

// extensionActionMsg carries the result of invoking one extension UI action
// (an async extension/ui/action round-trip to the sidecar). The extension's
// (already redacted) message surfaces as a transcript notice.
type extensionActionMsg struct {
	message string
	err     error
}

// refsResolvedMsg carries the result of resolving the @references in a
// submitted line (async file reads / MCP resources/read).
type refsResolvedMsg struct {
	sent    string
	display string
	restore string
	block   string
	errs    []string
}

type clipboardImageMsg struct {
	path string
	err  error
}

// newChatTUI assembles the initial model. The controller has already been wired
// with an event sink that feeds eventCh; the TUI issues commands to it and
// renders the events it emits. Model identity, label, history, host, and commands
// are read from the controller, so explicit selections and resumed sessions stay
// authoritative.
func newChatTUI(ctrl control.SessionAPI, missing string, eventCh chan event.Event, termW int) chatTUI {
	ti := textarea.New()
	configureChatTextarea(&ti)

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = themeStyle(activeCLITheme.accent)

	commitBuf := []string{}
	nativeScrollback := detectTermuxTerminal()
	history := chatUIDisplayHistory(ctrl)
	nextPasteID, usedPasteIDs := pasteIDStateForHistory(history)
	return chatTUI{
		ctrl:                     ctrl,
		label:                    ctrl.Label(),
		modelRef:                 ctrl.ModelRef(),
		missing:                  missing,
		nativeScrollback:         nativeScrollback,
		legacyScrollClear:        useLegacyViewportScrollClear(runtime.GOOS, os.Environ()),
		mouseCaptureOff:          mouseCaptureOffByDefault(),
		input:                    ti,
		spinner:                  sp,
		submittedInputCursor:     -1,
		queueEditCursor:          -1,
		maintenanceTranscriptIdx: -1,
		maintenanceTerminal:      make(map[string]struct{}),
		maintenanceLatest:        make(map[string]event.SessionOperationInfo),
		nextPasteID:              nextPasteID,
		usedPasteIDs:             usedPasteIDs,
		reasoningLineIdx:         -1,
		reasoningTextIdx:         -1,
		answerIdx:                -1,
		toolStreamIdx:            -1,
		reasoning:                &strings.Builder{},
		pending:                  &strings.Builder{},
		pendingCommit:            &commitBuf,
		diffMaxLines:             diffFoldLimit,
		showReasoning:            nativeScrollback,
		showTurnUsage:            true,
		shellOutputs:             make(map[string]string),
		shellExpanded:            make(map[string]bool),
		shellTranscriptIdx:       make(map[string]int),
		toolLineCountByID:        make(map[string]int),
		subagentProgressIdx:      make(map[string]int),
		subagentProgress:         make(map[string]*cliSubagentProgress),
		eventCh:                  eventCh,
		history:                  history,
		host:                     ctrl.Host(),
		commands:                 ctrl.Commands(),
		skills:                   ctrl.SlashSkills(),
		viewport:                 viewport.New(viewport.WithWidth(termW)),
		statusLineCount:          3,
	}
}

func isTermuxTerminal() bool {
	if os.Getenv("TERMUX_VERSION") != "" || os.Getenv("TERMUX_APP_PID") != "" || os.Getenv("TERMUX__PREFIX") != "" {
		return true
	}
	return strings.Contains(os.Getenv("PREFIX"), "/com.termux/")
}

var detectTermuxTerminal = isTermuxTerminal

func (m chatTUI) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink, forceSyncOutputCmd(),
		waitForAgentEvent(m.eventCh), fetchBalance(m.ctrl),
		m.runStatusline(), // nil (no-op) unless a custom status line is configured
		m.refreshGitStatus(),
	)
}

func suspendWithMouseReset() tea.Cmd {
	return tea.Sequence(tea.Raw(resetMouseTracking), tea.Suspend)
}

var clearWideInputChanges = runtime.GOOS == "windows"

func shouldClearWideInputChange(before, after string) bool {
	return clearWideInputChanges &&
		before != after &&
		(hasWideInputCells(before) || hasWideInputCells(after))
}

func hasWideInputCells(s string) bool {
	return s != "" && visibleWidth(s) != utf8.RuneCountInString(s)
}

func waitForAgentEvent(ch chan event.Event) tea.Cmd {
	return func() tea.Msg { return agentEventMsg(<-ch) }
}

func elapsedTick(generation uint64) tea.Cmd {
	return tea.Tick(time.Second, func(_ time.Time) tea.Msg {
		return elapsedTickMsg{generation: generation}
	})
}
