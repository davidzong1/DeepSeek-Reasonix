package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"reasonix/internal/tool"
)

// ValidateOptions is assembled by the host at registration and is never read
// from the spec, the tool schema, or the call arguments.
type ValidateOptions struct {
	// AllowAgentNodes is false for a team session and true otherwise.
	AllowAgentNodes bool
	// Budget is a declared ceiling only. Validate reads no spend.
	Budget TaskBudget
	// Tools is the session's tool registry, consulted read-only. A spec that
	// names a tool node requires it; a nil registry refuses those nodes.
	Tools *tool.Registry
}

// PlanItem is one compiled task as the plan publishes it. It is the stable
// view a caller reads, so nothing outside this file depends on the private
// item type the graph is built from.
type PlanItem struct {
	ID         string
	Kind       NodeKind
	Needs      []string
	Profile    string
	Prompt     string
	WritePaths []string
	ReadOnly   bool
}

// Plan is a validated spec compiled onto fleet's graph. It is immutable and
// hashable: every field is unexported and every accessor returns a copy, so a
// caller cannot edit the graph a run was attributed to.
type Plan struct {
	hash   string
	nodes  []NodeSpec
	view   []PlanItem
	graph  fleetPlan
	reduce *ReduceSpec
}

// Hash identifies the exact plan, so a replay is matched to the spec that
// produced it rather than to a spec that merely resembles it.
func (p Plan) Hash() string { return p.hash }

// Nodes returns the compiled nodes in spec order, as deep copies.
func (p Plan) Nodes() []NodeSpec { return copyNodeSpecs(p.nodes) }

// copyNodeSpecs owns every slice and byte buffer of the nodes, so neither the
// plan nor the spec nor a caller can reach another's memory through them. The
// node table is the one accessor that publishes arguments, and a shallow copy
// would let a caller edit the bytes a run is attributed to.
func copyNodeSpecs(nodes []NodeSpec) []NodeSpec {
	out := make([]NodeSpec, len(nodes))
	for i, node := range nodes {
		out[i] = node
		out[i].Needs = append([]string(nil), node.Needs...)
		out[i].Outputs = append([]FieldSpec(nil), node.Outputs...)
		out[i].WritePaths = append([]string(nil), node.WritePaths...)
		out[i].Args = append(json.RawMessage(nil), node.Args...)
	}
	return out
}

// Items returns the compiled tasks in spec order, as copies.
func (p Plan) Items() []PlanItem {
	items := make([]PlanItem, len(p.view))
	for i, item := range p.view {
		items[i] = item
		items[i].Needs = append([]string(nil), item.Needs...)
		items[i].WritePaths = append([]string(nil), item.WritePaths...)
	}
	return items
}

// NodeCount is the compiled node count.
func (p Plan) NodeCount() int { return len(p.nodes) }

// Reduce reports the plan's closed operator. It is false for a plan with no
// reduce node, which is every plan whose mode is not fanout_reduce.
func (p Plan) Reduce() (ReduceSpec, bool) {
	if p.reduce == nil {
		return ReduceSpec{}, false
	}
	return *p.reduce, true
}

// ValidateOrchestration is pure: it reads no disk, executes nothing, and reads
// no spend. It is the only admission step, and every refusal below happens
// before anything is dispatched.
func ValidateOrchestration(spec OrchestrationSpec, opts ValidateOptions) error {
	index, err := validateNodeIDs(spec)
	if err != nil {
		return err
	}
	if err := validateOutputs(spec); err != nil {
		return err
	}
	// Reusing fleet's own constructor is what makes the graph rules shared
	// rather than re-derived: cycles, ids and edges are its checks, not mine.
	if _, err := newFleetPlan(compileItems(spec, opts), false); err != nil {
		return fmt.Errorf("orchestration graph: %w", err)
	}
	if err := validateReferences(spec, index); err != nil {
		return err
	}
	if err := validateModeShape(spec); err != nil {
		return err
	}
	if err := validateWriters(spec, opts); err != nil {
		return err
	}
	if err := validateReduce(spec); err != nil {
		return err
	}
	if err := validateCaps(spec, opts); err != nil {
		return err
	}
	if err := validateDeclaredBudget(spec, opts); err != nil {
		return err
	}
	if !opts.AllowAgentNodes {
		for _, node := range spec.Nodes {
			if node.Kind == NodeAgent {
				return fmt.Errorf("node %q: agent nodes are not available in this session", node.ID)
			}
		}
	}
	return validateToolNodes(spec, opts)
}

