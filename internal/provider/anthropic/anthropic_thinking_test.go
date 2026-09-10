package anthropic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

func TestBuildRequestDeepSeekThinking(t *testing.T) {
	c := &client{model: "deepseek-v4-flash", deepseek: true, thinking: "enabled", effort: "max"}
	r := c.buildRequest(context.Background(), provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "stable system"},
			{Role: provider.RoleUser, Content: "weather?"},
			{Role: provider.RoleAssistant, ReasoningContent: "I should call the tool.",
				ToolCalls: []provider.ToolCall{{ID: "t1", Name: "get_weather", Arguments: `{"city":"Paris"}`}}},
			{Role: provider.RoleTool, ToolCallID: "t1", Content: "sunny"},
		},
		Tools: []provider.ToolSchema{{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})

	if !provider.RequiresToolCallReasoning(c) || provider.RequiresReasoningRoundTrip(c) {
		t.Fatal("DeepSeek thinking must preserve tool-call reasoning without retaining ordinary-turn reasoning")
	}
	if r.Thinking == nil || r.Thinking.Type != "enabled" || r.Thinking.Display != "" {
		t.Fatalf("thinking config = %+v, want enabled without Anthropic display", r.Thinking)
	}
	if r.OutputConfig == nil || r.OutputConfig.Effort != "max" {
		t.Fatalf("output_config = %+v, want max", r.OutputConfig)
	}
	asst := r.Messages[1]
	if len(asst.Content) != 2 || asst.Content[0].Type != "thinking" || asst.Content[0].Thinking != "I should call the tool." || asst.Content[0].Signature != "" || asst.Content[1].Type != "tool_use" {
		t.Fatalf("DeepSeek assistant blocks = %+v, want unsigned thinking before tool_use", asst.Content)
	}
	if r.System[0].CacheControl != nil || r.Tools[0].CacheControl != nil {
		t.Fatal("DeepSeek ignores cache_control; system/tools must omit it")
	}
	for _, message := range r.Messages {
		for _, block := range message.Content {
			if block.CacheControl != nil {
				t.Fatalf("DeepSeek message block unexpectedly carries cache_control: %+v", block)
			}
		}
	}
}

func TestMissingToolCallReasoningWarningFingerprintTracksAnthropicConfiguration(t *testing.T) {
	first := &client{name: "deepseek", baseURL: "https://api.deepseek.com/anthropic", model: "deepseek-v4-pro", deepseek: true, thinking: "enabled", effort: "high"}
	same := &client{name: "deepseek", baseURL: "https://api.deepseek.com/anthropic", model: "deepseek-v4-pro", deepseek: true, thinking: "enabled", effort: "high"}
	flash := &client{name: "deepseek", baseURL: "https://api.deepseek.com/anthropic", model: "deepseek-v4-flash", deepseek: true, thinking: "enabled", effort: "high"}
	got := provider.MissingToolCallReasoningWarningFingerprint(first)
	if got != provider.MissingToolCallReasoningWarningFingerprint(same) {
		t.Fatal("equivalent Anthropic configurations produced different fingerprints")
	}
	if got == provider.MissingToolCallReasoningWarningFingerprint(flash) {
		t.Fatal("Anthropic model change did not re-key the warning fingerprint")
	}
}

func TestBuildRequestDeepSeekThinkingModes(t *testing.T) {
	for _, tc := range []struct {
		name, model, input, want string
	}{
		{name: "Flash low", model: "deepseek-v4-flash", input: "low", want: "low"},
		{name: "Flash legacy medium", model: "deepseek-v4-flash", input: "medium", want: "high"},
		{name: "Flash legacy xhigh", model: "deepseek-v4-flash", input: "xhigh", want: "high"},
		{name: "Pro low", model: "deepseek-v4-pro", input: "low", want: "low"},
		{name: "Pro legacy medium", model: "deepseek-v4-pro", input: "medium", want: "high"},
		{name: "Pro legacy xhigh", model: "deepseek-v4-pro", input: "xhigh", want: "high"},
		{name: "Sonnet alias uses Flash", model: "claude-sonnet-4-6", input: "low", want: "low"},
		{name: "Opus alias legacy xhigh", model: "claude-opus-4-8", input: "xhigh", want: "high"},
		{name: "unknown model falls back to Flash", model: "unknown-model", input: "xhigh", want: "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.input != tc.want {
				_, err := New(provider.Config{BaseURL: "https://api.deepseek.com/anthropic", Model: tc.model, Extra: map[string]any{"effort": tc.input}})
				if err == nil {
					t.Fatal("undeclared alias accepted")
				}
				return
			}
			r := (&client{model: tc.model, deepseek: true, effort: tc.input}).buildRequest(context.Background(), provider.Request{})
			if r.Thinking == nil || r.Thinking.Type != "enabled" || r.OutputConfig == nil || r.OutputConfig.Effort != tc.want {
				t.Fatalf("DeepSeek thinking = %+v / %+v, want enabled/%s", r.Thinking, r.OutputConfig, tc.want)
			}
		})
	}
	t.Run("provider default", func(t *testing.T) {
		r := (&client{model: "deepseek-v4-flash", deepseek: true}).buildRequest(context.Background(), provider.Request{})
		if r.Thinking == nil || r.Thinking.Type != "enabled" || r.OutputConfig != nil {
			t.Fatalf("default DeepSeek thinking = %+v / %+v, want enabled/provider-default effort", r.Thinking, r.OutputConfig)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		c := &client{model: "deepseek-v4-flash", deepseek: true, thinking: "enabled", effort: "disabled"}
		r := c.buildRequest(context.Background(), provider.Request{
			Messages: []provider.Message{{Role: provider.RoleAssistant, ReasoningContent: "replay anyway"}},
			Tools:    []provider.ToolSchema{{Name: "tool"}},
		})
		if r.Thinking == nil || r.Thinking.Type != "disabled" || r.OutputConfig != nil {
			t.Fatalf("disabled DeepSeek thinking = %+v / %+v", r.Thinking, r.OutputConfig)
		}
		if provider.RequiresToolCallReasoning(c) || provider.RequiresReasoningRoundTrip(c) {
			t.Fatal("disabled DeepSeek thinking must not retain reasoning for replay")
		}
		// Historical thinking blocks are replayed even when the current request
		// disables thinking, the same rule tool-call turns already follow.
		if len(r.Messages) != 1 || len(r.Messages[0].Content) != 1 || r.Messages[0].Content[0].Type != "thinking" ||
			r.Messages[0].Content[0].Thinking != "replay anyway" {
			t.Fatalf("reasoning-only assistant under disabled thinking = %+v, want the thinking block replayed", r.Messages)
		}
	})
}

