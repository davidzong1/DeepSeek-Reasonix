package anthropic

// streamUsage folds a Messages stream's usage events into one reading.
//
// The counters are neither all on one event nor all cumulative, and the same
// field name does not mean the same thing in every event of a stream. The native
// stream reports input/cache on message_start and output_tokens on message_delta;
// compatible gateways such as LongCat report everything on message_delta. Some
// DeepSeek-compatible gateways emit message_start twice: once with the whole
// prompt as an estimate and no cache counters, then again with the split they
// actually served, where input_tokens is the UNCACHED REMAINDER.
//
// Keeping the largest input_tokens across those two took the prompt estimate and
// subtracted the real cache read from it, reporting a cache miss no upstream
// counter ever contained: a 39,149-token estimate against 33,408 cached reads
// produced a 5,741-token miss where the gateway reported 29.
type streamUsage struct {
	in, out, cacheCreate, cacheRead int
	// estimateInput is the largest split-free input_tokens seen. A gateway that
	// later sends a smaller one alongside a cache split is describing a remainder.
	estimateInput int
	// exclusive records that input_tokens is the uncached remainder rather than the
	// whole prompt, so the caller folds through the opposite convention.
	exclusive bool
	// haveSplit records that a split-bearing event has been folded, so the reading
	// describes how the request was served and no later split-free event may
	// replace its input reading.
	haveSplit bool
	have      bool
}

func newStreamUsage() *streamUsage { return &streamUsage{} }

// merge folds one usage event. The counters are read from the last event that
// carried a cache split, because that is the event describing how the request was
// served. A stream that first sent a larger input_tokens with no cache counters
// at all is sending an estimate, and that is exactly when the split event's
// input_tokens is the remainder. Without such a predecessor — a single-event
// stream, or a gateway that omits the estimate — the route's declared convention
// stands. A stream with no split anywhere (a cold request, or a gateway that
// reports no cache counters) keeps the newest non-zero reading of each counter, so
// a later event that omits a field cannot erase an earlier one that carried it.
// Once a split has been folded, a later split-free event is a restatement rather
// than a correction: its input_tokens is the whole prompt, and taking it would add
// the served cache read to the prompt a second time. output_tokens is genuinely
// cumulative and keeps the maximum.
func (u *streamUsage) merge(usage *wireUsage) {
	if usage == nil {
		return
	}
	u.out = max(u.out, usage.OutputTokens)
	u.have = true
	if usage.CacheReadInputTokens == 0 && usage.CacheCreationInputTokens == 0 {
		if usage.InputTokens != 0 && !u.haveSplit {
			u.in = usage.InputTokens
			u.estimateInput = max(u.estimateInput, usage.InputTokens)
		}
		return
	}
	// Two independent signals that input_tokens is the uncached remainder: a
	// larger split-free predecessor sent as an estimate, or a cache read that
	// simply cannot fit inside input_tokens.
	u.exclusive = u.estimateInput > usage.InputTokens || usage.CacheReadInputTokens > usage.InputTokens
	u.in, u.cacheCreate, u.cacheRead = usage.InputTokens, usage.CacheCreationInputTokens, usage.CacheReadInputTokens
	u.haveSplit = true
}
