package config

import (
	"strings"
)

func (s MCPConfigSource) UserAuthorized() bool {
	switch s {
	case MCPSourceUserConfig, MCPSourceLegacyUser, MCPSourcePluginPackage,
		MCPSourceProjectConfig, MCPSourceProjectMCPJSON:
		return true
	default:
		return false
	}
}

// ProjectScoped reports whether an MCP entry belongs to one workspace. Project
// scope remains useful for provenance, activation, and relative-path handling;
// it no longer implies a separate launch-approval workflow.
func (s MCPConfigSource) ProjectScoped() bool {
	return s == MCPSourceProjectConfig || s == MCPSourceProjectMCPJSON
}

func (e PluginEntry) ShouldAutoStart() bool {
	return e.AutoStart == nil || *e.AutoStart
}

// ResolvedTier returns the normalized tier ("eager"|"background") with the
// project default applied. Legacy lazy and unknown values fall back to
// background so enabled MCPs are available without manual connection.
//
// Tier no longer changes runtime process start timing; it remains for config
// compatibility and diagnostics only.
func (e PluginEntry) ResolvedTier() string {
	return resolvedMCPTier(e.Tier)
}

func resolvedMCPTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "eager":
		return "eager"
	case "background", "lazy":
		return "background"
	case "":
		return "background"
	default:
		return "background"
	}
}

// AutoStartPlugins returns enabled MCP entries for the catalog. Durable
// enable/disable overrides in mcp-activation.json take precedence over the
// legacy auto_start field. auto_start=false without an override still maps to
// disabled; true/nil map to enabled. "Auto start" no longer means "spawn the
// process at session boot" — enabled servers register cached tools and start
// on first real tool call.
func (c *Config) AutoStartPlugins() []PluginEntry {
	return c.EnabledPlugins("", DefaultMCPActivationStore())
}

// EnabledPlugins returns catalog-enabled MCP entries for workspace, consulting
// the activation store when provided.
func (c *Config) EnabledPlugins(workspace string, activation *MCPActivationStore) []PluginEntry {
	if c == nil {
		return nil
	}
	out := make([]PluginEntry, 0, len(c.Plugins))
	for _, p := range c.Plugins {
		enabled := p.ShouldAutoStart()
		if activation != nil {
			if resolved, err := activation.IsEnabled(p, workspace); err == nil {
				enabled = resolved
			}
		}
		if enabled {
			out = append(out, p)
		}
	}
	return out
}
