package config

import (
	"net/url"
	"reasonix/internal/netclient"
	"strings"
)

// LSPConfig governs the optional Language Server Protocol tools (lsp_definition,
// lsp_references, lsp_hover, lsp_diagnostics). Enabled defaults to true; the
// servers themselves are never bundled — each resolves on PATH and the tool
// returns an install hint when it is missing, so the capability is dormant until
// the user installs a server. Servers overrides or extends the built-in language
// → server map, keyed by language id (e.g. "go", "rust", "python").
type LSPConfig struct {
	Enabled bool                 `toml:"enabled"`
	Servers map[string]LSPServer `toml:"servers"`
}

// LSPServer overrides a built-in language's server or, when keyed by a new
// language, adds one. An empty field falls back to the built-in default for that
// language; Extensions is required when adding a language the built-ins don't
// cover (e.g. ".ex" for Elixir) so files route to it.
type LSPServer struct {
	Command     string            `toml:"command"`
	Args        []string          `toml:"args"`
	Env         map[string]string `toml:"env"`
	LanguageID  string            `toml:"language_id"`
	Extensions  []string          `toml:"extensions"`
	InstallHint string            `toml:"install_hint"`
}

// StatuslineConfig configures a custom status line. Command, when set, is run at
// startup and after each turn; its first line of stdout replaces the built-in
// status data row. A JSON payload (model, context tokens, cwd) is fed on stdin.
type StatuslineConfig struct {
	Command string `toml:"command"`
}

// CheckpointsConfig tunes rewind snapshot retention. Zero values leave the
// built-in defaults in place (100 turns, 1 GiB soft budget).
type CheckpointsConfig struct {
	// RetainTurns caps how many turns of file payloads are kept.
	RetainTurns int `toml:"retain_turns"`
	// BlobQuotaBytes is the soft byte budget for retained file payloads. A
	// protected or current turn may temporarily exceed it.
	BlobQuotaBytes int64 `toml:"blob_quota_bytes"`
}

// BotConfig 控制多渠道 IM bot 消息网关。
type BotConfig struct {
	Enabled            bool                  `toml:"enabled"`
	Model              string                `toml:"model"` // 用于 bot 的模型名，空则用 default_model
	ToolApprovalMode   string                `toml:"tool_approval_mode"`
	MaxSteps           int                   `toml:"max_steps"`
	DebounceMs         int                   `toml:"debounce_ms"` // 消息合并窗口，毫秒
	QueueMode          string                `toml:"queue_mode"`  // steer|followup|collect|interrupt
	QueueCap           int                   `toml:"queue_cap"`
	QueueDrop          string                `toml:"queue_drop"` // summarize|old|new
	IgnoreSelfMessages bool                  `toml:"ignore_self_messages"`
	SelfUserIDs        BotSelfUserIDs        `toml:"self_user_ids"`
	Control            BotControlConfig      `toml:"control"`
	Pairing            BotPairingConfig      `toml:"pairing"`
	Allowlist          BotAllowlist          `toml:"allowlist"`
	QQ                 QQBotConfig           `toml:"qq"`
	Feishu             FeishuBotConfig       `toml:"feishu"`
	Weixin             WeixinBotConfig       `toml:"weixin"`
	Dingtalk           DingtalkBotConfig     `toml:"dingtalk"`
	Routes             []BotRouteConfig      `toml:"routes"`
	Connections        []BotConnectionConfig `toml:"connections"`
	// DesktopWatchers persists /desktop watch subscriptions so god-view
	// notifications survive a desktop restart. Managed by the desktop bot
	// bridge, not the settings UI.
	DesktopWatchers []BotDesktopWatcherConfig `toml:"desktop_watchers"`
}

// BotDesktopWatcherConfig is one bot chat subscribed to desktop events
// (/desktop watch on).
type BotDesktopWatcherConfig struct {
	Platform     string `toml:"platform"`
	ConnectionID string `toml:"connection_id"`
	Domain       string `toml:"domain"`
	ChatType     string `toml:"chat_type"`
	ChatID       string `toml:"chat_id"`
}

