package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"reasonix/internal/cachereason"
	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// PrefixShape hashes the portions of the request prefix that influence
// provider-side prompt-cache reuse. Comparing snapshots across turns
// lets us explain *why* a cache miss happened.
type PrefixShape struct {
	SystemHash           string
	ToolsHash            string
	PrefixHash           string
	LogRewriteVersion    int
	ToolSchemaTokens     int
	SessionContextDigest string
	// Messages fingerprints the conversation: the one part that grows every
	// turn, and therefore where "appended to what was already sent" and
	// "rewrote bytes the provider had already read" are told apart.
	Messages MessageShape
}

// MessageShape fingerprints one request's provider-visible conversation — the
// messages minus the system prompt — without keeping a body, argument or
// credential. The system prompt is excluded on purpose: it and the tool schemas
// have their own hash, and folding them in would report one change as two.
type MessageShape struct {
	// Hash identifies the whole conversation and is what gets published.
	Hash string
	// Count is how many conversation messages the array carried; the system
	// prompt is not one of them.
	Count int
	// digests is one fingerprint per message, at the message's own index. It
	// places the first divergence between two requests and is local-only: no
	// caller outside this package reads it.
	digests []string
}

// CaptureMessageShape fingerprints the provider-visible conversation of one
// request. Its callers pass the same list they hand the provider, so the shape
// describes what was actually sent; every role=system message is skipped so this
// shape and SystemHash never report the same change.
func CaptureMessageShape(messages []provider.Message) MessageShape {
	digests := providerVisibleMessageDigests(conversationMessages(messages))
	return MessageShape{Hash: shortHash(digests), Count: len(digests), digests: digests}
}

// conversationMessages drops the system prompt from a provider-bound list, at
// any position, so the remaining indices are the conversation's own.
func conversationMessages(messages []provider.Message) []provider.Message {
	conversation := make([]provider.Message, 0, len(messages))
	for _, m := range messages {
		if m.Role == provider.RoleSystem {
			continue
		}
		conversation = append(conversation, m)
	}
	return conversation
}

// messageDivergence places the first message at which two consecutive requests
// disagree, which is the whole point of keeping per-message digests.
//
// comparable is false when there is no previous request to compare against — a
// session's first request, or a shape captured before this field existed. An
// incomparable pair reports nothing rather than inventing an append-only
// verdict it cannot support.
//
// offset is the index of the first differing message, and on an append-only
// request it equals the previous request's message count. rewritten is how many
// messages of the previous request this one did not reuse: zero for append-only,
// positive when bytes the provider had already read were rewritten in place.
// A shorter array than the previous one reports the truncation as a rewrite of
// everything past the new end, which is what the provider re-reads.
func messageDivergence(prev, cur MessageShape) (offset, rewritten int, comparable bool) {
	if prev.Hash == "" {
		return -1, 0, false
	}
	common := 0
	for common < len(prev.digests) && common < len(cur.digests) && prev.digests[common] == cur.digests[common] {
		common++
	}
	return common, max(len(prev.digests)-common, 0), true
}

// CacheDiagnostics is a type alias for event.CacheDiagnostics so the agent
// can construct and compare diagnostics without importing event itself in
// every call site, while still assigning to event.Event.CacheDiagnostics.
type CacheDiagnostics = event.CacheDiagnostics

func shortHash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h[:8])
}

// projectionRewriteReason maps a maintenance action to the content-rewrite reason
// it reports. The values come from the shared vocabulary, so the team cache
// report understands them without keeping a list of its own.
func projectionRewriteReason(action string) string {
	switch strings.TrimSpace(action) {
	case maintenanceActionSummary:
		return cachereason.CompactAuto
	case maintenanceActionPrune:
		return cachereason.Prune
	case maintenanceActionTruncate:
		return cachereason.Truncate
	}
	return ""
}

// noteProjectionRewrite queues the cache-diagnostics reason for a projection the
// agent just installed. Only the two install sites call it. The receipt re-emits
// at context_receipt.go and session_checkpoint.go republish an install that
// already happened, so queueing there would report a rewrite on a request that
// never saw one — the misattribution this reason exists to prevent.
func (a *Agent) noteProjectionRewrite(r *ContextMaintenanceReceipt) {
	if a == nil || r == nil || a.sess.conversation == nil {
		return
	}
	if reason := projectionRewriteReason(r.Action); reason != "" {
		a.sess.conversation.NoteContentRewrite(reason)
	}
}

