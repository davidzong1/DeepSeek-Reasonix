package cachelab

import (
	"fmt"
	"sort"
	"strings"
)

// Stage separates the pilot that only checks feasibility from the formal run
// whose sample size is registered before any result is seen.
type Stage string

const (
	StagePilot  Stage = "pilot"
	StageFormal Stage = "formal"
)

// Variable names the single factor an arm is allowed to change. Every other
// factor in the arm is frozen, and the driver records the frozen set with each
// sample so a drifted run is detectable instead of silently pooled.
type Variable string

const (
	// VariableBaseline re-sends identical, frozen request bytes: it establishes
	// whether the provider reports cache reads at all for this account, model
	// and route.
	VariableBaseline Variable = "baseline_repeat"
	// VariableInterval changes only the gap between otherwise identical requests.
	VariableInterval Variable = "request_interval"
	// VariableComponent changes exactly one request component (one tool schema
	// field, the system tail, or the serialization of one schema), then restores
	// the baseline bytes.
	VariableComponent Variable = "single_component"
	// VariableContextSize changes only the pre-frozen request-bytes ladder.
	VariableContextSize Variable = "context_size"
	// VariableRoute changes only the route or account scope. It is gated: it runs
	// only where the configuration is approved and clearly controllable.
	VariableRoute Variable = "route_or_account"
	// VariableVersion compares two named client builds on one machine.
	VariableVersion Variable = "client_build"
)

// Arm is one pre-registered condition. Its fields are the frozen banner the
// driver stamps onto every sample it produces.
type Arm struct {
	ID       string   `json:"id"`
	Stage    Stage    `json:"stage"`
	Variable Variable `json:"variable"`
	// Changed states the one factor this arm moves, in the operator's terms.
	Changed string `json:"changed"`
	// Frozen lists what this arm holds constant. A sample whose observed
	// configuration contradicts it is excluded as a confound.
	Frozen []string `json:"frozen"`
	// WarmTarget is the registered number of eligible warm requests this arm
	// aims for; WarmMin is the floor below which no verdict is issued.
	WarmTarget int `json:"warm_target"`
	WarmMin    int `json:"warm_min"`
	// IntervalMS is the registered gap after each request, for the interval arms.
	IntervalMS int64 `json:"interval_ms,omitempty"`
	// Bytes is the registered request size, for the context ladder arms.
	Bytes int `json:"bytes,omitempty"`
	// Component is the registered perturbation, for the component arms.
	Component string `json:"component,omitempty"`
	// Gated arms do not run from a plain replay: they need an approved
	// configuration, so the driver refuses them unless it is explicitly asked.
	Gated bool `json:"gated,omitempty"`
	// Gate names what an operator must confirm before a gated arm may run.
	Gate string `json:"gate,omitempty"`
	// Conditions names the client conditions this arm may run under. The condition
	// is what the client code does; this arm's Variable is what the request bytes
	// do. Empty means the arm is not registered for a condition comparison.
	Conditions []Condition `json:"conditions,omitempty"`
}

// ConditionOfArm reports the condition a run must be configured for before this
// arm produces comparable samples. A condition arm is the baseline request bytes
// re-run under a different client build, so it carries the condition in its own
// registration: a driver reads the condition from the arm, never from an
// environment variable it could get wrong.
func ConditionOfArm(arm Arm) (Condition, bool) {
	if len(arm.Conditions) == 0 {
		return ConditionBaseline, false
	}
	// The baseline arm is registered under every condition; the one a run is
	// actually in is named by the arm's own id, which the driver selects.
	return arm.Conditions[0], true
}

// ConditionArms returns one arm per registered condition, all carrying the
// baseline variable: the condition matrix is the plan's P3 comparison, and every
// row of it must move exactly the client build and nothing else.
func ConditionArms() []Arm {
	frozenBytes := []string{"member", "provider", "model", "route", "credential scope", "tool surface", "concurrency", "request bytes"}
	out := make([]Arm, 0, len(RegisteredConditions()))
	for _, spec := range RegisteredConditions() {
		out = append(out, Arm{
			ID:       "C-" + string(spec.Condition),
			Stage:    StageFormal,
			Variable: VariableVersion,
			Changed:  "client condition " + string(spec.Condition),
			Frozen:   frozenBytes,
			// A condition arm runs the baseline request bytes, so it needs the same
			// warm-request gate as the baseline it is compared against.
			WarmTarget: FormalWarmTarget,
			WarmMin:    FormalWarmMin,
			Conditions: []Condition{spec.Condition},
			// Every condition arm is gated on a run that actually configured it: a
			// condition claimed without its switches is an unregistered build.
			Gated: true,
			Gate:  conditionGate(spec),
		})
	}
	return out
}

