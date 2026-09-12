package agent

// P0 acceptance tests for the declarative orchestration model: the validator
// refuses bad specs with zero side effects, a valid spec compiles onto fleet's
// own graph, and the team and scheduling invariants hold on the compiled plan.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/tool"
)

// stubTool is the smallest registered target a tool node can name; the counter
// makes "nothing was executed" a measurement rather than a claim.
type stubTool struct {
	name  string
	calls *int
}

func (s stubTool) Name() string        { return s.name }
func (s stubTool) Description() string { return "stub" }
func (s stubTool) ReadOnly() bool      { return true }
func (s stubTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"],"additionalProperties":false}`)
}
func (s stubTool) Execute(context.Context, json.RawMessage) (string, error) {
	*s.calls++
	return "executed", nil
}

// mcpStubTool carries MCP identity, standing in for a tool whose dispatch a
// pure validator cannot reproduce.
type mcpStubTool struct{ stubTool }

func (m mcpStubTool) MCPServerName() string  { return "srv" }
func (m mcpStubTool) MCPRawToolName() string { return "raw" }

// writingStubTool is a registered tool that reports itself as a writer, which
// is what a spec's own read_only cannot override.
type writingStubTool struct{ stubTool }

func (w writingStubTool) ReadOnly() bool { return false }

// unparseableSchema returns a schema the validator cannot compile, which is how
// the Skipped branch is reached: a third-party MCP tool whose schema is not
// compilable is admissible for a live call but not for a plan.
type unparseableSchema struct{ stubTool }

func (u unparseableSchema) Schema() json.RawMessage { return json.RawMessage(`{"type":"object",`) }

type unparseableMCPSchema struct{ unparseableSchema }

func (u unparseableMCPSchema) MCPServerName() string  { return "srv" }
func (u unparseableMCPSchema) MCPRawToolName() string { return "raw" }

type toolSurface struct {
	registry *tool.Registry
	calls    int
}

func newToolSurface() *toolSurface {
	registry := tool.NewRegistry()
	surface := &toolSurface{registry: registry}
	registry.Add(stubTool{name: "stub_ok", calls: &surface.calls})
	registry.Add(mcpStubTool{stubTool{name: "mcp__srv__raw", calls: &surface.calls}})
	registry.Add(writingStubTool{stubTool{name: "stub_rw", calls: &surface.calls}})
	return surface
}

func optionsFor(surface *toolSurface) ValidateOptions {
	return ValidateOptions{AllowAgentNodes: true, Tools: surface.registry}
}

func agentNode(id string, needs ...string) NodeSpec {
	return NodeSpec{ID: id, Kind: NodeAgent, Needs: needs, Prompt: "do " + id, ReadOnly: true}
}

func toolNode(id, toolName string) NodeSpec {
	return NodeSpec{ID: id, Kind: NodeTool, Tool: toolName, ReadOnly: true, Args: json.RawMessage(`{"n":1}`)}
}

func writerNode(id, toolName string, needs ...string) NodeSpec {
	return NodeSpec{ID: id, Kind: NodeTool, Tool: toolName, Needs: needs, WritePaths: []string{"src/"},
		Args: json.RawMessage(`{"n":1}`)}
}

// maskedWriterNode is a spec that claims a registered writing tool is
// read-only. The claim is model-authored, so the conjunction must ignore it.
func maskedWriterNode(id, toolName string, needs ...string) NodeSpec {
	return NodeSpec{ID: id, Kind: NodeTool, Tool: toolName, Needs: needs, ReadOnly: true,
		Args: json.RawMessage(`{"n":1}`)}
}

func (n NodeSpec) withNeeds(needs ...string) NodeSpec {
	n.Needs = needs
	return n
}

func spec(mode OrchestrationMode, nodes ...NodeSpec) OrchestrationSpec {
	return OrchestrationSpec{Version: OrchestrationSpecVersion, Mode: mode, Nodes: nodes}
}

