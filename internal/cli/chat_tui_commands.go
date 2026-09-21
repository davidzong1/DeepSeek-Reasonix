package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/i18n"
	"reasonix/internal/provider"
	"reasonix/internal/sandbox"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// showStatusDetails keeps diagnostics available without permanently crowding
// the two-line composer footer.
func (m *chatTUI) showStatusDetails() {
	var lines []string
	lines = append(lines, viewHeader("%s", "Session status"))
	mode := "Workspace"
	if m.ctrl != nil {
		mode = m.modeTagText()
	}
	lines = append(lines, "  mode       "+mode)
	model := strings.TrimSpace(m.modelRef)
	if model == "" {
		model = strings.TrimSpace(m.label)
	}
	if model != "" {
		lines = append(lines, "  model      "+model)
	}
	if m.ctrl != nil {
		if tag := m.contextTag(); tag != "" {
			lines = append(lines, "  context    "+tag)
		}
	}
	if m.effortLevel != "" {
		// The persistent footer uses an uppercase semantic label. The expanded
		// diagnostic view keeps its sentence-like wording for readability.
		lines = append(lines, "  effort     effort "+m.effortLevel)
	}
	if m.ctrl != nil {
		if tag := m.cacheTag(); tag != "" {
			lines = append(lines, "  cache      "+tag)
		}
	}
	if tag := m.gitTag(); tag != "" {
		lines = append(lines, "  git        "+tag)
	}
	if m.ctrl != nil {
		if tag := m.jobsTag(); tag != "" {
			lines = append(lines, "  jobs       "+tag)
		}
	}
	if m.balance != "" {
		lines = append(lines, "  balance    "+m.balance)
	}
	if tag := m.mouseTag(); tag != "" {
		lines = append(lines, "  mouse      "+tag)
	}
	lines = append(lines, "  config     "+activeConfigTag())
	m.commitLine(strings.Join(lines, "\n"))
}

func (m *chatTUI) runGoalSubcommand(input string) tea.Cmd {
	cmd, ok := control.ParseGoalCommand(input)
	if !ok {
		m.echoLocalCommand(input)
		m.notice(i18n.M.GoalEmpty)
		return nil
	}
	switch m.noticeDeprecatedGoalBudget(cmd); cmd.Action {
	case control.GoalCommandSet:
		return m.setGoalCommand(cmd, input)
	case control.GoalCommandClear:
		m.echoLocalCommand(input)
		m.ctrl.ClearGoal()
		m.notice(i18n.M.GoalCleared)
	case control.GoalCommandPause:
		m.echoLocalCommand(input)
		if !m.ctrl.PauseGoal() {
			m.notice(i18n.M.GoalNotRunning)
		}
	case control.GoalCommandResume:
		m.echoLocalCommand(input)
		if !m.ctrl.ResumeGoal() {
			m.notice(i18n.M.GoalNotPaused)
		}
	default:
		m.echoLocalCommand(input)
		goal := m.ctrl.Goal()
		if strings.TrimSpace(goal) == "" {
			m.notice(i18n.M.GoalEmpty)
			break
		}
		m.notice(fmt.Sprintf(i18n.M.GoalCurrentFmt, goal))
		rt := m.ctrl.GoalRuntime()
		m.notice(fmt.Sprintf(i18n.M.GoalRuntimeFmt,
			rt.TurnsUsed, rt.RequestsUsed, rt.TokensUsed,
			control.GoalWorkDurationText(rt.WorkDurationMs)))
		if rt.LastReason != "" {
			m.notice(fmt.Sprintf("%s: %s", i18n.M.GoalRuntimeLastReason, rt.LastReason))
		}
		if rt.StopCause != "" {
			m.notice(fmt.Sprintf(i18n.M.GoalPausedFmt, rt.StopCause))
		}
	}
	return nil
}

