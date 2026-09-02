package boot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/provider"
)

// TestNewProviderCarriesAnthropicBetaToWire pins the P0 main-path contract: a
// configured AnthropicBeta lands on the wire request as the anthropic-beta
// header (and the ?beta=true query the gateway uses to enable the beta), and an
// empty value sends neither.
func TestNewProviderCarriesAnthropicBetaToWire(t *testing.T) {
	var gotBeta, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBeta = r.Header.Get("anthropic-beta")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	p, err := NewProvider(&config.ProviderEntry{
		Name: "gateway", Kind: "anthropic", BaseURL: srv.URL, Model: "deepseek-v4-flash",
		AnthropicBeta: "context-1m-2025-08-07",
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	chunks, err := p.Stream(context.Background(), provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range chunks {
	}
	if gotBeta != "context-1m-2025-08-07" || gotQuery != "beta=true" {
		t.Fatalf("beta header=%q query=%q, want context-1m beta wired", gotBeta, gotQuery)
	}

	// An empty field must not fire old config at a beta endpoint.
	empty, err := NewProvider(&config.ProviderEntry{
		Name: "plain", Kind: "anthropic", BaseURL: srv.URL, Model: "claude-sonnet-4.5",
	})
	if err != nil {
		t.Fatalf("NewProvider(empty): %v", err)
	}
	if _, err := empty.Stream(context.Background(), provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("Stream(empty): %v", err)
	}
	if gotBeta != "" {
		t.Fatalf("empty AnthropicBeta still sent anthropic-beta=%q", gotBeta)
	}
}