// Compile compiles a validated spec onto the existing fleet graph. It is pure
// and calls ValidateOrchestration first, so there is no path to a Plan that
// skipped validation.
func Compile(spec OrchestrationSpec, opts ValidateOptions) (Plan, error) {
	if err := ValidateOrchestration(spec, opts); err != nil {
		return Plan{}, err
	}
	items := compileItems(spec, opts)
	graph, err := newFleetPlan(items, false)
	if err != nil {
		return Plan{}, fmt.Errorf("orchestration graph: %w", err)
	}
	hash, err := planFingerprint(spec, opts)
	if err != nil {
		return Plan{}, err
	}
	var reduce *ReduceSpec
	if spec.Reduce != nil {
		held := *spec.Reduce
		reduce = &held
	}
	return Plan{
		hash:   hash,
		nodes:  copyNodeSpecs(spec.Nodes),
		view:   planView(spec, items),
		graph:  graph,
		reduce: reduce,
	}, nil
}

// planView is the plan's published task list. It is derived from the same
// items the graph was built from, so the view and the graph cannot disagree.
func planView(spec OrchestrationSpec, items []fleetTaskItem) []PlanItem {
	view := make([]PlanItem, len(items))
	for i := range items {
		view[i] = PlanItem{
			ID:         items[i].ID,
			Kind:       spec.Nodes[i].Kind,
			Needs:      append([]string(nil), items[i].DependsOn...),
			Profile:    items[i].Profile,
			Prompt:     items[i].Prompt,
			WritePaths: append([]string(nil), items[i].WritePaths...),
			ReadOnly:   items[i].ReadOnly,
		}
	}
	return view
}

// compileItems shapes each node as a fleet task, so the graph is built from
// items a run would accept rather than from a parallel shape of my own. A
// non-agent node has no prompt of its own — its work is host-side — so it
// carries the placeholder below, which is never dispatched to a model.
func compileItems(spec OrchestrationSpec, opts ValidateOptions) []fleetTaskItem {
	registry := registeredTargets(spec, opts)
	items := make([]fleetTaskItem, len(spec.Nodes))
	for i, node := range spec.Nodes {
		prompt := strings.TrimSpace(node.Prompt)
		if prompt == "" {
			prompt = fmt.Sprintf("orchestrate %s node %q (host-side; no prompt)", node.Kind, node.ID)
		}
		items[i] = fleetTaskItem{
			Prompt:     prompt,
			ID:         node.ID,
			DependsOn:  append([]string(nil), node.Needs...),
			Profile:    node.Profile,
			WritePaths: append([]string(nil), node.WritePaths...),
			ReadOnly:   effectiveReadOnly(node, registry),
		}
	}
	return items
}

func registeredTargets(spec OrchestrationSpec, opts ValidateOptions) map[string]tool.Tool {
	targets := map[string]tool.Tool{}
	if opts.Tools == nil {
		return targets
	}
	for _, node := range spec.Nodes {
		if node.Kind != NodeTool {
			continue
		}
		name := strings.TrimSpace(node.Tool)
		if _, done := targets[name]; done {
			continue
		}
		if target, ok := opts.Tools.Get(name); ok {
			targets[name] = target
		}
	}
	return targets
}

