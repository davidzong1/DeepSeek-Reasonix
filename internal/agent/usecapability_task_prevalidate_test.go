package agent

import (
	"context"
	"strings"
	"testing"

	"reasonix/internal/capability"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// prevalidatedTaskAgent assembles an agent whose use_capability proxy resolves
// the real task tool from a registry, with a gate that records every
// permission consultation and a fresh ledger/audit to prove the failed call
// left no authorization trace.
func prevalidatedTaskAgent(t *testing.T) (*Agent, *recordingPermissionGate, *capability.Ledger, *capability.Audit) {
	t.Helper()
	reg := tool.NewRegistry()
	reg.Add(NewTaskToolWithOptions(TaskToolOptions{SysPrompt: "sys"}))
	ledger := capability.NewLedger()
	audit := &capability.Audit{}
	uc := NewUseCapabilityTool(context.Background(), nil, nil, reg, ledger, audit, nil)
	reg.Add(uc)
	gate := &recordingPermissionGate{allow: true}
	return New(&scriptedProvider{name: "p"}, reg, NewSession("sys"), Options{Gate: gate}, event.Discard), gate, ledger, audit
}

// TestTaskCapabilityInvalidArgsFailBeforePermission pins the pre-validation
// contract: a registry-backed task call with broken nested arguments (missing
// prompt, wrong prompt type, non-object arguments) must fail while the proxy
// resolves — before the permission gate runs, before audit records, and before
// the target's Execute — so no authorization outcome (allow_persistent) or any
// other side effect can be produced for it.
func TestTaskCapabilityInvalidArgsFailBeforePermission(t *testing.T) {
	a, gate, ledger, audit := prevalidatedTaskAgent(t)
	cases := []struct {
		name, args string
	}{
		{"missing nested prompt", `{"action":"call","capability_id":"task:subagent","arguments":{"description":"x"}}`},
		{"wrong prompt type", `{"action":"call","capability_id":"task:subagent","arguments":{"prompt":42}}`},
		{"non-object arguments", `{"action":"call","capability_id":"tool:task","arguments":[1,2]}`},
	}
	for _, c := range cases {
		out := a.executeOne(context.Background(), &a.turn, provider.ToolCall{
			ID: "1", Name: "use_capability", Arguments: c.args,
		})
		if out.errMsg == "" {
			t.Errorf("%s: invalid task must fail while resolving, outcome=%+v", c.name, out)
		}
		if len(gate.calls) != 0 {
			t.Errorf("%s: the permission gate must not run for invalid task args, calls=%v", c.name, gate.calls)
		}
		if out.resolved || out.executed {
			t.Errorf("%s: a pre-validation refusal must not resolve or execute a target, outcome=%+v", c.name, out)
		}
	}
	if audit.MCPCall != 0 || audit.MCPCallFailures != 0 || audit.MCPInspect != 0 {
		t.Errorf("pre-validation failures must leave no audit record: %+v", audit.Snapshot())
	}
	if _, ok := ledger.Get("task:subagent"); ok {
		t.Error("pre-validation failures must leave no ledger entry")
	}
}

// TestTaskCapabilityValidArgsStillResolve pins the no-over-reject half: a
// well-formed task call still resolves to the real task tool and reaches the
// permission gate exactly once, exactly as it did before the pre-validation
// existed. (Full sub-agent execution is covered by task_test.go; the pin here
// is that pre-validation never blocks a legal call.)
func TestTaskCapabilityValidArgsStillResolve(t *testing.T) {
	a, gate, _, _ := prevalidatedTaskAgent(t)
	out := a.executeOne(context.Background(), &a.turn, provider.ToolCall{
		ID: "1", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"task:subagent","arguments":{"prompt":"delegate the review"}}`,
	})
	if len(gate.calls) != 1 {
		t.Fatalf("a valid task call must reach the permission gate exactly once, calls=%v", gate.calls)
	}
	if !out.resolved || out.resolvedName != "task" {
		t.Fatalf("valid task call must resolve to the real task tool, outcome=%+v", out)
	}
}

// TestTaskCapabilityMalformedJSONFailsBeforePermission pins malformed JSON at
// both layers: broken top-level JSON is refused by the proxy parser itself,
// and a malformed inner arguments blob is refused at the same pre-permission,
// no-side-effect position as the other invalid-args cases.
func TestTaskCapabilityMalformedJSONFailsBeforePermission(t *testing.T) {
	a, gate, ledger, audit := prevalidatedTaskAgent(t)
	for _, args := range []string{
		`{"action":"call","capability_id":"task:subagent","arguments":{"prompt":"x"}`,
		`not json at all`,
		`{"action":"call","capability_id":"task:subagent","arguments":"{{not json"}`,
	} {
		out := a.executeOne(context.Background(), &a.turn, provider.ToolCall{
			ID: "1", Name: "use_capability", Arguments: args,
		})
		if out.errMsg == "" || len(gate.calls) != 0 || out.resolved || out.executed {
			t.Errorf("args %q: errMsg=%q gate=%v resolved=%v executed=%v — malformed JSON must fail before permission", args, out.errMsg, gate.calls, out.resolved, out.executed)
		}
	}
	if audit.MCPCall != 0 || audit.MCPCallFailures != 0 {
		t.Errorf("malformed JSON must leave no audit record: %+v", audit.Snapshot())
	}
	if _, ok := ledger.Get("task:subagent"); ok {
		t.Error("malformed JSON must leave no ledger entry")
	}
}

// TestLocalToolWithoutValidatorUnchanged pins the incremental contract: a
// local registry tool that does not implement the validator keeps its exact
// old behavior — its own Execute rules apply, and the permission gate runs as
// before.
func TestLocalToolWithoutValidatorUnchanged(t *testing.T) {
	reg := tool.NewRegistry()
	var calls int32
	reg.Add(fakeTool{name: "grep", readOnly: true, calls: &calls})
	uc := NewUseCapabilityTool(context.Background(), nil, nil, reg, capability.NewLedger(), nil, nil)
	reg.Add(uc)
	gate := &recordingPermissionGate{allow: true}
	a := New(&scriptedProvider{name: "p"}, reg, NewSession("sys"), Options{Gate: gate}, event.Discard)

	out := a.executeOne(context.Background(), &a.turn, provider.ToolCall{
		ID: "1", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"tool:grep","arguments":{"pattern":"x"}}`,
	})
	if len(gate.calls) != 1 {
		t.Fatalf("a validator-less local tool must still reach the permission gate, calls=%v", gate.calls)
	}
	if calls != 1 {
		t.Fatalf("a validator-less local tool must still execute, calls=%d", calls)
	}
	if out.errMsg != "" || out.resolvedName != "grep" {
		t.Fatalf("validator-less local tool outcome should be the ordinary execution, outcome=%+v", out)
	}
}

// TestMCPToolArgumentsCrossPermissionUnchanged pins the mcp-tool contract that
// the local-tool pre-validation must not disturb: an object-form arguments call
// resolves to its real authorized target and crosses the permission layer via
// ExplicitlyDenies, not the per-call Check (that is how an authorized MCP server
// is decided).
//
// The nested-JSON-string form this test once accepted is refused now — see
// TestMCPToolNestedJSONStringArgumentsRefused. Argument validation runs before
// the gate, so such a call never executes and never reaches it: fail-closed, not
// a skipped check.
func TestMCPToolArgumentsCrossPermissionUnchanged(t *testing.T) {
	reg := tool.NewRegistry()
	var calls int32
	reg.Add(annotatedMCPTool{fakeTool: fakeTool{name: "mcp__github__search_issues", readOnly: true, calls: &calls}, server: "github", raw: "search_issues", serverAuthorized: true})
	uc := NewUseCapabilityTool(context.Background(), nil, nil, reg, capability.NewLedger(), nil, nil)
	reg.Add(uc)
	gate := &recordingPermissionGate{allow: true}
	a := New(&scriptedProvider{name: "p"}, reg, NewSession("sys"), Options{Gate: gate}, event.Discard)

	out := a.executeOne(context.Background(), &a.turn, provider.ToolCall{
		ID: "1", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-tool:github/search_issues","arguments":{"query":"x"}}`,
	})
	if len(gate.denyCalls) != 1 || len(gate.calls) != 0 {
		t.Fatalf("an mcp-tool call must cross permission via ExplicitlyDenies once, deny=%v check=%v", gate.denyCalls, gate.calls)
	}
	if !out.resolved || out.resolvedName != "mcp__github__search_issues" {
		t.Fatalf("mcp-tool call must resolve to its real target, outcome=%+v", out)
	}
	if calls != 1 {
		t.Fatalf("mcp-tool call must execute, calls=%d", calls)
	}
}

// TestMCPToolNestedJSONStringArgumentsRefused pins the stricter contract that
// replaced the old single-JSON-string compatibility: an MCP tool's arguments must
// be a JSON object. The refusal lands before the permission gate, so the call
// neither executes nor consults it — the safe direction, and the reason a gate
// assertion on this input reads as "no check ran".
func TestMCPToolNestedJSONStringArgumentsRefused(t *testing.T) {
	reg := tool.NewRegistry()
	var calls int32
	reg.Add(annotatedMCPTool{fakeTool: fakeTool{name: "mcp__github__search_issues", readOnly: true, calls: &calls}, server: "github", raw: "search_issues", serverAuthorized: true})
	uc := NewUseCapabilityTool(context.Background(), nil, nil, reg, capability.NewLedger(), nil, nil)
	reg.Add(uc)
	gate := &recordingPermissionGate{allow: true}
	a := New(&scriptedProvider{name: "p"}, reg, NewSession("sys"), Options{Gate: gate}, event.Discard)

	out := a.executeOne(context.Background(), &a.turn, provider.ToolCall{
		ID: "1", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-tool:github/search_issues","arguments":"{\"query\":\"x\"}"}`,
	})
	if out.errMsg == "" {
		t.Fatalf("a nested JSON string must be refused, outcome=%+v", out)
	}
	if !strings.Contains(out.errMsg, "must be a JSON object") {
		t.Fatalf("the refusal must name the contract, errMsg=%q", out.errMsg)
	}
	if calls != 0 || out.executed {
		t.Fatalf("a refused call must not execute, calls=%d executed=%v", calls, out.executed)
	}
	if len(gate.denyCalls) != 0 || len(gate.calls) != 0 {
		t.Fatalf("validation precedes the gate, so nothing crosses it: deny=%v check=%v", gate.denyCalls, gate.calls)
	}
}
