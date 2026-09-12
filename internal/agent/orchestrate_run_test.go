package agent

// P1 acceptance tests for the orchestration runner: the four closed shapes run
// real nodes over fleet's own engine, permissions come from the compiled view,
// and a run's receipts decide the verdict rather than its own bookkeeping.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/completion"
	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// orchRecordTool accepts the extra field a resolved reference lands in, so a
// test can read whether a value crossed as an id or as a producer's body.
type orchRecordTool struct {
	mu    sync.Mutex
	calls int
	args  []string
}

func (o *orchRecordTool) Name() string        { return "orch_record" }
func (o *orchRecordTool) Description() string { return "stub" }
func (o *orchRecordTool) ReadOnly() bool      { return true }
func (o *orchRecordTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"},"v":{"type":"string"}},"additionalProperties":false}`)
}

func (o *orchRecordTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	o.args = append(o.args, string(args))
	return "recorded", nil
}

func (o *orchRecordTool) lastArgs() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.args) == 0 {
		return ""
	}
	return o.args[len(o.args)-1]
}

// orchWriterTool is writer-classified by name, so a successful call leaves a
// content-mutation receipt the completion report must refuse to call verified.
type orchWriterTool struct{ calls *atomic.Int32 }

func (o orchWriterTool) Name() string        { return "orch_write" }
func (o orchWriterTool) Description() string { return "stub" }
func (o orchWriterTool) ReadOnly() bool      { return false }
func (o orchWriterTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}},"additionalProperties":false}`)
}

func (o orchWriterTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	o.calls.Add(1)
	return "written", nil
}

// orchBashTool is the verification arm: its receipt carries the command, which
// is what turns a run's receipts into a proven tree.
type orchBashTool struct{ calls *atomic.Int32 }

func (o orchBashTool) Name() string        { return "bash" }
func (o orchBashTool) Description() string { return "stub" }
func (o orchBashTool) ReadOnly() bool      { return true }
func (o orchBashTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"additionalProperties":false}`)
}

func (o orchBashTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	o.calls.Add(1)
	return "ok", nil
}

// orchReadOnlyTool gives a sub-agent one callable tool, which the sub-agent
// registry requires: a child with nothing to call is refused before it starts.
type orchReadOnlyTool struct{}

func (o orchReadOnlyTool) Name() string        { return "orch_read" }
func (o orchReadOnlyTool) Description() string { return "stub" }
func (o orchReadOnlyTool) ReadOnly() bool      { return true }
func (o orchReadOnlyTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func (o orchReadOnlyTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "read", nil
}

// orchTaskTool is the real TaskTool path with no network behind it: enough for
// buildTaskSpec, a real sub-agent registry and a real transcript store.
func orchTaskTool(t *testing.T) *TaskTool {
	t.Helper()
	registry := tool.NewRegistry()
	registry.Add(orchReadOnlyTool{})
	return NewTaskTool(&orchProvider{}, nil, registry, 20, 0, 0, 0, 0, 0, 0, 0.0, "", "sys", nil, 0, "", "", nil).
		WithTranscripts(mustSubagentStore(t), t.TempDir(), "base", "high")
}

// orchRunningTask builds a task tool around a provider that decides when node
// calls overlap, with the session scheduler the caller names.
func orchRunningTask(t *testing.T, prov provider.Provider, scheduler *SubagentScheduler) *TaskTool {
	t.Helper()
	registry := tool.NewRegistry()
	registry.Add(orchReadOnlyTool{})
	return NewTaskTool(prov, nil, registry, 20, 0, 0, 0, 0, 0, 0, 0.0, "", "sys", nil, 0, "", "", nil).
		WithTranscripts(mustSubagentStore(t), t.TempDir(), "base", "high").
		WithScheduler(scheduler)
}

// orchProvider answers an agent node from its prompt, so the real RunProfileSpec
// path can be exercised without a network.
type orchProvider struct{}

func (p *orchProvider) Name() string { return "orchestrate" }

func (p *orchProvider) Stream(_ context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 2)
	ch <- provider.Chunk{Type: provider.ChunkText, Text: "done " + lastUserMessage(req)}
	ch <- provider.Chunk{Type: provider.ChunkDone}
	close(ch)
	return ch, nil
}

