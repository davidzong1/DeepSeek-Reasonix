package team

import (
	"fmt"
	"net/url"
	"strings"
)

// Agent-user pool-form field names; each matches the field's role in the
// import mapping and the TUI form, so a refusal names what the user typed.
const (
	AgentUserFieldID       = "id"
	AgentUserFieldIdentity = "identity"
	AgentUserFieldProvider = "provider"
	AgentUserFieldBaseURL  = "base_url"
	AgentUserFieldModel    = "model"
	AgentUserFieldEffort   = "effort"
	AgentUserFieldAPIKey   = "api_key"
)

// agentUserLimits bounds every field against accidental bloat. The values are
// generous: nothing format-free is format-restricted, only bounded.
const (
	agentUserIDLimit       = 128
	agentUserIdentityLimit = 256
	agentUserProviderLimit = 64
	agentUserBaseURLLimit  = 1024
	agentUserModelLimit    = 256
	agentUserEffortLimit   = 64
	agentUserAPIKeyLimit   = 4096
)

// Canonical agent-user providers. The runtime registers protocol kinds
// "anthropic" and "openai" (provider.Register); "deepseek" is a semantic value
// the later resolution chain maps onto the official Anthropic-compatible
// endpoint (https://api.deepseek.com/anthropic) — never a fabricated kind.
// Whole-entry validation refuses any other non-blank value, so a member can
// never be launched with a provider the runtime cannot resolve. Empty stays
// legal: an unconfigured entry renders until a field is set.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
	ProviderDeepSeek  = "deepseek"
)

// CanonicalProviders is the provider option list in UI order.
func CanonicalProviders() []string {
	return []string{ProviderAnthropic, ProviderOpenAI, ProviderDeepSeek}
}

// NormalizeProvider maps legacy import values onto the canonical set; an
// unknown value passes through unchanged, so old documents stay loadable and
// reachability is judged at consumption, never silently rewritten (K5).
func NormalizeProvider(name string) string {
	switch name {
	case "dsh", "deepseek-responses":
		return ProviderDeepSeek
	case "codex":
		return ProviderOpenAI
	}
	return name
}

// providerCanonical reports whether name is one of the canonical providers.
func providerCanonical(name string) bool {
	switch name {
	case ProviderAnthropic, ProviderOpenAI, ProviderDeepSeek:
		return true
	}
	return false
}

// AgentUserFieldError reports one field-level refusal: which field and why.
// It never carries the value — an api_key must not echo itself into an error
// message (K2/K3), and other fields are long enough to bloat logs for no
// reason.
type AgentUserFieldError struct {
	Field  string
	Reason string
}

func (e *AgentUserFieldError) Error() string {
	return fmt.Sprintf("agent user %s: %s", e.Field, e.Reason)
}

// ValidateAgentUserField checks one pool-form field. An empty value is valid
// for every field — the form skips blank entries, and "unconfigured" renders
// until a field is set. An unknown field name is refused so a form typo
// surfaces instead of silently passing. A non-empty provider must be one of
// the canonical values (ValidateProviderValue); the refusal lists the legal
// options and never echoes the value.
func ValidateAgentUserField(name, value string) error {
	if value == "" {
		return nil
	}
	var limit int
	switch name {
	case AgentUserFieldID:
		limit = agentUserIDLimit
	case AgentUserFieldIdentity:
		limit = agentUserIdentityLimit
	case AgentUserFieldProvider:
		limit = agentUserProviderLimit
	case AgentUserFieldBaseURL:
		limit = agentUserBaseURLLimit
	case AgentUserFieldModel:
		limit = agentUserModelLimit
	case AgentUserFieldEffort:
		limit = agentUserEffortLimit
	case AgentUserFieldAPIKey:
		limit = agentUserAPIKeyLimit
	default:
		return &AgentUserFieldError{Field: name, Reason: "unknown field"}
	}
	if err := validateFieldLength(name, value, limit); err != nil {
		return err
	}
	if name == AgentUserFieldProvider {
		return ValidateProviderValue(value)
	}
	if name == AgentUserFieldBaseURL {
		return validateBaseURL(value)
	}
	return nil
}

// validateFieldLength refuses a value over its field's ceiling.
func validateFieldLength(name, value string, limit int) error {
	if len(value) > limit {
		return &AgentUserFieldError{Field: name, Reason: fmt.Sprintf("too long (%d > %d)", len(value), limit)}
	}
	return nil
}

