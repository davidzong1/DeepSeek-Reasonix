package cachelab

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// anthropicSSE is a minimal streamed response in the Anthropic vocabulary: input
// on the first event, output on the last, and a cache read that is not repeated
// later (so last-wins cannot accidentally supply it).
func anthropicSSE(marker string, input, read, write, output int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":%d,\"cache_read_input_tokens\":%d,\"cache_creation_input_tokens\":%d}}}\n\n", input, read, write)
	fmt.Fprintf(&b, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"%s\"}}\n\n", marker)
	fmt.Fprintf(&b, "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":%d}}\n\n", output)
	return b.String()
}

// upstream is a scripted provider stand-in: it records what it received and
// answers with the queued responses in order, so a test can drive success,
// failure and retry paths offline.
type upstream struct {
	mu        sync.Mutex
	server    *httptest.Server
	bodies    [][]byte
	headers   []http.Header
	responses []upstreamResponse
}

type upstreamResponse struct {
	status int
	body   string
}

func newUpstream(t *testing.T, responses ...upstreamResponse) *upstream {
	t.Helper()
	u := &upstream{responses: responses}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		u.mu.Lock()
		u.bodies = append(u.bodies, body)
		u.headers = append(u.headers, req.Header.Clone())
		var reply upstreamResponse
		if len(u.responses) > 0 {
			reply = u.responses[0]
			u.responses = u.responses[1:]
		} else {
			reply = upstreamResponse{status: 200, body: anthropicSSE("CACHELAB-ACK", 10, 90, 0, 1)}
		}
		u.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(u.server.Close)
	return u
}

func (u *upstream) received() [][]byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([][]byte(nil), u.bodies...)
}

