package config

import (
	"reflect"
	"testing"

	"reasonix/internal/provider"
)

func TestNormalizeDesktopOfficialProviderAccessCanonicalizesOnlyDeepSeekIDs(t *testing.T) {
	c := Default()
	c.DefaultModel = "deepseek-flash/deepseek-v4-pro"
	c.Desktop.ProviderAccess = []string{"deepseek-flash", "mimo-pro"}
	normalizeDesktopOfficialProviderAccess(c)
	if len(c.Desktop.ProviderAccess) != 2 || c.Desktop.ProviderAccess[0] != "deepseek" || c.Desktop.ProviderAccess[1] != "mimo-pro" {
		t.Fatalf("provider_access = %+v, want only DeepSeek canonicalized", c.Desktop.ProviderAccess)
	}
	if c.DefaultModel != "deepseek/deepseek-v4-pro" {
		t.Fatalf("default_model = %q, want deepseek/deepseek-v4-pro", c.DefaultModel)
	}
	if _, ok := c.Provider("deepseek"); !ok {
		t.Fatal("canonical deepseek provider missing")
	}
	if resolved, ok := c.ResolveModel("deepseek-pro/deepseek-v4-pro"); !ok || resolved.Name != "deepseek" {
		t.Fatalf("compatible legacy reference resolved to %+v, found=%v; want canonical DeepSeek", resolved, ok)
	}
	if _, ok := c.Provider("mimo-token-plan"); ok {
		t.Fatal("mimo-token-plan should not be injected as an official provider")
	}
}

func TestNormalizeDesktopOfficialProviderAccessBackfillsOfficialContextWindow(t *testing.T) {
	c := &Config{
		Desktop: DesktopConfig{ProviderAccess: []string{"deepseek-flash", "mimo-api", "mimo-token-plan"}},
		Providers: []ProviderEntry{
			{
				Name:      "deepseek-flash",
				Kind:      "openai",
				BaseURL:   "https://api.deepseek.com",
				Model:     "deepseek-v4-flash",
				APIKeyEnv: "DEEPSEEK_API_KEY",
			},
			{
				Name:      "mimo-api",
				Kind:      "openai",
				BaseURL:   "https://api.xiaomimimo.com/v1",
				Model:     "mimo-v2.5-pro",
				APIKeyEnv: "MIMO_API_KEY",
			},
			{
				Name:      "mimo-token-plan",
				Kind:      "openai",
				BaseURL:   "https://token-plan-cn.xiaomimimo.com/v1",
				Model:     "mimo-v2.5-pro",
				APIKeyEnv: "MIMO_API_KEY",
			},
		},
	}

	normalizeDesktopOfficialProviderAccess(c)

	deepseek, ok := c.Provider("deepseek")
	if !ok {
		t.Fatal("deepseek provider missing")
	}
	if deepseek.ContextWindow != 1_000_000 {
		t.Fatalf("deepseek context_window = %d, want official default", deepseek.ContextWindow)
	}
	mimoAPI, ok := c.Provider("mimo-api")
	if !ok {
		t.Fatal("mimo-api provider missing")
	}
	if mimoAPI.ContextWindow != 1_048_576 {
		t.Fatalf("mimo-api context_window = %d, want migrated MiMo default", mimoAPI.ContextWindow)
	}
	mimoTokenPlan, ok := c.Provider("mimo-token-plan")
	if !ok {
		t.Fatal("mimo-token-plan provider missing")
	}
	if mimoTokenPlan.ContextWindow != 1_048_576 {
		t.Fatalf("mimo-token-plan context_window = %d, want migrated MiMo default", mimoTokenPlan.ContextWindow)
	}
}

func TestNormalizeDesktopOfficialProviderAccessKeepsCustomAlias(t *testing.T) {
	c := &Config{
		Desktop: DesktopConfig{ProviderAccess: []string{"deepseek-flash"}},
		Providers: []ProviderEntry{{
			Name:    "deepseek-flash",
			Kind:    "openai",
			BaseURL: "https://proxy.example/v1",
			Model:   "deepseek-v4-flash",
		}},
	}

	normalizeDesktopOfficialProviderAccess(c)

	if len(c.Desktop.ProviderAccess) != 1 || c.Desktop.ProviderAccess[0] != "deepseek-flash" {
		t.Fatalf("provider_access = %+v, want custom alias preserved", c.Desktop.ProviderAccess)
	}
	if _, ok := c.Provider("deepseek"); ok {
		t.Fatal("custom deepseek-flash proxy should not create canonical deepseek provider")
	}
}

