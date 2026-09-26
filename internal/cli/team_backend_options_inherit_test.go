// Part B (TEAM_MEMBER_CACHE_ROOTCAUSE_3AGENT_EXECUTION_PLAN.zh-CN.md) §3 Agent B
// item 6: the two cache-shaping knobs must reach a Team member's OWN backend, not
// only the ambient chat's.
//
// The failure this guards against was observed once: agent.New assembled its
// agentConfig without copying visibleWindowTokens or cacheAwareCompaction, so
// both keys were dead in every build while boot kept passing them, so this test
// reads the knob off the member's live agent rather than off its options.
package cli

import (
	"io"
	"os"
	"testing"

	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/netclient"
	"reasonix/internal/skill"
	"reasonix/internal/team"
)

// TestMemberBackendInheritsTheCacheShapingKnobs walks the whole member chain:
// ambient config snapshot → memberBackendOptions → boot.Build → the member's own
// agent. The observable is HeadroomGoal, the verbatim tail a fold must keep,
// which is the consumed form of agent.visible_window_tokens: with the cap it is
// the configured value, without it 16% of the context window.
func TestMemberBackendInheritsTheCacheShapingKnobs(t *testing.T) {
	// The window cap only binds below 16% of the context window, so the member
	// is given a 1M window (the [1m] alias) and a cap well under its 160K uncapped
	// budget: with the cap the goal is 80000, without it 160000.
	const capped = 80_000
	user := team.AgentUser{
		UserID: "u", Provider: "openai", Model: "gpt-5.6[1m]",
		BaseURL: "https://example.invalid/v1", APIKey: "k",
	}
	cfg := config.Default()
	cfg.DefaultModel = user.UserID + "/" + user.Model
	cfg.Agent.VisibleWindowTokens = capped
	cfg.Agent.CacheAwareCompaction = true

	// A member's build-time write roots include the user state root, and boot
	// refuses a scope it cannot resolve, so the root a real host always has is
	// created here rather than left to chance.
	if dir := config.MemoryUserDir(); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	deps := memberBackendDeps{
		ctx:           t.Context(),
		users:         fakePool{users: map[string]team.AgentUser{user.UserID: user}},
		events:        newMemberEventPump(),
		workspaceRoot: t.TempDir(),
		base: func() boot.Options {
			return boot.Options{
				SessionDir: t.TempDir(),
				Stderr:     io.Discard,
				// The ambient session's snapshot: whatever the chat launched
				// with is what a member must inherit, including these two keys.
				ConfigSnapshot: cfg,
			}
		},
	}
	resolver, err := newMemberProviderResolver(user, netclient.ProxySpec{})
	if err != nil {
		t.Fatal(err)
	}
	opts, _ := memberBackendOptions(deps, team.MemberBinding{
		Team: "alpha", MemberID: "lead", Leader: false, AgentUserRef: user.UserID,
	}, resolver)
	if opts.TeamRole != skill.TeamRoleMember {
		t.Fatalf("TeamRole = %q, want the member role: this test is about a member backend", opts.TeamRole)
	}
	if opts.ConfigSnapshot != cfg {
		t.Fatal("the member must inherit the ambient config snapshot instead of reloading one")
	}

	ctrl, err := boot.Build(deps.ctx, opts)
	if err != nil {
		t.Fatalf("member boot: %v", err)
	}
	defer ctrl.Close()

	if got := ctrl.Executor().ContextMaintenanceSnapshot().HeadroomGoal; got != capped {
		t.Errorf("member HeadroomGoal = %d, want %d: agent.visible_window_tokens did not reach the member's agent", got, capped)
	}
}