// effectiveReadOnly is the conjunction the graph is built from. A tool node is
// read-only only when both the spec says so and the registered tool reports it:
// the spec is model-authored, so its flag is a claim, and the tool's own
// ReadOnly is the host's answer. An unregistered tool is taken at its word here
// because validateToolNodes refuses every spec that names one.
func effectiveReadOnly(node NodeSpec, registry map[string]tool.Tool) bool {
	if node.Kind != NodeTool {
		return node.ReadOnly
	}
	target, ok := registry[strings.TrimSpace(node.Tool)]
	if !ok {
		return node.ReadOnly
	}
	return node.ReadOnly && target.ReadOnly()
}

// planFingerprint is the plan's identity. It covers the spec as declared and
// the capability the registry resolved, because the spec's own read_only is a
// claim: two plans whose nodes may do opposite things are not the same plan.
func planFingerprint(spec OrchestrationSpec, opts ValidateOptions) (string, error) {
	encoded, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("orchestration spec is not encodable: %w", err)
	}
	sum := sha256.Sum256(capabilityWitness(encoded, spec, opts))
	return hex.EncodeToString(sum[:]), nil
}

// capabilityWitness appends each node's resolved capability to the encoded
// spec, so the digest moves exactly where the effective capability does and
// nowhere else. A node no registry answers for is refused before a plan exists.
func capabilityWitness(encoded []byte, spec OrchestrationSpec, opts ValidateOptions) []byte {
	registry := registeredTargets(spec, opts)
	witness := make([]string, 0, len(spec.Nodes))
	for _, node := range spec.Nodes {
		entry := string(node.Kind) + ":n/a"
		if node.Kind == NodeTool {
			canonical := "unresolved"
			answer := "write"
			if target, ok := registry[strings.TrimSpace(node.Tool)]; ok {
				canonical = target.Name()
			}
			if effectiveReadOnly(node, registry) {
				answer = "read"
			}
			entry = string(node.Kind) + ":" + canonical + ":" + answer
		}
		witness = append(witness, entry)
	}
	return []byte(string(encoded) + "\x00" + strings.Join(witness, ","))
}

func validateNodeIDs(spec OrchestrationSpec) (map[string]int, error) {
	if spec.Version != OrchestrationSpecVersion {
		return nil, fmt.Errorf("orchestration spec version %d is not supported", spec.Version)
	}
	if len(spec.Nodes) == 0 {
		return nil, fmt.Errorf("orchestration spec declares no nodes")
	}
	if len(spec.Nodes) > OrchestrationMaxNodes {
		return nil, fmt.Errorf("orchestration spec declares %d nodes, over the %d limit", len(spec.Nodes), OrchestrationMaxNodes)
	}
	index := make(map[string]int, len(spec.Nodes))
	for i, node := range spec.Nodes {
		if err := checkNodeID(node.ID); err != nil {
			return nil, err
		}
		if prior, dup := index[node.ID]; dup {
			return nil, fmt.Errorf("node %q is already used by node %q", node.ID, spec.Nodes[prior].ID)
		}
		index[node.ID] = i
	}
	for _, node := range spec.Nodes {
		for _, need := range node.Needs {
			if _, ok := index[need]; !ok {
				return nil, fmt.Errorf("node %q: needs %q, which no node declares", node.ID, need)
			}
			if need == node.ID {
				return nil, fmt.Errorf("node %q: needs itself", node.ID)
			}
		}
	}
	return index, nil
}

var nodeIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// checkNodeID enforces the three id rules. A padded id is refused rather than
// trimmed: fleet trims ids when it builds its graph, so accepting " a" here
// would let two distinct spec ids collapse into one compiled id.
func checkNodeID(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("orchestration node id is empty")
	}
	if id != strings.TrimSpace(id) {
		return fmt.Errorf("orchestration node id %q is padded; ids are used verbatim", id)
	}
	if !nodeIDRe.MatchString(id) {
		return fmt.Errorf("orchestration node id %q is not 1-64 characters of [A-Za-z0-9._-]", id)
	}
	return nil
}

