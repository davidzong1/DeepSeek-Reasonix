package config

import (
	"reasonix/internal/provider"
	"slices"
	"strings"
)

// ProviderEntry declares a model provider instance. ContextWindow is the model's
// token budget; the harness compacts older history as a turn's prompt approaches
// it (see agent compaction). 0 disables compaction for the instance.
type ProviderEntry struct {
	DisplayName   string            `toml:"display_name,omitempty"` // UI label; Name remains the stable routing identity.
	Name          string            `toml:"name"`
	Kind          string            `toml:"kind"`
	BaseURL       string            `toml:"base_url"`
	ChatURL       string            `toml:"chat_url"`    // legacy OpenAI chat endpoint override; retained with its historical semantics
	RequestURL    string            `toml:"request_url"` // exact provider request URL written by current settings UI
	Model         string            `toml:"model"`       // a single model (back-compat)
	Models        []string          `toml:"models"`      // a vendor's model list (one base_url/key, many models)
	ModelsURL     string            `toml:"models_url"`  // auto-fetch models from this URL on startup
	Default       string            `toml:"default"`     // default model when Models is set (else Models[0])
	APIKeyEnv     string            `toml:"api_key_env"`
	PresetID      string            `toml:"preset_id"`      // curated preset identity; UI-only metadata, not sent to model providers.
	PresetVersion int               `toml:"preset_version"` // curated preset schema version for future migrations.
	Headers       map[string]string `toml:"headers"`        // optional extra HTTP headers for compatible gateways; secrets should stay in api_key_env.
	ExtraBody     map[string]any    `toml:"extra_body"`     // optional extra top-level JSON request body fields for OpenAI-compatible gateways.
	AuthHeader    bool              `toml:"auth_header"`    // for Anthropic-compatible gateways that expect Authorization: Bearer instead of x-api-key.
	// ResponsesMode selects the Responses API context strategy. Empty preserves
	// vendor detection; DeepSeek is stateless while compatible endpoints may use
	// stateful previous_response_id continuation.
	ResponsesMode string `toml:"responses_mode"`
	// ResponsesStateful is the legacy boolean form retained for config
	// compatibility. ResponsesMode wins when both are present.
	ResponsesStateful  *bool `toml:"responses_stateful"`
	resolvedAPIKey     string
	credentialsFrozen  bool
	credentialProxyURL string // runtime-only loopback transport, never persisted
	resolvedSource     CredentialSource
	BalanceURL         string `toml:"balance_url"` // optional; a provider-specific wallet-balance endpoint (DeepSeek: https://api.deepseek.com/user/balance). Empty = no balance readout.
	ContextWindow      int    `toml:"context_window"`
	// MaxOutputTokens is a protocol-neutral output budget for one turn. Zero
	// omits the field where the server allows; positive is an explicit cost
	// cap; negative omits optional wire limits. Never feeds compact_ratio.
	MaxOutputTokens int                          `toml:"max_output_tokens"`
	Price           *provider.Pricing            `toml:"price"`  // legacy/provider-wide fallback
	Prices          map[string]*provider.Pricing `toml:"prices"` // optional per-model prices; keys are model ids
	// BillingCurrency is the frozen list-price currency (ISO-4217). Independent
	// of [billing].display_currency; switching display never rewrites this.
	BillingCurrency string `toml:"billing_currency"`
	// BillingMode is payg (default) or subscription_equivalent (e.g. MiMo Token Plan).
	BillingMode string `toml:"billing_mode"`

	persistedOfficialCurrency string

	// Thinking / Effort are provider-kind knobs forwarded via Config.Extra:
	// Anthropic reads Thinking="adaptive" and Effort (low..max); the
	// openai-compatible provider forwards Effort as reasoning_effort.
	Thinking string `toml:"thinking"`
	Effort   string `toml:"effort"`
	// AnthropicBeta is the anthropic-beta request header value sent to
	// Anthropic-compatible endpoints, enabling features like 1M context
	// ("context-1m-2025-08-07"). Empty (the default) omits the header.
	AnthropicBeta string `toml:"anthropic_beta"`
	// Vision marks the model as accepting image input (image_url for
	// openai-kind, base64 blocks for anthropic). Off by default: text-only
	// models 400 on images, and image tokens are heavy.
	Vision bool `toml:"vision"`
	// VisionModels is legacy; new settings use model-level ModelOverrides.Vision.
	// Keep this field readable for existing configurations.
	VisionModels []string `toml:"vision_models"`
	// VisionDetail sets the openai image_url detail hint (low|high); empty = auto
	// (the field is omitted). "low" caps an image to a fixed ~85 tokens for cheap
	// coarse reads; ignored by providers without the knob (e.g. anthropic).
	VisionDetail string `toml:"vision_detail"`
	// WebSearch enables independent search with this account. Nil uses the
	// official DeepSeek default; explicit values and legacy native search
	// history survive config rewrites.
	WebSearch *bool `toml:"web_search"`
	// ReasoningProtocol selects the request shape for OpenAI-compatible
	// reasoning models. Empty/auto uses the capability registry plus
	// endpoint heuristics; none disables automatic reasoning controls.
	ReasoningProtocol string `toml:"reasoning_protocol"`
	// SupportedEfforts lists the /effort levels this provider exposes,
	// overriding built-in Kind/BaseURL defaults except fixed Kimi K3.
	// "auto" is the implicit prefix -- always accepted.
	SupportedEfforts []string `toml:"supported_efforts"`
	// DefaultEffort is the /effort level used when the user picks "auto" or
	// has not set Effort. Ignored for empty SupportedEfforts or fixed Kimi K3.
	DefaultEffort string `toml:"default_effort"`
	// ModelOverrides customizes capability metadata after ResolveModel picks
	// a concrete model: use it when a gateway exposes mixed reasoning or
	// vision models under one base_url/key.
	ModelOverrides map[string]ProviderModelOverride `toml:"model_overrides"`
	visionOverride *bool
	// NoProxy reaches this provider's base_url directly, never through the proxy.
	// For China-only endpoints a foreign-exit proxy resets the TLS handshake (#2803).
	NoProxy bool `toml:"no_proxy"`
	// CacheTTLMinutes overrides the vendor-default prefix-cache retention used by
	// cold-resume prune. Zero uses the vendor default (DeepSeek/unknown 24h, DashScope/Anthropic 5m).
	CacheTTLMinutes int `toml:"cache_ttl_minutes"`
}

