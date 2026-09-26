package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// agentCacheKeyCases are the five keys that shape provider-cache behaviour, each
// set to a NON-default value. Every one of them is written by an operator as a
// rollback or an experiment, so every one of them has to survive a save.
var agentCacheKeyCases = []struct {
	key   string
	line  string
	check func(*Config) bool
}{
	{"low_yield_latch", "low_yield_latch = false", func(c *Config) bool { return !c.LowYieldLatchEnabled() }},
	{"message_shape_diagnosis", "message_shape_diagnosis = false", func(c *Config) bool { return !c.ShapeDiagnosisEnabled() }},
	{"cache_aware_compaction", "cache_aware_compaction = true", func(c *Config) bool { return c.Agent.CacheAwareCompaction }},
	{"context_rescue", "context_rescue = true", func(c *Config) bool { return c.Agent.ContextRescue }},
	{"visible_window_tokens", "visible_window_tokens = 16000", func(c *Config) bool { return c.Agent.VisibleWindowTokens == 16000 }},
}

// The rollback guard. A key the renderer omits is dropped by the next config
// save, and because these decode to their defaults when absent, an operator's
// explicit `false` reverted to enabled — silently, after any unrelated edit
// (`config currency`, /effort, a desktop settings save).
func TestAgentCacheKeysSurviveAConfigEdit(t *testing.T) {
	for _, tc := range agentCacheKeyCases {
		t.Run(tc.key, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			if err := os.WriteFile(path, []byte("[agent]\n"+tc.line+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("REASONIX_HOME", dir)

			cfg := LoadForEdit(path)
			if !tc.check(cfg) {
				t.Fatalf("setup: %s was not honoured at load, so the round trip proves nothing", tc.key)
			}
			// Any unrelated edit: this is the step that used to drop the key.
			if err := cfg.SetDisplayCurrency("USD"); err != nil {
				t.Fatal(err)
			}
			if err := cfg.SaveTo(path); err != nil {
				t.Fatal(err)
			}
			if !tc.check(LoadForEdit(path)) {
				t.Fatalf("%s did not survive a config edit: the rollback is not durable", tc.key)
			}
		})
	}
}

// The other half of the contract: a config that never mentions these keys must
// render without them. Materializing a default would turn "unset" into an
// explicit value, which is a behaviour change for every existing config.
func TestUnsetAgentCacheKeysStayAbsent(t *testing.T) {
	cfg := Default()
	bodies := map[string]string{
		"full":          RenderTOML(cfg),
		"user":          RenderTOMLForScope(cfg, RenderScopeUser),
		"project":       RenderTOMLForScope(cfg, RenderScopeProject),
		"project delta": RenderTOMLProjectDelta(cfg),
	}
	for scope, body := range bodies {
		for _, tc := range agentCacheKeyCases {
			if strings.Contains(body, tc.key) {
				t.Errorf("%s render materialized an unset %s", scope, tc.key)
			}
		}
	}
}

// The keys are read back as the values that were written, in every render scope
// the config layer persists through.
func TestAgentCacheKeysRoundTripInEveryScope(t *testing.T) {
	on := true
	cfg := Default()
	cfg.Agent.LowYieldLatch = &on
	cfg.Agent.ShapeDiagnosis = &on
	cfg.Agent.CacheAwareCompaction = true
	cfg.Agent.ContextRescue = true
	cfg.Agent.VisibleWindowTokens = 16_000

	for scope, body := range map[string]string{
		"full":          RenderTOMLForScope(cfg, RenderScopeFull),
		"user":          RenderTOMLForScope(cfg, RenderScopeUser),
		"project delta": RenderTOMLProjectDelta(cfg),
	} {
		got := Default()
		if _, err := toml.Decode(body, got); err != nil {
			t.Fatalf("%s render does not parse: %v", scope, err)
		}
		if !got.LowYieldLatchEnabled() || !got.ShapeDiagnosisEnabled() || !got.Agent.CacheAwareCompaction ||
			!got.Agent.ContextRescue || got.Agent.VisibleWindowTokens != 16_000 {
			t.Errorf("%s render round trip = %+v", scope, got.Agent)
		}
	}
}
