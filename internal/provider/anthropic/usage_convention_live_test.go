//go:build live

package anthropic

// Read-only usage verification against a real Anthropic-compatible endpoint. See
// TestLiveUsageConvention for the method and the credentials it accepts.

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
)

// liveConventionCredential is the endpoint and key the probe dials. It prefers
// the experiment's own variables and falls back to the gateway convention the
// product itself uses.
type liveConventionCredential struct {
	baseURL string
	apiKey  string
	bearer  bool
}

func liveConventionCredentialFromEnv() liveConventionCredential {
	creds := liveConventionCredential{
		baseURL: strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_BASE_URL")),
		apiKey:  strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_API_KEY")),
	}
	if creds.apiKey != "" {
		return creds
	}
	return liveConventionCredential{
		baseURL: strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL")),
		apiKey:  strings.TrimSpace(os.Getenv("ANTHROPIC_AUTH_TOKEN")),
		bearer:  true,
	}
}

// conventionTee forwards every request byte unchanged and keeps the last
// response body, so the probe can re-read exactly what the adapter parsed.
type conventionTee struct {
	server *httptest.Server
	mu     sync.Mutex
	body   []byte
}

func newConventionTee(t *testing.T, upstream string) *conventionTee {
	t.Helper()
	target, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	tee := &conventionTee{}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
		},
		ModifyResponse: func(resp *http.Response) error {
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				return err
			}
			resp.Body = io.NopCloser(strings.NewReader(string(body)))
			tee.mu.Lock()
			tee.body = body
			tee.mu.Unlock()
			return nil
		},
	}
	tee.server = httptest.NewServer(proxy)
	t.Cleanup(tee.server.Close)
	return tee
}

func (t *conventionTee) last() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.body...)
}

// conventionUsageEvent is one usage object as the endpoint wrote it, with the
// event that carried it and whether the gateway's private billing block rode
// along. Counters and a boolean only — no response text.
type conventionUsageEvent struct {
	event      string
	input      int
	read       int
	create     int
	output     int
	hasBilling bool
}

// parseConventionEvents reads the usage objects out of a raw SSE body, keeping
// the event name that carried each one. It decodes the JSON but keeps only the
// four counters and the presence of billing_usage.
func parseConventionEvents(body []byte) []conventionUsageEvent {
	var out []conventionUsageEvent
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if payload == "" || payload[0] != '{' {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Message *struct {
				Usage json.RawMessage `json:"usage"`
			} `json:"message"`
			Usage json.RawMessage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		raw := ev.Usage
		if len(raw) == 0 && ev.Message != nil {
			raw = ev.Message.Usage
		}
		if len(raw) == 0 {
			continue
		}
		var counters map[string]json.RawMessage
		if err := json.Unmarshal(raw, &counters); err != nil {
			continue
		}
		read := func(key string) int {
			value, ok := counters[key]
			if !ok {
				return 0
			}
			var n int
			if json.Unmarshal(value, &n) != nil {
				return 0
			}
			return n
		}
		out = append(out, conventionUsageEvent{
			event:      ev.Type,
			input:      read("input_tokens"),
			read:       read("cache_read_input_tokens"),
			create:     read("cache_creation_input_tokens"),
			output:     read("output_tokens"),
			hasBilling: len(counters["billing_usage"]) > 0,
		})
	}
	return out
}

