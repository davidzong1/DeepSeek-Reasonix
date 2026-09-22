package anthropic

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

// usageSSE renders a Messages stream whose only usage record is the given one,
// delivered in message_start (the native placement).
func usageSSE(inTok, cacheCreate, cacheRead, outTok int) string {
	n := strconv.Itoa
	return `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":` + n(inTok) +
		`,"cache_creation_input_tokens":` + n(cacheCreate) +
		`,"cache_read_input_tokens":` + n(cacheRead) +
		`,"output_tokens":` + n(outTok) + `}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":` + n(outTok) + `}}

event: message_stop
data: {"type":"message_stop"}
`
}

// readUsage feeds one SSE body through readStream and returns its usage chunk.
func readUsage(t *testing.T, c *client, sse string) *provider.Usage {
	t.Helper()
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(sse))}
	ch := make(chan provider.Chunk)
	go c.readStream(context.Background(), resp, ch)
	var usage *provider.Usage
	for ck := range ch {
		if ck.Type == provider.ChunkError {
			t.Fatalf("unexpected error chunk: %v", ck.Err)
		}
		if ck.Type == provider.ChunkUsage {
			usage = ck.Usage
		}
	}
	if usage == nil {
		t.Fatal("expected a usage chunk")
	}
	return usage
}

// TestUsageNativeAnthropicSumpsTheExclusiveCounters pins the native contract:
// input_tokens excludes both cache counters, so the request total is their sum
// and every uncached token is a miss.
func TestUsageNativeAnthropicSumpsTheExclusiveCounters(t *testing.T) {
	usage := readUsage(t, &client{name: "anthropic"}, usageSSE(100, 0, 50, 25))

	if usage.PromptTokens != 150 || usage.CacheHitTokens != 50 || usage.CacheMissTokens != 100 {
		t.Fatalf("native usage = prompt %d hit %d miss %d, want 150/50/100",
			usage.PromptTokens, usage.CacheHitTokens, usage.CacheMissTokens)
	}
	if usage.TotalTokens != 175 {
		t.Fatalf("total = %d, want 175", usage.TotalTokens)
	}
}

// TestUsageDeepSeekDoesNotDoubleCountCacheReads covers the DeepSeek Anthropic
// route, whose input_tokens already covers the cache reads: summing them there
// counted every cached token twice and pinned the displayed rate at 50%.
func TestUsageDeepSeekDoesNotDoubleCountCacheReads(t *testing.T) {
	// A 39047-token request with 39040 cached reads arrives as input=39047.
	c := &client{name: "deepseek-anthropic", deepseek: true}
	usage := readUsage(t, c, usageSSE(39047, 0, 39040, 4))

	if usage.PromptTokens != 39047 {
		t.Fatalf("prompt = %d, want the reported 39047 (not input+cache_read)", usage.PromptTokens)
	}
	if usage.CacheHitTokens != 39040 || usage.CacheMissTokens != 7 {
		t.Fatalf("cache = hit %d miss %d, want 39040/7", usage.CacheHitTokens, usage.CacheMissTokens)
	}
	if usage.TotalTokens != 39051 {
		t.Fatalf("total = %d, want 39051", usage.TotalTokens)
	}
	if got := usage.CacheHitTokens * 100 / (usage.CacheHitTokens + usage.CacheMissTokens); got != 99 {
		t.Fatalf("cache-hit rate = %d%%, want 99%%", got)
	}
}

// TestUsageDeepSeekKeepsCacheWritesBilled keeps the cache-creation accounting
// intact: a write is uncached input, so it stays in the miss under either
// convention and the invariant hit+miss == prompt still holds.
func TestUsageDeepSeekKeepsCacheWritesBilled(t *testing.T) {
	c := &client{name: "deepseek-anthropic", deepseek: true}
	usage := readUsage(t, c, usageSSE(1000, 200, 700, 5))

	if usage.CacheWriteTokens != 200 {
		t.Fatalf("cache write = %d, want 200", usage.CacheWriteTokens)
	}
	if usage.CacheHitTokens != 700 || usage.CacheMissTokens != 300 {
		t.Fatalf("cache = hit %d miss %d, want 700/300", usage.CacheHitTokens, usage.CacheMissTokens)
	}
	if usage.PromptTokens != 1000 {
		t.Fatalf("prompt = %d, want 1000", usage.PromptTokens)
	}
}

// TestUsageInclusiveCounterNeverGoesNegative covers the malformed-record guard:
// a gateway whose cache read exceeds its own input counter must not produce a
// negative miss.
func TestUsageInclusiveCounterNeverGoesNegative(t *testing.T) {
	c := &client{name: "deepseek-anthropic", deepseek: true}
	usage := readUsage(t, c, usageSSE(10, 0, 50, 5))

	if usage.CacheMissTokens != 0 {
		t.Fatalf("miss = %d, want 0", usage.CacheMissTokens)
	}
	if usage.PromptTokens != 50 {
		t.Fatalf("prompt = %d, want 50 (hit+miss)", usage.PromptTokens)
	}
}

// TestUsageEveryConventionKeepsTheBillingInvariant holds the one contract every
// consumer of the billable input relies on: prompt == hit + miss.
func TestUsageEveryConventionKeepsTheBillingInvariant(t *testing.T) {
	for _, inclusive := range []bool{false, true} {
		usage := messagesUsage(1234, 7, 56, 890, 0, inclusive)
		if usage.PromptTokens != usage.CacheHitTokens+usage.CacheMissTokens {
			t.Fatalf("inclusive=%v: prompt %d != hit %d + miss %d",
				inclusive, usage.PromptTokens, usage.CacheHitTokens, usage.CacheMissTokens)
		}
	}
}