// A3: every malformed spec is refused, and the refusal leaves nothing behind —
// no tool ran, no sub-agent started, no file was written.
func TestValidateOrchestrationRefusesBadSpecsWithoutSideEffects(t *testing.T) {
	surface := newToolSurface()
	opts := optionsFor(surface)
	tokenCeiling := func(limit int) (OrchestrationSpec, ValidateOptions) {
		s := spec(ModeParallel, agentNode("a"))
		s.Nodes[0].MaxTokens = limit
		return s, opts
	}
	overBudget, overOpts := tokenCeiling(500)
	overOpts.Budget = TaskBudget{Tokens: 100}
	cases := map[string]struct {
		spec OrchestrationSpec
		opts ValidateOptions
		want string
	}{
		"empty id":                {spec(ModeParallel, agentNode("")), opts, "is empty"},
		"padded id":               {spec(ModeParallel, agentNode(" a")), opts, "padded"},
		"padded id beside":        {spec(ModeParallel, agentNode("a"), agentNode(" a")), opts, "padded"},
		"malformed id":            {spec(ModeParallel, agentNode("a/b")), opts, "1-64 characters"},
		"duplicate id":            {spec(ModeSequence, agentNode("a"), agentNode("a"), agentNode("b", "a")), opts, "already used"},
		"unknown need":            {spec(ModeParallel, agentNode("a", "ghost")), opts, "no node declares"},
		"self edge":               {spec(ModeParallel, agentNode("a", "a")), opts, "needs itself"},
		"cycle":                   {spec(ModePipeline, agentNode("a", "b"), agentNode("b", "a")), opts, "cycle"},
		"wrong version":           {OrchestrationSpec{Version: 99, Mode: ModeParallel, Nodes: []NodeSpec{agentNode("a")}}, opts, "version 99"},
		"no nodes":                {spec(ModeParallel), opts, "no nodes"},
		"node count over cap":     {manyNodes(OrchestrationMaxNodes + 1), opts, "over the 64 limit"},
		"unknown mode":            {spec("shuffle", agentNode("a")), opts, "not one of"},
		"parallel with edges":     {spec(ModeParallel, agentNode("a"), agentNode("b", "a")), opts, "not independent"},
		"pipeline with two needs": {spec(ModePipeline, agentNode("a"), agentNode("b"), agentNode("c", "a", "b")), opts, "chain"},
		"sequence not total":      {spec(ModeSequence, agentNode("a"), agentNode("b")), opts, "total order"},
		"writer in parallel":      {spec(ModeParallel, writerNode("w", "stub_ok")), opts, "only serial in a sequence"},
		"writer in pipeline":      {spec(ModePipeline, writerNode("w", "stub_ok")), opts, "only serial in a sequence"},
		"unordered writers":       {spec(ModeSequence, writerNode("w1", "stub_ok"), writerNode("w2", "stub_ok")), opts, "total order"},
		"writer that reduces": {
			spec(ModeSequence, NodeSpec{ID: "r", Kind: NodeReduce, Outputs: []FieldSpec{{Name: "n", Kind: FieldScalar}}}),
			opts, "must be read-only"},
		"reduce without producers": {
			reduceWith(nil, []string{"n"}), opts, "needs its producers"},
		"reduce input without outputs": {
			func() OrchestrationSpec {
				s := reduceWith([]string{"a"}, []string{"n"})
				s.Nodes[0].Outputs = nil
				return s
			}(), opts, "declares no outputs"},
		"reduce outside fanout": {
			func() OrchestrationSpec {
				s := reduceWith([]string{"a"}, []string{"n"})
				s.Mode = ModePipeline
				return s
			}(), opts, "only a fanout_reduce"},
		"fanout without reduce": {
			spec(ModeFanoutReduce, agentNode("a")), opts, "no reduce node"},
		"fanout with two reduces": {
			reducePlan(ReduceSelect, agentNode("a"), agentNode("b")), opts, "reduces once"},
		"unknown reduce operator": {
			reducePlan("sum", agentNode("a")), opts, "closed set"},
		"output with no name": {
			func() OrchestrationSpec {
				s := spec(ModeParallel, agentNode("a"))
				s.Nodes[0].Outputs = []FieldSpec{{Kind: FieldList}}
				return s
			}(), opts, "has no name"},
		"unknown output kind": {
			func() OrchestrationSpec {
				s := spec(ModeParallel, agentNode("a"))
				s.Nodes[0].Outputs = []FieldSpec{{Name: "n", Kind: "banana"}}
				return s
			}(), opts, "not scalar, list or ref"},
		"reduce node without output": {
			reduceWith([]string{"a"}, nil), opts, "declare the fields it publishes"},
		"operator without node": {func() OrchestrationSpec {
			s := spec(ModeParallel, agentNode("a"))
			s.Reduce = &ReduceSpec{Operator: ReduceCount}
			return s
		}(), opts, "no reduce node"},
		"cap above session ceiling": {
			withCaps(spec(ModeParallel, agentNode("a")), CapsRequest{MaxNodes: OrchestrationMaxNodes + 1}), opts, "over the 64 limit"},
		"cap below node count": {
			withCaps(spec(ModeParallel, agentNode("a"), agentNode("b")), CapsRequest{MaxNodes: 1}), opts, "its own cap of 1"},
		"parallel over its own cap": {
			withCaps(spec(ModeParallel, agentNode("a"), agentNode("b"), agentNode("c")), CapsRequest{MaxParallel: 2}), opts, "its own cap of 2"},
		"declared budget over": {overBudget, overOpts, "over the 100 budget"},
		"negative token ceiling": {func() OrchestrationSpec {
			s, _ := tokenCeiling(-1)
			return s
		}(), opts, "must not be negative"},
		"tool not registered":  {spec(ModeParallel, toolNode("t", "nope")), opts, "not registered"},
		"no registry bound":    {spec(ModeParallel, toolNode("t", "stub_ok")), ValidateOptions{AllowAgentNodes: true}, "no tool registry"},
		"mcp tool":             {spec(ModeParallel, toolNode("t", "mcp__srv__raw")), opts, "MCP"},
		"tool args wrong type": {spec(ModeParallel, NodeSpec{ID: "t", Kind: NodeTool, Tool: "stub_ok", ReadOnly: true, Args: json.RawMessage(`{"n":"text"}`)}), opts, "schema"},
		"forward reference": {
			spec(ModeSequence, NodeSpec{ID: "t1", Kind: NodeTool, Tool: "stub_ok", ReadOnly: true,
				Args: json.RawMessage(`{"n":1,"v":"$nodes.a.f"}`)}, agentNode("a")), opts, "has not run yet"},
		"undeclared field reference": {
			spec(ModeSequence, NodeSpec{ID: "t1", Kind: NodeTool, Tool: "stub_ok", ReadOnly: true, Needs: []string{"a"},
				Args: json.RawMessage(`{"n":1,"v":"$nodes.a.missing"}`)}, agentNode("a")), opts, "does not declare"},
		"reference to no node": {
			spec(ModeSequence, NodeSpec{ID: "t1", Kind: NodeTool, Tool: "stub_ok", ReadOnly: true,
				Args: json.RawMessage(`{"n":1,"v":"$nodes.ghost.f"}`)}), opts, "names no node"},
		"self reference": {
			spec(ModeSequence, NodeSpec{ID: "t1", Kind: NodeTool, Tool: "stub_ok", ReadOnly: true,
				Args: json.RawMessage(`{"n":1,"v":"$nodes.t1.f"}`)}), opts, "refers to the node itself"},
		"team session with agent node": {spec(ModeParallel, agentNode("a")), ValidateOptions{Tools: surface.registry}, "not available in this session"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Compile(tc.spec, tc.opts); err == nil {
				t.Fatalf("the spec was accepted; expected a refusal mentioning %q", tc.want)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal %q does not mention %q", err, tc.want)
			}
			if surface.calls != 0 {
				t.Fatalf("a refused spec executed %d tool calls", surface.calls)
			}
		})
	}
}

