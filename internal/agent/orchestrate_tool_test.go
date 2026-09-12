package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/tool"
)

// orchestrateToolFor builds the host tool over a registry carrying one writer
// and one reader, so a refused call can be told apart from one that never had a
// dispatch path.
func orchestrateToolFor(t *testing.T, allowAgentNodes bool) (*OrchestrateTool, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	registry := tool.NewRegistry()
	registry.Add(orchWriterTool{calls: calls})
	registry.Add(orchReadOnlyTool{})
	return NewOrchestrateTool(orchTaskTool(t), registry, allowAgentNodes, TaskBudget{}, nil), calls
}

// A3's decode-level half: the tool receives raw bytes, so the cases that belong
// to `dec.DisallowUnknownFields` are exercised here rather than in the
// compiler's table, which sees an already-parsed struct.
func TestOrchestrateToolRejectsUndecodableArguments(t *testing.T) {
	cases := map[string]struct {
		args string
		want string
	}{
		"unknown field": {`{"spec":{"version":1,"mode":"sequence","nodes":[{"id":"a","kind":"agent","prompt":"do a"}],"script":"rm -rf /"}}`,
			"invalid args"},
		"unknown node field": {`{"spec":{"version":1,"mode":"sequence","nodes":[{"id":"a","kind":"agent","prompt":"do a","loop":3}]}}`,
			"invalid args"},
		"unknown top field": {`{"spec":{"version":1,"mode":"sequence","nodes":[{"id":"a","kind":"agent","prompt":"do a"}]},"budget":10}`,
			"invalid args"},
		"missing spec":  {`{}`, ""},
		"not an object": {`[]`, "invalid args"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			orchestrate, _ := orchestrateToolFor(t, true)
			out, err := orchestrate.Execute(context.Background(), json.RawMessage(tc.args))
			if tc.want == "" {
				// A shape the decoder accepts still has to be refused by the
				// validator, so the guarantee is "no error means no run", not
				// "every malformed input fails to decode".
				if err == nil && strings.Contains(out, "plan ") {
					t.Fatalf("%s ran a plan: %s", name, out)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s was accepted: %s", name, out)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s returned %q, want a %q refusal", name, err, tc.want)
			}
		})
	}
}

// A wrong version is refused by the validator, but it must be refused before a
// node is dispatched, and the tool's own schema is the only place a model reads
// the version it may declare.
func TestOrchestrateToolRefusesAVersionItDoesNotImplement(t *testing.T) {
	orchestrate, calls := orchestrateToolFor(t, true)
	_, err := orchestrate.Execute(context.Background(),
		json.RawMessage(`{"spec":{"version":2,"mode":"sequence","nodes":[{"id":"a","kind":"agent","prompt":"do a"}]}}`))
	if err == nil {
		t.Fatal("version 2 was accepted")
	}
	if calls.Load() != 0 {
		t.Fatalf("%d tool calls ran for a refused spec", calls.Load())
	}
	var schema struct {
		Properties struct {
			Spec struct {
				Properties struct {
					Version struct {
						Maximum int `json:"maximum"`
					} `json:"version"`
				} `json:"properties"`
			} `json:"spec"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(orchestrate.Schema(), &schema); err != nil {
		t.Fatalf("the tool's schema is not decodable: %v", err)
	}
	if got := schema.Properties.Spec.Properties.Version.Maximum; got != OrchestrationSpecVersion {
		t.Fatalf("the schema offers versions up to %d, want %d", got, OrchestrationSpecVersion)
	}
}

// A kind the tool does not run is refused by name at the tool boundary, so a
// caller learns which kinds exist rather than watching a plan half-run.
func TestOrchestrateToolRefusesNodeKindsItDoesNotRun(t *testing.T) {
	for _, kind := range []string{"tool", "reduce", "script"} {
		orchestrate, calls := orchestrateToolFor(t, true)
		args := `{"spec":{"version":1,"mode":"sequence","nodes":[{"id":"a","kind":"` + kind + `","prompt":"do a","tool":"orch_write"}]}}`
		_, err := orchestrate.Execute(context.Background(), json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), "not available") {
			t.Fatalf("kind %q returned %v, want a named refusal", kind, err)
		}
		if calls.Load() != 0 {
			t.Fatalf("kind %q ran %d tool calls", kind, calls.Load())
		}
	}
}

// A4's honest report at the tool boundary: the plan's own line names each
// node's receipt count, so a caller cannot read "completed" as "proven". The
// call's result stays bounded text — no node body is inlined.
func TestOrchestrateToolReportsReceiptCountsWithoutBodies(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Add(orchReadOnlyTool{})
	plan, err := Compile(spec(ModeSequence, agentNode("a")), ValidateOptions{AllowAgentNodes: true, Tools: registry})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	result, err := RunOrchestration(orchContext(), plan, NewRunOptions(nil, registry))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	out := formatOrchestrationResult(result)
	if !strings.Contains(out, "receipts=0") {
		t.Fatalf("a node that left no receipt is not reported as such:\n%s", out)
	}
	if strings.Contains(out, "sa-a") || strings.Contains(out, "body of") {
		t.Fatalf("the formatted result inlined a body or a handle:\n%s", out)
	}
}

// The progress preview reuses the event's own timing fields and stays
// content-free: the line is a state, never a node's answer.
func TestOrchestrateProgressCarriesTimingAndStaysBounded(t *testing.T) {
	var seen []event.Event
	sink := event.Sink(eventSinkFunc(func(e event.Event) { seen = append(seen, e) }))
	report := OrchestrationNodeResult{
		ID: "a", Kind: NodeAgent, State: NodeCompleted, Ref: "sa-a",
		ReceiptCount: 2, StartedAt: 1_000, EndedAt: 1_040, Output: "the whole node body",
	}
	emitOrchestrateProgress(withCallContext(context.Background(), "call-1", sink, nil, false), report)
	if len(seen) != 1 {
		t.Fatalf("emitted %d events, want 1", len(seen))
	}
	got := seen[0]
	if got.Kind != event.ToolResultPreview {
		t.Fatalf("kind = %v, want ToolResultPreview", got.Kind)
	}
	if got.Tool.DurationMs != 40 || got.Tool.StartedAt != 1_000 || got.Tool.EndedAt != 1_040 {
		t.Fatalf("timing = %dms (%d..%d), want 40ms (1000..1040)",
			got.Tool.DurationMs, got.Tool.StartedAt, got.Tool.EndedAt)
	}
	if !strings.Contains(got.Tool.Output, "receipts=2") || !strings.Contains(got.Tool.Output, "40ms") {
		t.Fatalf("the preview omits timing or receipts: %q", got.Tool.Output)
	}
	if strings.Contains(got.Tool.Output, "the whole node body") {
		t.Fatalf("the preview carried the node's body: %q", got.Tool.Output)
	}
}

type eventSinkFunc func(event.Event)

func (f eventSinkFunc) Emit(e event.Event) { f(e) }
