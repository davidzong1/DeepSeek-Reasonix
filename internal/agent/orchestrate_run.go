package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"reasonix/internal/completion"
	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/tool"
)

// OrchestrationNodeState is one node's state in a run. Five of the six are
// fleet's own vocabulary; running is the addition, because the runner publishes
// a node's start where fleet tracks in-flight items positionally.
type OrchestrationNodeState string

const (
	NodePending   OrchestrationNodeState = "pending"
	NodeRunning   OrchestrationNodeState = "running"
	NodeCompleted OrchestrationNodeState = "completed"
	NodeFailed    OrchestrationNodeState = "failed"
	NodeCancelled OrchestrationNodeState = "cancelled"
	NodeSkipped   OrchestrationNodeState = "skipped"
)

// OrchestrationNodeResult is one node's audited outcome. Err carries the reason
// a node did not complete, including the cut that skipped it. StartedAt and
// EndedAt are unix-millisecond bounds set once the node reaches a terminal
// state; a node that never ran carries none. ReceiptCount is how many receipts
// the node's work left in the turn's ledger, so a caller can tell "ran and left
// proof" from "reported success and left none" without reading any body.
type OrchestrationNodeResult struct {
	ID           string
	Kind         NodeKind
	State        OrchestrationNodeState
	Attempts     int
	Output       string
	Ref          string
	Receipts     []string
	ReceiptCount int
	StartedAt    int64
	EndedAt      int64
	Err          string
}

// DurationMs is the node's wall-clock time, or 0 when it never reached a
// terminal state.
func (n OrchestrationNodeResult) DurationMs() int64 {
	if n.StartedAt == 0 || n.EndedAt < n.StartedAt {
		return 0
	}
	return n.EndedAt - n.StartedAt
}

// OrchestrationValue is a reduce node's published result: references, never
// content. Count is how many ids the operator saw, Refs the ids a selecting
// operator kept, and Pairs the producer-to-id pairing map produced.
type OrchestrationValue struct {
	Node  string
	Field string
	Count int
	Refs  []string
	Pairs []string
}

// OrchestrationResult is what one plan run leaves behind. Completion is the
// host's own verdict over the receipts the run produced, so a plan that mutated
// the workspace without verifying it cannot read as done.
type OrchestrationResult struct {
	PlanHash   string
	Nodes      []OrchestrationNodeResult
	Reduced    []OrchestrationValue
	Cancelled  bool
	BudgetAxis string
	Completion completion.Report
}

// Completed reports whether every node completed.
func (r OrchestrationResult) Completed() bool {
	if len(r.Nodes) == 0 {
		return false
	}
	for _, node := range r.Nodes {
		if node.State != NodeCompleted {
			return false
		}
	}
	return true
}

// agentNodeOut is this feature's own projection of one sub-agent call. It is
// deliberately not SubagentOutcome, which is a persisted record rather than a
// call result.
type agentNodeOut struct {
	Result   string
	Ref      string
	Receipts []string
}

// toolNodeOut is this feature's own projection of one tool call. It is
// deliberately not the unexported batch type the top-level call path uses.
type toolNodeOut struct {
	Output   string
	Receipts []string
}

// RunOptions is the seam P2's host tool fills. Every field is optional: a nil
// RunAgent or RunTool refuses the nodes that need it, a nil BudgetCheck reads as
// unbounded, and a nil Checkpoint records nothing. Substituting a field adds
// policy to the session's own path, never a second mechanism.
type RunOptions struct {
	RunAgent func(ctx context.Context, spec ProfileExecSpec) (agentNodeOut, error)
	RunTool  func(ctx context.Context, name string, args json.RawMessage) (toolNodeOut, error)
	// BudgetCheck is this turn's bound plus the spend accumulated against it,
	// resolved by the host through taskBudgetLimit and runBudget.exceeded: the
	// runner reads the predicate, never the arithmetic. P1 leaves it nil.
	BudgetCheck func(ctx context.Context) (axis, detail string)
	Checkpoint  func(report OrchestrationNodeResult)
	// taskTool shapes an agent node's ProfileExecSpec. It rides the options
	// rather than a second parameter, so the spec an agent node runs is built by
	// the same task tool its RunAgent was bound to.
	taskTool *TaskTool
}

