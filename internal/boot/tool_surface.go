package boot

import (
	"strings"

	"reasonix/internal/config"
	"reasonix/internal/tool"
)

// AtomicReadToolName and AtomicWriteToolName are the provider-visible names of
// the team-session atomic file pair.
const (
	AtomicReadToolName  = "atomic_read"
	AtomicWriteToolName = "atomic_write"
)

// atomicFSSubstitutions maps each legacy file tool to the atomic tool that
// replaces it on an atomic FS surface. The substitution is applied only when the
// replacement is actually selectable, so a build that never registered the pair
// keeps the legacy trio instead of losing file access entirely.
var atomicFSSubstitutions = map[string]string{
	"read_file":  AtomicReadToolName,
	"write_file": AtomicWriteToolName,
	"edit_file":  AtomicWriteToolName,
}

// applyUnifiedProviderToolSurface restricts Schemas/ContractEntries to the
// shared core, host-control tools, provider-visible host additions, and the
// host's own allowlist.
//
// visible, when non-nil, narrows the unified part of the surface to the names
// it lists; tools passed in extra are host-contributed and stay, because the
// host already controls them by choosing whether to contribute them. A name in
// visible that the unified surface does not carry survives only when it is a
// compile-time built-in this boot registered — an MCP server, a plugin package,
// or any other dynamic tool can never be revealed by naming it here. Nothing is
// added to the registry: every selectable name is already registered, and
// therefore already reachable through use_capability and replay. The host
// changes which schemas the provider sees, never what this runtime executes.
// Nil keeps today's behavior byte-for-byte.
func applyUnifiedProviderToolSurface(reg *tool.Registry, extra []tool.Tool, visible []string) {
	if reg == nil {
		return
	}
	allow := make([]string, 0, 16)
	names := coreProviderToolNamesForRegistry(reg)
	names = append(names, HostControlToolNames()...)
	if visible != nil {
		names = selectNames(reg, names, visible)
	}
	for _, name := range names {
		if _, ok := reg.Get(name); ok {
			allow = append(allow, name)
		}
	}
	for _, candidate := range extra {
		if candidate != nil && reg.ProviderVisible(candidate.Name()) {
			allow = append(allow, candidate.Name())
		}
	}
	allow = dropSubstitutedLegacy(allow)
	if len(allow) == 0 {
		if _, ok := reg.Get("use_capability"); ok {
			allow = []string{"use_capability"}
		}
	}
	reg.SetProviderVisibleTools(allow)
}

// selectNames keeps the names of unified that the allowlist names or that it
// names as a registered compile-time built-in (the atomic FS pair replacing
// read_file/write_file/edit_file). Order follows the allowlist, so a host's list
// is what the provider surface is ordered by; duplicates collapse.
func selectNames(reg *tool.Registry, unified, allowlist []string) []string {
	selectable := make(map[string]bool, len(unified)+len(allowlist))
	for _, name := range unified {
		selectable[name] = true
	}
	for _, name := range allowlist {
		if _, isBuiltin := tool.LookupBuiltin(name); !isBuiltin {
			continue
		}
		if _, registered := reg.Get(name); registered {
			selectable[name] = true
		}
	}
	out := make([]string, 0, len(allowlist))
	seen := make(map[string]bool, len(allowlist))
	for _, name := range allowlist {
		if !selectable[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// dropSubstitutedLegacy removes a legacy file tool once its atomic replacement
// is on the surface. Both sides must be present for the swap, so a build whose
// registry never carried the pair (a host that passed its own ExtraTools, or a
// build predating it) leaves the trio exactly where it was.
func dropSubstitutedLegacy(allow []string) []string {
	present := make(map[string]bool, len(allow))
	for _, name := range allow {
		present[name] = true
	}
	kept := allow[:0]
	for _, name := range allow {
		if replacement, ok := atomicFSSubstitutions[name]; ok && present[replacement] {
			continue
		}
		kept = append(kept, name)
	}
	return kept
}

// providerVisibleTools resolves the provider-visible allowlist for one boot: an
// explicit Options.ProviderVisibleTools wins outright, because a host that
// states its own surface has already decided. Otherwise the configured
// tools.atomic_fs mode decides — the team role a build carries is what makes it
// a team build, so the CLI, each member's TUI backend, ACP, and the desktop all
// derive the same answer without re-implementing the swap.
func providerVisibleTools(opts Options, cfg *config.Config) []string {
	if opts.ProviderVisibleTools != nil {
		return opts.ProviderVisibleTools
	}
	return atomicFSSurface(cfg, isTeamBuild(opts))
}

// isTeamBuild reports whether this boot assembles a team session backend. The
// team role is set by the team builder and nowhere else, so it is the one
// signal that distinguishes a member/leader backend from an ordinary session.
func isTeamBuild(opts Options) bool {
	return strings.TrimSpace(opts.TeamRole) != ""
}

// atomicFSSurface returns the provider-visible allowlist for an atomic FS
// surface — the unified surface plus the atomic pair — or nil when the
// configured mode leaves this build's surface alone (the default). The legacy
// trio is not removed here: applyUnifiedProviderToolSurface substitutes it only
// once the pair is known to be registered.
func atomicFSSurface(cfg *config.Config, team bool) []string {
	if cfg == nil || !cfg.AtomicFSSurfaceEnabled(team) {
		return nil
	}
	names := UnifiedProviderToolNames()
	return append(append(make([]string, 0, len(names)+2), names...), AtomicReadToolName, AtomicWriteToolName)
}
