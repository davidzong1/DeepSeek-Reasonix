package anthropic

import (
	"strings"

	"reasonix/internal/provider"
)

func (c *client) replayMessages(messages []provider.Message) []provider.Message {
	if c.deepseek {
		messages, _ = provider.ProjectReplaySafeMessages(c, messages)
	}
	return messages
}

func (c *client) replayReasoningBlock(m provider.Message) (contentBlock, bool) {
	// DeepSeek's thinking mode requires every historical assistant turn's
	// thinking block to be passed back whenever the request declares tools —
	// including plain question-answer turns with no tool activity.
	if c.deepseek && m.ReasoningContent != "" {
		return contentBlock{Type: "thinking", Thinking: m.ReasoningContent}, true
	}
	if !c.deepseek && c.thinking == "adaptive" && m.ReasoningContent != "" && m.ReasoningSignature != "" {
		return contentBlock{Type: "thinking", Thinking: m.ReasoningContent, Signature: m.ReasoningSignature}, true
	}
	return contentBlock{}, false
}

// mergeThinkingFirst merges appended blocks into an existing same-role
// message's content. A thinking block must lead its message, so a leading
// thinking run in the appended blocks moves to the front: after projection
// degrades a reasoning-less turn to plain text, a following healthy assistant
// turn merging into it must stay [thinking, text, ...], never [text, ...].
func mergeThinkingFirst(existing, blocks []contentBlock) []contentBlock {
	head := leadingThinkingBlocks(blocks)
	if head == 0 || len(existing) == 0 {
		return append(existing, blocks...)
	}
	merged := make([]contentBlock, 0, len(existing)+len(blocks))
	merged = append(merged, blocks[:head]...)
	merged = append(merged, existing...)
	merged = append(merged, blocks[head:]...)
	return merged
}

// leadingThinkingBlocks counts the thinking blocks at the head of blocks.
// replayReasoningBlock emits at most one, so the run is normally 0 or 1.
func leadingThinkingBlocks(blocks []contentBlock) int {
	head := 0
	for head < len(blocks) && blocks[head].Type == "thinking" {
		head++
	}
	return head
}

func (c *client) applyDeepSeekThinking(r *anthRequest, req provider.Request) {
	r.Temperature = req.Temperature
	t := c.thinking
	if t != "disabled" {
		t = "enabled"
	}
	if c.effort == "disabled" {
		t = "disabled"
	}
	effort := normalizeDeepSeekAnthropicEffort(c.model, c.effort)
	switch override := strings.ToLower(strings.TrimSpace(req.EffortOverride)); override {
	case "disabled":
		t = "disabled"
	case "":
	default:
		if normalized := normalizeDeepSeekAnthropicEffort(c.model, override); normalized != "" {
			effort = normalized
		}
	}
	r.Thinking = &thinkingConfig{Type: t}
	if t == "disabled" {
		return
	}
	switch effort {
	case "low", "high", "max":
		r.OutputConfig = &outputConfig{Effort: effort}
	}
}
