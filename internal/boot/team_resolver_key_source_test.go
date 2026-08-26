package boot

import (
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/provider"
)

// TestMemberResolverEntryNeverTriggersGlobalKeyNotice pins where a team
// member's key errors surface: the resolver dials with the pool entry's key
// directly, so the synthetic entry carries no api_key_env and requires no
// key — the global missing-key notice never fires for a member entry, and
// the config path is the control that still requires one.
func TestMemberResolverEntryNeverTriggersGlobalKeyNotice(t *testing.T) {
	r := &provider.StaticResolver{Descriptors: []provider.Descriptor{{
		Ref: "u1/gpt-5.6", DisplayName: "u1", Model: "gpt-5.6", Tools: true,
	}}}
	entry, _, err := resolveModelEntry(r, nil, "u1/gpt-5.6")
	if err != nil {
		t.Fatal(err)
	}
	if entry.APIKeyEnv != "" {
		t.Errorf("api_key_env = %q, want empty (the key travels through the resolver)", entry.APIKeyEnv)
	}
	if entry.RequiresAPIKey() {
		t.Error("a member entry must not require a key: the global missing-key notice would fire for it")
	}

	// Control: the config path still demands its key, so the divergence above
	// is the resolver seam, not a broken RequiresAPIKey.
	cfgEntry := &config.ProviderEntry{
		Name: "x", Kind: "openai", BaseURL: "https://api.openai.com", APIKeyEnv: "OPENAI_API_KEY",
	}
	if !cfgEntry.RequiresAPIKey() {
		t.Error("an official-host config entry must still require its key")
	}
}
