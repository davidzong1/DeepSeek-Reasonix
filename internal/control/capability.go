package control

import (
	"context"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/capability"
	"reasonix/internal/config"
	"reasonix/internal/plugin"
	"reasonix/internal/tool"
)

func (c *Controller) withCapabilityRoute(ctx context.Context, composed, routeInput string) string {
	if c == nil {
		return composed
	}
	routeInput = strings.TrimSpace(agent.StripTransientUserBlocks(routeInput))
	if routeInput == "" {
		routeInput = strings.TrimSpace(agent.StripTransientUserBlocks(composed))
	}
	if routeInput == "" {
		return composed
	}
	decision := c.routeCapabilities(ctx, routeInput)
	// Pass structured decision to the agent via ledger — never re-parse the prompt.
	if c.executor != nil {
		c.executor.SeedCapabilityRoute(decision)
	}
	// Dual-model Planner also consumes the route through the user turn; seed
	// its ledger when the runner exposes a planner agent.
	if c.runner != nil {
		if coord, ok := c.runner.(interface{ PlannerAgent() *agent.Agent }); ok {
			if p := coord.PlannerAgent(); p != nil {
				p.SeedCapabilityRoute(decision)
			}
		}
	}
	block := capability.RenderTransientBlock(decision)
	if block == "" {
		return composed
	}
	return block + "\n\n" + composed
}

// semanticRoutingApplies gates the model call routing makes to disambiguate a
// tie. A strong match already decided, and one candidate needs no ranking.
func semanticRoutingApplies(decision capability.RouteDecision) bool {
	return !hasStrongPolicy(decision) && len(decision.Candidates) > 1
}

// semanticDiscoveryApplies gates the other model call: the one made when the
// deterministic pass matched nothing at all. It requires a multi-target request
// so discovery cannot run on ordinary prose — that is what bounds the cost, and
// it is the only reason a request that matched nothing can be found at all.
func semanticDiscoveryApplies(decision capability.RouteDecision, routeInput string) bool {
	return !hasStrongPolicy(decision) &&
		len(decision.Candidates) == 0 &&
		capability.LooksMultiTarget(routeInput)
}

func hasStrongPolicy(decision capability.RouteDecision) bool {
	for _, cand := range decision.Candidates {
		if cand.Policy == capability.AutoUseRequire || cand.Policy == capability.AutoUsePrefer {
			return true
		}
	}
	return false
}