type BotSelfUserIDs struct {
	QQ       []string `toml:"qq"`
	Feishu   []string `toml:"feishu"`
	Weixin   []string `toml:"weixin"`
	Dingtalk []string `toml:"dingtalk"`
}

type BotControlConfig struct {
	Enabled  bool   `toml:"enabled"`
	Addr     string `toml:"addr"`
	TokenEnv string `toml:"token_env"`
}

type BotRouteConfig struct {
	ConnectionID     string `toml:"connection_id"`
	Platform         string `toml:"platform"`
	ChatType         string `toml:"chat_type"`
	ChatID           string `toml:"chat_id"`
	UserID           string `toml:"user_id"`
	ThreadID         string `toml:"thread_id"`
	Model            string `toml:"model"`
	ToolApprovalMode string `toml:"tool_approval_mode"`
	WorkspaceRoot    string `toml:"workspace_root"`
}

// BotAllowlist 控制哪些用户可以使用 bot。
type BotAllowlist struct {
	Enabled           bool     `toml:"enabled"`
	AllowAll          bool     `toml:"allow_all"`
	QQUsers           []string `toml:"qq_users"`
	FeishuUsers       []string `toml:"feishu_users"`
	WeixinUsers       []string `toml:"weixin_users"`
	QQApprovers       []string `toml:"qq_approvers"`
	FeishuApprovers   []string `toml:"feishu_approvers"`
	WeixinApprovers   []string `toml:"weixin_approvers"`
	QQAdmins          []string `toml:"qq_admins"`
	FeishuAdmins      []string `toml:"feishu_admins"`
	WeixinAdmins      []string `toml:"weixin_admins"`
	QQGroups          []string `toml:"qq_groups"`
	FeishuGroups      []string `toml:"feishu_groups"`
	WeixinGroups      []string `toml:"weixin_groups"`
	DingtalkUsers     []string `toml:"dingtalk_users"`
	DingtalkApprovers []string `toml:"dingtalk_approvers"`
	DingtalkAdmins    []string `toml:"dingtalk_admins"`
	DingtalkGroups    []string `toml:"dingtalk_groups"`
}

type BotPairingConfig struct {
	Enabled               bool `toml:"enabled"`
	RequestTTLMinutes     int  `toml:"request_ttl_minutes"`
	MaxPendingPerPlatform int  `toml:"max_pending_per_platform"`
}

// BotAccessConfig controls who may use one concrete bot connection.
type BotAccessConfig struct {
	Enabled        bool     `toml:"enabled"`
	AllowAll       bool     `toml:"allow_all"`
	PairingEnabled bool     `toml:"pairing_enabled"`
	Users          []string `toml:"users"`
	Groups         []string `toml:"groups"`
	Approvers      []string `toml:"approvers"`
	Admins         []string `toml:"admins"`
}

// QQBotConfig QQ 官方 Bot API v2 配置。
type QQBotConfig struct {
	Enabled          bool            `toml:"enabled"`
	AppID            string          `toml:"app_id"`
	AppSecretEnv     string          `toml:"app_secret_env"` // 环境变量名，如 QQ_BOT_APP_SECRET
	Sandbox          bool            `toml:"sandbox"`        // true 使用 QQ 沙箱 API / gateway
	Model            string          `toml:"model"`
	ToolApprovalMode string          `toml:"tool_approval_mode"`
	WorkspaceRoot    string          `toml:"workspace_root"`
	Access           BotAccessConfig `toml:"access"`
}

// FeishuBotConfig 飞书自建应用 Bot 配置。
type FeishuBotConfig struct {
	Enabled           bool   `toml:"enabled"`
	Domain            string `toml:"domain"` // feishu（默认）| lark
	AppID             string `toml:"app_id"`
	AppSecretEnv      string `toml:"app_secret_env"`     // 如 FEISHU_BOT_APP_SECRET
	VerificationToken string `toml:"verification_token"` // 事件订阅验证 token
	Mode              string `toml:"mode"`               // webhook（默认）| websocket
	WebhookPort       int    `toml:"webhook_port"`       // webhook 模式端口
	RequireMention    bool   `toml:"require_mention"`
	// OutboundMediaRoots lists absolute directories the loopback /send API may
	// attach files from. Empty disables outbound file sending.
	OutboundMediaRoots []string `toml:"outbound_media_roots"`
}

