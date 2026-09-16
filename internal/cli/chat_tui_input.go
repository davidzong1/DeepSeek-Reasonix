package cli

import (
	"fmt"
	"reasonix/internal/config"
	"reasonix/internal/i18n"
	"reasonix/internal/plugin"
	"reasonix/internal/sessioninbox"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/lipgloss/v2"
)

func transcriptContentWidth(termW int, nativeScrollback bool) int {
	if !nativeScrollback {
		termW-- // reserve the last column for the transcript scrollbar
	}
	return max(termW, 1)
}

func configureChatTextarea(ti *textarea.Model) {
	// Keep a stable two-cell input affordance, matching the prompt treatment in
	// other coding TUIs. Continuation rows receive two spaces so text and the
	// real terminal cursor stay aligned without repeating the arrow.
	ti.SetPromptFunc(composerPromptWidth, func(info textarea.PromptInfo) string {
		if info.LineNumber != 0 {
			return ""
		}
		if info.Focused {
			return accent("❯ ")
		}
		return dim("❯ ")
	})
	ti.CharLimit = 16384
	// The prompt and real terminal cursor already show where typing starts. Keep
	// the idle composer quiet; modal free-text questions set their own temporary
	// placeholder through refreshInputPlaceholder.
	ti.Placeholder = ""
	ti.DynamicHeight = true
	ti.MinHeight = 1
	ti.MaxHeight = maxInputRows
	ti.MaxContentHeight = ti.CharLimit
	ti.SetHeight(1)
	ti.ShowLineNumbers = false
	applyTextareaTheme(ti)
	// Use the real terminal cursor (not a styled virtual one) so View can place
	// it at the insertion point and IME candidate windows anchor to the input.
	ti.SetVirtualCursor(false)
	// Plain Enter submits (the chatTUI handler intercepts it), so the textarea's
	// own InsertNewline binding moves to Alt+Enter / Ctrl+J / Shift+Enter.
	ti.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "ctrl+j", "shift+enter"))
	// bubbles binds word motion to Alt+arrows (the macOS convention); Windows and
	// Linux terminals send Ctrl+arrows for the same intent.
	ti.KeyMap.WordForward = key.NewBinding(key.WithKeys("alt+right", "alt+f", "ctrl+right"))
	ti.KeyMap.WordBackward = key.NewBinding(key.WithKeys("alt+left", "alt+b", "ctrl+left"))
	// Linux terminals send Ctrl+Backspace to delete the word behind the cursor.
	ti.KeyMap.DeleteWordBackward = key.NewBinding(key.WithKeys("alt+backspace", "ctrl+w", "ctrl+backspace"))
	ti.Focus()
}

func (m *chatTUI) refreshInputPlaceholder() {
	if m.chooserTyping() {
		m.input.Placeholder = i18n.M.AskTypeSomething
		return
	}
	m.input.Placeholder = ""
}

func (m *chatTUI) rememberSubmittedInput(input string) {
	if strings.TrimSpace(input) == "" {
		return
	}
	if len(m.submittedInputs) == 0 || m.submittedInputs[len(m.submittedInputs)-1] != input {
		m.submittedInputs = append(m.submittedInputs, input)
	}
	m.submittedInputCursor = -1
	m.submittedInputDraft = ""
}

func (m *chatTUI) recallSubmittedInput(delta int) bool {
	if len(m.submittedInputs) == 0 {
		return false
	}
	cursor := m.submittedInputCursor
	if cursor < 0 {
		if delta > 0 {
			return false
		}
		if m.input.Line() != 0 {
			return false // first-line Up enters history; lower lines navigate the draft
		}
		m.submittedInputDraft = m.input.Value()
		cursor = len(m.submittedInputs) - 1
	} else {
		cursor += delta
	}

	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(m.submittedInputs) {
		m.submittedInputCursor = -1
		m.input.SetValue(m.submittedInputDraft)
		m.growInputToFit()
		return true
	}
	m.submittedInputCursor = cursor
	m.input.SetValue(m.submittedInputs[cursor])
	m.growInputToFit()
	return true
}

func (m *chatTUI) resetSubmittedInputRecall() {
	m.submittedInputCursor = -1
	m.submittedInputDraft = ""
}