func (c *Controller) routeCapabilities(ctx context.Context, routeInput string) capability.RouteDecision {
	if ctx == nil {
		ctx = context.Background()
	}
	// The catalog must reflect every registered tool, not just the provider-visible
	// surface: optional tools (skills, subagents, …) stay off that surface but are
	// reachable through use_capability, so their readiness decides the route.
	tools := c.AllToolContractEntries()
	// Deterministic routing is first. The semantic router runs only when that
	// catalog match is itself ambiguous — never as a per-turn classification.
	var proxyTools map[string][]plugin.CachedTool
	if c.proxyToolsFn != nil {
		proxyTools = c.proxyToolsFn()
	}
	if proxyTools == nil {
		if reg := c.mcp.registry(); reg != nil {
			if t, ok := reg.Get("use_capability"); ok {
				if p, ok := t.(interface {
					ConnectedProxyTools() map[string][]plugin.CachedTool
				}); ok {
					proxyTools = p.ConnectedProxyTools()
				}
			}
		}
	}
	opts := capability.CatalogOptions{
		Tools:  tools,
		Skills: c.Skills(),
		// The router may suggest a tool the provider schema hides, which is the
		// only way a delegation tool reaches the model without being asked for.
		// The tool surface itself stays Tools, above.
		RoutableTools: c.routableToolEntries(),
	}
	if c.capabilityRuntime != nil {
		opts.Plugins, opts.CachedTools, opts.CacheKeyOK, opts.Disabled, proxyTools = c.capabilityRuntime.CapabilityCatalogState()
	} else if c.pluginCfg != nil {
		opts.Plugins = c.pluginCfg
		opts.CachedTools = c.capCachedTools
		opts.CacheKeyOK = c.capCacheKeyOK
	}
	// Cached MCP tool schemas (loaded once in WireCapabilityRouting) let
	// auto_start=false servers contribute concrete mcp-tool candidates to
	// deterministic and semantic routing before any connection exists.
	opts.ProxyTools = proxyTools
	if h := c.Host(); h != nil {
		opts.Connected = map[string]bool{}
		for _, n := range h.ServerNames() {
			opts.Connected[n] = true
		}
		opts.Failed = map[string]string{}
		for _, f := range h.Failures() {
			opts.Failed[f.Name] = f.Error
		}
	}
	catalog := capability.BuildCatalog(opts)
	decision := capability.Route(routeInput, catalog.Entries)
	if c.capabilityProxy {
		decision.CapabilityProxy = true
	}

	if (semanticRoutingApplies(decision) || semanticDiscoveryApplies(decision, routeInput)) && c.semanticRouter != nil {
		before := len(decision.Candidates)
		decision = c.semanticRouter.RouteSemantic(ctx, routeInput, catalog, decision)
		if c.capabilityProxy {
			decision.CapabilityProxy = true
		}
		if c.capabilityAudit != nil {
			c.capabilityAudit.RecordRoute(true, len(decision.Candidates) == before)
		}
	} else if c.capabilityAudit != nil {
		c.capabilityAudit.RecordRoute(false, false)
	}
	if c.capabilityAudit != nil {
		c.capabilityAudit.RecordDecision(decision)
	}
	return decision
}

// WireCapabilityRouting attaches hybrid routing helpers. Safe to call with nil
// semantic router (deterministic only). specs are the boot-converted plugin
// specs; their persisted schema caches are loaded once here so every routing
// turn can offer cached tools of not-yet-started servers.
func (c *Controller) WireCapabilityRouting(plugins []config.PluginEntry, specs []plugin.Spec, router *capability.SemanticRouter, audit *capability.Audit) {
	if c == nil {
		return
	}
	c.pluginCfg = append([]config.PluginEntry(nil), plugins...)
	c.capCachedTools, c.capCacheKeyOK = capability.LoadCachedToolsForSpecs(specs, c.mcpHostProfile())
	c.semanticRouter = router
	c.capabilityAudit = audit
}

// SetCapabilityProxyRouting directs unready MCP route candidates to
// use_capability instead of connect_tool_source. Used by closed-loop routes and
// dual-model Planner boots.
func (c *Controller) SetCapabilityProxyRouting(v bool) {
	if c == nil {
		return
	}
	c.capabilityProxy = v
}

// SetCapabilityProxyTools registers a getter for live tools observed through
// use_capability without entering the provider-visible registry.
func (c *Controller) SetCapabilityProxyTools(fn func() map[string][]plugin.CachedTool) {
	if c == nil {
		return
	}
	c.proxyToolsFn = fn
}

// RoutableToolContractEntries is the diagnostic view of the hidden, routable
// tools the capability router may suggest (see routableToolEntries).
func (c *Controller) RoutableToolContractEntries() []tool.ContractEntry {
	return c.routableToolEntries()
}

// routableToolEntries returns the hidden tools that declared routing triggers.
// A provider-visible tool is already in the router's catalog, so it is excluded
// here rather than entered twice.
func (c *Controller) routableToolEntries() []tool.ContractEntry {
	if c == nil {
		return nil
	}
	reg := c.mcp.registry()
	if reg == nil {
		return nil
	}
	visible := map[string]bool{}
	for _, e := range reg.ContractEntries() {
		visible[e.Name] = true
	}
	var out []tool.ContractEntry
	for _, e := range reg.AllContractEntries() {
		if !visible[e.Name] && len(e.Triggers) > 0 {
			out = append(out, e)
		}
	}
	return out
}