// WeixinBotConfig 微信 iLink Bot 配置。
type WeixinBotConfig struct {
	Enabled   bool   `toml:"enabled"`
	AccountID string `toml:"account_id"`
	TokenEnv  string `toml:"token_env"` // 环境变量名，如 WEIXIN_BOT_TOKEN
	APIBase   string `toml:"api_base"`  // iLink API base URL
}

// DingtalkBotConfig 钉钉企业内部应用机器人（Stream 模式）配置。
type DingtalkBotConfig struct {
	Enabled          bool            `toml:"enabled"`
	ClientID         string          `toml:"client_id"`          // 钉钉应用 AppKey（ClientID）
	ClientSecret     string          `toml:"client_secret"`      // 钉钉应用 AppSecret（ClientSecret）
	ClientIDEnv      string          `toml:"client_id_env"`      // 环境变量名，如 DINGTALK_CLIENT_ID
	SecretEnv        string          `toml:"secret_env"`         // 环境变量名，如 DINGTALK_CLIENT_SECRET
	BotName          string          `toml:"bot_name"`           // 机器人昵称；群聊 @ 剥离时匹配
	RequireMention   bool            `toml:"require_mention"`    // 群聊是否必须 @ 机器人
	Model            string          `toml:"model"`              // 会话模型；空 = 全局默认
	ToolApprovalMode string          `toml:"tool_approval_mode"` // ask|auto|yolo；空 = 全局默认
	WorkspaceRoot    string          `toml:"workspace_root"`     // 会话工作目录；空 = 启动 Bot 时的 cwd
	Access           BotAccessConfig `toml:"access"`             // 该渠道访问控制（allowlist）
	// SessionMappings 直配渠道的会话绑定（与 [[bot.connections]] 同构）。
	// legacy [bot.dingtalk] 没有 connection 记录，/new 旋转后的新会话路径
	// 持久化在这里，重启后仍能恢复（见 botruntime.rememberInbound）。
	SessionMappings []BotConnectionSessionMapping `toml:"session_mappings"`
}

// BotConnectionConfig is the desktop-friendly connection record for IM bot
// channels. It keeps install/runtime state separate from legacy per-provider
// knobs so the UI can expose a simple "connect first" flow while old configs
// keep working.
type BotConnectionConfig struct {
	ID               string                        `toml:"id"`
	Provider         string                        `toml:"provider"` // qq|feishu|weixin
	Domain           string                        `toml:"domain"`   // feishu|lark|weixin|qq
	Label            string                        `toml:"label"`
	Enabled          bool                          `toml:"enabled"`
	Status           string                        `toml:"status"` // disconnected|pending|connected|error
	Model            string                        `toml:"model"`
	ToolApprovalMode string                        `toml:"tool_approval_mode"`
	WorkspaceRoot    string                        `toml:"workspace_root"`
	Access           BotAccessConfig               `toml:"access"`
	Credential       BotConnectionCredential       `toml:"credential"`
	SessionMappings  []BotConnectionSessionMapping `toml:"session_mappings"`
	LastError        string                        `toml:"last_error"`
	CreatedAt        string                        `toml:"created_at"`
	UpdatedAt        string                        `toml:"updated_at"`
}

type BotConnectionCredential struct {
	AppID        string `toml:"app_id"`
	AppSecretEnv string `toml:"app_secret_env"`
	AccountID    string `toml:"account_id"`
	TokenEnv     string `toml:"token_env"`
}

type BotConnectionSessionMapping struct {
	RemoteID      string `toml:"remote_id"`
	SessionID     string `toml:"session_id"`
	SessionSource string `toml:"session_source"`
	ChatType      string `toml:"chat_type"`
	UserID        string `toml:"user_id"`
	ThreadID      string `toml:"thread_id"`
	Scope         string `toml:"scope"`
	WorkspaceRoot string `toml:"workspace_root"`
	UpdatedAt     string `toml:"updated_at"`
}