func TestNormalizeDesktopOfficialProviderAccessKeepsIncompatibleOfficialAliases(t *testing.T) {
	c := &Config{
		DefaultModel: "deepseek-pro/deepseek-v4-pro",
		Desktop:      DesktopConfig{ProviderAccess: []string{"deepseek"}},
		Providers: []ProviderEntry{
			{
				Name: "deepseek-flash", Kind: "anthropic", BaseURL: deepSeekAnthropicBaseURL,
				Model: "deepseek-v4-flash", APIKeyEnv: "DEEPSEEK_API_KEY",
				Headers: map[string]string{"X-Route": "flash"},
			},
			{
				Name: "deepseek-pro", Kind: "anthropic", BaseURL: deepSeekAnthropicBaseURL,
				Model: "deepseek-v4-pro", APIKeyEnv: "DEEPSEEK_API_KEY",
				Headers: map[string]string{"X-Route": "pro"},
			},
		},
	}

	normalizeDesktopOfficialProviderAccess(c)

	if got := c.Desktop.ProviderAccess; !reflect.DeepEqual(got, []string{"deepseek-flash", "deepseek-pro"}) {
		t.Fatalf("provider_access = %v, want incompatible aliases preserved separately", got)
	}
	if _, ok := c.Provider("deepseek"); ok {
		t.Fatal("incompatible provider-wide headers must not be collapsed into a canonical provider")
	}
	if c.DefaultModel != "deepseek-pro/deepseek-v4-pro" {
		t.Fatalf("default_model = %q, want legacy Pro reference preserved", c.DefaultModel)
	}
	resolved, ok := c.ResolveModel(c.DefaultModel)
	if !ok || resolved.Headers["X-Route"] != "pro" {
		t.Fatalf("resolved Pro provider = %+v, found=%v; want its original transport", resolved, ok)
	}
}

func TestNormalizeDesktopOfficialProviderAccessKeepsAliasIncompatibleWithExistingCanonical(t *testing.T) {
	c := &Config{
		DefaultModel: "deepseek-pro/deepseek-v4-pro",
		Desktop:      DesktopConfig{ProviderAccess: []string{"deepseek", "deepseek-pro"}},
		Providers: []ProviderEntry{
			{
				Name: "deepseek", Kind: "anthropic", BaseURL: deepSeekAnthropicBaseURL,
				Models: []string{"deepseek-v4-flash", "deepseek-v4-pro"}, Default: "deepseek-v4-flash",
				APIKeyEnv: "DEEPSEEK_API_KEY",
			},
			{
				Name: "deepseek-pro", Kind: "openai", BaseURL: "https://api.deepseek.com",
				Model: "deepseek-v4-pro", APIKeyEnv: "DEEPSEEK_API_KEY",
				Headers: map[string]string{"X-Route": "legacy-pro"},
			},
		},
	}

	normalizeDesktopOfficialProviderAccess(c)

	if got := c.Desktop.ProviderAccess; !reflect.DeepEqual(got, []string{"deepseek", "deepseek-pro"}) {
		t.Fatalf("provider_access = %v, want canonical and incompatible alias preserved", got)
	}
	if c.DefaultModel != "deepseek-pro/deepseek-v4-pro" {
		t.Fatalf("default_model = %q, want incompatible legacy reference preserved", c.DefaultModel)
	}
	resolved, ok := c.ResolveModel(c.DefaultModel)
	if !ok || resolved.Kind != "openai" || resolved.Headers["X-Route"] != "legacy-pro" {
		t.Fatalf("resolved legacy provider = %+v, found=%v; want original transport", resolved, ok)
	}
}

