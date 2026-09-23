package cli

import (
	"fmt"
	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/i18n"
	"reasonix/internal/recovery"
	"reasonix/internal/tool"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// planApprovalTool is the Tool name the controller puts on the ApprovalRequest it
// emits to gate a plan (mirrors control's constant). The banner, status line, and
// approval handler key on it to render the plan-specific prompt and to keep the
// [plan] tag in sync when the user starts execution or exits without executing.
const planApprovalTool = "exit_plan_mode"

func isRecoveryApprovalEvent(a *event.Approval) bool {
	return a != nil && (a.Kind == recovery.ApprovalKindRecovery || a.Recovery != nil)
}

func isRecoveryPlanChangeApproval(a *event.Approval) bool {
	if !isRecoveryApprovalEvent(a) || a.Recovery == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(a.Recovery.ChangeKind)) {
	case string(recovery.ChangeStrategy), string(recovery.ChangeScope):
		return true
	default:
		return false
	}
}

func freshApprovalAllowsSession(toolName string) bool {
	return toolName == control.SandboxEscapeApprovalTool || toolName == control.ManagedConfigWriteApprovalTool
}

var (
	// Input box: only top + bottom borders, no sides. The concrete colors are
	// refreshed from the active CLI theme during startup.
	inputBoxStyle    lipgloss.Style
	todoPanelStyle   lipgloss.Style
	statusBlockStyle lipgloss.Style
	workingStyle     lipgloss.Style
)

func (m chatTUI) cancelRequested() bool {
	if m.state != tuiRunning || m.ctrl == nil {
		return false
	}
	return m.ctrl.CancelRequested()
}

func (m chatTUI) runningWorkingLine(cancelRequested, styled bool) string {
	if m.state != tuiRunning {
		if m.maintenance == nil {
			return ""
		}
		var label string
		switch strings.ToLower(strings.TrimSpace(m.maintenance.Activity)) {
		case "cancelling":
			label = i18n.M.CompactionStopping
		case "finalizing":
			label = i18n.M.CompactionSaving
		case "recovery_required":
			label = i18n.M.CompactionRecoveryRequired
		default:
			label = i18n.M.CompactionWorking
		}
		return fmt.Sprintf("  %s %s", m.spinner.View(), label)
	}
	if m.retryAttempt > 0 && !cancelRequested {
		if line, ok := m.waitingRecoveryLine(); ok {
			return line
		}

		return fmt.Sprintf("  "+i18n.M.ChatStatusRetryingFmt, m.spinner.View(), m.retryAttempt, m.retryMax)
	}

	var working string
	if cancelRequested {
		working = fmt.Sprintf("  "+i18n.M.ChatStatusCancellingFmt, m.spinner.View(), m.elapsed)
	} else {
		phaseLabel := m.readStatusLabel
		if phaseLabel == "" {
			phaseLabel = turnPhaseStatusLabel(m.turnPhase)
		}
		if phaseLabel != "" {
			working = fmt.Sprintf("  %s %s · %ds", m.spinner.View(), phaseLabel, m.elapsed)
		} else {
			working = fmt.Sprintf("  "+i18n.M.ChatStatusThinkingFmt, m.spinner.View(), m.elapsed)
		}
	}
	if m.turnTokens > 0 {
		working += " · ↓" + shortTokens(m.turnTokens)
	}
	if n := m.inboxQueuedCount(); n > 0 {
		var queued string
		if n == 1 {
			queued = " · ✎ 1 in inbox"
		} else {
			queued = fmt.Sprintf(" · ✎ %d in inbox", n)
		}
		if m.inboxSnap().Paused {
			queued += " (paused)"
		}
		if styled {
			working += dim(queued)
		} else {
			working += queued
		}
	}
	return working
}