// NewRunOptions binds the runner to the session's own dispatch paths: the
// assembled TaskTool supplies agent nodes through RunProfileSpec, and the
// registry supplies tool nodes through the Execute/ValidateArguments pair a
// top-level call takes. P2's host tool is built from these same values and adds
// BudgetCheck at assembly, where the turn's Agent is reachable.
func NewRunOptions(taskTool *TaskTool, tools *tool.Registry) RunOptions {
	options := RunOptions{taskTool: taskTool, RunTool: registryToolSeam(tools)}
	if taskTool != nil {
		options.RunAgent = func(ctx context.Context, spec ProfileExecSpec) (agentNodeOut, error) {
			out, err := taskTool.RunProfileSpec(ctx, spec)
			if err != nil {
				return agentNodeOut{}, err
			}
			answer, ref := splitSubagentRunResult(out)
			return agentNodeOut{Result: answer, Ref: ref}, nil
		}
	}
	return options
}

// registryToolSeam is the ordinary tool dispatch: the same argument validation
// and the same Execute a top-level call makes, with the receipt an audited call
// leaves. The receipt id is the node's published handle, so a value crosses the
// node boundary as an id and the body stops here.
func registryToolSeam(registry *tool.Registry) func(context.Context, string, json.RawMessage) (toolNodeOut, error) {
	return func(ctx context.Context, name string, args json.RawMessage) (toolNodeOut, error) {
		if registry == nil {
			return toolNodeOut{}, fmt.Errorf("tool %q: no tool registry is bound to this session", name)
		}
		target, ok := registry.Get(name)
		if !ok {
			return toolNodeOut{}, fmt.Errorf("tool %q is not registered in this session", name)
		}
		if result := tool.ValidateArguments(target, args); result.Skipped {
			return toolNodeOut{}, fmt.Errorf("tool %q: the arguments could not be validated", name)
		} else if result.CompileErr != nil {
			return toolNodeOut{}, fmt.Errorf("tool %q: invalid argument schema", name)
		} else if len(result.Violations) > 0 {
			return toolNodeOut{}, fmt.Errorf("tool %q: arguments violate the tool's schema (%s)", name, violationPath(result.Violations))
		}
		output, err := target.Execute(ctx, args)
		receipt := evidence.ReceiptFromToolCall(name, args, err == nil, target.ReadOnly())
		if ledger, ok := evidence.FromContext(ctx); ok {
			receipt = ledger.Record(receipt)
		}
		out := toolNodeOut{Output: output}
		if receipt.ID != "" {
			out.Receipts = append(out.Receipts, receipt.ID)
		}
		return out, err
	}
}

// orchestrationRun is one plan's live state. It owns no queue, no clock and no
// counter of its own: what may start is the graph's answer, and what runs at
// once is the session scheduler's.
type orchestrationRun struct {
	ctx      context.Context
	plan     Plan
	items    []PlanItem
	nodes    []NodeSpec
	options  RunOptions
	sink     event.Sink
	parentID string

	results []fleetItemResult
	reports []OrchestrationNodeResult
	refs    [][]string
	handles []map[string]string
	reduced []OrchestrationValue
	doneCh  chan fleetItemResult
	wg      sync.WaitGroup
	mu      sync.Mutex
	axis    string

	// ledger and receiptMark tie a node's receipt count to the turn's own
	// ledger, so a report cannot claim proof the ledger does not hold. The mark
	// is written once per dispatch and read under mu.
	ledger      *evidence.Ledger
	receiptMark []int
}

// RunOrchestration runs one compiled plan. Ordering, dependent skipping and the
// terminal sweep are fleet's own engine; every node this file adds is a call the
// host's seams make.
func RunOrchestration(ctx context.Context, plan Plan, options RunOptions) (OrchestrationResult, error) {
	items := plan.Items()
	nodes := plan.Nodes()
	if len(items) == 0 {
		return OrchestrationResult{}, fmt.Errorf("orchestrate: the plan declares no nodes")
	}
	if len(items) != len(nodes) {
		return OrchestrationResult{}, fmt.Errorf("orchestrate: the plan's task view and node table disagree")
	}
	for i := range items {
		if items[i].ID != nodes[i].ID {
			return OrchestrationResult{}, fmt.Errorf("orchestrate: node %d is %q in the task view and %q in the node table", i+1, items[i].ID, nodes[i].ID)
		}
	}
	sink := event.Sink(event.Discard)
	parentID := "orchestrate"
	if id, callSink, _, ok := CallContext(ctx); ok {
		if strings.TrimSpace(id) != "" {
			parentID = id
		}
		if callSink != nil {
			sink = callSink
		}
	}
	run := &orchestrationRun{
		ctx:         ctx,
		plan:        plan,
		items:       items,
		nodes:       nodes,
		options:     options,
		sink:        sink,
		parentID:    parentID,
		results:     make([]fleetItemResult, len(items)),
		reports:     make([]OrchestrationNodeResult, len(items)),
		refs:        make([][]string, len(items)),
		handles:     make([]map[string]string, len(items)),
		doneCh:      make(chan fleetItemResult, len(items)),
		receiptMark: make([]int, len(items)),
	}
	if ledger, ok := evidence.FromContext(ctx); ok {
		run.ledger = ledger
	}
	for i := range run.results {
		run.results[i] = fleetItemResult{index: i, status: fleetItemPending, profile: items[i].Profile}
		run.reports[i] = OrchestrationNodeResult{ID: items[i].ID, Kind: items[i].Kind, State: NodePending}
		run.sinceNode(i)
	}
	cancelled := driveFleet(ctx, plan.graph, run.results, run.doneCh, run.wg.Wait, run.startOne)
	return run.collect(cancelled)
}

