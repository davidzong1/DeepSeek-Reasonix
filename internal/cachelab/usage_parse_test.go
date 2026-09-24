package cachelab

import (
	"encoding/json"
	"testing"
)

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

// TestUsageParserRefusesAMixedVocabulary pins the gap the earlier probe found: a
// response carrying both vocabularies at once used to resolve to neither, which
// silently produced no split on a response that did carry a cache read. It must
// now resolve through one of them and say which.
func TestUsageParserRefusesAMixedVocabulary(t *testing.T) {
	body := `{"usage":{"input_tokens":10,"cache_read_input_tokens":90,"cache_creation_input_tokens":0,` +
		`"prompt_tokens":100,"prompt_cache_hit_tokens":90,"completion_tokens":4}}`
	got := usageOf(t, body)
	if !got.Reported {
		t.Fatalf("usage = %+v, want the response recognized as carrying usage", got)
	}
	if !got.Split {
		t.Fatalf("usage = %+v, want a split: both vocabularies agree the hit is 90, so refusing is a lost measurement", got)
	}
	if got.Shape != UsageShapeAnthropic {
		t.Fatalf("shape = %q, want the gateway's own %q vocabulary preferred", got.Shape, UsageShapeAnthropic)
	}
	if got.Hit != 90 || got.Miss != 10 || got.Prompt != 100 {
		t.Fatalf("split = hit %d miss %d prompt %d, want 90/10/100", got.Hit, got.Miss, got.Prompt)
	}
}

// TestUsageParserPrefersTheProvidersOwnShapeOverAMixedOne pins the fallback: a
// response whose two vocabularies disagree is resolved through the shape the
// response's own event stream used, and the choice is recorded in Shape.
func TestUsageParserPrefersTheProvidersOwnShapeOverAMixedOne(t *testing.T) {
	// Only OpenAI keys carry a value here; the anthropic keys are present but the
	// response has no input_tokens, so the OpenAI reading is the usable one.
	got := usageOf(t, `{"usage":{"prompt_tokens":1000,"prompt_cache_hit_tokens":900,"cache_read_input_tokens":0}}`)
	if !got.Split || got.Shape != UsageShapeOpenAI {
		t.Fatalf("usage = %+v, want the openai reading when the anthropic keys cannot resolve", got)
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
	var raw rawUsage
	raw.merge(map[string]any{"input_tokens": json.Number("100"), "cache_read_input_tokens": json.Number("0")})
	if !raw.has(keyCacheReadInput) {
		t.Fatal("an explicit zero cache read must count as present: the provider reported a full miss")
	}
	var absent rawUsage
	absent.merge(map[string]any{"input_tokens": json.Number("100")})
	if absent.has(keyCacheReadInput) {
		t.Fatal("an omitted cache read must not count as present")
	}
	// The explicit zero resolves to a full miss, which is a measurement.
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
func TestUsageParserReadsNestedAndStreamedUsage(t *testing.T) {
	nested := usageOf(t, `{"id":"x","billing_usage":{"openai_usage":{"prompt_tokens":1000,"cached_tokens":900}},"extra":{"usage":{"completion_tokens":3}}}`)
	if !nested.Split || nested.Hit != 900 || nested.Prompt != 1000 || nested.Completion != 3 {
		t.Fatalf("nested usage = %+v, want the values read out of the nesting", nested)
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
