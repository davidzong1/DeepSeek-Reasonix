package boot

// Strict provider/model failure semantics (route P0.2): failures name the
// requested model, resolved provider kind/name, route, and missing credential
// source. %w preserves cause and error ownership.

import (
	"fmt"
	"strings"

	"reasonix/internal/config"
	"reasonix/internal/provider"
)

// missingCredentialText returns the "which credential is missing" fragment
// for the selected entry, or "" when the entry has a key or needs none.
func missingCredentialText(e *config.ProviderEntry) string {
	if e == nil || !e.RequiresAPIKey() || e.APIKey() != "" {
		return ""
	}
	env := strings.TrimSpace(e.APIKeyEnv)
	if env == "" {
		return "; missing credential: api_key_env is not set for a remote provider (set one in reasonix.toml)"
	}
	if src := strings.TrimSpace(e.APIKeySourceLabel()); src != "" {
		return fmt.Sprintf("; missing credential %s (looked up via %s)", env, src)
	}
	return "; missing credential " + env
}

// providerConfigSource names the config file the resolved route came from,
// or "<no config file>" for in-memory configs.
func providerConfigSource(cfg *config.Config) string {
	if cfg == nil {
		return "<no config file>"
	}
	if path := config.SourcePath(); path != "" {
		return path
	}
	return "<no config file>"
}

// strictEntryFailure wraps a resolution/validation failure with the selected
// entry's observability fields: requested model, provider kind/name, route,
// and missing-credential detail. The cause is preserved for errors.Is/As.
func strictEntryFailure(e *config.ProviderEntry, requestedModel string, cause error) error {
	if e == nil {
		return cause
	}
	route := strings.TrimSpace(e.BaseURL)
	if route == "" {
		route = "(no base_url)"
	}
	kind := strings.TrimSpace(e.Kind)
	if kind == "" {
		kind = "(unset kind)"
	}
	name := strings.TrimSpace(e.Name)
	if name == "" {
		name = "(unnamed provider)"
	}
	return fmt.Errorf("model %q resolved to provider %q (kind %s, route %s)%s: %w",
		requestedModel, name, kind, route, missingCredentialText(e), cause)
}

// ensureRegisteredKind fails fast on a configured kind no provider factory
// serves, so an invalid provider/model never reaches backend assembly. The
// error lists the registered kinds for a corrective path.
func ensureRegisteredKind(e *config.ProviderEntry, requestedModel string) error {
	if e == nil {
		return nil
	}
	kind := strings.TrimSpace(e.Kind)
	for _, registered := range provider.Kinds() {
		if registered == kind {
			return nil
		}
	}
	return strictEntryFailure(e, requestedModel,
		fmt.Errorf("provider kind %q is not registered (registered: %v)", e.Kind, provider.Kinds()))
}
