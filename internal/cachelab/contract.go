package cachelab

import (
	"fmt"
	"strings"
	"time"
)

// Contract is the frozen experiment contract the plan requires before any live
// sample is taken: the snapshot, the arms, the profiles, the effective samples,
// the metrics and the stop rules, all fixed before a result is seen. It is a
// record, not a switch: rendering it changes nothing, and a run that contradicts
// it is detectable rather than silently pooled.
//
// Every field is a string or a list of strings on purpose. A contract whose
// values can be computed is a contract a reader cannot diff against the one that
// was signed, and the point of freezing it is that the diff is trivial.
type Contract struct {
	BaselineCommit string `yaml:"baseline_commit"`
	ContentHash    string `yaml:"content_hash"`
	GoVersion      string `yaml:"go_version"`
	BuildTags      string `yaml:"build_tags"`
	FrozenAt       string `yaml:"frozen_at"`
	// Defaults records what the target deployment actually consumes, which is
	// not the same question as what the code defaults to.
	Defaults []string `yaml:"defaults"`
	// Arms is the matrix, one row per condition.
	Arms []ContractArm `yaml:"arms"`
	// Profiles separates the reachability run from the representative one.
	Profiles []ContractProfile `yaml:"trigger_profiles"`
	// Samples defines what counts, and what is excluded.
	Samples ContractSamples `yaml:"effective_samples"`
	// Metrics are the registered outcomes, in the contract's own words.
	Metrics []string `yaml:"metrics"`
	// StopRules are the registered stopping conditions.
	StopRules []string `yaml:"stop_rules"`
	// Privacy is what may never be written, and what may.
	Privacy []string `yaml:"privacy"`
	// Budget and Credentials bound one run.
	Budget      string `yaml:"budget"`
	Credentials string `yaml:"credentials"`
}

// ContractArm is one condition's frozen configuration.
type ContractArm struct {
	Condition Condition         `yaml:"condition"`
	Switches  map[string]string `yaml:"switches"`
	Enables   []string          `yaml:"enables"`
	Observes  []string          `yaml:"observes"`
}

// ContractProfile is one registered threshold, with the value it sets.
type ContractProfile struct {
	ID                       string  `yaml:"id"`
	CompactRatio             float64 `yaml:"compact_ratio"`
	VisibleWindowTokens      int     `yaml:"visible_window_tokens"`
	ProductionRepresentative bool    `yaml:"production_representative"`
}

// ContractSamples is what makes one observation eligible.
type ContractSamples struct {
	WarmMin    int      `yaml:"warm_min"`
	WarmTarget int      `yaml:"warm_target"`
	Runs       int      `yaml:"runs"`
	Eligible   []string `yaml:"eligible"`
	Excluded   []string `yaml:"excluded"`
	Unknown    []string `yaml:"unknown"`
}