type ProviderModelOverride struct {
	ReasoningProtocol string   `toml:"reasoning_protocol"`
	SupportedEfforts  []string `toml:"supported_efforts"`
	DefaultEffort     string   `toml:"default_effort"`
	Vision            *bool    `toml:"vision"`
	// ContextWindow overrides the provider-wide context budget for this model.
	// Zero inherits ProviderEntry.ContextWindow so existing configurations keep
	// their current compaction behavior.
	ContextWindow int `toml:"context_window"`
	// MaxOutputTokens overrides the provider-wide output budget. Zero inherits;
	// positive values set a cap and negative values omit optional wire limits.
	MaxOutputTokens int `toml:"max_output_tokens"`
}

// ModelList returns the models this provider exposes: the explicit `models` list,
// or the single `model` as a one-element list (back-compat). Empty if neither set.
func (e *ProviderEntry) ModelList() []string {
	if len(e.Models) > 0 {
		return e.Models
	}
	if e.Model != "" {
		return []string{e.Model}
	}
	return nil
}

// IsLikelyChatModel reports whether a model ID looks like a chat/completion
// model rather than a specialised audio/vision/embedding model. It applies a
// conservative name-based heuristic — the OpenAI-compatible /models API does
// not return capability/modality metadata, so this is the most reliable
// fallback until providers add such fields.
//
// The heuristic works in two passes:
//  1. Multi-word substring check for compound terms that span separators
//     (e.g. "text-embedding", "text-to-speech").
//  2. Token-level check: the model ID is split on common separators (- _ . / :)
//     and each token is compared against a set of known non-chat keywords.
//
// "voice" is intentionally absent from the non-chat set because it is too
// broad — legitimate future chat models may include it in their name.
func IsLikelyChatModel(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	lower := strings.ToLower(model)

	// Pass 1: compound terms that span separator boundaries.
	var compoundNonChat = []string{
		"text-embedding", "text-to-speech", "speech-to-text",
	}
	for _, c := range compoundNonChat {
		if strings.Contains(lower, c) {
			return false
		}
	}

	// Pass 2: token-level check.
	tokens := strings.FieldsFunc(lower, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == '/' || r == ':'
	})
	var nonChatTokens = map[string]bool{
		"asr": true, "stt": true, "tts": true,
		"whisper": true, "embedding": true,
		"moderation": true, "rerank": true, "dall": true,
		"transcription": true,
	}
	for _, tok := range tokens {
		if nonChatTokens[tok] {
			return false
		}
	}
	return true
}

