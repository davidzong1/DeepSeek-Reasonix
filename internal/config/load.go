package config

import (
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	fileencoding "reasonix/internal/fileutil/encoding"
	"strings"

	"github.com/BurntSushi/toml"
)

// Load builds the configuration: defaults, then user config, then project
// config, then MCP servers from Claude Code's .mcp.json, then (lowest priority)
// the v0.x ~/.reasonix/config.json's mcpServers. Provider api_key_env values
// resolve from Reasonix's global .env, not from project .env files.
func Load() (*Config, error) {
	return LoadForRoot(".")
}

// LoadForRoot builds the configuration with project files resolved from root
// instead of the current working directory. When root is "" or ".", it behaves
// like Load(). This is the workspace-aware entry point: desktop tabs use it so
// each project's reasonix.toml + .mcp.json are resolved independently without
// changing the process cwd, while provider keys stay rooted in Reasonix home.
//
// Note: LoadForRoot may rewrite legacy MCP `tier` lines on disk (see
// mergeRuntimeTOMLFileSnapshot). Callers that must not mutate config files should use
// LoadForRootReadOnly instead.
func LoadForRoot(root string) (*Config, error) {
	return loadForRoot(root, loadForRootOptions{migrateOnDisk: true, loadCredentials: true})
}

// LoadForRootReadOnly is like LoadForRoot but never writes config files: it skips
// on-disk legacy MCP tier migration. Prefer this for diagnostics, doctor, and
// other read-only inspection paths.
func LoadForRootReadOnly(root string) (*Config, error) {
	return loadForRoot(root, loadForRootOptions{loadCredentials: true})
}

// LoadForRootWithoutCredentialsReadOnly is the credential-free form of
// LoadForRootReadOnly. It still merges the effective user + project config and
// carries project .env values for workspace-scoped expansion, but it neither
// pins Reasonix credentials into the process environment nor resolves provider
// API keys. Settings probes use it when they need runtime network policy before
// resolving only the edited provider's credential explicitly.
func LoadForRootWithoutCredentialsReadOnly(root string) (*Config, error) {
	return loadForRoot(root, loadForRootOptions{})
}

// LoadUserConfigReadOnly loads only the trusted user-global config. It never
// reads project reasonix.toml files and never performs on-disk migrations.
// Host-owned features that may execute a configured binary should use this
// instead of LoadForRoot so an untrusted checkout cannot choose the process.
func LoadUserConfigReadOnly() (*Config, error) {
	cfg := Default()
	if path := userConfigLoadPath(); path != "" {
		meta, err := mergeFileSnapshot(cfg, path)
		if err != nil {
			return nil, err
		}
		if meta.IsDefined("agent", "system_prompt_file") {
			cfg.systemPromptFileSource = promptFileSourceUser
		}
	}
	normalizeConfigForEdit(cfg)
	cfg.loadOpenCodeGoJournal(userConfigLoadPath())
	return cfg, nil
}

type loadForRootOptions struct {
	migrateOnDisk   bool
	loadCredentials bool
}