func validateModeShape(spec OrchestrationSpec) error {
	switch spec.Mode {
	case ModeSequence, ModeParallel, ModePipeline, ModeFanoutReduce:
	default:
		return fmt.Errorf("orchestration mode %q is not one of sequence, parallel, pipeline, fanout_reduce", spec.Mode)
	}
	ancestors := reachableAncestors(spec)
	for i, node := range spec.Nodes {
		switch spec.Mode {
		case ModeParallel:
			if len(node.Needs) > 0 {
				return fmt.Errorf("mode parallel: node %q declares needs, so the nodes are not independent", node.ID)
			}
		case ModePipeline:
			if len(node.Needs) > 1 {
				return fmt.Errorf("mode pipeline: node %q declares %d needs; a pipeline is a chain", node.ID, len(node.Needs))
			}
		case ModeSequence:
			for j := range spec.Nodes {
				if i == j {
					continue
				}
				if ancestors[i][j] || ancestors[j][i] {
					continue
				}
				return fmt.Errorf("mode sequence: nodes %q and %q are unordered, so the plan is not a total order", node.ID, spec.Nodes[j].ID)
			}
		}
	}
	if spec.Mode == ModePipeline && len(spec.Nodes) > 1 && countEdges(spec) == 0 {
		return fmt.Errorf("mode pipeline: no node declares a need, so there is no chain")
	}
	return nil
}

// validateWriters enforces that a writer only ever appears where the edges
// make it provably serial. Mode conformance has already required a total order
// in the only mode that admits one, so a writer anywhere else is refused here.
// A spec's read_only on a tool node is a claim, not a grant — the test is the
// same conjunction the compiled graph carries.
func validateWriters(spec OrchestrationSpec, opts ValidateOptions) error {
	registry := registeredTargets(spec, opts)
	for _, node := range spec.Nodes {
		if node.Kind == NodeReduce {
			if !node.ReadOnly {
				return fmt.Errorf("node %q: a reduce node runs host-side and must be read-only", node.ID)
			}
			continue
		}
		if effectiveReadOnly(node, registry) {
			continue
		}
		if spec.Mode != ModeSequence {
			return fmt.Errorf("mode %s: node %q writes, and a writer is only serial in a sequence", spec.Mode, node.ID)
		}
	}
	return nil
}

// validateReduce checks the reduce node's own shape before the plan's: a node
// that cannot say what it reduces is a defect whatever the spec declares.
func validateReduce(spec OrchestrationSpec) error {
	var reduce []string
	for _, node := range spec.Nodes {
		if node.Kind == NodeReduce {
			reduce = append(reduce, node.ID)
		}
	}
	if len(reduce) == 0 {
		if spec.Reduce != nil {
			return fmt.Errorf("the spec declares a reduce operator but no reduce node applies it")
		}
		if spec.Mode == ModeFanoutReduce {
			return fmt.Errorf("mode fanout_reduce: the plan declares no reduce node")
		}
		return nil
	}
	if len(reduce) > 1 {
		return fmt.Errorf("the spec declares %d reduce nodes, and a plan reduces once", len(reduce))
	}
	target := nodesByID(spec)[reduce[0]]
	if spec.Reduce == nil {
		return fmt.Errorf("node %q: a reduce node must name its operator", target.ID)
	}
	if !knownReduceOp(spec.Reduce.Operator) {
		return fmt.Errorf("reduce operator %q is not in the closed set", spec.Reduce.Operator)
	}
	if spec.Mode != ModeFanoutReduce {
		return fmt.Errorf("mode %s: node %q reduces, which only a fanout_reduce plan does", spec.Mode, target.ID)
	}
	if len(target.Needs) == 0 {
		return fmt.Errorf("node %q: a reduce node needs its producers", target.ID)
	}
	if len(target.Outputs) == 0 {
		return fmt.Errorf("node %q: a reduce node must declare the fields it publishes", target.ID)
	}
	byID := nodesByID(spec)
	ancestors := reachableAncestors(spec)
	at := nodeIndex(spec, target.ID)
	for _, need := range target.Needs {
		producer := byID[need]
		if producer.Kind == NodeReduce {
			return fmt.Errorf("node %q: needs %q, and a reduce node takes producers, not another reduce", target.ID, need)
		}
		if len(producer.Outputs) == 0 {
			return fmt.Errorf("node %q: its producer %q declares no outputs, so the reduction has no declared input", target.ID, need)
		}
	}
	for i, node := range spec.Nodes {
		if i == at {
			continue
		}
		if !ancestors[at][i] {
			return fmt.Errorf("mode fanout_reduce: node %q does not feed the reduce node %q", node.ID, target.ID)
		}
	}
	return nil
}

