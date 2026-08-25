package boot

import (
	"fmt"
	"strings"

	"reasonix/internal/command"
	"reasonix/internal/config"
	"reasonix/internal/extension"
	"reasonix/internal/hook"
	"reasonix/internal/netclient"
	"reasonix/internal/provider"
	"reasonix/internal/skill"
	"reasonix/internal/tool"
)

// ReusedAssembly holds rediscovery-free inputs for narrow/no-op rebuilds.
// Populated on successful BuildRuntime so the next RebuildFrom can skip
// skill/command/hook rediscovery and snapshot re-freeze when the plan allows.
type ReusedAssembly struct {
	SystemPrompt            string
	Skills                  []skill.Skill
	Commands                []command.Command
	Hooks                   []hook.ResolvedHook
	Registry                *tool.Registry
	ImplicitSkillInvocation bool
}

// shouldReuseDiscovery reports whether rediscovery of skills/commands/hooks
// can be skipped for this rebuild plan. Provider-only and MCP-only rebuilds
// do not change skill/command/hook discovery either — only the live backend.
func shouldReuseDiscovery(plan *extension.RuntimePlan) bool {
	if plan == nil {
		return false
	}
	switch plan.Kind {
	case extension.SubgraphNone,
		extension.SubgraphInterceptorOnly,
		extension.SubgraphUIOnly,
		extension.SubgraphProviderOnly,
		extension.SubgraphMCPOnly:
		return true
	default:
		return false
	}
}

// shouldReuseSnapshot reports whether the previous RuntimeSnapshot body can be
// re-published with a new generation (provider-visible prefix unchanged).
func shouldReuseSnapshot(plan *extension.RuntimePlan) bool {
	if plan == nil {
		return false
	}
	return plan.IsNoOp() || !plan.MayChangePrefix()
}

// shouldSkipPromptStrategy is true when system_prompt.build must not re-run.
func shouldSkipPromptStrategy(plan *extension.RuntimePlan) bool {
	return shouldReuseSnapshot(plan)
}

// resolveVisionProvider resolves a named vision model to a live provider. It
// lives outside build() so the fallback wiring stays out of the assembly
// function's budget.
func resolveVisionProvider(resolver provider.Resolver, cfg *config.Config, proxy netclient.ProxySpec, ref string) (provider.Provider, error) {
	ve, ok := resolveOptionalEntry(resolver, cfg, strings.TrimSpace(ref))
	if !ok || ve == nil || strings.TrimSpace(ve.Model) == "" {
		return nil, fmt.Errorf("unknown vision model %q", ref)
	}
	return resolveProvider(resolver, cfg, proxy, provider.Selection{Ref: modelRefFromEntry(ve)})
}

// selectVisionModel picks the first vision-capable model on the current
// provider, or "" when none qualifies (text/vision model fallback).
func selectVisionModel(resolver provider.Resolver, cfg *config.Config, currentRef string) (string, bool) {
	current, ok := resolveOptionalEntry(resolver, cfg, strings.TrimSpace(currentRef))
	if !ok || current == nil {
		return "", false
	}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.Name != current.Name || !p.Configured() {
			continue
		}
		models := p.ModelList()
		ordered := make([]string, 0, len(models))
		if d := p.DefaultModel(); d != "" {
			ordered = append(ordered, d)
		}
		for _, model := range models {
			if model != "" && model != p.DefaultModel() {
				ordered = append(ordered, model)
			}
		}
		for _, model := range ordered {
			candidate, found := cfg.ResolveModel(p.Name + "/" + model)
			if found && candidate.Configured() && config.EffectiveVision(candidate) {
				return candidate.Name + "/" + candidate.Model, true
			}
		}
	}
	return "", false
}