// DraftContract renders the contract from the registrations this package owns,
// with the caller supplying the snapshot facts only it can know. Nothing here is
// a default a run may rely on: the values come from the same registrations the
// matrix executes, so a contract cannot describe arms the code does not have.
func DraftContract(baselineCommit, contentHash, goVersion, frozenAt string) Contract {
	arms := make([]ContractArm, 0, len(RegisteredConditions()))
	for _, spec := range RegisteredConditions() {
		arms = append(arms, ContractArm{
			Condition: spec.Condition,
			Switches:  spec.Switches,
			Enables:   spec.Enables,
			Observes:  spec.Observes,
		})
	}
	profiles := make([]ContractProfile, 0, len(RegisteredTriggerProfiles()))
	for _, profile := range RegisteredTriggerProfiles() {
		profiles = append(profiles, ContractProfile{
			ID: profile.ID, CompactRatio: profile.Ratio,
			VisibleWindowTokens:      profile.VisibleWindowTokens,
			ProductionRepresentative: profile.ProductionRepresentative,
		})
	}
	return Contract{
		BaselineCommit: baselineCommit,
		ContentHash:    contentHash,
		GoVersion:      goVersion,
		BuildTags:      "live",
		FrozenAt:       frozenAt,
		Defaults: []string{
			"agent.low_yield_latch: default enabled, decode-only; an experiment arm sets it explicitly",
			"agent.message_shape_diagnosis: default enabled, decode-only; an experiment arm sets it explicitly",
			"agent.cache_aware_compaction: unchanged upstream default; fixed at one value across the matrix",
			"agent.context_rescue: default disabled; only the rescue condition enables it",
			"agent.compact_ratio: unchanged production default; only the pressure profile lowers it",
		},
		Arms:     arms,
		Profiles: profiles,
		Samples: ContractSamples{
			WarmMin:    FormalWarmMin,
			WarmTarget: FormalWarmTarget,
			Runs:       3,
			Eligible: []string{
				"a warm or repeat sample: a later request whose usage carries a resolved hit/miss split",
				"a sample whose request count was measured rather than defaulted",
			},
			Excluded: []string{
				"first requests: a session's opening request can never be warm",
				"retries: an attempt issued after a failed one for the same turn",
				"errors: a transport or HTTP failure, which is never a zero rate",
				"no_cache_split: a response whose usage carried no resolvable split",
				"usage_missing, usage_estimated and invalid_accounting: readings that cannot support a provider claim",
			},
			Unknown: []string{
				"an absent value stays absent: a missing field is never filled with zero or inferred",
				"a sample whose confounds cannot be ruled out is reported with them, not pooled silently",
			},
		},
		Metrics: []string{
			"miss tokens per request, and the token-weighted hit rate",
			"total input tokens per request",
			"summary requests, projection installs and rewrites per session",
			"cold-start cost, measured separately from the warm rate",
			"task completion, required-context retention and tool-call correctness",
			"p50 and p90 latency, stratified by arm, model and route",
			"usage coverage, and the unknown, error and retry rates",
			"the registered safety events",
		},
		StopRules: []string{
			fmt.Sprintf("consecutive service errors reach %d", MaxConsecutiveErrors),
			fmt.Sprintf("usage is unavailable on %d consecutive requests", MaxConsecutiveUsageMissing),
			"the provider-visible tool surface drifted inside one arm",
			"the recorder endpoint changed the derived reasoning protocol",
			fmt.Sprintf("the run's priced spend passes %.2f USD", CostCapUSD),
			"a hard-ceiling breach, a lost required context, a member cross-talk, or a latency or cost breach: stop the arm, keep the evidence, roll back",
		},
		Privacy: []string{
			"journalled: request digests and lengths, status, latency, the provider's own usage numbers, the marker check",
			"never journalled: prompt or completion text, tool arguments, credentials, file paths, or anything that could restore user content",
			"member records carry no prompt text; the quality outcome is a mechanical marker check, not a model judgement",
		},
		Budget:      fmt.Sprintf("%.2f USD per run, with a separate allowance for failures and retries; reaching it stops the run", CostCapUSD),
		Credentials: "read from the existing secure configuration only; never written to a report, an archive or a journal",
	}
}