// knownFieldKind holds the vocabulary to the set the tool schema already
// advertises. An omitted kind stays legal, because that schema requires only a
// field's name; a declared one outside the set is refused rather than carried
// as a string nothing can interpret.
func knownFieldKind(kind FieldKind) bool {
	switch kind {
	case "", FieldScalar, FieldList, FieldRef:
		return true
	}
	return false
}

func validateOutputs(spec OrchestrationSpec) error {
	for _, node := range spec.Nodes {
		for _, output := range node.Outputs {
			// An omitted name and an explicit "" are one value here, so this is
			// the only place the schema's required name can be enforced. A
			// nameless field also passes validateReduce yet never resolves.
			if strings.TrimSpace(output.Name) == "" {
				return fmt.Errorf("node %q: an output field has no name", node.ID)
			}
			if !knownFieldKind(output.Kind) {
				return fmt.Errorf("node %q: output %q declares kind %q, which is not scalar, list or ref",
					node.ID, output.Name, output.Kind)
			}
		}
	}
	return nil
}

func knownReduceOp(op ReduceOp) bool {
	switch op {
	case ReduceSelect, ReduceMap, ReduceFilter, ReduceCount:
		return true
	}
	return false
}

// validateCaps refuses a spec whose own declared ceilings do not hold. A cap
// is a refusal input: it may lower the session's limit and never raise it, and
// a plan that does not fit inside the cap it declared is refused rather than
// slimmed down. Caps are checked against the effective plan, so a spec cannot
// hide a writer behind a read-only claim to slip under its own writer cap.
func validateCaps(spec OrchestrationSpec, opts ValidateOptions) error {
	caps := spec.Caps
	if caps.MaxNodes < 0 || caps.MaxParallel < 0 || caps.MaxWriters < 0 {
		return fmt.Errorf("orchestration caps must not be negative")
	}
	if caps.MaxNodes > OrchestrationMaxNodes {
		return fmt.Errorf("caps ask for %d nodes, over the %d limit", caps.MaxNodes, OrchestrationMaxNodes)
	}
	if caps.MaxNodes > 0 && len(spec.Nodes) > caps.MaxNodes {
		return fmt.Errorf("the spec declares %d nodes, over its own cap of %d", len(spec.Nodes), caps.MaxNodes)
	}
	if caps.MaxParallel > MaxSubagentConcurrencyLimit {
		return fmt.Errorf("caps ask for %d nodes at once, over the %d limit", caps.MaxParallel, MaxSubagentConcurrencyLimit)
	}
	if caps.MaxParallel > 0 {
		if width := maxLevelWidth(spec); width > caps.MaxParallel {
			return fmt.Errorf("the plan can run %d nodes at once, over its own cap of %d", width, caps.MaxParallel)
		}
	}
	if caps.MaxWriters > DefaultMaxParallelWriters {
		return fmt.Errorf("caps ask for %d writers at once, over the %d limit", caps.MaxWriters, DefaultMaxParallelWriters)
	}
	if caps.MaxWriters > 0 {
		registry := registeredTargets(spec, opts)
		writers := 0
		for _, node := range spec.Nodes {
			if !effectiveReadOnly(node, registry) {
				writers++
			}
		}
		if writers > caps.MaxWriters {
			return fmt.Errorf("the spec declares %d writers, over its own cap of %d", writers, caps.MaxWriters)
		}
	}
	return nil
}

