package cachelab

import (
	"fmt"
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
