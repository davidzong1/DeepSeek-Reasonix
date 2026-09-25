package responses

import (
	"testing"

	"reasonix/internal/provider"
)

// cachedDetail is the Responses nested shape: the cache read lives inside
// input_tokens_details rather than beside input_tokens.
func cachedDetail(n int) *struct {
	CachedTokens int `json:"cached_tokens"`
} {
	return &struct {
		CachedTokens int `json:"cached_tokens"`
	}{CachedTokens: n}
}

// reasoningDetail is the nested output_tokens_details shape.
func reasoningDetail(n int) *struct {
	ReasoningTokens int `json:"reasoning_tokens"`
} {
	return &struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	}{ReasoningTokens: n}
}

// TestUsageFromResponseShapeMatrix is the Responses-dialect usage matrix.
//
// This dialect is a third path: it does not share normaliseUsage with the chat
// completions provider, and it reads a single terminal usage object rather than
// folding events. So "the OpenAI path covers it" is not something to assume —
// each shape is pinned here instead.
//
// Every row names what its evidence is worth:
//
//	verified  — a documented Responses shape, read from the vocabulary it uses
//	malformed — no supported provider sends this; the row pins the behaviour
func TestUsageFromResponseShapeMatrix(t *testing.T) {
	shapes := []struct {
		name      string
		response  *sseResponse
		prompt    int
		hit       int
		miss      int
		complet   int
		reasoning int
		total     int
		// split says the reading claims a cache split, so prompt == hit + miss
		// must hold.
		split  bool
		status string
		why    string
	}{
		{
			name: "warm: the read is a subset of input_tokens",
			response: &sseResponse{Usage: &sseUsage{
				InputTokens: 1000, OutputTokens: 50, TotalTokens: 1050, InputTokensDetails: cachedDetail(900),
			}},
			prompt: 1000, hit: 900, miss: 100, complet: 50, total: 1050,
			split: true, status: "verified",
			why: "Responses documents input_tokens as the whole prompt with cached_tokens as a subset",
		},
		{
			name: "cold: no cache detail block",
			response: &sseResponse{Usage: &sseUsage{
				InputTokens: 1000, OutputTokens: 50, TotalTokens: 1050,
			}},
			prompt: 1000, hit: 0, miss: 1000, complet: 50, total: 1050,
			split: true, status: "verified",
			why: "the whole input is uncached when no cache detail is reported; the miss is the remainder",
		},
		{
			name: "full hit: cached equals input",
			response: &sseResponse{Usage: &sseUsage{
				InputTokens: 1000, OutputTokens: 50, TotalTokens: 1050, InputTokensDetails: cachedDetail(1000),
			}},
			prompt: 1000, hit: 1000, miss: 0, complet: 50, total: 1050,
			split: true, status: "verified",
			why: "a full hit leaves no uncached remainder",
		},
		{
			name: "reasoning tokens ride output_tokens_details",
			response: &sseResponse{Usage: &sseUsage{
				InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500,
				InputTokensDetails: cachedDetail(600), OutputTokensDetails: reasoningDetail(180),
			}},
			prompt: 1000, hit: 600, miss: 400, complet: 500, reasoning: 180, total: 1500,
			split: true, status: "verified",
			why: "reasoning is a subset of the output, not an addend",
		},
		{
			name: "total omitted: derived from input plus output",
			response: &sseResponse{Usage: &sseUsage{
				InputTokens: 1000, OutputTokens: 50, InputTokensDetails: cachedDetail(900),
			}},
			prompt: 1000, hit: 900, miss: 100, complet: 50, total: 1050,
			split: true, status: "verified",
			why: "the Responses spec always sends total_tokens, but a compatible endpoint may omit it; " +
				"the derived value is the same number",
		},
		{
			name:     "no usage object at all",
			response: &sseResponse{},
			prompt:   0, hit: 0, miss: 0, complet: 0, total: 0,
			split: false, status: "verified",
			why: "an absent usage block reads as zeros here; the caller decides whether to emit a usage " +
				"chunk at all (see emitTerminalResponseUsage, which requires total>0 or a finish reason)",
		},
		{
			name:     "nil response",
			response: nil,
			prompt:   0, hit: 0, miss: 0, complet: 0, total: 0,
			split: false, status: "verified",
			why: "the nil guard",
		},
		{
			name: "malformed: cached_tokens exceeds input_tokens",
			response: &sseResponse{Usage: &sseUsage{
				InputTokens: 1000, OutputTokens: 50, TotalTokens: 1050, InputTokensDetails: cachedDetail(1200),
			}},
			prompt: 1000, hit: 1200, miss: 0, complet: 50, total: 1050,
			split: false, status: "malformed",
			why: "a cache read cannot be a subset of an input smaller than itself. The reading keeps both " +
				"reported numbers rather than clamping the hit or inventing a negative miss, so " +
				"prompt == hit + miss does not hold — which is what makes the contradiction visible",
		},
	}

	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			u := usageFromResponse(s.response)

			if u.PromptTokens != s.prompt || u.CacheHitTokens != s.hit || u.CacheMissTokens != s.miss {
				t.Fatalf("%s\n got prompt=%d hit=%d miss=%d\nwant prompt=%d hit=%d miss=%d\n(%s)",
					s.name, u.PromptTokens, u.CacheHitTokens, u.CacheMissTokens, s.prompt, s.hit, s.miss, s.why)
			}
			if u.CompletionTokens != s.complet || u.ReasoningTokens != s.reasoning || u.TotalTokens != s.total {
				t.Fatalf("%s: completion=%d reasoning=%d total=%d, want %d/%d/%d",
					s.name, u.CompletionTokens, u.ReasoningTokens, u.TotalTokens, s.complet, s.reasoning, s.total)
			}
			if s.split && u.PromptTokens != u.CacheHitTokens+u.CacheMissTokens {
				t.Fatalf("%s: prompt %d != hit %d + miss %d", s.name, u.PromptTokens, u.CacheHitTokens, u.CacheMissTokens)
			}
		})
	}
}