// liveConventionTurn sends one request through the adapter and returns its usage
// reading together with the raw events the endpoint sent.
func liveConventionTurn(t *testing.T, ctx context.Context, p provider.Provider, prompt string, maxTokens int) *provider.Usage {
	t.Helper()
	ch, err := p.Stream(ctx, provider.Request{
		Messages:    []provider.Message{{Role: provider.RoleUser, Content: prompt}},
		Temperature: provider.TemperaturePtr(0),
		MaxTokens:   maxTokens,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var usage *provider.Usage
	for chunk := range ch {
		switch chunk.Type {
		case provider.ChunkUsage:
			usage = chunk.Usage
		case provider.ChunkError:
			t.Fatalf("stream error: %v", chunk.Err)
		}
	}
	return usage
}

// TestLiveUsageConvention reads the configured endpoint's own usage events and
// checks the reading the adapter produced against them: does the adapter's
// convention match the endpoint's, and does the reading stay inside the counters
// the endpoint actually sent? The check is oracle-free — it compares the adapter
// against the raw event stream, not against the gateway's private billing block —
// so a missing oracle does not weaken it.
//
// The probe dials the endpoint through a tee proxy, so the adapter reads the
// endpoint's own bytes while the test keeps a copy of the response body. Only
// counters are parsed out of it: no prompt, completion text, tool argument or
// credential is read or stored.
//
// Run with REASONIX_LIVE_CACHE_BASE_URL / REASONIX_LIVE_CACHE_API_KEY (or
// ANTHROPIC_BASE_URL / ANTHROPIC_AUTH_TOKEN) and -tags live. Without credentials
// the test skips, and the route stays unverified rather than passing by default.
func TestLiveUsageConvention(t *testing.T) {
	creds := liveConventionCredentialFromEnv()
	if creds.baseURL == "" || creds.apiKey == "" {
		t.Skip("set REASONIX_LIVE_CACHE_BASE_URL/REASONIX_LIVE_CACHE_API_KEY (or ANTHROPIC_BASE_URL/ANTHROPIC_AUTH_TOKEN) to verify a real endpoint")
	}
	model := strings.TrimSpace(os.Getenv("REASONIX_LIVE_CACHE_MODEL"))
	if model == "" {
		model = "deepseek/deepseek-v4.1-flash"
	}

	tee := newConventionTee(t, creds.baseURL)
	p, err := New(provider.Config{
		Name: "live-convention", BaseURL: tee.server.URL, Model: model, APIKey: creds.apiKey,
		Extra: map[string]any{"thinking": "disabled", "effort": "disabled", "auth_header": creds.bearer},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if closer, ok := p.(interface{ CloseIdleConnections() }); ok {
		t.Cleanup(closer.CloseIdleConnections)
	}
	c := p.(*client)

	t.Logf("endpoint host: %s", hostOf(creds.baseURL))
	t.Logf("declared convention: inclusive=%v (endpoint-derived, not reasoning_protocol-derived)", c.inclusiveInput())

	nonce := fmt.Sprintf("usage-convention-%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Turn 1 establishes the prefix; turns 2+ are the warm ones the fold rule is
	// about. The filler is a repeated word so the prompt is long enough to cache
	// but carries no real content.
	filler := strings.Repeat("cache probe filler ", 400)
	const turns = 3
	oracleSeen, warmChecked := 0, 0
	for turn := 1; turn <= turns; turn++ {
		prompt := nonce + "\n\n" + filler + "\n\nReply with exactly the word ACK."
		usage := liveConventionTurn(t, ctx, p, prompt, 16)
		events := parseConventionEvents(tee.last())
		if usage == nil {
			t.Fatalf("turn %d: no usage chunk", turn)
		}
		for _, ev := range events {
			if ev.hasBilling {
				oracleSeen++
			}
		}

		closed := usage.PromptTokens == usage.CacheHitTokens+usage.CacheMissTokens
		t.Logf("turn %d: adapter prompt=%d hit=%d miss=%d write=%d | events=%d closed=%v",
			turn, usage.PromptTokens, usage.CacheHitTokens, usage.CacheMissTokens, usage.CacheWriteTokens, len(events), closed)

		if !closed {
			t.Errorf("turn %d: prompt %d != hit %d + miss %d — the reading left the counters the endpoint sent",
				turn, usage.PromptTokens, usage.CacheHitTokens, usage.CacheMissTokens)
		}
		if usage.CacheHitTokens < 0 || usage.CacheMissTokens < 0 || usage.CacheWriteTokens < 0 {
			t.Errorf("turn %d: a negative counter reached the bill: hit=%d miss=%d write=%d",
				turn, usage.CacheHitTokens, usage.CacheMissTokens, usage.CacheWriteTokens)
		}
		// The reading must be inside the counters the endpoint actually sent: a
		// hit larger than every cache read reported is a reading nothing stated.
		maxRead := 0
		for _, ev := range events {
			if ev.read > maxRead {
				maxRead = ev.read
			}
		}
		if usage.CacheHitTokens > maxRead {
			t.Errorf("turn %d: adapter reported hit=%d but the endpoint's largest cache read was %d",
				turn, usage.CacheHitTokens, maxRead)
		}

		if turn > 1 && usage.CacheHitTokens > 0 {
			warmChecked++
		}
		time.Sleep(150 * time.Millisecond)
	}

	if warmChecked == 0 {
		t.Logf("no warm turn reported a cache read; the convention check could not be exercised")
	}
	t.Logf("endpoint %s: %d turns, %d warm, %d usage events carried the gateway's private billing block",
		hostOf(creds.baseURL), turns, warmChecked, oracleSeen)
	t.Logf("this check compares the adapter against the raw event stream, so it holds with or without the private oracle")
}

// hostOf returns the endpoint's host for logging. It never returns a path,
// query or credential.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "unknown-host"
	}
	return u.Hostname()
}