// ChatModelList returns ModelList filtered to likely chat/completion models.
// Non-chat models (TTS, STT, ASR, embedding, etc.) are excluded so they do
// not appear in the chat model picker. Use ModelList() only when the full
// raw provider model list is needed, such as config serialization, provider
// diagnostics, or model-fetch editing.
func (e *ProviderEntry) ChatModelList() []string {
	raw := e.ModelList()
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, m := range raw {
		if IsLikelyChatModel(m) {
			out = append(out, m)
		}
	}
	return out
}

// DefaultModel returns the provider's default model: the explicit `default`, else
// the first of ModelList.
func (e *ProviderEntry) DefaultModel() string {
	if e.Default != "" {
		return e.Default
	}
	if l := e.ModelList(); len(l) > 0 {
		return l[0]
	}
	return ""
}

// HasModel reports whether m is one of the provider's models.
func (e *ProviderEntry) HasModel(m string) bool {
	return slices.Contains(e.ModelList(), m)
}

// PriceForModel returns the configured per-1M-token price for model. Per-model
// prices win; the legacy provider-wide price is a fallback for older configs.
func (e *ProviderEntry) PriceForModel(model string) *provider.Pricing {
	if e == nil {
		return nil
	}
	if e.Prices != nil {
		if p := e.Prices[strings.TrimSpace(model)]; p != nil {
			return clonePricing(p)
		}
	}
	return clonePricing(e.Price)
}

func (e *ProviderEntry) applyModelPrice() {
	if e == nil {
		return
	}
	e.Price = e.PriceForModel(e.Model)
}

func (e *ProviderEntry) applyModelOverride() {
	if e == nil || len(e.ModelOverrides) == 0 {
		return
	}
	ov, ok := e.modelOverrideForModel(e.Model)
	if !ok {
		return
	}
	if ov.ReasoningProtocol != "" {
		e.ReasoningProtocol = ov.ReasoningProtocol
	}
	if ov.SupportedEfforts != nil {
		e.SupportedEfforts = append([]string(nil), ov.SupportedEfforts...)
	}
	if ov.DefaultEffort != "" || ov.SupportedEfforts != nil {
		e.DefaultEffort = ov.DefaultEffort
	}
	if ov.Vision != nil {
		e.visionOverride = ov.Vision
	}
	if ov.ContextWindow > 0 {
		e.ContextWindow = ov.ContextWindow
	}
	if ov.MaxOutputTokens != 0 {
		e.MaxOutputTokens = ov.MaxOutputTokens
	}
}

func (e *ProviderEntry) modelOverrideForModel(model string) (ProviderModelOverride, bool) {
	model = strings.TrimSpace(model)
	if e == nil || model == "" || len(e.ModelOverrides) == 0 {
		return ProviderModelOverride{}, false
	}
	if ov, ok := e.ModelOverrides[model]; ok {
		return ov, true
	}
	return ProviderModelOverride{}, false
}

