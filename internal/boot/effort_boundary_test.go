package boot

import (
	"errors"
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/netclient"
	"reasonix/internal/provider"
)

// One resolved vocabulary feeds both boundaries: the explicit-selection side
// (/effort, desktop menu, --effort, ACP session config) through
// config.NormalizeEffort, and the per-request side through the capability the
// adapter freezes at construction and Stream re-checks. Pin them together so a
// clamp reintroduced on either side fails here.
func TestEffortVerdictAgreesAcrossConfigAndRequestBoundaries(t *testing.T) {
	entries := []config.ProviderEntry{
		{Name: "official-v4-flash", Kind: "openai", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-flash"},
		{Name: "official-v4-pro", Kind: "openai", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-pro"},
		{Name: "declared-high-max", Kind: "openai", BaseURL: "https://gateway.example.invalid/v1", Model: "custom", SupportedEfforts: []string{"high", "max"}},
		{Name: "declared-high", Kind: "openai", BaseURL: "https://gateway.example.invalid/v1", Model: "custom", SupportedEfforts: []string{"high"}},
		{Name: "undeclared-generic", Kind: "openai", BaseURL: "https://gateway.example.invalid/v1", Model: "custom"},
		{Name: "mimo", Kind: "openai", BaseURL: "https://api.xiaomimimo.com/v1", Model: "m"},
	}
	levels := []string{"disabled", "low", "medium", "high", "max", "xhigh", "turbo"}
	for _, e := range entries {
		t.Run(e.Name, func(t *testing.T) {
			built, err := NewProvider(&e)
			if err != nil {
				t.Fatalf("NewProvider: %v", err)
			}
			requestCap, ok := built.(provider.ReasoningProvider)
			if !ok {
				t.Fatal("adapter does not expose a reasoning capability")
			}
			for _, level := range levels {
				_, cfgErr := config.NormalizeEffort(&e, level)
				reqErr := requestCap.ReasoningCapability().Validate(e.Model, level)
				if (cfgErr == nil) != (reqErr == nil) {
					t.Errorf("level %q: selection verdict %v disagrees with request verdict %v", level, cfgErr, reqErr)
				}
				if reqErr != nil {
					var unsupported *provider.UnsupportedReasoningEffort
					if !errors.As(reqErr, &unsupported) {
						t.Errorf("level %q: refusal is not a typed UnsupportedReasoningEffort: %v", level, reqErr)
					}
				}
			}
		})
	}
}

// The official V4 models are the one endpoint that accepts max without any
// declaration. Losing that would silently narrow the built-in scale.
func TestOfficialDeepSeekV4AcceptsMaxWithoutDeclaration(t *testing.T) {
	for _, model := range []string{"deepseek-v4-flash", "deepseek-v4-pro"} {
		e := config.ProviderEntry{Name: "official", Kind: "openai", BaseURL: "https://api.deepseek.com", Model: model}
		if got, err := config.NormalizeEffort(&e, "max"); err != nil || got != "max" {
			t.Fatalf("%s /effort max = %q/%v, want max/nil", model, got, err)
		}
		built, err := NewProvider(&e)
		if err != nil {
			t.Fatalf("%s NewProvider: %v", model, err)
		}
		if err := built.(provider.ReasoningProvider).ReasoningCapability().Validate(model, "max"); err != nil {
			t.Fatalf("%s request-period max refused: %v", model, err)
		}
		// The same entry still refuses the retired aliases, so the positive
		// guardrail cannot be met by widening the vocabulary to everything.
		for _, alias := range []string{"medium", "xhigh"} {
			if _, err := config.NormalizeEffort(&e, alias); err == nil {
				t.Fatalf("%s /effort %s was accepted", model, alias)
			}
		}
	}
}

// An explicit selection is never rewritten to a neighbouring level: the
// declared list is the whole vocabulary, and undeclared means refused.
func TestUndeclaredEffortIsRefusedNotClamped(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entry   config.ProviderEntry
		refused []string
	}{
		{
			name:    "declared-high prefers no max fallback",
			entry:   config.ProviderEntry{Name: "g", Kind: "openai", BaseURL: "https://gateway.example.invalid/v1", Model: "custom", SupportedEfforts: []string{"high"}},
			refused: []string{"max", "xhigh", "medium"},
		},
		{
			name:    "mimo has no max to fall back to",
			entry:   config.ProviderEntry{Name: "mimo", Kind: "openai", BaseURL: "https://api.xiaomimimo.com/v1", Model: "m"},
			refused: []string{"max", "xhigh", "disabled"},
		},
		{
			name:    "generic endpoint declares nothing",
			entry:   config.ProviderEntry{Name: "g", Kind: "openai", BaseURL: "https://gateway.example.invalid/v1", Model: "custom"},
			refused: []string{"max", "high", "low", "medium"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, level := range tc.refused {
				_, err := config.NormalizeEffort(&tc.entry, level)
				if err == nil {
					t.Errorf("explicit %q was accepted", level)
					continue
				}
				if !strings.Contains(err.Error(), "UNSUPPORTED_REASONING_EFFORT") {
					t.Errorf("explicit %q refused with %v, want the typed contract error", level, err)
				}
			}
		})
	}
}

// The pinned thinking mode is refused where the level is selected, so the
// store and the adapter's constructor are never the first to see it: --effort
// reaches the resolver and the role preflight before assembly.
func TestPinnedThinkingSelectionRefusedBeforeAssembly(t *testing.T) {
	entry := config.ProviderEntry{Name: "gateway", Kind: "openai", BaseURL: "https://gateway.example.invalid/v1", Model: "custom", Models: []string{"custom"}, Thinking: "disabled"}
	cfg := &config.Config{Providers: []config.ProviderEntry{entry}}
	disabled := "disabled"

	resolver := NewLocalProviderResolver(cfg, netclient.ProxySpec{})
	_, err := resolver.Resolve(provider.Selection{Ref: "gateway/custom", Effort: &disabled})
	var unsupported *provider.UnsupportedReasoningEffort
	if !errors.As(err, &unsupported) {
		t.Fatalf("--effort disabled resolved with %v, want a typed refusal", err)
	}
	if strings.Contains(err.Error(), "must be low, medium, or high") {
		t.Fatalf("the adapter's constructor error leaked into selection: %v", err)
	}
	cfg.DefaultModel = "gateway/custom"
	roleErr := preflightRoleReasoning(cfg, Options{EffortOverride: &disabled}, nil, false)
	var role *RoleReasoningError
	if !errors.As(roleErr, &role) || role.Role != "execution" || !errors.As(roleErr, &unsupported) {
		t.Fatalf("preflight = %v, want the typed role/adapter refusal", roleErr)
	}
	// The inherit spelling still clears the override on the same entry.
	if err := config.ValidateEffortSelection(&entry, "auto"); err != nil {
		t.Fatalf("auto = %v, want no verdict", err)
	}
}

// The CLI --effort flag and the ACP per-session override both arrive as a
// provider.Selection.Effort, which the resolver normalizes before the adapter
// is constructed. A level the adapter would refuse must fail there instead of
// being quietly dropped.
func TestSelectionEffortRefusedDuringResolution(t *testing.T) {
	cfg := &config.Config{Providers: []config.ProviderEntry{{
		Name: "gateway", Kind: "openai", BaseURL: "https://gateway.example.invalid/v1",
		Models: []string{"custom-model"}, SupportedEfforts: []string{"low", "high"},
	}}}
	resolver := NewLocalProviderResolver(cfg, netclient.ProxySpec{})
	for _, tc := range []struct {
		effort  string
		refused bool
	}{
		{effort: "high"},
		{effort: "low"},
		{effort: "max", refused: true},
		{effort: "turbo", refused: true},
	} {
		effort := tc.effort
		_, err := resolver.Resolve(provider.Selection{Ref: "gateway/custom-model", Effort: &effort})
		var unsupported *provider.UnsupportedReasoningEffort
		if tc.refused {
			if !errors.As(err, &unsupported) {
				t.Errorf("--effort %s resolved with %v, want a typed refusal", effort, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("--effort %s failed to resolve: %v", effort, err)
		}
	}
}