// validateBaseURL refuses anything the provider layer could not dial: a
// scheme other than http(s) or a missing host is a typo, not a config.
func validateBaseURL(value string) error {
	u, err := url.Parse(value)
	if err != nil {
		return &AgentUserFieldError{Field: AgentUserFieldBaseURL, Reason: "not a URL: " + err.Error()}
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return &AgentUserFieldError{Field: AgentUserFieldBaseURL, Reason: "must be an http(s) URL with a host"}
	}
	return nil
}

// ValidateAgentUser checks a whole pool entry: the id is never empty, and
// every non-empty field passes ValidateAgentUserField. The provider is
// restricted to the canonical set — a member must never be launched with a
// provider the runtime cannot resolve — while model and effort stay
// open-ended (import supports models the TUI has never heard of), and an
// entry configured over several form passes is legal at every intermediate
// step. Store updates apply the legacy-preserve exemption via
// validateAgentUserAllowLegacy.
func ValidateAgentUser(u AgentUser) error {
	if u.Provider != "" && !providerCanonical(u.Provider) {
		return &AgentUserFieldError{Field: AgentUserFieldProvider, Reason: "must be one of anthropic, openai, deepseek"}
	}
	return validateAgentUserFields(u)
}

// validateAgentUserAllowLegacy is ValidateAgentUser with one exemption: a
// non-canonical provider that equals legacyProvider passes, so editing other
// fields of an entry imported before the canonical set existed never orphans
// it — the provider is preserved, never silently rewritten.
func validateAgentUserAllowLegacy(u AgentUser, legacyProvider string) error {
	if u.Provider != "" && u.Provider != legacyProvider && !providerCanonical(u.Provider) {
		return &AgentUserFieldError{Field: AgentUserFieldProvider, Reason: "must be one of anthropic, openai, deepseek"}
	}
	return validateAgentUserFields(u)
}

// validateAgentUserFields runs the whole-entry checks below the provider
// gate: the id is required, every non-empty field is legal, and the
// provider/model pair must be one the resolved endpoint can serve. The
// provider is length-checked here but its legality is decided by the caller's
// gate (ValidateAgentUser or validateAgentUserAllowLegacy) — a legacy value
// must not be refused again after the gate let it through.
func validateAgentUserFields(u AgentUser) error {
	if strings.TrimSpace(u.UserID) == "" {
		return ErrInvalidAgentUser
	}
	fields := []struct{ name, value string }{
		{AgentUserFieldID, u.UserID},
		{AgentUserFieldIdentity, u.Identity},
		{AgentUserFieldProvider, u.Provider},
		{AgentUserFieldBaseURL, u.BaseURL},
		{AgentUserFieldModel, u.Model},
		{AgentUserFieldEffort, u.Effort},
		{AgentUserFieldAPIKey, u.APIKey},
	}
	for _, f := range fields {
		if f.name == AgentUserFieldProvider {
			if err := validateFieldLength(f.name, f.value, agentUserProviderLimit); err != nil {
				return err
			}
			continue
		}
		if err := ValidateAgentUserField(f.name, f.value); err != nil {
			return err
		}
	}
	return validateProviderModel(u)
}

// validateProviderModel refuses a model the entry's resolved endpoint cannot
// serve. DeepSeek's official route and Anthropic both dial the anthropic
// protocol, which serves no "gpt-*" model — a gpt model there fails on the
// first request with an authentication-shaped error that reads like a
// credential problem. The OpenAI route (openai provider, or a deepseek entry
// with an OpenAI-compatible base url) stays open-ended, so custom OpenAI
// models remain legal. Empty provider or model is an in-progress form state,
// not an error; a legacy provider the allow-legacy gate preserved is judged
// at consumption, never refused twice here.
func validateProviderModel(u AgentUser) error {
	model := strings.TrimSpace(u.Model)
	if u.Provider == "" || model == "" {
		return nil
	}
	if !strings.HasPrefix(strings.ToLower(model), "gpt") {
		return nil
	}
	kind, _, err := ResolveProvider(u.Provider, u.BaseURL)
	if err != nil || kind != "anthropic" {
		return nil
	}
	return &AgentUserFieldError{Field: AgentUserFieldModel, Reason: "gpt-prefixed models are not served by the anthropic endpoint this provider dials; use provider \"openai\" for custom gpt models, or a deepseek/anthropic model"}
}