func lastUserMessage(req provider.Request) string {
	last := ""
	for _, m := range req.Messages {
		if m.Role == provider.RoleUser {
			last = m.Content
		}
	}
	return last
}

// orchAgentSeam answers agent nodes without a provider, and counts dispatches so
// a retry is a measurement rather than a claim.
func orchAgentSeam(dispatches *atomic.Int32, refPrefix string) func(context.Context, ProfileExecSpec) (agentNodeOut, error) {
	return func(_ context.Context, spec ProfileExecSpec) (agentNodeOut, error) {
		dispatches.Add(1)
		if strings.Contains(spec.Task.Objective, "FAIL") {
			return agentNodeOut{}, errors.New("scripted node failure")
		}
		return agentNodeOut{Result: "body of " + spec.Task.Description, Ref: refPrefix + spec.Task.Description}, nil
	}
}

func orchContext() context.Context {
	return withCallContext(context.Background(), "orch-call", event.Discard, nil, false)
}

// A sequence carries one node's published id into the next node's arguments.
// The value that crosses is the handle: the producer's body never appears.
func TestOrchestrationSequenceCarriesIdsNotBodies(t *testing.T) {
	calls := &atomic.Int32{}
	registry := tool.NewRegistry()
	recorder := &orchRecordTool{}
	registry.Add(recorder)
	options := ValidateOptions{AllowAgentNodes: true, Tools: registry}
	plan, err := Compile(spec(ModeSequence,
		NodeSpec{ID: "a", Kind: NodeAgent, ReadOnly: true, Prompt: "do a",
			Outputs: []FieldSpec{{Name: "out", Kind: FieldRef}}},
		NodeSpec{ID: "b", Kind: NodeTool, Tool: "orch_record", ReadOnly: true, Needs: []string{"a"},
			Args: json.RawMessage(`{"n":1,"v":"$nodes.a.out"}`)},
	), options)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	runOptions := NewRunOptions(orchTaskTool(t), registry)
	runOptions.RunAgent = orchAgentSeam(calls, "sa-")
	result, err := RunOrchestration(orchContext(), plan, runOptions)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.Completed() || len(result.Nodes) != 2 {
		t.Fatalf("run did not complete: %+v", result.Nodes)
	}
	if result.Nodes[0].Ref != "sa-a" {
		t.Fatalf("node a published ref %q, want sa-a", result.Nodes[0].Ref)
	}
	if got := recorder.lastArgs(); !strings.Contains(got, `"v":"sa-a"`) {
		t.Fatalf("the reference did not resolve to the published id: %s", got)
	}
	if got := recorder.lastArgs(); strings.Contains(got, "body of") {
		t.Fatalf("a producer's body crossed the node boundary: %s", got)
	}
	for _, node := range result.Nodes {
		if node.State != NodeCompleted || node.Attempts != 1 {
			t.Fatalf("node %s = %s after %d attempts", node.ID, node.State, node.Attempts)
		}
	}
}