// runCopyCommand copies the Nth-latest assistant message from the current turn
// (after the last user message) to the clipboard.
//
//   - "/copy"   — shows a numbered list of assistant messages to choose from.
//   - "/copy N" — copies the Nth message directly (1 = most recent).
//
// Counting does not cross user message boundaries.
func (m *chatTUI) runCopyCommand(input string) tea.Cmd {
	m.echoLocalCommand(input)
	// "/copy N" copies the Nth-newest assistant message directly (1 = most
	// recent), matching the picker's newest-first ordering. A bare "/copy"
	// (or a non-numeric argument) opens the interactive picker instead.
	arg := strings.TrimSpace(strings.TrimPrefix(input, "/copy"))
	if n, err := strconv.Atoi(arg); err == nil && n > 0 {
		msgs := chatUIDisplayHistory(m.ctrl)
		parts := copyAssistantParts(msgs)
		if len(parts) == 0 {
			m.notice(i18n.M.SlashCopyEmpty)
			return nil
		}
		// copyAssistantParts is oldest-first; index 0 of the reversed slice
		// is the most recent, so "/copy 1" = parts[len-1].
		idx := len(parts) - n
		if idx < 0 || idx >= len(parts) {
			m.notice(i18n.M.SlashCopyEmpty)
			return nil
		}
		return copyToClipboard(parts[idx])
	}
	m.openCopyPicker()
	return nil
}

// firstLine returns the first non-empty line of s, truncated to 80 runes.
func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			runes := []rune(t)
			if len(runes) > 80 {
				return string(runes[:77]) + "..."
			}
			return t
		}
	}
	return "..."
}

// runExportCommand exports the entire session as a markdown file, excluding
// system messages, reasoning/thinking content, and tool calls/results.
func (m *chatTUI) runExportCommand(input string) {
	m.echoLocalCommand(input)
	msgs := chatUIDisplayHistory(m.ctrl)
	if len(msgs) == 0 {
		m.notice(i18n.M.SlashExportEmpty)
		return
	}

	var b strings.Builder
	b.WriteString("# reasonix session\n\n")
	lastRole := provider.Role("")
	exportedMessages := 0
	for _, msg := range cliHistoryWithoutPinnedContextRevisions(msgs) {
		switch msg.Role {
		case provider.RoleUser:
			// Skip internal steer messages.
			if _, isSteer := agent.SteerText(msg.Content); isSteer {
				continue
			}
			content := exportUserContent(msg.Content)
			if content == "" {
				continue
			}
			if lastRole != provider.RoleUser {
				b.WriteString("## User\n\n")
			}
			b.WriteString(content)
			b.WriteString("\n\n")
			exportedMessages++
			lastRole = provider.RoleUser
		case provider.RoleAssistant:
			content := strings.TrimSpace(msg.Content)
			if content == "" {
				continue
			}
			if lastRole != provider.RoleAssistant {
				b.WriteString("## Assistant\n\n")
			}
			b.WriteString(content)
			b.WriteString("\n\n")
			exportedMessages++
			lastRole = provider.RoleAssistant
		}
	}
	if exportedMessages == 0 {
		m.notice(i18n.M.SlashExportEmpty)
		return
	}

	// Choose a filename. If the workspace has a root, save there; otherwise
	// the current directory. Use a timestamp-based name.
	dir := "."
	if m.ctrl != nil {
		if wr := m.ctrl.WorkspaceRoot(); wr != "" {
			dir = wr
		}
	}
	ts := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("session-%s.md", ts)
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		m.notice(fmt.Sprintf("%s: %v", i18n.M.SlashUnknown, err))
		return
	}
	m.notice(fmt.Sprintf(i18n.M.SlashExportDoneFmt, path))
}

func exportUserContent(content string) string {
	content = control.StripComposePrefixes(content)
	content = control.StripReferencedContextPrefix(content)
	return strings.TrimSpace(content)
}

func (m *chatTUI) echoLocalCommand(input string) {
	input = strings.TrimSpace(input)
	if input == "" {
		return
	}
	m.commitLine(dim("  › " + input))
}