// navigateQueue moves through the durable inbox during tuiRunning.
// delta < 0 means ↑ (older), delta > 0 means ↓ (newer). Returns true if the
// input was updated. Bodies are loaded by ID only for the selected row.
func (m *chatTUI) navigateQueue(delta int) bool {
	items := m.inboxPreviews()
	if len(items) == 0 {
		return false
	}
	cursor := m.queueEditCursor
	if cursor < 0 {
		if delta > 0 {
			return false // already at "new draft" — nothing newer
		}
		// First ↑: save the current draft and jump to the last queued item.
		m.queueEditDraft = m.input.Value()
		cursor = len(items) - 1
	} else {
		cursor += delta
	}

	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(items) {
		// Past the end: restore the draft the user was composing.
		m.queueEditCursor = -1
		m.inboxSelectedID = ""
		m.input.SetValue(m.queueEditDraft)
		m.growInputToFit()
		return true
	}
	m.queueEditCursor = cursor
	m.inboxSelectedID = items[cursor].ID
	// Load body only for the selected item (edit path).
	if _, env, err := m.ctrl.ReadInboxItem(items[cursor].ID); err == nil {
		m.input.SetValue(env.SubmitText)
	} else {
		m.input.SetValue(items[cursor].Preview)
	}
	m.growInputToFit()
	return true
}

// resetQueueNavigation resets the queue browsing cursor so the user returns to
// normal input mode. Any in-progress edit is discarded (the queued item keeps
// its previous value).
func (m *chatTUI) resetQueueNavigation() {
	m.queueEditCursor = -1
	m.queueEditDraft = ""
	m.inboxSelectedID = ""
	m.queueConfirmDelete = false
}

// renderQueueIndicator renders up to three bounded inbox previews above the
// input when instructions are queued. Full bodies are never materialised here.
func (m chatTUI) renderQueueIndicator() string {
	items := m.inboxPreviews()
	if len(items) == 0 {
		return ""
	}
	queueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")) // dim grey
	highlightStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	var lines []string
	// Ordinary status: at most three rows; full list via /queue.
	limit := min(len(items), 3)
	for i := range limit {
		it := items[i]
		preview := it.Preview
		if preview == "" {
			preview = "(empty)"
		}
		// Already bounded by sessioninbox.PreviewText; cap display further.
		if r := []rune(preview); len(r) > 50 {
			preview = string(r[:47]) + "…"
		}
		cursor := " "
		style := queueStyle
		if m.queueEditCursor == i || m.inboxSelectedID == it.ID {
			cursor = "▸"
			style = highlightStyle
		}
		mark := ""
		switch it.State {
		case sessioninbox.StateUncertain:
			mark = " ?"
		case sessioninbox.StateBlocked:
			mark = " !"
		case sessioninbox.StateRunning, sessioninbox.StateSteerAccepted, sessioninbox.StateSteerConsumed:
			mark = " …"
		}
		lines = append(lines, style.Render(fmt.Sprintf("  %s [%d]%s %s", cursor, it.Pos, mark, preview)))
	}
	if more := len(items) - limit; more > 0 {
		lines = append(lines, queueStyle.Render(fmt.Sprintf("  … +%d more (/queue list)", more)))
	}
	if m.inboxSnap().Paused {
		lines = append(lines, queueStyle.Render("  ⏸ inbox paused (space to resume)"))
	}
	return strings.Join(lines, "\n")
}

// prompts returns the MCP prompts discovered at startup (nil when no plugins).
func (m *chatTUI) prompts() []plugin.Prompt {
	if m.host == nil {
		return nil
	}
	return m.host.Prompts()
}

// The composer grows with its content up to this comfort cap. The effective
// cap is lowered for short terminals by syncInputHeightLimit, after which the
// textarea scrolls internally and keeps the caret visible.
const maxInputRows = 8

const (
	composerBorderRows = 2
	minTranscriptRows  = 3
)

const foldedPasteMinChars = 1000

const foldedPasteMinLines = 5

type pastedBlock struct {
	label string
	text  string
	image bool // an image attachment: expands to its bare @ref, not a wrapped block
}

func (m *chatTUI) chooserTyping() bool {
	return m.chooser != nil && m.chooser.typing
}