// startOne publishes a node's start and runs it under the session's own
// dispatch path. A node the budget cuts off is never dispatched: it lands in
// the terminal sweep as skipped, carrying the axis that cut it.
func (r *orchestrationRun) startOne(idx int) {
	item, node := r.items[idx], r.nodes[idx]
	if axis, detail := r.budgetCrossed(); axis != "" {
		r.mu.Lock()
		if r.axis == "" {
			r.axis = axis
		}
		r.mu.Unlock()
		r.publish(fleetItemResult{index: idx, profile: item.Profile, status: fleetItemSkipped,
			err: fmt.Errorf("skipped: %s", detail)}, nil)
		return
	}
	r.markRunning(idx)
	r.sinceNode(idx)
	r.emit(idx, event.ToolDispatch, item, "", "")
	r.wg.Go(func() {
		output, ref, receipts, err := r.dispatch(idx, item, node)
		res := fleetItemResult{index: idx, profile: item.Profile, output: output, ref: ref, err: err}
		switch {
		case err == nil:
			res.status = fleetItemCompleted
		case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
			res.status = fleetItemCancelled
		default:
			res.status = fleetItemFailed
		}
		r.publish(res, receipts)
	})
}

func (r *orchestrationRun) dispatch(idx int, item PlanItem, node NodeSpec) (string, string, []string, error) {
	nodeCtx := withCallContext(r.ctx, r.nodeID(idx), subSinkFor(r.nodeID(idx), r.sink), nil, false)
	switch item.Kind {
	case NodeAgent:
		return r.runAgentNode(nodeCtx, idx, item)
	case NodeTool:
		return r.runToolNode(nodeCtx, idx, item, node)
	case NodeReduce:
		output, err := r.runReduceNode(idx, node)
		return output, "", nil, err
	}
	return "", "", nil, fmt.Errorf("node %q: kind %q is not dispatchable", item.ID, item.Kind)
}

func (r *orchestrationRun) runAgentNode(ctx context.Context, idx int, item PlanItem) (string, string, []string, error) {
	if r.options.RunAgent == nil {
		return "", "", nil, fmt.Errorf("node %q: no agent run surface is bound to this session", item.ID)
	}
	if r.options.taskTool == nil {
		return "", "", nil, fmt.Errorf("node %q: no task tool is bound to this session", item.ID)
	}
	// The compiled item carries the effective read-only flag, so the node runs
	// with the permission the registry answered for and never with the claim.
	spec, err := r.options.taskTool.buildTaskSpec(ctx, item.Prompt, item.ID, item.Profile, item.WritePaths, nil, 0, "", "", "", "", false, item.ReadOnly)
	if err != nil {
		return "", "", nil, fmt.Errorf("node %q: %w", item.ID, err)
	}
	out, err := r.options.RunAgent(ctx, spec)
	// A retry is only ever one, and only for a side-effect-free node: replaying
	// a mutation needs the ordinary recovery path, not a loop here.
	if err != nil && item.ReadOnly && ctx.Err() == nil && retryableNodeFailure(err) && r.takeRetry(idx) {
		out, err = r.options.RunAgent(ctx, spec)
	}
	if err != nil {
		return "", "", nil, err
	}
	r.publishHandles(idx, out.Ref)
	return out.Result, out.Ref, out.Receipts, nil
}

