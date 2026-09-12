package agent

import (
	"reflect"
	"testing"
)

// P1's own claim, and the one that makes "one graph engine" mechanical rather
// than nominal: a spec declaring explicit ids and neither outputs nor a reduce
// node compiles to exactly the fleetPlan the same work expressed directly to
// fleet would build. The fleet side is hand-authored here rather than taken
// from compileItems, because routing both sides through the compiler would
// compare it with itself.
//
// The claim is scoped to explicit ids on purpose: ValidateOrchestration refuses
// an empty id that fleet would have accepted by synthesising a placeholder, so
// an empty-id spec is not equivalent and is not claimed to be.
func TestExplicitIdPlanCompilesToTodaysFleetPlan(t *testing.T) {
	plan, err := Compile(spec(ModeSequence,
		NodeSpec{ID: "survey", Kind: NodeAgent, ReadOnly: true, Prompt: "do survey"},
		NodeSpec{ID: "patch", Kind: NodeAgent, Needs: []string{"survey"}, Prompt: "do patch",
			WritePaths: []string{"src/"}},
		NodeSpec{ID: "review", Kind: NodeAgent, Needs: []string{"patch"}, ReadOnly: true, Prompt: "do review"},
	), ValidateOptions{AllowAgentNodes: true})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	direct, err := newFleetPlan([]fleetTaskItem{
		{Prompt: "do survey", ID: "survey", ReadOnly: true},
		{Prompt: "do patch", ID: "patch", DependsOn: []string{"survey"}, WritePaths: []string{"src/"}},
		{Prompt: "do review", ID: "review", DependsOn: []string{"patch"}, ReadOnly: true},
	}, false)
	if err != nil {
		t.Fatalf("the same work is not expressible to fleet directly: %v", err)
	}
	if !reflect.DeepEqual(plan.graph, direct) {
		t.Fatalf("the compiled graph is not fleet's own:\n compiled = %+v\n direct   = %+v", plan.graph, direct)
	}
	if plan.reduce != nil {
		t.Fatalf("a spec with no reduce node carried an operator: %+v", plan.reduce)
	}
}

// The scope clause above, as a refusal rather than a sentence: fleet accepts an
// empty id by synthesising a position, and the orchestrator does not.
func TestEmptyIdIsRefusedWhereFleetWouldSynthesiseOne(t *testing.T) {
	if _, err := newFleetPlan([]fleetTaskItem{{Prompt: "a"}, {Prompt: "b"}}, false); err != nil {
		t.Fatalf("fleet stopped accepting empty ids, so the divergence this pins is gone: %v", err)
	}
	_, err := Compile(spec(ModeParallel,
		NodeSpec{ID: "", Kind: NodeAgent, ReadOnly: true, Prompt: "a"},
		NodeSpec{ID: "b", Kind: NodeAgent, ReadOnly: true, Prompt: "b"},
	), ValidateOptions{AllowAgentNodes: true})
	if err == nil {
		t.Fatal("an empty node id compiled; $nodes references would resolve to a name no node declares")
	}
}
