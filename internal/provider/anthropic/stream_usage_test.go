package anthropic

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

// usageEvent is one usage-bearing event of a synthetic Messages stream.
//
// kind is "start" (message_start, whose usage nests under message.usage) or
// "delta" (message_delta, whose usage sits at the top level). body is the raw
// usage object; "" means the event carries no usage key at all, which is a
// different wire fact from a usage object whose counters are all zero.
type usageEvent struct {
	kind string
	body string
}

// usageStream renders a Messages stream from usage events in the given order, so
// an event shape can be described declaratively instead of as hand-written SSE.
func usageStream(events ...usageEvent) string {
	var b strings.Builder
	for _, e := range events {
		switch e.kind {
		case "start":
			b.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":")
			if e.body == "" {
				b.WriteString(`{"id":"msg_1"}`)
			} else {
				b.WriteString(`{"id":"msg_1","usage":` + e.body + `}`)
			}
			b.WriteString("}\n\n")
		case "delta":
			b.WriteString("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}")
			if e.body != "" {
				b.WriteString(`,"usage":` + e.body)
			}
			b.WriteString("}\n\n")
		}
	}
	b.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n")
	return b.String()
}

// usageBody renders one wire usage object.
func usageBody(in, create, read, out int) string {
	return fmt.Sprintf(`{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":%d}`,
		in, create, read, out)
}

// The dialects this adapter claims to support, as the four routes the matrix
// distinguishes. They differ in two independent things: the cache-accounting
// convention the route declares (client.inclusiveUsage), and which event its
// stream puts the counters on. The replay obligation (client.deepseek) is a
// third thing that happens to travel with the official endpoint and is not
// asserted here — see client.inclusiveUsage for why they are separate.
var (
	nativeClient = &client{name: "anthropic"}
	// longCatClient is the delta-only Anthropic-compatible gateway. It reports
	// input_tokens as the uncached part, like the native API.
	longCatClient = &client{name: "longcat-anthropic"}
	// deepSeekOfficialClient is api.deepseek.com/anthropic: the one route that
	// documents input_tokens as covering the cache counters.
	deepSeekOfficialClient = &client{name: "deepseek-anthropic", deepseek: true, inclusiveUsage: true}
	// deepSeekCompatClient is a DeepSeek-compatible gateway that carries the
	// replay obligation but reports the native split, so its input_tokens is the
	// uncached part. This is the combination one flag could not express.
	deepSeekCompatClient = &client{name: "gateway", deepseek: true}
)

// usageReadout renders the billable fields for a failure message.
func usageReadout(u *provider.Usage) string {
	return fmt.Sprintf("prompt=%d hit=%d miss=%d write=%d out=%d",
		u.PromptTokens, u.CacheHitTokens, u.CacheMissTokens, u.CacheWriteTokens, u.CompletionTokens)
}

// usageShape is one row of the event-shape matrix.
//
// status records what the row's reading is worth: "verified" when the shape is
// observed on a supported route and the reading follows from the route's
// declared convention; "unresolved" when the counters alone cannot distinguish
// the reading from an alternative; "malformed" when no supported provider sends
// the shape and the row exists only to pin what the adapter does with it. An
// unresolved row still pins today's output, because a consumer that reads
// hit == 0 must call it "no cache read reported", never a cold start.
type usageShape struct {
	name   string
	client *client
	events []usageEvent
	// want is the expected normalized reading.
	prompt, hit, miss, write, out int
	status                        string
	why                           string
}

func (s usageShape) want() string {
	return fmt.Sprintf("prompt=%d hit=%d miss=%d write=%d out=%d", s.prompt, s.hit, s.miss, s.write, s.out)
}