func TestBuildRequestDeepSeekPreservesCallerTemperature(t *testing.T) {
	zero := provider.TemperaturePtr(0)
	r := (&client{model: "deepseek-v4-flash", deepseek: true}).buildRequest(context.Background(), provider.Request{Temperature: zero})
	if r.Temperature == nil || *r.Temperature != 0 {
		t.Fatalf("DeepSeek temperature = %v, want explicit zero", r.Temperature)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"temperature":0`) {
		t.Fatalf("DeepSeek request omitted explicit temperature: %s", b)
	}

	native := (&client{model: "claude-opus-4-8"}).buildRequest(context.Background(), provider.Request{Temperature: provider.TemperaturePtr(0.5)})
	if native.Temperature != nil {
		t.Fatalf("native Anthropic temperature = %v, want omitted", native.Temperature)
	}
	b, err = json.Marshal(native)
	if err != nil {
		t.Fatalf("marshal native: %v", err)
	}
	if strings.Contains(string(b), `"temperature"`) {
		t.Fatalf("native Anthropic request must omit temperature: %s", b)
	}
}

// TestBuildRequestThinkingOff is the default: no thinking field, and reasoning is
// NOT replayed (even with a signature present) since the model wasn't asked to think.
func TestBuildRequestThinkingOff(t *testing.T) {
	c := &client{model: "claude-opus-4-8"}
	r := c.buildRequest(context.Background(), provider.Request{Messages: []provider.Message{
		{Role: provider.RoleUser, Content: "hi"},
		{Role: provider.RoleAssistant, Content: "ok", ReasoningContent: "x", ReasoningSignature: "sig"},
	}})
	if r.Thinking != nil || r.OutputConfig != nil {
		t.Fatalf("thinking should be off by default: %+v / %+v", r.Thinking, r.OutputConfig)
	}
	for _, b := range r.Messages[1].Content {
		if b.Type == "thinking" {
			t.Fatal("thinking block must not be replayed when thinking is off")
		}
	}
}

func TestBuildRequestDropsLocalMetadata(t *testing.T) {
	c := &client{model: "claude-opus-4-8"}
	r := c.buildRequest(context.Background(), provider.Request{Messages: []provider.Message{
		{Role: provider.RoleUser, Content: "continue"},
		{Role: provider.RoleUser, Content: "edited prompt", Edited: true, Original: "original prompt"},
		{Role: provider.RoleAssistant, Content: "done", WorkDurationMs: 24_000, MemoryCitations: []provider.MemoryCitation{{
			ID: "mem-1", Source: "MEMORY.md", LineStart: 116, LineEnd: 123, Note: "workflow",
		}}},
	}})
	b, err := json.Marshal(r.Messages)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "memoryCitations") || strings.Contains(string(b), "MEMORY.md") {
		t.Fatalf("local memory citations leaked into Anthropic request: %s", b)
	}
	if strings.Contains(string(b), "workDurationMs") || strings.Contains(string(b), "work_duration_ms") {
		t.Fatalf("local work duration leaked into Anthropic request: %s", b)
	}
	if strings.Contains(string(b), "original prompt") || strings.Contains(string(b), `"edited"`) || strings.Contains(string(b), `"original"`) {
		t.Fatalf("local edit metadata leaked into Anthropic request: %s", b)
	}
	if !strings.Contains(string(b), "done") {
		t.Fatalf("assistant content was dropped with local metadata: %s", b)
	}
}