// validateDeclaredBudget sums the nodes' declared ceilings against the
// declared budget. It is declarative and spend-free: a plan whose own numbers
// do not fit is refused here, and the runner re-checks real spend per node.
func validateDeclaredBudget(spec OrchestrationSpec, opts ValidateOptions) error {
	declared := 0
	for _, node := range spec.Nodes {
		if node.MaxTokens < 0 {
			return fmt.Errorf("node %q: a token ceiling must not be negative", node.ID)
		}
		declared += node.MaxTokens
	}
	if opts.Budget.Tokens <= 0 || declared == 0 {
		return nil
	}
	if declared > opts.Budget.Tokens {
		return fmt.Errorf("the plan declares %d tokens of nodes, over the %d budget", declared, opts.Budget.Tokens)
	}
	return nil
}

func validateToolNodes(spec OrchestrationSpec, opts ValidateOptions) error {
	for _, node := range spec.Nodes {
		if node.Kind != NodeTool {
			continue
		}
		if strings.TrimSpace(node.Tool) == "" {
			return fmt.Errorf("node %q: a tool node must name a tool", node.ID)
		}
		if opts.Tools == nil {
			return fmt.Errorf("node %q: no tool registry is bound to this session", node.ID)
		}
		target, ok := opts.Tools.Get(strings.TrimSpace(node.Tool))
		if !ok {
			return fmt.Errorf("node %q: tool %q is not registered in this session", node.ID, node.Tool)
		}
		result := tool.ValidateArguments(target, node.Args)
		if result.Skipped {
			return fmt.Errorf("node %q: the arguments of %q could not be validated", node.ID, node.Tool)
		}
		if mcpDispatchTarget(target, node.Tool) {
			return fmt.Errorf("node %q: tool %q is served over MCP, whose dispatch a pure validator cannot reproduce", node.ID, node.Tool)
		}
		if result.CompileErr != nil {
			return fmt.Errorf("node %q: tool %q has an invalid argument schema", node.ID, node.Tool)
		}
		if len(result.Violations) > 0 {
			return fmt.Errorf("node %q: arguments for %q violate the tool's schema (%s)", node.ID, node.Tool, violationPath(result.Violations))
		}
	}
	return nil
}

// mcpDispatchTarget is the one MCP test this validator uses. It is the
// dispatcher's own classification plus every adapter that claims MCP identity,
// so a plan cannot admit a call whose routing this package would decide against
// a different rule. Names are the dispatcher's test, the metadata claim is the
// fail-closed superset of it.
func mcpDispatchTarget(target tool.Tool, name string) bool {
	if _, claims := target.(tool.MCPMetadata); claims {
		return true
	}
	return isMCPExecutionTarget(target, name) || isMCPLifecycleConnectTarget(target)
}