// TestUsageEventShapeMatrix is the deliverable matrix for the shapes the
// Anthropic-compatible routes actually send. Each row names the provider shape it
// stands for and asserts the whole normalized reading, so a fold rule that trades
// one counter for another cannot pass by accident.
//
// Every row also asserts prompt == hit + miss. That invariant is necessary but
// not sufficient — the adapter constructs it — so it is asserted here only to
// catch a fold that drops a counter outright.
//
// Rows marked "unresolved" pin a reading the counters alone cannot justify. They
// are here so the limit is testable rather than a comment: a change that makes
// one of them look decisive is a change to what the adapter claims to know.
func TestUsageEventShapeMatrix(t *testing.T) {
	shapes := []usageShape{
		{
			name:   "native: message_start carries the split, message_delta only output",
			client: nativeClient,
			events: []usageEvent{
				{"start", usageBody(100, 10, 50, 0)},
				{"delta", usageBody(0, 0, 0, 25)},
			},
			prompt: 160, hit: 50, miss: 110, write: 10, out: 25,
			status: "verified",
			why:    "input_tokens excludes both cache counters on the native route, so the prompt is their sum and the cache write stays uncached input",
		},
		{
			name:   "native cold: no cache counter anywhere in the stream",
			client: nativeClient,
			events: []usageEvent{
				{"start", usageBody(100, 0, 0, 0)},
				{"delta", usageBody(0, 0, 0, 25)},
			},
			prompt: 100, hit: 0, miss: 100, write: 0, out: 25,
			status: "verified",
			why:    "no split ever seen, so the route's own convention decides",
		},
		{
			name:   "native full hit: input_tokens is zero and the read is the whole prompt",
			client: nativeClient,
			events: []usageEvent{
				{"start", usageBody(0, 0, 100, 0)},
				{"delta", usageBody(0, 0, 0, 25)},
			},
			prompt: 100, hit: 100, miss: 0, write: 0, out: 25,
			status: "verified",
			why:    "a read larger than input_tokens cannot be a subset of it",
		},
		{
			name:   "LongCat: message_start has no usage, message_delta carries everything",
			client: longCatClient,
			events: []usageEvent{
				{"start", ""},
				{"delta", usageBody(13, 5, 7, 3)},
			},
			prompt: 25, hit: 7, miss: 18, write: 5, out: 3,
			status: "verified",
			why:    "delta-only gateway; the native placement never fires",
		},
		{
			name:   "LongCat: the delta repeats and the last reading stands",
			client: longCatClient,
			events: []usageEvent{
				{"start", ""},
				{"delta", usageBody(13, 5, 7, 3)},
				{"delta", usageBody(13, 5, 7, 3)},
			},
			prompt: 25, hit: 7, miss: 18, write: 5, out: 3,
			status: "verified",
			why:    "a repeated delta is idempotent",
		},
		{
			name:   "DeepSeek: message_start twice, the first an estimate without a split",
			client: deepSeekCompatClient,
			events: []usageEvent{
				{"start", usageBody(39149, 0, 0, 0)},
				{"start", usageBody(29, 0, 33408, 0)},
				{"delta", usageBody(29, 0, 33408, 2)},
			},
			prompt: 33437, hit: 33408, miss: 29, write: 0, out: 2,
			status: "verified",
			why:    "the split-bearing event describes how the request was served, not the estimate before it",
		},
		{
			name:   "DeepSeek cold: the estimate is followed by the served prompt with no split",
			client: deepSeekCompatClient,
			events: []usageEvent{
				{"start", usageBody(39149, 0, 0, 0)},
				{"delta", usageBody(33437, 0, 0, 2)},
			},
			prompt: 33437, hit: 0, miss: 33437, write: 0, out: 2,
			status: "verified",
			why:    "no split anywhere, so the later whole-prompt reading stands and every token is uncached",
		},
		{
			name:   "DeepSeek: the served split is repeated on a later event",
			client: deepSeekCompatClient,
			events: []usageEvent{
				{"start", usageBody(39149, 0, 0, 0)},
				{"start", usageBody(29, 0, 33408, 0)},
				{"start", usageBody(29, 0, 33408, 0)},
				{"delta", usageBody(29, 0, 33408, 2)},
			},
			prompt: 33437, hit: 33408, miss: 29, write: 0, out: 2,
			status: "verified",
			why:    "a repeated split is idempotent",
		},
		{
			name:   "DeepSeek: the split arrives, then a later event restates the prompt without one",
			client: deepSeekCompatClient,
			events: []usageEvent{
				{"start", usageBody(39149, 0, 0, 0)},
				{"start", usageBody(29, 0, 33408, 0)},
				{"delta", usageBody(33437, 0, 0, 2)},
			},
			prompt: 33437, hit: 33408, miss: 29, write: 0, out: 2,
			status: "verified",
			why:    "a split-free restatement after the split must not erase the served read or add the read to the prompt twice",
		},
		{
			name:   "DeepSeek: the split arrives first and the estimate after it",
			client: deepSeekCompatClient,
			events: []usageEvent{
				{"start", usageBody(29, 0, 33408, 0)},
				{"start", usageBody(39149, 0, 0, 0)},
				{"delta", usageBody(29, 0, 33408, 2)},
			},
			prompt: 33437, hit: 33408, miss: 29, write: 0, out: 2,
			status: "verified",
			why:    "event order does not change which event describes the serving",
		},
		{
			name:   "DeepSeek: a read larger than input_tokens is the uncached remainder",
			client: deepSeekCompatClient,
			events: []usageEvent{
				{"delta", usageBody(10, 0, 50, 5)},
			},
			prompt: 60, hit: 50, miss: 10, write: 0, out: 5,
			status: "verified",
			why:    "the counters decide the convention when they cannot be inclusive",
		},
		{
			name:   "DeepSeek: a full hit whose split event reports input=0",
			client: deepSeekCompatClient,
			events: []usageEvent{
				{"start", usageBody(39149, 0, 0, 0)},
				{"delta", usageBody(0, 0, 39149, 2)},
			},
			prompt: 39149, hit: 39149, miss: 0, write: 0, out: 2,
			status: "verified",
			why:    "a read larger than input_tokens cannot be a subset of it",
		},
		{
			name:   "DeepSeek-compatible: a delta-only split whose input exceeds its read",
			client: deepSeekCompatClient,
			events: []usageEvent{
				{"start", ""},
				{"delta", usageBody(39047, 0, 39040, 4)},
			},
			prompt: 78087, hit: 39040, miss: 39047, write: 0, out: 4,
			status: "unresolved",
			why: "both conventions are self-consistent for these bytes: the exclusive reading gives " +
				"78,087 (input is the uncached part) and the inclusive reading gives 39,047 (the read is " +
				"a subset). Nothing in the counters discriminates, because a remainder-convention gateway " +
				"that sends no estimate predecessor is byte-identical to an inclusive-convention one. The " +
				"reading follows the route's declared convention — which for this gateway is the native " +
				"Messages contract, since no observation has shown it covering its cache counters",
		},
		{
			name:   "DeepSeek: an over-estimate arrives after the split was already read",
			client: deepSeekCompatClient,
			events: []usageEvent{
				{"start", usageBody(39149, 0, 0, 0)},
				{"start", usageBody(29, 0, 33408, 0)},
				{"start", usageBody(99999, 0, 0, 0)},
				{"delta", usageBody(29, 0, 33408, 2)},
			},
			prompt: 33437, hit: 33408, miss: 29, write: 0, out: 2,
			status: "verified",
			why:    "a split-free event after the split is a restatement, so it cannot raise the estimate or the reading",
		},
		{
			name:   "DeepSeek: a split carrying only a cache write, no read",
			client: deepSeekCompatClient,
			events: []usageEvent{
				{"start", usageBody(39149, 0, 0, 0)},
				{"delta", usageBody(29, 33408, 0, 2)},
			},
			// cache_creation alone marks the serving event; a write is uncached input.
			prompt: 33437, hit: 0, miss: 33437, write: 33408, out: 2,
			status: "verified",
			why: "the production gateway reports cache_creation as zero in every observed " +
				"response, so this shape is constructed rather than observed. It is pinned so a " +
				"route that starts sending writes does not silently change the prompt arithmetic",
		},
		{
			name:   "DeepSeek: a negative counter is malformed and reaches the bill unchanged",
			client: deepSeekCompatClient,
			events: []usageEvent{
				{"delta", usageBody(-5, 0, 10, 2)},
			},
			prompt: 5, hit: 10, miss: -5, write: 0, out: 2,
			status: "malformed",
			why: "no Messages counter is negative, so no supported provider sends this. The adapter does " +
				"not clamp it — a guessed floor would invent a reading the counters never stated — and the " +
				"member record's own accounting marks the sample negative:cache_miss_tokens so it leaves " +
				"the baseline by a named exclusion rather than being silently corrected",
		},
		{
			name:   "DeepSeek: the final event zeroes every counter, so the split was omitted",
			client: deepSeekCompatClient,
			events: []usageEvent{
				{"start", usageBody(39149, 0, 0, 0)},
				{"delta", usageBody(0, 0, 0, 2)},
			},
			prompt: 39149, hit: 0, miss: 39149, write: 0, out: 2,
			status: "unresolved",
			why: "a full hit whose split the gateway omitted is indistinguishable from a stream that " +
				"reported no cache read at all: the counters carry no discriminator. The reading is pinned " +
				"so a consumer that sees hit == 0 calls it \"no cache read reported\" (Part A contract " +
				"§2.3), never a cold start. Resolving it needs the gateway's own billing block, which is " +
				"optional and private",
		},
	}

	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			usage := readUsage(t, s.client, usageStream(s.events...))

			if got := usageReadout(usage); got != s.want() {
				t.Fatalf("%s\n got %s\nwant %s\n(%s)", s.name, got, s.want(), s.why)
			}
			if usage.PromptTokens != usage.CacheHitTokens+usage.CacheMissTokens {
				t.Fatalf("prompt %d != hit %d + miss %d", usage.PromptTokens, usage.CacheHitTokens, usage.CacheMissTokens)
			}
		})
	}
}