func TestNormalizeDesktopOfficialProviderAccessKeepsConflictingModelAliases(t *testing.T) {
	c := &Config{
		DefaultModel: "deepseek-pro/deepseek-v4-flash",
		Desktop:      DesktopConfig{ProviderAccess: []string{"deepseek"}},
		Providers: []ProviderEntry{
			{
				Name: "deepseek-flash", Kind: "anthropic", BaseURL: deepSeekAnthropicBaseURL,
				Model: "deepseek-v4-flash", APIKeyEnv: "DEEPSEEK_API_KEY", ContextWindow: 900_000,
			},
			{
				Name: "deepseek-pro", Kind: "anthropic", BaseURL: deepSeekAnthropicBaseURL,
				Model: "deepseek-v4-flash", APIKeyEnv: "DEEPSEEK_API_KEY", ContextWindow: 800_000,
			},
		},
	}

	normalizeDesktopOfficialProviderAccess(c)

	if got := c.Desktop.ProviderAccess; !reflect.DeepEqual(got, []string{"deepseek-flash", "deepseek-pro"}) {
		t.Fatalf("provider_access = %v, want aliases with conflicting model metadata preserved", got)
	}
	if _, ok := c.Provider("deepseek"); ok {
		t.Fatal("conflicting model metadata must not be collapsed into a canonical provider")
	}
	resolved, ok := c.ResolveModel(c.DefaultModel)
	if !ok || resolved.ContextWindow != 800_000 {
		t.Fatalf("resolved Pro alias = %+v, found=%v; want its original context window", resolved, ok)
	}
}

func TestNormalizeDesktopOfficialProviderAccessKeepsUnsupportedOfficialProtocolAlias(t *testing.T) {
	c := &Config{
		DefaultModel: "deepseek-flash/deepseek-v4-flash",
		Desktop:      DesktopConfig{ProviderAccess: []string{"deepseek-flash"}},
		Providers: []ProviderEntry{{
			Name: "deepseek-flash", Kind: "responses", BaseURL: "https://api.deepseek.com",
			Model: "deepseek-v4-flash", APIKeyEnv: "DEEPSEEK_API_KEY",
		}},
	}

	normalizeDesktopOfficialProviderAccess(c)

	if got := c.Desktop.ProviderAccess; !reflect.DeepEqual(got, []string{"deepseek-flash"}) {
		t.Fatalf("provider_access = %v, want unsupported legacy protocol alias preserved", got)
	}
	if _, ok := c.Provider("deepseek"); ok {
		t.Fatal("a legacy Responses alias must not be rewritten into the Anthropic canonical provider")
	}
	if c.DefaultModel != "deepseek-flash/deepseek-v4-flash" {
		t.Fatalf("default_model = %q, want unsupported protocol reference preserved", c.DefaultModel)
	}
}

func TestNormalizeDesktopOfficialProviderAccessPreservesDefaultAndUnknownPrices(t *testing.T) {
	futurePrice := &provider.Pricing{Input: 4.2, Output: 8.4, Currency: "T"}
	c := &Config{
		Desktop: DesktopConfig{ProviderAccess: []string{"deepseek"}},
		Providers: []ProviderEntry{
			{
				Name: "deepseek-flash", Kind: "anthropic", BaseURL: deepSeekAnthropicBaseURL,
				Models: []string{"deepseek-v4-flash", "deepseek-v5-preview"}, Default: "deepseek-v5-preview",
				APIKeyEnv: "DEEPSEEK_API_KEY", Prices: map[string]*provider.Pricing{"deepseek-v5-preview": futurePrice},
			},
			{
				Name: "deepseek-pro", Kind: "anthropic", BaseURL: deepSeekAnthropicBaseURL,
				Model: "deepseek-v4-pro", APIKeyEnv: "DEEPSEEK_API_KEY",
			},
		},
	}

	normalizeDesktopOfficialProviderAccess(c)

	canonical, ok := c.Provider("deepseek")
	if !ok {
		t.Fatal("canonical DeepSeek provider missing")
	}
	if canonical.Default != "deepseek-v5-preview" {
		t.Fatalf("canonical default = %q, want explicit legacy default preserved", canonical.Default)
	}
	got := canonical.Prices["deepseek-v5-preview"]
	if got == nil || !reflect.DeepEqual(got, futurePrice) {
		t.Fatalf("future model price = %+v, want %+v", got, futurePrice)
	}
}
