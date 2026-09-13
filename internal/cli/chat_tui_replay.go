package cli

import (
	"fmt"
	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/i18n"
	"reasonix/internal/provider"
	"regexp"
	"strconv"
	"strings"
)

// replaySectionsFor turns a loaded session into scrollback blocks. Normal tool
// results remain quiet, while interrupted-turn reasoning and tool cards replay
// from provider-excluded LocalOnly records so restart matches the live view.
func replaySectionsFor(history []provider.Message, width int) []string {
	return replaySectionsForWithAssistantRenderer(history, width, renderAssistantMarkdown)
}

func replaySectionsForWithAssistantRenderer(
	history []provider.Message,
	width int,
	renderAssistant func(string, int) string,
) []string {
	return replaySectionsForWithRenderers(history, width, renderAssistant, reasoningBlock)
}

func replaySectionsForWithRenderers(
	history []provider.Message,
	width int,
	renderAssistant func(string, int) string,
	renderReasoning func(string, int, int) string,
) []string {
	var out []string
	for _, m := range cliHistoryWithoutPinnedContextRevisions(history) {
		if m.LocalOnly {
			if recovery, ok := provider.DecodeProtocolRecovery(m.ProtocolRecovery); ok && recovery.State == "pending" {
				out = append(out, fmt.Sprintf("  · %s: /recover-context %s\n\n", i18n.M.ProtocolRecoveryLabel, recovery.ID))
			}
			if m.FinalReadinessRecovery != nil && m.FinalReadinessRecovery.Pending {
				out = append(out, fmt.Sprintf("  · %s\n\n", i18n.M.FinalReadinessRecovery))
				continue
			}
			if reasoning := strings.TrimSpace(m.ReasoningContent); reasoning != "" {
				out = append(out, dim("  ▎ "+i18n.M.ChatThinking)+"\n"+renderReasoning(reasoning, width, 0)+"\n\n")
			}
			if body := strings.TrimSpace(m.Content); body != "" {
				out = append(out, renderAssistant(body, width)+"\n\n")
			}
			for _, call := range m.ToolCalls {
				out = append(out, toolCard(call.Name, "", width)+"\n\n")
			}
			if m.InterruptedTurn != nil {
				out = append(out, fmt.Sprintf("  · %s\n\n", interruptedTurnDisplayNotice()))
			}
			continue
		}
		out = append(out, searchHistorySections(m, width, renderAssistant)...)
		switch m.Role {
		case provider.RoleUser:
			// Steer messages are surfaced as a notice line, not a user bubble.
			if text, handled := agent.ReplaySteerText(m.Content); handled {
				if text != "" {
					out = append(out, fmt.Sprintf("  ↪ %s\n\n", text))
				}
				continue
			}
			content := control.StripComposePrefixes(m.Content)
			out = append(out, renderUserBubble(content, width, false)+"\n\n")
		case provider.RoleAssistant:
			if reasoning := strings.TrimSpace(m.ReasoningContent); reasoning != "" {
				out = append(out, dim("  ▎ "+i18n.M.ChatThinking)+"\n"+renderReasoning(reasoning, width, 0)+"\n\n")
			}
			body := strings.TrimSpace(m.Content)
			if body != "" {
				out = append(out, renderAssistant(body, width)+"\n\n")
			}
			for _, call := range m.ToolCalls {
				out = append(out, toolCard(call.Name, call.Arguments, width)+"\n\n")
			}
		}
	}
	return out
}

func interruptedTurnDisplayNotice() string {
	return i18n.M.InterruptedRecovery
}

// renderTUIBanner is the title + tip + optional missing-key warning printed once
// at the top of the session.
func renderTUIBanner(label, missing string, width int) string {
	var b strings.Builder
	b.WriteString(accent("◆") + " " + bold("reasonix") + "  " + dim("· "+label) + "\n")
	b.WriteString(dim("  "+i18n.M.ChatTip) + "\n")
	if missing != "" {
		b.WriteString(wrapForViewport("  ! "+missing, width, activeCLITheme.warn) + "\n")
	}
	return b.String()
}

// wrapForViewport hard-wraps text to fit width columns and colours every line.
func wrapForViewport(text string, width int, fg cliColor) string {
	if width <= 0 {
		width = 80
	}
	return themeStyle(fg).Width(width).Render(text)
}

// renderUserBubble renders the just-submitted prompt as a transcript line. Keep
// it visually lighter than the real bottom composer so a fresh session does not
// look like it has a second input box in the transcript.
func renderUserBubble(line string, width int, planMode bool) string {
	line = displayLineForImageRefs(line)
	prefix := "› "
	if planMode {
		prefix = "› [plan] "
	}
	if !colorOn() {
		return "│ " + prefix + line
	}
	return "  " + accent(prefix+line)
}

var cliImageRefRe = regexp.MustCompile(`(?:^|\s)@\.reasonix/attachments/clipboard-\d{8}-\d{6}\.\d+(?:-(?:\d{6}|[a-f0-9]{8}))?\.(?:png|jpg|jpeg|gif|webp)`)

func displayLineForImageRefs(line string) string {
	idx := 0
	out := cliImageRefRe.ReplaceAllStringFunc(line, func(_ string) string {
		idx++
		return " [image" + strconv.Itoa(idx) + "]"
	})
	return strings.TrimSpace(out)
}

// eventSink is the event.Sink the agent emits to in TUI mode. Each event
// becomes an agentEventMsg. The channel is generously buffered so streaming
// bursts don't back-pressure the agent goroutine.
type eventSink struct {
	ch chan<- event.Event
}

func (s *eventSink) Emit(e event.Event) { s.ch <- e }