// TestUsageNoUsageAnywhereEmitsNoUsageChunk pins the one shape where the adapter
// must stay silent: a stream that never reports usage at all. Emitting a
// zero-valued usage would be indistinguishable downstream from a request the
// provider served with no tokens, which is not a fact any counter stated.
func TestUsageNoUsageAnywhereEmitsNoUsageChunk(t *testing.T) {
	sse := usageStream(usageEvent{"start", ""}, usageEvent{"delta", ""})
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(sse))}
	ch := make(chan provider.Chunk)
	go deepSeekCompatClient.readStream(t.Context(), resp, ch)

	for ck := range ch {
		if ck.Type == provider.ChunkUsage {
			t.Fatalf("a stream with no usage object must not report usage: %s", usageReadout(ck.Usage))
		}
	}
}

// TestUsageMissingAndExplicitZeroAreIndistinguishable records the structural
// limit behind the unresolved rows. wireUsage decodes both an absent key and an
// explicit 0 into the same int, so no fold rule can tell a gateway that omitted
// a counter from one that reported zero. Anything that needs that distinction
// must read the raw event, not the merged counters.
func TestUsageMissingAndExplicitZeroAreIndistinguishable(t *testing.T) {
	absent := readUsage(t, deepSeekCompatClient, usageStream(usageEvent{"delta", `{"output_tokens":2}`}))
	explicit := readUsage(t, deepSeekCompatClient, usageStream(usageEvent{"delta", usageBody(0, 0, 0, 2)}))

	if usageReadout(absent) != usageReadout(explicit) {
		t.Fatalf("absent and explicit-zero readings differ: %s vs %s", usageReadout(absent), usageReadout(explicit))
	}
	if absent.CacheHitTokens != 0 || absent.CacheMissTokens != 0 {
		t.Fatalf("a stream that reports no counters must not report a split: %s", usageReadout(absent))
	}
}