// CaptureShape takes a snapshot of the current prefix state.
func CaptureShape(systemPrompt string, schemas []provider.ToolSchema, rewriteVersion int) PrefixShape {
	normalizedSchemas := normalizeToolSchemas(schemas)
	toolsJSON, _ := json.Marshal(normalizedSchemas)
	return PrefixShape{
		SystemHash: shortHash(systemPrompt),
		ToolsHash:  shortHash(string(toolsJSON)),
		PrefixHash: shortHash(map[string]any{
			"system": systemPrompt,
			"tools":  string(toolsJSON),
		}),
		LogRewriteVersion: rewriteVersion,
		ToolSchemaTokens:  estimateTokens(string(toolsJSON)),
	}
}

func normalizeToolSchemas(schemas []provider.ToolSchema) []provider.ToolSchema {
	out := make([]provider.ToolSchema, len(schemas))
	copy(out, schemas)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].Description != out[j].Description {
			return out[i].Description < out[j].Description
		}
		return string(out[i].Parameters) < string(out[j].Parameters)
	})
	return out
}

// CompareShape returns diagnostics describing what changed between two shapes.
// contentReasons is the set of provider-visible rewrite reasons (values from
// internal/cachereason) drained from the Session since prev was captured — see
// Session.DrainContentRewriteReasons. It is the sole source of rewrite-caused
// reasons: a bare LogRewriteVersion change with no drained reason means only
// local-only metadata was touched (a decision receipt, tool-call
// preview/resolution, or an Edited-message replace), which never reaches the
// provider and so must not be reported as a cache change.
func CompareShape(prev, cur PrefixShape, usage *provider.Usage, contentReasons []string) CacheDiagnostics {
	reasons := []string{}
	if prev.SystemHash != "" && prev.SystemHash != cur.SystemHash {
		reasons = append(reasons, cachereason.System)
	}
	if prev.ToolsHash != "" && prev.ToolsHash != cur.ToolsHash {
		reasons = append(reasons, cachereason.Tools)
	}
	if prev.SessionContextDigest != cur.SessionContextDigest {
		reasons = append(reasons, cachereason.SessionContext)
	}
	reasons = append(reasons, contentReasons...)
	// A rewrite something already explains — a system refresh, a tool-surface
	// change, a session-context revision, a claimed fold — is read as that
	// reason. One nothing explains is named, not passed as an ordinary append.
	divergenceOffset, rewritten, comparable := messageDivergence(prev.Messages, cur.Messages)
	if rewritten > 0 && len(reasons) == 0 {
		reasons = append(reasons, cachereason.Messages)
	}
	var miss, hit int
	if usage != nil {
		miss = usage.CacheMissTokens
		hit = usage.CacheHitTokens
	}
	return CacheDiagnostics{
		PrefixHash:            cur.PrefixHash,
		PrefixChanged:         len(reasons) > 0,
		PrefixChangeReasons:   reasons,
		MessagePrefixHash:     cur.Messages.Hash,
		MessageCount:          cur.Messages.Count,
		FirstDivergenceOffset: divergenceOffset,
		MessagesRewritten:     rewritten,
		MessagesComparable:    comparable,
		// The stable prefix (system + tools) is what a provider cache keys on.
		// prev.PrefixHash cannot report it: it also folds in the turn tail's
		// digest, so a tail-only change moves it.
		StablePrefixHash:    shortHash(map[string]string{"system": cur.SystemHash, "tools": cur.ToolsHash}),
		StablePrefixChanged: prev.SystemHash != "" && prev.ToolsHash != "" && (prev.SystemHash != cur.SystemHash || prev.ToolsHash != cur.ToolsHash),
		SystemHash:          cur.SystemHash,
		ToolsHash:           cur.ToolsHash,
		LogRewriteVersion:   cur.LogRewriteVersion,
		ToolSchemaTokens:    cur.ToolSchemaTokens,
		CacheMissTokens:     miss,
		CacheHitTokens:      hit,
	}
}

// estimateTokens gives a rough token count from byte length.
// A proper tokenizer would be more accurate, but for diagnostic
// purposes a byte-based estimate is sufficient and zero-alloc.
func estimateTokens(s string) int {
	// ~4 chars per token is a workable heuristic for code-heavy JSON.
	if len(s) == 0 {
		return 0
	}
	return len(s) / 4
}

// SchemaTokenCosts returns per-tool token cost estimates for display.
func SchemaTokenCosts(schemas []provider.ToolSchema) []ToolSchemaCost {
	out := make([]ToolSchemaCost, 0, len(schemas))
	for _, s := range schemas {
		b, _ := json.Marshal(s)
		out = append(out, ToolSchemaCost{Name: s.Name, Tokens: estimateTokens(string(b))})
	}
	return out
}

// ToolSchemaCost is a per-tool token cost estimate for diagnostic display.
type ToolSchemaCost struct {
	Name   string
	Tokens int
}
