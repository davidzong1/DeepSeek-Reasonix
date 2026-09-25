package cachelab

import (
	"strings"
	"testing"
)

// usageStream renders an SSE body from the given data payloads, so a test can
// state the exact event sequence a provider sent.
func usageStream(payloads ...string) string {
	var b strings.Builder
	for _, payload := range payloads {
		b.WriteString("event: x\ndata: ")
		b.WriteString(payload)
		b.WriteString("\n\n")
	}
	return b.String()
}

// The usage shapes this parser must read. Each case names the vocabulary the
// response spoke, what the reading is, and — where the gateway sent its own
// account — whether the two agree.
func TestParseUsageReadsEachProviderShape(t *testing.T) {
	cases := []struct {
		name string
		body string
		want ReportedUsage
	}{
		{
			// The shape the live gateway sent on 2026-09-24: a split-free
			// message_start carrying the prompt estimate, then a message_delta
			// carrying the served remainder beside the gateway's own account.
			name: "gateway delta with nested oracle",
			body: usageStream(
				`{"type":"message_start","message":{"usage":{"input_tokens":1312,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`,
				`{"type":"message_delta","usage":{"input_tokens":78,"cache_creation_input_tokens":0,"cache_read_input_tokens":1024,"output_tokens":16,"billing_usage":{"semantic":"openai","openai_usage":{"prompt_tokens":1102,"completion_tokens":16,"prompt_tokens_details":{"cached_tokens":1024}}}}}`,
			),
			want: ReportedUsage{
				Reported: true, Split: true, Shape: UsageShapeAnthropic,
				Keys: []string{"cache_creation_input_tokens", "cache_read_input_tokens", "input_tokens", "output_tokens"},
				// input_tokens is the remainder, so the prompt is read+input.
				Prompt: 1102, Hit: 1024, Miss: 78, Completion: 16,
				OraclePresent: true, OracleAgrees: true,
				OracleKeys:   []string{"cached_tokens", "completion_tokens", "prompt_tokens"},
				OraclePrompt: 1102, OracleHit: 1024, OracleMiss: 78,
			},
		},
		{
			// The same request on a response that carried no billing block: the
			// reading is unchanged and the oracle is undecided, not agreeing.
			name: "gateway delta without an oracle",
			body: usageStream(
				`{"type":"message_start","message":{"usage":{"input_tokens":1312,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`,
				`{"type":"message_delta","usage":{"input_tokens":78,"cache_creation_input_tokens":0,"cache_read_input_tokens":1024,"output_tokens":16}}`,
			),
			want: ReportedUsage{
				Reported: true, Split: true, Shape: UsageShapeAnthropic,
				Prompt: 1102, Hit: 1024, Miss: 78, Completion: 16,
			},
		},
		{
			// A full hit: the served remainder is zero and the cache read covers
			// the whole prompt. Zero here is a reading, not an absent field.
			name: "full hit with a zero remainder",
			body: usageStream(
				`{"type":"message_start","message":{"usage":{"input_tokens":1604,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`,
				`{"type":"message_delta","usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":1280,"output_tokens":16}}`,
			),
			want: ReportedUsage{
				Reported: true, Split: true, Shape: UsageShapeAnthropic,
				Prompt: 1280, Hit: 1280, Miss: 0, Completion: 16,
			},
		},
		{
			// A cold request: no cache read anywhere, so there is no split. That is
			// an answer — "no cache read was reported" — and not a zero rate.
			name: "cold request has no split",
			body: usageStream(
				`{"type":"message_start","message":{"usage":{"input_tokens":988,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`,
				`{"type":"message_delta","usage":{"input_tokens":816,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":16}}`,
			),
			want: ReportedUsage{
				Reported: true, Split: true, Shape: UsageShapeAnthropic,
				Prompt: 816, Hit: 0, Miss: 816, Completion: 16,
			},
		},
		{
			// The OpenAI vocabulary with the cache read nested in
			// prompt_tokens_details, which is where that dialect puts it.
			name: "openai vocabulary with a nested detail block",
			body: `{"usage":{"prompt_tokens":1102,"completion_tokens":16,"prompt_tokens_details":{"cached_tokens":1024}}}`,
			want: ReportedUsage{
				Reported: true, Split: true, Shape: UsageShapeOpenAI,
				Keys:   []string{"cached_tokens", "completion_tokens", "prompt_tokens"},
				Prompt: 1102, Hit: 1024, Miss: 78, Completion: 16,
			},
		},
		{
			// A detail block that is present and explicitly zero is a reported
			// zero hit, which is a different fact from the key being absent.
			name: "explicit zero detail block",
			body: `{"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":0}}}`,
			want: ReportedUsage{
				Reported: true, Split: true, Shape: UsageShapeOpenAI,
				Prompt: 100, Hit: 0, Miss: 100, Completion: 5,
			},
		},
		{
			// A prompt with no cache read at all: no split, and the reason is
			// named rather than the sample being read as a zero hit.
			name: "prompt without a cache read",
			body: `{"usage":{"prompt_tokens":100,"completion_tokens":5}}`,
			want: ReportedUsage{
				Reported: true, Problem: UsageProblemNoCacheRead, Shape: UsageShapeOpenAI,
				Keys: []string{"completion_tokens", "prompt_tokens"}, Completion: 5,
			},
		},
		{
			// One event speaking both dialects: which one describes how the
			// request was served is not decidable, so no split is claimed.
			name: "one event speaking both dialects",
			body: `{"usage":{"input_tokens":42,"prompt_tokens":42}}`,
			want: ReportedUsage{
				Reported: true, Problem: UsageProblemUnresolved,
				Keys: []string{"input_tokens", "prompt_tokens"},
			},
		},
		{
			// Two events speaking different dialects. The old fold merged them
			// into a split that neither event stated.
			name: "two events speaking different dialects",
			body: usageStream(
				`{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":90,"cache_creation_input_tokens":0}}}`,
				`{"type":"message_delta","usage":{"prompt_tokens":100,"cached_tokens":90}}`,
			),
			want: ReportedUsage{
				Reported: true, Problem: UsageProblemDialectMix,
				Keys: []string{"cache_creation_input_tokens", "cache_read_input_tokens", "cached_tokens", "input_tokens", "prompt_tokens"},
			},
		},
		{
			// Only the gateway's private block: a reading exists, but it is the
			// gateway's own account rather than the protocol's usage event.
			name: "oracle only",
			body: `{"billing_usage":{"semantic":"openai","openai_usage":{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":90}}}}`,
			want: ReportedUsage{
				Reported: true, Problem: UsageProblemOracleOnly,
				OraclePresent: true,
				OracleKeys:    []string{"cached_tokens", "prompt_tokens"},
				OraclePrompt:  100, OracleHit: 90, OracleMiss: 10,
			},
		},
		{
			name: "no usage at all",
			body: `{"type":"message_stop"}`,
			want: ReportedUsage{Problem: UsageProblemNoUsage},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseUsage([]byte(tc.body))
			compareReportedUsage(t, got, tc.want)
		})
	}
}

