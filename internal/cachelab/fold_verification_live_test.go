//go:build live

package cachelab

// Oracle-free verification of the usage fold: see TestLiveUsageFoldVerification.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/provider"
	"reasonix/internal/provider/anthropic"
)

// liveCredential is the endpoint and key the probe dials.
type liveCredential struct {
	baseURL string
	apiKey  string
	bearer  bool
}

// liveCredentialFromEnv reads the probe's endpoint. It prefers the experiment's
// own variables and falls back to the gateway convention the product uses.
func liveCredentialFromEnv() liveCredential {
	creds := liveCredential{
		baseURL: strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_BASE_URL")),
		apiKey:  strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_API_KEY")),
	}
	if creds.apiKey != "" {
		return creds
	}
	return liveCredential{
		baseURL: strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL")),
		apiKey:  strings.TrimSpace(os.Getenv("ANTHROPIC_AUTH_TOKEN")),
		bearer:  true,
	}
}

// rawUsageEvent is one usage object as the endpoint wrote it, with the event
// that carried it. Only counters are kept: no response text is read or stored.
type rawUsageEvent struct {
	event        string
	input        int
	read         int
	create       int
	output       int
	hasBilling   bool
	oraclePrompt int
	oracleHit    int
}

// teeProxy forwards to the recorder unchanged and keeps the last response body,
// so the probe can re-read exactly the bytes the adapter read. It changes no
// request byte and stores nothing but the body of the request it is serving.
type teeProxy struct {
	server *httptest.Server
	mu     sync.Mutex
	body   []byte
}

func newTeeProxy(t *testing.T, upstream string) *teeProxy {
	t.Helper()
	target, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	p := &teeProxy{}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			pr.Out.URL.Path = pr.In.URL.Path
			pr.Out.Host = target.Host
		},
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(strings.NewReader(string(body)))
			resp.ContentLength = int64(len(body))
			p.mu.Lock()
			p.body = body
			p.mu.Unlock()
			return nil
		},
	}
	p.server = httptest.NewServer(proxy)
	t.Cleanup(p.server.Close)
	return p
}

func (p *teeProxy) URL() string { return p.server.URL }

func (p *teeProxy) Last() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.body...)
}