// TestUsageNativeShapeIsUnchangedByTheRemainderRule is the non-regression guard
// the plan asks for on the native route: the remainder rule must not change a
// stream whose events all follow the route's declared convention. The expected
// reading is the one the pre-fix per-field max produced for this shape, because
// native never mixes an estimate with a split.
func TestUsageNativeShapeIsUnchangedByTheRemainderRule(t *testing.T) {
	usage := readUsage(t, nativeClient, usageSSE(1234, 56, 890, 42))

	// native sums the exclusive counters: prompt = 1234 + 56 + 890.
	if usage.PromptTokens != 2180 || usage.CacheHitTokens != 890 || usage.CacheMissTokens != 1290 {
		t.Fatalf("native usage = %s, want prompt=2180 hit=890 miss=1290", usageReadout(usage))
	}
	if usage.CacheWriteTokens != 56 {
		t.Fatalf("cache write = %d, want 56", usage.CacheWriteTokens)
	}
}

// TestUsageLongCatShapeIsUnchangedByTheRemainderRule is the same guard for the
// delta-only gateway: it has no split-free predecessor, so the route's declared
// convention stands and the reading matches the pre-fix one.
func TestUsageLongCatShapeIsUnchangedByTheRemainderRule(t *testing.T) {
	events := []usageEvent{{"start", ""}, {"delta", usageBody(1234, 56, 890, 42)}}
	usage := readUsage(t, longCatClient, usageStream(events...))

	if usage.PromptTokens != 2180 || usage.CacheHitTokens != 890 || usage.CacheMissTokens != 1290 {
		t.Fatalf("longcat usage = %s, want prompt=2180 hit=890 miss=1290", usageReadout(usage))
	}
	if usage.CacheWriteTokens != 56 {
		t.Fatalf("cache write = %d, want 56", usage.CacheWriteTokens)
	}
}

