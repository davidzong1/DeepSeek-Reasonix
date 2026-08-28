package boot

import "reasonix/internal/tool"

// applyUnifiedProviderToolSurface restricts Schemas/ContractEntries to the
// shared core, host-control tools, and provider-visible host additions.
func applyUnifiedProviderToolSurface(reg *tool.Registry, extra ...tool.Tool) {
	if reg == nil {
		return
	}
	allow := make([]string, 0, 16)
	for _, name := range UnifiedProviderToolNames() {
		if _, ok := reg.Get(name); ok {
			allow = append(allow, name)
		}
	}
	for _, candidate := range extra {
		if candidate != nil && reg.ProviderVisible(candidate.Name()) {
			allow = append(allow, candidate.Name())
		}
	}
	if len(allow) == 0 {
		if _, ok := reg.Get("use_capability"); ok {
			allow = []string{"use_capability"}
		}
	}
	reg.SetProviderVisibleTools(allow)
}
