package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/event"
	"reasonix/internal/tool"
)

// OrchestrateTool runs a declarative OrchestrationSpec as a real graph over the
// session's own dispatch paths. It is a host tool rather than a compiler: the
// spec is data, and every node it starts is a call the session would have made
// itself through task or an ordinary tool.
type OrchestrateTool struct {
	taskTool *TaskTool
	tools    *tool.Registry
	// AllowAgentNodes is bound at assembly and is never read from the spec, the
	// schema or the call arguments, so a spec cannot widen it. A team session
	// binds false: the leader already orchestrates.
	AllowAgentNodes bool
	// Budget is this turn's declared ceiling, bound at assembly. Validate reads
	// no spend; the runner re-checks real spend through BudgetCheck.
	Budget TaskBudget
	// BudgetCheck is the per-dispatch spend predicate, bound at assembly where
	// the turn's Agent is reachable. Nil reads as unbounded.
	BudgetCheck func(ctx context.Context) (axis, detail string)
}

// NewOrchestrateTool binds the tool to the session's own task tool, registry and
// capability answer. The bool is the session's agent-node permission and the
// budget pair is its declared ceiling plus its spend predicate.
func NewOrchestrateTool(taskTool *TaskTool, tools *tool.Registry, allowAgentNodes bool, budget TaskBudget, budgetCheck func(context.Context) (string, string)) *OrchestrateTool {
	return &OrchestrateTool{taskTool: taskTool, tools: tools, AllowAgentNodes: allowAgentNodes, Budget: budget, BudgetCheck: budgetCheck}
}

func (*OrchestrateTool) Name() string { return tool.HostOrchestrate }

func (*OrchestrateTool) Description() string {
	return "Run a declarative plan of agent nodes as a dependency graph. The spec is data: a closed mode (sequence|parallel|pipeline), explicit node ids, and needs edges. Every node is a real sub-agent call through the session's own task path, run under the session scheduler and its write-claim rules, so this tool adds no runtime of its own. A node's result is held host-side and addressed downstream as $nodes.<id>.<field>; only ids cross a node boundary. Writers are admitted only in a sequence, where the edges make the order total. Declared-but-unsatisfiable plans are refused before anything starts."
}

