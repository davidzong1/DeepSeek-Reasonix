package anthropic

import "reasonix/internal/provider"

// messagesUsage folds one stream's merged Messages-dialect counters into a
// provider.Usage record.
//
// Anthropic's native API reports input_tokens as the *uncached* input: the
// request total is input_tokens + cache_creation + cache_read, and every
// uncached token is a miss. DeepSeek's Anthropic-compatible endpoint instead
// reports the whole input in input_tokens with the cache counters as subsets,
// so summing them there counted every cached token twice and pinned the
// displayed cache-hit rate at 50%. The counters do not say which convention
// they follow, so the route that knows its upstream declares it.
//
// Both readings keep PromptTokens == CacheHitTokens + CacheMissTokens, the
// invariant every consumer of the billable input relies on.
func messagesUsage(inTok, outTok, cacheCreate, cacheRead int, billedWrite float64, inclusive bool) *provider.Usage {
	miss := inTok + cacheCreate
	prompt := inTok + cacheCreate + cacheRead
	if inclusive {
		// The cache counters are already inside inTok, so the uncached part is
		// what is left once the reads come out. Cache writes are uncached input
		// and stay in that remainder, which is the subset relationship the
		// CacheWriteTokens field documents.
		miss = max(inTok-cacheRead, 0)
		prompt = cacheRead + miss
	}
	return &provider.Usage{
		PromptTokens:           prompt,
		CompletionTokens:       outTok,
		TotalTokens:            prompt + outTok,
		CacheHitTokens:         cacheRead,
		CacheMissTokens:        miss,
		CacheWriteTokens:       cacheCreate,
		CacheWriteBilledTokens: billedWrite,
	}
}
