package openai

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

func newClient(t *testing.T, baseURL, effort string) *client {
	t.Helper()
	extra := map[string]any{}
	if effort != "" {
		extra["effort"] = effort
	}
	p, err := New(provider.Config{Name: "p", BaseURL: baseURL, Model: "m", APIKey: "k", Extra: extra})
	if err != nil {
		t.Fatalf("New(%q, effort=%q): %v", baseURL, effort, err)
	}
	return p.(*client)
}

func TestEffortNormalization(t *testing.T) {
	const mimo = "https://api.xiaomimimo.com/v1"
	const deepseek = "https://api.deepseek.com/v1"

	tests := []struct {
		base, effort, want string
	}{
		{mimo, "high", "high"},
		{mimo, "medium", "medium"},
		{mimo, "low", "low"},
		{mimo, "auto", ""}, // UI/config auto means omit provider-specific effort
		{mimo, "", ""},     // unset stays omitted
		{deepseek, "max", "max"},
		{deepseek, "high", "high"},
		{deepseek, "auto", "high"},
		{deepseek, "", "high"}, // DeepSeek default depth
	}
	for _, tc := range tests {
		if got := newClient(t, tc.base, tc.effort).effort; got != tc.want {
			t.Errorf("base=%s effort=%q: got %q, want %q", tc.base, tc.effort, got, tc.want)
		}
	}

	// MiMo 400s on "max" and its capability stops at high, so the value is
	// refused outright — never rewritten to the neighbouring ceiling. Case
	// folding does not soften the verdict.
	for _, effort := range []string{"max", "MAX", "Max"} {
		_, err := New(provider.Config{Name: "p", BaseURL: mimo, Model: "m", APIKey: "k", Extra: map[string]any{"effort": effort}})
		if err == nil || !strings.Contains(err.Error(), "UNSUPPORTED_REASONING_EFFORT") {
			t.Errorf("effort %q: New error = %v, want a refusal", effort, err)
		}
	}
}