// TestUsageWarmShapeIsDecidedByTheCountersAlone answers the question this part
// asks: can the production fix be verified without the gateway's private billing
// block? On the shape the production gateway actually sends, yes.
//
// The served read (33,408) is larger than the served input_tokens (29), and a
// cache read cannot be a subset of a number smaller than itself. That
// contradiction is proof from the counters alone that input_tokens is a
// remainder. The estimate predecessor is therefore not what settles this shape,
// which the test asserts by removing it. The consequence is the evidence
// boundary: on this route the fix rests on arithmetic, not on trusting a private
// extension.
func TestUsageWarmShapeIsDecidedByTheCountersAlone(t *testing.T) {
	withEstimate := usageStream(
		usageEvent{"start", usageBody(39149, 0, 0, 0)},
		usageEvent{"start", usageBody(29, 0, 33408, 0)},
		usageEvent{"delta", usageBody(29, 0, 33408, 2)},
	)
	withoutEstimate := usageStream(
		usageEvent{"start", usageBody(29, 0, 33408, 0)},
		usageEvent{"delta", usageBody(29, 0, 33408, 2)},
	)

	a := readUsage(t, deepSeekCompatClient, withEstimate)
	b := readUsage(t, deepSeekCompatClient, withoutEstimate)

	if usageReadout(a) != usageReadout(b) {
		t.Fatalf("the estimate predecessor changed the reading:\n with    %s\n without %s", usageReadout(a), usageReadout(b))
	}
	if a.PromptTokens != 33437 || a.CacheHitTokens != 33408 || a.CacheMissTokens != 29 {
		t.Fatalf("warm reading = %s, want prompt=33437 hit=33408 miss=29", usageReadout(a))
	}
	// The row is only decisive while the read exceeds the served input_tokens:
	// that is the contradiction the counters state on their own.
	if a.CacheHitTokens <= 29 {
		t.Fatalf("this row is only decisive while the read exceeds input_tokens; got read=%d input=29", a.CacheHitTokens)
	}
}

// TestUsageEstimatePredecessorIsTheOnlySignalOnAnAmbiguousShape draws the other
// half of that boundary. On a route that declares the inclusive convention, a
// split-bearing event whose input_tokens is LARGER than its read is ambiguous:
// both conventions are self-consistent for those bytes (the read fits as a
// subset, and it also fits as a separate part). The estimate predecessor is then
// the only signal left, and it is a heuristic rather than a proof — an
// inclusive-convention gateway that sends an over-estimate first would be read
// as a remainder.
//
// The row is pinned rather than fixed. It is the shape that still needs an oracle
// or a cold/warm controlled differential before its reading can be called
// verified, and this test is where that debt is recorded.
func TestUsageEstimatePredecessorIsTheOnlySignalOnAnAmbiguousShape(t *testing.T) {
	// A route that declares the inclusive convention but is not the official
	// endpoint: the declaration is the only thing making it inclusive.
	inclusive := &client{name: "gateway", inclusiveUsage: true}
	events := []usageEvent{
		{"start", usageBody(2000, 0, 0, 0)},
		{"delta", usageBody(900, 0, 300, 2)},
	}
	usage := readUsage(t, inclusive, usageStream(events...))

	// The predecessor is larger than the served input, so the fold overrides the
	// declared convention and takes the remainder reading: prompt = 900 + 300.
	if usage.PromptTokens != 1200 || usage.CacheHitTokens != 300 || usage.CacheMissTokens != 900 {
		t.Fatalf("ambiguous reading = %s, want prompt=1200 hit=300 miss=900", usageReadout(usage))
	}
	// Without the predecessor the same split falls back to the declared
	// convention, and the two readings differ — which is exactly why this shape
	// cannot be settled from the counters.
	withoutPredecessor := readUsage(t, inclusive, usageStream(usageEvent{"delta", usageBody(900, 0, 300, 2)}))
	if usageReadout(withoutPredecessor) == usageReadout(usage) {
		t.Fatalf("the predecessor was supposed to be the only signal here, but both readings are %s", usageReadout(usage))
	}
	if withoutPredecessor.PromptTokens != 900 || withoutPredecessor.CacheMissTokens != 600 {
		t.Fatalf("declared-convention reading = %s, want prompt=900 hit=300 miss=600", usageReadout(withoutPredecessor))
	}
}