func withCaps(s OrchestrationSpec, caps CapsRequest) OrchestrationSpec {
	s.Caps = caps
	return s
}

// reducePlan builds a fanout_reduce spec with one reduce node per producer id,
// so a test can give the plan more than one reducer.
func reducePlan(operator ReduceOp, producers ...NodeSpec) OrchestrationSpec {
	nodes := append([]NodeSpec(nil), producers...)
	for _, producer := range producers {
		nodes = append(nodes, NodeSpec{ID: "r-" + producer.ID, Kind: NodeReduce, Needs: []string{producer.ID},
			ReadOnly: true, Outputs: []FieldSpec{{Name: "n", Kind: FieldScalar}}})
	}
	s := spec(ModeFanoutReduce, nodes...)
	s.Reduce = &ReduceSpec{Operator: operator}
	return s
}

// reduceWith builds a fanout_reduce spec whose reduce node has the given needs
// and declared outputs, so a test can drop exactly one of them.
func reduceWith(needs, outputs []string) OrchestrationSpec {
	fields := make([]FieldSpec, len(outputs))
	for i, name := range outputs {
		fields[i] = FieldSpec{Name: name, Kind: FieldScalar}
	}
	nodes := []NodeSpec{
		{ID: "a", Kind: NodeAgent, ReadOnly: true, Prompt: "do a", Outputs: []FieldSpec{{Name: "n", Kind: FieldScalar}}},
		{ID: "r", Kind: NodeReduce, Needs: needs, ReadOnly: true, Outputs: fields},
	}
	s := spec(ModeFanoutReduce, nodes...)
	s.Reduce = &ReduceSpec{Operator: ReduceSelect}
	return s
}

func manyNodes(n int) OrchestrationSpec {
	nodes := make([]NodeSpec, n)
	for i := range nodes {
		nodes[i] = agentNode("n" + string(rune('A'+i/26)) + string(rune('a'+i%26)))
	}
	return spec(ModeParallel, nodes...)
}

// A7, as the mechanism rather than a shipped restriction: when a host withholds
// agent nodes the refusal is the validator's and not the dispatcher's, so no
// node is reached. No role binds the field false today (§9.5) — this pins that
// the carrier still works for a host that wants it.
func TestAgentNodesAreRefusedWhenTheHostWithholdsThem(t *testing.T) {
	surface := newToolSurface()
	opts := ValidateOptions{AllowAgentNodes: false, Tools: surface.registry}
	withheld := spec(ModeParallel, agentNode("a"))
	if err := ValidateOrchestration(withheld, opts); err == nil {
		t.Fatal("a session that withholds agent nodes accepted one")
	}
	if _, err := Compile(withheld, opts); err == nil {
		t.Fatal("Compile accepted an agent node the host withheld")
	}
	if err := ValidateOrchestration(spec(ModeParallel, toolNode("t", "stub_ok")), opts); err != nil {
		t.Fatalf("withholding agent nodes also refused a read-only tool plan: %v", err)
	}
}