// A declared field resolves only once its producer actually published a handle:
// a producer that returned no reference leaves the downstream node a refusal
// rather than a raw "$nodes." string passed to a tool as if it were an id.
func TestOrchestrationRefusesAnUnpublishedReference(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Add(&orchRecordTool{})
	plan, err := Compile(spec(ModeSequence,
		NodeSpec{ID: "a", Kind: NodeAgent, ReadOnly: true, Prompt: "do a",
			Outputs: []FieldSpec{{Name: "out", Kind: FieldRef}}},
		NodeSpec{ID: "b", Kind: NodeTool, Tool: "orch_record", ReadOnly: true, Needs: []string{"a"},
			Args: json.RawMessage(`{"n":1,"v":"$nodes.a.out"}`)}),
		ValidateOptions{AllowAgentNodes: true, Tools: registry})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	runOptions := NewRunOptions(orchTaskTool(t), registry)
	runOptions.RunAgent = func(context.Context, ProfileExecSpec) (agentNodeOut, error) {
		return agentNodeOut{Result: "answered without a reference"}, nil
	}
	result, err := RunOrchestration(orchContext(), plan, runOptions)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Nodes[1].State != NodeFailed {
		t.Fatalf("node b = %s, want failed", result.Nodes[1].State)
	}
	if !strings.Contains(result.Nodes[1].Err, "was not published") {
		t.Fatalf("node b carries %q, which does not name the unpublished reference", result.Nodes[1].Err)
	}
	if id, field, ok := parseNodeRefString("$nodes.a.out"); !ok || id != "a" || field != "out" {
		t.Fatalf("parseNodeRefString read %q/%q (ok=%v)", id, field, ok)
	}
	if _, _, ok := parseNodeRefString("prefix $nodes.a.out"); ok {
		t.Fatal("a string that is not exactly one reference was read as one")
	}
}

// A pipeline chains its nodes one to the next, and every node runs exactly once.
func TestOrchestrationPipelineRunsTheChainOnce(t *testing.T) {
	recorder := &orchRecordTool{}
	registry := tool.NewRegistry()
	registry.Add(recorder)
	plan, err := Compile(spec(ModePipeline,
		NodeSpec{ID: "a", Kind: NodeTool, Tool: "orch_record", ReadOnly: true, Args: json.RawMessage(`{"n":1}`)},
		NodeSpec{ID: "b", Kind: NodeTool, Tool: "orch_record", ReadOnly: true, Needs: []string{"a"}, Args: json.RawMessage(`{"n":2}`)},
		NodeSpec{ID: "c", Kind: NodeTool, Tool: "orch_record", ReadOnly: true, Needs: []string{"b"}, Args: json.RawMessage(`{"n":3}`)},
	), ValidateOptions{AllowAgentNodes: true, Tools: registry})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	result, err := RunOrchestration(orchContext(), plan, NewRunOptions(nil, registry))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.Completed() {
		t.Fatalf("the chain did not complete: %+v", result.Nodes)
	}
	if recorder.calls != 3 {
		t.Fatalf("the chain executed %d tool calls, want 3", recorder.calls)
	}
	if result.PlanHash == "" || result.PlanHash != plan.Hash() {
		t.Fatalf("result hash %q does not name the plan %q", result.PlanHash, plan.Hash())
	}
}

// applyReduce is the closed operator set over ids. Every arm keeps references
// and none can carry content, which is what makes a successor unreachable.
func TestOrchestrationReduceOperatorsPublishIdsOnly(t *testing.T) {
	outputs := []FieldSpec{{Name: "out", Kind: FieldList}}
	inputs := []reduceInput{
		{Node: "a", Refs: []string{"sa_a"}, Completed: true},
		{Node: "b", Refs: []string{"sa_b"}, Completed: false},
	}
	cases := map[ReduceOp]struct {
		count int
		refs  []string
		pairs int
	}{
		ReduceSelect: {2, []string{"sa_a", "sa_b"}, 0},
		ReduceCount:  {2, []string{"sa_a", "sa_b"}, 0},
		ReduceFilter: {1, []string{"sa_a"}, 0},
		ReduceMap:    {0, nil, 2},
	}
	for op, want := range cases {
		values := applyReduce(op, "r", outputs, inputs)
		if len(values) != 1 || values[0].Count != want.count {
			t.Fatalf("%s published %+v, want count %d", op, values, want.count)
		}
		if strings.Join(values[0].Refs, ",") != strings.Join(want.refs, ",") {
			t.Fatalf("%s kept %v, want %v", op, values[0].Refs, want.refs)
		}
		if len(values[0].Pairs) != want.pairs {
			t.Fatalf("%s paired %v, want %d pairs", op, values[0].Pairs, want.pairs)
		}
		for _, ref := range values[0].Refs {
			if !strings.HasPrefix(ref, "sa_") {
				t.Fatalf("%s published %q, which is not a handle", op, ref)
			}
		}
	}
}