// post drives one request through the recorder.
func post(t *testing.T, recorderURL string, body []byte) *http.Response {
	t.Helper()
	resp, err := http.Post(recorderURL+"/v1/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp
}

func TestRecorderRecordsExactRequestAndRawUsage(t *testing.T) {
	up := newUpstream(t)
	journal, err := OpenJournal(t.TempDir() + "/journal.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	rec, err := NewRecorder(up.server.URL, journal)
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()

	body := []byte(`{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`)
	rec.Begin(TurnContext{Arm: "B1-baseline-repeat", MemberID: "m1", TurnSeq: 1, Expect: "CACHELAB-ACK"})
	if resp := post(t, rec.URL(), body); resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	received := up.received()
	if len(received) != 1 || !bytes.Equal(received[0], body) {
		t.Fatalf("upstream received %q, want the client's exact bytes", received)
	}
	samples := rec.WaitForSamples(1, time.Second)
	if len(samples) != 1 {
		t.Fatalf("samples = %d, want 1", len(samples))
	}
	s := samples[0]
	if s.RequestHash != RequestDigest(body) || s.RequestBytes != len(body) {
		t.Fatalf("request identity = %s/%d, want the body's digest and length", s.RequestHash, s.RequestBytes)
	}
	if !s.UsageReported || !s.UsageSplit || s.UsageShape != UsageShapeAnthropic {
		t.Fatalf("usage = %+v, want a resolved anthropic split", s)
	}
	// input_tokens is the uncached prompt: miss = input + creation, prompt = hit+miss.
	if s.CacheHitTokens != 90 || s.CacheMissTokens != 10 || s.PromptTokens != 100 {
		t.Fatalf("split = hit %d miss %d prompt %d, want 90/10/100", s.CacheHitTokens, s.CacheMissTokens, s.PromptTokens)
	}
	if s.CompletionTokens != 1 {
		t.Fatalf("completion = %d, want the last event's output_tokens", s.CompletionTokens)
	}
	if s.QualityCheck != QualityPass {
		t.Fatalf("quality = %s, want the marker found", s.QualityCheck)
	}
	if s.Classify() != ClassFirstRequest {
		t.Fatalf("class = %s, want %s", s.Classify(), ClassFirstRequest)
	}

	// The journal must hold the record and none of the request body.
	raw, err := os.ReadFile(journal.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(s.RequestHash)) {
		t.Fatal("journal does not carry the recorded sample")
	}
	if bytes.Contains(raw, []byte("deepseek-v4.1-flash")) {
		t.Fatalf("journal leaked request body content: %s", raw)
	}
}

func TestRecorderSeparatesRetryFromRepeat(t *testing.T) {
	up := newUpstream(t,
		upstreamResponse{status: 500, body: anthropicSSE("", 0, 0, 0, 0)},
		upstreamResponse{status: 200, body: anthropicSSE("CACHELAB-ACK", 0, 100, 0, 1)},
		upstreamResponse{status: 200, body: anthropicSSE("CACHELAB-ACK", 0, 100, 0, 1)},
	)
	rec, err := NewRecorder(up.server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()

	body := []byte(`{"messages":[{"role":"user","content":"frozen"}]}`)
	for range 3 {
		rec.Begin(TurnContext{Arm: "B1-baseline-repeat", MemberID: "m1", TurnSeq: 2, Expect: "CACHELAB-ACK"})
		post(t, rec.URL(), body)
		time.Sleep(2 * time.Millisecond)
	}
	samples := rec.WaitForSamples(3, time.Second)
	if len(samples) != 3 {
		t.Fatalf("samples = %d, want 3", len(samples))
	}
	if samples[0].Classify() != ClassError {
		t.Fatalf("first class = %s, want %s", samples[0].Classify(), ClassError)
	}
	if samples[1].Classify() != ClassRetry || samples[1].Attempt != 2 {
		t.Fatalf("second = %s attempt %d, want a retry of attempt 2", samples[1].Classify(), samples[1].Attempt)
	}
	if samples[2].Classify() != ClassRepeat || samples[2].Attempt != 1 {
		t.Fatalf("third = %s attempt %d, want a repeat of attempt 1", samples[2].Classify(), samples[2].Attempt)
	}
	if !samples[2].Eligible() {
		t.Fatal("a byte-identical re-send after a success is a warm-eligible sample")
	}
	if samples[2].Repeat != 3 {
		t.Fatalf("repeat = %d, want the third occurrence of these bytes", samples[2].Repeat)
	}
	if samples[0].GapMS != 0 || samples[1].GapMS <= 0 {
		t.Fatalf("gaps = %d,%d, want no gap on the first request and a positive one after", samples[0].GapMS, samples[1].GapMS)
	}
}

func TestRecorderRecordsUsageGaps(t *testing.T) {
	cases := map[string]struct {
		body      string
		want      string
		wantClass Class
	}{
		"no usage at all":       {body: `{"type":"message_stop"}`, want: UsageProblemNoUsage, wantClass: ClassUsageMissing},
		"unknown vocabulary":    {body: `{"usage":{"total_token_count":42}}`, want: UsageProblemNoUsage, wantClass: ClassUsageMissing},
		"prompt but no cache":   {body: `{"usage":{"input_tokens":42,"output_tokens":2}}`, want: UsageProblemNoCacheRead, wantClass: ClassNoCacheSplit},
		"mixed vocabularies":    {body: `{"usage":{"input_tokens":42,"prompt_tokens":42}}`, want: UsageProblemUnresolved, wantClass: ClassNoCacheSplit},
		"negative openai split": {body: `{"usage":{"prompt_tokens":10,"cached_tokens":50}}`, want: UsageProblemNegativeSplit, wantClass: ClassNoCacheSplit},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			up := newUpstream(t, upstreamResponse{status: 200, body: tc.body})
			rec, err := NewRecorder(up.server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer rec.Close()
			rec.Begin(TurnContext{Arm: "B0-pilot", TurnSeq: 1})
			post(t, rec.URL(), []byte(`{"messages":[]}`))
			samples := rec.WaitForSamples(1, time.Second)
			if len(samples) != 1 {
				t.Fatalf("samples = %d, want 1", len(samples))
			}
			s := samples[0]
			if s.UsageSplit || s.UsageProblem != tc.want {
				t.Fatalf("split/problem = %v/%q, want no split and %q (%+v)", s.UsageSplit, s.UsageProblem, tc.want, s)
			}
			if s.Eligible() {
				t.Fatal("a response without a resolved split must not enter a baseline")
			}
			if s.Classify() != tc.wantClass {
				t.Fatalf("class = %s, want %s", s.Classify(), tc.wantClass)
			}
			// The raw keys are the audit trail for the verdict above.
			if tc.want == UsageProblemNoCacheRead && len(s.UsageKeys) == 0 {
				t.Fatal("a recognised vocabulary must record its keys")
			}
			if tc.want == UsageProblemNoUsage && len(s.UsageKeys) != 0 {
				t.Fatalf("keys = %v, want none recorded for an unrecognised response", s.UsageKeys)
			}
		})
	}
}

func TestRecorderRecordsTransportFailure(t *testing.T) {
	up := newUpstream(t)
	target := up.server.URL
	up.server.Close() // a closed listener produces a dial failure, not a status
	rec, err := NewRecorder(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()
	rec.Begin(TurnContext{Arm: "B0-pilot", TurnSeq: 1})
	if resp := post(t, rec.URL(), []byte(`{"messages":[]}`)); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	samples := rec.WaitForSamples(1, time.Second)
	if len(samples) != 1 {
		t.Fatalf("samples = %d, want 1", len(samples))
	}
	if samples[0].Error == "" || samples[0].Classify() != ClassError {
		t.Fatalf("sample = %+v, want a recorded transport error", samples[0])
	}
}

func TestRecorderChecksTheMarkerAcrossStreamedDeltas(t *testing.T) {
	// The same answer split over two events: the marker never appears
	// contiguously in the response body, but it is what the model said.
	body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":90}}}\n\n" +
		"event: content_block_delta\ndata: {\"delta\":{\"text\":\"CACHE\"}}\n\n" +
		"event: content_block_delta\ndata: {\"delta\":{\"text\":\"LAB-ACK\"}}\n\n"
	up := newUpstream(t, upstreamResponse{status: 200, body: body})
	rec, err := NewRecorder(up.server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()
	rec.Begin(TurnContext{Arm: "B1-baseline-repeat", TurnSeq: 2, Expect: "CACHELAB-ACK"})
	post(t, rec.URL(), []byte(`{"messages":[]}`))
	s := rec.WaitForSamples(1, time.Second)[0]
	if s.QualityCheck != QualityPass {
		t.Fatalf("quality = %s, want the marker found across deltas", s.QualityCheck)
	}
}

func TestRecorderKeepsTheUpstreamBasePath(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		paths = append(paths, req.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()
	rec, err := NewRecorder(up.URL+"/anthropic", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()
	post(t, rec.URL(), []byte(`{"messages":[]}`))
	rec.WaitForSamples(1, time.Second)
	// A client pointed at a versioned upstream keeps that version: the same
	// request path it would send the provider directly is not doubled.
	versioned, err := NewRecorder(up.URL+"/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer versioned.Close()
	post(t, versioned.URL(), []byte(`{"messages":[]}`))
	versioned.WaitForSamples(1, time.Second)
	mu.Lock()
	defer mu.Unlock()
	want := []string{"/anthropic/v1/messages", "/v1/messages"}
	if len(paths) != len(want) || paths[0] != want[0] || paths[1] != want[1] {
		t.Fatalf("upstream paths = %v, want %v", paths, want)
	}
}

func TestRecorderStampsConfoundsAndModelRef(t *testing.T) {
	up := newUpstream(t)
	rec, err := NewRecorder(up.server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()
	rec.Begin(TurnContext{
		Arm: "B3-tool-schema", MemberID: "m2", ModelRef: "gw/deepseek-v4.1-flash", TurnSeq: 3,
		Confounds: []string{ConfoundEndpointProtocol, ConfoundEndpointProtocol},
	})
	post(t, rec.URL(), []byte(`{"messages":[]}`))
	s := rec.WaitForSamples(1, time.Second)[0]
	if s.ModelRef != "gw/deepseek-v4.1-flash" || s.TurnSeq != 3 {
		t.Fatalf("sample = %+v, want the bound member, model and turn", s)
	}
	if len(s.Confounds) != 1 || s.Confounds[0] != ConfoundEndpointProtocol {
		t.Fatalf("confounds = %v, want the single distinct reason", s.Confounds)
	}
	if s.UpstreamHost == "" || strings.Contains(s.UpstreamHost, "://") {
		t.Fatalf("upstream = %q, want a bare host", s.UpstreamHost)
	}
	if encoded, err := json.Marshal(s); err != nil || len(encoded) == 0 {
		t.Fatalf("sample must be encodable: %v", err)
	}
}
