// Package config loads Reasonix's runtime configuration from TOML. Resolution order:
// flag > project ./reasonix.toml > user config.toml (in the OS user-config dir) > built-in defaults.
// Secrets come from the environment via api_key_env and are never stored in
// config files.
package config

import (
	"errors"
	"fmt"
	"reasonix/internal/netclient"
	"regexp"
	"runtime"
	"strings"
)

var validSkillName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// IsValidSkillName reports whether name is a usable skill identifier.
func IsValidSkillName(name string) bool { return validSkillName.MatchString(name) }

// SkillNameKey normalizes a skill identifier for config comparisons.
func SkillNameKey(name string) string {
	name = strings.TrimSpace(name)
	if !IsValidSkillName(name) {
		return ""
	}
	if runtime.GOOS == "windows" {
		return strings.ToLower(name)
	}
	return name
}

// Config is Reasonix's runtime configuration.
type Config struct {
	ConfigVersion    int                 `toml:"config_version"`
	DefaultModel     string              `toml:"default_model"`
	Language         string              `toml:"language"` // ui/model language tag (e.g. "zh"); empty = auto-detect from $LANG / $REASONIX_LANG
	CredentialsStore string              `toml:"credentials_store"`
	UI               UIConfig            `toml:"ui"`
	CLI              CLIConfig           `toml:"cli"`
	Desktop          DesktopConfig       `toml:"desktop"`
	Billing          BillingConfig       `toml:"billing"`
	Telemetry        TelemetryConfig     `toml:"telemetry"`
	Notifications    NotificationsConfig `toml:"notifications"`
	Agent            AgentConfig         `toml:"agent"`
	Providers        []ProviderEntry     `toml:"providers"`
	Tools            ToolsConfig         `toml:"tools"`
	Checkpoints      CheckpointsConfig   `toml:"checkpoints"`
	Permissions      PermissionsConfig   `toml:"permissions"`
	Sandbox          SandboxConfig       `toml:"sandbox"`
	Network          NetworkConfig       `toml:"network"`
	Environment      EnvironmentConfig   `toml:"environment"`
	Plugins          []PluginEntry       `toml:"plugins"`
	Skills           SkillsConfig        `toml:"skills"`
	Statusline       StatuslineConfig    `toml:"statusline"`
	LSP              LSPConfig           `toml:"lsp"`
	Browser          BrowserConfig       `toml:"browser"`
	Bot              BotConfig           `toml:"bot"`
	Serve            ServeConfig         `toml:"serve"`
	Secrets          SecretsConfig       `toml:"secrets"`
	Remote           RemoteConfig        `toml:"remote"`

	systemPromptFileSource     promptFileSource
	providerSources            map[string]providerSourceScope
	shadowedProjectProviders   []ProviderEntry
	ignoredProjectDefaultModel string
	ignoredLegacyStepLimits    bool
	expansionEnv               map[string]string
	pluginPackageOwners        map[string]string
	pluginPackageSkillOwners   map[string][]string
	pluginPackageAgentOwners   map[string][]string
	// explicitProjectSkillKeys records project-level skill fields that the
	// settings UI intentionally owns even when their value equals the built-in
	// default. It is transient edit metadata and is never serialized directly.
	explicitProjectSkillKeys map[string]bool
	stagedModelCredentials   []string
	editLoadErr              error
	// loadWarnings are non-fatal issues observed while loading config (corrupt
	// user/project files recovered via last-known-good or defaults). They never
	// rewrite the original file; the UI may surface them for doctor repair.
	loadWarnings      []string
	openCodeGoJournal *openCodeGoJournal
}

// KeepProjectSkillKey marks a skill field as an intentional project override.
// An explicit empty/false project value must still be written so it can
// override a non-default user setting in the layered configuration.
func (c *Config) KeepProjectSkillKey(key string) error {
	key = strings.TrimSpace(key)
	switch key {
	case "paths", "excluded_paths", "disabled_skills", "disable_implicit_invocation", "max_depth":
	default:
		return fmt.Errorf("unknown project skill key %q", key)
	}
	if c.explicitProjectSkillKeys == nil {
		c.explicitProjectSkillKeys = make(map[string]bool)
	}
	c.explicitProjectSkillKeys[key] = true
	return nil
}

func (c *Config) keepsProjectSkillKey(key string) bool {
	return c != nil && c.explicitProjectSkillKeys[key]
}

type promptFileSource uint8

const (
	promptFileSourceUnknown promptFileSource = iota
	promptFileSourceUser
	promptFileSourceProject
)

type systemPromptFileError struct {
	configured string
	candidates []string
	errors     []error
	allMissing bool
}

func (e *systemPromptFileError) Error() string {
	detail := "could not be read from any configured location"
	if e.allMissing {
		detail = "not found at any configured location"
	}
	message := fmt.Sprintf("system_prompt_file %q %s: %s", e.configured, detail, strings.Join(e.candidates, ", "))
	if !e.allMissing && len(e.errors) > 0 {
		message += ": " + errors.Join(e.errors...).Error()
	}
	return message
}