// renderApprovalBanner is the slim notice shown above the input while a tool
// call (or a plan) awaits the user's decision.
func (m chatTUI) renderApprovalBanner() string {
	w := max(m.width, 10)
	if m.pendingApproval == nil {
		return ""
	}
	if isRecoveryApprovalEvent(m.pendingApproval) {
		return choicePanelStyle.Width(w).Render("ℹ Historical recovery record (retired). It cannot confirm or replay an operation.\n" + dim("Esc/n dismiss"))
	}
	var text string
	var planDetails []string
	if m.pendingApproval.Tool == planApprovalTool {
		text = i18n.M.PlanApprovalPrompt
	} else if isRecoveryPlanChangeApproval(m.pendingApproval) {
		text = i18n.M.RecoveryPlanDecisionPrompt
		if rec := m.pendingApproval.Recovery; rec != nil {
			if before := compactApprovalPlan(rec.PlanBefore); before != "" {
				planDetails = append(planDetails, fmt.Sprintf(i18n.M.RecoveryPlanBeforeFmt, truncateSubject(before, w)))
			}
			if after := compactApprovalPlan(rec.PlanAfter); after != "" {
				planDetails = append(planDetails, fmt.Sprintf(i18n.M.RecoveryPlanAfterFmt, truncateSubject(after, w)))
			}
		}
	} else {
		name, detail := approvalToolDetails(m.pendingApproval.Tool)
		subj := strings.TrimSpace(m.pendingApproval.Subject)
		full := subj
		if subj != "" {
			subj = " " + truncateSubject(subj, w)
		}
		text = strings.TrimSpace(fmt.Sprintf(i18n.M.ToolApprovalPromptFmt, name, subj, detail, ""))
		// A command clipped to one line can hide the part that matters — the
		// path being written, the flag that makes it destructive (#4682).
		if body := approvalSubjectBody(full, strings.TrimSpace(subj), w); body != "" {
			planDetails = append(planDetails, body)
		}
	}
	planDetails = append(planDetails, writeAccessBannerDetails(m.pendingApproval)...)
	if reason := strings.TrimSpace(m.pendingApproval.Reason); reason != "" {
		text += " · " + truncateSubject(reason, w)
	}
	if len(planDetails) > 0 {
		text += "\n" + strings.Join(planDetails, "\n")
	}
	var b strings.Builder
	b.WriteString("⏸ " + text + "\n")
	for i, choice := range approvalChoices(m.pendingApproval) {
		b.WriteString(rowLine(i == m.approvalSelection, i+1, "", choice.label, false) + "\n")
	}
	b.WriteString(dim("↑/↓ navigate · Enter select · y/a/p/n shortcuts"))
	return choicePanelStyle.Width(w).Render(b.String())
}

// maxApprovalSubjectLines bounds the expanded command so a heredoc cannot push
// the composer off screen.
const maxApprovalSubjectLines = 8

// approvalSubjectBody returns the full command wrapped over several lines when
// the banner's one-line preview had to clip it, or "" when the preview already
// showed everything.
func approvalSubjectBody(full, preview string, width int) string {
	full = strings.TrimSpace(full)
	if full == "" || full == preview {
		return ""
	}
	wrapWidth := max(width-4, 20)
	lines := strings.Split(wrapStatusLine(full, wrapWidth), "\n")
	if len(lines) > maxApprovalSubjectLines {
		lines = lines[:maxApprovalSubjectLines]
		lines[maxApprovalSubjectLines-1] = ansi.Truncate(lines[maxApprovalSubjectLines-1], wrapWidth-1, "") + "…"
	}
	return strings.Join(lines, "\n")
}

func compactApprovalPlan(plan string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(strings.TrimSpace(plan), "\n", " · ")), " ")
}

// approvalToolDetails turns provider-visible tool IDs into user-facing labels.
// MCP tools are advertised as mcp__<server>__<tool>; showing the short tool name
// first keeps the approval prompt readable while preserving the source.
func approvalToolDetails(toolName string) (name, detail string) {
	if toolName == agent.PlanModeReadOnlyCommandApprovalTool {
		return i18n.M.ApprovalToolLabelPlanModeReadOnly, fmt.Sprintf(i18n.M.ToolApprovalSourceFmt, i18n.M.ToolApprovalBuiltIn)
	}
	if toolName == control.SandboxEscapeApprovalTool {
		return i18n.M.ApprovalToolLabelSandboxEscape, fmt.Sprintf(i18n.M.ToolApprovalSourceFmt, i18n.M.ToolApprovalBuiltIn)
	}
	if toolName == control.ManagedConfigWriteApprovalTool {
		return i18n.M.ApprovalToolLabelConfigWrite, fmt.Sprintf(i18n.M.ToolApprovalSourceFmt, i18n.M.ToolApprovalBuiltIn)
	}
	if server, short, ok := tool.SplitMCPName(toolName); ok {
		lines := []string{}
		if strings.EqualFold(short, "understand_image") {
			lines = append(lines, i18n.M.ToolApprovalImageUse)
		}
		lines = append(lines, fmt.Sprintf(i18n.M.ToolApprovalSourceFmt, server))
		return short, strings.Join(lines, "\n")
	}
	return approvalToolLabel(toolName), fmt.Sprintf(i18n.M.ToolApprovalSourceFmt, i18n.M.ToolApprovalBuiltIn)
}