// collectRawUsageEvents decodes an event stream into the usage events it carried.
func collectRawUsageEvents(t *testing.T, body []byte) []rawUsageEvent {
	t.Helper()
	var out []rawUsageEvent
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" || payload[0] != '{' {
			continue
		}
		var doc struct {
			Type    string `json:"type"`
			Message *struct {
				Usage map[string]any `json:"usage"`
			} `json:"message"`
			Usage map[string]any `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &doc); err != nil {
			continue
		}
		usage := doc.Usage
		if usage == nil && doc.Message != nil {
			usage = doc.Message.Usage
		}
		if usage == nil {
			continue
		}
		ev := rawUsageEvent{
			event:  doc.Type,
			input:  usageInt(usage["input_tokens"]),
			read:   usageInt(usage["cache_read_input_tokens"]),
			create: usageInt(usage["cache_creation_input_tokens"]),
			output: usageInt(usage["output_tokens"]),
		}
		if billing, ok := usage["billing_usage"].(map[string]any); ok {
			ev.hasBilling = true
			if oracle, ok := billing["openai_usage"].(map[string]any); ok {
				ev.oraclePrompt = usageInt(oracle["prompt_tokens"])
				if details, ok := oracle["prompt_tokens_details"].(map[string]any); ok {
					ev.oracleHit = usageInt(details["cached_tokens"])
				}
			}
		}
		out = append(out, ev)
	}
	return out
}

func usageInt(value any) int {
	if f, ok := value.(float64); ok {
		return int(f)
	}
	return 0
}

// foldPerFieldMax reproduces the rule the fold replaced: the per-field maximum
// across every usage event, read through the route's inclusive convention.
func foldPerFieldMax(events []rawUsageEvent) (prompt, hit, miss int) {
	in := 0
	for _, ev := range events {
		in = max(in, ev.input)
		hit = max(hit, ev.read)
	}
	miss = max(in-hit, 0)
	return hit + miss, hit, miss
}

// foldServedSplit is the reading the shipped fold produces: the counters come
// from the last event that carried a cache split, with output keeping the
// maximum because that field alone is genuinely cumulative.
func foldServedSplit(events []rawUsageEvent) (prompt, hit, miss, completion int) {
	for _, ev := range events {
		completion = max(completion, ev.output)
		if ev.read == 0 && ev.create == 0 {
			continue
		}
		hit, miss = ev.read, ev.input+ev.create
	}
	return hit + miss, hit, miss, completion
}

// liveProbeRequest builds one frozen request whose bytes are identical on every
// turn, so a warm turn differs from the cold one in nothing but the provider's
// cache state. The nonce makes the first turn a genuine cold start.
func liveProbeRequest(nonce string, items int) provider.Request {
	var system strings.Builder
	fmt.Fprintf(&system, "You are a terse assistant under a cache-observation probe. run-nonce: %s\n", nonce)
	for range 80 {
		system.WriteString("stable prefix clause that never changes between turns.\n")
	}
	var user strings.Builder
	for i := range items {
		fmt.Fprintf(&user, "item %06d: a stable clause that never changes between turns.\n", i)
	}
	user.WriteString("\nReply with exactly the word CACHELAB-ACK and nothing else.\n")
	return provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: system.String()},
			{Role: provider.RoleUser, Content: user.String()},
		},
		MaxTokens: 32,
	}
}

// liveProbeTurn streams one turn and returns the adapter's own usage reading.
func liveProbeTurn(ctx context.Context, client provider.Provider, req provider.Request) (*provider.Usage, error) {
	stream, err := client.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	var usage *provider.Usage
	for chunk := range stream {
		switch chunk.Type {
		case provider.ChunkError:
			if chunk.Err != nil {
				return usage, chunk.Err
			}
		case provider.ChunkUsage:
			usage = chunk.Usage
		}
	}
	return usage, nil
}

// TestLiveUsageFoldVerification checks the shipped fold against the protocol's
// own counters and a cold/warm differential over byte-identical requests, so it
// does not depend on the gateway's private billing sidecar. The rule the fold
// replaced is recomputed from the same events, so the size of the defect is
// reported rather than asserted, and no hit rate is asserted: that is the
// provider's behaviour.
//
//	REASONIX_LIVE_CACHE_BASE_URL=... REASONIX_LIVE_CACHE_API_KEY=... \
//	  go test -tags live ./internal/cachelab/ -run TestLiveUsageFoldVerification -v -count=1
func TestLiveUsageFoldVerification(t *testing.T) {
	creds := liveCredentialFromEnv()
	if creds.baseURL == "" || creds.apiKey == "" {
		t.Skip("set REASONIX_LIVE_CACHE_BASE_URL/REASONIX_LIVE_CACHE_API_KEY (or ANTHROPIC_BASE_URL/ANTHROPIC_AUTH_TOKEN) to verify the fold against the real endpoint")
	}
	model := strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_MODEL"))
	if model == "" {
		model = "deepseek/deepseek-v4.1-flash"
	}
	journal, err := OpenJournal(os.TempDir() + "/cachelab-fold-verification.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	recorder, err := NewRecorder(creds.baseURL, journal)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	// The client dials the tee, the tee dials the recorder, the recorder dials the
	// endpoint: the request bytes are the client's own at every hop, and the tee
	// keeps the response the adapter parsed so the raw events can be re-read.
	tee := newTeeProxy(t, recorder.URL())

	client, err := anthropic.New(provider.Config{
		Name: "fold-verification", BaseURL: tee.URL(), Model: model, APIKey: creds.apiKey,
		Extra: map[string]any{"thinking": "disabled", "effort": "disabled", "auth_header": creds.bearer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := client.(interface{ CloseIdleConnections() }); ok {
		t.Cleanup(closer.CloseIdleConnections)
	}

	nonce := fmt.Sprintf("fold-verification-%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	type turnResult struct {
		turn    int
		adapter *provider.Usage
		events  []rawUsageEvent
		sample  Sample
	}
	const turnsToRun = 5
	results := make([]turnResult, 0, turnsToRun)
	for turn := 1; turn <= turnsToRun; turn++ {
		recorder.Begin(TurnContext{
			Arm: "B1-baseline-repeat", RunID: nonce, TeamID: "fold-verification", MemberID: "probe",
			ModelRef: model, TurnSeq: turn,
		})
		usage, _ := liveProbeTurn(ctx, client, liveProbeRequest(nonce, 60))
		samples := recorder.WaitForSamples(turn, 30*time.Second)
		if len(samples) < turn {
			t.Fatalf("turn %d: the recorder holds %d samples, want %d", turn, len(samples), turn)
		}
		results = append(results, turnResult{
			turn: turn, adapter: usage, events: collectRawUsageEvents(t, tee.Last()), sample: samples[turn-1],
		})
		time.Sleep(150 * time.Millisecond)
	}

	if results[0].adapter == nil {
		t.Fatal("the cold turn produced no adapter reading")
	}
	coldPrompt := results[0].adapter.PromptTokens
	t.Logf("cold turn: adapter prompt=%d hit=%d | events: %s",
		results[0].adapter.PromptTokens, results[0].adapter.CacheHitTokens, describeRawEvents(results[0].events))

	violations, withoutOracle := 0, 0
	for _, tr := range results[1:] {
		if tr.adapter == nil {
			t.Errorf("turn %d: the adapter reported no usage", tr.turn)
			violations++
			continue
		}
		a := tr.adapter
		servedPrompt, servedHit, servedMiss, _ := foldServedSplit(tr.events)
		maxPrompt, maxHit, maxMiss := foldPerFieldMax(tr.events)
		closed := a.PromptTokens == a.CacheHitTokens+a.CacheMissTokens
		coldClosure := a.CacheHitTokens+a.CacheMissTokens == coldPrompt
		aligned := a.CacheHitTokens%128 == 0
		bounded := a.PromptTokens <= coldPrompt
		matchesServed := a.PromptTokens == servedPrompt && a.CacheHitTokens == servedHit && a.CacheMissTokens == servedMiss
		oracle, carriedOracle := "none", false
		for _, ev := range tr.events {
			if ev.hasBilling {
				carriedOracle = true
				oracle = fmt.Sprintf("prompt=%d hit=%d", ev.oraclePrompt, ev.oracleHit)
			}
		}
		if !carriedOracle {
			withoutOracle++
		}
		t.Logf("turn %d: adapter(prompt=%d hit=%d miss=%d) served(prompt=%d hit=%d miss=%d) per_field_max(prompt=%d hit=%d miss=%d) cold=%d | closed=%v cold_closure=%v aligned=%v bounded=%v adapter==served=%v | oracle(%s)",
			tr.turn, a.PromptTokens, a.CacheHitTokens, a.CacheMissTokens,
			servedPrompt, servedHit, servedMiss, maxPrompt, maxHit, maxMiss, coldPrompt,
			closed, coldClosure, aligned, bounded, matchesServed, oracle)
		if !closed || !coldClosure || !aligned || !bounded || !matchesServed {
			violations++
		}
		if maxMiss != servedMiss {
			t.Logf("    the replaced rule would have reported miss=%d here, %d tokens more than the served %d",
				maxMiss, maxMiss-servedMiss, servedMiss)
		}
	}
	t.Logf("fold verification on %s via %s: %d warm turns (%d carried no billing block), %d invariant violations",
		model, recorder.UpstreamHost(), len(results)-1, withoutOracle, violations)
	t.Logf("journal (digests only): %s", journal.Path())
	if violations != 0 {
		t.Errorf("the fold failed %d oracle-free invariants", violations)
	}
}

// describeRawEvents renders the event shapes of one response, counters only.
func describeRawEvents(events []rawUsageEvent) string {
	parts := make([]string, 0, len(events))
	for _, ev := range events {
		parts = append(parts, fmt.Sprintf("%s(input=%d read=%d create=%d out=%d billing=%v)",
			ev.event, ev.input, ev.read, ev.create, ev.output, ev.hasBilling))
	}
	return strings.Join(parts, " -> ")
}