func (e *systemPromptFileError) Unwrap() error { return errors.Join(e.errors...) }

// IsMissingSystemPromptFile reports whether every allowed location for a
// configured prompt file was absent. Permission, containment, and other I/O
// failures deliberately return false so callers do not start without an
// explicitly configured prompt.
func IsMissingSystemPromptFile(err error) bool {
	var target *systemPromptFileError
	return errors.As(err, &target) && target.allMissing
}

// TelemetryConfig controls content-free CLI usage metrics. It is user-global:
// project reasonix.toml values are ignored so a cloned repository cannot opt a
// user into reporting.
type TelemetryConfig struct {
	CLIMetrics string `toml:"cli_metrics"` // auto|on|off; empty means consent has not been requested
}

// CLITelemetryConfigured reports whether the user has made an explicit CLI
// telemetry choice. The runtime policy still treats an absent value as auto,
// but persistence must preserve absence until the first eligible consent prompt.
func (c *Config) CLITelemetryConfigured() bool {
	if c == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(c.Telemetry.CLIMetrics)) {
	case "auto", "on", "off":
		return true
	default:
		return false
	}
}

// CLITelemetryMode returns the normalized CLI telemetry policy.
func (c *Config) CLITelemetryMode() string {
	if c == nil {
		return "auto"
	}
	switch strings.ToLower(strings.TrimSpace(c.Telemetry.CLIMetrics)) {
	case "on":
		return "on"
	case "off":
		return "off"
	default:
		return "auto"
	}
}

// LoadWarnings returns non-fatal config load issues (corrupt files recovered in
// memory). The returned slice is a copy.
func (c *Config) LoadWarnings() []string {
	if c == nil || len(c.loadWarnings) == 0 {
		return nil
	}
	out := make([]string, len(c.loadWarnings))
	copy(out, c.loadWarnings)
	return out
}

// HasLoadWarnings reports whether the load used a degraded in-memory fallback.
func (c *Config) HasLoadWarnings() bool {
	return c != nil && len(c.loadWarnings) > 0
}

