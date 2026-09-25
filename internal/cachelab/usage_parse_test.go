package cachelab

import "testing"

// usageOf parses one response body exactly as the recorder does, so a parser
// test exercises the production path rather than a copy of it.
func usageOf(t *testing.T, body string) ReportedUsage {
	t.Helper()
	return parseUsage([]byte(body))
}

// TestUsageParserResolvesTheAnthropicVocabulary pins the Anthropic reading:
// input_tokens counts the uncached prompt, so the miss side is input + creation
// and the prompt is the hit plus that miss.
func TestUsageParserResolvesTheAnthropicVocabulary(t *testing.T) {
	got := usageOf(t, `{"usage":{"input_tokens":10,"cache_read_input_tokens":90,"cache_creation_input_tokens":5,"output_tokens":7}}`)
	if !got.Reported || !got.Split || got.Shape != UsageShapeAnthropic {
		t.Fatalf("usage = %+v, want a resolved anthropic split", got)
	}
	if got.Prompt != 105 || got.Hit != 90 || got.Miss != 15 || got.Write != 5 || got.Completion != 7 {
		t.Fatalf("split = prompt %d hit %d miss %d write %d completion %d, want 105/90/15/5/7",
			got.Prompt, got.Hit, got.Miss, got.Write, got.Completion)
	}
	if got.Prompt != got.Hit+got.Miss {
		t.Fatalf("prompt %d != hit+miss %d: the parser must keep the split closed", got.Prompt, got.Hit+got.Miss)
	}
}

// TestUsageParserResolvesTheOpenAIVocabulary pins the OpenAI-compatible reading:
// prompt_tokens is the whole prompt and the cache read is a subset of it, so the
// miss is prompt minus hit unless the provider spells it out.
func TestUsageParserResolvesTheOpenAIVocabulary(t *testing.T) {
	got := usageOf(t, `{"usage":{"prompt_tokens":1000,"prompt_cache_hit_tokens":900,"completion_tokens":12}}`)
	if !got.Reported || !got.Split || got.Shape != UsageShapeOpenAI {
		t.Fatalf("usage = %+v, want a resolved openai split", got)
	}
	if got.Prompt != 1000 || got.Hit != 900 || got.Miss != 100 {
		t.Fatalf("split = prompt %d hit %d miss %d, want 1000/900/100", got.Prompt, got.Hit, got.Miss)
	}
	// An explicit miss wins over the derived one.
	explicit := usageOf(t, `{"usage":{"prompt_tokens":1000,"prompt_cache_hit_tokens":900,"prompt_cache_miss_tokens":90}}`)
	if explicit.Miss != 90 {
		t.Fatalf("explicit miss = %d, want the provider's own 90 rather than the derived 100", explicit.Miss)
	}
	// The alternative spellings resolve to the same hit.
	for _, body := range []string{
		`{"usage":{"prompt_tokens":1000,"cached_tokens":900}}`,
		`{"usage":{"prompt_tokens":1000,"cache_read_tokens":900}}`,
	} {
		if got := usageOf(t, body); got.Hit != 900 || !got.Split {
			t.Fatalf("usage %s = %+v, want hit 900 under an alternative spelling", body, got)
		}
	}
}

// TestUsageParserDeclinesOneObjectSpeakingTwoDialects pins the rule: when ONE
// object carries both vocabularies, no split is claimed rather than one dialect
// being preferred silently. The live gateway puts the two vocabularies in
// DIFFERENT objects, and that case resolves — see the oracle tests.
func TestUsageParserDeclinesOneObjectSpeakingTwoDialects(t *testing.T) {
	got := usageOf(t, `{"usage":{"input_tokens":42,"prompt_tokens":42}}`)
	if !got.Reported {
		t.Fatalf("usage = %+v, want the response recognized as carrying usage", got)
	}
	if got.Split {
		t.Fatalf("usage = %+v, want no split: one object speaking two dialects is not decidable", got)
	}
	if got.Problem != UsageProblemUnresolved {
		t.Fatalf("problem = %q, want %q", got.Problem, UsageProblemUnresolved)
	}
}

// TestUsageParserReadsADialectWithAnUnpopulatedSiblingKey pins what happens when
// a usage object carries an OpenAI reading beside a zero-valued Anthropic cache
// field: that key makes it speak both dialects, and one object speaking two
// dialects is declined rather than read as one.
func TestUsageParserReadsADialectWithAnUnpopulatedSiblingKey(t *testing.T) {
	// The cache_read_input_tokens makes the object speak both dialects, so it is
	// declined. The OpenAI reading is reachable only without that key.
	declined := usageOf(t, `{"usage":{"prompt_tokens":1000,"prompt_cache_hit_tokens":900,"cache_read_input_tokens":0}}`)
	if declined.Split || declined.Problem != UsageProblemUnresolved {
		t.Fatalf("usage = %+v, want one object speaking two dialects declined", declined)
	}
	// The same numbers without the Anthropic key resolve through OpenAI.
	got := usageOf(t, `{"usage":{"prompt_tokens":1000,"prompt_cache_hit_tokens":900}}`)
	if !got.Split || got.Shape != UsageShapeOpenAI {
		t.Fatalf("usage = %+v, want the openai reading", got)
	}
	if got.Hit != 900 || got.Miss != 100 {
		t.Fatalf("split = hit %d miss %d, want 900/100", got.Hit, got.Miss)
	}
}

