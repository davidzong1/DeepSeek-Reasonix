package config

import (
	"errors"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

// A pinned thinking field fixes the entry's mode. Its vocabulary still refuses a
// stored depth level, but it is not a menu: the ID must be refused at selection
// instead of being stored and only rejected by the adapter's constructor.
func TestPinnedThinkingIsNotASelectableLevel(t *testing.T) {
	pinned := &ProviderEntry{Name: "gateway", Kind: "openai", BaseURL: "https://gateway.example.invalid/v1", Model: "custom", Thinking: "disabled"}
	if cap := EffortCapabilityForEntry(pinned); !cap.Supported || len(cap.Levels) != 1 || cap.Levels[0] != "auto" || cap.Default != "disabled" {
		t.Fatalf("pinned capability = %+v, want auto only with the pinned default", cap)
	}
	_, err := NormalizeEffort(pinned, "disabled")
	var unsupported *provider.UnsupportedReasoningEffort
	if !errors.As(err, &unsupported) {
		t.Fatalf("NormalizeEffort(disabled) = %v, want a typed UnsupportedReasoningEffort", err)
	}
	for _, want := range []string{"UNSUPPORTED_REASONING_EFFORT", "thinking is disabled by configuration"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q lacks %q", err.Error(), want)
		}
	}
	if got, err := NormalizeEffort(pinned, "auto"); err != nil || got != "" {
		t.Fatalf("auto = %q/%v, want the inherit spelling", got, err)
	}
	// The stored-config half of the contract is untouched: a saved depth level
	// is still refused by the pinned vocabulary, not mapped to a neighbour.
	if err := ReasoningCapabilityForEntry(pinned).Validate("custom", "max"); err == nil || !strings.Contains(err.Error(), "[disabled]") {
		t.Fatalf("stored max verdict = %v, want the pinned vocabulary refusal", err)
	}
}

// Wherever the adapter does serialize disabled as a level — its own scale or a
// declaration — a pinned entry keeps the selection, so the refusal above cannot
// be met by narrowing every pinned vocabulary.
func TestDisabledStaysSelectableWhereTheAdapterSendsIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry *ProviderEntry
	}{
		{"official-deepseek", &ProviderEntry{Name: "ds", Kind: "openai", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-flash", Thinking: "disabled"}},
		{"deepseek-protocol-proxy", &ProviderEntry{Name: "proxy", Kind: "openai", BaseURL: "https://gateway.example.invalid/v1", Model: "custom", ReasoningProtocol: ReasoningProtocolDeepSeek, Thinking: "disabled"}},
		{"zhipu", &ProviderEntry{Name: "glm", Kind: "openai", BaseURL: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-5.1", Thinking: "disabled"}},
		{"longcat", &ProviderEntry{Name: "lc", Kind: "openai", BaseURL: "https://api.longcat.chat/v1", Model: "LongCat-2.0", Thinking: "disabled"}},
		{"minimax", &ProviderEntry{Name: "mm", Kind: "openai", BaseURL: "https://api.minimaxi.com/v1", Model: "MiniMax-M3", Thinking: "disabled"}},
		{"anthropic-knob", &ProviderEntry{Name: "anth", Kind: "anthropic", BaseURL: "https://custom.example", Model: "claude", Thinking: "disabled"}},
		{"declared-disabled", &ProviderEntry{Name: "gw", Kind: "openai", BaseURL: "https://gateway.example.invalid/v1", Model: "custom", Thinking: "disabled", SupportedEfforts: []string{"disabled", "high"}}},
		{"declared-depth-scale", &ProviderEntry{Name: "gw2", Kind: "openai", BaseURL: "https://gateway.example.invalid/v1", Model: "custom", ReasoningProtocol: ReasoningProtocolDeepSeek, Thinking: "disabled", SupportedEfforts: []string{"high", "max"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := NormalizeEffort(tc.entry, "disabled"); err != nil || got != "disabled" {
				t.Fatalf("disabled = %q/%v, want the adapter's own level", got, err)
			}
			if !containsString(EffortCapabilityForEntry(tc.entry).Levels, "disabled") {
				t.Fatalf("level list dropped a selectable ID: %+v", EffortCapabilityForEntry(tc.entry))
			}
		})
	}
}

// A vocabulary the adapter cannot send is refused on the selection path only:
// the stored verdict keeps reporting the pinned vocabulary so a saved value is
// still rejected at assembly, and the round-trip through ValidateEffortSelection
// stays the single verdict /effort, --effort and the resolver share.
func TestSelectionVerdictLeavesStoredRefusalIntact(t *testing.T) {
	pinned := &ProviderEntry{Name: "gateway", Kind: "openai", BaseURL: "https://gateway.example.invalid/v1", Model: "custom", Thinking: "disabled"}
	if err := ValidateEffortSelection(pinned, "max"); err == nil || !strings.Contains(err.Error(), "[disabled]") {
		t.Fatalf("selection max verdict = %v, want the vocabulary refusal", err)
	}
	if err := ValidateEffortSelection(pinned, ""); err != nil {
		t.Fatalf("empty selection = %v, want no verdict", err)
	}
	if err := ValidateEffortSelection(pinned, "auto"); err != nil {
		t.Fatalf("auto selection = %v, want no verdict", err)
	}
	if err := ValidateEffortSelection(nil, "high"); err == nil {
		t.Fatal("an absent entry still has an empty vocabulary")
	}
}