// A fanout_reduce plan runs its producers and then its one reduce node, and the
// plan itself carries the operator, so the runner takes no detached spec.
func TestOrchestrationFanoutReduceRunsTheClosedOperator(t *testing.T) {
	dispatches := &atomic.Int32{}
	plan, err := Compile(OrchestrationSpec{Version: OrchestrationSpecVersion, Mode: ModeFanoutReduce,
		Reduce: &ReduceSpec{Operator: ReduceSelect},
		Nodes: []NodeSpec{
			{ID: "a", Kind: NodeAgent, ReadOnly: true, Prompt: "do a", Outputs: []FieldSpec{{Name: "out", Kind: FieldRef}}},
			{ID: "b", Kind: NodeAgent, ReadOnly: true, Prompt: "do b", Outputs: []FieldSpec{{Name: "out", Kind: FieldRef}}},
			{ID: "r", Kind: NodeReduce, ReadOnly: true, Needs: []string{"a", "b"},
				Outputs: []FieldSpec{{Name: "all", Kind: FieldList}}},
		}}, ValidateOptions{AllowAgentNodes: true})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	held, ok := plan.Reduce()
	if !ok || held.Operator != ReduceSelect {
		t.Fatalf("the plan does not carry its operator: %+v (ok=%v)", held, ok)
	}
	runOptions := NewRunOptions(orchTaskTool(t), nil)
	runOptions.RunAgent = orchAgentSeam(dispatches, "sa_")
	result, err := RunOrchestration(orchContext(), plan, runOptions)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.Completed() {
		t.Fatalf("the plan did not complete: %+v", result.Nodes)
	}
	if dispatches.Load() != 2 {
		t.Fatalf("the plan dispatched %d agent nodes, want 2", dispatches.Load())
	}
	if len(result.Reduced) != 1 || result.Reduced[0].Field != "all" {
		t.Fatalf("reduced = %+v, want one value on the declared field", result.Reduced)
	}
	if got := strings.Join(result.Reduced[0].Refs, ","); got != "sa_a,sa_b" {
		t.Fatalf("the reduction kept %q, want the two published ids", got)
	}
	if strings.Contains(result.Nodes[2].Output, "body of") {
		t.Fatalf("the reduction read a producer's body: %s", result.Nodes[2].Output)
	}
}

// The second budget check runs before every dispatch. A cut node is never
// dispatched, so it lands as skipped with the axis that cut the plan.
func TestOrchestrationBudgetCutSkipsUndispatchedNodes(t *testing.T) {
	recorder := &orchRecordTool{}
	registry := tool.NewRegistry()
	registry.Add(recorder)
	plan, err := Compile(spec(ModeSequence,
		NodeSpec{ID: "a", Kind: NodeTool, Tool: "orch_record", ReadOnly: true, Args: json.RawMessage(`{"n":1}`)},
		NodeSpec{ID: "b", Kind: NodeTool, Tool: "orch_record", ReadOnly: true, Needs: []string{"a"}, Args: json.RawMessage(`{"n":2}`)},
		NodeSpec{ID: "c", Kind: NodeTool, Tool: "orch_record", ReadOnly: true, Needs: []string{"b"}, Args: json.RawMessage(`{"n":3}`)},
	), ValidateOptions{AllowAgentNodes: true, Tools: registry})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	checks := &atomic.Int32{}
	runOptions := NewRunOptions(nil, registry)
	runOptions.BudgetCheck = func(context.Context) (string, string) {
		if checks.Add(1) > 1 {
			return "tokens", "spent 100 of 100 tokens"
		}
		return "", ""
	}
	result, err := RunOrchestration(orchContext(), plan, runOptions)
	if err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("a cut plan returned %v, want a budget error", err)
	}
	if result.BudgetAxis != "tokens" {
		t.Fatalf("budget axis = %q, want tokens", result.BudgetAxis)
	}
	if recorder.calls != 1 {
		t.Fatalf("a cut plan executed %d tool calls, want 1", recorder.calls)
	}
	if result.Nodes[0].State != NodeCompleted {
		t.Fatalf("node a = %s, want completed", result.Nodes[0].State)
	}
	for _, node := range result.Nodes[1:] {
		if node.State != NodeSkipped {
			t.Fatalf("node %s = %s, want skipped: it was never dispatched", node.ID, node.State)
		}
		if !strings.Contains(node.Err, "skipped") {
			t.Fatalf("node %s carries %q, which does not name the skip", node.ID, node.Err)
		}
	}
	if result.Completed() {
		t.Fatal("a cut plan reported itself complete")
	}
}