func loadForRoot(root string, opts loadForRootOptions) (*Config, error) {
	root = resolveRoot(root)
	expansionEnv := loadProjectDotEnvForExpansion(root)
	if opts.loadCredentials {
		loadCredentialStoreForRoot(root)
	}
	cfg := Default()
	cfg.setExpansionEnv(expansionEnv)
	cfg.CredentialsStore = credentialsStoreMode()

	projectTOML := "reasonix.toml"
	if root != "." {
		projectTOML = filepath.Join(root, "reasonix.toml")
	}
	if primary := userConfigPath(); primary != "" {
		if _, err := resolveConfigAccessPath(primary, true); err != nil {
			return nil, err
		}
	}
	if _, err := resolveConfigAccessPath(projectTOML, false); err != nil {
		return nil, err
	}

	mergeTOML := mergeFileSnapshot
	if opts.migrateOnDisk {
		mergeTOML = mergeRuntimeTOMLFileSnapshot
	}

	var tomlSources []string
	userDefaultModelExplicit := false
	if uc := userConfigLoadPath(); uc != "" {
		tomlSources = append(tomlSources, uc)
		meta, err := mergeTOML(cfg, uc)
		if err != nil {
			// Never rewrite the broken original file. Prefer the last verified
			// snapshot in memory, then built-in defaults, and keep loading so
			// the rest of the app stays usable.
			lkgCfg := Default()
			lkgCfg.setExpansionEnv(expansionEnv)
			lkgCfg.CredentialsStore = credentialsStoreMode()
			if lkgErr := loadLastKnownGoodUserConfig(lkgCfg); lkgErr == nil {
				*cfg = *lkgCfg
				cfg.addLoadWarning(fmt.Sprintf(
					"user config %s is invalid (%v); using last-known-good snapshot in memory without modifying the original file",
					uc, err,
				))
			} else {
				cfg.addLoadWarning(fmt.Sprintf(
					"user config %s is invalid (%v); using built-in defaults in memory without modifying the original file",
					uc, err,
				))
			}
		} else {
			userDefaultModelExplicit = meta.IsDefined("default_model")
			if meta.IsDefined("agent", "system_prompt_file") {
				cfg.systemPromptFileSource = promptFileSourceUser
			}
		}
	}
	// A last-known-good recovery is still trusted user configuration even though
	// the broken source file cannot provide usable TOML metadata.
	if cfg.systemPromptFileSource == promptFileSourceUnknown && cfg.Agent.SystemPromptFile != "" {
		cfg.systemPromptFileSource = promptFileSourceUser
	}
	userDefaultModel := cfg.DefaultModel
	globalCLI := cfg.CLI
	globalSecrets := cfg.Secrets
	globalRemote := cfg.Remote.Clone()
	globalDesktopLanguage := cfg.Desktop.Language
	globalPricingCurrency := cfg.Desktop.Currency
	globalBillingDisplayCurrency := cfg.Billing.DisplayCurrency
	globalTelemetry, globalLegacyAnchorSafetyGate := cfg.Telemetry, cfg.Agent.LegacyAnchorSafetyGate

	tomlSources = append(tomlSources, projectTOML)
	projectMeta, err := mergeTOML(cfg, projectTOML)
	if err != nil {
		// Project config damage is isolated to this workspace: continue with
		// user/global config so other tabs stay available.
		cfg.addLoadWarning(fmt.Sprintf(
			"project config %s is invalid (%v); ignored for this workspace",
			projectTOML, err,
		))
		// Drop the project path from later multi-file merges so a broken TOML
		// cannot fail plugin/provider re-merges.
		tomlSources = tomlSources[:len(tomlSources)-1]
	} else if projectMeta.IsDefined("agent", "system_prompt_file") {
		cfg.systemPromptFileSource = promptFileSourceProject
	}
	// The native CLI update channel controls the one user-installed binary.
	// A repository-local reasonix.toml must never switch that global choice.
	cfg.CLI = globalCLI
	// Secret protection is a user-global security control: a cloned repo's
	// reasonix.toml must not be able to flip on the workflow-breaking env/path
	// protections.
	cfg.Secrets = globalSecrets
	// Remote SSH hosts are equally user-global: a cloned repo's reasonix.toml
	// must not be able to inject hosts, jump chains, or port forwards that
	// steer where Reasonix opens connections.
	cfg.Remote = globalRemote
	// Desktop language and pricing currency are user-level regional preferences.
	// A repository must not be able to alter how the user's spend is shown.
	cfg.Desktop.Language = globalDesktopLanguage
	cfg.Desktop.Currency = globalPricingCurrency
	cfg.Billing.DisplayCurrency = globalBillingDisplayCurrency
	// CLI telemetry is an explicit user-global privacy choice. Project config
	// cannot opt a user in or out, including when the global value is absent.
	cfg.Telemetry, cfg.Agent.LegacyAnchorSafetyGate = globalTelemetry, globalLegacyAnchorSafetyGate
	// TOML decoding replaces [[plugins]] wholesale, so cfg.Plugins holds only the
	// last file's. Re-merge by name across sources (later wins) so a project
	// reasonix.toml doesn't drop global MCP servers. mergeTOMLPlugins is read-only.
	plugins, err := mergeTOMLPlugins(tomlSources)
	if err != nil {
		cfg.addLoadWarning(fmt.Sprintf("plugin configuration could not be merged (%v); continuing without those entries", err))
	} else {
		cfg.Plugins = plugins
	}
	if providers, providerSources, shadowedProjectProviders, ok, err := mergeTOMLProviders(tomlSources); err != nil {
		cfg.addLoadWarning(fmt.Sprintf("provider configuration could not be merged (%v); keeping providers already loaded", err))
	} else if ok {
		cfg.Providers = providers
		cfg.providerSources = providerSources
		cfg.shadowedProjectProviders = shadowedProjectProviders
	}
	if access, ok, err := mergeTOMLProviderAccess(tomlSources); err != nil {
		cfg.addLoadWarning(fmt.Sprintf("provider access configuration could not be merged (%v)", err))
	} else if ok {
		cfg.Desktop.ProviderAccess = access
	}

	// Claude Code's .mcp.json (project root) is read last and merged into
	// [[plugins]], so a server configured for Claude works here unchanged.
	// Project reasonix.toml wins a collision, then project .mcp.json.
	mcpFile := mcpJSONFile
	if root != "." {
		mcpFile = filepath.Join(root, mcpJSONFile)
	}
	entries, err := loadMCPJSON(mcpFile)
	if err != nil {
		cfg.addLoadWarning(fmt.Sprintf("project .mcp.json is invalid (%v); MCP servers from that file are ignored", err))
	} else {
		cfg.mergeMCPJSON(entries)
	}

	// Lowest priority before the one-time v1.9.1 MCP migration: the v0.x
	// ~/.reasonix/config.json mcpServers. Once the marker exists the current
	// config is authoritative even when empty; re-reading resurrects removals.
	if !mcpGlobalMigrationComplete() {
		cfg.mergeMCPJSON(loadLegacyMCP(legacyConfigPath()))
	}
	_ = mergeInstalledPluginPackages(cfg, root)
	if err := normalizeRuntimeConfigWithMigrationJournal(cfg); err != nil {
		return nil, err
	}
	if userDefaultModelExplicit {
		restoreUnresolvableProjectDefaultModel(cfg, userDefaultModel)
	}
	cfg.CredentialsStore = credentialsStoreMode()
	cfg.setExpansionEnv(expansionEnv)
	if opts.loadCredentials {
		resolveProviderCredentialsForRoot(root, cfg)
	}
	return cfg, nil
}