func TestExplicitSupportedEffortsPreservesKimiK3Max(t *testing.T) {
	p, err := New(provider.Config{
		Name:    "opencode-go",
		BaseURL: "https://opencode.ai/zen/go/v1",
		Model:   "kimi-k3",
		APIKey:  "k",
		Extra: map[string]any{
			"effort":            "max",
			"supported_efforts": []string{"high", "max"},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := p.(*client).buildRequest(provider.Request{}).ReasoningEffort; got != "max" {
		t.Fatalf("reasoning_effort = %q, want explicitly supported max", got)
	}
}

func TestExplicitSupportedEffortsPreserveCustomGenericEffort(t *testing.T) {
	const effort = "ultra"
	p, err := New(provider.Config{
		Name:    "custom-openai",
		BaseURL: "https://gateway.example.com/v1",
		Model:   "custom-reasoning-model",
		APIKey:  "k",
		Extra: map[string]any{
			"effort":            effort,
			"supported_efforts": []string{"high", effort},
		},
	})
	if err != nil {
		t.Fatalf("New custom effort: %v", err)
	}
	req := p.(*client).buildRequest(provider.Request{})
	if req.ReasoningEffort != effort {
		t.Fatalf("reasoning_effort = %q, want %q", req.ReasoningEffort, effort)
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if !strings.Contains(string(body), `"reasoning_effort":"ultra"`) {
		t.Fatalf("custom effort was not preserved on the wire: %s", body)
	}
}

func TestExplicitSupportedEffortsPreserveCustomDeepSeekEfforts(t *testing.T) {
	for _, effort := range []string{"medium", "none"} {
		t.Run(effort, func(t *testing.T) {
			p, err := New(provider.Config{
				Name:    "custom-deepseek",
				BaseURL: "https://gateway.example.com/v1",
				Model:   "deepseek-v4-flash",
				APIKey:  "k",
				Extra: map[string]any{
					"effort":             effort,
					"reasoning_protocol": "deepseek",
					"supported_efforts":  []string{"low", "medium", "high", "none"},
				},
			})
			if err != nil {
				t.Fatalf("New custom DeepSeek effort: %v", err)
			}
			req := p.(*client).buildRequest(provider.Request{})
			if req.ReasoningEffort != effort {
				t.Fatalf("reasoning_effort = %q, want %q", req.ReasoningEffort, effort)
			}
			if req.Thinking == nil || req.Thinking.Type != "enabled" {
				t.Fatalf("thinking = %#v, want enabled", req.Thinking)
			}
			body, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			if !strings.Contains(string(body), `"reasoning_effort":"`+effort+`"`) {
				t.Fatalf("custom DeepSeek effort was not preserved on the wire: %s", body)
			}
		})
	}
}

func TestExplicitSupportedEffortsRejectUndeclaredEffort(t *testing.T) {
	tests := []struct {
		name  string
		extra map[string]any
	}{
		{
			name: "generic",
			extra: map[string]any{
				"effort":            "ultra",
				"supported_efforts": []string{"high"},
			},
		},
		{
			name: "generic-max",
			extra: map[string]any{
				"effort":            "max",
				"supported_efforts": []string{"high"},
			},
		},
		{
			name: "deepseek",
			extra: map[string]any{
				"effort":             "max",
				"reasoning_protocol": "deepseek",
				"supported_efforts":  []string{"medium", "none"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(provider.Config{
				Name:    "custom-" + tc.name,
				BaseURL: "https://gateway.example.com/v1",
				Model:   "custom-reasoning-model",
				APIKey:  "k",
				Extra:   tc.extra,
			})
			if err == nil || !strings.Contains(err.Error(), "UNSUPPORTED_REASONING_EFFORT") {
				t.Fatalf("New error = %v, want supported_efforts rejection", err)
			}
		})
	}
}

func TestImplicitOnlySupportedEffortsKeepBuiltInValidation(t *testing.T) {
	_, err := New(provider.Config{
		Name:    "generic",
		BaseURL: "https://gateway.example.com/v1",
		Model:   "reasoning-model",
		APIKey:  "k",
		Extra: map[string]any{
			"effort":            "max",
			"supported_efforts": []string{"", " auto ", " "},
		},
	})
	if err == nil {
		t.Fatal("an empty declaration must not enable or clamp max")
	}
}

func TestEffortInvalidRejected(t *testing.T) {
	_, err := New(provider.Config{
		Name: "p", BaseURL: "https://api.xiaomimimo.com/v1", Model: "m", APIKey: "k",
		Extra: map[string]any{"effort": "turbo"},
	})
	if err == nil || !strings.Contains(err.Error(), "UNSUPPORTED_REASONING_EFFORT") {
		t.Fatalf("expected a low/medium/high validation error, got: %v", err)
	}
}

// Official V4 Flash and Pro are the endpoints that accept max without any
// declaration, so the level must reach the wire unchanged from both the
// configured depth and a per-request override.
func TestOfficialDeepSeekV4MaxReachesTheWire(t *testing.T) {
	for _, model := range []string{"deepseek-v4-flash", "deepseek-v4-pro"} {
		p, err := New(provider.Config{
			Name: "official", BaseURL: "https://api.deepseek.com", Model: model, APIKey: "k",
			Extra: map[string]any{"effort": "max"},
		})
		if err != nil {
			t.Fatalf("%s New: %v", model, err)
		}
		c := p.(*client)
		if got := c.buildRequest(provider.Request{}).ReasoningEffort; got != "max" {
			t.Errorf("%s configured max reached the wire as %q", model, got)
		}
		if got := c.buildRequest(provider.Request{EffortOverride: "max"}).ReasoningEffort; got != "max" {
			t.Errorf("%s override max reached the wire as %q", model, got)
		}
	}
}

// Stream re-checks a per-request override against the capability frozen at
// construction, so an undeclared level is refused by identity and never leaves
// the process. Both fixtures point at the test server, so a regression shows up
// as a counted request rather than as traffic to a real endpoint.
func TestRequestEffortOverrideRefusedBeforeIO(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"contract fixture"}}`))
	}))
	defer server.Close()

	tests := []struct {
		name     string
		extra    map[string]any
		refused  []string
		accepted []string
	}{
		{
			name:    "generic endpoint declares nothing",
			refused: []string{"max", "high", "low", "turbo"},
		},
		{
			name:     "deepseek protocol without a declaration",
			extra:    map[string]any{"reasoning_protocol": "deepseek"},
			refused:  []string{"medium", "xhigh", "turbo"},
			accepted: []string{"max", "high", "disabled"},
		},
	}
	want := 0
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			extra := map[string]any{}
			maps.Copy(extra, tc.extra)
			p, err := New(provider.Config{
				Name: "p", BaseURL: server.URL + "/v1", Model: "deepseek-v4-flash", APIKey: "k", Extra: extra,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			for _, level := range tc.refused {
				_, err := p.Stream(context.Background(), provider.Request{EffortOverride: level})
				if err == nil || !strings.Contains(err.Error(), "UNSUPPORTED_REASONING_EFFORT") {
					t.Fatalf("override %q: Stream error = %v, want a refusal", level, err)
				}
			}
			for _, level := range tc.accepted {
				_, err := p.Stream(context.Background(), provider.Request{EffortOverride: level})
				if err != nil && strings.Contains(err.Error(), "UNSUPPORTED_REASONING_EFFORT") {
					t.Fatalf("override %q was refused: %v", level, err)
				}
			}
			want += len(tc.accepted)
		})
	}
	if calls != want {
		t.Fatalf("requests reaching the endpoint = %d, want %d", calls, want)
	}
}

func TestReasoningProtocolOverridesEndpointHeuristic(t *testing.T) {
	p, err := New(provider.Config{
		Name:    "deepseek-proxy",
		BaseURL: "https://proxy.example.com/v1",
		Model:   "deepseek-v4-flash",
		APIKey:  "k",
		Extra:   map[string]any{"reasoning_protocol": "deepseek"},
	})
	if err != nil {
		t.Fatalf("New deepseek protocol: %v", err)
	}
	c := p.(*client)
	if !c.deepseek || c.effort != "high" {
		t.Fatalf("deepseek=%v effort=%q, want true/high", c.deepseek, c.effort)
	}

	p, err = New(provider.Config{
		Name:    "deepseek-direct",
		BaseURL: "https://api.deepseek.com/v1",
		Model:   "deepseek-v4-flash",
		APIKey:  "k",
		Extra: map[string]any{
			"reasoning_protocol": "none",
			"effort":             "max",
			"supported_efforts":  []string{"low"},
		},
	})
	if err != nil {
		t.Fatalf("New none protocol: %v", err)
	}
	c = p.(*client)
	if c.deepseek || c.effort != "" {
		t.Fatalf("deepseek=%v effort=%q, want false/empty", c.deepseek, c.effort)
	}
}

func TestLongCatThinkingUsesThinkingField(t *testing.T) {
	p, err := New(provider.Config{
		Name:    "longcat",
		BaseURL: "https://api.longcat.chat/openai/v1",
		Model:   "LongCat-2.0",
		APIKey:  "k",
		Extra:   map[string]any{"effort": "disabled", "thinking": "enabled"},
	})
	if err != nil {
		t.Fatalf("New longcat: %v", err)
	}
	c := p.(*client)
	if !c.longcat || c.effort != "disabled" {
		t.Fatalf("longcat=%v effort=%q, want true/disabled", c.longcat, c.effort)
	}
	req := c.buildRequest(provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	thinking, _ := body["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Fatalf("thinking = %#v, want disabled", body["thinking"])
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Fatalf("LongCat request must omit reasoning_effort: %s", b)
	}
}