// A valid spec compiles onto fleet's own graph, and the plan is immutable: the
// accessors hand back copies, so a caller cannot edit the graph after the fact.
func TestCompileProducesAnImmutableHashedPlan(t *testing.T) {
	surface := newToolSurface()
	plan, err := Compile(spec(ModeSequence, agentNode("a"), agentNode("b", "a")), optionsFor(surface))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if plan.NodeCount() != 2 || plan.Hash() == "" {
		t.Fatalf("plan = %d nodes, hash %q", plan.NodeCount(), plan.Hash())
	}
	nodes := plan.Nodes()
	nodes[0].ID = "mutated"
	if again := plan.Nodes(); again[0].ID != "a" {
		t.Fatal("the plan's node table is reachable for writing")
	}
	items := plan.Items()
	items[0].ID = "mutated"
	if again := plan.Items(); again[0].ID != "a" {
		t.Fatal("the plan's item table is reachable for writing")
	}
	same, err := Compile(spec(ModeSequence, agentNode("a"), agentNode("b", "a")), optionsFor(surface))
	if err != nil {
		t.Fatalf("recompile: %v", err)
	}
	if same.Hash() != plan.Hash() {
		t.Fatalf("hash is not stable: %q then %q", plan.Hash(), same.Hash())
	}
	other, err := Compile(spec(ModeSequence, agentNode("a"), agentNode("c", "a")), optionsFor(surface))
	if err != nil {
		t.Fatalf("compile sibling: %v", err)
	}
	if other.Hash() == plan.Hash() {
		t.Fatal("two different specs share a hash")
	}
	if len(plan.graph.ids) != 2 || plan.graph.deps[1][0] != 0 {
		t.Fatalf("compiled graph = %v / %v", plan.graph.ids, plan.graph.deps)
	}
}

// A non-agent node has no prompt of its own, so the compiler supplies the
// placeholder the shared constructor requires, and never rewrites a real one.
func TestCompilePlacesAWholePromptOnlyWhereOneIsMissing(t *testing.T) {
	surface := newToolSurface()
	target := spec(ModeSequence, agentNode("a"), toolNode("t", "stub_ok").withNeeds("a"))
	plan, err := Compile(target, optionsFor(surface))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	items := plan.Items()
	if items[0].Prompt != "do a" {
		t.Fatalf("the model-authored prompt was rewritten: %q", items[0].Prompt)
	}
	if strings.TrimSpace(items[1].Prompt) == "" {
		t.Fatal("a tool node compiled with an empty prompt")
	}
	rebuilt, err := newFleetPlan(compileItems(target, optionsFor(surface)), false)
	if err != nil {
		t.Fatalf("the compiled items do not rebuild into a fleet plan: %v", err)
	}
	for i := range rebuilt.ids {
		if rebuilt.ids[i] != plan.graph.ids[i] {
			t.Fatalf("rebuilt id %d = %q, want %q", i, rebuilt.ids[i], plan.graph.ids[i])
		}
	}
}

// The plan's published task list is a stable view: it carries the caller-facing
// shape and never the private type the graph is built from.
func TestPlanItemsAreAStableView(t *testing.T) {
	surface := newToolSurface()
	plan, err := Compile(spec(ModeSequence, agentNode("a"), writerNode("w", "stub_ok", "a")), optionsFor(surface))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	items := plan.Items()
	if len(items) != 2 {
		t.Fatalf("plan published %d items, want 2", len(items))
	}
	if items[1].Kind != NodeTool || items[1].ID != "w" || len(items[1].Needs) != 1 || items[1].Needs[0] != "a" {
		t.Fatalf("item = %+v, want the tool node with its need", items[1])
	}
	if items[0].Kind != NodeAgent || items[0].Profile != "" {
		t.Fatalf("item = %+v, want the agent node", items[0])
	}
	items[1].Needs[0] = "mutated"
	items[1].WritePaths = append(items[1].WritePaths, "/etc")
	if again := plan.Items(); again[1].Needs[0] != "a" || len(again[1].WritePaths) != 1 {
		t.Fatalf("the published item shares its slices with the plan: %+v", again[1])
	}
	if len(plan.Items()) != plan.NodeCount() {
		t.Fatal("the published view and the node table disagree")
	}
}

