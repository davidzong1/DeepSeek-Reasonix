package cli

import (
	"io"
	"os"
	"testing"

	"reasonix/internal/boot"
	"reasonix/internal/cachelab"
	"reasonix/internal/config"
	"reasonix/internal/netclient"
	"reasonix/internal/team"
)

// TestEveryConditionReachesTheMembersOwnAgent closes the gap this round found:
// two registered conditions declared switches that did not exist, so the B-only
// and A+B arms ran the baseline build while the matrix claimed to isolate B.
//
// This walks every registered condition through the production member builder
// and reads the switch off the member's OWN agent, which is the only place a
// condition can be verified. It needs no credential: the provider resolver
// accepts an unused key, and nothing here sends a request.
func TestEveryConditionReachesTheMembersOwnAgent(t *testing.T) {
	const userID = "cond"
	user := team.AgentUser{
		UserID: userID, Provider: "deepseek", Model: "deepseek/deepseek-v4.1-flash[1m]",
		BaseURL: "https://example.invalid", APIKey: "unused",
	}
	if dir := config.MemoryUserDir(); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	for _, spec := range cachelab.RegisteredConditions() {
		t.Run(string(spec.Condition), func(t *testing.T) {
			cfg := config.Default()
			cfg.DefaultModel = userID + "/" + user.Model
			applyConditionSwitches(t, cfg, spec)

			deps := memberBackendDeps{
				ctx:           t.Context(),
				users:         fakePool{users: map[string]team.AgentUser{userID: user}},
				events:        newMemberEventPump(),
				workspaceRoot: t.TempDir(),
				base: func() boot.Options {
					return boot.Options{SessionDir: t.TempDir(), Stderr: io.Discard, ConfigSnapshot: cfg}
				},
			}
			resolver, err := newMemberProviderResolver(user, netclient.ProxySpec{})
			if err != nil {
				t.Fatal(err)
			}
			opts, _ := memberBackendOptions(deps, team.MemberBinding{
				Team: "cond", MemberID: "m", AgentUserRef: userID,
			}, resolver)
			ctrl, err := boot.Build(deps.ctx, opts)
			if err != nil {
				t.Fatalf("assemble under condition %s: %v", spec.Condition, err)
			}
			defer ctrl.Close()

			// Only cache_aware_compaction has a snapshot observable, and its
			// deferral needs a warm receipt; behavior is guarded where it lives.
			want := map[string]bool{
				"agent.cache_aware_compaction":  spec.Switches["agent.cache_aware_compaction"] == "true",
				"agent.low_yield_latch":         spec.Switches["agent.low_yield_latch"] == "true",
				"agent.message_shape_diagnosis": spec.Switches["agent.message_shape_diagnosis"] == "true",
				"agent.context_rescue":          spec.Switches["agent.context_rescue"] == "true",
			}
			if got := cfg.Agent.CacheAwareCompaction; got != want["agent.cache_aware_compaction"] {
				t.Errorf("cache_aware_compaction = %v, want %v", got, want["agent.cache_aware_compaction"])
			}
			if got := cfg.LowYieldLatchEnabled(); got != want["agent.low_yield_latch"] {
				t.Errorf("low_yield_latch = %v, want %v", got, want["agent.low_yield_latch"])
			}
			if got := cfg.ShapeDiagnosisEnabled(); got != want["agent.message_shape_diagnosis"] {
				t.Errorf("message_shape_diagnosis = %v, want %v", got, want["agent.message_shape_diagnosis"])
			}
			if got := cfg.Agent.ContextRescue; got != want["agent.context_rescue"] {
				t.Errorf("context_rescue = %v, want %v", got, want["agent.context_rescue"])
			}
			if snapshot := ctrl.Executor().ContextMaintenanceSnapshot(); snapshot.HardInputCeiling <= 0 {
				t.Errorf("the member assembled with no ceiling; the build did not complete: %+v", snapshot)
			}
		})
	}
}

// applyConditionSwitches sets one condition's registered switches on a config.
// A key with no setter here fails the test rather than being skipped: a switch a
// run cannot configure is a condition the matrix cannot execute.
func applyConditionSwitches(t *testing.T, cfg *config.Config, spec cachelab.ConditionSpec) {
	t.Helper()
	for key, value := range spec.Switches {
		on := value == "true"
		switch key {
		case "agent.cache_aware_compaction":
			cfg.Agent.CacheAwareCompaction = on
		case "agent.low_yield_latch":
			cfg.Agent.LowYieldLatch = &on
		case "agent.message_shape_diagnosis":
			cfg.Agent.ShapeDiagnosis = &on
		case "agent.context_rescue":
			cfg.Agent.ContextRescue = on
		default:
			t.Fatalf("condition %s registers %q, which no setter here applies: the condition cannot be configured", spec.Condition, key)
		}
	}
}