// LoadBuiltinDefaultsForRoot returns a read-only built-in-only configuration
// without reading or migrating user/project TOML. Diagnostic and recovery tools
// use it when configuration is malformed; it does not put the process into any
// degraded product "mode". Provider credentials still resolve only from
// Reasonix's global credential store.
func LoadBuiltinDefaultsForRoot(root string) *Config {
	cfg := Default()
	cfg.Plugins = nil
	cfg.Skills = SkillsConfig{}
	cfg.Bot.Enabled = false
	cfg.Bot.Connections = nil
	cfg.Bot.Routes = nil
	cfg.Statusline.Command = ""
	cfg.LSP.Enabled = false
	cfg.setExpansionEnv(nil)
	cfg.CredentialsStore = credentialsStoreMode()
	resolveProviderCredentialsForRoot(root, cfg)
	return cfg
}

// LoadRecoveryDefaultsForRoot is retained as an alias of LoadBuiltinDefaultsForRoot
// for older recovery call sites.
func LoadRecoveryDefaultsForRoot(root string) *Config {
	return LoadBuiltinDefaultsForRoot(root)
}

func (c *Config) setExpansionEnv(env map[string]string) {
	if c == nil {
		return
	}
	c.expansionEnv = cloneStringMap(env)
	for i := range c.Plugins {
		c.Plugins[i].expansionEnv = c.expansionEnv
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

// restoreUnresolvableProjectDefaultModel falls back to the user/global
// default_model when a project reasonix.toml overrides it with a reference no
// configured provider serves (#4218). Pre-v1.11 persistence paths (e.g. the
// "always allow" writer) full-rendered ./reasonix.toml and pinned the built-in
// default_model ("deepseek-flash") into it; once the user's [[providers]]
// replaced the built-in presets, that stale name resolved to nothing and boot
// hard-failed in every launch from that folder. In-memory only — the project
// file is untouched, and a project override that does resolve still wins. The
// ignored value is kept so boot can surface a notice.
//
// Callers must only invoke this when the user config explicitly defines
// default_model: falling back to the built-in default would silently mask a
// broken ref when the project file is the user's only config, and that case
// must keep the actionable boot error (TestBuildUnknownModelErrorIsActionable).
func restoreUnresolvableProjectDefaultModel(c *Config, userDefault string) {
	if c == nil {
		return
	}
	if c.DefaultModel == userDefault {
		return
	}
	if _, ok := c.ResolveModel(c.DefaultModel); ok {
		return
	}
	if _, ok := c.ResolveModel(userDefault); !ok {
		return
	}
	c.ignoredProjectDefaultModel = c.DefaultModel
	c.DefaultModel = userDefault
}

// tomlFileDefinesKey reports whether the TOML file at path explicitly defines
// the given top-level key. Missing or unparseable files report false.
func tomlFileDefinesKey(path string, key ...string) bool {
	var f Config
	meta, err := decodeTOMLFile(path, &f)
	if err != nil {
		return false
	}
	return meta.IsDefined(key...)
}

// normalizeLegacyEffort migrates the retired DeepSeek effort="off" (the old
// /thinking off that disabled thinking) to the provider default, so a config
// written by an older version keeps loading instead of erroring on a value the
// provider no longer accepts.
func normalizeLegacyEffort(c *Config) {
	for i := range c.Providers {
		if strings.EqualFold(strings.TrimSpace(c.Providers[i].Effort), "off") {
			c.Providers[i].Effort = ""
		}
	}
}

// mergeTOMLPlugins merges [[plugins]] across TOML sources by name (later source wins).
func mergeTOMLPlugins(paths []string) ([]PluginEntry, error) {
	var merged []PluginEntry
	index := map[string]int{}
	for _, path := range paths {
		_, exists, err := statConfigPath(path)
		if err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
		if !exists {
			continue
		}
		var f Config
		if _, err := decodeTOMLFile(path, &f); err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
		for _, p := range f.Plugins {
			p, _ = NormalizePluginCommandLine(p)
			if isUserConfigPath(path) {
				p.Source = MCPSourceUserConfig
			} else {
				p.Source = MCPSourceProjectConfig
			}
			if i, ok := index[p.Name]; ok {
				merged[i] = p
				continue
			}
			index[p.Name] = len(merged)
			merged = append(merged, p)
		}
	}
	return merged, nil
}

// mergeTOMLProviders merges [[providers]] across TOML sources by provider name.
// User-global providers win over same-named project providers; project providers
// only fill names the global config does not define. Keep official legacy aliases
// distinct here: they can carry different default models and effort capabilities,
// and the later desktop normalization layer handles canonical Settings access.
func mergeTOMLProviders(paths []string) ([]ProviderEntry, map[string]providerSourceScope, []ProviderEntry, bool, error) {
	var merged []ProviderEntry
	var shadowedProject []ProviderEntry
	index := map[string]int{}
	sources := map[string]providerSourceScope{}
	saw := false
	for _, path := range paths {
		_, exists, err := statConfigPath(path)
		if err != nil {
			return nil, nil, nil, false, fmt.Errorf("config %s: %w", path, err)
		}
		if !exists {
			continue
		}
		var f Config
		if _, err := decodeTOMLFile(path, &f); err != nil {
			return nil, nil, nil, false, fmt.Errorf("config %s: %w", path, err)
		}
		markPersistedDeepSeekOfficialPricing(&f)
		if len(f.Providers) == 0 {
			continue
		}
		saw = true
		source := providerSourceForPath(path)
		for _, p := range f.Providers {
			normalizeProviderEffortFields(&p)
			key := providerMergeKey(p)
			if i, ok := index[key]; ok {
				if sources[key] == providerSourceProject && source == providerSourceUser {
					shadowedProject = append(shadowedProject, merged[i])
					merged[i] = p
					sources[key] = source
				} else if sources[key] == providerSourceUser && source == providerSourceProject {
					shadowedProject = append(shadowedProject, p)
				}
				continue
			} else {
				index[key] = len(merged)
				merged = append(merged, p)
				sources[key] = source
			}
		}
	}
	return merged, sources, shadowedProject, saw, nil
}

func providerSourceForPath(path string) providerSourceScope {
	if isUserConfigPath(path) {
		return providerSourceUser
	}
	return providerSourceProject
}

func providerMergeKey(p ProviderEntry) string {
	return strings.TrimSpace(p.Name)
}

// mergeTOMLProviderAccess merges desktop.provider_access across TOML sources so
// project desktop settings do not hide account-level providers from the desktop
// model switcher.
func mergeTOMLProviderAccess(paths []string) ([]string, bool, error) {
	var merged []string
	seen := map[string]bool{}
	saw := false
	userDeclared := false
	for _, path := range paths {
		_, exists, err := statConfigPath(path)
		if err != nil {
			return nil, false, fmt.Errorf("config %s: %w", path, err)
		}
		if !exists {
			continue
		}
		var f Config
		meta, err := decodeTOMLFile(path, &f)
		if err != nil {
			return nil, false, fmt.Errorf("config %s: %w", path, err)
		}
		if !meta.IsDefined("desktop", "provider_access") {
			continue
		}
		if !saw {
			// Preserve declaration state even when the list is explicitly empty.
			// A nil slice means legacy/undeclared access; a non-nil empty slice
			// means the user intentionally removed every desktop provider.
			merged = []string{}
		}
		saw = true
		if isUserConfigPath(path) {
			userDeclared = true
		}
		for _, name := range f.Desktop.ProviderAccess {
			name = strings.TrimSpace(name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			merged = append(merged, name)
		}
	}
	// An undeclared user list means "allow all"; a union with a project-only
	// list would silently narrow that to whatever the project happens to name.
	if saw && !userDeclared {
		return nil, false, nil
	}
	return merged, saw, nil
}

// ConfigFileDeclarations contains provider settings explicitly declared by one
// TOML file, without defaults or values inherited from another scope.
type ConfigFileDeclarations struct {
	ProviderNames                 []string
	DesktopProviderAccessDeclared bool
}

// InspectConfigFileDeclarations returns the provider-related fields explicitly
// present in one TOML file. It deliberately does not include built-in defaults
// or values inherited from another config scope.
func InspectConfigFileDeclarations(path string) (ConfigFileDeclarations, error) {
	var declarations ConfigFileDeclarations
	path = strings.TrimSpace(path)
	if path == "" {
		return declarations, nil
	}
	_, exists, err := statConfigPath(path)
	if err != nil {
		return declarations, err
	}
	if !exists {
		return declarations, nil
	}
	var f Config
	meta, err := decodeTOMLFile(path, &f)
	if err != nil {
		return declarations, fmt.Errorf("config %s: %w", path, err)
	}
	seen := make(map[string]bool, len(f.Providers))
	for _, provider := range f.Providers {
		name := strings.TrimSpace(provider.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		declarations.ProviderNames = append(declarations.ProviderNames, name)
	}
	declarations.DesktopProviderAccessDeclared = meta.IsDefined("desktop", "provider_access")
	return declarations, nil
}

// DesktopProviderAccessDeclared reports whether path explicitly declares
// desktop.provider_access. It distinguishes omission from an intentional [].
func DesktopProviderAccessDeclared(path string) (bool, error) {
	declarations, err := InspectConfigFileDeclarations(path)
	return declarations.DesktopProviderAccessDeclared, err
}

// LoadForEdit returns a config to seed the `reasonix setup` wizard when reconfiguring:
// the built-in defaults with the file at path (if present) decoded on top, so a
// reconfigure preserves the user's existing providers and agent settings instead
// of resetting to defaults. Reasonix's global .env is loaded so api_key_env
// resolution works while the wizard decides which keys are still missing.
func LoadForEdit(path string) *Config {
	return loadForEdit(path, true, false)
}

// LoadForEditReadOnlyStrict is the error-returning commit-time variant. It must
// not fall back to defaults when another writer leaves malformed TOML, because
// saving that fallback would overwrite the user's recoverable file.
func LoadForEditReadOnlyStrict(path string) (*Config, error) {
	return loadForEditStrict(path, true, false)
}

// LoadForEditWithoutCredentialsReadOnlyStrict is the credential-free strict
// edit loader. It never writes migrations and never substitutes defaults for a
// malformed file.
func LoadForEditWithoutCredentialsReadOnlyStrict(path string) (*Config, error) {
	return loadForEditStrict(path, false, false)
}

// ValidateFile parses one TOML config in isolation without loading credentials,
// applying migrations, or writing the file. A missing file is valid.
func ValidateFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	_, exists, err := statConfigPath(path)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	cfg := Default()
	if _, err := decodeTOMLFile(path, cfg); err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	return nil
}

// ValidateBytes parses one in-memory TOML config without loading credentials,
// applying migrations, or writing any state.
func ValidateBytes(data []byte) error {
	cfg := Default()
	if _, err := decodeTOMLBytes(data, cfg); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

func loadForEdit(path string, loadCredentials, persistMigrations bool) *Config {
	cfg, err := loadForEditStrict(path, loadCredentials, persistMigrations)
	if err == nil {
		return cfg
	}
	slog.Warn("config: load for edit failed, using defaults", "path", path, "err", err)
	if loadCredentials {
		loadDotEnvForEditPath(path)
	}
	cfg = Default()
	normalizeConfigForEdit(cfg)
	cfg.editLoadErr = err
	return cfg
}

func LoadForEditWithoutCredentials(path string) *Config {
	return loadForEdit(path, false, false)
}

func loadForEditStrict(path string, loadCredentials, persistMigrations bool) (*Config, error) {
	if loadCredentials {
		loadDotEnvForEditPath(path)
	}
	cfg := Default()
	meta, err := mergeFileSnapshot(cfg, path)
	if err != nil {
		return nil, err
	}
	markExplicitDefaultProjectSkillKeys(cfg, path, meta)
	changed := normalizeConfigForEdit(cfg)
	cfg.loadOpenCodeGoJournal(path)
	if persistMigrations && changed && strings.TrimSpace(path) != "" {
		if _, err := os.Stat(path); err == nil {
			if err := cfg.SaveTo(path); err != nil {
				return nil, err
			}
		}
	}
	return cfg, nil
}

// markExplicitDefaultProjectSkillKeys preserves project skill fields that are
// explicitly present in a file but equal the built-in default. Without this
// transient provenance, saving an unrelated project setting would mistake an
// intentional `false`/empty override for a stale delta and remove it.
func markExplicitDefaultProjectSkillKeys(c *Config, path string, meta toml.MetaData) {
	if c == nil || isUserConfigPath(path) {
		return
	}
	for _, key := range projectSkillKeys {
		if !meta.IsDefined("skills", key) || !projectSkillKeyIsDefault(c, key) {
			continue
		}
		if c.explicitProjectSkillKeys == nil {
			c.explicitProjectSkillKeys = make(map[string]bool)
		}
		c.explicitProjectSkillKeys[key] = true
	}
}

func normalizeConfigForEdit(cfg *Config) bool {
	normalizePluginCommandLines(cfg)
	normalizeLegacyEffort(cfg)
	normalizeLegacyAgentStepLimits(cfg)
	changed := normalizeRetiredAutoPlan(cfg)
	changed = normalizeRetiredMultiThresholdCompaction(cfg) || changed
	normalizeLegacyMCPTiers(cfg)
	changed = normalizeLegacyStepFunBaseURLs(cfg) || changed
	changed = normalizeLegacyLongCatContextWindows(cfg) || changed
	changed = normalizeLegacyQwenContextWindows(cfg) || changed
	changed = normalizeLegacyKimiK3Catalog(cfg) || changed
	changed = normalizeLegacyOpenCodeGoInstalls(cfg) || changed
	changed = normalizeLegacyMimoCustomProviders(cfg) || changed
	normalizeLegacyProviderModels(cfg)
	normalizeDesktopOfficialProviderAccess(cfg)
	normalizeOfficialDeepSeekModels(cfg)
	migrateBillingDisplayCurrency(cfg)
	freezeProviderBillingCurrencies(cfg)
	applyDeepSeekOfficialDefaultPricing(cfg)
	backfillDeepSeekOfficialPrices(cfg)
	normalizeEffortConfig(cfg)
	return changed
}

// normalizeRetiredMultiThresholdCompaction clears retired multi-threshold keys
// so they never reach the Agent. Disk migration removes them on ordinary start;
// loading still ignores them if migration could not rewrite the file.
func normalizeRetiredMultiThresholdCompaction(c *Config) bool {
	if c == nil {
		return false
	}
	changed := c.Agent.SoftCompactRatio != 0 ||
		c.Agent.ToolResultSnipRatio != 0 ||
		c.Agent.CompactForceRatio != 0 ||
		c.Agent.ColdResumePrune != nil ||
		strings.TrimSpace(c.Agent.ContextEditing) != ""
	c.Agent.SoftCompactRatio = 0
	c.Agent.ToolResultSnipRatio = 0
	c.Agent.CompactForceRatio = 0
	c.Agent.ColdResumePrune = nil
	c.Agent.ContextEditing = ""
	if c.Agent.CompactRatio <= 0 {
		c.Agent.CompactRatio = Default().Agent.CompactRatio
		changed = true
	}
	return changed
}

// normalizeRetiredAutoPlan keeps pre-v5 configs readable while enforcing the
// single explicit-plan experience. The deprecated fields remain in AgentConfig
// only so old TOML and older desktop payloads decode safely.
func normalizeRetiredAutoPlan(c *Config) bool {
	if c == nil {
		return false
	}
	changed := strings.TrimSpace(c.Agent.AutoPlan) != "" && !strings.EqualFold(strings.TrimSpace(c.Agent.AutoPlan), "off") ||
		strings.TrimSpace(c.Agent.AutoPlanClassifier) != ""
	c.Agent.AutoPlan = "off"
	c.Agent.AutoPlanClassifier = ""
	return changed
}

func loadDotEnvForEditPath(path string) {
	path = strings.TrimSpace(path)
	if path == "" || isUserConfigPath(path) {
		loadDotEnv()
		return
	}
	loadDotEnvForRoot(filepath.Dir(path))
}

// mergeFile decodes a TOML file onto cfg if it exists. An absent file is not an error.
func mergeFile(cfg *Config, path string) error {
	_, err := mergeFileSnapshot(cfg, path)
	return err
}

// mergeFileSnapshot decodes one immutable read of a TOML file onto cfg and
// returns metadata from those exact bytes. Callers that derive source or
// precedence decisions from metadata must use this result instead of reading
// the path again: a config file may be atomically replaced between reads.
func mergeFileSnapshot(cfg *Config, path string) (toml.MetaData, error) {
	return mergeFileSnapshotWithRead(cfg, path, fileencoding.ReadFileUTF8)
}

func mergeFileSnapshotWithRead(cfg *Config, path string, readFile func(string) ([]byte, error)) (toml.MetaData, error) {
	resolved, exists, err := statConfigPath(path)
	if err != nil {
		return toml.MetaData{}, err
	}
	if !exists {
		return toml.MetaData{}, nil
	}
	data, err := readFile(resolved)
	if err != nil {
		return toml.MetaData{}, fmt.Errorf("config %s: %w", path, err)
	}
	// BurntSushi/toml decodes struct fields incrementally and can leave earlier
	// fields mutated when a later value has the wrong type. Validate the complete
	// snapshot against a disposable Config before merging those same bytes into
	// the active object. This makes user LKG fallback, project-level isolation,
	// and metadata-derived provenance transactional with respect to file changes.
	var validated Config
	if _, err := decodeTOMLBytes(data, &validated); err != nil {
		return toml.MetaData{}, fmt.Errorf("config %s: %w", path, err)
	}
	meta, err := decodeTOMLBytes(data, cfg)
	if err != nil {
		return toml.MetaData{}, fmt.Errorf("config %s: %w", path, err)
	}
	if meta.IsDefined("providers") {
		var persisted Config
		if _, err := decodeTOMLBytes(data, &persisted); err != nil {
			return toml.MetaData{}, fmt.Errorf("config %s: %w", path, err)
		}
		markPersistedDeepSeekOfficialPricing(&persisted)
		markers := map[string]string{}
		for i := range persisted.Providers {
			markers[providerMergeKey(persisted.Providers[i])] = persisted.Providers[i].persistedOfficialCurrency
		}
		for i := range cfg.Providers {
			cfg.Providers[i].persistedOfficialCurrency = markers[providerMergeKey(cfg.Providers[i])]
		}
	}
	return meta, nil
}

func mergeRuntimeTOMLFileSnapshot(cfg *Config, path string) (toml.MetaData, error) {
	if _, err := os.Stat(path); err == nil {
		if err := migrateLegacyMCPTiersFile(path); err != nil {
			slog.Warn("config: legacy mcp tier migration failed", "path", path, "err", err)
		}
	}
	return mergeFileSnapshot(cfg, path)
}