func violationPath(violations []tool.ArgumentViolation) string {
	parts := make([]string, 0, len(violations))
	for _, violation := range violations {
		parts = append(parts, violation.Path)
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

type nodeRef struct {
	raw   string
	id    string
	field string
}

var nodeRefPrefix = "$nodes."

// validateReferences enforces that a reference names a prior node's declared
// output and never carries content: a value crosses a node boundary as an id,
// so an undeclared field or a forward edge is a refusal.
func validateReferences(spec OrchestrationSpec, index map[string]int) error {
	ancestors := reachableAncestors(spec)
	byID := nodesByID(spec)
	for _, node := range spec.Nodes {
		for _, ref := range parseNodeRefs(node.Args) {
			if ref.id == node.ID {
				return fmt.Errorf("node %q: %q refers to the node itself", node.ID, ref.raw)
			}
			prior, ok := index[ref.id]
			if !ok {
				return fmt.Errorf("node %q: %q names no node", node.ID, ref.raw)
			}
			if !ancestors[index[node.ID]][prior] {
				return fmt.Errorf("node %q: %q names a node that has not run yet", node.ID, ref.raw)
			}
			if !declaresField(byID[ref.id], ref.field) {
				return fmt.Errorf("node %q: %q names a field %q does not declare", node.ID, ref.raw, ref.id)
			}
		}
	}
	return nil
}

// parseNodeRefs reads the id-and-field references out of a node's arguments.
// A malformed "$nodes." prefix is returned as a reference with no id, so the
// caller refuses it instead of silently ignoring it.
func parseNodeRefs(raw json.RawMessage) []nodeRef {
	text := string(raw)
	var refs []nodeRef
	for {
		at := strings.Index(text, nodeRefPrefix)
		if at < 0 {
			return refs
		}
		text = text[at+len(nodeRefPrefix):]
		token := text
		if cut := strings.IndexFunc(token, func(r rune) bool {
			return !strings.ContainsRune(".-_", r) && (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z')
		}); cut >= 0 {
			token = token[:cut]
		}
		ref := nodeRef{raw: nodeRefPrefix + token}
		if dot := strings.LastIndex(token, "."); dot > 0 && dot < len(token)-1 {
			ref.id, ref.field = token[:dot], token[dot+1:]
		}
		refs = append(refs, ref)
	}
}

func declaresField(node NodeSpec, field string) bool {
	if field == "" {
		return false
	}
	for _, output := range node.Outputs {
		if output.Name == field {
			return true
		}
	}
	return false
}

func nodesByID(spec OrchestrationSpec) map[string]NodeSpec {
	byID := make(map[string]NodeSpec, len(spec.Nodes))
	for _, node := range spec.Nodes {
		byID[node.ID] = node
	}
	return byID
}

// reachableAncestors maps each node position to the positions it depends on,
// transitively. Ids are validated before this runs, so every need resolves.
func reachableAncestors(spec OrchestrationSpec) []map[int]bool {
	index := make(map[string]int, len(spec.Nodes))
	for i, node := range spec.Nodes {
		index[node.ID] = i
	}
	out := make([]map[int]bool, len(spec.Nodes))
	for i := range out {
		seen := map[int]bool{}
		var walk func(int)
		walk = func(at int) {
			for _, need := range spec.Nodes[at].Needs {
				prior := index[need]
				if seen[prior] {
					continue
				}
				seen[prior] = true
				walk(prior)
			}
		}
		walk(i)
		out[i] = seen
	}
	return out
}

func nodeIndex(spec OrchestrationSpec, id string) int {
	for i, node := range spec.Nodes {
		if node.ID == id {
			return i
		}
	}
	return -1
}

func countEdges(spec OrchestrationSpec) int {
	count := 0
	for _, node := range spec.Nodes {
		count += len(node.Needs)
	}
	return count
}

// maxLevelWidth is the widest set of nodes that can be dispatched at the same
// time: a node's level is the longest path to it, and nodes sharing a level are
// unordered with each other.
func maxLevelWidth(spec OrchestrationSpec) int {
	levels := make([]int, len(spec.Nodes))
	index := make(map[string]int, len(spec.Nodes))
	for i, node := range spec.Nodes {
		index[node.ID] = i
	}
	var level func(int) int
	level = func(at int) int {
		if levels[at] > 0 {
			return levels[at]
		}
		deepest := 0
		for _, need := range spec.Nodes[at].Needs {
			if candidate := level(index[need]); candidate > deepest {
				deepest = candidate
			}
		}
		levels[at] = deepest + 1
		return levels[at]
	}
	counts := map[int]int{}
	widest := 0
	for i := range spec.Nodes {
		at := level(i)
		counts[at]++
		if counts[at] > widest {
			widest = counts[at]
		}
	}
	return widest
}
