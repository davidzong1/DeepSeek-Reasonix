package agent

import (
	"context"
	"strings"
)

// turnProtocolTag names the transient user-turn block that restates the turn
// boundary. It is turn tail, never the stable prefix: the requirement is already
// in the finish tool's description, and repeating it in the system prompt would
// cold-start every prompt cache to serve one provider's quirk.
const turnProtocolTag = "turn-protocol"

// turnProtocolWatch remembers that this conversation's model had to be told to
// finish. Some OpenAI-compatible models emit a terminal tool call only when the
// immediately preceding user message demands it: they answer, the host requests
// one repair, and they comply — every turn, for two round trips instead of one.
//
// Arming is observation-driven on purpose. A model that finishes on its own never
// sees the block and pays nothing, so the reminder costs tokens only in the
// conversations that already proved they need it.
type turnProtocolWatch struct {
	remind bool
}

// arm records that a protocol repair was needed in this conversation.
func (w *turnProtocolWatch) arm() { w.remind = true }

// armed reports whether later turns should carry the reminder.
func (w *turnProtocolWatch) armed() bool { return w != nil && w.remind }

// turnProtocolBlock is the reminder's wording. It mirrors requestProtocolRepair,
// the message this model demonstrably obeys; the only difference is that this one
// arrives before the answer instead of after it.
func turnProtocolBlock() string {
	return "<" + turnProtocolTag + ">\n" +
		"End this turn with the finish tool: give the user your visible answer, then call finish exactly once as the only tool call in that batch. " +
		"A turn that ends without it is incomplete and will be sent back.\n" +
		"</" + turnProtocolTag + ">"
}

// withTurnProtocol prefixes the transient reminder when this conversation has
// shown it needs one. Already-present blocks are left alone, matching the other
// transient injectors: a repair prompt states the rule itself, and re-stating it
// twice in one message reads as two requirements.
func withTurnProtocol(content string, remind bool) string {
	if !remind || hasLeadingInjectedBlock(content, turnProtocolTag) {
		return content
	}
	if strings.TrimSpace(content) == "" {
		return content
	}
	return turnProtocolBlock() + "\n\n" + content
}

// remindTurnProtocol reports whether this turn should carry the reminder: the
// conversation needed a repair before, and a structured finish is still required
// (a provider without the finish tool must never be told to call it).
func (a *Agent) remindTurnProtocol(ctx context.Context) bool {
	if a == nil {
		return false
	}
	return a.sess.turnProtocol.armed() && a.requiresStructuredFinish(ctx)
}