// TestUsageFromResponseDoesNotClampAndIsMarkedDownstream pins the behaviour a
// malformed shape actually gets. No supported Responses endpoint sends a negative
// count, so this is not a defect being worked around: the adapter passes the
// counters through as reported rather than clamping them — a guessed floor would
// invent a reading nothing stated — and the sample is excluded downstream by the
// accounting check, which is where a named reason belongs.
//
// The negative case is the one that matters, because the miss is derived by
// subtraction and that is where a floor would have to be invented. The derived
// miss is floored at zero (max(input-cached, 0)); a negative input is not.
func TestUsageFromResponseDoesNotClampAndIsMarkedDownstream(t *testing.T) {
	// The derived miss floors at zero rather than going negative.
	floored := usageFromResponse(&sseResponse{Usage: &sseUsage{
		InputTokens: 10, OutputTokens: 5, InputTokensDetails: cachedDetail(500),
	}})
	if floored.CacheMissTokens != 0 {
		t.Fatalf("derived miss = %d, want 0 (never negative)", floored.CacheMissTokens)
	}

	// A negative reported counter passes through, and is therefore detectable:
	// the downstream accounting check reads it as negative:input_tokens and
	// excludes the sample by name instead of silently correcting it.
	passthrough := usageFromResponse(&sseResponse{Usage: &sseUsage{InputTokens: -100, OutputTokens: -5}})
	if passthrough.PromptTokens != -100 || passthrough.CompletionTokens != -5 {
		t.Fatalf("reading = prompt %d completion %d, want the reported -100/-5 unchanged",
			passthrough.PromptTokens, passthrough.CompletionTokens)
	}
}

// TestUsageFromResponseIsNotTheChatCompletionsPath pins the separation the
// followup plan asks to check explicitly. The Responses dialect and the chat
// completions dialect read different vocabularies: Responses' input_tokens is the
// WHOLE prompt with cached_tokens as a subset, while the chat path's Anthropic
// fallback reads input_tokens as the UNCACHED part. A row that fed the same bytes
// to both would get different prompts, so the two must not be described as one
// shared usage path.
func TestUsageFromResponseIsNotTheChatCompletionsPath(t *testing.T) {
	// Responses: input_tokens covers the cached read.
	responsesReading := usageFromResponse(&sseResponse{Usage: &sseUsage{
		InputTokens: 1000, OutputTokens: 50, InputTokensDetails: cachedDetail(900),
	}})
	if responsesReading.PromptTokens != 1000 || responsesReading.CacheMissTokens != 100 {
		t.Fatalf("responses reading = prompt %d miss %d, want 1000/100",
			responsesReading.PromptTokens, responsesReading.CacheMissTokens)
	}
	// The same three numbers through the chat path's Anthropic fallback, where
	// input_tokens is the uncached part, gives a different prompt — the two
	// dialects are not interchangeable.
	chatReading := anthropicStyleFallbackForTest(t, 1000, 0, 900, 50)
	if chatReading.PromptTokens != 1900 || chatReading.CacheMissTokens != 1000 {
		t.Fatalf("chat-fallback reading = prompt %d miss %d, want 1900/1000",
			chatReading.PromptTokens, chatReading.CacheMissTokens)
	}
	if responsesReading.PromptTokens == chatReading.PromptTokens {
		t.Fatal("the two dialects were expected to differ on these bytes; if they now agree, the " +
			"separation claim in the report needs re-checking")
	}
}

// anthropicStyleFallbackForTest reproduces the chat-completions path's
// Anthropic-style fallback for the same three counters, without importing the
// openai package (which would create a test-only dependency between dialects).
// The arithmetic is the documented one: prompt = input + create + read,
// miss = input + create.
func anthropicStyleFallbackForTest(t *testing.T, input, create, read, output int) *provider.Usage {
	t.Helper()
	_ = output
	prompt := input + create + read
	return &provider.Usage{
		PromptTokens:    prompt,
		CacheHitTokens:  read,
		CacheMissTokens: input + create,
	}
}
