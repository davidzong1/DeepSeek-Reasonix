package openai

import (
	"testing"

	"reasonix/internal/provider"
)

// cachedDetail is the nested OpenAI/MiMo shape: the cache read lives inside
// prompt_tokens_details rather than beside prompt_tokens.
func cachedDetail(n int) *struct {
	CachedTokens int `json:"cached_tokens"`
} {
	return &struct {
		CachedTokens int `json:"cached_tokens"`
	}{CachedTokens: n}
}

// reasoningDetail is the nested completion_tokens_details shape.
func reasoningDetail(n int) *struct {
	ReasoningTokens int `json:"reasoning_tokens"`
} {
	return &struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	}{ReasoningTokens: n}
}

// TestNormaliseUsageEventShapeMatrix is the OpenAI-dialect counterpart of the
// Anthropic adapter's matrix. The two dialects reach the same provider.Usage
// through different code, so a claim that "the usage path is shared" has to be
// checked per shape rather than assumed.
//
// Every row asserts prompt == hit + miss where the reading claims a split, and
// names what the row's evidence is worth:
//
//	verified  — a documented provider shape, read from the vocabulary it uses
//	ambiguous — the counters cannot decide, so the reading follows one rule and
//	            is pinned rather than claimed
//	malformed — no supported provider sends this; the row pins the behaviour
func TestNormaliseUsageEventShapeMatrix(t *testing.T) {
	shapes := []struct {
		name string
		wire *wireUsage
		// want is the normalized reading.
		prompt, hit, miss, completion, reasoning int
		// split says the reading claims a cache split, so the invariant must hold.
		split  bool
		status string
		why    string
	}{
		{
			name:   "DeepSeek: top-level hit and miss reported explicitly",
			wire:   &wireUsage{PromptTokens: 1000, CompletionTokens: 50, TotalTokens: 1050, PromptCacheHitTokens: 900, PromptCacheMissTokens: 100},
			prompt: 1000, hit: 900, miss: 100, completion: 50,
			split: true, status: "verified",
			why: "DeepSeek's own spelling; the miss is reported, so it is preferred over prompt-hit",
		},
		{
			name:   "DeepSeek: hit reported, miss derived as the remainder",
			wire:   &wireUsage{PromptTokens: 1000, CompletionTokens: 50, PromptCacheHitTokens: 900},
			prompt: 1000, hit: 900, miss: 100, completion: 50,
			split: true, status: "verified",
			why: "the only reading consistent with a hit that is a subset of the prompt",
		},
		{
			name:   "OpenAI/MiMo: the read sits in prompt_tokens_details",
			wire:   &wireUsage{PromptTokens: 1000, CompletionTokens: 50, TotalTokens: 1050, PromptTokensDetails: cachedDetail(900)},
			prompt: 1000, hit: 900, miss: 100, completion: 50,
			split: true, status: "verified",
			why: "nested details belong to the object that declares them",
		},
		{
			name:   "OpenAI: reasoning tokens ride completion_tokens_details",
			wire:   &wireUsage{PromptTokens: 1000, CompletionTokens: 500, PromptTokensDetails: cachedDetail(600), CompletionTokensDetails: reasoningDetail(180)},
			prompt: 1000, hit: 600, miss: 400, completion: 500, reasoning: 180,
			split: true, status: "verified",
			why: "reasoning is a subset of completion, not an addend",
		},
		{
			name:   "Anthropic-style fallback: input excludes both cache counters",
			wire:   &wireUsage{InputTokens: 100, CacheCreationInputTokens: 20, CacheReadInputTokens: 880, OutputTokens: 50},
			prompt: 1000, hit: 880, miss: 120, completion: 50,
			split: true, status: "verified",
			why: "a compatible gateway on the openai kind may still speak the Messages vocabulary; " +
				"prompt_tokens is absent, which is the discriminator",
		},
		{
			name:   "Anthropic-style fallback with no cache write",
			wire:   &wireUsage{InputTokens: 120, CacheReadInputTokens: 880, OutputTokens: 50},
			prompt: 1000, hit: 880, miss: 120, completion: 50,
			split: true, status: "verified",
			why: "the same shape without a write",
		},
		{
			name:   "OpenAI cold: no cache key anywhere",
			wire:   &wireUsage{PromptTokens: 1000, CompletionTokens: 50},
			prompt: 1000, hit: 0, miss: 0, completion: 50,
			split: false, status: "verified",
			why: "no cache counter was reported, so no split is claimed — the reading must not invent " +
				"miss == prompt, which would be a cache fact nobody stated",
		},
		{
			name:   "OpenAI zero-valued usage object",
			wire:   &wireUsage{},
			prompt: 0, hit: 0, miss: 0, completion: 0,
			split: false, status: "verified",
			why: "an all-zero object is reported as zeros rather than as a cold miss",
		},
		{
			name:   "DeepSeek: a miss with no hit",
			wire:   &wireUsage{PromptTokens: 1000, CompletionTokens: 50, PromptCacheMissTokens: 100},
			prompt: 1000, hit: 0, miss: 100, completion: 50,
			split: false, status: "ambiguous",
			why: "the hit key is absent, so 900 prompt tokens are unaccounted for. The reading keeps the " +
				"reported miss rather than deriving prompt-hit; prompt != hit + miss, and the downstream " +
				"accounting check marks the sample — the honest outcome for counters that do not add up",
		},
		{
			name:   "malformed: a nested read larger than the prompt",
			wire:   &wireUsage{PromptTokens: 1000, CompletionTokens: 50, PromptTokensDetails: cachedDetail(1200)},
			prompt: 1000, hit: 1200, miss: 0, completion: 50,
			split: false, status: "malformed",
			why: "a cache read cannot be a subset of a prompt smaller than itself. The reading keeps both " +
				"reported numbers instead of clamping either, so prompt == hit + miss does not hold — " +
				"which is the point: the row exists so a clamp added later is a visible change",
		},
		{
			name:   "malformed: a hit larger than the prompt",
			wire:   &wireUsage{PromptTokens: 100, PromptCacheHitTokens: 500},
			prompt: 100, hit: 500, miss: 0, completion: 0,
			split: false, status: "malformed",
			why: "same contradiction in the top-level spelling",
		},
	}

	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			u := normaliseUsage(s.wire)

			if u.PromptTokens != s.prompt || u.CacheHitTokens != s.hit || u.CacheMissTokens != s.miss {
				t.Fatalf("%s\n got prompt=%d hit=%d miss=%d\nwant prompt=%d hit=%d miss=%d\n(%s)",
					s.name, u.PromptTokens, u.CacheHitTokens, u.CacheMissTokens, s.prompt, s.hit, s.miss, s.why)
			}
			if u.CompletionTokens != s.completion || u.ReasoningTokens != s.reasoning {
				t.Fatalf("%s: completion=%d reasoning=%d, want %d/%d",
					s.name, u.CompletionTokens, u.ReasoningTokens, s.completion, s.reasoning)
			}
			// The invariant is asserted only where the reading claims a split:
			// a shape that reports no cache key must not be forced into one.
			if s.split && u.PromptTokens != u.CacheHitTokens+u.CacheMissTokens {
				t.Fatalf("%s: prompt %d != hit %d + miss %d", s.name, u.PromptTokens, u.CacheHitTokens, u.CacheMissTokens)
			}
		})
	}
}

