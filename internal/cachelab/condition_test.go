package cachelab

import (
	"strings"
	"testing"
)

// TestRegisteredConditionsCoverThePlansExperimentMatrix pins the condition
// matrix the plan fixes: the baseline, each client change alone, both together,
// and rescue. A missing row is a condition the experiment cannot run.
func TestRegisteredConditionsCoverThePlansExperimentMatrix(t *testing.T) {
	want := []Condition{ConditionBaseline, ConditionAOnly, ConditionBOnly, ConditionAB, ConditionRescue}
	got := map[Condition]bool{}
	for _, spec := range RegisteredConditions() {
		if err := spec.Validate(); err != nil {
			t.Fatalf("registered condition %s is incomplete: %v", spec.Condition, err)
		}
		got[spec.Condition] = true
	}
	for _, condition := range want {
		if !got[condition] {
			t.Fatalf("condition %q is not registered; the matrix is missing a row", condition)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("registered %d conditions, want exactly %d", len(got), len(want))
	}
}

// TestConditionSwitchesAreAReproductionRecipe pins that every condition names
// the configuration it requires. A condition without its switches is a claim a
// reader cannot reproduce, which is the one thing an experiment arm must never
// be.
func TestConditionSwitchesAreAReproductionRecipe(t *testing.T) {
	for _, spec := range RegisteredConditions() {
		if len(spec.Switches) == 0 {
			t.Fatalf("condition %s names no switches", spec.Condition)
		}
		if len(spec.Unchanged) == 0 {
			t.Fatalf("condition %s does not list what it must leave unchanged", spec.Condition)
		}
	}
	// A-only and B-only must differ: if they carried the same switches, one of the
	// two factors would be unmeasured while appearing to be measured.
	a, err := ConditionByID(string(ConditionAOnly))
	if err != nil {
		t.Fatal(err)
	}
	b, err := ConditionByID(string(ConditionBOnly))
	if err != nil {
		t.Fatal(err)
	}
	joined := func(spec ConditionSpec) string {
		parts := []string{}
		for _, key := range sortedSwitchKeys(spec.Switches) {
			parts = append(parts, key+"="+spec.Switches[key])
		}
		return strings.Join(parts, ";")
	}
	if joined(a) == joined(b) {
		t.Fatalf("A-only and B-only carry identical switches (%s), so neither factor is isolated", joined(a))
	}
}

// TestCacheAwareCompactionIsFixedAcrossTheMatrix pins the matrix's shared-value
// rule: cache_aware_compaction is an existing upstream feature, and a condition
// that moved it would put a prefix-deferral behavior into an arm whose difference
// is then attributable to neither. Every condition must carry the same value for
// it, whatever that value is.
func TestCacheAwareCompactionIsFixedAcrossTheMatrix(t *testing.T) {
	const key = "agent.cache_aware_compaction"
	seen := map[string][]Condition{}
	for _, spec := range RegisteredConditions() {
		value, ok := spec.Switches[key]
		if !ok {
			t.Fatalf("condition %s does not name %s, so a run could not tell whether it moved", spec.Condition, key)
		}
		seen[value] = append(seen[value], spec.Condition)
	}
	if len(seen) != 1 {
		t.Fatalf("%s is not fixed across the routine matrix: %v", key, seen)
	}
}

// TestAOnlyMovesTheLatchAndNothingElse pins A's behavior surface. The classifier
// and the headroom goal are read only by the branch the latch gates, so the latch
// is the whole of what an A-only arm changes — and the observation surface stays
// in every arm, baseline included, which is why a receipt difference is never an
// effect. A second behavior switch appearing here would silently make the
// baseline not a baseline.
func TestAOnlyMovesTheLatchAndNothingElse(t *testing.T) {
	baseline, err := ConditionByID(string(ConditionBaseline))
	if err != nil {
		t.Fatal(err)
	}
	aOnly, err := ConditionByID(string(ConditionAOnly))
	if err != nil {
		t.Fatal(err)
	}
	moved := []string{}
	for _, key := range sortedSwitchKeys(aOnly.Switches) {
		if aOnly.Switches[key] != baseline.Switches[key] {
			moved = append(moved, key)
		}
	}
	want := []string{"agent.low_yield_latch"}
	if len(moved) != len(want) || moved[0] != want[0] {
		t.Fatalf("A-only moves %v against the baseline, want exactly %v: a second behavior change makes the baseline not a baseline", moved, want)
	}
	// The observation surface is reported in every arm, so it is not a factor.
	if len(aOnly.Observes) == 0 || len(baseline.Observes) == 0 {
		t.Fatal("both the baseline and A-only must name what they observe, or an observation would be read as an effect")
	}
	if strings.Join(aOnly.Observes, ";") != strings.Join(baseline.Observes, ";") {
		t.Fatalf("A-only observes %v but the baseline observes %v: the observation surface is not a factor",
			aOnly.Observes, baseline.Observes)
	}
	for _, key := range moved {
		if key == "agent.low_yield_latch" && !strings.Contains(strings.Join(aOnly.Enables, ";"), "latch") {
			t.Fatalf("A-only moves %s but does not declare it as an enabled behavior", key)
		}
	}
}

// TestOnlyTheBaselineEnablesNothing pins the reference condition: a condition
// that turns on no switch is the baseline under another name, and a run could
// claim it while measuring the reference build.
func TestOnlyTheBaselineEnablesNothing(t *testing.T) {
	for _, spec := range RegisteredConditions() {
		enabled := false
		for _, value := range spec.Switches {
			if value == "true" {
				enabled = true
			}
		}
		if spec.Condition == ConditionBaseline && enabled {
			t.Fatalf("the baseline sets a switch to true (%v); it is not the reference build", spec.Switches)
		}
		if spec.Condition != ConditionBaseline && !enabled {
			t.Fatalf("condition %s enables nothing; it is the baseline under another name", spec.Condition)
		}
	}
}

// TestConditionByIDRefusesAnUnregisteredCondition pins that a typo cannot
// silently run an unregistered build.
func TestConditionByIDRefusesAnUnregisteredCondition(t *testing.T) {
	if _, err := ConditionByID("a_and_b_and_more"); err == nil {
		t.Fatal("an unregistered condition id must be refused, not defaulted")
	}
	if _, err := ConditionByID(string(ConditionAB)); err != nil {
		t.Fatalf("registered condition refused: %v", err)
	}
}

// TestConditionArmsMoveOnlyTheClientBuild pins the matrix's single-variable
// property: every condition arm freezes the request bytes, so a measured
// difference can only be the client build.
func TestConditionArmsMoveOnlyTheClientBuild(t *testing.T) {
	arms := ConditionArms()
	if len(arms) != len(RegisteredConditions()) {
		t.Fatalf("condition arms = %d, want one per registered condition", len(arms))
	}
	for _, arm := range arms {
		if err := arm.Validate(); err != nil {
			t.Fatalf("condition arm %s is invalid: %v", arm.ID, err)
		}
		if arm.Variable != VariableVersion {
			t.Fatalf("condition arm %s changes %q, want the client build only", arm.ID, arm.Variable)
		}
		if arm.Bytes != 0 || arm.Component != "" || arm.IntervalMS != 0 {
			t.Fatalf("condition arm %s perturbs the request bytes (%+v); the condition matrix must not", arm.ID, arm)
		}
		if !arm.Gated {
			t.Fatalf("condition arm %s is not gated: it needs a build that actually configured it", arm.ID)
		}
		if !strings.Contains(arm.Gate, "cache_aware_compaction") {
			t.Fatalf("condition arm %s does not name its switches in the gate: %q", arm.ID, arm.Gate)
		}
	}
}

// TestRescueConditionIsNotARoutineArm pins the plan's rule: rescue rotates the
// session, so its cold prefix is part of its cost. It may only ever run as the
// baseline arm under its condition, never as an arm that also perturbs request
// bytes.
func TestRescueConditionIsNotARoutineArm(t *testing.T) {
	rescue, err := ConditionByID(string(ConditionRescue))
	if err != nil {
		t.Fatal(err)
	}
	if !rescue.NeverRoutine {
		t.Fatal("the rescue condition must be marked as never a routine arm")
	}
	// A registered arm that perturbs request bytes under rescue must be refused.
	bad := Arm{
		ID: "B4-ladder-mid", Stage: StageFormal, Variable: VariableContextSize,
		Changed: "frozen request bytes (mid)", Frozen: []string{"member", "model"},
		WarmTarget: FormalWarmTarget, WarmMin: FormalWarmMin, Bytes: LadderMidBytes,
		Conditions: []Condition{ConditionRescue},
	}
	if err := bad.Validate(); err == nil {
		t.Fatal("a request-byte arm must not be registered under the rescue condition")
	}
	// The baseline arm under rescue is fine: the condition itself is the variable.
	ok := Arm{
		ID: "B1-baseline-repeat", Stage: StageFormal, Variable: VariableBaseline,
		Changed: "none", Frozen: []string{"member", "model"},
		WarmTarget: FormalWarmTarget, WarmMin: FormalWarmMin,
		Conditions: []Condition{ConditionRescue},
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("the baseline arm under rescue must be registrable: %v", err)
	}
}

// TestEveryRegisteredArmValidates pins that the pre-registration itself is
// complete: an arm that fails validation has no sample size and could never
// support a verdict.
func TestEveryRegisteredArmValidates(t *testing.T) {
	for _, arm := range RegisteredArms() {
		if err := arm.Validate(); err != nil {
			t.Fatalf("registered arm %s is invalid: %v", arm.ID, err)
		}
	}
	for _, arm := range ConditionArms() {
		if err := arm.Validate(); err != nil {
			t.Fatalf("condition arm %s is invalid: %v", arm.ID, err)
		}
	}
}

// TestRenderReportStatesEachArmsCondition pins that a rendered report says which
// client condition each arm ran under, so two arms of the same bytes are never
// read as one.
func TestRenderReportStatesEachArmsCondition(t *testing.T) {
	arm := ConditionArms()[1]
	samples := []Sample{
		{Arm: arm.ID, Seq: 1, At: "2026-09-24T10:00:00Z", Status: 200, RequestHash: "aa", TurnSeq: 1,
			UsageReported: true, UsageSplit: true, PromptTokens: 1000, CacheHitTokens: 900, CacheMissTokens: 100,
			QualityCheck: QualityPass},
		{Arm: arm.ID, Seq: 2, At: "2026-09-24T10:00:01Z", Status: 200, RequestHash: "aa", TurnSeq: 2,
			UsageReported: true, UsageSplit: true, PromptTokens: 1000, CacheHitTokens: 900, CacheMissTokens: 100,
			QualityCheck: QualityPass},
	}
	rendered := RenderReport([]Arm{arm}, samples, Prices{})
	if !strings.Contains(rendered, string(arm.Conditions[0])) {
		t.Fatalf("the report does not name the arm's condition %q:\n%s", arm.Conditions[0], rendered)
	}
}

// TestTriggerProfilesKeepTheMaintenancePathReachable pins the arithmetic the
// pressure profile depends on: a lowered trigger is only usable when the
// headroom goal fits inside it, because the goal is a share of the window and a
// profile that leaves the goal above the trigger makes every fold latch — which
// would be reported as a maintenance finding when it is the profile's own
// arithmetic. The production profile must not carry an experiment value.
func TestTriggerProfilesKeepTheMaintenancePathReachable(t *testing.T) {
	profiles := RegisteredTriggerProfiles()
	if len(profiles) == 0 {
		t.Fatal("no trigger profile is registered")
	}
	representative := 0
	for _, profile := range profiles {
		if _, err := TriggerProfileByID(profile.ID); err != nil {
			t.Fatalf("registered profile %s does not resolve: %v", profile.ID, err)
		}
		if profile.ProductionRepresentative {
			representative++
			if profile.Ratio != 0 || profile.VisibleWindowTokens != 0 {
				t.Fatalf("profile %s is declared production-representative but carries an experiment value: %+v", profile.ID, profile)
			}
		}
		if profile.Ratio <= 0 {
			continue
		}
		// A 1M window is the member's own: the [1m] alias is what the live driver
		// registers, and it is the widest window the profile could meet.
		const window = 1_000_000
		goal := profile.VisibleWindowTokens
		if goal <= 0 {
			goal = int(float64(window) * 0.16)
		}
		trigger := int(float64(window) * profile.Ratio)
		if goal >= trigger {
			t.Fatalf("profile %s: goal %d does not fit inside trigger %d, so every fold would latch by construction",
				profile.ID, goal, trigger)
		}
	}
	if representative != 1 {
		t.Fatalf("%d profiles claim to be production-representative, want exactly one", representative)
	}
	if _, err := TriggerProfileByID("not-a-profile"); err == nil {
		t.Fatal("an unregistered profile id must be refused, not defaulted")
	}
}