// compareReportedUsage asserts the fields a reading is defined by.
func compareReportedUsage(t *testing.T, got, want ReportedUsage) {
	t.Helper()
	if got.Reported != want.Reported || got.Split != want.Split || got.Problem != want.Problem || got.Shape != want.Shape {
		t.Fatalf("reported/split/problem/shape = %v/%v/%q/%q, want %v/%v/%q/%q",
			got.Reported, got.Split, got.Problem, got.Shape, want.Reported, want.Split, want.Problem, want.Shape)
	}
	if got.Prompt != want.Prompt || got.Hit != want.Hit || got.Miss != want.Miss || got.Completion != want.Completion {
		t.Fatalf("prompt/hit/miss/completion = %d/%d/%d/%d, want %d/%d/%d/%d",
			got.Prompt, got.Hit, got.Miss, got.Completion, want.Prompt, want.Hit, want.Miss, want.Completion)
	}
	if want.Keys != nil && strings.Join(got.Keys, ",") != strings.Join(want.Keys, ",") {
		t.Fatalf("keys = %v, want %v", got.Keys, want.Keys)
	}
	if got.OraclePresent != want.OraclePresent || got.OracleAgrees != want.OracleAgrees {
		t.Fatalf("oracle present/agrees = %v/%v, want %v/%v",
			got.OraclePresent, got.OracleAgrees, want.OraclePresent, want.OracleAgrees)
	}
	if got.OraclePrompt != want.OraclePrompt || got.OracleHit != want.OracleHit || got.OracleMiss != want.OracleMiss {
		t.Fatalf("oracle prompt/hit/miss = %d/%d/%d, want %d/%d/%d",
			got.OraclePrompt, got.OracleHit, got.OracleMiss, want.OraclePrompt, want.OracleHit, want.OracleMiss)
	}
	if want.OracleKeys != nil && strings.Join(got.OracleKeys, ",") != strings.Join(want.OracleKeys, ",") {
		t.Fatalf("oracle keys = %v, want %v", got.OracleKeys, want.OracleKeys)
	}
	if got.Split && got.Prompt != got.Hit+got.Miss {
		t.Fatalf("prompt %d != hit %d + miss %d", got.Prompt, got.Hit, got.Miss)
	}
}

