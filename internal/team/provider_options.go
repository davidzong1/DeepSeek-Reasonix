package team

import "strings"

// ProviderOption pairs one canonical value with the label the TUI shows, in
// the order the pool form offers them. The canonical values themselves live
// beside the field constants (ProviderAnthropic et al.); this file holds the
// UI-facing option list and the runtime resolution seam.
type ProviderOption struct {
	Value string
	Label string
}

// ProviderOptions returns the ordered provider choices — canonical value
// stored in agent_users.json, label rendered in the pool form — so the CLI
// and the TUI share one definition instead of duplicating the pair list.
func ProviderOptions() []ProviderOption {
	return []ProviderOption{
		{ProviderAnthropic, "Claude (Anthropic)"},
		{ProviderOpenAI, "GPT (OpenAI)"},
		{ProviderDeepSeek, "DeepSeek"},
	}
}

// DeepSeekDefaultBaseURL is the official DeepSeek Anthropic-compatible
// Messages endpoint — the same endpoint the new-install default provider
// entry uses, so provider-executed web search works out of the box.
const DeepSeekDefaultBaseURL = "https://api.deepseek.com/anthropic"

// ValidateProviderValue refuses a non-empty provider outside the canonical
// set; empty stays legal — an entry is unconfigured until a provider is set.
// The refusal names the legal options and never echoes the value.
func ValidateProviderValue(value string) error {
	if value == "" {
		return nil
	}
	if !providerCanonical(strings.TrimSpace(value)) {
		return &AgentUserFieldError{Field: AgentUserFieldProvider, Reason: "must be one of: anthropic, openai, deepseek"}
	}
	return nil
}

// ResolveProvider is the seam where a pool entry maps onto the provider
// runtime: the runtime kind and the endpoint to dial. Legacy import values
// normalize first (NormalizeProvider); a DeepSeek entry with no base URL
// dials the official Anthropic-compatible endpoint, and one with a base URL
// picks the route the URL names. Anything else is refused, so a member never
// starts against a guessed endpoint.
func ResolveProvider(provider, baseURL string) (kind, endpoint string, err error) {
	p := NormalizeProvider(strings.TrimSpace(provider))
	switch p {
	case ProviderAnthropic:
		return routeOrDefault("anthropic", baseURL, "https://api.anthropic.com")
	case ProviderOpenAI:
		return routeOrDefault("openai", baseURL, "https://api.openai.com")
	case ProviderDeepSeek:
		if baseURL == "" {
			return "anthropic", DeepSeekDefaultBaseURL, nil
		}
		if strings.Contains(baseURL, "/anthropic") {
			return "anthropic", baseURL, nil
		}
		return "openai", baseURL, nil
	default:
		return "", "", &AgentUserFieldError{Field: AgentUserFieldProvider, Reason: "unsupported provider " + p + ", use: anthropic, openai, deepseek"}
	}
}

// routeOrDefault resolves a provider whose endpoint is its own official one
// when the entry carries no base URL override.
func routeOrDefault(kind, baseURL, official string) (string, string, error) {
	if baseURL == "" {
		return kind, official, nil
	}
	return kind, baseURL, nil
}