// orchHoldProvider blocks until its context ends, so a cancellation is observed
// at a known instant.
type orchHoldProvider struct{ started chan struct{} }

func (p *orchHoldProvider) Name() string { return "orchestrate-hold" }

func (p *orchHoldProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	p.started <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}

// Cancellation splits the plan by what actually ran: a dispatched node is
// cancelled, and a node the plan never got to is skipped.
func TestOrchestrationCancellationSeparatesDispatchedFromUnreached(t *testing.T) {
	prov := &orchHoldProvider{started: make(chan struct{}, 1)}
	task := orchRunningTask(t, prov, NewSubagentScheduler(4, 4))
	plan, err := Compile(spec(ModeSequence, agentNode("a"), agentNode("b", "a"), agentNode("c", "b")),
		ValidateOptions{AllowAgentNodes: true})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ctx, cancel := context.WithCancel(orchContext())
	go func() {
		<-prov.started
		cancel()
	}()
	result, err := RunOrchestration(ctx, plan, NewRunOptions(task, nil))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run returned %v, want context.Canceled", err)
	}
	if result.Nodes[0].State != NodeCancelled {
		t.Fatalf("the dispatched node = %s, want cancelled", result.Nodes[0].State)
	}
	for _, node := range result.Nodes[1:] {
		if node.State != NodeSkipped {
			t.Fatalf("node %s = %s, want skipped", node.ID, node.State)
		}
	}
	if !result.Cancelled {
		t.Fatal("a cancelled run did not report itself cancelled")
	}
}

// A failed node cuts its own branch and nothing else: the dependent is skipped
// and never dispatched.
func TestOrchestrationFailureSkipsOnlyTheDependents(t *testing.T) {
	plan, err := Compile(spec(ModePipeline, agentNode("a"), agentNode("b", "a"), agentNode("c", "b")),
		ValidateOptions{AllowAgentNodes: true})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	dispatches := &atomic.Int32{}
	runOptions := NewRunOptions(orchTaskTool(t), nil)
	runOptions.RunAgent = func(_ context.Context, spec ProfileExecSpec) (agentNodeOut, error) {
		dispatches.Add(1)
		if spec.Task.Description == "b" {
			return agentNodeOut{}, errors.New("scripted node failure")
		}
		return agentNodeOut{Result: "ok", Ref: "sa-" + spec.Task.Description}, nil
	}
	result, err := RunOrchestration(orchContext(), plan, runOptions)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Nodes[0].State != NodeCompleted {
		t.Fatalf("node a = %s, want completed", result.Nodes[0].State)
	}
	if result.Nodes[1].State != NodeFailed {
		t.Fatalf("node b = %s, want failed", result.Nodes[1].State)
	}
	if result.Nodes[2].State != NodeSkipped || !strings.Contains(result.Nodes[2].Err, `"b"`) {
		t.Fatalf("node c = %s (%q), want skipped by its dependency", result.Nodes[2].State, result.Nodes[2].Err)
	}
	if dispatches.Load() != 2 {
		t.Fatalf("the plan dispatched %d nodes, want only a and b", dispatches.Load())
	}
}