// ServeConfig controls the HTTP serve frontend security settings.
type ServeConfig struct {
	// AuthMode selects the HTTP serve auth: "none" (default), "token" (a URL
	// query pre-shared token), or "password" (bcrypt login page).
	AuthMode string `toml:"auth_mode"`
	// Token is a pre-shared token for auth_mode = "token". When empty, a
	// cryptographically random token is generated at startup and printed.
	Token string `toml:"token"`
	// PasswordHash is a bcrypt hash of the password for auth_mode = "password".
	// Generate one with: reasonix serve --hash-password --password '...'
	PasswordHash string `toml:"password_hash"`
	// BehindProxy trusts X-Forwarded-For/Proto from a reverse proxy for
	// rate-limiting and Secure cookies. Leave false otherwise: the headers
	// are forgeable by a direct client.
	BehindProxy bool `toml:"behind_proxy"`
}

// NetworkConfig controls ordinary outbound HTTP traffic such as model providers,
// wallet-balance lookups, updater checks, CodeGraph downloads, and web_fetch.
// web_fetch reuses these proxy settings while keeping its own SSRF-guarded
// dialer.
type NetworkConfig struct {
	// ProxyMode is "auto" (default; environment proxy for now), "env", "custom",
	// or "off". auto leaves room for OS proxy detection later without changing the
	// config shape.
	ProxyMode string `toml:"proxy_mode"`
	// ProxyURL is an advanced custom override such as "socks5://127.0.0.1:7890".
	// When set and proxy_mode = "custom", it wins over the structured proxy table.
	ProxyURL string `toml:"proxy_url"`
	// NoProxy is honored for custom proxies. Env/auto modes use NO_PROXY from the
	// process environment instead.
	NoProxy string             `toml:"no_proxy"`
	Proxy   NetworkProxyConfig `toml:"proxy"`
}

// NetworkProxyConfig is the structured custom-proxy editor shape. Password is
// optional and supports ${VAR} expansion, so users can avoid storing it literally.
type NetworkProxyConfig struct {
	Type     string `toml:"type"` // http|https|socks5|socks5h
	Server   string `toml:"server"`
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	Password string `toml:"password"`
}

// NetworkProxySpec returns the expanded proxy settings used by netclient.
func (c *Config) NetworkProxySpec() netclient.ProxySpec {
	return netclient.ProxySpec{
		Mode:        c.Network.ProxyMode,
		URL:         c.expandVars(c.Network.ProxyURL),
		NoProxy:     c.expandVars(c.Network.NoProxy),
		Type:        c.Network.Proxy.Type,
		Server:      c.expandVars(c.Network.Proxy.Server),
		Port:        c.Network.Proxy.Port,
		Username:    c.expandVars(c.Network.Proxy.Username),
		Password:    c.expandVars(c.Network.Proxy.Password),
		DirectHosts: c.directProxyHosts(),
	}
}

// directProxyHosts collects the base_url hosts of providers marked no_proxy, so
// netclient bypasses the proxy for them without knowing any provider by name.
//
// Only for an auto-detected proxy (auto/env): that proxy is typically a
// GFW-circumvention one not meant for domestic endpoints (e.g. mimo), so keep
// them direct. An explicit proxy_mode = "custom" is the user saying "route
// everything through this" — e.g. a mandatory corporate proxy — so honor it for
// every provider; a custom-proxy user who wants a host direct uses
// network.no_proxy instead (#3635).
func (c *Config) directProxyHosts() []string {
	if c.NetworkProxyMode() == netclient.ModeCustom {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range c.Providers {
		if !p.NoProxy {
			continue
		}
		u, err := url.Parse(strings.TrimSpace(p.BaseURL))
		if err != nil {
			continue
		}
		if h := u.Hostname(); h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

// NetworkProxyMode normalizes network.proxy_mode to a known value.
func (c *Config) NetworkProxyMode() string {
	return netclient.NormalizeMode(c.Network.ProxyMode)
}