// TestUsageParserDistinguishesAMissingKeyFromAnExplicitZero pins the gap the
// plan names: `has` must report a key that was present with value zero, because
// a provider that reports cache_read_input_tokens=0 has reported a real miss,
// while a provider that omitted the key has reported nothing.
func TestUsageParserDistinguishesAMissingKeyFromAnExplicitZero(t *testing.T) {
	// The key-vs-zero distinction is asserted through the parser's own output,
	// not the collector's internal map: what a reader relies on is the resolved
	// reading. An explicit zero resolves to a full miss, which is a measurement.
	got := usageOf(t, `{"usage":{"input_tokens":100,"cache_read_input_tokens":0}}`)
	if !got.Split || got.Hit != 0 || got.Miss != 100 {
		t.Fatalf("usage = %+v, want an explicit zero hit and a full miss", got)
	}
	// The omitted key has no split, which is not a zero rate.
	missing := usageOf(t, `{"usage":{"input_tokens":100}}`)
	if missing.Split {
		t.Fatalf("usage = %+v, want no split when the response reported no cache read at all", missing)
	}
	if missing.Problem != UsageProblemNoCacheRead {
		t.Fatalf("problem = %q, want %q", missing.Problem, UsageProblemNoCacheRead)
	}
}

// TestUsageParserReadsNestedAndStreamedUsage pins the two shapes the recorder
// actually receives: a usage object nested inside a larger document, and a
// stream whose numbers arrive across several events.
//
// The gateway's billing block is read as the oracle, not as the reading.
func TestUsageParserReadsNestedAndStreamedUsage(t *testing.T) {
	nested := usageOf(t, `{"id":"x","usage":{"input_tokens":10,"cache_read_input_tokens":90,"output_tokens":3},"billing_usage":{"openai_usage":{"prompt_tokens":1000,"cached_tokens":900}}}`)
	if !nested.Split || nested.Hit != 90 || nested.Miss != 10 || nested.Completion != 3 {
		t.Fatalf("nested usage = %+v, want the protocol's own reading out of the nesting", nested)
	}
	if !nested.OraclePresent || nested.OraclePrompt != 1000 || nested.OracleHit != 900 {
		t.Fatalf("oracle = %+v, want the gateway's own account recorded beside it", nested)
	}
	stream := usageOf(t, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":100,\"cache_read_input_tokens\":900}}}\n\n"+
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n")
	if !stream.Split || stream.Hit != 900 || stream.Miss != 100 || stream.Completion != 5 {
		t.Fatalf("streamed usage = %+v, want input from the first event and output from the last", stream)
	}
}

// TestUsageParserTakesTheLastValueForARepeatedKey pins the last-wins rule: a
// stream that repeats a key is reporting the settled value last, so a reader
// that kept the first would report a stale number.
func TestUsageParserTakesTheLastValueForARepeatedKey(t *testing.T) {
	got := usageOf(t, "data: {\"usage\":{\"prompt_tokens\":1000,\"prompt_cache_hit_tokens\":100}}\n\n"+
		"data: {\"usage\":{\"prompt_tokens\":1000,\"prompt_cache_hit_tokens\":900}}\n\n")
	if got.Hit != 900 {
		t.Fatalf("hit = %d, want the last reported 900", got.Hit)
	}
}

// TestUsageParserReportsNegativeAndUnusableSplits pins that a response whose
// numbers cannot be read is reported as a problem rather than silently coerced:
// a negative split is a provider-side anomaly, and naming it keeps it out of a
// rate without pretending it was a miss.
func TestUsageParserReportsNegativeAndUnusableSplits(t *testing.T) {
	negative := usageOf(t, `{"usage":{"prompt_tokens":100,"prompt_cache_hit_tokens":900}}`)
	if negative.Split || negative.Problem != UsageProblemNegativeSplit {
		t.Fatalf("usage = %+v, want a negative split named rather than clamped", negative)
	}
	empty := usageOf(t, `{"id":"x"}`)
	if empty.Reported || empty.Problem != UsageProblemNoUsage {
		t.Fatalf("usage = %+v, want no usage reported", empty)
	}
}

// TestUsageParserRecordsTheKeyNamesItRead pins the audit trail: a sample states
// which keys it resolved from, so a vocabulary the provider changed is visible
// in the journal rather than inferred from a rate.
func TestUsageParserRecordsTheKeyNamesItRead(t *testing.T) {
	got := usageOf(t, `{"usage":{"input_tokens":10,"cache_read_input_tokens":90}}`)
	want := []string{keyCacheReadInput, keyInputTokens}
	if len(got.Keys) != len(want) {
		t.Fatalf("keys = %v, want %v", got.Keys, want)
	}
	for i := range want {
		if got.Keys[i] != want[i] {
			t.Fatalf("keys = %v, want %v (sorted, so two reads of one response agree)", got.Keys, want)
		}
	}
}