// Every node reaches a checkpoint exactly once, in a terminal state, so a
// recovered run can tell which steps finished without re-deriving the graph.
func TestOrchestrationCheckpointsEveryNodeOnce(t *testing.T) {
	plan, err := Compile(spec(ModePipeline, agentNode("a"), agentNode("b", "a"), agentNode("c", "b")),
		ValidateOptions{AllowAgentNodes: true})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	var mu sync.Mutex
	seen := map[string][]OrchestrationNodeState{}
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
		seen[report.ID] = append(seen[report.ID], report.State)
	}
	if _, err := RunOrchestration(orchContext(), plan, runOptions); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("checkpoints covered %d nodes, want 3: %+v", len(seen), seen)
	}
	for id, states := range seen {
		if len(states) != 1 {
			t.Fatalf("node %s was checkpointed %d times: %v", id, len(states), states)
		}
		switch states[0] {
		case NodeCompleted, NodeFailed, NodeCancelled, NodeSkipped:
		default:
			t.Fatalf("node %s checkpointed in the non-terminal state %s", id, states[0])
		}
	}
	if seen["b"][0] != NodeFailed || seen["c"][0] != NodeSkipped {
		t.Fatalf("checkpoint states = %+v, want b failed and c skipped", seen)
	}
}

// A retryable, side-effect-free failure earns exactly one replay; the same
// failure on a writer is never replayed, because replaying a mutation is the
// ordinary recovery path rather than a loop here.
func TestOrchestrationRetriesOnceAndOnlyWhenSideEffectFree(t *testing.T) {
	unrecoverable := NewSubagentRunError(nil, io.ErrUnexpectedEOF)
	if !retryableNodeFailure(unrecoverable) {
		t.Fatal("the fixture error is not retryable, so the case would prove nothing")
	}
	surface := newToolSurface()
	cases := map[string]struct {
		node NodeSpec
		want int
	}{
		"read-only": {NodeSpec{ID: "a", Kind: NodeAgent, ReadOnly: true, Prompt: "do a"}, 2},
		"writer": {NodeSpec{ID: "a", Kind: NodeAgent, Prompt: "do a",
			WritePaths: []string{"src/"}}, 1},
	}
	for name, tc := range cases {
		plan, err := Compile(spec(ModeSequence, tc.node), optionsFor(surface))
		if err != nil {
			t.Fatalf("%s: compile: %v", name, err)
		}
		dispatches := &atomic.Int32{}
		runOptions := NewRunOptions(orchTaskTool(t), nil)
		runOptions.RunAgent = func(context.Context, ProfileExecSpec) (agentNodeOut, error) {
			dispatches.Add(1)
			return agentNodeOut{}, unrecoverable
		}
		result, err := RunOrchestration(orchContext(), plan, runOptions)
		if err != nil {
			t.Fatalf("%s: run: %v", name, err)
		}
		if got := int(dispatches.Load()); got != tc.want || result.Nodes[0].Attempts != tc.want {
			t.Fatalf("%s: %d dispatches / %d attempts, want %d", name, got, result.Nodes[0].Attempts, tc.want)
		}
		if result.Nodes[0].State != NodeFailed {
			t.Fatalf("%s: node = %s, want failed", name, result.Nodes[0].State)
		}
	}
}