func (*OrchestrateTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "spec":{
    "type":"object",
    "description":"The plan. Declarative data only: there is no field for a condition, a loop, or a computed edge.",
    "properties":{
      "version":{"type":"integer","description":"Spec version. Only 1 is accepted.","minimum":1,"maximum":1},
      "mode":{"type":"string","enum":["sequence","parallel","pipeline"],"description":"The plan's declared shape. sequence admits writers because its edges make the order total; parallel and pipeline admit read-only nodes only."},
      "nodes":{
        "type":"array",
        "minItems":1,
        "maxItems":64,
        "description":"The nodes, in declaration order.",
        "items":{
          "type":"object",
          "properties":{
            "id":{"type":"string","description":"Stable id, 1-64 characters of [A-Za-z0-9._-]. Used verbatim and addressable as $nodes.<id>.<field>."},
            "kind":{"type":"string","enum":["agent"],"description":"Node kind. Only agent nodes are available."},
            "needs":{"type":"array","items":{"type":"string"},"description":"Ids this node runs after. Unknown ids, self-edges and cycles are refused."},
            "profile":{"type":"string","description":"Optional runAs=subagent profile name."},
            "prompt":{"type":"string","description":"The node's task prompt."},
            "outputs":{"type":"array","description":"Typed fields this node publishes. Only a declared field is addressable downstream.","items":{"type":"object","properties":{"name":{"type":"string"},"kind":{"type":"string","enum":["scalar","list","ref"]}},"required":["name"]}},
            "max_tokens":{"type":"integer","description":"Optional declared token ceiling for this node.","minimum":0},
            "write_paths":{"type":"array","items":{"type":"string"},"description":"Write targets. A writer is admitted only in a sequence."},
            "read_only":{"type":"boolean","description":"Declared read-only. The effective permission is the conjunction of this and the session's own answer, so this cannot grant a capability."}
          },
          "required":["id","kind"]
        }
      },
      "caps":{"type":"object","description":"A request to narrow the session's own ceilings. A cap may only lower one; a plan that does not fit inside the cap it declared is refused.","properties":{"max_nodes":{"type":"integer","minimum":0},"max_parallel":{"type":"integer","minimum":0},"max_writers":{"type":"integer","minimum":0}}}
    },
    "required":["version","mode","nodes"]
  }
},
"required":["spec"]
}`)
}

// ReadOnly reports false: an agent node may write, and the writer rule is what
// decides whether a given plan is admitted.
func (*OrchestrateTool) ReadOnly() bool { return false }

// planToolNodeKinds is the node kinds this phase admits. A tool or reduce node
// would make the plan's work host-side rather than delegation, and that is a
// later phase rather than an option here.
var planToolNodeKinds = map[NodeKind]bool{NodeAgent: true}

// BindBudgetCheck composes the two unexported primitives §7.3 names into the one
// predicate a run reads. It lives here rather than at the assembly because both
// halves are unexported: taskBudgetLimit resolves the turn's bound and
// runBudget.exceeded names the first axis the turn has spent past. It returns
// nil for a nil agent, which reads as unbounded.
func BindBudgetCheck(a *Agent) func(ctx context.Context) (axis, detail string) {
	if a == nil {
		return nil
	}
	return func(ctx context.Context) (string, string) {
		return a.task.budget.exceeded(a.taskBudgetLimit(ctx))
	}
}

// Execute parses the spec strictly, validates it, compiles it, and runs it.
// Every refusal happens before a node is dispatched, so a rejected plan leaves
// nothing behind.
func (o *OrchestrateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if o == nil || o.taskTool == nil {
		return "", fmt.Errorf("orchestrate is not configured")
	}
	var params struct {
		Spec OrchestrationSpec `json:"spec"`
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&params); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	// Reject what the type has no field for rather than what the schema failed
	// to describe. Version is checked here so the decode-level case names it
	// before the validator does.
	for _, node := range params.Spec.Nodes {
		if !planToolNodeKinds[node.Kind] {
			return "", fmt.Errorf("node %q: kind %q is not available; this tool runs agent nodes", node.ID, node.Kind)
		}
	}
	options := ValidateOptions{AllowAgentNodes: o.AllowAgentNodes, Budget: o.Budget, Tools: o.tools}
	plan, err := Compile(params.Spec, options)
	if err != nil {
		return "", err
	}
	runOptions := NewRunOptions(o.taskTool, o.tools)
	runOptions.BudgetCheck = o.BudgetCheck
	// Progress rides the existing checkpoint seam rather than a store of this
	// tool's own: the runner calls it once per node, in a terminal state.
	runOptions.Checkpoint = func(report OrchestrationNodeResult) {
		emitOrchestrateProgress(ctx, report)
	}
	result, err := RunOrchestration(ctx, plan, runOptions)
	out := formatOrchestrationResult(result)
	if err != nil {
		return out, err
	}
	return out, nil
}

// emitOrchestrateProgress renders one node's terminal state into the call's own
// sink as the same kind of bounded preview fleet's items use, so a frontend
// shows a plan's progress without a second event vocabulary. Timing rides the
// fields the event already carries for a tool call, and the receipt count is a
// number: no node body reaches this channel.
func emitOrchestrateProgress(ctx context.Context, report OrchestrationNodeResult) {
	_, sink, _, ok := CallContext(ctx)
	if !ok || sink == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "- %s [%s] %s", report.ID, report.Kind, report.State)
	if report.Ref != "" {
		fmt.Fprintf(&b, " ref=%s", report.Ref)
	}
	fmt.Fprintf(&b, " receipts=%d", report.ReceiptCount)
	if ms := report.DurationMs(); ms > 0 {
		fmt.Fprintf(&b, " %dms", ms)
	}
	if reason := strings.TrimSpace(report.Err); reason != "" {
		fmt.Fprintf(&b, " (%s)", reason)
	}
	sink.Emit(event.Event{Kind: event.ToolResultPreview, Tool: event.Tool{
		ID: "orchestrate/" + report.ID, Name: "orchestrate", ReadOnly: true,
		Output: b.String(), StartedAt: report.StartedAt, EndedAt: report.EndedAt,
		DurationMs: report.DurationMs(),
	}})
}

// formatOrchestrationResult renders the bounded plan outcome: one line per node
// with its state, timing and receipt count, and the reduction or the stopping
// reason. Node bodies are never inlined here — a caller that wants one
// addresses it by id.
func formatOrchestrationResult(result OrchestrationResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "plan %s: %d nodes", result.PlanHash, len(result.Nodes))
	if axis := strings.TrimSpace(result.BudgetAxis); axis != "" {
		fmt.Fprintf(&b, " (cut by the %s budget)", axis)
	} else if result.Cancelled {
		b.WriteString(" (cancelled)")
	}
	b.WriteByte('\n')
	for _, node := range result.Nodes {
		fmt.Fprintf(&b, "- %s [%s] %s", node.ID, node.Kind, node.State)
		if node.Ref != "" {
			fmt.Fprintf(&b, " ref=%s", node.Ref)
		}
		fmt.Fprintf(&b, " receipts=%d", node.ReceiptCount)
		if ms := node.DurationMs(); ms > 0 {
			fmt.Fprintf(&b, " %dms", ms)
		}
		if reason := strings.TrimSpace(node.Err); reason != "" {
			fmt.Fprintf(&b, " (%s)", reason)
		}
		b.WriteByte('\n')
	}
	for _, value := range result.Reduced {
		fmt.Fprintf(&b, "reduce %s.%s: count=%d", value.Node, value.Field, value.Count)
		if len(value.Refs) > 0 {
			fmt.Fprintf(&b, " refs=%s", strings.Join(value.Refs, ","))
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}