func (c *Config) addLoadWarning(msg string) {
	if c == nil {
		return
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	c.loadWarnings = append(c.loadWarnings, msg)
}

// IgnoredLegacyAgentStepLimits reports whether this load found and ignored the
// retired [agent].max_steps or planner_max_steps settings. Boot removes standard
// key assignments before loading, while read-only/config-only loads only report
// and normalize them in memory.
func (c *Config) IgnoredLegacyAgentStepLimits() bool {
	return c != nil && c.ignoredLegacyStepLimits
}

// IgnoredProjectDefaultModel returns the project reasonix.toml default_model
// that LoadForRoot ignored because no configured provider serves it (see
// restoreUnresolvableProjectDefaultModel), or "" when none was ignored.
func (c *Config) IgnoredProjectDefaultModel() string {
	if c == nil {
		return ""
	}
	return c.ignoredProjectDefaultModel
}

// SecretsConfig controls the credential protection layers. It is a user-global
// setting: project reasonix.toml values are ignored (see LoadForRoot), so a
// cloned repository cannot silently opt the user into workflow-breaking
// protections.
type SecretsConfig struct {
	// FilterSubprocessEnv strips credential-like variables (*_API_KEY,
	// *TOKEN*, *SECRET*) from tool subprocesses. Default off: it breaks
	// token-based workflows such as gh, HTTPS git push and npm publish.
	FilterSubprocessEnv bool `toml:"filter_subprocess_env"`
	// ProtectSensitiveFiles makes read/list/search treat credential paths
	// (.env, .git-credentials, .netrc, *.pem/*.key, ~/.ssh) as invisible.
	// Default off: hiding the files breaks legitimate edit-my-.env flows.
	ProtectSensitiveFiles bool `toml:"protect_sensitive_files"`
}

type providerSourceScope string

const (
	providerSourceUser    providerSourceScope = "user"
	providerSourceProject providerSourceScope = "project"
)

// Default returns the built-in default configuration.
func Default() *Config {
	return &Config{
		ConfigVersion:    10,
		DefaultModel:     "deepseek-flash",
		CredentialsStore: CredentialsStoreAuto,
		UI:               UIConfig{Theme: "auto", ShowTurnUsage: true},
		Desktop:          DesktopConfig{DefaultToolApprovalMode: "workspace-write", ConversationWidth: "standard"},
		Billing:          BillingConfig{},
		Notifications: NotificationsConfig{
			Enabled:         false,
			TurnDone:        true,
			ApprovalRequest: true,
			AskRequest:      true,
		},
		Agent: AgentConfig{
			SystemPrompt: DefaultSystemPrompt,
			// Normal interactive execution has no configurable total round cap. It
			// is bounded by adaptive progress guards and context compaction instead.
			MaxSteps:        0,
			PlannerMaxSteps: 0,
			AutoPlan:        "off",
			// Soft/snip/force are load-only compatibility; CompactRatio alone drives maintenance.
			SoftCompactRatio:       0,
			ToolResultSnipRatio:    0,
			CompactRatio:           0.80,
			CompactForceRatio:      0,
			ContextEditing:         "",
			MaxSubagentDepth:       2,
			MaxSubagentConcurrency: 6,
			MaxParallelWriters:     3,
		},
		// The policy fallback remains an internal rule-engine input. The active
		// PermissionPreset supplies the user-facing execution posture, while
		// explicit deny/ask/allow rules remain authoritative refinements.
		Permissions: PermissionsConfig{Mode: "ask"},
		// Restricted permission presets select the platform sandbox at runtime:
		// Seatbelt on macOS, bubblewrap on Linux, and the restricted-token helper
		// on Windows. Network=true preserves normal egress inside that boundary.
		Sandbox: SandboxConfig{Network: true},
		// LSP tools on by default, but dormant until a language server is on PATH;
		// a missing server yields an install hint rather than an error.
		LSP:     LSPConfig{Enabled: true},
		Network: NetworkConfig{ProxyMode: netclient.ModeAuto},
		Bot: BotConfig{
			ToolApprovalMode:   "workspace-write",
			MaxSteps:           0,
			DebounceMs:         1500,
			QueueMode:          "steer",
			QueueCap:           20,
			QueueDrop:          "summarize",
			IgnoreSelfMessages: true,
			Control:            BotControlConfig{Addr: "127.0.0.1:37913", TokenEnv: "REASONIX_BOT_CONTROL_TOKEN"},
			Pairing:            BotPairingConfig{Enabled: true, RequestTTLMinutes: 60, MaxPendingPerPlatform: 3},
			Allowlist:          BotAllowlist{Enabled: true},
			QQ:                 QQBotConfig{AppSecretEnv: "QQ_BOT_APP_SECRET"},
			Feishu:             FeishuBotConfig{Domain: "feishu", AppSecretEnv: "FEISHU_BOT_APP_SECRET", Mode: "webhook", WebhookPort: 8080, RequireMention: true},
			Dingtalk:           DingtalkBotConfig{RequireMention: true},
			Weixin:             WeixinBotConfig{AccountID: "default", TokenEnv: "WEIXIN_BOT_TOKEN", APIBase: "https://ilinkai.weixin.qq.com"},
		},
		// Main conversations use Chat Completions; independent web_search uses
		// the official Messages endpoint with the same account.
		Providers: []ProviderEntry{
			{
				Name: "deepseek-flash", Kind: "openai", BaseURL: "https://api.deepseek.com",
				Model: "deepseek-v4-flash", APIKeyEnv: "DEEPSEEK_API_KEY",
				BalanceURL: "https://api.deepseek.com/user/balance", Thinking: "enabled",
				WebSearch: boolPointer(true), AnthropicBeta: anthropicBetaContext1M, SupportedEfforts: []string{"disabled", "low", "high", "max"}, DefaultEffort: "high",
				ContextWindow: 1_000_000, Price: deepSeekV4FlashPriceUSD(),
				BillingCurrency: "USD", BillingMode: "payg",
			},
			{
				Name: "deepseek-pro", Kind: "openai", BaseURL: "https://api.deepseek.com",
				Model: "deepseek-v4-pro", APIKeyEnv: "DEEPSEEK_API_KEY",
				BalanceURL: "https://api.deepseek.com/user/balance", Thinking: "enabled",
				WebSearch: boolPointer(true), AnthropicBeta: anthropicBetaContext1M, SupportedEfforts: []string{"disabled", "low", "high", "max"}, DefaultEffort: "high",
				ContextWindow: 1_000_000, Price: deepSeekV4ProPriceUSD(),
				BillingCurrency: "USD", BillingMode: "payg",
			},
		},
	}
}

// WriteFile writes the configuration to path as annotated TOML. The write is
// atomic + fsynced so an interrupted write or power loss can never truncate the
// main config into an unparseable state that leaves the app with no usable
// models (#4615, #4708).
func (c *Config) WriteFile(path string) error {
	return atomicWriteToConfigFile(path, RenderTOMLForScope(c, renderScopeForPath(path)), configFilePerm(path))
}

// Validate checks that the selected model's provider is usable.
func (c *Config) Validate(model string) error {
	e, ok := c.ResolveModel(model)
	if !ok {
		return fmt.Errorf("unknown model %q (configured: %s)", model, c.providerNames())
	}
	if e.Kind == "" {
		return fmt.Errorf("provider %q: kind is required", model)
	}
	if e.BaseURL == "" {
		return fmt.Errorf("provider %q: base_url is required", model)
	}
	if strings.TrimSpace(e.APIKeyEnv) != "" && !IsValidCredentialKey(e.APIKeyEnv) {
		return fmt.Errorf("provider %q: api_key_env %q is invalid; use letters, numbers, and underscores, not a model name", model, e.APIKeyEnv)
	}
	if e.RequiresAPIKey() && e.APIKey() == "" {
		return fmt.Errorf("provider %q: missing env %s", model, e.APIKeyEnv)
	}
	return nil
}

func (c *Config) providerNames() string {
	names := make([]string, len(c.Providers))
	for i, p := range c.Providers {
		names[i] = p.Name
	}
	return strings.Join(names, ", ")
}
