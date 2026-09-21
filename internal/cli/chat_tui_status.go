package cli

import (
	"fmt"
	"path/filepath"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/i18n"
	"reasonix/internal/provider"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// clampCursorToTerminal keeps the reported caret inside [0,w) × [0,h).
func clampCursorToTerminal(cur *tea.Cursor, width, height int) *tea.Cursor {
	if cur == nil {
		return nil
	}
	if width > 0 {
		if cur.X < 0 {
			cur.X = 0
		}
		if cur.X >= width {
			cur.X = width - 1
		}
	}
	if height > 0 {
		if cur.Y < 0 {
			cur.Y = 0
		}
		if cur.Y >= height {
			cur.Y = height - 1
		}
	}
	return cur
}

// compactionCardLines renders a finished compaction as a titled card: a header
// with the message count and trigger, then the structured summary under a dim
// gutter so it reads as one block in scrollback. The summary is also the new
// context base, so this card is the user's window into exactly what was kept.
func compactionCardLines(c event.Compaction) []string {
	trigger := c.Trigger
	switch c.Trigger {
	case "auto":
		trigger = i18n.M.CompactionAuto
	case "manual":
		trigger = i18n.M.CompactionManual
	}
	header := fmt.Sprintf("%s · %d %s · %s", i18n.M.CompactionTitle, c.Messages, i18n.M.CompactionUnit, trigger)
	lines := []string{accent("◆ " + header)}
	for ln := range strings.SplitSeq(strings.TrimRight(c.Summary, "\n"), "\n") {
		lines = append(lines, dim("  │ "+ln))
	}
	if c.Archive != "" {
		lines = append(lines, dim("  │ archived "+c.Archive))
	}
	return lines
}

// The status band's controller reads are guarded per tag rather than once for
// the whole band. The band has two consumers that must agree — View() renders
// it, computeStatusLineCount reserves its height for bottomRows() — and a window
// with no usable controller is a real state: a team overlay opens degraded when
// a member backend fails to assemble, and the submit path treats that as a
// recoverable refusal (turn_lifecycle). So these answer zero values instead of
// letting View dereference a nil or typed-nil SessionAPI on a frame it still has
// to draw.
//
// One guard per tag, not one for the band: a backend that answers some reads and
// not others (a partial host, a typed-nil getter) must blank only the tag it
// broke, and the recover must not hide which read was broken behind three
// unrelated tags going empty together. The recover mirrors controllerRunning,
// which exists for the same malformed-host state.
func (m chatTUI) contextReads() (used, window int, ratio float64) {
	if m.ctrl == nil {
		return 0, 0, 0
	}
	defer func() {
		if recover() != nil {
			used, window, ratio = 0, 0, 0
		}
	}()
	used, window = m.ctrl.ContextSnapshot()
	return used, window, m.ctrl.CompactRatio()
}

// cacheReads returns the provider usage the cache tags report.
func (m chatTUI) cacheReads() (usage *provider.Usage, hit, miss int) {
	if m.ctrl == nil {
		return nil, 0, 0
	}
	defer func() {
		if recover() != nil {
			usage, hit, miss = nil, 0, 0
		}
	}()
	usage = m.ctrl.LastUsage()
	hit, miss = m.ctrl.SessionCache()
	return usage, hit, miss
}

// jobsRead counts the background jobs the jobs tag reports.
func (m chatTUI) jobsRead() (n int) {
	if m.ctrl == nil {
		return 0
	}
	defer func() {
		if recover() != nil {
			n = 0
		}
	}()
	return len(m.ctrl.Jobs())
}

// modeReads reads the state the mode tag renders: the goal badge, the approval
// preset, and the auto-approve colour View() picks from.
func (m chatTUI) modeReads() (goalRunning bool, toolApprovalMode string, autoApprove bool) {
	if m.ctrl == nil {
		return false, "", false
	}
	defer func() {
		if recover() != nil {
			goalRunning, toolApprovalMode, autoApprove = false, "", false
		}
	}()
	goalRunning = strings.TrimSpace(m.ctrl.Goal()) != "" && m.ctrl.GoalStatus() == control.GoalStatusRunning
	return goalRunning, m.ctrl.ToolApprovalMode(), m.ctrl.AutoApproveTools()
}

// contextTag renders the prompt-vs-context-window gauge for the status line,
// framed around the auto-compaction threshold: it shows how much headroom is
// left until the next compaction, and colours by proximity to that point rather
// than the raw window. Falls back to a plain percentage when compaction is disabled.
func (m chatTUI) contextTag() string {
	used, window, ratio := m.contextReads()
	if used == 0 || window == 0 {
		return ""
	}
	pct := used * 100 / window
	if ratio <= 0 || ratio >= 1 {
		// Compaction disabled: just the raw gauge, coloured on window fill.
		body := fmt.Sprintf("%s / %s ctx (%d%%)", shortTokens(used), shortTokens(window), pct)
		switch {
		case pct >= 85:
			return themeStyle(activeCLITheme.danger).Render(body)
		case pct >= 60:
			return themeStyle(activeCLITheme.warn).Render(body)
		default:
			return dim(body)
		}
	}
	threshold := int(ratio * 100)
	// Headroom to the compaction point, as a percentage of the window (clamped at 0).
	left := max(threshold-pct, 0)
	body := fmt.Sprintf("%s ctx (%d%%) · %d%% to compact", shortTokens(used), pct, left)
	switch {
	case pct >= threshold:
		return themeStyle(activeCLITheme.danger).Render(fmt.Sprintf("%s ctx (%d%%) · compacting soon", shortTokens(used), pct))
	case left <= 10:
		return themeStyle(activeCLITheme.warn).Render(body)
	default:
		return dim(body)
	}
}

func cacheRateLabel(format string, hit, denom int) string {
	if denom <= 0 {
		return ""
	}
	return fmt.Sprintf(format, fmt.Sprintf("%.2f%%", float64(hit)*100/float64(denom)))
}

// cacheTag renders both prompt cache-hit rates for the status line —
// "turn hit 88.00% · avg 78.00%": the single-turn rate (latest turn, the higher/steeper
// number on a non-compacting DeepSeek session) and the session-aggregate rate
// Σhit/Σ(hit+miss) (the steadier, cost-oriented number that matches the legacy
// dashboard). "" before any cache tokens have been reported.
func (m chatTUI) cacheStatus() (body string, rate float64, ok bool) {
	usage, hit, miss := m.cacheReads()
	now := ""
	nowRate := 0.0
	if u := usage; u != nil {
		// Only render when the provider actually reports cache token fields:
		// falling back to PromptTokens as the denominator painted a bogus
		// "turn hit 0.00%" for providers with no prompt-cache support.
		now = cacheRateLabel(i18n.M.ChatStatusCacheNowFmt, u.CacheHitTokens, u.CacheHitTokens+u.CacheMissTokens)
		if denom := u.CacheHitTokens + u.CacheMissTokens; denom > 0 {
			nowRate = float64(u.CacheHitTokens) * 100 / float64(denom)
		}
	}
	avg := ""
	avgRate := 0.0
	if hit+miss > 0 {
		avg = cacheRateLabel(i18n.M.ChatStatusCacheAvgFmt, hit, hit+miss)
		avgRate = float64(hit) * 100 / float64(hit+miss)
	}
	switch {
	case now != "" && avg != "":
		return now + " · " + avg, avgRate, true
	case now != "":
		return now, nowRate, true
	case avg != "":
		return avg, avgRate, true
	}
	return "", 0, false
}

func (m chatTUI) cacheTag() string {
	body, _, ok := m.cacheStatus()
	if !ok {
		return ""
	}
	return dim(body)
}

// jobsTag shows the count of running background jobs in the status line. Job
// start/finish emit Notices that arrive on eventCh and re-render the frame, so
// the count stays current without a dedicated tick.
func (m chatTUI) jobsTag() string {
	n := m.jobsRead()
	if n == 0 {
		return ""
	}
	return dim(fmt.Sprintf("⚙ %d", n))
}

func (m chatTUI) effortTag() string {
	if m.effortLevel == "" {
		return ""
	}
	value := footerValue(m.effortLevel)
	if m.effortLevel != "auto" {
		value = themeStyle(activeCLITheme.info).Bold(true).Render(m.effortLevel)
	}
	return footerMetric(i18n.M.ChatStatusEffortLabel, value)
}

// mouseTag is a persistent status-line marker while mouseCaptureOff is on, so
// the loss of in-app scrollbar/wheel-scroll/drag-select reads as a deliberate
// state rather than a bug the user has to guess at.
func (m chatTUI) mouseTag() string {
	if !m.mouseCaptureOff {
		return ""
	}
	return dim(i18n.M.MouseCaptureTag)
}

// shortTokens prints token counts compactly: 1_500 → "1.5K", 142_000 → "142.0K", 1_000_000 → "1.0M".
func shortTokens(n int) string {
	switch {
	case n >= 999_950:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// turnPhaseStatusLabel maps host turn_phase values to a short status label.
// Empty when the phase is unknown so callers fall back to the default thinking line.
func turnPhaseStatusLabel(phase string) string {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "working":
		return i18n.M.TurnPhaseWorking
	case "checking":
		return i18n.M.TurnPhaseChecking
	case "verifying":
		return i18n.M.TurnPhaseVerifying
	case "reviewing":
		return i18n.M.TurnPhaseReviewing
	default:
		return ""
	}
}

// formatCompletionSummaryLine renders a content-free quality summary for TUI scrollback.
func formatCompletionSummaryLine(c *event.CompletionSummaryInfo) string {
	if c == nil {
		return ""
	}
	verdict := strings.TrimSpace(c.Verdict)
	if verdict == "" {
		verdict = "complete"
	}
	line := fmt.Sprintf("%s · mut=%d · checks %d✓/%d✗/%d⊘",
		verdict, c.Mutations, c.ChecksPassed, c.ChecksFailed, c.ChecksSuppressed)
	if c.Review != "" && c.Review != "none" {
		line += " · review=" + c.Review
	}
	if len(c.GapKinds) > 0 {
		line += " · gaps=" + strings.Join(c.GapKinds, ",")
	}
	if c.ConstraintDegraded {
		line += " · constraints"
	}
	return line
}

func completionSummaryNeedsAttention(c *event.CompletionSummaryInfo, _ string) bool {
	if c == nil {
		return false
	}
	if strings.TrimSpace(c.Floor) != "" {
		return c.Attention
	}
	if strings.EqualFold(strings.TrimSpace(c.Verdict), "blocked") || c.ChecksFailed > 0 || c.ChecksSuppressed > 0 {
		return true
	}
	for _, gap := range c.GapKinds {
		switch strings.ToLower(strings.TrimSpace(gap)) {
		case "unbacked_claim", "failed_verification":
			return true
		}
	}
	return false
}

func completionSummaryWarning(c *event.CompletionSummaryInfo) string {
	if c != nil && strings.EqualFold(strings.TrimSpace(c.Verdict), "blocked") {
		return i18n.M.CompletionSummaryBlocked
	}
	return i18n.M.CompletionSummaryNeedsAttention
}

// computeStatusLineCount returns the number of terminal rows the status block
// (working line + first status line + optional data band) will occupy after
// wrapping to `width`. It mirrors the construction in View() so the reserved
// height matches the rendered height exactly — the load-bearing invariant for
// bottomRows().
// Use the same width (m.width) that View() passes to wrapStatusLine.
func (m chatTUI) computeStatusLineCount(width int) int {
	if m.ctrl == nil {
		return 3 // two information rows plus their divider
	}
	shellMode := strings.HasPrefix(strings.TrimSpace(m.input.Value()), "!")
	cancelRequested := m.cancelRequested()

	// Replicate the first status line (mode tag + state) from View().
	// ModeTag is rendered with Padding(0,1) in View() — add the same padding
	// here so the visible width matches exactly.
	modeTag := " " + m.modeTagText() + " "
	if shellMode {
		modeTag = " Shell "
	}
	primaryStatus := m.appendTeamButton(m.primaryStatusLine(modeTag, shellMode, cancelRequested))
	statusBlock := m.renderStatusBlock(primaryStatus, width)

	// Replicate the working (spinner) line from View(), shown only while a turn
	// runs or a controller-owned maintenance operation is active.
	working := m.runningWorkingLine(cancelRequested, false)

	// Count wrapped rows for every piece that View() renders as wrapped.
	var lines int
	if working != "" {
		// working (spinner) line — wraps independently of the status block below.
		lines += strings.Count(wrapStatusLine(working, width), "\n") + 1
	}
	lines += strings.Count(statusBlock, "\n") + 1
	return lines
}

func (m chatTUI) modeTagText() string {
	goalMode, toolApprovalMode, _ := m.modeReads()
	if m.desktopShortcutLayout() {
		switch {
		case m.planMode && toolApprovalMode == control.ToolApprovalDangerFullAccess:
			return "Plan+YOLO"
		case goalMode && toolApprovalMode == control.ToolApprovalDangerFullAccess:
			return "Goal+YOLO"
		case toolApprovalMode == control.ToolApprovalDangerFullAccess:
			return "YOLO"
		case m.planMode:
			return "Plan"
		case goalMode && toolApprovalMode == control.ToolApprovalWorkspaceWrite:
			return "Goal+Workspace"
		case goalMode:
			return "Goal"
		case toolApprovalMode == control.ToolApprovalWorkspaceWrite:
			return "Workspace"
		case toolApprovalMode == control.ToolApprovalDontAsk:
			return "Read only"
		default:
			return "Read only"
		}
	}
	switch {
	case m.planMode && toolApprovalMode == control.ToolApprovalDangerFullAccess:
		return "Plan+YOLO"
	case m.planMode && toolApprovalMode == control.ToolApprovalWorkspaceWrite:
		return "Plan+Workspace"
	case goalMode && toolApprovalMode == control.ToolApprovalDangerFullAccess:
		return "Goal+YOLO"
	case goalMode && toolApprovalMode == control.ToolApprovalWorkspaceWrite:
		return "Goal+Workspace"
	case toolApprovalMode == control.ToolApprovalDangerFullAccess:
		return "YOLO"
	case toolApprovalMode == control.ToolApprovalWorkspaceWrite:
		return "Workspace"
	case toolApprovalMode == control.ToolApprovalDontAsk:
		return "Read only"
	case m.planMode:
		return "Plan"
	case goalMode:
		return "Goal"
	default:
		return "Read only"
	}
}

// activeConfigTag names the config file actually in effect. A ./reasonix.toml
// outranks the user-global file, so a session started in a directory holding
// one silently ignores global edits unless the source is visible (#3317).
func activeConfigTag() string {
	path := config.SourcePath()
	if path == "" {
		return "(defaults — no config file)"
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return displayPath(path)
	}
	return displayPath(abs)
}
