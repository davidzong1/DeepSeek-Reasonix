package config

import (
	"os"
	"path/filepath"
	"strings"
)

// SkillsConfig configures skill discovery. Paths adds extra "custom"-scope skill
// roots — each a directory of SKILL.md / <name>.md playbooks — scanned between
// the project roots (team/.reasonix/.agents/.agent/.claude under the workspace) and
// the global roots. ExcludedPaths hides matching discovery roots without deleting
// folders. ~, relative paths, and ${VAR} expansion are supported. DisabledSkills
// hides named skills from the agent prompt, slash invocation, and skill tools
// while keeping them manageable. DisableImplicitInvocation keeps skills
// discoverable to the host for explicit /skill use and management, but hides
// their index and model-facing invocation tools.
type SkillsConfig struct {
	Paths                     []string `toml:"paths"`
	ExcludedPaths             []string `toml:"excluded_paths"`
	DisabledSkills            []string `toml:"disabled_skills"`
	DisableImplicitInvocation bool     `toml:"disable_implicit_invocation"`
	MaxDepth                  int      `toml:"max_depth"`
}

// ImplicitSkillInvocationEnabled reports whether the model may discover and
// invoke skills without an explicit user slash command. The zero value keeps
// the historical default enabled for old configs.
func (c *Config) ImplicitSkillInvocationEnabled() bool {
	return c == nil || !c.Skills.DisableImplicitInvocation
}

