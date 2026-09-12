package agent

import "encoding/json"

// OrchestrationSpecVersion is the only spec version this package accepts.
const OrchestrationSpecVersion = 1

// OrchestrationMaxNodes reuses fleet's own ceiling rather than adding a second
// one, so the two graphs cannot disagree about how large a plan may be.
const OrchestrationMaxNodes = fleetMaxTasks

// OrchestrationMode is a spec's declared convenience shape. It grants nothing:
// what a node may do is decided by the edges and the node's own declarations.
type OrchestrationMode string

const (
	ModeSequence     OrchestrationMode = "sequence"
	ModeParallel     OrchestrationMode = "parallel"
	ModePipeline     OrchestrationMode = "pipeline"
	ModeFanoutReduce OrchestrationMode = "fanout_reduce"
)

// NodeKind is the closed set of node kinds. A fifth kind needs an ADR, which
// is why the type carries no open-ended extension field.
type NodeKind string

const (
	NodeAgent  NodeKind = "agent"
	NodeTool   NodeKind = "tool"
	NodeReduce NodeKind = "reduce"
)

// FieldKind is the declared shape of one published output field. ref is an
// opaque handle: an id, never the body it names.
type FieldKind string

const (
	FieldScalar FieldKind = "scalar"
	FieldList   FieldKind = "list"
	FieldRef    FieldKind = "ref"
)

// ReduceOp is the closed operator set a fanout_reduce plan may apply. These are
// the only operations the reducer performs; the set grows by ADR.
type ReduceOp string

const (
	ReduceSelect ReduceOp = "select"
	ReduceMap    ReduceOp = "map"
	ReduceFilter ReduceOp = "filter"
	ReduceCount  ReduceOp = "count"
)

// FieldSpec is one typed projection a node publishes. Only declared fields are
// addressable by a downstream reference, so an undeclared field is a refusal
// rather than a runtime surprise.
type FieldSpec struct {
	Name string    `json:"name"`
	Kind FieldKind `json:"kind"`
}

// ReduceSpec names the operator a fanout_reduce plan applies. The typed fields
// it publishes live on the reduce node's own Outputs, so there is one place
// that declares them rather than two that can disagree.
type ReduceSpec struct {
	Operator ReduceOp `json:"operator"`
}

// CapsRequest is a spec's request to narrow the session's scheduling ceilings.
// It is a refusal input only: a request above the session's own ceiling fails
// validation, and nothing here can widen one.
type CapsRequest struct {
	MaxNodes    int `json:"max_nodes,omitempty"`
	MaxParallel int `json:"max_parallel,omitempty"`
	MaxWriters  int `json:"max_writers,omitempty"`
}

// NodeSpec is one node of a model-authored plan: declarative data, never code.
// The type has no field for a condition, a loop, or a computed edge, and that
// absence is the anti-drift mechanism this design relies on.
type NodeSpec struct {
	ID      string          `json:"id"`
	Kind    NodeKind        `json:"kind"`
	Needs   []string        `json:"needs,omitempty"`
	Profile string          `json:"profile,omitempty"`
	Prompt  string          `json:"prompt,omitempty"`
	Tool    string          `json:"tool,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	// Outputs names the typed projection this node publishes. A node whose
	// result feeds a reduce must declare it; the compiler cannot guess a
	// schema from a prompt.
	Outputs []FieldSpec `json:"outputs,omitempty"`
	// MaxTokens is this node's declared token ceiling. Only tokens compose
	// across nodes, so it is the one axis Validate can sum.
	MaxTokens  int      `json:"max_tokens,omitempty"`
	WritePaths []string `json:"write_paths,omitempty"`
	ReadOnly   bool     `json:"read_only,omitempty"`
}

// OrchestrationSpec is a model-authored plan. Unknown keys are rejected by the
// host at the JSON decode site, not here: a validator receives an already
// parsed value and cannot see a key the type has no field for.
type OrchestrationSpec struct {
	Version int               `json:"version"`
	Mode    OrchestrationMode `json:"mode"`
	Nodes   []NodeSpec        `json:"nodes"`
	Reduce  *ReduceSpec       `json:"reduce,omitempty"`
	Caps    CapsRequest       `json:"caps,omitempty"`
}