// conditionGate states what an operator must confirm before a condition arm may
// run, in the condition's own terms.
func conditionGate(spec ConditionSpec) string {
	switches := make([]string, 0, len(spec.Switches))
	for _, key := range sortedSwitchKeys(spec.Switches) {
		switches = append(switches, key+"="+spec.Switches[key])
	}
	return fmt.Sprintf("a build configured with %s, on the same member, route, account and request bytes as the baseline",
		strings.Join(switches, ", "))
}

// sortedSwitchKeys lists a switch map's keys in a stable order, so two renders
// of the same registration are byte-identical.
func sortedSwitchKeys(switches map[string]string) []string {
	keys := make([]string, 0, len(switches))
	for key := range switches {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Registered experiment constants. They are fixed before any formal result is
// seen: a pilot result cannot change them, and a verdict is only issued against
// them.
const (
	// PilotWarmTarget is the per-condition pilot size the plan fixes.
	PilotWarmTarget = 8
	// FormalWarmMin is the floor for a formal arm's eligible warm requests.
	FormalWarmMin = 20
	// FormalWarmTarget is the registered formal sample size per arm.
	FormalWarmTarget = 30
	// FormalWarmCap is the registered ceiling: raising it needs a new
	// registration made before the formal results are read.
	FormalWarmCap = 100
	// MinEffectPP is the minimum effect of interest as a token-weighted
	// percentage-point difference between arms. It is a planning threshold
	// chosen before any measurement, not a target or a promise.
	MinEffectPP = 5.0
	// CostCapUSD bounds one experiment run's provider spend.
	CostCapUSD = 25.0
	// MaxConsecutiveErrors stops a run whose provider is failing.
	MaxConsecutiveErrors = 3
	// MaxConsecutiveUsageMissing stops a run whose provider stopped reporting
	// usage: without usage there is no measurement, so continuing only spends.
	MaxConsecutiveUsageMissing = 3
	// ConfoundToolSurfaceDrift marks samples whose provider-visible tool
	// surface moved inside one arm.
	ConfoundToolSurfaceDrift = "tool_surface_drift"
	// ConfoundEndpointProtocol marks samples where the recorder endpoint makes
	// the client derive a different reasoning protocol than the real endpoint.
	ConfoundEndpointProtocol = "endpoint_protocol_divergence"
	// ConfoundAccountPool marks samples that may have been served by another
	// member of a shared account pool.
	ConfoundAccountPool = "shared_account_pool"
)

// Condition names one client build under test. The condition matrix is the
// plan's P3 experiment: a condition is what the client code does, while an Arm's
// Variable is what the request bytes do. Keeping them apart is what lets one
// fixture ladder be re-run under two builds without the registration of either
// changing.
type Condition string

const (
	// ConditionBaseline is the unmodified build: the reference every other
	// condition is measured against.
	ConditionBaseline Condition = "baseline"
	// ConditionAOnly enables the context-maintenance state machine only.
	ConditionAOnly Condition = "a_only"
	// ConditionBOnly enables the provider-visible shape stability only.
	ConditionBOnly Condition = "b_only"
	// ConditionAB enables both.
	ConditionAB Condition = "a_plus_b"
	// ConditionRescue enables context rescue. It validates the extreme-recovery
	// path and is never a routine optimization arm: acting on a rescue rotates the
	// session, so its cost includes a cold prefix by construction.
	ConditionRescue Condition = "rescue_enabled"
)

// ConditionSpec is one condition's registration: what it turns on, which
// switches a run must set to reproduce it, and what it must not change. The
// switches are named rather than applied here, because the switch surface is the
// configuration's, not this package's.
type ConditionSpec struct {
	Condition Condition `json:"condition"`
	// Enables names the client behaviors this condition turns on.
	Enables []string `json:"enables"`
	// Switches are the configuration keys a run sets, with the value it sets them
	// to. They are the reproduction recipe, so a run cannot claim a condition it
	// did not configure.
	Switches map[string]string `json:"switches"`
	// Unchanged names what this condition must leave alone, so a run that drifted
	// is detectable rather than pooled.
	Unchanged []string `json:"unchanged"`
	// NeverRoutine marks a condition that is not an optimization arm.
	NeverRoutine bool `json:"never_routine,omitempty"`
}

// RegisteredConditions is the condition matrix. The three client conditions
// share one switch set: A and B are independent configuration, so enabling one
// must not imply the other.
func RegisteredConditions() []ConditionSpec {
	unchanged := []string{
		"provider-visible request bytes", "cache policy", "context pruning policy",
		"member isolation", "statistics denominator",
	}
	return []ConditionSpec{
		{
			Condition: ConditionBaseline,
			Enables:   []string{"none (the reference build)"},
			Switches:  map[string]string{"agent.cache_aware_compaction": "false", "agent.context_rescue": "false"},
			Unchanged: unchanged,
		},
		{
			Condition: ConditionAOnly,
			Enables:   []string{"context maintenance state machine", "post-fold headroom target"},
			Switches:  map[string]string{"agent.cache_aware_compaction": "true", "agent.context_rescue": "false"},
			Unchanged: unchanged,
		},
		{
			Condition: ConditionBOnly,
			Enables:   []string{"provider-visible shape stability"},
			Switches:  map[string]string{"agent.cache_aware_compaction": "false", "agent.context_rescue": "false"},
			Unchanged: unchanged,
		},
		{
			Condition: ConditionAB,
			Enables:   []string{"context maintenance state machine", "provider-visible shape stability"},
			Switches:  map[string]string{"agent.cache_aware_compaction": "true", "agent.context_rescue": "false"},
			Unchanged: unchanged,
		},
		{
			Condition: ConditionRescue,
			Enables:   []string{"context rescue on an unrecoverable fold"},
			Switches:  map[string]string{"agent.cache_aware_compaction": "true", "agent.context_rescue": "true"},
			Unchanged: unchanged,
			// A rescue rotates the session, so its cold prefix is part of its cost.
			// Running it as a routine arm would report that cost as an optimization.
			NeverRoutine: true,
		},
	}
}

// ConditionByID resolves one registered condition. An unknown id is refused
// rather than defaulted, so a typo cannot silently run an unregistered build.
func ConditionByID(id string) (ConditionSpec, error) {
	id = strings.TrimSpace(id)
	for _, spec := range RegisteredConditions() {
		if string(spec.Condition) == id {
			return spec, nil
		}
	}
	return ConditionSpec{}, fmt.Errorf("cachelab: %q is not a registered condition", id)
}

// Validate refuses a condition whose registration is incomplete: a condition
// that does not name its switches has no reproduction recipe, and a result
// attributed to it would be unfalsifiable.
func (c ConditionSpec) Validate() error {
	switch {
	case strings.TrimSpace(string(c.Condition)) == "":
		return fmt.Errorf("cachelab: condition has no id")
	case len(c.Enables) == 0:
		return fmt.Errorf("cachelab: condition %s does not name what it enables", c.Condition)
	case len(c.Switches) == 0:
		return fmt.Errorf("cachelab: condition %s names no switches, so it has no reproduction recipe", c.Condition)
	case len(c.Unchanged) == 0:
		return fmt.Errorf("cachelab: condition %s does not list what it must leave unchanged", c.Condition)
	}
	return nil
}

// RegisteredArms is the pre-registration itself: the arms a run may execute.
// B0/B1 establish whether the provider reports cache reads under frozen bytes;
// B2/B3/B4 each move exactly one factor; B5/B6 are gated on an approved
// configuration the experiment cannot assume.
func RegisteredArms() []Arm {
	frozenBytes := []string{"member", "provider", "model", "route", "credential scope", "client build", "tool surface", "concurrency"}
	return []Arm{
		{
			ID: "B0-pilot", Stage: StagePilot, Variable: VariableBaseline,
			Changed: "none (feasibility: usage availability, request stability, error rate)",
			Frozen:  frozenBytes, WarmTarget: PilotWarmTarget, WarmMin: PilotWarmTarget,
			IntervalMS: 0,
		},
		{
			ID: "B1-baseline-repeat", Stage: StageFormal, Variable: VariableBaseline,
			Changed: "none (identical frozen bytes, serial, short interval)",
			Frozen:  frozenBytes, WarmTarget: FormalWarmTarget, WarmMin: FormalWarmMin,
			IntervalMS: 0,
			// The baseline arm is the one arm every client condition is measured
			// against, so it is the only arm registered under all of them.
			Conditions: []Condition{ConditionBaseline, ConditionAOnly, ConditionBOnly, ConditionAB, ConditionRescue},
		},
		{
			// The K1 regression guard: a warm response's uncached remainder is the
			// difference of two events, so the expected rate is the provider's own
			// reading, and `prompt == hit + miss` stays closed either way.
			ID: "K1-warm-fold", Stage: StageFormal, Variable: VariableBaseline,
			Changed: "none (the fold regression guard: the warm rate must equal the provider's own reading, not a fold artifact)",
			Frozen:  frozenBytes, WarmTarget: FormalWarmTarget, WarmMin: FormalWarmMin,
			IntervalMS: 0,
		},
		{
			ID: "B2-interval-short", Stage: StageFormal, Variable: VariableInterval,
			Changed: "request interval (short)",
			Frozen:  frozenBytes, WarmTarget: FormalWarmTarget, WarmMin: FormalWarmMin,
			IntervalMS: 2_000,
		},
		{
			ID: "B2-interval-long", Stage: StageFormal, Variable: VariableInterval,
			Changed: "request interval (long)",
			Frozen:  frozenBytes, WarmTarget: FormalWarmTarget, WarmMin: FormalWarmMin,
			IntervalMS: 300_000,
		},
		{
			ID: "B3-tool-schema", Stage: StageFormal, Variable: VariableComponent,
			Changed: "one tool schema field",
			Frozen:  frozenBytes, WarmTarget: FormalWarmTarget, WarmMin: FormalWarmMin,
			Component: "tool_schema_field",
		},
		{
			ID: "B3-system-tail", Stage: StageFormal, Variable: VariableComponent,
			Changed: "system prompt tail clause",
			Frozen:  frozenBytes, WarmTarget: FormalWarmTarget, WarmMin: FormalWarmMin,
			Component: "system_tail",
		},
		{
			ID: "B3-serialization", Stage: StageFormal, Variable: VariableComponent,
			Changed: "one schema's key order",
			Frozen:  frozenBytes, WarmTarget: FormalWarmTarget, WarmMin: FormalWarmMin,
			Component: "schema_key_order",
		},
		{
			ID: "B4-ladder-small", Stage: StageFormal, Variable: VariableContextSize,
			Changed: "frozen request bytes (small)", Frozen: frozenBytes,
			WarmTarget: FormalWarmTarget, WarmMin: FormalWarmMin, Bytes: LadderSmallBytes,
		},
		{
			ID: "B4-ladder-mid", Stage: StageFormal, Variable: VariableContextSize,
			Changed: "frozen request bytes (mid)", Frozen: frozenBytes,
			WarmTarget: FormalWarmTarget, WarmMin: FormalWarmMin, Bytes: LadderMidBytes,
		},
		{
			ID: "B4-ladder-large", Stage: StageFormal, Variable: VariableContextSize,
			Changed: "frozen request bytes (large)", Frozen: frozenBytes,
			WarmTarget: FormalWarmTarget, WarmMin: FormalWarmMin, Bytes: LadderLargeBytes,
		},
		{
			ID: "B5-route-or-account", Stage: StageFormal, Variable: VariableRoute,
			Changed: "route or account scope", Frozen: []string{"member", "model", "request bytes", "client build", "concurrency"},
			WarmTarget: FormalWarmTarget, WarmMin: FormalWarmMin,
			Gated: true, Gate: "an approved route/account pair that stays fixed for the whole arm",
		},
		{
			ID: "B6-client-build", Stage: StageFormal, Variable: VariableVersion,
			Changed: "client build", Frozen: []string{"member", "model", "route", "request bytes", "concurrency"},
			WarmTarget: FormalWarmTarget, WarmMin: FormalWarmMin,
			Gated: true, Gate: "a second named build whose request diff and usage are both traceable",
		},
	}
}

// ArmByID resolves one registered arm. An unknown id is refused rather than
// defaulted, so a typo cannot silently run an unregistered condition.
func ArmByID(id string) (Arm, error) {
	id = strings.TrimSpace(id)
	for _, arm := range RegisteredArms() {
		if arm.ID == id {
			return arm, nil
		}
	}
	return Arm{}, fmt.Errorf("cachelab: %q is not a registered arm", id)
}

// Validate refuses an arm whose registration is incomplete: an unregistered arm
// has no sample size, and a conclusion drawn from it would be unfalsifiable.
func (a Arm) Validate() error {
	switch {
	case strings.TrimSpace(a.ID) == "":
		return fmt.Errorf("cachelab: arm has no id")
	case a.Stage != StagePilot && a.Stage != StageFormal:
		return fmt.Errorf("cachelab: arm %s has stage %q", a.ID, a.Stage)
	case strings.TrimSpace(a.Changed) == "":
		return fmt.Errorf("cachelab: arm %s does not name the factor it changes", a.ID)
	case len(a.Frozen) == 0:
		return fmt.Errorf("cachelab: arm %s does not list its frozen conditions", a.ID)
	case a.WarmMin <= 0 || a.WarmTarget < a.WarmMin:
		return fmt.Errorf("cachelab: arm %s registers warm %d/%d", a.ID, a.WarmTarget, a.WarmMin)
	case a.Stage == StageFormal && a.WarmMin < FormalWarmMin && !a.Gated:
		return fmt.Errorf("cachelab: formal arm %s registers %d warm requests, under the %d floor", a.ID, a.WarmMin, FormalWarmMin)
	case a.Stage == StageFormal && a.WarmTarget > FormalWarmCap:
		return fmt.Errorf("cachelab: formal arm %s registers %d warm requests, over the %d ceiling", a.ID, a.WarmTarget, FormalWarmCap)
	case a.Variable == VariableComponent && strings.TrimSpace(a.Component) == "":
		return fmt.Errorf("cachelab: component arm %s names no component", a.ID)
	case a.Variable == VariableContextSize && a.Bytes <= 0:
		return fmt.Errorf("cachelab: ladder arm %s registers no size", a.ID)
	case a.Gated && strings.TrimSpace(a.Gate) == "":
		return fmt.Errorf("cachelab: gated arm %s names no gate", a.ID)
	}
	for _, condition := range a.Conditions {
		spec, err := ConditionByID(string(condition))
		if err != nil {
			return fmt.Errorf("cachelab: arm %s is registered under %s", a.ID, err)
		}
		// A condition arm carries the condition as its own variable, so it is the
		// registered way to run a never-routine condition. Any other arm under such
		// a condition would be measuring the rotation as if it were an optimization.
		conditionArm := a.Variable == VariableVersion && len(a.Conditions) == 1
		if spec.NeverRoutine && a.Variable != VariableBaseline && !conditionArm {
			return fmt.Errorf("cachelab: arm %s may not run under condition %s: that condition rotates the session, so its cold prefix is part of its cost rather than an optimization",
				a.ID, condition)
		}
	}
	return nil
}

// StopReason returns the registered reason this run must stop, or "" to
// continue. Only registered causes stop a run: a favourable or unfavourable
// interim hit rate is never one of them, and the reason is reported verbatim.
func StopReason(arm Arm, samples []Sample, spendUSD float64, priced bool) string {
	if spendUSD > CostCapUSD && priced {
		return fmt.Sprintf("cost cap reached: %.2f USD of %.2f", spendUSD, CostCapUSD)
	}
	consecutive := 0
	for _, s := range samples {
		switch {
		case s.Error != "":
			consecutive++
		case s.Status >= 200 && s.Status <= 299:
			consecutive = 0
		}
		if consecutive >= MaxConsecutiveErrors {
			return fmt.Sprintf("consecutive service errors: %d", consecutive)
		}
	}
	if n := trailingClass(samples, ClassUsageMissing); n >= MaxConsecutiveUsageMissing {
		return fmt.Sprintf("usage unavailable on the last %d requests", n)
	}
	if anyConfound(samples, ConfoundToolSurfaceDrift) {
		return "provider-visible tool surface drifted inside the arm"
	}
	if anyConfound(samples, ConfoundEndpointProtocol) {
		return "recorder endpoint changed the derived reasoning protocol"
	}
	return ""
}

// trailingClass counts the trailing run of samples of one class.
func trailingClass(samples []Sample, class Class) int {
	n := 0
	for i := len(samples) - 1; i >= 0; i-- {
		if samples[i].Classify() != class {
			break
		}
		n++
	}
	return n
}

// anyConfound reports whether any sample carries the named confound.
func anyConfound(samples []Sample, reason string) bool {
	for _, s := range samples {
		for _, have := range s.Confounds {
			if have == reason {
				return true
			}
		}
	}
	return false
}