// commandNames renders the custom command list for /help, "" when there are none.
func (m *chatTUI) commandNames() string {
	names := make([]string, 0, len(m.commands))
	for _, c := range m.commands {
		if !c.Hidden {
			names = append(names, "/"+c.Name)
		}
	}
	return strings.Join(names, " · ")
}

// showSandboxStatus displays the current sandbox configuration and whether
// the OS sandbox backend is available. It reads from the stored config so
// the user can inspect sandbox state without leaving the TUI (closes #3316).
func (m *chatTUI) showSandboxStatus() {
	if m.cfg == nil {
		m.notice("sandbox: config not loaded")
		return
	}
	bash := m.cfg.BashMode()
	network := m.cfg.Sandbox.Network
	available := sandbox.Available()
	roots := m.cfg.WriteRoots()

	var b strings.Builder
	b.WriteString("sandbox\n")
	b.WriteString("  phase 0  file-writer confinement\n")
	if len(roots) > 0 {
		fmt.Fprintf(&b, "    write_roots  %s\n", strings.Join(roots, ", "))
	}
	if m.cfg.Sandbox.WorkspaceRoot != "" {
		fmt.Fprintf(&b, "    workspace_root  %s\n", m.cfg.Sandbox.WorkspaceRoot)
	}
	if len(m.cfg.Sandbox.AllowWrite) > 0 {
		fmt.Fprintf(&b, "    allow_write  %s\n", strings.Join(m.cfg.Sandbox.AllowWrite, ", "))
	}
	b.WriteString("  phase 1  OS bash sandbox\n")
	fmt.Fprintf(&b, "    bash        %s", bash)
	if bash == "enforce" && !available {
		b.WriteString(" (unavailable: no OS sandbox on this host; bash execution is refused. " + sandbox.UnavailableRemediation() + ")")
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "    network     %v\n", network)
	m.notice(b.String())
}

// runMCPSubcommand handles "/mcp" (status), "/mcp add …" (connect a server live
// and persist it), and "/mcp remove <name>" (disconnect + drop from config). Add
// connects synchronously — like /compact, an explicit command may briefly block
// the UI while the handshake runs.
func (m *chatTUI) runMCPSubcommand(input string) {
	args := tokenizeArgs(input) // args[0] == "/mcp"
	if len(args) < 2 {
		m.openMCPManager("")
		return
	}
	switch args[1] {
	case "list", "ls":
		// The completion menu offers "list"; treat it as the status view (same as
		// the legacy /mcp output) rather than an unknown subcommand.
		m.showMCPStatus()
	case "show":
		if len(args) < 3 {
			m.notice("usage: /mcp show <name>")
			return
		}
		m.openMCPManager(args[2])
	case "tools":
		if len(args) < 3 {
			m.notice("usage: /mcp tools <name>")
			return
		}
		m.openMCPManager(args[2])
		if m.mcp != nil {
			m.mcp.stage = mcpStageTools
		}
	case "add":
		entry, err := parseMCPAdd(args[2:])
		if err != nil {
			m.notice(err.Error())
			return
		}
		n, err := m.ctrl.AddMCPServer(entry)
		if err != nil {
			m.notice("mcp add: " + err.Error())
			return
		}
		m.refreshHostAndInvalidateSlashCatalog()
		m.notice(fmt.Sprintf("connected %s — %d tools, saved to global config (available next message)", entry.Name, n))
	case "connect":
		if len(args) < 3 {
			m.notice("usage: /mcp connect <name>")
			return
		}
		n, err := m.ctrl.ConnectConfiguredMCPServer(args[2])
		if err != nil {
			m.notice("mcp connect: " + err.Error())
			return
		}
		m.refreshHostAndInvalidateSlashCatalog()
		m.notice(fmt.Sprintf("connected %s — %d tools (available next message)", args[2], n))
	case "remove", "rm":
		if len(args) < 3 {
			m.notice("usage: /mcp remove <name>")
			return
		}
		name := args[2]
		disconnected, err := m.ctrl.RemoveMCPServer(name)
		if err != nil {
			m.notice("mcp remove: " + err.Error())
			return
		}
		m.refreshHostAndInvalidateSlashCatalog()
		if disconnected {
			m.notice("disconnected " + name + " and removed it from config")
		} else {
			m.notice("removed " + name + " from config")
		}
	case "import":
		m.openMCPImportPicker()
	default:
		m.notice("unknown /mcp subcommand " + args[1] + " — try: /mcp, /mcp list, /mcp show, /mcp add, /mcp connect, /mcp import, /mcp remove")
	}
}