// A4: the verdict follows the receipts, not the run. A plan that mutated the
// workspace and verified nothing cannot read as done, and the same plan with a
// passing verification after the change does.
func TestOrchestrationVerdictFollowsTheReceipts(t *testing.T) {
	writer := NodeSpec{ID: "w", Kind: NodeTool, Tool: "orch_write", WritePaths: []string{"w.md"},
		Args: json.RawMessage(`{"n":1}`)}
	check := NodeSpec{ID: "v", Kind: NodeTool, Tool: "bash", ReadOnly: true, Needs: []string{"w"},
		Args: json.RawMessage(`{"command":"go test ./..."}`)}
	run := func(t *testing.T, nodes ...NodeSpec) OrchestrationResult {
		t.Helper()
		calls := &atomic.Int32{}
		registry := tool.NewRegistry()
		registry.Add(orchWriterTool{calls: calls})
		registry.Add(orchBashTool{calls: calls})
		plan, err := Compile(spec(ModeSequence, nodes...), ValidateOptions{AllowAgentNodes: true, Tools: registry})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		ledger := evidence.NewLedger()
		ctx := evidence.WithLedger(orchContext(), ledger)
		result, err := RunOrchestration(ctx, plan, NewRunOptions(nil, registry))
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if !result.Completed() {
			t.Fatalf("the plan did not complete: %+v", result.Nodes)
		}
		return result
	}
	unverified := run(t, writer)
	if unverified.Completion.Mutations == 0 {
		t.Fatal("the writing node left no mutation receipt, so the case would prove nothing")
	}
	if len(unverified.Completion.Gaps) == 0 {
		t.Fatal("a mutation with no verification produced no gap")
	}
	if unverified.Completion.Verdict == completion.VerdictDone {
		t.Fatalf("an unverified mutation read as %s", unverified.Completion.Verdict)
	}
	verified := run(t, writer, check)
	if len(verified.Completion.Gaps) != 0 {
		t.Fatalf("a change followed by a passing check still carries gaps: %+v", verified.Completion.Gaps)
	}
	if verified.Completion.Verdict != completion.VerdictDone {
		t.Fatalf("a verified plan read as %s, want done", verified.Completion.Verdict)
	}
}

