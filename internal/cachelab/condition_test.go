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