func approvalToolLabel(toolName string) string {
	switch toolName {
	case "bash", "pwsh", "powershell", "shell":
		return i18n.M.ApprovalToolLabelBash
	case "edit_file":
		return i18n.M.ApprovalToolLabelEditFile
	case "atomic_write":
		// The atomic writer covers both replacing a file and patching it, so its
		// approval prompt names the operation the user is authorizing rather than
		// borrowing the edit-only label.
		return i18n.M.ApprovalToolLabelEditFile
	case "write_file":
		return i18n.M.ApprovalToolLabelWriteFile
	case "multi_edit":
		return i18n.M.ApprovalToolLabelMultiEdit
	case "move_file":
		return i18n.M.ApprovalToolLabelMoveFile
	case "web_fetch":
		return i18n.M.ApprovalToolLabelWebFetch
	case "run_skill":
		return i18n.M.ApprovalToolLabelRunSkill
	case "remember":
		return i18n.M.ApprovalToolLabelRemember
	case "forget":
		return i18n.M.ApprovalToolLabelForget
	default:
		return toolName
	}
}

// todoPanelMaxRows caps how many task lines the pinned panel shows; a long list
// is truncated with a "+N more" footer so the bottom region stays compact.
const todoPanelMaxRows = 8

// renderTodoPanel renders the committed current-turn task list above the input.
// Completed lists remain inspectable until the next host turn boundary. The
// panel renders the mounted owner's list — see todoView for why the owner is
// part of the state rather than derived here.
func (m chatTUI) renderTodoPanel() string {
	todos := m.todo.todos
	if m.todo.dismissed || len(todos) == 0 {
		return ""
	}
	done := 0
	for _, t := range todos {
		if t.Status == "completed" {
			done++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", accent("To-dos"), dim(fmt.Sprintf("%d/%d", done, len(todos))))
	start, end := todoPanelWindow(todos)
	if start > 0 {
		b.WriteString(dim(fmt.Sprintf("  +%d above", start)) + "\n")
	}
	for _, t := range todos[start:end] {
		indent := "  "
		switch t.Status {
		case "completed":
			b.WriteString(indent + green("✔") + " " + dim(t.Content) + "\n")
		case "in_progress":
			b.WriteString(indent + yellow("▶ "+t.Content) + "\n")
		default:
			b.WriteString(indent + dim("○ "+t.Content) + "\n")
		}
	}
	if end < len(todos) {
		b.WriteString(dim(fmt.Sprintf("  +%d more", len(todos)-end)) + "\n")
	}
	return todoPanelStyle.Width(max(m.width, 10)).Render(strings.TrimRight(b.String(), "\n"))
}

func todoPanelWindow(todos []event.Todo) (int, int) {
	if len(todos) <= todoPanelMaxRows {
		return 0, len(todos)
	}
	active := -1
	for i, t := range todos {
		if t.Status == "in_progress" {
			active = i
			break
		}
	}
	if active < 0 {
		return 0, todoPanelMaxRows
	}
	start := max(active-todoPanelMaxRows/2, 0)
	if maxStart := len(todos) - todoPanelMaxRows; start > maxStart {
		start = maxStart
	}
	return start, start + todoPanelMaxRows
}

// truncateSubject trims a tool subject so the approval banner fits one line.
func truncateSubject(s string, width int) string {
	max := width - 28
	if max < 16 {
		max = 16
	}
	return ansi.Truncate(s, max, "…")
}

// wrapStatusLine wraps a status line to `width` visible columns, ANSI-aware,
// so text that exceeds one row flows onto additional lines instead of being
// truncated with an ellipsis. Wrapping is permissive — spaces are preferred
// break points — and works within the alt-screen view so there is no scrollback
// artifact.
func wrapStatusLine(s string, width int) string {
	if width <= 0 || s == "" {
		return s
	}
	return ansi.Hardwrap(s, width, true)
}