// A5: the runner adds no scheduler of its own. Every slot comes from the
// session's scheduler, so a plan wider than the ceiling still cannot exceed it.
func TestOrchestrationNeverExceedsTheSessionSlots(t *testing.T) {
	var concurrent, peak atomic.Int32
	prov := &orchBarrierProvider{onPrompt: func() {
		cur := concurrent.Add(1)
		for {
			old := peak.Load()
			if cur <= old || peak.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		concurrent.Add(-1)
	}}
	scheduler := NewSubagentScheduler(6, 3)
	store := mustSubagentStore(t)
	registry := tool.NewRegistry()
	registry.Add(orchReadOnlyTool{})
	task := NewTaskTool(prov, nil, registry, 20, 0, 0, 0, 0, 0, 0, 0.0, "", "sys", nil, 0, "", "", nil).
		WithTranscripts(store, t.TempDir(), "base", "high").
		WithScheduler(scheduler)
	nodes := make([]NodeSpec, 8)
	for i := range nodes {
		nodes[i] = agentNode("n" + string(rune('a'+i)))
	}
	plan, err := Compile(spec(ModeParallel, nodes...), ValidateOptions{AllowAgentNodes: true})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ctx := withCallContext(WithParentSession(context.Background(), "orch-session"), "orch-call", event.Discard, nil, false)
	result, err := RunOrchestration(ctx, plan, NewRunOptions(task, nil))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	total, writers := scheduler.Limits()
	if total != 6 || writers != 3 {
		t.Fatalf("the run changed the session's ceilings to %d/%d", total, writers)
	}
	if peak.Load() > 6 {
		t.Fatalf("the run reached %d concurrent nodes, over the session's 6", peak.Load())
	}
	if peak.Load() < 2 {
		t.Fatalf("peak concurrency was %d, so the plan never ran in parallel", peak.Load())
	}
	if !result.Completed() {
		t.Fatalf("the plan did not complete: %+v", result.Nodes)
	}
	for _, node := range result.Nodes {
		if node.Ref == "" {
			t.Fatalf("node %s published no transcript reference", node.ID)
		}
		meta, err := store.LoadMeta(node.Ref)
		if err != nil {
			t.Fatalf("node %s: the published reference does not resolve: %v", node.ID, err)
		}
		if meta.Status != SubagentCompleted {
			t.Fatalf("node %s: transcript status %s, want completed", node.ID, meta.Status)
		}
	}
}

// Two runs of the same plan are two different runs: the plan is immutable and
// reusable, and a second run over it re-derives every node rather than
// inheriting the first run's results.
func TestOrchestrationReusesAFrozenPlanAcrossRuns(t *testing.T) {
	plan, err := Compile(spec(ModeSequence, agentNode("a"), agentNode("b", "a")), ValidateOptions{AllowAgentNodes: true})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	digests := []string{}
	for run := range 2 {
		dispatches := &atomic.Int32{}
		options := NewRunOptions(orchTaskTool(t), nil)
		options.RunAgent = orchAgentSeam(dispatches, fmt.Sprintf("sa%d-", run))
		result, err := RunOrchestration(orchContext(), plan, options)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if !result.Completed() || dispatches.Load() != 2 {
			t.Fatalf("run %d: %d dispatches, nodes %+v", run, dispatches.Load(), result.Nodes)
		}
		if result.PlanHash != plan.Hash() {
			t.Fatalf("run %d hashed the plan %q, want %q", run, result.PlanHash, plan.Hash())
		}
		if len(result.Nodes) != 2 || result.Nodes[0].Attempts != 1 || result.Nodes[1].Attempts != 1 {
			t.Fatalf("run %d did not start from a clean node table: %+v", run, result.Nodes)
		}
		digests = append(digests, result.Nodes[0].Ref+"/"+result.Nodes[1].Ref)
	}
	if digests[0] == digests[1] {
		t.Fatalf("the second run inherited the first run's handles: %v", digests)
	}
	if plan.NodeCount() != 2 || strings.Count(plan.Hash(), "") != len(plan.Hash())+1 {
		t.Fatalf("the plan moved between runs: %d nodes, hash %q", plan.NodeCount(), plan.Hash())
	}
}

// orchBarrierProvider reports each prompt and answers immediately, so a test can
// measure how many node calls overlap.
type orchBarrierProvider struct{ onPrompt func() }

func (p *orchBarrierProvider) Name() string { return "orchestrate-barrier" }

func (p *orchBarrierProvider) Stream(_ context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	if p.onPrompt != nil {
		p.onPrompt()
	}
	ch := make(chan provider.Chunk, 2)
	ch <- provider.Chunk{Type: provider.ChunkText, Text: "done"}
	ch <- provider.Chunk{Type: provider.ChunkDone}
	close(ch)
	return ch, nil
}

// The plan is a fixed graph: the runner exposes no repeat, no script and no
// computed successor, so a run cannot extend itself.
func TestOrchestrationExposesNoUnboundedSurface(t *testing.T) {
	dispatches := &atomic.Int32{}
	plan, err := Compile(spec(ModeSequence, agentNode("a"), agentNode("b", "a")), ValidateOptions{AllowAgentNodes: true})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	runOptions := NewRunOptions(orchTaskTool(t), nil)
	runOptions.RunAgent = orchAgentSeam(dispatches, "sa-")
	result, err := RunOrchestration(orchContext(), plan, runOptions)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if dispatches.Load() != 2 {
		t.Fatalf("a two-node plan dispatched %d nodes", dispatches.Load())
	}
	if result.PlanHash != plan.Hash() || plan.NodeCount() != 2 {
		t.Fatalf("the run changed the plan: %q vs %q over %d nodes", result.PlanHash, plan.Hash(), plan.NodeCount())
	}
	for _, node := range result.Nodes {
		if node.Attempts != 1 {
			t.Fatalf("node %s ran %d times without a retryable failure", node.ID, node.Attempts)
		}
	}
}