// showMCPStatus queues the connected MCP servers, their counts, and the prompt
// commands / resource refs they expose — the discovery surface for /mcp.
func (m *chatTUI) showMCPStatus() {
	if m.host == nil || (len(m.host.Servers()) == 0 && len(m.host.Failures()) == 0) {
		m.notice(i18n.M.SlashMCPNone)
		return
	}
	m.commitLine(renderMCPStatus(m.width, m.host.Servers(), m.host.Prompts(), m.host.Resources(), m.host.Failures(), m.host.CapabilityViews()))
}

// notice queues a dim informational line to scrollback.
func (m *chatTUI) notice(note string) {
	m.commitLine(dim("  · " + note))
}

// showRemoteHosts renders a read-only summary of configured remote hosts. The
// remote session lives in a `reasonix serve` on the remote host, so connecting
// happens from a terminal (`reasonix remote connect`), not inside this chat.
func (m *chatTUI) showRemoteHosts() {
	cfg, err := config.Load()
	if err != nil {
		m.notice(err.Error())
		return
	}
	if len(cfg.Remote.Hosts) == 0 {
		m.notice(i18n.M.RemoteNoHostsHint)
		return
	}
	var b strings.Builder
	for _, h := range cfg.Remote.Hosts {
		target := h.Host
		if h.User != "" {
			target = h.User + "@" + target
		}
		if h.Port != 0 && h.Port != 22 {
			target = fmt.Sprintf("%s:%d", target, h.Port)
		}
		fmt.Fprintf(&b, "  · %s  %s\n", h.Name, target)
	}
	fmt.Fprintf(&b, "  run `reasonix remote connect <name>` in a terminal to open the remote workspace")
	m.commitLine(dim(b.String()))
}

// resolveRefs resolves a line's @references off the event loop via the
// controller, delivering a refsResolvedMsg with the tagged context block.
func (m *chatTUI) resolveRefs(sent, display, restore string) tea.Cmd {
	return func() tea.Msg {
		block, errs := m.ctrl.ResolveRefs(context.Background(), sent)
		return refsResolvedMsg{sent: sent, display: display, restore: restore, block: block, errs: errs}
	}
}

// runMCPPrompt resolves a /mcp__server__prompt command off the event loop via
// the controller, delivering a promptResolvedMsg with the rendered prompt.
func (m *chatTUI) runMCPPrompt(input string) tea.Cmd {
	return func() tea.Msg {
		sent, found, err := m.ctrl.MCPPrompt(context.Background(), input)
		if !found {
			name := strings.TrimPrefix(strings.Fields(input)[0], "/")
			return promptResolvedMsg{display: input, err: fmt.Errorf("%s: /%s", i18n.M.SlashUnknown, name)}
		}
		return promptResolvedMsg{display: input, sent: sent, err: err}
	}
}

// runExtensionAction invokes one extension UI action off the event loop (the
// call is a blocking sidecar round-trip), delivering an extensionActionMsg
// whose message surfaces as a transcript notice.
func (m *chatTUI) runExtensionAction(name string, args map[string]string) tea.Cmd {
	return func() tea.Msg {
		message, err := m.ctrl.InvokeExtensionAction(context.Background(), name, args)
		return extensionActionMsg{message: message, err: err}
	}
}