func clonePricing(p *provider.Pricing) *provider.Pricing {
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

// ToolsConfig selects which built-in tools are enabled. Empty means all of them.
type ToolsConfig struct {
	Enabled                  []string             `toml:"enabled"`
	BashTimeoutSeconds       *int                 `toml:"bash_timeout_seconds"`
	MCPStartupTimeoutSeconds *int                 `toml:"mcp_startup_timeout_seconds"`
	MCPCallTimeoutSeconds    *int                 `toml:"mcp_call_timeout_seconds"`
	BackgroundJobs           BackgroundJobsConfig `toml:"background_jobs"`
	Search                   SearchConfig         `toml:"search"`
	Shell                    ShellConfig          `toml:"shell"`
}

const (
	defaultBashTimeoutSeconds             = 120
	defaultMCPStartupTimeoutSeconds       = 30
	defaultMCPCallTimeoutSeconds          = 300
	defaultBackgroundJobStalledWarningSec = 900
	maxBackgroundJobStalledWarningSec     = 86400
)

// BashTimeoutSeconds returns the foreground bash timeout in seconds. An omitted
// config keeps the historical 120s safety cap, explicit 0 disables the
// tool-local cap, and positive values set a custom cap. Negative values fall
// back to the default so a typo cannot silently remove the safety net.
func (c *Config) BashTimeoutSeconds() int {
	if c.Tools.BashTimeoutSeconds == nil || *c.Tools.BashTimeoutSeconds < 0 {
		return defaultBashTimeoutSeconds
	}
	return *c.Tools.BashTimeoutSeconds
}

// MCPCallTimeoutSeconds returns the default MCP JSON-RPC call timeout in
// seconds. Omitted, zero, and negative values keep the built-in safety cap so a
// hung MCP server cannot block a turn indefinitely.
func (c *Config) MCPCallTimeoutSeconds() int {
	if c.Tools.MCPCallTimeoutSeconds == nil || *c.Tools.MCPCallTimeoutSeconds <= 0 {
		return defaultMCPCallTimeoutSeconds
	}
	return *c.Tools.MCPCallTimeoutSeconds
}

// MCPStartupTimeoutSeconds returns the background initialize + tools/list
// safety cap. Omitted, zero, and negative values keep the built-in default so
// a slow but healthy MCP can outlive the short interactive wait without running
// indefinitely.
func (c *Config) MCPStartupTimeoutSeconds() int {
	if c.Tools.MCPStartupTimeoutSeconds == nil || *c.Tools.MCPStartupTimeoutSeconds <= 0 {
		return defaultMCPStartupTimeoutSeconds
	}
	return *c.Tools.MCPStartupTimeoutSeconds
}

// BackgroundJobsConfig tunes parent-created background jobs.
type BackgroundJobsConfig struct {
	StalledWarningSeconds *int `toml:"stalled_warning_seconds"`
}

// BackgroundJobStalledWarningSeconds returns the stalled warning threshold in
// seconds. Omitted/negative values keep the default, explicit 0 disables the
// notice, and oversized values clamp to one day so a typo cannot become
// effectively invisible.
func (c *Config) BackgroundJobStalledWarningSeconds() int {
	if c.Tools.BackgroundJobs.StalledWarningSeconds == nil || *c.Tools.BackgroundJobs.StalledWarningSeconds < 0 {
		return defaultBackgroundJobStalledWarningSec
	}
	if *c.Tools.BackgroundJobs.StalledWarningSeconds > maxBackgroundJobStalledWarningSec {
		return maxBackgroundJobStalledWarningSec
	}
	return *c.Tools.BackgroundJobs.StalledWarningSeconds
}

// SearchConfig tunes the grep tool's engine. Engine is "auto" (default — use
// ripgrep when it's on PATH, else the native Go scanner), "native" (always Go),
// or "rg" (require ripgrep; warn at startup and fall back to native if absent).
// RgPath optionally points at a specific ripgrep binary instead of a PATH lookup.
type SearchConfig struct {
	Engine string `toml:"engine"`
	RgPath string `toml:"rg_path"`
}

// ShellConfig chooses the interpreter the bash tool runs commands under. Prefer
// is "auto" (default — real bash when present, else PowerShell on Windows),
// "bash", or "powershell"/"pwsh" (force it; warn at startup and fall back to
// auto if absent). Path optionally points at a specific shell executable.
type ShellConfig struct {
	Prefer string `toml:"prefer"`
	Path   string `toml:"path"`
}

// PermissionsConfig declares the per-call permission policy (see
// internal/permission). Mode is the fallback decision for writer tools when no
// rule matches ("ask" | "allow" | "deny"; default "ask"); read-only tools always
// fall back to allow. Allow/Ask/Deny are rule lists of the form "ToolName" or
// "ToolName(glob)". Precedence: deny > ask > allow > fallback.
type PermissionsConfig struct {
	Mode             string   `toml:"mode"`
	Allow            []string `toml:"allow"`
	Ask              []string `toml:"ask"`
	Deny             []string `toml:"deny"`
	AllowDynamicBash bool     `toml:"allow_dynamic_bash"`
}

// MCPConfigSource records where a merged MCP entry came from. It is runtime
// provenance only and is never serialized back into TOML or .mcp.json.
type MCPConfigSource string

const (
	MCPSourceUnknown        MCPConfigSource = ""
	MCPSourceUserConfig     MCPConfigSource = "user_config"
	MCPSourceProjectConfig  MCPConfigSource = "project_config"
	MCPSourceProjectMCPJSON MCPConfigSource = "project_mcp_json"
	MCPSourceLegacyUser     MCPConfigSource = "legacy_user_config"
	MCPSourcePluginPackage  MCPConfigSource = "plugin_package"
)