// SkillCustomPaths returns the configured custom skill roots with ${VAR}
// expanded; empty entries are dropped.
func (c *Config) SkillCustomPaths() []string {
	var out []string
	for _, p := range c.Skills.Paths {
		if p = c.expandVars(p); strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// SkillExcludedPaths returns configured skill roots that should be hidden from
// discovery, with ${VAR} expanded and empty entries dropped.
func (c *Config) SkillExcludedPaths() []string {
	var out []string
	for _, p := range c.Skills.ExcludedPaths {
		if p = c.expandVars(p); strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// SkillMaxDepth bounds nested skill discovery. Depth 3 favors bundled skill
// packs while Store keeps nested markdown safe by requiring descriptions.
func (c *Config) SkillMaxDepth() int {
	const (
		defaultDepth = 3
		maxDepth     = 5
	)
	if c == nil || c.Skills.MaxDepth == 0 {
		return defaultDepth
	}
	if c.Skills.MaxDepth < 1 {
		return 1
	}
	if c.Skills.MaxDepth > maxDepth {
		return maxDepth
	}
	return c.Skills.MaxDepth
}

// DisabledSkillNames returns valid disabled skill identifiers, preserving the
// first spelling and dropping duplicates/empty entries.
func (c *Config) DisabledSkillNames() []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range c.Skills.DisabledSkills {
		name = strings.TrimSpace(name)
		if !IsValidSkillName(name) {
			continue
		}
		key := SkillNameKey(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, name)
	}
	return out
}

// IsSkillDisabled reports whether name is configured as disabled.
func (c *Config) IsSkillDisabled(name string) bool {
	key := SkillNameKey(name)
	if key == "" {
		return false
	}
	for _, disabled := range c.DisabledSkillNames() {
		if SkillNameKey(disabled) == key {
			return true
		}
	}
	return false
}

// SandboxConfig bounds the blast radius of tool calls. WorkspaceRoot is
// what the file writers may modify; AllowWrite adds directories they may
// also touch; ForbidRead hides paths (.ssh). All support ${VAR}.
type SandboxConfig struct {
	WorkspaceRoot string   `toml:"workspace_root"`
	AllowWrite    []string `toml:"allow_write"`
	ForbidRead    []string `toml:"forbid_read"`
	// Bash is the OS-sandbox mode for the bash tool: "enforce" jails each
	// command when an OS sandbox is available and refuses bash otherwise; "off"
	// runs it unconfined. Empty uses the platform default.
	Bash string `toml:"bash"`
	// Network allows network egress from inside the bash sandbox. Defaults true
	// so module/package downloads keep working; the boundary is then writes.
	Network bool `toml:"network"`
}

// WriteRoots returns the directories file-writer tools may modify: the
// workspace root (defaulting to the current working directory when unset), plus
// any AllowWrite extras, with ${VAR} expanded. The roots are returned as given
// (relative or absolute); the confiner resolves them to absolute, symlink-free
// paths. The result is always non-empty, so confinement is on by default.
func (c *Config) WriteRoots() []string {
	return c.WriteRootsForRoot(".")
}

// WriteRootsForRoot is like WriteRoots but falls back to fallbackRoot when the
// config doesn't explicitly set a workspace_root. Desktop tabs pass their
// project root here so tool confinement is correct without changing cwd.
func (c *Config) WriteRootsForRoot(fallbackRoot string) []string {
	root := c.expandVars(c.Sandbox.WorkspaceRoot)
	if root == "" {
		root = fallbackRoot
		if root == "" || root == "." {
			if wd, err := os.Getwd(); err == nil {
				root = wd
			} else {
				root = "."
			}
		}
	}
	roots := []string{root}
	for _, d := range c.Sandbox.AllowWrite {
		if d = c.expandVars(d); d != "" {
			roots = append(roots, d)
		}
	}
	return roots
}

// AllowWriteRoots returns only the configured [sandbox] allow_write extras with
// ${VAR} expanded — the explicit escape-hatch entries, without the workspace
// root that WriteRoots prepends. The session-data write guard treats these as
// user-sanctioned raw access.
func (c *Config) AllowWriteRoots() []string {
	var roots []string
	for _, d := range c.Sandbox.AllowWrite {
		if d = c.expandVars(d); d != "" {
			roots = append(roots, d)
		}
	}
	return roots
}

// ForbidReadRoots returns the paths the agent is forbidden from reading
// or listing, with ${VAR} expanded. Relative roots are resolved against the
// current working directory; the confiner resolves them to symlink-free paths.
// Empty when no forbid_read entries are configured.
func (c *Config) ForbidReadRoots() []string {
	return c.ForbidReadRootsForRoot(".")
}

// ForbidReadRootsForRoot is like ForbidReadRoots but uses fallbackRoot when
// resolving relative paths (for desktop tabs that pass their project root).
func (c *Config) ForbidReadRootsForRoot(fallbackRoot string) []string {
	root := fallbackRoot
	if root == "" || root == "." {
		if wd, err := os.Getwd(); err == nil {
			root = wd
		} else {
			root = "."
		}
	}
	roots := make([]string, 0, len(c.Sandbox.ForbidRead))
	for _, d := range c.Sandbox.ForbidRead {
		if d = c.expandVars(d); d != "" {
			if !filepath.IsAbs(d) {
				d = filepath.Join(root, d)
			}
			roots = append(roots, d)
		}
	}
	return roots
}

// BashMode normalises the bash-sandbox mode for the current host.
func (c *Config) BashMode() string {
	return c.BashModeForGOOS(runtimeGOOS)
}

// BashModeForGOOS normalises the bash-sandbox mode for tests and cross-platform
// rendering. macOS and Linux default to enforcement; backend capability is
// checked at launch and restricted presets fail closed when it is unavailable.
// Windows has no OS-level shell sandbox, so every value resolves to "off":
// an explicit "enforce" stays readable (doctor reports it as ignored) but
// never turns into a fail-closed launch.
func (c *Config) BashModeForGOOS(goos string) string {
	if goos == "windows" {
		return "off"
	}
	switch strings.TrimSpace(c.Sandbox.Bash) {
	case "enforce":
		return "enforce"
	case "off":
		return "off"
	case "":
		return "enforce"
	default:
		return "enforce"
	}
}

// AgentConfig configures the harness loop. PlannerModel is optional: when set
// to another provider's name it enables two-model collaboration, where the
// planner handles low-frequency planning in its own session (kept separate so
// each model's prompt prefix stays cache-stable). SubagentModel is the optional
// default for runAs=subagent skills; SubagentModels overrides it per skill name.
type AgentConfig struct {
	SystemPrompt     string `toml:"system_prompt"`
	SystemPromptFile string `toml:"system_prompt_file"`
	// Deprecated compatibility fields. Old TOML and desktop clients may still
	// send them, but config loading normalizes both to zero and rendering omits
	// them. One-off CLI and unattended bot limits remain separate controls.
	MaxSteps        int     `toml:"max_steps"`
	PlannerMaxSteps int     `toml:"planner_max_steps"`
	Temperature     float64 `toml:"temperature"`
	PlannerModel    string  `toml:"planner_model"`
	WebSearchModel  string  `toml:"web_search_model"` // empty or auto preserves automatic search selection
	// VisionModel is empty (off), "auto", or a canonical provider/model ref
	// used to summarize images before a text-only executor turn.
	VisionModel         string  `toml:"vision_model"`
	GuardianModel       string  `toml:"guardian_model"`
	GuardianTemperature float64 `toml:"guardian_temperature"`
	// RecoveryModel is decoded from old configurations for compatibility. The
	// Auto Guard reviewer is retired, so runtime and renderers ignore it.
	// rule-only recovery; it is not implied by guardian or the main model.
	RecoveryModel string `toml:"recovery_model"`
	// RecoveryTemperature is accepted from older configs but ignored. Auto
	// Guard review is deterministic at temperature zero.
	RecoveryTemperature float64           `toml:"recovery_temperature"`
	SubagentModel       string            `toml:"subagent_model"`
	SubagentModels      map[string]string `toml:"subagent_models"`
	SubagentEffort      string            `toml:"subagent_effort"`
	SubagentEfforts     map[string]string `toml:"subagent_efforts"`
	MaxSubagentDepth    int               `toml:"max_subagent_depth"`
	// TaskCostBudget lands a task on one summary once it spends this much.
	TaskCostBudget float64 `toml:"task_cost_budget"`
	// TaskTimeBudgetMinutes is the same gate on wall clock. Both ship off.
	TaskTimeBudgetMinutes float64 `toml:"task_time_budget_minutes"`
	// GoalTokenBudget bounds an unattended Goal loop by cumulative tokens.
	// Off unless set: a Goal runs until it finishes or you stop it.
	GoalTokenBudget int `toml:"goal_token_budget"`
	// MaxSubagentConcurrency bounds how many sub-agents (task, fleet items,
	// profile skills, nested children) may run at once in one session.
	// 0 means the default (6). Values outside 1–32 are clamped on load.
	MaxSubagentConcurrency int `toml:"max_subagent_concurrency"`
	// MaxParallelWriters bounds concurrent writer-capable sub-agents that
	// declare non-overlapping write_paths. 0 means the default (3). Must not
	// exceed MaxSubagentConcurrency after normalization.
	MaxParallelWriters int `toml:"max_parallel_writers"`
	// OutputStyle selects a persona/tone block folded into the system prompt at
	// startup (a built-in like "explanatory"/"learning"/"concise", or a custom
	// .reasonix/output-styles/<name>.md). Empty = the unmodified prompt.
	OutputStyle string `toml:"output_style"`
	// Deprecated compatibility field. Automatic plan mode was retired in
	// config version 5; old TOML stays readable but loads as "off", and
	// rendering omits it. Plan mode is still an explicit user choice.
	AutoPlan string `toml:"auto_plan"`
	// ReasoningLanguage controls the preferred language for visible reasoning
	// text. Empty/auto follows the conversation language. Applied as transient
	// turn context, not the stable prompt.
	ReasoningLanguage string `toml:"reasoning_language"`
	// Deprecated compatibility field paired with AutoPlan. Old TOML remains
	// readable, but loading clears it and rendering omits it.
	AutoPlanClassifier string `toml:"auto_plan_classifier"`
	// Soft/snip/force are retired compatibility keys; only CompactRatio is active.
	SoftCompactRatio    float64 `toml:"soft_compact_ratio"`
	ToolResultSnipRatio float64 `toml:"tool_result_snip_ratio"`
	CompactRatio        float64 `toml:"compact_ratio"`
	CompactForceRatio   float64 `toml:"compact_force_ratio"`
	// VisibleWindowTokens caps the provider-visible recent verbatim tail below
	// its default 16% of the context window. Zero keeps the default behavior.
	VisibleWindowTokens  int  `toml:"visible_window_tokens"`
	CacheAwareCompaction bool `toml:"cache_aware_compaction"`
	// ContextEditing is retired; native tool clearing is no longer an auto path.
	ContextEditing string `toml:"context_editing"`
	// Keep and RecentKeep are deprecated compatibility fields. They remain
	// readable and writable but Harness-style compaction ignores them.
	Keep       []string `toml:"keep"`
	RecentKeep int      `toml:"recent_keep"`
	// ColdResumePrune elides stale tool results when a session reopens past the
	// provider cache window. nil = default enabled.
	ColdResumePrune *bool `toml:"cold_resume_prune"`
	// PlanModeReadOnlyCommands is retained for old config/session round trips. Main
	// Plan bash calls now use the ordinary Permissions classifier and Sandbox.
	PlanModeReadOnlyCommands []string `toml:"plan_mode_read_only_commands"`
	LegacyAnchorSafetyGate   bool     `toml:"legacy_anchor_safety_gate"`  // retired; decoded for compatibility and ignored
	CompletionValidation     string   `toml:"completion_validation"`      // retired; retained for old config reads
	CompletionEvaluatorModel string   `toml:"completion_evaluator_model"` // retired; ignored
}
