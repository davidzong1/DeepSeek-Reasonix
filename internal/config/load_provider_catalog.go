package config

import (
	"strings"
)

// normalizeLegacyProviderModels repairs provider entries written by older
// desktop builds that carried the official provider name/endpoint but omitted the
// model field. The repair is intentionally narrow: valid user-provided model
// lists are left untouched, while known official aliases get the model implied by
// their preset name so model pickers and provider validation have an option.
func normalizeLegacyProviderModels(c *Config) {
	if c == nil {
		return
	}
	for i := range c.Providers {
		p := &c.Providers[i]
		if providerHasAnyModel(*p) {
			continue
		}
		if model := legacyOfficialProviderModel(p.Name); model != "" {
			p.Model = model
		}
	}
}

const (
	legacyStepFunOpenAIBaseURL      = "https://api.stepfun.ai/step_plan/v1"
	officialStepFunOpenAIBaseURL    = "https://api.stepfun.com/step_plan/v1"
	legacyStepFunAnthropicBaseURL   = "https://api.stepfun.ai/step_plan"
	officialStepFunAnthropicBaseURL = "https://api.stepfun.com/step_plan"
)

func normalizeLegacyStepFunBaseURLs(c *Config) bool {
	// Both stepfun.ai (global) and stepfun.com (China) are official endpoints.
	// BaseURL is user-owned provider configuration, so neither runtime loading
	// nor an unrelated settings save may infer a region and rewrite it.
	return false
}