// RenderContract renders the contract as YAML. The render is deterministic: the
// maps are flattened through sortedSwitchKeys, so two renders of the same
// registration are byte-identical and a diff means a registration changed.
func RenderContract(c Contract) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# cachelab experiment contract\n")
	fmt.Fprintf(&b, "# frozen before any result was read; a diff against a signed copy is a change of plan\n")
	fmt.Fprintf(&b, "baseline_commit: %s\n", yamlString(c.BaselineCommit))
	fmt.Fprintf(&b, "content_hash: %s\n", yamlString(c.ContentHash))
	fmt.Fprintf(&b, "go_version: %s\n", yamlString(c.GoVersion))
	fmt.Fprintf(&b, "build_tags: %s\n", yamlString(c.BuildTags))
	fmt.Fprintf(&b, "frozen_at: %s\n", yamlString(c.FrozenAt))
	b.WriteString("defaults:\n")
	for _, line := range c.Defaults {
		fmt.Fprintf(&b, "  - %s\n", yamlString(line))
	}
	b.WriteString("arms:\n")
	for _, arm := range c.Arms {
		fmt.Fprintf(&b, "  - condition: %s\n", yamlString(string(arm.Condition)))
		b.WriteString("    switches:\n")
		for _, key := range sortedSwitchKeys(arm.Switches) {
			fmt.Fprintf(&b, "      %s: %s\n", yamlString(key), yamlString(arm.Switches[key]))
		}
		writeYAMLList(&b, "    enables:", arm.Enables)
		writeYAMLList(&b, "    observes:", arm.Observes)
	}
	b.WriteString("trigger_profiles:\n")
	for _, profile := range c.Profiles {
		fmt.Fprintf(&b, "  - id: %s\n", yamlString(profile.ID))
		fmt.Fprintf(&b, "    compact_ratio: %v\n", profile.CompactRatio)
		fmt.Fprintf(&b, "    visible_window_tokens: %d\n", profile.VisibleWindowTokens)
		fmt.Fprintf(&b, "    production_representative: %v\n", profile.ProductionRepresentative)
	}
	b.WriteString("effective_samples:\n")
	fmt.Fprintf(&b, "  warm_min: %d\n  warm_target: %d\n  runs: %d\n", c.Samples.WarmMin, c.Samples.WarmTarget, c.Samples.Runs)
	writeYAMLList(&b, "  eligible:", c.Samples.Eligible)
	writeYAMLList(&b, "  excluded:", c.Samples.Excluded)
	writeYAMLList(&b, "  unknown:", c.Samples.Unknown)
	writeYAMLList(&b, "metrics:", c.Metrics)
	writeYAMLList(&b, "stop_rules:", c.StopRules)
	writeYAMLList(&b, "privacy:", c.Privacy)
	fmt.Fprintf(&b, "budget: %s\n", yamlString(c.Budget))
	fmt.Fprintf(&b, "credentials: %s\n", yamlString(c.Credentials))
	return b.String()
}

// writeYAMLList writes one indented list under its key, or nothing when it is
// empty. The item indent is the key's own plus two, so the document keeps one
// shape whatever depth the key sits at.
func writeYAMLList(b *strings.Builder, key string, items []string) {
	if len(items) == 0 {
		return
	}
	b.WriteString(key + "\n")
	indent := key[:len(key)-len(strings.TrimLeft(key, " "))] + "  "
	for _, item := range items {
		fmt.Fprintf(b, "%s- %s\n", indent, yamlString(item))
	}
}

// yamlString quotes a value so a reader can tell an empty string from an absent
// key, and so a value containing a colon or a hash cannot change the document's
// shape. The quoting is deliberately simple and always applied.
func yamlString(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(value) + `"`
}

// ContractDigest is the content hash a contract is stamped with. It covers the
// registration rather than the prose, so a snapshot can be named by what it
// would execute.
func ContractDigest(c Contract) string {
	parts := []string{c.BaselineCommit, c.GoVersion, c.BuildTags}
	for _, arm := range c.Arms {
		parts = append(parts, string(arm.Condition))
		for _, key := range sortedSwitchKeys(arm.Switches) {
			parts = append(parts, key+"="+arm.Switches[key])
		}
	}
	for _, profile := range c.Profiles {
		parts = append(parts, fmt.Sprintf("%s=%v/%d", profile.ID, profile.CompactRatio, profile.VisibleWindowTokens))
	}
	return ConfigDigest(parts...)
}

// FrozenNow is the instant a contract records as its freeze time, in UTC.
func FrozenNow() string { return time.Now().UTC().Format(time.RFC3339) }