// TestNormaliseUsageDoesNotClampAndIsMarkedDownstream pins the behaviour a
// malformed shape actually gets. No supported provider sends a negative count,
// so this is not a defect being worked around: the adapter passes the counters
// through as reported rather than clamping them — a guessed floor would invent a
// reading nothing stated — and the sample is excluded downstream by the
// accounting check, which is where a named reason belongs.
//
// The two properties that DO hold are asserted here as well: the derived miss is
// never negative (it is a subtraction), and a negative reported counter survives
// into the reading so the downstream check can name it.
func TestNormaliseUsageDoesNotClampAndIsMarkedDownstream(t *testing.T) {
	// The derived miss floors at zero rather than going negative.
	floored := normaliseUsage(&wireUsage{PromptTokens: 10, PromptTokensDetails: cachedDetail(500)})
	if floored.CacheMissTokens != 0 {
		t.Fatalf("derived miss = %d, want 0 (never negative)", floored.CacheMissTokens)
	}

	// A negative reported counter passes through, and is therefore detectable.
	passthrough := normaliseUsage(&wireUsage{PromptTokens: -5, CompletionTokens: -1})
	if passthrough.PromptTokens != -5 || passthrough.CompletionTokens != -1 {
		t.Fatalf("reading = prompt %d completion %d, want the reported -5/-1 unchanged",
			passthrough.PromptTokens, passthrough.CompletionTokens)
	}

	// A negative hit likewise survives, so accounting can name it.
	negativeHit := normaliseUsage(&wireUsage{PromptTokens: 100, PromptCacheHitTokens: -50})
	if negativeHit.CacheHitTokens != -50 {
		t.Fatalf("hit = %d, want the reported -50 unchanged", negativeHit.CacheHitTokens)
	}
}