// A5: the compiled plan carries fleet's own ordering rule, so writers the plan
// serialises may share a path while the scheduling ceiling stays the session's.
func TestCompiledPlanSerialisesWritersThroughTheGraph(t *testing.T) {
	surface := newToolSurface()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	writer := func(id, need string) NodeSpec {
		node := NodeSpec{ID: id, Kind: NodeTool, Tool: "stub_ok", WritePaths: []string{"src/"},
			Args: json.RawMessage(`{"n":1}`)}
		if need != "" {
			node.Needs = []string{need}
		}
		return node
	}
	plan, err := Compile(spec(ModeSequence, writer("w1", ""), writer("w2", "w1")), optionsFor(surface))
	if err != nil {
		t.Fatalf("a totally ordered writer chain must validate: %v", err)
	}
	if !plan.graph.ordered(0, 1) {
		t.Fatal("the compiled graph does not serialise the two writers")
	}
	claim, err := NormalizeWritePaths(root, []string{"src/"})
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.graph.validateConcurrentWriteClaims([]WritePathSet{claim, claim}); err != nil {
		t.Fatalf("ordered writers sharing a path must pass the concurrent-claim check: %v", err)
	}
	concurrent, err := Compile(spec(ModeParallel, toolNode("t1", "stub_ok"), toolNode("t2", "stub_ok")), optionsFor(surface))
	if err != nil {
		t.Fatalf("two read-only nodes in parallel must validate: %v", err)
	}
	if concurrent.graph.ordered(0, 1) {
		t.Fatal("independent nodes were reported as ordered")
	}
	if err := concurrent.graph.validateConcurrentWriteClaims([]WritePathSet{claim, claim}); err != nil {
		t.Fatalf("read-only nodes hold no claim to conflict over: %v", err)
	}
	if DefaultMaxParallelWriters >= DefaultMaxSubagentConcurrency {
		t.Fatal("the session's writer ceiling must stay below its concurrency ceiling")
	}
}

// The ceilings a spec cannot raise: a cap above the session's own is refused
// rather than clamped, and a cap that fits narrows the accepted plan.
func TestCapsCanOnlyNarrow(t *testing.T) {
	surface := newToolSurface()
	opts := optionsFor(surface)
	chain := func() OrchestrationSpec {
		s := spec(ModeSequence, writerChain("a", "b", "c")...)
		return s
	}
	if err := ValidateOrchestration(chain(), opts); err != nil {
		t.Fatalf("the baseline chain must validate: %v", err)
	}
	narrow := chain()
	narrow.Caps = CapsRequest{MaxNodes: 3, MaxParallel: 1, MaxWriters: 3}
	if err := ValidateOrchestration(narrow, opts); err != nil {
		t.Fatalf("a narrowing cap was refused: %v", err)
	}
	narrow.Caps = CapsRequest{MaxNodes: 2}
	if err := ValidateOrchestration(narrow, opts); err == nil {
		t.Fatal("a cap below the node count was accepted")
	}
	narrow.Caps = CapsRequest{MaxWriters: 2}
	if err := ValidateOrchestration(narrow, opts); err == nil {
		t.Fatal("a writer cap below the writer count was accepted")
	}
	narrow.Caps = CapsRequest{MaxNodes: -1}
	if err := ValidateOrchestration(narrow, opts); err == nil {
		t.Fatal("a negative cap was accepted")
	}
}

// An unvalidatable schema is a refusal, not a downgrade: Skipped is the one
// case where the host cannot say what the arguments mean, so a plan that
// contains it must not be admitted on the strength of not knowing.
func TestUnvalidatableToolSchemasAreRefused(t *testing.T) {
	surface := newToolSurface()
	surface.registry.Add(unparseableSchema{stubTool{name: "stub_broken", calls: &surface.calls}})
	surface.registry.Add(unparseableMCPSchema{unparseableSchema{stubTool{name: "mcp__srv__broken", calls: &surface.calls}}})
	opts := optionsFor(surface)
	plain := toolNode("t", "stub_broken")
	if err := ValidateOrchestration(spec(ModeParallel, plain), opts); err == nil {
		t.Fatal("a tool whose schema cannot be compiled was admitted")
	} else if !strings.Contains(err.Error(), "invalid argument schema") {
		t.Fatalf("refusal %q does not name the schema", err)
	}
	mcp := toolNode("t", "mcp__srv__broken")
	if err := ValidateOrchestration(spec(ModeParallel, mcp), opts); err == nil {
		t.Fatal("an MCP tool with an uncompilable schema was admitted")
	} else if !strings.Contains(err.Error(), "could not be validated") {
		t.Fatalf("refusal %q does not name the skipped validation", err)
	}
	if surface.calls != 0 {
		t.Fatalf("the refused nodes executed %d calls", surface.calls)
	}
}

