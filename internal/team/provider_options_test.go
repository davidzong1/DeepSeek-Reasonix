package team

import (
	"errors"
	"strings"
	"testing"
)

// TestProviderOptionsOrderAndLabels pins the pool form's choice list: three
// canonical values in UI order with the labels the TUI renders, so the CLI
// and the TUI share one definition.
func TestProviderOptionsOrderAndLabels(t *testing.T) {
	got := ProviderOptions()
	want := []ProviderOption{
		{ProviderAnthropic, "Claude (Anthropic)"},
		{ProviderOpenAI, "GPT (OpenAI)"},
		{ProviderDeepSeek, "DeepSeek"},
	}
	if len(got) != len(want) {
		t.Fatalf("options = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("option %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestValidateProviderValue pins the canonical gate: the three values and
// empty pass (unconfigured stays legal), anything else is refused with a
// message that lists the options and never echoes the value.
func TestValidateProviderValue(t *testing.T) {
	for _, v := range []string{"", ProviderAnthropic, ProviderOpenAI, ProviderDeepSeek, " openai "} {
		if err := ValidateProviderValue(v); err != nil {
			t.Errorf("ValidateProviderValue(%q) = %v, want nil", v, err)
		}
	}
	refused := "some-future-provider"
	err := ValidateProviderValue(refused)
	if err == nil {
		t.Fatalf("ValidateProviderValue(%q) should be refused", refused)
	}
	if !strings.Contains(err.Error(), "must be one of: anthropic, openai, deepseek") {
		t.Fatalf("refusal should list the options, got: %v", err)
	}
	if strings.Contains(err.Error(), refused) {
		t.Fatalf("refusal must not echo the value, got: %v", err)
	}
	var fe *AgentUserFieldError
	if !errors.As(err, &fe) || fe.Field != AgentUserFieldProvider {
		t.Fatalf("refusal should be a provider field error, got %T: %v", err, err)
	}
}

// TestResolveProvider pins the runtime seam: legacy values normalize first
// (dsh, codex), DeepSeek with no base URL dials the official
// Anthropic-compatible endpoint, a DeepSeek base URL picks the route its
// path names, and an unknown provider is refused — never guessed.
func TestResolveProvider(t *testing.T) {
	cases := []struct {
		name, provider, baseURL, wantKind, wantEndpoint string
		wantErr                                         bool
	}{
		{"anthropic default", ProviderAnthropic, "", "anthropic", "https://api.anthropic.com", false},
		{"anthropic override", ProviderAnthropic, "https://proxy.example.com", "anthropic", "https://proxy.example.com", false},
		{"openai default", ProviderOpenAI, "", "openai", "https://api.openai.com", false},
		{"deepseek default", ProviderDeepSeek, "", "anthropic", DeepSeekDefaultBaseURL, false},
		{"deepseek anthropic route", ProviderDeepSeek, "https://api.deepseek.com/anthropic", "anthropic", "https://api.deepseek.com/anthropic", false},
		{"deepseek openai route", ProviderDeepSeek, "https://api.deepseek.com", "openai", "https://api.deepseek.com", false},
		{"legacy dsh", "dsh", "", "anthropic", DeepSeekDefaultBaseURL, false},
		{"legacy codex", "codex", "", "openai", "https://api.openai.com", false},
		{"legacy deepseek-responses", "deepseek-responses", "", "anthropic", DeepSeekDefaultBaseURL, false},
		{"unknown", "mystery", "", "", "", true},
	}
	for _, c := range cases {
		kind, endpoint, err := ResolveProvider(c.provider, c.baseURL)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: ResolveProvider(%q) should fail", c.name, c.provider)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: ResolveProvider(%q, %q) = %v", c.name, c.provider, c.baseURL, err)
			continue
		}
		if kind != c.wantKind || endpoint != c.wantEndpoint {
			t.Errorf("%s: ResolveProvider(%q, %q) = (%q, %q), want (%q, %q)",
				c.name, c.provider, c.baseURL, kind, endpoint, c.wantKind, c.wantEndpoint)
		}
	}
}

func TestResolveAgentUserProviderLongContextDeepSeekUsesAnthropic(t *testing.T) {
	kind, endpoint, err := ResolveAgentUserProvider(AgentUser{
		Provider: ProviderDeepSeek,
		BaseURL:  "https://gateway.example.com/v1",
		Model:    "deepseek/deepseek-v4-flash[1m]",
	})
	if err != nil {
		t.Fatal(err)
	}
	if kind != "anthropic" || endpoint != "https://gateway.example.com/v1" {
		t.Fatalf("long-context route = (%q, %q), want anthropic gateway", kind, endpoint)
	}

	kind, _, err = ResolveAgentUserProvider(AgentUser{
		Provider: ProviderDeepSeek,
		BaseURL:  "https://gateway.example.com/v1",
		Model:    "deepseek/deepseek-v4-flash",
	})
	if err != nil {
		t.Fatal(err)
	}
	if kind != "openai" {
		t.Fatalf("ordinary custom DeepSeek route = %q, want openai", kind)
	}
}