func normalizedBaseURLForMigration(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func normalizeLegacyLongCatContextWindows(c *Config) bool {
	if c == nil {
		return false
	}
	changed := false
	for i := range c.Providers {
		p := &c.Providers[i]
		if p.ContextWindow != legacyLongCat20ContextWindow {
			continue
		}
		var kind, baseURL string
		switch strings.TrimSpace(p.PresetID) {
		case "longcat-openai":
			kind, baseURL = "openai", longCatOpenAIBaseURL
		case "longcat-anthropic":
			kind, baseURL = "anthropic", longCatAnthropicBaseURL
		default:
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(p.Kind), kind) ||
			normalizedBaseURLForMigration(p.BaseURL) != baseURL ||
			!stringSlicesEqual(p.Models, longCat20Models) ||
			p.Model != "" ||
			p.Default != longCat20Models[0] {
			continue
		}
		p.ContextWindow = longCat20ContextWindow
		changed = true
	}
	return changed
}

// normalizeLegacyQwenContextWindows upgrades only installed official Qwen
// presets that still carry the old zero context window and untouched model
// catalog. Custom endpoints, catalogs, provider-wide windows, and existing
// per-model override values remain user-owned.
func normalizeLegacyQwenContextWindows(c *Config) bool {
	if c == nil {
		return false
	}
	changed := false
	for i := range c.Providers {
		p := &c.Providers[i]
		if p.ContextWindow != 0 {
			continue
		}
		presetID := qwenPresetIDForMigration(*p)
		if presetID == "" {
			continue
		}
		preset, ok := CuratedProviderPreset(presetID)
		if !ok || len(preset.Entries) != 1 {
			continue
		}
		canonical := preset.Entries[0]
		if !strings.EqualFold(strings.TrimSpace(p.Kind), strings.TrimSpace(canonical.Kind)) ||
			normalizedBaseURLForMigration(p.BaseURL) != normalizedBaseURLForMigration(canonical.BaseURL) ||
			!stringSlicesEqual(p.Models, canonical.Models) ||
			strings.TrimSpace(p.Model) != "" {
			continue
		}
		p.ContextWindow = canonical.ContextWindow
		mergeMissingQwenContextOverrides(p, canonical.ModelOverrides)
		changed = true
	}
	return changed
}

func qwenPresetIDForMigration(p ProviderEntry) string {
	presetID := strings.TrimSpace(p.PresetID)
	if presetID == "" {
		presetID = strings.TrimSpace(p.Name)
	}
	switch presetID {
	case "qwen-cn",
		"qwen-global",
		"qwen-coding-plan-cn",
		"qwen-coding-plan-cn-anthropic",
		"qwen-coding-plan-global",
		"qwen-coding-plan-global-anthropic":
		return presetID
	default:
		return ""
	}
}

func mergeMissingQwenContextOverrides(p *ProviderEntry, defaults map[string]ProviderModelOverride) {
	if p == nil || len(defaults) == 0 {
		return
	}
	if p.ModelOverrides == nil {
		p.ModelOverrides = make(map[string]ProviderModelOverride, len(defaults))
	}
	for defaultKey, defaultOverride := range defaults {
		overrideKey := defaultKey
		for key := range p.ModelOverrides {
			if strings.EqualFold(strings.TrimSpace(key), defaultKey) {
				overrideKey = key
				break
			}
		}
		override := p.ModelOverrides[overrideKey]
		if override.ContextWindow == 0 {
			override.ContextWindow = defaultOverride.ContextWindow
			p.ModelOverrides[overrideKey] = override
		}
	}
}

// normalizeLegacyKimiK3Catalog upgrades only untouched Kimi direct-API model
// catalogs on the official regional endpoints. Custom model lists, endpoints,
// defaults, credentials, and provider-wide settings remain user-owned.
func normalizeLegacyKimiK3Catalog(c *Config) bool {
	if c == nil {
		return false
	}
	changed := false
	for i := range c.Providers {
		p := &c.Providers[i]
		presetID := strings.TrimSpace(p.PresetID)
		name := strings.TrimSpace(p.Name)
		var baseURL string
		switch {
		case presetID == "kimi-cn" || (presetID == "" && name == "kimi-cn"):
			baseURL = "https://api.moonshot.cn/v1"
		case presetID == "kimi-global" || (presetID == "" && name == "kimi-global"):
			baseURL = "https://api.moonshot.ai/v1"
		default:
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(p.Kind), "openai") ||
			normalizedBaseURLForMigration(p.BaseURL) != baseURL ||
			!stringSlicesEqual(p.Models, legacyKimiAPIModels) ||
			strings.TrimSpace(p.Model) != "" {
			continue
		}
		p.Models = append([]string(nil), kimiAPIModels...)
		p.VisionModels = migrateKimiK3VisionModels(p.VisionModels, legacyKimiAPIModels)
		mergeMissingKimiK3Override(p, kimiK3DirectOverride())
		changed = true
	}
	return changed
}

// migrateKimiK3VisionModels preserves explicit provider-level vision choices.
// A nil list or an exact copy of the old preset list indicates that the user
// has not customized vision support and should receive Kimi K3's capability.
func migrateKimiK3VisionModels(current, legacy []string) []string {
	if current != nil && (legacy == nil || !stringSlicesEqual(current, legacy)) {
		return current
	}
	return mergeModelLists([]string{"kimi-k3"}, current)
}

func mergeMissingKimiK3Override(p *ProviderEntry, defaults ProviderModelOverride) {
	if p.ModelOverrides == nil {
		p.ModelOverrides = map[string]ProviderModelOverride{}
	}
	overrideKey := "kimi-k3"
	for key := range p.ModelOverrides {
		if strings.EqualFold(strings.TrimSpace(key), overrideKey) {
			overrideKey = key
			break
		}
	}
	kimiK3 := p.ModelOverrides[overrideKey]
	if strings.TrimSpace(kimiK3.ReasoningProtocol) == "" {
		kimiK3.ReasoningProtocol = defaults.ReasoningProtocol
	}
	if kimiK3.SupportedEfforts == nil {
		kimiK3.SupportedEfforts = append([]string(nil), defaults.SupportedEfforts...)
	}
	if strings.TrimSpace(kimiK3.DefaultEffort) == "" && containsString(normalizedEffortLevels(kimiK3.SupportedEfforts), defaults.DefaultEffort) {
		kimiK3.DefaultEffort = defaults.DefaultEffort
	}
	if kimiK3.ContextWindow <= 0 {
		kimiK3.ContextWindow = defaults.ContextWindow
	}
	p.ModelOverrides[overrideKey] = kimiK3
}

// normalizeLegacyOpenCodeGoKimiK3Catalog upgrades only the untouched model
// catalog from the original editable OpenCode Go preset. A user-curated model
// list or custom endpoint is left alone, while other provider edits (headers,
// key env, provider-wide context) survive the additive K3 capability update.
func normalizeLegacyOpenCodeGoKimiK3Catalog(c *Config) (changed bool) {
	if c == nil {
		return false
	}
	for i := range c.Providers {
		p := &c.Providers[i]
		presetID := strings.TrimSpace(p.PresetID)
		if (presetID != "opencode-go" && (presetID != "" || strings.TrimSpace(p.Name) != "opencode-go")) ||
			!strings.EqualFold(strings.TrimSpace(p.Kind), "openai") ||
			normalizedBaseURLForMigration(p.BaseURL) != "https://opencode.ai/zen/go/v1" ||
			!stringSlicesEqual(p.Models, legacyOpenCodeGoModels) ||
			strings.TrimSpace(p.Model) != "" {
			continue
		}
		p.Models = append([]string(nil), opencodeGoModels...)
		p.VisionModels = migrateKimiK3VisionModels(p.VisionModels, nil)
		mergeMissingKimiK3Override(p, ProviderModelOverride{
			ReasoningProtocol: ReasoningProtocolOpenAI,
			SupportedEfforts:  []string{"high", "max"},
			DefaultEffort:     "max",
			ContextWindow:     1_048_576,
		})
		changed = true
	}
	return changed
}

// normalizeLegacyOpenCodeGoVisionCatalog upgrades only the untouched Chat
// catalog that predates OpenCode Go's DeepSeek vision SKU. Custom model lists
// and explicit image-input choices remain user-owned.
func normalizeLegacyOpenCodeGoVisionCatalog(c *Config) (changed bool) {
	if c == nil {
		return false
	}
	for i := range c.Providers {
		p := &c.Providers[i]
		presetID := strings.TrimSpace(p.PresetID)
		if (presetID != "opencode-go" && (presetID != "" || strings.TrimSpace(p.Name) != "opencode-go")) ||
			!strings.EqualFold(strings.TrimSpace(p.Kind), "openai") ||
			normalizedBaseURLForMigration(p.BaseURL) != "https://opencode.ai/zen/go/v1" ||
			!stringSlicesEqual(p.Models, preVisionOpenCodeGoModels) ||
			strings.TrimSpace(p.Model) != "" {
			continue
		}
		p.Models = append([]string(nil), opencodeGoModels...)
		if p.VisionModels == nil || stringSlicesEqual(p.VisionModels, preVisionOpenCodeGoVisionModels) {
			p.VisionModels = append([]string(nil), opencodeGoVisionModels...)
		}
		mergeMissingOpenCodeGoVisionOverride(p)
		changed = true
	}
	return changed
}

func mergeMissingOpenCodeGoVisionOverride(p *ProviderEntry) {
	if p.ModelOverrides == nil {
		p.ModelOverrides = map[string]ProviderModelOverride{}
	}
	const model = "deepseek-v4-flash-vision-exp"
	key := model
	for candidate := range p.ModelOverrides {
		if strings.EqualFold(strings.TrimSpace(candidate), model) {
			key = candidate
			break
		}
	}
	override := p.ModelOverrides[key]
	if strings.TrimSpace(override.ReasoningProtocol) == "" {
		override.ReasoningProtocol = ReasoningProtocolDeepSeek
	}
	if override.SupportedEfforts == nil {
		override.SupportedEfforts = []string{"disabled", "low", "high", "max"}
	}
	if strings.TrimSpace(override.DefaultEffort) == "" && containsString(normalizedEffortLevels(override.SupportedEfforts), "high") {
		override.DefaultEffort = "high"
	}
	if override.ContextWindow <= 0 {
		override.ContextWindow = 1_000_000
	}
	p.ModelOverrides[key] = override
}

func normalizeLegacyMimoProviderCatalogs(c *Config) bool {
	if c == nil {
		return false
	}
	changed := false
	for i := range c.Providers {
		p := &c.Providers[i]
		if legacyMimoProviderName(p.Name) == "" || len(p.Models) > 0 {
			continue
		}
		switch officialProviderHost(p.BaseURL) {
		case "api.xiaomimimo.com":
			if applyLegacyMimoCatalog(p, legacyMimoAPIModels(), []string{"mimo-v2.5", "mimo-v2-omni"}, "mimo-v2.5-pro") {
				changed = true
			}
		case "token-plan-cn.xiaomimimo.com":
			if applyLegacyMimoCatalog(p, legacyMimoTokenPlanModels(), []string{"mimo-v2.5"}, "mimo-v2.5-pro") {
				changed = true
			}
		}
	}
	return changed
}