func (r *orchestrationRun) runToolNode(ctx context.Context, idx int, item PlanItem, node NodeSpec) (string, string, []string, error) {
	if r.options.RunTool == nil {
		return "", "", nil, fmt.Errorf("node %q: no tool surface is bound to this session", item.ID)
	}
	args, err := r.resolveArgs(idx, node.Args)
	if err != nil {
		return "", "", nil, err
	}
	out, err := r.options.RunTool(ctx, strings.TrimSpace(node.Tool), args)
	if err != nil {
		return "", "", nil, err
	}
	ref := firstNonEmpty(out.Receipts...)
	r.publishHandles(idx, ref)
	return out.Output, ref, out.Receipts, nil
}

// runReduceNode applies the plan's closed operator to the ids its producers
// published. It reads no producer's body, and its result cannot choose a
// successor: the graph was fixed before the run started.
func (r *orchestrationRun) runReduceNode(idx int, node NodeSpec) (string, error) {
	spec, ok := r.plan.Reduce()
	if !ok {
		return "", fmt.Errorf("node %q: the plan declares no reduce operator", r.items[idx].ID)
	}
	values := applyReduce(spec.Operator, r.items[idx].ID, node.Outputs, r.producerInputs(idx))
	r.mu.Lock()
	r.reduced = append(r.reduced, values...)
	r.mu.Unlock()
	for _, value := range values {
		r.publishHandles(idx, "reduce:"+value.Node+"."+value.Field)
	}
	return formatOrchestrationValues(values), nil
}

// producerInputs collects what each producer published, as ids.
func (r *orchestrationRun) producerInputs(idx int) []reduceInput {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]reduceInput, 0, len(r.plan.graph.deps[idx]))
	for _, dep := range r.plan.graph.deps[idx] {
		input := reduceInput{Node: r.items[dep].ID, Completed: r.results[dep].status == fleetItemCompleted}
		input.Refs = append(input.Refs, r.refs[dep]...)
		out = append(out, input)
	}
	return out
}

// reduceInput is one producer's contribution: the ids it published and whether
// it completed at all.
type reduceInput struct {
	Node      string
	Refs      []string
	Completed bool
}

// applyReduce is the closed operator set, stated over ids and nothing else.
// select keeps every published id, filter keeps the ids of producers that
// completed, map pairs each producer with its id, and count reports how many
// there were. No branch can see a body, so no branch can act on one.
func applyReduce(op ReduceOp, nodeID string, outputs []FieldSpec, inputs []reduceInput) []OrchestrationValue {
	fields := make([]string, 0, len(outputs))
	for _, output := range outputs {
		fields = append(fields, output.Name)
	}
	value := OrchestrationValue{Node: nodeID, Field: firstNonEmpty(fields...)}
	for _, input := range inputs {
		switch op {
		case ReduceFilter:
			if !input.Completed {
				continue
			}
			value.Refs = append(value.Refs, input.Refs...)
		case ReduceMap:
			value.Pairs = append(value.Pairs, input.Node+"="+strings.Join(input.Refs, "+"))
		default: // select, count
			value.Refs = append(value.Refs, input.Refs...)
		}
	}
	value.Count = len(value.Refs)
	return []OrchestrationValue{value}
}

func formatOrchestrationValues(values []OrchestrationValue) string {
	var b strings.Builder
	for i, value := range values {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "reduce %s.%s: count=%d", value.Node, value.Field, value.Count)
		if len(value.Refs) > 0 {
			fmt.Fprintf(&b, " refs=%s", strings.Join(value.Refs, ","))
		}
		if len(value.Pairs) > 0 {
			fmt.Fprintf(&b, " map=%s", strings.Join(value.Pairs, ","))
		}
	}
	return b.String()
}

// resolveArgs replaces a declared reference with the id the referenced node
// published. The grammar was checked at compile time, so the runner resolves
// ids and never re-interprets text: a string that is not exactly one reference
// is passed through untouched.
func (r *orchestrationRun) resolveArgs(idx int, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || !bytes.Contains(raw, []byte(nodeRefPrefix)) {
		return raw, nil
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("node %q: arguments are not decodable: %w", r.items[idx].ID, err)
	}
	resolved, err := r.substitute(idx, doc)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(resolved)
	if err != nil {
		return nil, fmt.Errorf("node %q: resolved arguments are not encodable: %w", r.items[idx].ID, err)
	}
	return encoded, nil
}