// inputHeightLimit returns the number of visible textarea rows that fit without
// letting the complete composer block consume more than half the terminal or
// pushing the transcript below its minimum useful height. Panel and wrapped
// status rows are treated as fixed bottom chrome and remain outside the input
// viewport.
func (m chatTUI) inputHeightLimit() int {
	if m.height <= 0 {
		return maxInputRows
	}

	limit := maxInputRows
	// Match the bounded-composer convention used by other coding TUIs: borders
	// are part of the half-screen budget, not extra rows added afterward.
	halfScreen := max(1, m.height/2-composerBorderRows)
	limit = min(limit, halfScreen)

	// bottomRows includes the current composer. Remove it to get the fixed
	// panels/status budget, then reserve the input borders and a readable slice
	// of transcript. On extremely short terminals one editable row still wins.
	fixedBottomRows := m.bottomRows()
	if !m.hideComposer() {
		fixedBottomRows -= m.input.Height() + composerBorderRows
	}
	available := max(1, m.height-fixedBottomRows-composerBorderRows-minTranscriptRows)
	return max(1, min(limit, available))
}

func (m *chatTUI) syncInputHeightLimit() {
	limit := m.inputHeightLimit()
	if m.input.MaxHeight == limit {
		return
	}
	m.followComposerCursor()
	m.input.MaxHeight = limit
	// SetWidth recalculates DynamicHeight from the full soft-wrapped content,
	// clamping the visible viewport to the new limit while preserving the text.
	m.input.SetWidth(max(m.width-4, 1))
}

func (m *chatTUI) growInputToFit() {
	if m.input.DynamicHeight {
		return
	}
	lines := min(max(strings.Count(m.input.Value(), "\n")+1, 1), maxInputRows)
	if lines != m.input.Height() {
		m.input.SetHeight(lines)
	}
}

func (m chatTUI) desktopShortcutLayout() bool {
	return m.cfg != nil && m.cfg.UIShortcutLayout() == "desktop"
}

func (m *chatTUI) toggleVerboseReasoning(notify bool) {
	m.showReasoning = !m.showReasoning
	var saveErr error
	if m.cfg != nil {
		_ = m.cfg.SetShowReasoning(m.showReasoning)
		path := config.SourcePath()
		if path == "" {
			path = "reasonix.toml"
		}
		saveErr = config.EditConfigFile(path, func(cfg *config.Config) error {
			return cfg.SetShowReasoning(m.showReasoning)
		})
	}
	if !notify {
		return
	}
	suffix := ""
	if saveErr != nil {
		suffix = "\npreference was not saved: " + saveErr.Error()
	}
	if m.showReasoning {
		m.notice("verbose on — thinking text will be shown" + suffix)
	} else {
		m.notice("verbose off — thinking text will stay collapsed" + suffix)
	}
}

// toggleMouseCapture flips whether Reasonix owns the mouse. It's session-only
// (unlike /verbose, this accommodates the terminal/multiplexer at hand rather
// than recording a lasting preference) — mirrors nativeScrollback, which is
// likewise never persisted to config. Clears any in-app selection/scrollbar
// drag in flight so a stale one can't be found mid-gesture once the terminal
// starts intercepting the events that would have finished it.
func (m *chatTUI) toggleMouseCapture() {
	m.mouseCaptureOff = !m.mouseCaptureOff
	m.sel = selection{}
	m.composerSel = composerSelection{}
	m.scrollbarDrag = false
	m.autoScroll = 0
	if m.mouseCaptureOff {
		m.notice(i18n.M.MouseCaptureOffHint)
	} else {
		m.notice(i18n.M.MouseCaptureOnHint)
	}
}

// unsendPending "un-sends" the in-flight turn while the server hasn't replied yet
// (bubblePending): it pops the echoed bubble back off the transcript, restores the
// just-sent text to the input box, and cancels the request — marking the turn
// discarded so its already-buffered events reach nothing. Once a packet has arrived
// the bubble is confirmed and this path isn't taken (Esc cancels normally instead).
func (m *chatTUI) unsendPending() {
	m.input.SetValue(m.pendingRestore)
	m.growInputToFit()
	m.truncateTranscriptBlocks(m.bubbleStartIdx)
	m.transcriptDirty = true
	m.bubblePending = false
	m.pendingRestore = ""
	m.pendingPastes = nil
	m.turnDiscarded = true
	m.ctrl.Cancel()
}