// TestMergeUsageRequestCountProvenanceIsTheAndOfItsParts pins the provenance rule
// the Part A contract depends on: a merged count is a measurement only while
// every part of it was measured. One defaulted attempt makes the total a lower
// bound, so the flag must not survive on the strength of the last part alone.
func TestMergeUsageRequestCountProvenanceIsTheAndOfItsParts(t *testing.T) {
	measured := &provider.Usage{PromptTokens: 100, RequestCount: 1, RequestCountObserved: true}
	assumed := &provider.Usage{PromptTokens: 100, RequestCount: 1, RequestCountObserved: false}

	if got := mergeUsage(nil, measured, false); !got.RequestCountObserved {
		t.Fatal("a single measured part must keep its provenance")
	}
	both := mergeUsage(mergeUsage(nil, measured, false), measured, false)
	if !both.RequestCountObserved {
		t.Fatal("two measured parts must keep their provenance")
	}
	mixed := mergeUsage(mergeUsage(nil, measured, false), assumed, false)
	if mixed.RequestCountObserved {
		t.Fatal("one unmeasured part makes the merged count a lower bound, not a measurement")
	}
}

// TestMergeUsageKeepsOneRequestForOneStream pins the difference between the two
// merge modes. A stream that emits several usage chunks is still ONE provider
// request, so its count must not grow; distinct prefix-continuation requests are
// separate requests and must sum.
func TestMergeUsageKeepsOneRequestForOneStream(t *testing.T) {
	first := &provider.Usage{PromptTokens: 100, RequestCount: 1, RequestCountObserved: true}
	second := &provider.Usage{PromptTokens: 50, RequestCount: 1, RequestCountObserved: true}

	oneStream := mergeUsage(mergeUsage(nil, first, false), second, false)
	if oneStream.RequestCount != 1 {
		t.Fatalf("one stream reported %d requests, want 1", oneStream.RequestCount)
	}
	if oneStream.PromptTokens != 150 {
		t.Fatalf("tokens must still sum across usage chunks: got %d, want 150", oneStream.PromptTokens)
	}

	twoRequests := mergeUsage(mergeUsage(nil, first, true), second, true)
	if twoRequests.RequestCount != 2 {
		t.Fatalf("two continuation requests reported %d, want 2", twoRequests.RequestCount)
	}
	if !twoRequests.RequestCountObserved {
		t.Fatal("both parts were measured, so the sum is a measurement")
	}
}