func (r *orchestrationRun) substitute(idx int, value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			resolved, err := r.substitute(idx, item)
			if err != nil {
				return nil, err
			}
			out[key] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			resolved, err := r.substitute(idx, item)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	case string:
		id, field, ok := parseNodeRefString(typed)
		if !ok {
			return typed, nil
		}
		handle, ok := r.handleFor(id, field)
		if !ok {
			return nil, fmt.Errorf("node %q: %s names a result that was not published", r.items[idx].ID, strings.TrimSpace(typed))
		}
		return handle, nil
	}
	return value, nil
}

// parseNodeRefString reads one exact "$nodes.<id>.<field>" reference, splitting
// on the last dot as the compiler's grammar does.
func parseNodeRefString(value string) (id, field string, ok bool) {
	text := strings.TrimSpace(value)
	if !strings.HasPrefix(text, nodeRefPrefix) {
		return "", "", false
	}
	token := text[len(nodeRefPrefix):]
	dot := strings.LastIndex(token, ".")
	if dot <= 0 || dot == len(token)-1 {
		return "", "", false
	}
	return token[:dot], token[dot+1:], true
}

func (r *orchestrationRun) handleFor(id, field string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.items {
		if r.items[i].ID != id {
			continue
		}
		handle, ok := r.handles[i][field]
		return handle, ok
	}
	return "", false
}

// publishHandles records the id one node published, keyed by the field names it
// declared, so a downstream reference resolves to an id and never to a body.
func (r *orchestrationRun) publishHandles(idx int, ref string) {
	if strings.TrimSpace(ref) == "" {
		return
	}
	handles := map[string]string{}
	for _, output := range r.nodes[idx].Outputs {
		handles[output.Name] = ref
	}
	r.mu.Lock()
	r.refs[idx] = append(r.refs[idx], ref)
	r.handles[idx] = handles
	r.mu.Unlock()
}

// budgetCrossed is the second budget check: the bound this turn carries plus the
// spend the host accumulated against it. No remaining-budget arithmetic is
// added — the runner needs the predicate, not the number.
func (r *orchestrationRun) budgetCrossed() (axis, detail string) {
	if r.options.BudgetCheck == nil {
		return "", ""
	}
	return r.options.BudgetCheck(r.ctx)
}

// retryableNodeFailure reads the classification RunProfileSpec already applied,
// so the runner retries what the sub-agent path calls retryable and nothing it
// does not. A failure with no envelope is never retried.
func retryableNodeFailure(err error) bool {
	var runErr *SubagentRunError
	if !errors.As(err, &runErr) {
		return false
	}
	return runErr.Outcome.Retryable
}

// maxNodeAttempts is the runner's whole retry budget: one dispatch plus at most
// one replay, so the attempt count a report publishes is bounded by this.
const maxNodeAttempts = 2

// markRunning records the dispatch that is about to happen, which is one attempt.
func (r *orchestrationRun) markRunning(idx int) {
	r.mu.Lock()
	r.reports[idx].State = NodeRunning
	r.reports[idx].Attempts++
	r.reports[idx].StartedAt = time.Now().UnixMilli()
	r.mu.Unlock()
}

// takeRetry spends the one replay a side-effect-free failure earns. A node that
// already spent its attempt is refused, so no failure can loop here.
func (r *orchestrationRun) takeRetry(idx int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reports[idx].Attempts >= maxNodeAttempts {
		return false
	}
	r.reports[idx].Attempts++
	return true
}

// publish files a node's terminal report and hands its result to the graph's
// collector. Every dispatched node publishes exactly one, including after
// cancellation, so partial work is never reported as a node that never ran.
func (r *orchestrationRun) publish(res fleetItemResult, receipts []string) {
	r.mu.Lock()
	report := r.reports[res.index]
	report.Output = res.output
	report.Ref = res.ref
	report.Receipts = append([]string(nil), receipts...)
	report.State = stateFor(res.status)
	report.EndedAt = time.Now().UnixMilli()
	report.ReceiptCount = r.receiptCount(res.index)
	if res.err != nil {
		report.Err = res.err.Error()
	}
	r.reports[res.index] = report
	r.mu.Unlock()
	if r.options.Checkpoint != nil {
		r.options.Checkpoint(report)
	}
	r.emit(res.index, event.ToolResult, r.items[res.index], report.Output, report.Err)
	r.doneCh <- res
}

// receiptCount reads the turn's own ledger, so the report's claim about a node
// leaving proof is the ledger's count rather than the runner's bookkeeping.
func (r *orchestrationRun) receiptCount(idx int) int {
	if r.ledger == nil {
		return 0
	}
	return len(r.ledger.Receipts()) - r.receiptMark[idx]
}

