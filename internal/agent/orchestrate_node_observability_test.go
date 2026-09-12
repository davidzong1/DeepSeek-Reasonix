package agent

// The per-node observability contract: when a node ran, and how much proof it
// left. Both are read off the turn's own ledger and event fields, so neither
// can report a body or a claim the ledger does not hold.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"reasonix/internal/evidence"
	"reasonix/internal/tool"
)

// A node's line carries when it ran and how much proof it left. A node the
// graph settled without dispatching still names bounds, so no node reads as one
// that never existed, and a silent node's receipt count stays zero rather than
// inheriting a neighbour's.
func TestOrchestrationReportsTimingAndReceiptCountsPerNode(t *testing.T) {
	plan, err := Compile(spec(ModeSequence, agentNode("a"), agentNode("b", "a"), agentNode("c", "b")),
		ValidateOptions{AllowAgentNodes: true})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	checkpoints := map[string]OrchestrationNodeResult{}
	var mu sync.Mutex
	dispatches := &atomic.Int32{}
	runOptions := NewRunOptions(orchTaskTool(t), nil)
	runOptions.RunAgent = func(_ context.Context, spec ProfileExecSpec) (agentNodeOut, error) {
		dispatches.Add(1)
		if spec.Task.Description == "b" {
			return agentNodeOut{}, errors.New("scripted node failure")
		}
		return agentNodeOut{Result: "ok", Ref: "sa-" + spec.Task.Description}, nil
	}
	runOptions.Checkpoint = func(report OrchestrationNodeResult) {
		mu.Lock()
		defer mu.Unlock()
		if _, seen := checkpoints[report.ID]; !seen {
			checkpoints[report.ID] = report
		}
	}
	result, err := RunOrchestration(orchContext(), plan, runOptions)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, node := range result.Nodes {
		if node.StartedAt == 0 || node.EndedAt < node.StartedAt {
			t.Fatalf("node %s carries bounds %d..%d", node.ID, node.StartedAt, node.EndedAt)
		}
		report, ok := checkpoints[node.ID]
		if !ok {
			t.Fatalf("node %s was never checkpointed", node.ID)
		}
		if report.StartedAt != node.StartedAt || report.EndedAt != node.EndedAt {
			t.Fatalf("node %s: the checkpoint carries %d..%d and the result %d..%d",
				node.ID, report.StartedAt, report.EndedAt, node.StartedAt, node.EndedAt)
		}
		if node.State == NodeSkipped && node.StartedAt != node.EndedAt {
			t.Fatalf("skipped node %s claims %dms of work it never did", node.ID, node.DurationMs())
		}
		if node.ReceiptCount != 0 {
			t.Fatalf("node %s reports %d receipts with no ledger bound", node.ID, node.ReceiptCount)
		}
	}
}

// The receipt count is the ledger's, not the runner's: a node whose work left
// no receipt reports zero even though it completed, which is what stops a plan
// from presenting success as proof.
func TestOrchestrationReceiptCountFollowsTheLedger(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Add(orchWriterTool{calls: &atomic.Int32{}})
	registry.Add(orchReadOnlyTool{})
	plan, err := Compile(spec(ModeSequence,
		NodeSpec{ID: "w", Kind: NodeTool, Tool: "orch_write", ReadOnly: true, Args: json.RawMessage(`{"n":1}`)},
		NodeSpec{ID: "r", Kind: NodeTool, Tool: "orch_read", ReadOnly: true, Needs: []string{"w"}, Args: json.RawMessage(`{}`)}),
		ValidateOptions{AllowAgentNodes: true, Tools: registry})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ledger := evidence.NewLedger()
	ctx := evidence.WithLedger(orchContext(), ledger)
	result, err := RunOrchestration(ctx, plan, NewRunOptions(nil, registry))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, node := range result.Nodes {
		if node.State != NodeCompleted {
			t.Fatalf("node %s = %s, want completed", node.ID, node.State)
		}
		if node.ReceiptCount != 1 {
			t.Fatalf("node %s reports %d receipts, want the one its call recorded", node.ID, node.ReceiptCount)
		}
		if len(node.Receipts) != 1 || node.Receipts[0] != node.Ref {
			t.Fatalf("node %s publishes %v with ref %q", node.ID, node.Receipts, node.Ref)
		}
	}
	// The second node's mark is taken when it starts, so it counts its own call
	// rather than the first node's.
	if result.Nodes[0].ReceiptCount != result.Nodes[1].ReceiptCount {
		t.Fatalf("per-node counts collapsed: %d and %d",
			result.Nodes[0].ReceiptCount, result.Nodes[1].ReceiptCount)
	}
}