// TestParseUsageBindsKeysToTheirOwnObject pins the defect the live gateway
// exposed: its billing block repeats the Anthropic key names with zero values
// beside its own OpenAI-dialect numbers. Harvesting every recognised name at any
// depth merged the two objects, so which value a name ended up with depended on
// the order Go walked a map — the same bytes parsed to two different readings.
func TestParseUsageBindsKeysToTheirOwnObject(t *testing.T) {
	body := usageStream(
		`{"type":"message_start","message":{"usage":{"input_tokens":1312,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`,
		`{"type":"message_delta","usage":{"input_tokens":78,"cache_creation_input_tokens":0,"cache_read_input_tokens":1024,"output_tokens":16,"billing_usage":{"semantic":"openai","openai_usage":{"prompt_tokens":1102,"completion_tokens":16,"input_tokens":0,"output_tokens":0,"prompt_tokens_details":{"cached_tokens":1024}}}}}`,
	)
	first := parseUsage([]byte(body))
	for i := 0; i < 500; i++ {
		got := parseUsage([]byte(body))
		if got.Prompt != first.Prompt || got.Hit != first.Hit || got.Miss != first.Miss ||
			got.Completion != first.Completion || got.Problem != first.Problem ||
			strings.Join(got.Keys, ",") != strings.Join(first.Keys, ",") {
			t.Fatalf("parse %d differed from the first: %+v vs %+v", i, got, first)
		}
	}
	// The oracle's zero-valued input_tokens must not overwrite the served
	// remainder, and its zero output_tokens must not erase the completion count.
	if first.Miss != 78 {
		t.Fatalf("miss = %d, want the served remainder 78 (the oracle repeats input_tokens=0)", first.Miss)
	}
	if first.Completion != 16 {
		t.Fatalf("completion = %d, want 16 (the oracle repeats output_tokens=0)", first.Completion)
	}
	if first.Prompt != 1102 || first.Hit != 1024 {
		t.Fatalf("prompt/hit = %d/%d, want 1102/1024", first.Prompt, first.Hit)
	}
}

// TestParseUsageReportsTheOracleOnlyWhenItDisagrees keeps the oracle a sidecar:
// a reading the gateway's own account contradicts is reported as a disagreement,
// never silently replaced by the oracle.
func TestParseUsageReportsTheOracleOnlyWhenItDisagrees(t *testing.T) {
	body := usageStream(
		`{"type":"message_delta","usage":{"input_tokens":5741,"cache_creation_input_tokens":0,"cache_read_input_tokens":33408,"output_tokens":16,"billing_usage":{"semantic":"openai","openai_usage":{"prompt_tokens":33437,"prompt_tokens_details":{"cached_tokens":33408}}}}}`,
	)
	got := parseUsage([]byte(body))
	if !got.Split || got.Prompt != 39149 || got.Miss != 5741 {
		t.Fatalf("primary reading = prompt %d miss %d split %v, want the protocol's own 39149/5741", got.Prompt, got.Miss, got.Split)
	}
	if !got.OraclePresent {
		t.Fatal("the response carried a billing block, so the oracle must be recorded as present")
	}
	if got.OracleAgrees {
		t.Fatal("the gateway's own account disagrees with this reading, so agreement must not be claimed")
	}
	if got.OraclePrompt != 33437 || got.OracleHit != 33408 || got.OracleMiss != 29 {
		t.Fatalf("oracle = %d/%d/%d, want 33437/33408/29", got.OraclePrompt, got.OracleHit, got.OracleMiss)
	}
}

// TestParseUsagePrefersAPopulatedSpellingOfOneNumber covers a usage object that
// carries two spellings of the same number, one of them unpopulated. The zero is
// the empty field, not the reading: preferring it would report a served cache
// read as a zero hit.
func TestParseUsagePrefersAPopulatedSpellingOfOneNumber(t *testing.T) {
	body := `{"usage":{"prompt_tokens":100,"prompt_cache_hit_tokens":90,"cached_tokens":0,"completion_tokens":5}}`
	got := parseUsage([]byte(body))
	if !got.Split || got.Hit != 90 || got.Miss != 10 {
		t.Fatalf("hit/miss = %d/%d split %v, want 90/10 from the populated spelling", got.Hit, got.Miss, got.Split)
	}
	// Every spelling present is zero: that is a reported zero hit.
	zero := parseUsage([]byte(`{"usage":{"prompt_tokens":100,"prompt_cache_hit_tokens":0,"cached_tokens":0}}`))
	if !zero.Split || zero.Hit != 0 || zero.Miss != 100 {
		t.Fatalf("all-zero spellings = hit %d miss %d split %v, want a reported 0/100", zero.Hit, zero.Miss, zero.Split)
	}
}

// TestParseUsageKeepsTheOracleOutOfThePrimaryReading holds the boundary the
// contract fixes: the gateway's private block can corroborate a sample and can
// never create one.
func TestParseUsageKeepsTheOracleOutOfThePrimaryReading(t *testing.T) {
	// An OpenAI-dialect response whose own numbers are complete, beside an oracle
	// that says something else: the primary reading is the response's, and the
	// disagreement is recorded.
	body := `{"usage":{"prompt_tokens":100,"cached_tokens":10,"completion_tokens":5,"billing_usage":{"openai_usage":{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":90}}}}}`
	got := parseUsage([]byte(body))
	if got.Hit != 10 {
		t.Fatalf("hit = %d, want the response's own 10 (the oracle is a sidecar)", got.Hit)
	}
	if !got.OraclePresent || got.OracleAgrees {
		t.Fatalf("oracle present/agrees = %v/%v, want present and disagreeing", got.OraclePresent, got.OracleAgrees)
	}
	if got.OracleHit != 90 {
		t.Fatalf("oracle hit = %d, want 90", got.OracleHit)
	}
}