// TestUsageConventionIsIndependentOfTheReplayObligation is the guard for the
// split itself. deepseek is which reasoning block the next request must replay;
// inclusiveUsage is how this response's counters are read. Before they were
// separated, a reasoning_protocol setting could move a token reading, so the same
// bytes produced different prompts depending on a knob that has nothing to do
// with accounting.
//
// The two clients below carry the SAME replay obligation and differ only in the
// declared convention, which is the combination one flag could not express.
func TestUsageConventionIsIndependentOfTheReplayObligation(t *testing.T) {
	// The native Messages shape: input_tokens is the uncached part.
	events := []usageEvent{
		{"start", usageBody(1000, 0, 500, 0)},
		{"delta", usageBody(0, 0, 0, 2)},
	}

	exclusive := readUsage(t, deepSeekCompatClient, usageStream(events...))
	if exclusive.PromptTokens != 1500 || exclusive.CacheHitTokens != 500 || exclusive.CacheMissTokens != 1000 {
		t.Fatalf("exclusive route = %s, want prompt=1500 hit=500 miss=1000", usageReadout(exclusive))
	}

	inclusive := readUsage(t, deepSeekOfficialClient, usageStream(events...))
	if inclusive.PromptTokens != 1000 || inclusive.CacheHitTokens != 500 || inclusive.CacheMissTokens != 500 {
		t.Fatalf("inclusive route = %s, want prompt=1000 hit=500 miss=500", usageReadout(inclusive))
	}

	// Both clients carry the same replay obligation, so the difference above is
	// the convention alone — not the reasoning protocol.
	if deepSeekCompatClient.deepseek != deepSeekOfficialClient.deepseek {
		t.Fatalf("this test is only meaningful while both clients share a replay obligation")
	}
}

// TestUsageConventionFollowsTheEndpointNotTheProtocol is the regression guard for
// the defect the split fixes. A gateway that is NOT the official DeepSeek
// endpoint follows the Messages contract the native API documents, whatever
// reasoning_protocol says, because no observation has shown it covering its cache
// counters. Before, declaring `reasoning_protocol="deepseek"` on such a gateway
// silently switched its reading to the inclusive convention and understated the
// prompt.
func TestUsageConventionFollowsTheEndpointNotTheProtocol(t *testing.T) {
	const gateway = "https://aiapi.example.com"

	// The observed member-gateway shape: the split event's input is a remainder.
	warm := usageStream(
		usageEvent{"start", usageBody(39149, 0, 0, 0)},
		usageEvent{"start", usageBody(29, 0, 33408, 0)},
		usageEvent{"delta", usageBody(29, 0, 33408, 2)},
	)

	for _, protocol := range []string{"", "auto", "deepseek", "none"} {
		extra := map[string]any{"thinking": "enabled"}
		if protocol != "" {
			extra["reasoning_protocol"] = protocol
		}
		p, err := New(provider.Config{Name: "gw", BaseURL: gateway, Model: "deepseek-v4-flash", Extra: extra})
		if err != nil {
			t.Fatal(err)
		}
		usage := readUsage(t, p.(*client), warm)
		// The reading is the same under every protocol: the reasoning knob must
		// not be able to move a token count.
		if usage.PromptTokens != 33437 || usage.CacheHitTokens != 33408 || usage.CacheMissTokens != 29 {
			t.Fatalf("protocol=%q: reading = %s, want prompt=33437 hit=33408 miss=29",
				protocol, usageReadout(usage))
		}
	}
}

// TestUsageOfficialEndpointStillDeclaresTheInclusiveConvention keeps the one
// route that genuinely documents the other convention. If the split ever dropped
// it, the official endpoint's readings would double-count every cached token —
// the defect messagesUsage documents.
func TestUsageOfficialEndpointStillDeclaresTheInclusiveConvention(t *testing.T) {
	p, err := New(provider.Config{Name: "ds", BaseURL: "https://api.deepseek.com/anthropic", Model: "deepseek-v4-flash"})
	if err != nil {
		t.Fatal(err)
	}
	c := p.(*client)
	if !c.inclusiveInput() {
		t.Fatal("the official DeepSeek endpoint must keep its declared inclusive convention")
	}
	// A 39047-token request with 39040 cached reads arrives as input=39047.
	usage := readUsage(t, c, usageSSE(39047, 0, 39040, 4))
	if usage.PromptTokens != 39047 || usage.CacheHitTokens != 39040 || usage.CacheMissTokens != 7 {
		t.Fatalf("official reading = %s, want prompt=39047 hit=39040 miss=7", usageReadout(usage))
	}
}