// The pin: a writer is admitted only where the whole order is a total one, so
// parallel and pipeline refuse a writer that their edges would otherwise admit.
func TestWritersRunOnlyInATotalOrder(t *testing.T) {
	surface := newToolSurface()
	opts := optionsFor(surface)
	admitted := spec(ModeSequence, writerChain("a", "b", "c")...)
	if err := ValidateOrchestration(admitted, opts); err != nil {
		t.Fatalf("an ordered writer chain must be admitted: %v", err)
	}
	refused := map[OrchestrationMode]OrchestrationSpec{
		ModeSequence: spec(ModeSequence, toolNode("r1", "stub_ok"), toolNode("r2", "stub_ok"), writerNode("w", "stub_ok", "r1")),
		ModeParallel: spec(ModeParallel, toolNode("r1", "stub_ok"), writerNode("w", "stub_ok")),
		ModePipeline: spec(ModePipeline, toolNode("r1", "stub_ok"), writerNode("w", "stub_ok", "r1")),
	}
	for mode, target := range refused {
		if err := ValidateOrchestration(target, opts); err == nil {
			t.Fatalf("mode %s admitted a writer: its own shape is not a total order", mode)
		} else if !strings.Contains(err.Error(), "total order") && !strings.Contains(err.Error(), "only serial in a sequence") {
			t.Fatalf("mode %s refusal %q names neither the total order nor the writer rule", mode, err)
		}
		if surface.calls != 0 {
			t.Fatalf("mode %s executed %d tool calls while being refused", mode, surface.calls)
		}
	}
}

// writerChain builds a totally ordered chain of writers, which is the only
// shape the writer rule admits.
func writerChain(ids ...string) []NodeSpec {
	nodes := make([]NodeSpec, len(ids))
	for i, id := range ids {
		node := NodeSpec{ID: id, Kind: NodeTool, Tool: "stub_ok", WritePaths: []string{"src/"},
			Args: json.RawMessage(`{"n":1}`)}
		if i > 0 {
			node.Needs = []string{ids[i-1]}
		}
		nodes[i] = node
	}
	return nodes
}

// C6: a tool node is read-only only where the spec and the registered tool
// agree. A writing tool cannot be talked out of its own ReadOnly report, so it
// stays subject to the writer rule and is refused wherever the order is not
// total, while an ordered chain of it still compiles.
func TestWriterToolNodesNeedBothReadOnlyFlags(t *testing.T) {
	surface := newToolSurface()
	opts := optionsFor(surface)
	target, ok := surface.registry.Get("stub_rw")
	if !ok || target.ReadOnly() {
		t.Fatal("the writing stub is not a registered writer, so the case would pass for the wrong reason")
	}
	masquerade := map[string]OrchestrationSpec{
		"parallel": spec(ModeParallel, maskedWriterNode("w", "stub_rw")),
		"pipeline": spec(ModePipeline, toolNode("r", "stub_ok"), maskedWriterNode("w", "stub_rw", "r")),
	}
	for mode, target := range masquerade {
		if err := ValidateOrchestration(target, opts); err == nil {
			t.Fatalf("mode %s admitted a writing tool declared read-only", mode)
		} else if !strings.Contains(err.Error(), "only serial in a sequence") {
			t.Fatalf("mode %s refusal %q does not name the writer rule", mode, err)
		}
		if surface.calls != 0 {
			t.Fatalf("the refused %s spec executed %d calls", mode, surface.calls)
		}
	}
	fanIn := spec(ModeSequence, toolNode("r1", "stub_ok"), toolNode("r2", "stub_ok"), maskedWriterNode("w", "stub_rw", "r1"))
	if err := ValidateOrchestration(fanIn, opts); err == nil {
		t.Fatal("a sequence that is not a total order admitted a writing tool declared read-only")
	} else if !strings.Contains(err.Error(), "total order") {
		t.Fatalf("refusal %q does not name the total order", err)
	}
	if surface.calls != 0 {
		t.Fatalf("the refused sequence executed %d calls", surface.calls)
	}
	ordered := spec(ModeSequence, maskedWriterNode("w1", "stub_rw"), maskedWriterNode("w2", "stub_rw", "w1"))
	plan, err := Compile(ordered, opts)
	if err != nil {
		t.Fatalf("a totally ordered chain of writers must compile: %v", err)
	}
	items := plan.Items()
	if items[0].ReadOnly || items[1].ReadOnly {
		t.Fatalf("the plan published the spec's read-only claim: %+v", items)
	}
	if !plan.graph.ordered(0, 1) {
		t.Fatal("the compiled chain is not ordered, so the writers are not serial")
	}
	honest := spec(ModeSequence, writerNode("w1", "stub_rw"), writerNode("w2", "stub_rw", "w1"))
	if _, err := Compile(honest, opts); err != nil {
		t.Fatalf("the same chain declared honestly must compile too: %v", err)
	}
	if err := ValidateOrchestration(withCaps(honest, CapsRequest{MaxWriters: 1}), opts); err == nil {
		t.Fatal("a writer cap of 1 admitted two effective writers behind read-only claims")
	}
	readOnly := spec(ModeParallel, toolNode("t1", "stub_ok"), toolNode("t2", "stub_ok"))
	if _, err := Compile(readOnly, opts); err != nil {
		t.Fatalf("two genuinely read-only nodes in parallel must compile: %v", err)
	}
	if _, err := Compile(spec(ModeSequence, toolNode("t1", "stub_ok"), maskedWriterNode("t2", "stub_ok", "t1")), opts); err != nil {
		t.Fatalf("a read-only tool declared as such must not become a writer: %v", err)
	}
}