// sinceNode advances the node's ledger watermark to everything recorded since
// it was dispatched. Nested sub-agent work records into the same turn ledger,
// so the mark is taken the moment the node starts rather than assumed.
func (r *orchestrationRun) sinceNode(idx int) {
	if r.ledger == nil {
		return
	}
	r.receiptMark[idx] = len(r.ledger.Receipts())
}

func (r *orchestrationRun) nodeID(idx int) string {
	return fmt.Sprintf("%s/orchestrate-%d", r.parentID, idx+1)
}

func (r *orchestrationRun) emit(idx int, kind event.Kind, item PlanItem, output, errText string) {
	if r.sink == nil {
		return
	}
	args, _ := json.Marshal(map[string]string{"id": item.ID, "kind": string(item.Kind), "plan": r.plan.Hash()})
	r.sink.Emit(event.Event{Kind: kind, Tool: event.Tool{
		ID: r.nodeID(idx), ParentID: r.parentID, Name: "orchestrate",
		Args: string(args), Output: output, Err: errText, ReadOnly: item.ReadOnly,
	}})
}

func (r *orchestrationRun) collect(cancelled bool) (OrchestrationResult, error) {
	r.reconcile()
	result := OrchestrationResult{PlanHash: r.plan.Hash(), Cancelled: cancelled}
	r.mu.Lock()
	result.Nodes = append([]OrchestrationNodeResult(nil), r.reports...)
	result.Reduced = append([]OrchestrationValue(nil), r.reduced...)
	result.BudgetAxis = r.axis
	r.mu.Unlock()
	ledger, ok := evidence.FromContext(r.ctx)
	if !ok {
		ledger = evidence.NewLedger()
	}
	// Receipt honesty: the plan's verdict is the host's own completion report
	// over the receipts the run produced, never the runner's bookkeeping.
	result.Completion = completion.Build(nil, ledger)
	for _, node := range result.Nodes {
		if node.State == NodeCancelled {
			result.Cancelled = true
		}
	}
	if result.BudgetAxis != "" {
		return result, fmt.Errorf("orchestrate: the %s budget cut the plan after %d of %d nodes", result.BudgetAxis, countCompleted(result.Nodes), len(result.Nodes))
	}
	if result.Cancelled {
		return result, firstNonNilErr(r.ctx.Err(), context.Canceled)
	}
	return result, nil
}

// reconcile gives every node that never published a report the state the graph
// settled it in. A dependent fleet skips, or a node the terminal sweep cut, is
// skipped here too, so no node is left reading as pending when the run ends.
func (r *orchestrationRun) reconcile() {
	type pending struct {
		idx    int
		report OrchestrationNodeResult
	}
	var emits []pending
	r.mu.Lock()
	for i := range r.results {
		report := r.reports[i]
		if report.State != NodePending && report.State != NodeRunning {
			continue
		}
		if r.results[i].status == fleetItemPending {
			report.State = NodeSkipped
			if report.Err == "" {
				report.Err = "skipped: the plan stopped before this node ran"
			}
		} else {
			report.State = stateFor(r.results[i].status)
			if report.Err == "" && r.results[i].err != nil {
				report.Err = r.results[i].err.Error()
			}
		}
		// A node the graph settled without dispatching still names when it was
		// settled: a report with no bounds reads as a node that never existed.
		report.EndedAt = time.Now().UnixMilli()
		if report.StartedAt == 0 {
			report.StartedAt = report.EndedAt
		}
		report.ReceiptCount = r.receiptCount(i)
		r.reports[i] = report
		emits = append(emits, pending{idx: i, report: report})
	}
	r.mu.Unlock()
	for _, item := range emits {
		if r.options.Checkpoint != nil {
			r.options.Checkpoint(item.report)
		}
		r.emit(item.idx, event.ToolResult, r.items[item.idx], item.report.Output, item.report.Err)
	}
}

// stateFor maps the graph's own status word onto the report's.
func stateFor(status fleetItemStatus) OrchestrationNodeState {
	switch status {
	case fleetItemCompleted:
		return NodeCompleted
	case fleetItemCancelled:
		return NodeCancelled
	case fleetItemSkipped:
		return NodeSkipped
	default:
		return NodeFailed
	}
}

func countCompleted(nodes []OrchestrationNodeResult) int {
	completed := 0
	for _, node := range nodes {
		if node.State == NodeCompleted {
			completed++
		}
	}
	return completed
}