// The MCP branch is reached through the same stub surface: the tool is refused
// after registration and before dispatch, so the refusal is a refusal and not a
// narrowing the plan could route around.
func TestMCPToolNodesAreRefusedBeforeDispatch(t *testing.T) {
	surface := newToolSurface()
	opts := optionsFor(surface)
	if _, ok := surface.registry.Get("mcp__srv__raw"); !ok {
		t.Fatal("the MCP stub is not registered, so the case would pass for the wrong reason")
	}
	node := NodeSpec{ID: "t", Kind: NodeTool, Tool: "mcp__srv__raw", ReadOnly: true, Args: json.RawMessage(`{"n":1}`)}
	err := ValidateOrchestration(spec(ModeParallel, node), opts)
	if err == nil {
		t.Fatal("an MCP-served tool node was admitted")
	}
	if !strings.Contains(err.Error(), "MCP") {
		t.Fatalf("refusal %q does not name MCP", err)
	}
	if surface.calls != 0 {
		t.Fatalf("the refused MCP node executed %d calls", surface.calls)
	}
	if _, err := Compile(spec(ModeParallel, node), opts); err == nil {
		t.Fatal("Compile admitted an MCP-served tool node")
	}
	mcp := mcpStubTool{stubTool{name: "mcp__other__raw", calls: &surface.calls}}
	mcpOnly := tool.NewRegistry()
	mcpOnly.Add(mcp)
	if err := ValidateOrchestration(spec(ModeParallel, toolNode("t", "mcp__other__raw")),
		ValidateOptions{AllowAgentNodes: true, Tools: mcpOnly}); err == nil {
		t.Fatal("an unregistered MCP stub was admitted")
	}
}

// blankMCPTool claims MCP identity with blank names: the tool type says MCP
// while the narrow metadata test refuses to call it one.
type blankMCPTool struct{ stubTool }

func (b blankMCPTool) MCPServerName() string  { return "" }
func (b blankMCPTool) MCPRawToolName() string { return "" }

// lifecycleMCPTool is a connect-and-list target: neither metadata-identified nor
// mcp__-prefixed, so only the dispatcher's connect arm names it MCP.
type lifecycleMCPTool struct{ stubTool }

func (l lifecycleMCPTool) MCPLifecycleConnect() bool { return true }
func (l lifecycleMCPTool) MCPServerAuthorized() bool { return true }

// The declaration view is a deep copy: a caller mutating what Nodes() returns
// reaches neither the plan nor the spec it was compiled from. Args is a byte
// buffer and Needs/Outputs/WritePaths are slices, so a shallow struct copy
// would alias all four.
func TestPlanNodesAreADeepCopy(t *testing.T) {
	surface := newToolSurface()
	source := spec(ModeSequence, agentNode("a"), NodeSpec{
		ID: "t", Kind: NodeTool, Tool: "stub_ok", Needs: []string{"a"}, ReadOnly: true,
		Args:       json.RawMessage(`{"n":1}`),
		Outputs:    []FieldSpec{{Name: "n", Kind: FieldScalar}},
		WritePaths: []string{"src/"},
	})
	plan, err := Compile(source, optionsFor(surface))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	nodes := plan.Nodes()
	nodes[1].Needs[0] = "clobbered"
	nodes[1].WritePaths[0] = "clobbered"
	nodes[1].Outputs[0].Name = "clobbered"
	nodes[1].Args[0] = 'X'
	nodes[1].ReadOnly = false
	after := plan.Nodes()[1]
	if after.Needs[0] != "a" || after.WritePaths[0] != "src/" || after.Outputs[0].Name != "n" ||
		string(after.Args) != `{"n":1}` || !after.ReadOnly {
		t.Fatalf("the plan's node table is reachable for writing: %+v", after)
	}
	if source.Nodes[1].Needs[0] != "a" || source.Nodes[1].WritePaths[0] != "src/" ||
		source.Nodes[1].Outputs[0].Name != "n" || string(source.Nodes[1].Args) != `{"n":1}` {
		t.Fatalf("the compiled plan aliases the spec it was compiled from: %+v", source.Nodes[1])
	}
	source.Nodes[1].Needs[0] = "clobbered"
	source.Nodes[1].WritePaths[0] = "clobbered"
	source.Nodes[1].Outputs[0].Name = "clobbered"
	source.Nodes[1].Args[0] = 'X'
	if held := plan.Nodes()[1]; held.Needs[0] != "a" || held.WritePaths[0] != "src/" ||
		held.Outputs[0].Name != "n" || string(held.Args) != `{"n":1}` {
		t.Fatalf("the plan is reachable through the spec it was compiled from: %+v", held)
	}
	if items := plan.Items(); items[1].Needs[0] != "a" || len(items[1].WritePaths) != 1 {
		t.Fatalf("the effective view moved with the caller's spec: %+v", items[1])
	}
}

// The plan's identity covers the capability the registry resolved, not only the
// spec text: one spec compiled against two registries that disagree about a
// tool's ReadOnly yields plans that differ in what the node may do, so they
// must not share a hash.
func TestPlanHashCoversEffectiveCapability(t *testing.T) {
	target := spec(ModeSequence, toolNode("t", "stub_ok"))
	readOnly := newToolSurface()
	writing := tool.NewRegistry()
	calls := 0
	writing.Add(writingStubTool{stubTool{name: "stub_ok", calls: &calls}})
	plain, err := Compile(target, optionsFor(readOnly))
	if err != nil {
		t.Fatalf("compile against the read-only registry: %v", err)
	}
	claimed, err := Compile(target, ValidateOptions{AllowAgentNodes: true, Tools: writing})
	if err != nil {
		t.Fatalf("compile against the writing registry: %v", err)
	}
	if !plain.Items()[0].ReadOnly || claimed.Items()[0].ReadOnly {
		t.Fatalf("the two plans do not differ in effective capability: %v / %v",
			plain.Items()[0].ReadOnly, claimed.Items()[0].ReadOnly)
	}
	if plain.Hash() == claimed.Hash() {
		t.Fatalf("two plans with opposite effective permissions share the hash %q", plain.Hash())
	}
	again, err := Compile(target, optionsFor(readOnly))
	if err != nil {
		t.Fatalf("recompile: %v", err)
	}
	if again.Hash() != plain.Hash() {
		t.Fatalf("the hash is not stable: %q then %q", plain.Hash(), again.Hash())
	}
}

// The MCP refusal uses the dispatcher's own classification, so every form an
// MCP identity takes in this package is refused — and an ordinary tool is not.
func TestMCPRefusalFollowsTheDispatchPredicate(t *testing.T) {
	surface := newToolSurface()
	surface.registry.Add(blankMCPTool{stubTool{name: "stub_mcp_blank", calls: &surface.calls}})
	surface.registry.Add(lifecycleMCPTool{stubTool{name: "mcp_connect__srv", calls: &surface.calls}})
	surface.registry.Add(stubTool{name: "mcp__ghost__raw", calls: &surface.calls})
	surface.registry.Add(stubTool{name: "stub_plain", calls: &surface.calls})
	opts := optionsFor(surface)
	claimsMCP := func(target tool.Tool, _ string) bool {
		_, claims := target.(tool.MCPMetadata)
		return claims
	}
	cases := []struct {
		tool string
		arm  func(tool.Tool, string) bool
	}{
		{"mcp__srv__raw", isMCPExecutionTarget},
		{"mcp__ghost__raw", isMCPExecutionTarget},
		{"mcp_connect__srv", func(target tool.Tool, _ string) bool { return isMCPLifecycleConnectTarget(target) }},
		{"stub_mcp_blank", claimsMCP},
	}
	for _, tc := range cases {
		target, ok := surface.registry.Get(tc.tool)
		if !ok {
			t.Fatalf("%q is not registered, so the case would prove nothing", tc.tool)
		}
		if !tc.arm(target, tc.tool) {
			t.Fatalf("%q is not MCP by its own arm, so a refusal would come from elsewhere", tc.tool)
		}
		if !mcpDispatchTarget(target, tc.tool) {
			t.Fatalf("the unified predicate misses %q, which the dispatcher calls MCP", tc.tool)
		}
		err := ValidateOrchestration(spec(ModeParallel, toolNode("t", tc.tool)), opts)
		if err == nil {
			t.Fatalf("tool node %q was admitted although dispatch routes it over MCP", tc.tool)
		}
		if !strings.Contains(err.Error(), "MCP") {
			t.Fatalf("refusal %q does not name MCP", err)
		}
	}
	plain, ok := surface.registry.Get("stub_plain")
	if !ok || mcpDispatchTarget(plain, "stub_plain") {
		t.Fatal("an ordinary tool is MCP to the predicate, so the refusals above prove nothing")
	}
	if err := ValidateOrchestration(spec(ModeParallel, toolNode("t", "stub_plain")), opts); err != nil {
		t.Fatalf("an ordinary registered tool node was refused: %v", err)
	}
	if surface.calls != 0 {
		t.Fatalf("the refused nodes executed %d calls", surface.calls)
	}
}
