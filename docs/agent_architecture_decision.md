# Agent architecture decision: agent-driven vs code-driven orchestration

Status: proposed. Scope: the Reasonix Go kernel.
Audience: kernel maintainers, and anyone about to add a "workflow" feature.

## 0. Evidence discipline

This document separates three kinds of statement, and never blurs them:

- **Verified** — a claim anchored to a `file:line` in this repository, checked
  while writing. Every mechanism claim about Reasonix is of this kind.
- **Team-defined** — a term defined inside this project's design discussion.
  PTC and "Dynamics Workflow" are named here as *mechanisms*, per the
  definitions in §1.4. We could not verify either vendor's internal
  implementation from this repository or from public documentation, so no
  vendor internals are asserted as fact. Where a product's behaviour is
  discussed, it is by mechanism only, and the mechanism is attributed to the
  definition rather than to the product.
- **Assumption** — flagged inline as such, with the test that would falsify it.

Any future edit that turns a team-defined mechanism claim into a vendor fact
must add its own evidence. Do not cite this document as proof of what a vendor
ships.

### 0.1 Corrections carried in this revision

A review re-checked the previous draft's `file:line` claims against the tree,
and this revision was re-checked the same way in full. At the P2 hand-off it
carries 167 `path:line` citations over 49 files plus 17 bare `:N-M` ranges that
inherit the file named in their own paragraph, and every one of them resolves
to a file present here and to a range inside that file. The counts are of
*reference sites*, not distinct pairs — a later revision should recount
rather than inherit them, because the previous revision's "75 across 30" was a
different convention on a smaller tree and read as a smaller claim than the
document makes. The paths it names all resolve, with one
declared exception: `internal/orchestrate` does not exist and is not meant to —
it is the rejected alternative of §14 AL2, cited as a discarded option rather
than as a plan. `docs/ORCHESTRATION.md` and `docs/ORCHESTRATION.zh-CN.md`,
which earlier revisions flagged as unwritten, are now in the tree (243 and 155
lines). The two files
the review questioned are both present, neither empty and neither deleted:
`internal/eval/replay/median.go` (66 lines, with its own test and testdata
beside it) and `internal/boot/effect_test.go` (313 lines). The review's nine
findings are recorded here rather than applied silently, because two of them
changed a decision:

1. **The `cancelled`/`skipped` semantics were inverted** against the engine
   they claimed to follow. §7.3 and §12 now state fleet's actual rule. This is
   the one error that would have shipped a defect: it put two opposite
   effect-safety meanings on one status word inside `internal/agent`.
2. **The node-state vocabulary did not match fleet's.** fleet has no `running`
   and spells success `completed` (`internal/agent/fleet.go:96-100`). The draft
   claimed a match, and claimed `cancelled` was new while its own closing
   paragraph correctly cited `fleetItemCancelled`.
3. **D3(a)'s layering argument does not hold.** `tools/repolint/layers.go`
   gates a declared set, not a structural inference, so it would not have
   refused a new package. §5 D3 and §14 AL2 now carry the grounds that do hold.
4. **`internal/boot/effect_test.go` does not pin what D3(b) claimed.** The
   ablation contract is enforced at registration. §17 A2 is now scoped as new
   work rather than an extension of a passing test.
5. **P1 duplicated `fleet`.** `NodeSpec{kind:agent}` is a strict subset of
   `fleetTaskItem`, so §13 re-scopes P1 onto fleet's engine and repairs P0's
   decision rule, whose fallback branch previously landed on the worst outcome.
6. **`internal/plancontract` was never evaluated**, though it is the repo's
   existing plan-as-data layer and this design is a fourth. §14 AL6 evaluates it.
7. **Ablation granularity was left implicit.** §7.4 and §9.3 now name the arm
   the reduction path belongs to, and what P3 owes for it.
8. **P0's method named a harness that does not exist.** `internal/eval/replay`
   is a 66-line median helper over hand-authored JSON pairs
   (`internal/eval/replay/median.go`), whose only caller is its own test; there
   is no session recorder, no replay driver and no eval CLI. D5's "not new
   infrastructure" was therefore false and P0's "one working session" ceiling
   had no referent. §5 D5 and §13 P0 now state the cost and the ceiling is
   removed; the reduction gate is demoted to a post-lowering decision (§13 P0,
   §16).
9. **The §7 interfaces were not constructible as written.** A round-2 review
   checked each one against the tree: `toolOut` does not exist (the real name is
   the unexported `toolOutcome`), `SubagentOutcome` is a persisted record rather
   than a call result, `Validate`/`Compile` were both said to return a `Plan`,
   and step 9's "registered and `ProviderVisible`-or-dispatchable" was two
   mutually exclusive conditions treated as one. §7.1–§7.4 carry the corrected
   types, signatures and the single predicate.
10. **The §7.2 shapes were stated as prose rather than as the checks the
    compiler makes.** §10's rows were right and its "bounded stage fan-out"
    was not a rule at all — the bound is `Caps`, measured against the plan's
    widest level — and §7.1's placeholder paragraph had outlived the code,
    which now gives `ReduceSpec`, `FieldSpec` and `CapsRequest` their real
    fields while leaving `FieldKind` declared-but-unread until P3. §7.1, §7.2,
    §8, §10 and §13 now state each rule where it is enforced, and separate what
    has landed from what the host tool and P3 still owe.
11. **The runner's §7.3 block restated a seam that had been replaced, and its
    landing table denied the runner's own tests.** The code has one `RunOptions`
    struct with `RunAgent`/`RunTool`/`BudgetCheck`/`Checkpoint` and an
    unexported `taskTool` (`internal/agent/orchestrate_run.go:118-130`); the
    draft still showed an `orchestrationSeams`-era constructor and a
    `NewOrchestrateTool` that appeared nowhere in the tree, and asserted a
    `boot.go` construction point that did not exist. (Both landed with P2 —
    `internal/agent/orchestrate_tool.go:36` and
    `internal/boot/boot.go:1161-1162` — which is why this item reads as history
    rather than as a current defect.) `Checkpoint` *is* called
    — once per node from `publish`, and again for anything the terminal
    `reconcile` settles, both in that same file — and
    `orchestrate_run_test.go` (753 lines) covers the four modes, a budget cut,
    cancellation, failure, retry, skip, the per-node checkpoint, and the
    in-package halves of A4 and A5. §7.3, §13's
    P1/P2 paragraphs and its landing table now say so, and the anchor for a
    runner range is the function name rather than a line span that a concurrent
    edit invalidates.

The `gate` node kind is deleted throughout: the previous draft's §19 answered
its own question about it — "a node kind that does nothing a `reduce` cannot is
drift".

## 1. Background and the decision

### 1.1 What triggered this

Reasonix already runs multi-agent work: `task`, `parallel_tasks`, `fleet`, and
`read_only_task` dispatch sub-agents under a shared session scheduler. The
open question is whether the *next* step is a code-driven orchestrator — the
model writing a program that calls tools in a loop, with intermediate results
staying in program variables — or a model-driven workflow, where the next node
is chosen by the model at run time.

### 1.2 The decision

**Adopt Dynamics (model-driven dynamic workflow) as the default and only
always-on path. Admit code-driven orchestration only as *host-side bounded
deterministic reduction*, written by the model as declarative data and compiled
by the host into the existing scheduler. Reject a general-purpose script
runtime.**

The one-line form: the model decides *what to think about*; the host decides
*how it is computed*. The model writes data; the host executes an already
validated graph.

### 1.3 Why this is a real decision and not a default

The repository has already ruled on this boundary once, and this decision
reopens that ruling deliberately:

> Dependencies live here, on the graph, and never on a task spec: what a task is
> does not depend on what ran before it. Keeping them apart is what stops fleet
> from growing into a workflow language.
> — `internal/agent/fleet_graph.go:10-13`

A second, independent warning sits on the adjacent field:

> … admitting it here is the first step from a profile toward a workflow
> language.
> — `internal/agent/profile_spec.go:37-40`

So the burden of proof is on the feature, not on the status quo. §6 records
what would have to be true for the script-runtime answer to win, and §13 P0 is
the measurement that falsifies it.

### 1.4 Terms

| Term | Definition used here |
| --- | --- |
| **PTC** | Programmatic tool calling / code-driven topology. The model writes code; the host executes it; loop, branch, and parallelism live in the code; the topology is fixed when the code is written. Intermediate results stay in program variables. |
| **Dynamics Workflow** | Model-involved dynamic node workflow. The plan is data; the next step is chosen by the model at run time; the graph is discovered rather than declared. |
| **Node** | One unit of work in a plan. Kinds: `agent` (one sub-agent turn), `tool` (one tool call), `reduce` (host-side data reduction over prior outputs). |
| **Closed mode** | One of the four shapes in §10. A plan is a composition of closed modes; there is no free-form control flow. |

Both terms are contested. Round 1 could not establish a shared definition and
this document fixes one (§1.4) rather than assuming one. Any argument that
depends on a *different* definition must restate which one it means.

## 2. Mechanism calibration

The two designs are not competitors on one axis. They optimise different
resources.

### 2.1 PTC optimises tool-call shape

One programmatic call replaces N tool calls. The win is that intermediate
results never enter the model's context. The costs are structural, not
incidental:

1. A sandbox that can contain arbitrary code.
2. A language runtime inside the kernel.
3. A bridge exposing tools as callable functions, which means **each call
   inside the program passes a permission gate** — the gate multiplies by N
   rather than disappearing.
4. A result that is a program's output rather than a tool's output, which is a
   different shape for everything downstream that observes tool calls.

### 2.2 Dynamics optimises agent-call shape

Each node is an ordinary sub-agent turn with its own context. The plan can be
validated *before* anything runs (cycles, duplicate ids, overlapping write
claims). The permission model is inherited wholesale, because every node is a
normal turn going through the normal path. The cost is that each node is a
model round trip, and a run-time-discovered graph cannot be statically costed.

### 2.3 They are orthogonal

PTC saves tokens; Dynamics saves context window. This project's scarce resource
is the **context window** — the compaction, projection, and fold machinery
under `internal/agent/` exists precisely because of it. That asymmetry, not a
general preference, is why Dynamics is the trunk here.

### 2.4 The fence, stated as an invariant

Both designs can be expressed as one rule, which §7 enforces in code:

> **Materialisation rule.** Nothing unbounded crosses a node boundary. A node
> produces either a bounded summary or a typed structured result; a `reduce`
> operator sees only typed fields, never raw unbounded text.

This is what makes the reduction path safe without a sandbox: there is no
program to sandbox, because the operator set is closed and its inputs are typed.

## 3. Applicability and boundaries

**Where Dynamics wins.** Heterogeneous, exploratory work where the next step
depends on what the last one found: investigation, migration planning,
diagnosis, review. The topology is not knowable in advance, and every node
benefits from its own context window.

**Where bounded code-driven reduction wins.** Wide, *known-shape* fan-out with
a small answer: read 200 files, keep the 3 that match; run one command over N
targets and aggregate exit codes. The per-item reasoning is trivial or absent,
so paying a full agent turn per item is waste, and the raw results would blow
the context window.

**Where neither wins.** Work whose control flow is genuinely programmatic and
open-ended (arbitrary loops with computed bounds, recursion, string
manipulation over unbounded content). This is the honest gap in the
recommendation. §14 lists it as the alternative that P0 could promote.

**Hard boundaries.** The orchestrator may not:

- reach the network or filesystem except through a registered tool;
- introduce a script runtime, a new language, or an embedded interpreter;
- compute its own permissions instead of intersecting with the session's;
- dispatch sub-agents when `ablation.Subagent` is off (§9.3);
- bypass writer claims, plan mode, or evidence receipts.

## 4. Comparison

| Dimension | PTC (code-driven) | Dynamics (model-driven) | Decision here |
| --- | --- | --- | --- |
| **Mechanism** | Model writes code; host executes; topology fixed at write time | Plan is data; next node chosen at run time; topology discovered | Dynamics trunk; code-driven only as closed reduction |
| **Applicability** | Known-shape large fan-out; mechanical filter/aggregate | Exploratory, heterogeneous, adaptive | Both needed; the split is by node kind, not by session |
| **Extension & maintenance** | New capability = new language feature or new host binding; grows a surface forever | New capability = a new node kind or a new profile; additive | Closed modes (§10) cap the growth: 4 shapes, no fifth without an ADR |
| **Debugging & observability** | Program state is invisible to the harness unless the host instruments every call; **a missed receipt makes `completion.Build` report `done` for unverified work** | Every node is a normal turn: receipts, events, jobs, checkpoints apply unchanged | Decisive, see §9.4 |
| **Performance & concurrency** | Cheap per call; still bound by the same permission gates, so the gate count grows | Per-node model latency; parallelism via the session scheduler | Reuse `SubagentScheduler` + writeClaims; never a second scheduler |
| **State & context** | Intermediate results in program memory — invisible to the transcript, so nothing replays or resumes | Node outcomes in host state; `jobs` sidecar + `read_subagent_result` + checkpoint give replay and resume | Host-side state, reuse `jobs`/transcript/checkpoint (§11) |
| **Existing Agent/Team/Skill/tool compatibility** | Needs a new exec path beside `runtimepolicy`; risks a parallel permission system | Reuses `TaskSpec`/profiles/scheduler/write claims/tool dispatch verbatim | Dynamics-compatible by construction; PTC only where it compiles into the same path |
| **Learning cost & ecosystem** | Model must write correct code against a bridge; failures are program bugs, not tool retries | Model writes a typed step table (4 fields per node) that `ValidateOrchestration` rejects loudly | Ship the schema, not a language; docs `docs/ORCHESTRATION.md` (§16) alongside the code |

Two rows carry most of the weight, and both are verified rather than argued:

- **Observability.** `internal/completion/report.go` derives its verdict from
  `evidence.Receipt` values — `GapUnverifiedChange`, `GapUnreviewedChange`,
  `GapStaleVerification`, and the rest of the `GapKind` set
  (`internal/completion/report.go:64-101`, `gapsOf` at
  `internal/completion/report.go:245`). A tool call that produced no receipt is
  invisible to that derivation, so the report can say `done` while nothing was
  verified. An execution path that can call tools without receipts is therefore
  not merely harder to observe — it makes the completion report lie, and the
  report is the artifact users trust. That is the strongest single argument
  against a general script runtime, and §7.5 turns it into a test.
- **Compatibility.** `runtimepolicy` guards are monotonic and deliberate:
  "Guards may only add Deny, Ask, Allow, or obligations; they never revoke a
  stronger decision, rewrite a resolved tool identity, or wait on I/O while the
  contract lock is held" (`internal/runtimepolicy/doc.go:1-4`). A second exec
  path has to re-establish every property that engine already provides, for a
  call shape it was not designed for.

## 5. Key disagreements and how they were resolved

Recorded because the losing positions are reasonable and will return.

**D1 — reviewer: "a bounded `repeat{max}` makes cost statically computable."**
*Rejected.* Round count is not the binding constraint. Code-driven topologies
are costable because static dataflow analysis sees every call's schema and
arity. Here, round k+1's specification is produced by round k's model output,
so the call count, the types, and the cost ceiling are undecidable before
execution. A bound gives "at most N rounds", never "at most M tool calls".
Consequence, adopted: bindings are restricted to *closed operators over typed
inputs* (§10), which restores a static bound for the reduction path only, and
the plan's budget is a **refusal condition in `ValidateOrchestration`, never a grant**.

**D2 — reviewer: "adopt the architectural principle, reject the runtime
shape."** *Accepted.* Host-side deterministic orchestration with a
non-model-visible intermediate state is exactly what §7 builds. This is the
part of PTC that survives.

**D3 — coder: an `Executor` interface plus a standalone `internal/orchestrate`
leaf package.** *Rejected, but not on the ground the first draft gave.* The
layering argument was re-checked and fails: `violates`
(`tools/repolint/layers.go:67-77`) fires only for packages listed in the
`leaves` set (`layers.go:24-47`), a hand-maintained list of 22 packages. A new
`internal/orchestrate` would not be in it, would not import `control`, and
would not import a frontend, so repolint would permit it to import
`internal/agent`. Two grounds do hold. (a) It duplicates the fleet assembly:
the graph, its preflight and its dispatch already exist
(`internal/agent/fleet_graph.go:27-63`), so a second package means a second
copy. (b) The import-cycle rule in `REASONIX.md` makes the split fragile in the
direction test files move: the moment an `internal/agent` test reaches for the
orchestrator the build is `[setup failed]`, which is the shape that rule exists
to prevent. Consequence, unchanged: the plan/compile/run state machine lives in
`internal/agent`, beside `fleet_graph.go`, reusing FleetTool's assembly. No new
package, no new scheduler.

Ablation was the draft's second ground and is withdrawn as *evidence*, though
not as a requirement: it is enforced at registration
(`internal/boot/boot.go:1141-1164`), not by the test that was cited. §7.4
states the mechanism and §9.3 the obligation.

**D4 — architect (round 1): "ceilings belong in `Caps`, never per call."**
*Half-corrected.* The principle is right (it mirrors "profiles carry ceilings,
never per-call values", `REASONIX.md`), but it was aimed at the wrong knob:
`DefaultMaxSubagentConcurrency = 6`, `DefaultMaxParallelWriters = 3`, and
`MaxSubagentConcurrencyLimit = 32` (`internal/agent/write_claims.go:12-21`) are
**session-level** limits. So concurrency and write-claim budgets come from the
existing scheduler configuration; `Caps` carries only ceilings the plan cannot
prove for itself.

**D5 — coder: "P0 measurement is procrastination."** *Rejected, with a
concession and a correction.* Reasonix ships an attribution mechanism —
`ablation.Modules()` covers `evidence`, `planner`, `subagent`, `retrieval`,
`compaction`, `full-fold` (`internal/ablation/ablation.go:11-26`) — and the
previous revision claimed that made P0 "one ablation arm against an existing
harness, not new infrastructure". That claim is withdrawn as false.
`internal/eval/replay` holds one file, `median.go` (66 lines), a median helper
over hand-authored JSON pairs with its own test as its only caller; there is no
session recorder, no replay driver and no eval CLI. The ablation modules are
subsystem switches, not an attribution measurement. So P0 *is* new
infrastructure, its "one working session" ceiling was never enforceable, and
§13 P0 is rewritten accordingly. What survives from this disagreement is the
concession, which is the real content of it: a measurement must ship with a
pre-registered decision rule and a costed method, or it becomes open-ended.

**D6 — tester: typed step-graph over an embedded language.** *Accepted*, and it
is why `ValidateOrchestration`/`Compile` types are the deliverable rather than a grammar.

**D7 — architect (round 1): "closed operator sets are just a worse workflow
language."** *Retained as the live counter-argument.* If measurement shows the
dominant context growth comes from long exploratory turns rather than
mechanical fan-out reduction, the operator set buys little and §13 P3 should
not be built; the honest next step is evaluating a real sandbox (§14 AL1).
Keeping this counter-argument in the document is deliberate.

## 6. What would change the decision

The recommendation is falsifiable. It flips to a general script runtime only if
**all** of the following hold:

1. Measurement shows context growth is dominated by mechanical fan-out over
   known-shape inputs.
2. Closed operators plus typed nodes cannot absorb ≥80% of that growth.
3. A sandbox and tool bridge can be built that still produces a receipt for
   every inner call, and an end-to-end test proves `completion.Build` cannot be
   made to report `done` for unverified work.
4. The added runtime does not break prompt-prefix stability or the monotonic
   guard invariants.

Condition 3 is the hard one and is expected to fail. If it fails, the answer is
"bounded reduction, and accept the remaining growth", not "add a language".

## 7. Core modules

All paths are relative to the repository root. New files land under
`internal/agent/`, beside the fleet machinery they reuse.

### 7.1 `OrchestrationSpec` — the model-authored document

Package: `internal/agent`. File: `orchestrate_spec.go`.

```go
// OrchestrationMode is the spec's declared convenience shape (§10). It is named
// apart from the four other `Mode` types in this repository so that a reader
// copying from this document cannot silently land on the wrong one.
type OrchestrationMode string

const (
	ModeSequence     OrchestrationMode = "sequence"
	ModeParallel     OrchestrationMode = "parallel"
	ModePipeline     OrchestrationMode = "pipeline"
	ModeFanoutReduce OrchestrationMode = "fanout_reduce"
)

type NodeKind string

const (
	NodeAgent  NodeKind = "agent"
	NodeTool   NodeKind = "tool"
	NodeReduce NodeKind = "reduce"
)

// OrchestrationSpec is a model-authored plan: declarative data, never code.
type OrchestrationSpec struct {
	Version int               `json:"version"`
	Mode    OrchestrationMode `json:"mode"`
	Nodes   []NodeSpec        `json:"nodes"`
	Reduce  *ReduceSpec       `json:"reduce,omitempty"`
	Caps    CapsRequest       `json:"caps,omitempty"`
}

type NodeSpec struct {
	ID      string          `json:"id"`
	Kind    NodeKind        `json:"kind"`
	Needs   []string        `json:"needs,omitempty"`
	Profile string          `json:"profile,omitempty"` // agent
	Prompt  string          `json:"prompt,omitempty"`  // agent
	Tool    string          `json:"tool,omitempty"`    // tool
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
```

`MaxTokens` is declarative and validate-only: `ValidateOrchestration` refuses a
negative ceiling, sums the nodes' ceilings against `opts.Budget` (§7.2 step 8),
and `Compile` does not carry it onto the dispatcher, so it cannot become a
per-call provider value. Tokens is the only budget axis a spec may declare, and
the reason is in §7.2 step 8: they are the one axis that composes across
nodes, so they are the one a pre-spend refusal can be written over. `Cost` and
`Wall` stay execution-time checks.

`ReduceSpec`, `FieldSpec` and `CapsRequest` are no longer placeholders: the
landed `orchestrate_spec.go` gives each its real fields, and the table below is
what the compiler then enforces over them.

| Type | Fields | Rule the landed compiler enforces |
| --- | --- | --- |
| `CapsRequest` | `MaxNodes`, `MaxParallel`, `MaxWriters` | Non-negative, at or under the session's own ceiling, and at or above the plan the spec declares. **Narrowing only** — a cap is a refusal input, and a plan that does not fit one is refused rather than clamped (`internal/agent/orchestrate_compile.go:465-500`) |
| `FieldSpec` | `Name`, `Kind` | `Name` is what a `$nodes.<id>.<field>` reference resolves against (§8), and a `reduce` node's producers must each declare one. `Kind` is held to its closed vocabulary and **not otherwise read** — see below |
| `ReduceSpec` | `Operator` | In the closed set `select`, `map`, `filter`, `count`; the operator must have a `reduce` node applying it, and only a `fanout_reduce` plan may carry one |

Two of those rules are weaker than a reader would assume, and naming them is
the point of the table. `FieldKind` is now held to its own vocabulary and to
nothing more: `validateOutputs` refuses a declared kind outside
`scalar`/`list`/`ref` (`knownFieldKind`), which closes the gap between the enum
the **tool schema already advertised** and what the validator would accept — a
spec declaring `kind: "banana"` validated before it. The same function closes
the schema's other unenforced promise, `"required":["name"]` on an output: a
field with no name validated too, and because an omitted name and an explicit
`""` are the same value in Go, the validator is the only place that requirement
can be enforced at all. That one had a consequence beyond a dead declaration —
`declaresField` can never match a nameless field, yet its presence satisfied
`validateReduce`'s "the producer declares outputs" check, so a `fanout_reduce`
plan could pass with nothing a reference could resolve. Both corrections are
P2's debt rather than P3's capability, because both enums shipped with P2's
tool; they admit nothing new and cancel nothing. The rest of the weakness stands: an
omitted kind stays legal (the schema requires only `name`), `declaresField`
still matches `Name` alone
(`internal/agent/orchestrate_compile.go`), and **no cross-node kind
propagation happens**. And a `reduce` node's `Outputs` are checked for presence,
never for agreement with what its operator can produce. Those last two belong to
P3's operator work (§13), and neither claim is made by this revision.

Unknown fields are rejected by the **host, at the JSON decode site**, not by
`ValidateOrchestration`: the validator receives an already-parsed value, so an
unknown key is not visible to it. The precedent is the existing tool entry
points, which decode with `dec.DisallowUnknownFields()`
(`internal/agent/fleet.go:169-170`, `internal/agent/parallel_tasks.go:116`). The
declaration above still carries the anti-drift argument, and it is a different
one: a spec that carries control flow (`if`, `while`, a variable, a computed
edge) is invalid **by schema**, because the type has no field for it. Absence of
a field, plus `DisallowUnknownFields` at decode, is what makes "no control flow"
structural rather than conventional.

`NodeSpec.ID` is model-authored, so the validator — not the model, and not
fleet — owns its three rules:

1. **Non-empty.** `fleet` synthesises `strconv.Itoa(i+1)` for an empty id
   (`internal/agent/fleet_graph.go:38-41`), which is harmless for `fleet`'s own
   addressing but would make `$nodes.<id>` (§8) resolve to a name no node
   declares. `ValidateOrchestration` refuses empty before anything compiles.
2. **Unique, verbatim.** Duplicate detection must not trim first. `fleet` trims
   at both `fleet_graph.go:38` and `:50`, so `"a"` and `" a"` are two distinct
   ids in the spec and one id after compilation — a duplicate that would only
   surface from inside `newFleetPlan`, as a confusing "already used by task N".
   `ValidateOrchestration` therefore **rejects a padded id** rather than normalising it.
3. **Stable and well-formed**, over `[A-Za-z0-9._-]{1,64}`.

The placeholder ids above are compiled into, and inherited from, `fleet` — the
orchestrator does not add them, and `newFleetPlan` is not modified to reject
empty ids, because that would change `fleet`'s own tool behaviour (§13 P1: one
graph engine).

One field needs its divergence stated rather than assumed. `NodeSpec.ID` is
model-authored, and the repo's other plan-as-data layer says the opposite:
"Identity is host-assigned. ID and Revision are stamped when a plan is
accepted; a planner submits neither" (`internal/plancontract/doc.go:11-13`).
The justification is scope, and it is narrow: a node id is a call-scoped name
whose only job is to wire `needs` inside one spec, and it never outlives the
call, never keys an evidence requirement, and is never diffed against a
predecessor. The moment a node id has to survive a round — the rebinding case —
that justification is gone, and §14 AL6 is the alternative that owns it.

### 7.2 `ValidateOrchestration` and `Compile`

Package: `internal/agent`. File: `orchestrate_compile.go`.

```go
// ValidateOptions is assembled by the host at registration and is never read
// from the spec, the tool schema, or the call arguments.
type ValidateOptions struct {
	// AllowAgentNodes is false for a team session and true otherwise (§9.5).
	AllowAgentNodes bool
	// Budget is a declared ceiling only. Validate reads no spend.
	Budget agent.TaskBudget
	// Tools is the session's tool registry, consulted read-only.
	Tools *tool.Registry
}

func ValidateOrchestration(spec OrchestrationSpec, opts ValidateOptions) error

func Compile(spec OrchestrationSpec, opts ValidateOptions) (Plan, error)
```

`ValidateOrchestration` is pure: it reads no disk, executes nothing, and reads
no spend. It is **error-only**, and the naming is deliberate — no bare
`Validate` exists in `package agent` today, so the longer name costs nothing and
buys a name that pairs with `OrchestrationSpec` and greps unambiguously. Do not
shorten it back; a future reader who does will not find the pairing this
document relies on.

`Compile` calls `ValidateOrchestration` as its first statement, so there is no
"validated" value to pass around and no way to compile an unvalidated spec: the
runner has only `Compile`, and skipping validation is structurally impossible
rather than a convention. The earlier draft wrote `Compile(validated)` beside an
error-only `Validate`, which cannot be typed in Go — an error-only function
returns no value to compile. Both functions are pure, so `Compile` calling the
validator does not weaken the purity claim. Both return errors; only `Compile`
returns a `Plan`.

It rejects, in this order (order matters — a cheap structural failure must not
be reported as a policy failure):

1. Duplicate, empty, or malformed node ids (§7.1 rules 1–3), unknown `needs`,
   self-edges (`validateNodeIDs`,
   `internal/agent/orchestrate_compile.go:282-313`).
2. Cycles — reuse `fleetPlan.rejectCycles` (Kahn) rather than re-deriving it.
3. `$nodes.<id>.<field>` references (§8): the grammar is checked over `args`
   before any policy step reads the plan (`validateReferences`, `:586-607`).
4. `OrchestrationMode` conformance (§10). This is a **derived check over the
   edges**, not the source of the writer rule: a declared `sequence` grants
   nothing, and a spec whose edges disagree with its declaration is a failure
   (§10). It reads no `read_only` and no `Caps`; the shape must hold on the
   edges alone (`validateModeShape`, `:333-366`).
5. Writers (§9.2), the first step to consult a writer flag at all: a node is a
   writer when its **effective** `read_only` is false — for a `tool` node the
   conjunction `spec.ReadOnly && target.ReadOnly()` (§9.1), so the edges are
   checked against the narrower of the two claims (`validateWriters`,
   `:373-390`). An `agent` node is judged on its declared `read_only`; the
   profile's own `ReadOnly` is a further ceiling that only narrows what this
   step already admitted (§9.1), so it cannot widen it.
6. `needs`/`kind` and reduce shape: a `reduce` node's inputs must all be
   producers declared in this spec and carrying `Outputs`, its operator must be
   in the closed set applying them, and only a `fanout_reduce` plan may carry
   one (`validateReduce`, `:394-450`).
7. Fan-out and node-count ceilings against `Caps` (`validateCaps`, `:465-500`).
8. Budget pre-check, **declarative and spend-free**: the sum of the nodes'
   declared per-node ceilings must fit inside `opts.Budget`. It is `error`-only
   because it cannot say how much is left. No accessor for remaining budget is
   added and none is needed — see §7.3 for where the second check lands. Note
   that `Cost` counts only on priced turns
   (`internal/agent/run_budget.go:116-118`),
   so **tokens is the only axis that composes across nodes**; a spec may declare
   a tokens ceiling, and the other axes stay execution-time checks.
   `MaxTokens` is the one field `Compile` does **not** carry onto the item:
   `fleetTaskItem` has no token field (`internal/agent/fleet.go:79-91`), so the
   ceiling cannot reach the dispatcher by construction, and the declared sum is
   the item's only trace. The landed check is
   `validateDeclaredBudget` (`internal/agent/orchestrate_compile.go:505-520`),
   which also refuses a negative ceiling before it can subtract from the sum.
9. After the team-session gate of §9.5, tool allowlist: every `tool` node names
   a tool that resolves in this session.
   The predicate is **one** condition, not the compound the earlier draft wrote:
   `opts.Tools.Get(node.Tool)` returns `(Tool, bool)`
   (`internal/tool/tool.go:477`). "registered **and** `ProviderVisible`-or-
   dispatchable" is withdrawn: those two are mutually exclusive after boot.
   `fleet`, `task` and `parallel_tasks` are registered and dispatchable through
   `use_capability → workflow:` (`internal/agent/usecapability.go:766-767`) yet
   are absent from `UnifiedProviderToolNames()` (`internal/boot/agent_preset.go:87`),
   so the conjunction would have refused exactly the tools §9.3 and A2 depend on.
   `ValidateOrchestration` therefore calls `Get` directly, which is the fail-closed check;
   `resolveRegistryTool` (`internal/agent/usecapability_registry.go:15`) is a
   secondary consultation and **must not be the gate**, because an unregistered
   name there returns `resolveUnavailable` with a **nil** error (`:28`) — a
   silent downgrade, not a refusal.

   Compile-time argument validation rides the same rule: `Compile` runs the
   existing `tool.ValidateArguments(target, raw)`
   (`internal/tool/arguments.go:78`) over each `tool` node's `Args` and fails
   closed on `CompileErr != nil || len(Violations) > 0`. Two properties of that
   helper matter. It never resolves filesystem or network references
   (`arguments.go:76-77`), so it is safe inside a pure `Compile`. And for a
   third-party/MCP target it **downgrades a schema compile failure to
   `Skipped = true, CompileErr = nil`** (`arguments.go:84-88`) — `Skipped` is
   not a pass. `tool` nodes naming an MCP tool are refused outright: dispatching
   one requires `lockAuthorizedRuntimeServer` plus a runtime spec match
   (`usecapability_registry.go:30-46`), which a pure `ValidateOrchestration` cannot
   reproduce.

   **Landing status.** `validateToolNodes`
   (`internal/agent/orchestrate_compile.go:522-552`) implements all four
   refusals, and it tests them in one order: registered, then `Skipped`, then
   the MCP identity, then `CompileErr`, then `Violations`. The order is
   load-bearing in exactly one place, and that place is the one a test pins:
   `Skipped` precedes the MCP test, so an MCP tool whose schema cannot be
   compiled is refused for the reason that applies to it — "could not be
   validated" — rather than for the one that happens to be checked first.
   `TestUnvalidatableToolSchemasAreRefused`
   (`internal/agent/orchestrate_compile_test.go:449-469`) is that pin, and it
   needs a stub that is deliberately uncompilable **and** carries MCP identity,
   because the plain MCP stub's schema compiles and the reverse order would
   refuse it for the MCP reason instead. Nothing else about the sequence is
   test-visible: no case reaches `CompileErr` and `Violations` at once, so the
   document claims the order as code, not as a proven one.

   **The identity test is `mcpDispatchTarget`**, not the dispatcher's own
   predicate and not the metadata claim alone
   (`internal/agent/orchestrate_compile.go:559-564`). It is a fail-closed
   **superset**: a target that claims `tool.MCPMetadata` is refused whatever its
   names say, and otherwise `isMCPExecutionTarget`
   (`internal/agent/agent.go:2389`, the metadata answer unioned with the `mcp__`
   name prefix) or `isMCPLifecycleConnectTarget`
   (`internal/agent/agent.go:2359`, the `mcp_connect__` connect-and-list
   targets) is enough. Each arm covers the others' blind spot — a placeholder
   whose `MCPServerName()` is empty while its model-visible name still carries
   the prefix, and a blank-metadata tool whose name carries neither — so the
   gate can refuse a dispatch the dispatcher would have run. That is the
   direction it fails in, and `TestMCPRefusalFollowsTheDispatchPredicate`
   (`internal/agent/orchestrate_compile_test.go:700-749`) pins it arm by arm,
   plus the negative case that an ordinary registered tool is admitted.

`Compile` is the pure translation of a validated spec into the existing
dispatch types: `NodeSpec{kind:agent}` → a `ProfileExecSpec`
(`internal/agent/profile_spec.go:59`) — **not** a bare `agent.TaskSpec`, which
carries only `Objective` and `Description` (`profile_spec.go:69`) and cannot
express the profile or the write paths a node declares; the dependency edges →
the `fleetPlan`-equivalent graph the session scheduler already consumes;
`reduce` nodes → a host-side operator application. Compile returns a `Plan`
that is immutable and hashable; the hash goes into the plan's events so a
replay can be matched to the plan that produced it.

What the plan **publishes** is the exported `PlanItem`
(`internal/agent/orchestrate_compile.go:30-38`), not `fleetTaskItem`:
`Plan.Items()` returns `[]PlanItem` deep-copied on every call
(`:75-83`), and the view is derived from the same items the graph was built from
(`planView`, `:170-184`), so the two cannot disagree. `fleetTaskItem` stays
private to the reuse. That is what keeps `fleet`'s schema free to change without
republishing this feature's surface, and it is why `Plan` carries a node table
and a view beside the graph rather than exposing the graph's own item type.

The two accessors publish different things, and the difference is a rule, not a
convenience. `Plan.Nodes()` (`:56`) returns the spec's nodes **as declared** —
the model-authored `read_only` and `max_tokens` claims, unmodified.
`Plan.Items()` returns the **compiled** view, whose `ReadOnly` is the
`effectiveReadOnly` result (`:235-244`) and whose `Needs`/`WritePaths` are the
edges the graph was built from. The runner therefore reads a node's tool, args
and expected outputs from `Nodes()` and takes every permission decision —
writer or not, which claim to hold — from `Items()`. Re-deriving `read_only`
from `Nodes()` would read the claim back as if it were the verdict, which is
exactly the masquerade §9.1's conjunction exists to refuse.

"As declared" is a statement about **whose claims** those are, never about
aliasing: both accessors hand back deep copies, and the node table's slice and
byte fields are copied node by node (`copyNodeSpecs`, `:62-72`). Without that
the caller and the plan would share the `Args` a later mutation could change,
and a run would be attributed to bytes the validator never saw.

"Reuse `fleetPlan.rejectCycles`" above is a stronger commitment than it looks.
`rejectCycles` is a method on `fleetPlan`, and `fleetPlan` is constructible only
through `newFleetPlan(items []fleetTaskItem, failFast bool)`
(`internal/agent/fleet_graph.go:27`). So reuse means compiling `NodeSpec` into
`fleetTaskItem` and calling the existing constructor — not lifting an algorithm
out of it. That is deliberate, and §13 P1 follows it to its conclusion: the
compiler targets fleet's engine rather than standing a second one beside it.
The equivalence P1 must prove is scoped: a spec that declares **explicit ids**
and neither `outputs` nor a `reduce` node compiles to exactly today's
`fleetPlan`. A spec with empty ids is no longer equivalent, by §7.1 rule 1, and
is not part of the claim. Both halves are landed as tests —
`TestExplicitIdPlanCompilesToTodaysFleetPlan` for the equivalence and
`TestEmptyIdIsRefusedWhereFleetWouldSynthesiseOne` for the scope, the second
asserting fleet still accepts what the orchestrator refuses, so the divergence
is pinned from both sides rather than assumed.

### 7.3 Runner

Package: `internal/agent`. File: `orchestrate_run.go`.

The runner is the only component that touches real work, and every seam it has
is supplied by the host and implemented nowhere here. They are function-value
seams on one options struct, not an interface the host implements:

```go
// agentNodeOut is this feature's own narrow projection of one sub-agent call.
// It is deliberately NOT SubagentOutcome, which is a persisted record
// (Ref/Status/FinalAnswer/ErrorCode/Retryable, internal/agent/subagent_outcome.go:26,
// written by SubagentStore.SaveOutcome), not a call result.
type agentNodeOut struct {
	Result   string // the bounded final answer, as RunProfileSpec returns it
	Ref      string
	Receipts []string
}

// toolNodeOut is this feature's own projection of one tool call. It is
// deliberately NOT the unexported toolOutcome (internal/agent/execute_batch.go:50),
// which carries provider run state, images and mutation bookkeeping — an
// internal batch type, not a contract.
type toolNodeOut struct {
	Output   string
	Receipts []string
}

// RunOptions is the seam P2's host tool fills. Every field is optional: a nil
// RunAgent or RunTool refuses the nodes needing it, a nil BudgetCheck reads as
// unbounded, and a nil Checkpoint records nothing. Substituting a field adds
// policy to the session's own path, never a second mechanism.
// (agent.RunOptions, internal/agent/orchestrate_run.go:118-130)
type RunOptions struct {
	// RunAgent is a method value: taskTool.RunProfileSpec
	// (internal/agent/task.go:700). A package-level function may not be
	// substituted — it would drop the per-session closure.
	RunAgent func(ctx context.Context, spec ProfileExecSpec) (agentNodeOut, error)
	// RunTool is the same dispatch path a top-level call takes, which is what
	// makes §17 A4's receipt requirement true rather than aspirational
	// (registryToolSeam, internal/agent/orchestrate_run.go:156-183).
	RunTool func(ctx context.Context, name string, args json.RawMessage) (toolNodeOut, error)
	// BudgetCheck is this turn's bound plus the spend accumulated against it,
	// resolved by the host through taskBudgetLimit and runBudget.exceeded: the
	// runner reads the predicate, never the arithmetic. P1 leaves it nil.
	BudgetCheck func(ctx context.Context) (axis, detail string)
	// Checkpoint is called once per node as its terminal report is filed: after
	// publication, and again in the terminal reconcile for a node the graph
	// settled without a dispatch. Nil skips the call; P1 stores nothing.
	Checkpoint func(report OrchestrationNodeResult)
	// taskTool is unexported and rides the options rather than a second
	// parameter, so the spec an agent node runs is built by the same task tool
	// its RunAgent was bound to.
	taskTool *TaskTool
}

// NewRunOptions binds the runner to the session's own dispatch paths and
// deliberately leaves BudgetCheck nil and Checkpoint unset.
// (agent.NewRunOptions, internal/agent/orchestrate_run.go:137-150)
func NewRunOptions(taskTool *TaskTool, tools *tool.Registry) RunOptions
```

The two optional seams on that struct are the ones P1 adds. **`Checkpoint` is
called by P1, once per node.** A node that dispatched is filed through `publish`
(`internal/agent/orchestrate_run.go:593-612`), which stamps its terminal report
and then hands that report to the field; a node the graph settled without a
dispatch — a dependent of a failure, or a node the terminal sweep cut — gets the
same call in `reconcile` (`internal/agent/orchestrate_run.go:680-720`). So the
sequence a host receives covers every node exactly once, in a terminal state,
and never a pending one. A **nil** field skips the call, which is what P1's own
path does: `NewRunOptions` sets neither `BudgetCheck` nor `Checkpoint`, and
`TestOrchestrationCheckpointsEveryNodeOnce` pins the shape a host that does
supply one observes. The runner therefore *can* report progress without owning
storage; whether anything durable is written stays P2's decision.

**`BudgetCheck` is nil in P1, and P1 reads no spend.** A nil field makes
`budgetCrossed` (`internal/agent/orchestrate_run.go:547-552`) report no axis, so
the per-dispatch check refuses nothing and P1's only budget rule is §7.2 step
8's declarative, spend-free pre-check. What P2 binds at assembly is the pair
this section already names — `taskBudgetLimit(ctx)`, declared at
`internal/agent/run_budget.go:128-136`, and `runBudget.exceeded(limit)`,
declared with its doc comment at `internal/agent/run_budget.go:107-126` —
composed into the one closure `func(ctx) (axis, detail string)`. P2 owns that
closure: it lives at assembly, where the turn's `Agent` is reachable, and P1
takes neither the session's Agent nor its accumulator as an input.

The supplier is worth naming, because the earlier draft said only "supplied by
the host" and the draft's `TaskTool` has no exported field a host could reach.
`NewFleetTool(taskTool *TaskTool)` is constructed with the assembled
`*TaskTool` and calls `RunProfileSpec` on it (`internal/agent/fleet.go:33`,
`internal/agent/fleet.go:341`), and `NewRunOptions(taskTool, tools)`
(`internal/agent/orchestrate_run.go:137-150`) takes that same value. A method
value is required rather than a plain function: `RunProfileSpec` closes over the
session's ablation set, scheduler, workspace lease and transcript store, and
§9.3's structural compliance depends on that closure being the session's. **The
assembly that would supply it is P2's, and it has landed:** the constructor is
`NewOrchestrateTool` (`internal/agent/orchestrate_tool.go:36`), registered from
the `addTaskTool` block (`internal/boot/boot.go:1161-1162`) beside the four
that predate it (`internal/boot/boot.go:1155-1158`), so the runner's caller is
now the session's own registration rather than a test.

The second budget check §7.2 step 8 defers runs through that seam and no other
path: `startOne` asks `budgetCrossed` before dispatching
(`internal/agent/orchestrate_run.go:271-299`), and a non-empty axis refuses the
dispatch, so every node that never dispatched lands in the terminal sweep as
`skipped` carrying that reason, exactly as a cut `fleet` branch does
(`internal/agent/fleet_graph.go:249-255`). No "remaining budget" accessor is
added, because the runner never needs a **number** — it needs the predicate, and
the predicate is what P2 binds.

**The plan is self-sufficient about its operator.** `Plan.Reduce()`
(`internal/agent/orchestrate_compile.go:88-95`) reports the closed operator as a
value copy with an `ok`, and `Compile` stores a copy of `spec.Reduce` rather
than the caller's pointer
(`internal/agent/orchestrate_compile.go:154-158`). `runReduceNode`
(`internal/agent/orchestrate_run.go:361-374`) therefore reads the operator from
the plan it was handed and never from the spec: nodes, compiled view, graph and
operator all travel together, so a run is attributable to the hash alone.

**P1's operators are id-level, and that is a boundary rather than a
placeholder.** `applyReduce` (`internal/agent/orchestrate_run.go:401-422`) — the
runner's operator application, with no `Compile`-side namesake: `Compile`
validates the operator (`validateReduce`,
`internal/agent/orchestrate_compile.go:394-450`) and carries it as a value,
never applying one — switches over `select|map|filter|count` and sees only
`reduceInput{Node, Refs, Completed}` — node ids, published ids, and a completion
bit — so no branch can read a body, and the result it publishes
(`OrchestrationValue{Node, Field, Count, Refs, Pairs}`) carries ids and counts
only. `FieldKind` is declared and read by nothing (`orchestrate_spec.go:33-41`,
§7.1), so typed propagation and the aggregates stated over declared fields are
P3's: what P1 closes is the operator **shape** over ids, and P3 extends the same
set over kinds. A fifth operator is not a P3 extension — §10's closure rule
makes it an ADR.

Node states are `pending|running|completed|failed|cancelled|skipped`. Five of
the six are fleet's vocabulary verbatim (`internal/agent/fleet.go:96-100`);
`running` is the only addition, and it exists because the runner publishes a
node's start as an event where fleet tracks in-flight items positionally. Two
things this corrects in the previous draft: success is `completed`, not `done`,
and `cancelled` is not new — fleet has had it since `fleet.go:352`. The runner:

- acquires no slot of its own: an agent node dispatches through the bound
  `RunAgent`, which is `TaskTool.RunProfileSpec`
  (`internal/agent/task.go:700`), so the `scheduler.AcquireRequest{Writer,
  WritePaths, Nested}` the session already builds
  (`internal/agent/task.go:767-772`) and the ceilings it already enforces are
  what the node inherits — the runner constructs no semaphore and no counter of
  its own;
- writes to the transcript only a bounded per-node summary or the final typed
  result, never raw intermediate content (§2.4);
- emits one event per node (id, kind, start/end, outcome) and relies on the
  ordinary tool path for receipts;
- propagates cancellation from the parent context to every in-flight node;
- on cancel, marks a node that had already dispatched `cancelled` and names the
  effects it may have committed, and marks a node that never dispatched
  `skipped`, so the report can never present a cancelled plan as complete;
- and **returns that cancellation as an error**, so a cancel is never a silent
  success: `collect` (`internal/agent/orchestrate_run.go:648-675`) stamps the
  result `Cancelled` from the graph's own answer and again from any node
  reporting `cancelled`, then returns that result together with
  `firstNonNilErr(ctx.Err(), context.Canceled)` — never a `nil` error and a
  partial report the caller could read as done. A budget axis is checked first
  and wins, and a node that merely failed is not a cancel: it returns `nil` and
  travels in the node state and the verdict.

The `cancelled`/`skipped` split is fleet's, and it runs in the opposite
direction from the one the previous draft assumed. In fleet, `cancelled` means
**started and then killed by the context** — `startOne`'s goroutine is the only
writer of that status (`internal/agent/fleet.go:351-352`) — so it is precisely
the case that *may* carry partial effects and therefore has to be reported as
such. Everything that never dispatched is `skipped`, whatever cut it off: a
failed sibling (`fleetPlan.skipDependents`,
`internal/agent/fleet_graph.go:177`) and a cancelled or budget-exhausted plan
land in the same terminal sweep, which stamps `fleetItemSkipped` with the
first non-nil of `ctx.Err()` and `errFleetBranchNotStarted`
(`firstNonNilErr`, `internal/agent/fleet_graph.go:190-197`) as the reason
(that call site is `internal/agent/fleet_graph.go:253-256`).

The orchestrator adopts that rule unchanged, and it is worth saying why the
inverse is tempting: a never-dispatched node has no effects by construction, so
calling it `cancelled` would let the runner speak from its own bookkeeping with
no receipt check, no tool re-resolution, no scheduler query. That is exactly the
trade `fleet` refuses, and its own comment says why — "started items always
publish one, including after cancellation, so partial writer work is never
reported as a task that never ran" (`internal/agent/fleet_graph.go:200-203`).
Inverting the words would make `cancelled` mean "provably no effects" here and
"may have committed effects" ten files away, and §15 R1 is the risk that lands
on.

What the split buys is also narrower than the draft claimed, and the overclaim
is worth deleting rather than softening: separating "we chose to stop" from
"something broke" is *not* what these two statuses do, because both causes
collapse into `skipped`. What they separate is "may have committed effects"
from "provably did not" — the distinction the report actually needs. Telling a
cancelled plan from a broken one is the job of the per-node reason string and
the plan's own outcome, not of the status word.

### 7.4 Host tool

Package: `internal/agent`. File: `orchestrate_tool.go`. Constant
`tool.HostOrchestrate = "orchestrate"` added to the host-tool block in
`internal/tool/identity.go` (the `HostFleet` entry at `internal/tool/identity.go:10`
is the model to follow).

Registration sits beside the fleet registration in the `addTaskTool` block
(`internal/boot/boot.go:1161-1162`) so the tool inherits the assembled
scheduler, profile lookup, write-root set,
`bashSandboxEnforced`, and capability runtime. That registration plus the late
`BudgetCheck` bind (`internal/boot/boot.go:1717`) is the whole of P2's assembly
change; nothing else in `boot.Build` moves.

`AllowAgentNodes` reaches that constructor as a **field bound at assembly**, not
through the spec, the tool schema, or the call arguments. The one call site now
binds it **`true` unconditionally** (`internal/boot/boot.go`), including for a
team build: §9.5 records why the earlier role-derived binding
(`opts.TeamRole == ""`) was lifted, and §19 Q4 why the difference was removed
rather than extended to `fleet`. What matters for this section is unchanged —
the value is the host's, decided where the tool is constructed, so no spec can
widen it and a host that wants to withhold agent nodes changes one argument. The
precedent is the
deliverable tool surface, which binds its team and member identity when it is
assembled and exposes no argument a caller could use to claim another identity
(`internal/cli/team_deliverable_tools.go:27-41`). The earlier draft left this
unstated, and stated as a consequence that team sessions refuse an `agent` node
without saying what carries the refusal — a rule with no carrier is unimplementable,
since a team session's only tool wiring is `opts.ExtraTools = append(...)`
followed by `boot.Build` (`internal/cli/team_backend_build.go:415-430`), with no
suppression list anywhere in that path. §9.5 states the resulting contract.

That line sits inside the `addTaskTool` closure, which returns early when the
subagent arm is off (`internal/boot/boot.go:1141-1143`). So §9.3 is satisfied by
placement alone, and no per-node ablation check is needed while the tool has
only `agent` nodes. The cost arrives with P3: once `tool` and `reduce` nodes
exist, the reduction path has nothing to do with delegation, and leaving it
inside the subagent arm makes that arm measure two mechanisms at once — against
the single thing `internal/ablation` exists for, attributing a change "to one of
them" (`internal/ablation/ablation.go:1-2`). P3 therefore owes either its own
`ablation.Module` (`Modules()` is an ordered list, so appending is additive —
`ablation.go:24-26`) or a per-node gate. Choosing neither is how the
attribution quietly stops being true.

### 7.5 Observability

Three requirements, in descending order of importance:

1. **Receipt honesty.** Every inner call goes through the same dispatch path as
   a top-level call, so `evidence.Receipt` is produced. The test that pins this
   (§17 A4) asserts that a plan which mutates the workspace without a
   verification step produces a non-empty `Gap` set from `completion.Build` —
   i.e. the report degrades to `partial` exactly as it would for a top-level
   mutation. If that test cannot be written to pass, the design is wrong.
2. **Per-node events.** Node id, kind, timing, outcome, and whether the node
   produced a receipt. This is what makes a plan debuggable without dumping
   context.
3. **Plan identity.** The `Compile` hash enters the event stream, so a run can
   be attributed to the exact plan, and two specs that differ only in a
   semantically irrelevant way are distinguishable rather than confusable. It
   covers the **effective capability** and not only the spec text
   (`planFingerprint`, `internal/agent/orchestrate_compile.go:246-256`, over
   `capabilityWitness`, `internal/agent/orchestrate_compile.go:258-280`): the
   spec's `read_only` is a
   claim, so hashing the declaration alone would give one hash to two plans
   whose nodes may do opposite things.

One caveat on what "untouched" means, because A1 as first written did not cover
it. The provider-visible prefix does stay byte-stable (§13 P2), but the
capability catalog is built from `reg.AllContractEntries()`
(`internal/boot/boot.go:1620`, `:1653`) and the routed decision is rendered as a
transient block prepended to the *user* turn
(`internal/control/capability.go:38-42`). Registering the tool therefore does
change what the model can be shown — on the turn tail, where the cache
tolerates it. `Cache-impact: none` survives; "the model sees nothing new" does
not, and §17 A1 carries the clause that pins the difference — and, with it, the
assertion point: the claim is only meaningful after `boot.Build` has narrowed
the surface (§17 A1), which is where
`TestEffectOrchestrateStaysOffTheProviderSurfaceByDefault`
(`internal/boot/effect_orchestrate_surface_test.go`) takes it.

## 8. Interfaces and data flow

```
model
  │  OrchestrationSpec (data)
  ▼
ValidateOrchestration ──refuse──▶ error (zero side effects, nothing dispatched)
  │ ok
  ▼
Compile ──▶ Plan (immutable, hashable)
  │
  ▼
Runner ──▶ scheduler.AcquireRequest ──▶ TaskSpec / tool dispatch
  │                                        │
  │            host-side node outcomes ◀────┘  (typed fields; never raw content)
  ▼
Reduce (closed operators over typed fields)
  │
  ▼
bounded Result ──▶ tool result / transcript ──▶ evidence receipts ──▶ completion.Build
```

Two properties are load-bearing and easy to lose in implementation:

- **Host-side outcomes.** Node results are held in host state, keyed by node id.
  They do not accumulate in the model's context. The model sees the final
  bounded result; if it needs an intermediate, it addresses it by
  `$nodes.<id>.<field>`, and only declared `Outputs` fields resolve.
- **No interpolation of unbounded content.** A field is either a scalar, a
  bounded list, or an opaque ref. Passing an unbounded string into a `reduce`
  operator is a compile error, not a runtime truncation.

`$nodes.<id>.<field>` is the whole reference grammar, and the compiler reads it
out of a node's `args`:

- the literal prefix `$nodes.`, followed by a token over `[A-Za-z0-9._-]`;
- the token splits at its **last** `.` — the id is everything before it, the
  field everything after;
- the id must name a node that is a transitive predecessor of the referring
  node (reached through `needs`) and never that node itself, and the field must
  appear in the referenced node's `Outputs`;
- a token with no interior dot, or with the dot first or last, resolves to no id
  and is refused rather than ignored.

The check runs in `ValidateOrchestration`, before anything is dispatched, so a
forward edge or an undeclared field never reaches the runner. It is landed:
`parseNodeRefs` scans each node's `args` for the literal prefix and splits the
token at its last dot, leaving `id` and `field` empty where the dot is missing
or leading or trailing, and `validateReferences` then applies the predecessor
and `Outputs` tests (`internal/agent/orchestrate_compile.go:586-607`). The four
refusals it can produce are four cases in §17 A3's table.

## 9. Permissions and invariants

### 9.1 Permission is an intersection

A node's effective permission is the **intersection** of:

- the session's `runtimepolicy` decision for the equivalent top-level call;
- the sub-agent ceiling (profile `AllowedTools`, `ReadOnly`) — ceilings, never
  grants;
- the tool's own declaration (`ReadOnly`, `PlanModeSafe`, `ContextualTool`,
  `EffectHint`);
- the plan's declared `Caps`.

For a `tool` node the `ReadOnly` member is **computed, not read**: the node's
effective flag is the conjunction `spec.ReadOnly && target.ReadOnly()`, taken
over the tool the node names as it is registered in this session
(`internal/agent/orchestrate_compile.go:235-244`). The spec's `read_only` is
model-authored and is therefore a claim; the registered tool's `ReadOnly()` is
the host's answer to the same question, and the intersection takes whichever is
narrower. No spec text widens it, and an unregistered tool is not a loophole —
§7.2 step 9 refuses the spec before this matters.

The computation reads only `opts.Tools`, which `ValidateOptions` already
carries, so it adds no input: `ValidateOrchestration` and `Compile` stay pure
and spend-free, and the runner inherits the flag from the compiled item rather
than re-deriving it, so §7.3 gains no rule of its own here.

Never a union, and never a value the spec supplies. A spec that asks for more
than the intersection is refused by `ValidateOrchestration`, not silently narrowed, because
a silently narrowed plan reports success for work it never attempted.

### 9.2 Writers only in explicit sequence

A writer node may appear only where the plan's edges make it **provably
serial**. The compiler enforces the strict form of that, in two parts, both of
them checks over the edges rather than over the mode's name:

1. **Mode.** A node is a writer when its effective `read_only` is false — for
   a `tool` node that is `spec.ReadOnly && target.ReadOnly()` (§9.1) — and a
   `reduce` node must be `read_only` whatever the mode declares. Every mode
   except `sequence` refuses a writer outright, so there is no "concurrent
   writers declaring disjoint paths" admission at all.
2. **Total order.** `sequence` is accepted only when **every** pair of distinct
   nodes is ordered by the transitive `needs` relation. A writer in a `sequence`
   plan is therefore serial against every node in the plan, not merely against
   the other writers.

The conjunction is what the writer rule rests on, and it moves the verdict in
both directions. A writing tool declared `read_only: true` is still a writer, so
`parallel` and `pipeline` refuse it and only a `sequence` whose edges order
every node admits it — and there it is serial against every node, not merely
against the other writers. The compiled item also carries the conjunction, not
the claim, so a later reader of the plan cannot recover a read-only story the
tool does not tell. A genuinely read-only tool declared `read_only: false` is
still a writer here, by its own declaration, so the flag can be narrowed but
never used to widen a plan. The same count feeds `Caps.MaxWriters`
(`internal/agent/orchestrate_compile.go:487-497`), so a spec cannot slip past a
writer ceiling by declaring its writers read-only.

This is deliberately stricter than `fleet`, which permits concurrent writers
that declare non-overlapping paths (`internal/agent/write_claims.go:18`,
`DefaultMaxParallelWriters = 3`). The reason for the extra strictness is the
model-driven rebinding: a rebinding can change path sets between rounds, so
"disjoint" would have to be re-proved at run time. Requiring static serialisation
removes the proof obligation. Relaxing this to fleet's rule is a possible later
change (§16 exit criteria), gated on the rebinding path proving disjointness at
`ValidateOrchestration` time.

### 9.3 Ablation is honoured

When `ablation.Subagent` is off, `orchestrate` must not dispatch sub-agents —
including via `use_capability`. The enforcement is structural rather than
conditional: the tool is registered inside the closure the arm short-circuits
(`internal/boot/boot.go:1141-1164`), and an unregistered tool cannot be reached
by `tool:`, `task:` or `workflow:` resolution either
(`internal/agent/usecapability.go:754-767`).

`internal/boot/effect_test.go:134-154` does **not** already pin this, and the
previous draft leaned on it twice. Its body asserts that the top-level schema
omits `task`/`parallel_tasks`/`fleet`
(`internal/boot/effect_test.go:148-153`) — true in both
arms, since the top level never carries `task` — so the ablated-arm assertion
says nothing about dispatch. §17 A2 is separate work rather than an extension —
it landed in its own file, `internal/boot/effect_orchestrate_ablation_test.go` —
and the P3 obligation from §7.4 rides with it.

### 9.4 Evidence, plan mode, and write claims are not bypassed

- **Evidence.** Inner mutations produce receipts; §17 A4 is the test.
- **Plan mode.** A plan may run in plan mode only if every node is plan-safe.
  `PlanModeClassifier` (`internal/tool/tool.go:111`) decides per tool; the
  compiler propagates the conjunction.
- **Write claims.** Claims are declared per node and acquired per node through
  the scheduler; the orchestrator never holds a claim the scheduler did not
  grant.
- **Scratch scope.** Paths classified `WriteScopeScratch` by
  `evidence.ClassifyWriteScope` remain non-delivery, exactly as at top level
  (`internal/evidence/writescope.go:33`).

### 9.5 Agent nodes are a host capability, not a team restriction

**This section is a reversal, and the reason it changed is the point.** Earlier
revisions had a team session refuse any node that dispatches a sub-agent, bound
at assembly from `opts.TeamRole == ""`. That gate is **lifted**: every session
binds `AllowAgentNodes: true`, a team member included, and no role withholds
agent nodes today.

What forced the reversal was not a preference but three findings, the first of
which is the one that mattered:

1. **A member is not a nested delegate.** It is its own `boot.Build`
   (`internal/cli/team_backend_build.go:430`), a peer session whose root agent
   runs at subagent depth 0, reached by the leader through a separate process
   rather than through the subagent scheduler. So a member running an `agent`
   node delegates **exactly once**, precisely as a solo session does. R7's
   "double orchestration" described a member-as-subagent shape the
   implementation does not have.
2. **The equivalent capability was never withheld.** The member build only
   *appends* to `opts.ExtraTools` and carries no suppression list, so a member
   already reaches `task`, `parallel_tasks` and `fleet` — and `fleet` fans out
   sub-agents through the same `RunProfileSpec` path an `agent` node uses. The
   gate withheld one spelling of a capability the session already had, which is
   the asymmetry §19 Q4 admitted was one-directional.
3. **Recursion is bounded structurally, and elsewhere.** `max_subagent_depth`
   (`DefaultMaxSubagentDepth = 2`) is enforced per dispatch at
   `internal/boot/boot.go:1272` in every session alike. Whatever bound the gate
   was reaching for, that is where it actually lives.

Against those, the gate's cost was concrete: combined with P2's kind gate, which
admits only `agent` nodes, its admitted set was **empty**, so a team session
could run no plan at all — the leader included, since `roleForLeader` returns
`"leader"` or `"member"` and never the empty string the old test compared
against. The tool was registered there and answered every spec with a refusal.
The team layer sits on top of the baseline, so a baseline capability it removes
for no safety property is a defect in the layer, not a policy.

**The carrier survives the reversal.** `ValidateOptions.AllowAgentNodes` (§7.2)
remains a constructor field of the `orchestrate` tool bound at assembly (§7.4),
still never read from the spec, the tool schema, or the call arguments
(`internal/agent/orchestrate_tool.go`), so a spec cannot widen it and a host
that wants to withhold agent nodes has the mechanism ready. Only the bound value
changed. §9.1's rule continues to apply: a host that does withhold them gets a
**refusal**, not a silently narrowed plan, because a silently dropped `agent`
node would report success for work it never dispatched.

§17 A7 is restated accordingly: its validator half now pins the **mechanism**
(`TestAgentNodesAreRefusedWhenTheHostWithholdsThem`) and its assembly half pins
**parity** — a member session runs a writing plan exactly as a solo session does
(`TestEffectOrchestrateRunsAgentNodesInAMemberSession`). §19 Q4 is reopened and
answered the other way.

What stays unavailable in a team session is only what is unavailable everywhere:
`tool` and `reduce` nodes, which P2's kind gate refuses in every session and
which P3 owns. That is parity, not a restriction.

## 10. Closed modes

A plan is a composition of exactly four shapes. A fifth shape requires an ADR
amending this one.

`OrchestrationMode` is the spec's **declared convenience shape**, not a
capability. It names the shape a reader should see, lets `ValidateOrchestration` check the
spec against the shape its author intended, and is what the closed-form rules
below are stated over. It grants nothing. What a node may actually do is decided
by the edges and the node's own capability declarations (§9), so a `sequence`
declaration never confers the right to write and a `parallel` declaration never
confers a licence to skip a lock: the shape must still *hold* on the edges, and
a spec whose declared mode disagrees with its edges is a validation failure, not
a spec that silently gets the mode's privileges. Mode conformance is therefore a
**derived check over the edges** in `ValidateOrchestration` (§7.2 step 4), not the source of
the writer rule. `fanout_reduce` is the clearest case — its name describes a
topology, while the writer rules live entirely in the edges and the node kinds.

| Mode | Shape | Constraint |
| --- | --- | --- |
| `sequence` | `a → b → c`, total order | **Every** pair of nodes ordered by the transitive `needs` relation, and the only mode in which a writer may appear at all — the edges, not the mode, are what make that safe (§9.2). |
| `parallel` | `a, b, c` with no edges between them | **No edges at all** — every node's `needs` is empty — and every node read-only, because a writer is refused outside a `sequence` (§9.2). Concurrency comes from the session scheduler. |
| `pipeline` | Producer/consumer chains, each stage read-only | Each node declares **at most one** `needs`, and a plan of more than one node declares **at least one** edge; every stage is read-only for the same reason as `parallel`. No cycles; bounded stage fan-out. |
| `fanout_reduce` | N read-only producers → one `reduce` node | The reduction target of the whole feature. Operators are closed: `select`, `map`, `filter`, `count` over **ids** in P1, extended in P3 by named aggregates over declared typed fields. |

Explicitly **not** in the set, and rejected by `ValidateOrchestration`: unbounded loops,
model-authored conditions over unbounded content, recursion, variable
assignment, and any computed edge. "Do something different based on what we
found" is expressed by opening an `agent` node — the model's reasoning belongs
in a turn, not in the graph.

**Landing status.** All four rows are enforced today by `validateModeShape`
(`internal/agent/orchestrate_compile.go:333-366`, dispatch order `:100-136`),
over the edges and never over the mode's name:

- `parallel` is refused the moment **any** node declares a single `needs` — the
  shape is zero edges, not "few" edges;
- `pipeline` refuses a node with more than one `needs`, and refuses a
  multi-node plan in which **no** node declares one, so a declared pipeline that
  is really a parallel plan fails rather than passing as its looser twin;
- `sequence` refuses any unordered pair, which is the total order §9.2 rests on;
- `fanout_reduce` refuses a plan with **no** `reduce` node, with more than one,
  with a `reduce` node that names no operator, or with one whose operator is
  outside the closed set — and every non-reduce node must be a transitive
  ancestor of it (`validateReduce`, `:394-450`).

The read-only half of the `parallel` and `pipeline` rows is not a second check:
it falls out of §9.2's writer rule, which refuses a writer in any mode but
`sequence`, computed over each node's effective `read_only` (§9.1). "Bounded
stage fan-out" is likewise not a shape rule of its own — the bound is `Caps`,
checked against the widest level the plan can actually run
(`maxLevelWidth`, `:702-732`), so a wide-but-legal chain is refused by the cap
it declared and not by the mode it named.

**Landing status of the operators.** The four operators are landed as
`applyReduce` (`internal/agent/orchestrate_run.go:401-422`), stated over ids and
nothing else — `select` keeps every published id, `filter` the ids of producers
that completed, `map` pairs each producer with its id, `count` reports how many
there were. So what P1 closes is the operator **shape**, and what P3 extends is
what an operator may be applied over: the typed half §7.1 records as
declared-but-unread.

## 11. State reuse

No new persistence layer. Three existing ones, used as-is — plus one that landed
outside this design and is reused without a dependency on it:

- **`jobs`** (`internal/jobs/artifacts.go`) for background execution: a plan run
  in the background gets a job id, and `wait` / the job log collect it. Crash
  recovery therefore works without new code.
- **Sub-agent transcripts + `read_subagent_result`** for `agent` node outcomes:
  a node is a sub-agent, so its full result is already retrievable by reference
  while the parent sees only a bounded preview.
- **`checkpoint`** (`internal/checkpoint/`, transaction + atomic JSON) for plan
  progress, so an interrupted plan can verify which completed steps are intact
  (`verify_artifacts` semantics) rather than re-running them. The runner's
  `Checkpoint` seam (§7.3) is where that write belongs; P1 calls the seam but
  leaves it unset, so nothing durable is written until P2 binds it.

The team deliverable store is **landed**, not designed: `internal/team/deliverable.go`
(the store), `internal/cli/team_deliverable_tools.go` (the tool surface it is
reached through) and `team/skills/shared/deliverable/SKILL.md` (the contract
members read). One team's documents live under
`<UserStateDir>/team/cache/<team>/`, each addressed as
`<member>-<slug>-<sha256 first 12 hex>.md` and re-checked against that digest on
every read; `DeliverableMaxBytes` (1 MiB) bounds one document, and publishing the
same bytes again is a no-op returning the same id, so an id is a stable handle a
peer can quote.

v1 uses it as an **artefact outlet, not a data channel**. A plan that must hand a
peer something larger than a bounded result publishes it and carries the *id* —
an id is a scalar, so §2.4 holds: the id crosses a node boundary and the body
does not. The optional-ledger role is that same property read backwards — ids are
content-addressed and immutable, so the outputs of a run stay addressable after
the process that wrote them exits, which is what a later process needs and what a
session-scoped job log cannot give. Both roles stay **optional**: the orchestrator
does not import `internal/team`, nothing in §7 depends on the store, and a plan
reaches it, if at all, through an ordinary `tool` node naming
`member_publish_deliverable` when the session has that tool.

Three boundaries come with it, and none of them is new. The surface is bound to a
team and member identity at assembly (`internal/cli/team_backend_build.go:423-425`),
with the publish half write-class and not plan-safe, so §9.4's plan-mode
conjunction already excludes publishing from a plan-mode turn. The leader half is
read/list only: a leader-session plan cannot publish and reads by id instead.
Publishing is not reporting — `member_report_result` remains the formal handoff,
and a deliverable is not evidence: it never substitutes for an `evidence.Receipt`,
so the receipt requirement at §7.5 and its test at §17 A4 are untouched by
its presence.

One existing layer is deliberately *not* reused, and §14 AL6 argues it rather
than leaving it unasked: `internal/plancontract` is already the repo's "plan is
data, not prose" record (`internal/plancontract/doc.go:1-7`), with host-stamped
ids, `Normalize`/`Validate`, and revisions that `Diff` pairs by id. v1 does not
build on it, which makes this design a fourth plan representation. AL6 states
the reasons and prices the debt.

## 12. Failure, cancellation, retry, budget, concurrency

- **Failure.** A failed node's dependents are `skipped`, each carrying the reason
  — the same semantics `fleet` already implements (`fleetPlan.failFast`,
  `fleetPlan.skipDependents` at `internal/agent/fleet_graph.go:177`).
- **Cancellation.** Parent context cancellation propagates to every in-flight
  node. A node that had already dispatched becomes `cancelled`, and the result
  names the effects it may have committed; a node that never dispatched becomes
  `skipped` — the same terminal sweep `fleet` uses for a cut branch
  (`internal/agent/fleet_graph.go:249-255`). §7.3 is the whole of the rule, and
  it is fleet's rule, not a new one.
- **Retry.** v1 retries a node at most once, and only when the failure is
  classified retryable and the node is side-effect-free. A writer is never
  retried automatically — replaying a mutation needs the ordinary recovery
  path, not a loop in the orchestrator.
- **Budget.** Two checks, in two places, and the split is deliberate.
  `ValidateOrchestration` refuses a plan whose *declared* per-node ceilings do not fit inside
  `opts.Budget` — a spend-free, declarative check (§7.2 step 8). The runner then
  re-checks before **each** node through the `RunOptions.BudgetCheck` seam
  (`agent.RunOptions`, `internal/agent/orchestrate_run.go:118-130`, read by
  `budgetCrossed` at `:547-552`), which P2 binds to
  `taskBudgetLimit(ctx)` (`internal/agent/run_budget.go:128-136`) plus
  `runBudget.exceeded` (`internal/agent/run_budget.go:107-126`), because a
  long plan can outlive the budget it was validated against. **P1 passes nil**,
  so a P1 run reads no spend and its only budget rule is the declarative one;
  P1 neither reads `spent` nor reaches for the session's Agent. No
  remaining-budget accessor is added: the runner needs the predicate, not the
  arithmetic. On exhaustion it stops, reports partial with the axis named, and
  every node that never dispatched is `skipped`.
- **Concurrency.** Always the session scheduler. `DefaultMaxSubagentConcurrency
  = 6` and `DefaultMaxParallelWriters = 3` apply unchanged; the plan cannot
  raise them, and `Caps` may only lower them.

## 13. Phasing

Approved phase names map onto this section as: **P0 measurement** = P0 below;
**P1 declarative graph** = P1; **P2 closed operators** = P2 + P3 (the host tool
is split out only because it is the first cache-visible change and deserves its
own review); **P3 conditional sandbox evaluation** = P4.

**P0 — measurement (pre-registered, and not free).** Determine whether context
growth in real sessions is dominated by (a) mechanical fan-out over known-shape
inputs or (b) long exploratory turns. **Decision rule, fixed in advance:** if
(a) is dominant and closed operators can absorb ≥80% of it, proceed to P1–P2 and
treat P3 as conditional. If (b) is dominant, **stop and build nothing** — record
the outcome with the data and evaluate §14 AL1 instead.

The method has to be stated honestly, because the previous revision overstated
it. Its claim was "an ablation arm plus `internal/eval/replay` comparison over
recorded sessions", and D5 concluded P0 was therefore "not new infrastructure".
Both are false. `internal/eval/replay` is one file, `median.go` (66 lines), a
median helper over hand-authored JSON pairs, and its only caller is its own
test; there is no
session recorder, no replay driver and no eval CLI. `internal/ablation`'s
modules are subsystem switches, not an attribution measurement. So P0 owes a
**minimal harness**: a recorder that turns a recorded session into the
per-turn context-growth series, and a driver that replays that series under one
`ablation` arm and reports the (a)/(b) split. That is a script plus one recorder
— deliberately not a subsystem, no new `internal/` package, and no production
path touched. What is *not* owed is a schedule: the earlier "must not run longer
than one working session" ceiling referred to infrastructure that did not exist,
so it was never enforceable and is withdrawn. What replaces it is the same
discipline in a form that can be checked: **P0 ships its decision rule before it
collects data** (above), and it records its sample size.

**P0's operationalisation, pre-registered.** The (a)/(b) rule above is a claim
about sessions; turning it into a number needs measures, and those are fixed
here **before any data is read**, because a split defined after seeing the
answer proves nothing. The harness lives in `tools/contextgrowth/` beside
`tools/repolint`, not under `internal/` — it is a reader, and no production path
imports it.

- **Unit.** One recorded session is `<state>/projects/<slug>/sessions/*.jsonl`,
  one message per line carrying `role`, `content`/`raw_content`,
  `reasoning_content`, `name` and `tool_calls`. Growth is measured in **bytes of
  message content**, not tokens: tokens are a provider's function of the same
  bytes, and bytes need no tokenizer to agree with.
- **(a) mechanical fan-out over known-shape inputs** is the growth contributed by
  `role:"tool"` messages. These are the bodies a closed operator could replace
  with an id, because they are results crossing a boundary.
- **(b) long exploratory turns** is the growth contributed by assistant
  `content` plus `reasoning_content`. No id-level operator can compress a turn's
  reasoning; §2.4 is what forbids it.
- **Dominance** is the larger of the two masses over their sum, reported as a
  fraction rather than a verdict word.
- **The ≥80% absorbability test** is applied to (a) only, and it is deliberately
  narrow: an operator absorbs a tool result only where the call is
  **fan-out shaped** — the result of a delegation tool (`task`,
  `parallel_tasks`, `fleet`, `orchestrate`, `read_subagent_result`), including
  one reached through `use_capability` with a `capability_id` naming it, or a
  tool whose name repeats **three or more times in the same session**. A one-off
  read is growth a plan would not have restructured, so counting it as
  absorbable would inflate exactly the number the gate turns on.
- **A second figure is reported beside it**, because it asks a different
  question and the verdict turned out to hinge on it: the share of (a) that
  arrived through a delegation tool at all. The gate asks whether an operator
  could *reach* the growth; this asks whether the workload contains the fan-out
  the gate was written about. A harness that reported only the first would leave
  a reader unable to tell the two apart.
- **Sample size** is recorded with the verdict: sessions read, sessions skipped,
  and total bytes attributed.

One honest limitation, stated before the run rather than after. §13's earlier
sentence called for a driver that "replays that series under one `ablation`
arm". A live counterfactual re-run would need provider spend and a recorded
request stream this repo does not keep, so what the driver does is **analytic
attribution over the recorded series** — it reports what the growth was and how
much of it an operator could have absorbed, not what a re-run would have cost.
That is enough for the gate, which asks where growth comes from, and it is less
than the earlier sentence implied.

**P0's outcome: measured, and P3 is cancelled.** The harness ran over this
machine's recorded sessions with the rule and the measures above fixed in
advance. Two samples, because the only free parameter is a length filter and
nothing should turn on where it sits:

| Sample | Sessions | Attributed | (a) tool-result | (b) assistant | of (a), absorbable | via delegation | Gate |
| --- | --- | --- | --- | --- | --- | --- | --- |
| all transcripts | 43 | 0.7 MiB | 69.7% | 30.3% | **51.2%** | **0.2%** | 80% |
| ≥20 messages | 7 | 0.4 MiB | 63.0% | 37.0% | **44.4%** | **0.0%** | 80% |

The verdict is the same at every filter that has data (`-min-messages` 0, 5, 20
and 50 give 51.2%, 51.2%, 44.4% and 44.5%): **P3 does not ship.**

**It fails on the second condition, not the first, and that distinction is the
finding.** (a) dominates — comfortably, and by the widest sample overwhelmingly
— so the earlier worry that this repo's sessions would turn out to be one long
exploratory turn is not what the data says. What the data says is that growth
which is (a) *by role* is not (a) *by character*: the top tool is `bash` at 66
calls, followed by one-off `read_file` and team status reads, and those are
varied one-shot calls over targets that differ every time. A closed operator
replaces a body with an id only where a known shape repeats, so it can reach
roughly a quarter of this growth and not the four fifths the gate asks for.

This is precisely what the narrow absorbability test was pre-registered to
catch. Had P0 counted every tool result as absorbable — the obvious and looser
definition — the widest sample would have read "69.7% mechanical fan-out,
ship P3", and the phase would have been built on a measurement that never tested
its own premise.

The measured case is also a **third** case, which the rule's prose did not
enumerate: it named (a)-dominant-and-absorbable and (b)-dominant, while this is
(a)-dominant-and-not-absorbable. The ship condition is a conjunction and it
fails, so the consequence for P3 is unambiguous. What changes is the pointer:
the rule sent a (b)-dominant outcome to §14 AL1, and AL1 is a *general script
runtime* — the answer to growth that operators cannot reach because it is
reasoning. It is not the answer here, and §6 condition 3 still gates it. Growth
that is exploratory *tool* output is a retrieval and compaction question, which
is where `internal/ablation`'s `Retrieval` and `Compaction` arms already point.

**The hardest limitation, and it is not the sample size.** Delegation output is
**0.2% of tool-result growth** in this corpus — 1,061 bytes of 479 KB. The
workload contains almost no fan-out at all, so the gate's own subject is largely
absent from the population it was applied to.

That does not weaken the cancellation; it sharpens the reason for it. The gate
was written to ask *"could closed operators absorb the mechanical fan-out?"* —
and the data answered a prior question instead: *"there is no mechanical fan-out
to absorb."* P3 exists to compress fan-out, so a corpus without fan-out is the
strongest possible case against building it. But the two readings imply
different things about the future, and a reader deserves both. Under the first,
P3 stays cancelled until operators get better at reaching diverse growth, which
nothing on the roadmap proposes. Under the second, P3 is cancelled *for now*:
if this workload's delegation share rises — and it plausibly could, since
`orchestrate` and `fleet` are now usable from team sessions — the same harness
re-run would be measuring a different population, and the question would be
worth asking again rather than assuming this answer carries.

**Sample size, stated as the second limitation.** 43 transcripts from one
developer, weighted to one project and one recent stretch of work. That is thin,
and a broader corpus could move the percentages. It would have to move them a
long way: absorbability would need to rise by more than half from its best
observed figure to reach the gate, and it did not approach it under any filter.
A leave-one-out over every session — the check that would expose a verdict
resting on one file — changes nothing: **0 flips out of 43** at the full sample,
and 0 of 7 at ≥20 messages, with absorbability staying between 42.4% and 59.5%
against an 80% gate. No single session dominates: the largest carries 21.6% of
everything attributed (148,865 B of 688,071), and `-sensitivity` drops it and
re-derives 53.9% — still under the gate. Re-running is one command —
`go run ./tools/contextgrowth -sensitivity` — so a later reader with more
sessions should re-measure rather than trust this table.

**A third limitation, and it is the one that cost this analysis a revision.**
An earlier pass of this section measured `raw_content` — the full local
original — instead of `Content`, the bounded wire representation. Those are not
close: one recorded `bash` result is 2,836,161 bytes locally against **19,384**
on the wire, because `maxToolOutputBytes` (32 KiB, `internal/agent/agent.go`)
bounds provider-visible `Content` while `RawContent` retains the original for
session-scoped paging and is **always stripped by a provider projection**
(`internal/provider/projection.go`). Measuring the local original inflated the
corpus from 0.7 MiB to 4.1 MiB, pushed (a) from 69.7% to 95.1%, and pushed
absorbability *down* from 51.2% to 23.4% — because the inflation landed almost
entirely on one non-repeating `bash` result, which the narrow test correctly
declines to call fan-out. The verdict was the same under both, which is luck
rather than robustness: the corrected margin is 51.2% against 80%, materially
narrower than the 23.4% first recorded. `-local-original` still reports the
other question deliberately, and the default is now the wire. **The lesson
generalises past this feature: a token or context measurement that reads
`raw_content` is measuring disk, not spend.**

Per this section's own terms, **a cancelled P3 is a successful outcome, not a
failure**: the gate did the job it was pre-registered to do, and it did it by
refusing the phase it was most expected to wave through.

**Two corrections to the harness were found by auditing it after the verdict,
and neither moved the numbers.** The prose above named `use_capability`
reaching a delegation tool as fan-out while the recorder did not implement
that case; the recorder now resolves each such call's `capability_id` and
attributes the result accordingly, which changes the figure by 0.2% — the
verdict was right for a reason the code had not actually tested, which is worth
recording rather than quietly fixing. And the recorder counted
`__reasonix_local_only__`, a display-only sentinel (`provider.LocalOnlyToolName`)
that is not a call, as an ordinary tool result. Both are fixed; the verdict at
every filter is byte-identical to the one first reported.

**The reduction gate is a post-P1 decision, not a pre-P1 one.** The previous
revision put P0 in front of everything, which with the harness unbuilt would
have blocked P1 indefinitely. P1 and P2 are executable today and are additive:
they extend `fleet`'s schema, add the spec type and compiler, and register a
tool that changes no provider-visible surface. So P0's verdict is taken **at the
end of P2**, where its own recorder exists and has real sessions to read, and it
decides one thing: whether P3's `tool`/`reduce` operators ship. If the verdict is
(b), P3 is cancelled, P1 lands as `fleet`'s validation improvements, and the
document records that outcome with the data. A cancelled P3 remains a successful
outcome; a P0 that gated P1 would have been neither.

The previous draft's fallback was "if (b) is dominant, stop after P1 — the
declarative graph still earns its keep for explicit fan-out". That is wrong,
and it is worth recording why, because it is the kind of fallback that reads as
safe. A declarative graph with no reduction is a second spelling of `fleet`.
Put `NodeSpec{kind:agent}` beside `fleetTaskItem`
(`internal/agent/fleet.go:79-91`) and the node is a strict *subset*: same `id`,
`depends_on`/`needs`, `prompt`, `profile`, `write_paths`, `read_only`, minus
`description`, `tools`, `max_steps`, `model`, `effort`. §7.2's id and cycle
refusals are largely `newFleetPlan`'s own checks — duplicate ids and unknown
`needs` at `internal/agent/fleet_graph.go:43-61`, cycles at `:62` — and fleet
already carries `fail_fast`, `skipDependents`, the write-claim
preflight, and background-job collection. Stopping after P1 would ship a
duplicate schema, a second id space and a new capability entry in exchange for
nothing the session cannot already do. When (b) is dominant the safe fallback
is to ship nothing.

**P1 — the graph, on fleet's engine.** *Retained, but its rationale narrowed by
the reconsideration at the end of this section: P0 cancelled the context saving,
so P1 now stands on its reference language and in-band permission claims rather
than on anything measured.*
 Add `outputs` and the `reduce` item kind
to the schema `fleet` already exposes (`internal/agent/fleet.go:43-75`), with
validation extended inside `fleet_graph.go` rather than duplicated beside it:
one graph engine, one status vocabulary, one preflight. `orchestrate_spec.go`
and `orchestrate_compile.go` still own the spec type and its compiler, and
`Compile`'s `NodeSpec → fleetTaskItem → newFleetPlan` path (§7.2) is what makes
the reuse real rather than nominal. No host tool, no UI, no prompt change, no
cache exposure. A spec that declares **explicit ids** and neither `outputs` nor
a `reduce` node must compile to exactly today's `fleetPlan` — that equivalence
is the phase's test. It is scoped to explicit ids deliberately: §7.1 rule 1 has
`ValidateOrchestration` refuse an empty id, which `fleet` itself would have accepted by
synthesising a placeholder, so an empty-id spec is no longer equivalent and is
not claimed to be.

**P1's first sentence is superseded by how P1 landed, and the `outputs`/`reduce`
additions to `fleet`'s own schema are deliberately not built.** The instruction
assumed the orchestrate path would reach fleet's engine through fleet's
*decoder*. It does not: `compileItems`
(`internal/agent/orchestrate_compile.go`) builds `[]fleetTaskItem` values
directly in Go and hands them to `newFleetPlan`, so fleet's JSON schema is never
on the path and adding fields to it would buy the compiler nothing. The division
that actually landed is cleaner than the one P1 described — the **runner** owns
reduction (`applyReduce`, §10) and fleet's engine owns only edges, cycles and
the write-claim preflight, which is why `compileItems` can flatten every kind,
reduce nodes included, onto a plain `fleetTaskItem`: fleet never needs to know a
node reduces. Three costs would follow from doing it anyway. It would put the
reduce vocabulary in two places that can disagree, against the rule
`ReduceSpec` states for itself — "one place that declares them rather than two".
It would make reduction addressable by the model *through `fleet`*, which
pre-commits the surface P0 exists to be able to cancel. And it would be the
second spelling this section's own P0 fallback warns against. One graph engine
was the goal, and the landed shape already has it; the schema edit was a means
that turned out not to be needed.

P1 also owns the runner (§7.3), and its boundary is stated with the operator
set: the four operators are **id-level** and closed, so a P1 run moves ids and
counts across node boundaries and never content. Two seams on `RunOptions` are
P1-shaped rather than P1-bound — `Checkpoint` is supplied only by a test and
`BudgetCheck` is nil, so a P1 run reads no spend and writes no durable progress
record of its own. P1's acceptance is the in-package one, and it is **landed**:
the compiler's refusals (§17 A3's validator half), the ordering case (A5's
in-package half), A7's mechanism half, and the runner's behaviour — the four
modes, a budget cut, cancellation, failure, retry, skip, the per-node
checkpoint, and A4's verdict rule — in `internal/agent/orchestrate_run_test.go`
through a `RunOptions` the test supplies. No host tool and no `boot` boundary:
those rows are P2's and P3's.

**P2 — host tool, `agent` nodes only.** `orchestrate_tool.go`, registered beside
`fleet` in the `addTaskTool` block (`internal/boot/boot.go:1161-1162`, next to
the four that predate it at `:1155`, `:1158`), so the runner has a production
caller and the tool is dispatchable through
`use_capability → tool:orchestrate` (`internal/agent/usecapability.go:757`,
`internal/agent/usecapability_batch.go:49`) without entering the
provider-visible surface (`internal/tool/tool.go:305-308`), so
`Cache-impact: none` holds by default. Opting in is §19 Q1's answer: the
carrier is the existing host-tool channel, never a new key.
Note that `workflow:` is already a live capability prefix resolving to a
registry tool (`internal/agent/usecapability.go:766`), so `workflow:orchestrate`
becomes addressable the moment the tool registers. That is free, and it is also
the one place this feature's surface collides with the name §1.3 says it must
not become. Say so in `docs/ORCHESTRATION.md` rather than discovering it in
review. P2 also binds `RunOptions.BudgetCheck` at assembly, to
`taskBudgetLimit` + `runBudget.exceeded` (§7.3) — the one place the runner gains
a real spend check, and the reason P1 can ship it nil.

#### P1 reconsidered

P0's verdict changes what P1 is, so P1 has to be re-argued rather than left
standing on a rationale that no longer holds. The reconsideration is recorded
here in full, including its counter-arguments, because the case against keeping
P1 is not weak.

**The case against.** §13's own P0-fallback paragraph is the strongest thing
that can be said against the shipped feature, and it was written about P1:

> A declarative graph with no reduction is a second spelling of `fleet`. Put
> `NodeSpec{kind:agent}` beside `fleetTaskItem` and the node is a strict
> *subset*: same `id`, `depends_on`/`needs`, `prompt`, `profile`, `write_paths`,
> `read_only`, minus `description`, `tools`, `max_steps`, `model`, `effort`.
> §7.2's id and cycle refusals are largely `newFleetPlan`'s own checks …
> and fleet already carries `fail_fast`, `skipDependents`, the write-claim
> preflight, and background-job collection. Stopping after P1 would ship a
> duplicate schema, a second id space and a new capability entry in exchange for
> nothing the session cannot already do. When (b) is dominant the safe fallback
> is to ship nothing.

Every sentence of that now applies to what shipped. Reduction is cancelled, so
the "with no reduction" premise is not hypothetical — it is the delivered
article. Measured against `fleet`, `agent` nodes add `RecordForRef`/`$nodes`
references, mode-shape validation, and declared budget; they subtract
`description`, `tools`, `max_steps`, `model`, `effort`, and *both* decoders
reject unknown keys (identified at `internal/agent/fleet.go:170`), so neither
tool can express what the other does. The feature cost roughly 1,834 lines of
production code and 2,645 lines of tests, against 1,060 for the whole fleet
side. Six `boot`-boundary tests run on every CI run to pin a surface most users
will never reach.

**The case for, on the merits.** Three things are not duplication, and each is
checked against code rather than asserted:

1. **A reference language `fleet` cannot express.** `fleet`'s `depends_on` is
   ordering and nothing else — a handoff carries no value. `$nodes.<id>.<field>`
   is a data dependency with per-node declarations, transitive-predecessor
   enforcement, and a resolution that yields `handleFor(id, field)` — the
   **receipt id**, never the body (`orchestrate_run.go:515`). §2.4 calls that
   fidelity load-bearing, and it makes a receipt the unit of currency across a
   node boundary. `fleet` has no analogue.
2. **A permission level genuinely between `task` and bare `fleet`.**
   `read_only: true` is a claim, not a grant: `effectiveReadOnly` conjoins it
   with the registered tool's own answer
   (`TestWriterToolNodesNeedBothReadOnlyFlags`), and §9.2 refuses a writer
   outside a total order. So a user can say *"fan out, but nothing may mutate"*
   in-band — where the session's posture is read-only and the **spec** is
   untrusted. `fleet` has no such in-band assertion.
3. **The team layer's whole point.** This revision lifted the team gate under
   the rule that the baseline's features should be the team layer's features
   too. Deleting P1 would re-create the same defect at a larger scale: the team
   layer would have strictly less than the baseline. That coherence argument is
   the strongest one here, and it is a fairness argument rather than an
   engineering one — worth saying plainly.

Against which: **P0 supplies no evidence for any of the three.** It measured
whether closed operators could absorb growth, not whether these three help. So
the case for P1 is structural, not measured, and should be read that way.

**Decision: P1 is retained, and the duplication finding is accepted rather than
refuted.** What §13 identified is real — `fleet` with `needs` can express most
of what an `agent`-only plan expresses, less ergonomically. The three points
above are why that is tolerable rather than fatal, not a demonstration that the
duplication is absent. §13's original fallback — *"when (b) is dominant the safe
fallback is to ship nothing"* — is therefore **superseded**, and deliberately:
it was written to argue against landing P1 *for* reduction, and reduction is gone
whether or not P1 stays. Removing P1 now would not un-spend what it cost.

Two consequences follow, and a reader should hold both:

- The honest one-line description of the shipped state is: **P1 is a thinner
  `fleet` with in-band permission claims and id-level handoffs; it earns its
  keep on those two, not on context saving.** P0 cancelled the context saving.
  Anyone reading `docs/ORCHESTRATION.md` hoping for the latter should be told
  that plainly, and the guide now does.
- **The reversibility is asymmetric.** `orchestrate` could be removed without
  touching `fleet`, so this decision is cheap to revisit; the reverse is not
  true, which is the ordinary argument for keeping an additive tool. That is a
  weak reason to keep something and is offered as such.

**P3 — closed operators over typed fields. CANCELLED by P0's verdict, and its
unreachable code is retained as reserve.**

Two separate things are settled here, and the project lead decided both. P3 does
not ship. And the `tool`/`reduce` machinery that already exists — `applyReduce`,
`validateReduce`, `reduceInput`, the tool-node validation, the `PlanItem.Kind`
dispatch — **stays in the tree** rather than being deleted with the phase.

That second decision needs its reasoning recorded, because deleting it was the
obvious alternative. The code is live, tested, and reachable from the library
API (`Compile` + `RunOrchestration`); what it is not is reachable from the host
tool, whose kind gate admits `agent` nodes only. Retaining it costs nothing that
a reader would otherwise have to pay for: it is off the provider surface, it
adds no runtime, and its tests run in the ordinary suite. Deleting it would
remove the only written record of what the four operators were specified to
mean, and reopening P3 after §6's conditions hold would mean re-deriving them.
The cost is that the tree carries code no production path can reach, which is
the trade the lead accepted; the mitigation is this paragraph, so that a reader
who finds `applyReduce` uncalled from any tool learns here that this is
deliberate rather than rot.

So the honest description of `tool` and `reduce` nodes is **reserve**, not a
staging post and not dead weight. `docs/ORCHESTRATION.md` says the same thing in
its limits section, from the user's side.

**P3 — closed operators over typed fields.** `tool` and `reduce` nodes with the
§10 operator set extended across kinds, `FieldKind` propagation, typed
`Outputs` (kind agreement between a producer and the operator that consumes it),
budget refusal, and the ablation obligation §7.4 attaches to this phase.
`FieldKind`'s *vocabulary* check is no longer part of this list — it landed with
P2's debt (§7.1) — but the propagation this phase names is untouched by that.
**P0 has now returned its verdict and it cancels this phase** (§13 P0). The gate
worked as designed: it refused the phase it was most expected to wave through,
on the second of its two conditions.

What P3 still owes, stated as work rather than as a phase name: (a) the two node
kinds generally — today the host tool admits `agent` nodes only
(`internal/agent/orchestrate_tool.go:127-131` rejects any other kind before
`Compile` sees it), so `tool` and `reduce` nodes are neither dispatchable from
the tool nor extended across kinds; (b) typed `FieldKind` propagation and the
kind agreement between a producer and the operator that consumes it; (c) the
acceptance rows over typed fields — A5 and A4 are both fully landed now (§17),
so what P3 adds here is the typed half only, not a missing boundary case; (d) the deletion of the kind
gate in (a), which
is the same edit that makes §7.4's multi-mechanism ablation cost arrive.

Already landed and not P3's: §17 A1, A2 and A6 at the `boot` boundary, A3's
validator half, A5's in-package half, A7's mechanism half, and A4's in-package half
together with the four modes, cancellation, retry and skip — the compiler's and
the runner's own suites (§13 below). **Team `agent` nodes are not a P3 item**:
a team session refuses them by construction and P3 does not change that gate;
relaxing it would be a decision about R7 (§19 Q4), not a phase of this design.
This is the phase that delivers the actual context saving, and the phase P0 can
cancel.

**P4 — conditional sandbox evaluation (expected not to be built).** Only if §6's
conditions hold. Note that `internal/sandbox/` and `internal/shellrun/` confine
*shell execution*; they contain no tool bridge, so a PTC bridge would be a new
subsystem rather than a reuse.

**Landing status.** Thirteen Go files under `internal/`, three more in `tools/contextgrowth/`, plus the two integration guides: the spec
type, its compiler and the compiler's tests, P1's runner and the runner's tests,
P2's host tool and the tool's own tests, four `boot` boundary tests that close
P2's surface rows and A4, A5 and A2, and the two in-package `fleet` cases that
complete A5 and pin P1's equivalence claim.
P1's *other* half has still not landed — `outputs` and the `reduce` item kind
added to `fleet`'s own schema (`internal/agent/fleet.go:43-75`) — and neither
has anything §13 lists as a later phase. What the runner's own acceptance *is*
done: its `RunOptions` seam is complete and P1-correct, and
`orchestrate_run_test.go` drives it through all four modes plus a budget cut,
cancellation, failure, retry, skip and the per-node checkpoint. The runner now
has its **production caller**, so the `boot`-boundary rows A1, A2, A4, A5 and
A6 are pinned rather than open,
A3's decode-level half landed with the tool, the two
integration guides are written (`docs/ORCHESTRATION.md` and its zh-CN pair),
and what remains open is every item §13 assigns to P3.
The boundary between what is built and what is still owed is the list at the end
of this section.

| File | Lines | What it is |
| --- | --- | --- |
| `internal/agent/orchestrate_spec.go` | 109 | §7.1's spec type and its four closed vocabularies |
| `internal/agent/orchestrate_compile.go` | 765 | §7.2's `ValidateOrchestration`, `Compile` and `Plan` |
| `internal/agent/orchestrate_compile_test.go` | 763 | §17 A3's validator-level cases, A5's ordering case, and A7's mechanism half |
| `internal/agent/orchestrate_run.go` | 744 | §7.3's runner: the `RunOptions` seam, the four id-level operators, and the fleet-driven dispatch loop |
| `internal/agent/orchestrate_run_test.go` | 753 | §7.3's runtime cases in-package: the four modes, a budget cut, cancellation, failure, retry, skip, the per-node checkpoint, A4's verdict rule over real receipts, and A5's in-package slot/claim case |
| `internal/agent/orchestrate_tool.go` | 216 | P2's host tool: the strict decode, the kind gate, the assembled `AllowAgentNodes`/`Budget`/`BudgetCheck` fields, and the `Validate → Compile → Run` chain |
| `internal/boot/effect_orchestrate_surface_test.go` | 143 | §17 A1 and A6 at the `boot.Build` boundary |
| `internal/boot/effect_orchestrate_ablation_test.go` | 24 | §17 A2: the ablation removes the tool structurally |
| `internal/agent/orchestrate_node_observability_test.go` | 109 | the per-node observability contract: timing bounds on the report and the event, and a receipt count taken from the turn's ledger |
| `internal/agent/orchestrate_tool_test.go` | 168 | §17 A3's decode-level half at the tool boundary, plus the refusal of a version and a kind the tool does not run |
| `internal/boot/effect_orchestrate_receipt_test.go` | 178 | §17 A4's `boot` half: one headless turn reaching the tool through `use_capability` |
| `internal/boot/effect_orchestrate_slots_test.go` | 151 | §17 A5's `boot` half: a wide parallel plan over the assembled session scheduler |
| `internal/boot/effect_orchestrate_team_test.go` | 84 | §17 A7's parity half: a member session *runs* a writing plan through `use_capability`, and the file lands |
| `internal/agent/orchestrate_fleet_equivalence_test.go` | 57 | P1's own claim: an explicit-id spec with neither `outputs` nor a `reduce` node compiles to exactly the `fleetPlan` the same work expressed directly to `fleet` builds, plus the empty-id divergence that scopes it |
| `internal/agent/orchestrate_fleet_contrast_test.go` | 215 | §17 A5's `fleet` contrast: a plan and a `fleet` concurrent over overlapping `write_paths` on one scheduler, and the same-file exclusion at `Realize` that makes the overlap safe |
| `tools/contextgrowth/record.go` | 182 | P0's recorder: one transcript to its attributed growth, with the fan-out test §13 pre-registered |
| `tools/contextgrowth/main.go` | 215 | P0's driver: the (a)/(b) split, the 80% gate, and the verdict |
| `tools/contextgrowth/record_test.go` | 137 | the harness's own cases: role attribution, the repetition threshold, and both halves of the ship condition |
| `docs/ORCHESTRATION.md` | 336 | the shipped API, in the mode `docs/ACP.md` established |
| `docs/ORCHESTRATION.zh-CN.md` | 190 | its zh-CN pair |

The line counts are `wc -l` on the tree this revision was checked against;
they move with any edit to those files, so a later reader should re-measure
rather than trust them, and the anchors in §7.3 are named for the same reason.
One caveat on this whole table, which the concurrent P2 landing made concrete:
every number in it moved at least once while this revision was being written —
`orchestrate_run_test.go` was 716, then 845, then 753; `orchestrate_tool.go` was
204, then 243, then 216 — because several members were editing these files at
the same time. Treat the column as a dated observation, not as a claim, and cite
the symbol name rather than the span when the exact figure matters.

What that covers, each point landing as a refusal a test names:

- **§7.2's rejection order** — node ids, the graph (fleet's own constructor),
  §8's references, mode shape, writers, reduce shape, caps, declared budget, the
  team-session gate, then tool nodes (`ValidateOrchestration`, `:100-136`),
  matching the numbered list above step for step.
- **§8's reference grammar** — a producer that is not a transitive predecessor,
  a self-reference, a reference to no declared node, and a reference to a field
  the producer does not declare are four separate refusals
  (`validateReferences`, `:586-607`).
- **§10's four shapes** — stated per row beside the table above.
- **§9.1's conjunction** — the compiler derives a `tool` node's effective
  `read_only` from the spec and the registered tool together
  (`effectiveReadOnly`, `:235-244`), and
  `TestWriterToolNodesNeedBothReadOnlyFlags` covers the masquerade in
  `parallel` and `pipeline` and its admission in a total-order `sequence`.
- **§7.2's tool refusals** — including the `Skipped` case, which is
  fail-closed and needs a stub that is both uncompilable and MCP-identified to
  reach at all.
- **`PlanItem`** — the plan publishes the exported view and never
  `fleetTaskItem` (§7.2).
- **The runner's own shape, in-package** — `RunOptions` with `BudgetCheck` nil
  reads as unbounded (`budgetCrossed`, `orchestrate_run.go:547-552`), a nil
  `RunAgent`/`RunTool` refuses the nodes that need it, and the four id-level
  operators are stated over `reduceInput`/`OrchestrationValue`
  (`applyReduce`, `:401-422`), each pinned in `orchestrate_run_test.go`.
- **§17 A4's in-package half** — `TestOrchestrationVerdictFollowsTheReceipts`
  runs a plan with an inner mutation and then one with a verification step, and
  asserts the first carries non-empty `completion.Gaps` and a verdict that is
  not `done` while the second's is `done`. So the receipt-honesty rule is
  already exercised end to end in-package, and the same assertion is taken at
  the `boot.Build` boundary by
  `TestEffectOrchestrateUnverifiedWriteCannotReadDone`
  (`internal/boot/effect_orchestrate_receipt_test.go`), which reads the host's
  own completion audit after a real writing node ran.
- **§17 A5's in-package half** — `TestOrchestrationNeverExceedsTheSessionSlots`
  runs a `parallel` plan over a deliberately narrow scheduler and asserts the
  slot counts, so the plan cannot raise the concurrency it declares.

**Still follow-up**, and the boundaries every one of them crosses: whether P1
should survive with reduction cancelled, which §13's own reconsideration answers
in the affirmative on structural grounds and which a later revision is free to
reverse at the cost of one tool; the
`tool`/`reduce` node kinds, cross-node `FieldKind` propagation and the
aggregates over declared fields, which are P3's and are gated on P0's verdict;
P0's own recorder and the verdict it exists to produce, which needs recorded
sessions this revision does not have; nothing, on the evidence. P3 is
cancelled by P0 (§13 P0), which also retires §19 Q5: an arm for a reduction path
that is not being built has nothing to own. The team-session intersection an earlier draft listed
here is **resolved**, not deferred: §9.5 lifted the gate, so a team session runs
`agent` plans like any other, and §19 Q4 is answered by removing the asymmetry
rather than extending it to `fleet`.

Three items left this list rather than moving down it. The `outputs`/`reduce`
additions to `fleet`'s schema are **withdrawn**, for the reasons stated under P1
above. The `fleet`-contrast shape §17 A5 names is **landed**
(`orchestrate_fleet_contrast_test.go`), which completes A1–A7. And the re-check
of `docs/ORCHESTRATION.md` and its zh-CN pair against the code is **done**: the
drift it was expected to catch was real but small — a Tests list three files
short, and a team-session section that stated the `agent` gate without noticing
the gate's intersection with P2's kind gate — and both are corrected in both
documents. The numeric claims those guides make were checked against the code
and hold.

## 14. Alternatives

The six entries below are numbered **AL1–AL6** to keep them apart from §17's
acceptance matrix, which also uses `A1`–`A7`. The two numbering spaces collided
in the previous revision — the alternatives' `A6` and the matrix's `A6` name
different things — and a document that cites itself has to keep them distinct.

**AL1 — general script runtime (rejected; re-open only via §6).** The gap it
closes is genuine (§3), but it requires a sandbox, a runtime, and a tool bridge
that preserves receipts. Condition 3 of §6 is the gate.

**AL2 — new `internal/orchestrate` package (rejected).** Duplicates the fleet
assembly, and separates a graph engine from the test files that will reach for
it — the `[setup failed]` shape `REASONIX.md` warns about. *Not* rejected on
layering: `tools/repolint/layers.go:67-77` gates a declared `leaves` set that a
new package would not be in. See D3.

**AL3 — a second scheduler inside the orchestrator (rejected).** Would race the
session scheduler over write claims and break the ablation attribution that
`internal/boot/effect_test.go:134-154` pins.

**AL4 — put the plan in a `task` spec / a profile (rejected).** Directly the move
that `fleet_graph.go:10-13` and `profile_spec.go:37-40` exist to block.

**AL5 — ship nothing (rejected, conditionally).** `fleet` already covers the
graph-and-dispatch half of Dynamics; what is missing is the *bounded reduction*
half, and that is the half the context budget actually needs. But this is the
alternative P0 can promote: if measurement finds (b), shipping nothing is the
correct outcome, because the missing half is the only half worth building
(§13 P0).

**AL6 — make orchestration a projection of `internal/plancontract` (deferred,
not rejected).** The alternative the previous draft never evaluated, and the
strongest one, because the repo already has a plan-as-data layer and this design
adds a fourth. `plancontract` carries what §7.1 and §7.2 rebuild:
`Step{ID, ParentID, Title, DependsOn, Acceptance, Verification, Risks}`
(`internal/plancontract/plan.go:37-47`); `Normalize` repairing what is
repairable and `Validate` rejecting what is not, "so code downstream of an
accepted Plan never re-checks its shape"
(`internal/plancontract/doc.go:19-22`); revisions that `Diff`
pairs by id, never by position or title; and `Criterion.ID` stamped host-side
"so it can key an evidence requirement downstream"
(`internal/plancontract/plan.go:49-53`) — the same
`completion.Build` chain §17 A4 depends on.

Two consequences would be real wins. Revisions are a better answer to rebinding
than §9.2's static serialisation: a discovered graph is a new revision of an
accepted plan, diffable against its predecessor, so disjointness can be
re-proved at `ValidateOrchestration` time and §19's question about relaxing §9.2 closes
itself instead of waiting. And identity stops being model-authored, which is the
divergence §7.1 has to justify today.

Why it is deferred rather than adopted. `plancontract`'s only projection today
is `ProjectTodos`, which renders the plan as "the serial task list a host seeds"
(`internal/plancontract/project.go:5-10`) — its `DependsOn` edges feed ordering
and normalisation, never a scheduler. Adoption therefore means writing a second
projection (plan → `fleetPlan`) *and* making one type serve as both a
user-approved unit and a dispatch unit, which couples the approval gate to the
scheduler. `Mode`, `Caps` and `Outputs` have no home in a `Step` either. That is
a larger change than P1, so v1 keeps the two apart and this ADR records the
debt: if a second plan representation turns out to cost what it looks like it
will, AL6 is the consolidation, and it is cheaper before P3 ships than after.

## 15. Risks

| # | Risk | Severity | Mitigation |
| --- | --- | --- | --- |
| R1 | Receipt honesty: inner calls escape evidence, `completion.Build` reports `done` for unverified work | **Critical** | §17 A4; runner never calls tools except through the host seam |
| R2 | Workflow-language creep — the boundary both in-repo warnings name | High | Closed modes (§10); the host tool's decoder rejects unknown fields (`dec.DisallowUnknownFields`, §7.4); a fifth mode needs an ADR |
| R3 | Prompt-prefix regression (cache-first invariant) | High | Default non-provider-visible; A1 asserts byte-equality of the tool-schema name set **and** that the tool reaches the model only through the catalog/route surface, never the provider-visible contract snapshot (§7.5) |
| R4 | Budget escape: unbounded plan cost | High | Spend-free declarative refusal in `ValidateOrchestration` (tokens axis) + per-node re-check through `runBudget.exceeded` at dispatch (§7.3); no model-side budget arithmetic, no new accessor |
| R5 | Second scheduler / write-claim bypass | High | Reuse `scheduler.AcquireRequest`; A5 cross-runs plans and fleet |
| R6 | Ablation attribution becomes untrue | Medium | A2 landed as `TestEffectOrchestrateIsAbsentUnderSubagentAblation` (`internal/boot/effect_orchestrate_ablation_test.go`), which asserts absence of the registration rather than of a schema: the pre-existing `internal/boot/effect_test.go:148-154` block pins schema absence, which is true in both arms |
| R7 | Team double-orchestration: leader already dispatches, claims files, and receives patches | **Withdrawn** | This row's own hedge turned out to be the answer. It recorded the mitigation as **unproven** and named the disjunction exactly — "either the risk is real and `fleet` already carries it, or the restriction is one-directional friction" — and it is the second. A member is its own `boot.Build` at subagent depth 0, not a nested delegate, so an `agent` node there delegates once; the same session already reaches `fleet`'s identical fan-out; and `max_subagent_depth` bounds recursion everywhere alike. The gate is lifted (§9.5) and §19 Q4 closes by removing the asymmetry. What the leader does with members is a separate axis from what any session does with sub-agents, which is the conflation this row rested on |
| R8 | New materialisation boundary leaks secrets into the transcript | Medium | Reduction output passes the existing redaction boundary before it becomes a tool result |
| R9 | Model writes a valid-but-wasteful graph | Low | Per-node events + plan hash make waste visible; P0's recorder measures it (§13 P0) |
| R10 | `Caps` drifts into a per-call grant | Low | `Caps` is a refusal input only; A3 asserts refusal, never expansion |

## 16. Exit criteria

This feature is done when all of the following hold:

- A1–A7 (§17) pass in CI, with A1, A2, A4, A5 and A6 asserted at the
  `boot.Build` boundary in the `internal/boot/effect_test.go` style: A1 and A6
  in `internal/boot/effect_orchestrate_surface_test.go`, A2 in
  `internal/boot/effect_orchestrate_ablation_test.go`, A4 in
  `internal/boot/effect_orchestrate_receipt_test.go`, and A5 in
  `internal/boot/effect_orchestrate_slots_test.go`. A5's boundary case pins one
  assembled session's slot ceiling, and the `fleet`-contrast shape the §17 row
  also names — a plan running beside a concurrent `fleet` over overlapping
  `write_paths` — is **landed** in
  `internal/agent/orchestrate_fleet_contrast_test.go`. **A1–A7 now hold in
  full.**
- `docs/ORCHESTRATION.md` and `docs/ORCHESTRATION.zh-CN.md` describe the shipped
  API — the spec schema, the four modes, the refusal vocabulary, the current
  limits, and the fact that `workflow:orchestrate` resolves the moment the tool
  registers. Both documents have now been re-checked against the code rather
  than against this design note: their node bounds (1–64), their `caps` ceilings
  (64/32/3) and their claim that `fanout_reduce` is absent from the tool's
  `mode` enum all hold, and the drift that was found — a Tests list missing
  three files, and the team-session statement §9.5 corrects below — is fixed in
  both.
  **The shared Skill under `team/skills/shared/` is not added, by the project
  lead's ruling, and this criterion is closed that way rather than ticked.** The
  argument an earlier draft of this bullet made for withholding it no longer
  applies and should not be cited: it rested on no team session being able to
  run a plan, which §9.5 reversed. A team session now runs `agent` plans exactly
  as a solo session does, so a shared Skill would document something its readers
  *can* invoke. What remains true is its cost — every skill in that directory
  enters *every* member's system prompt against a 32 KiB budget
  (`teamRoleSkillBudget`, `internal/cli/team_role_skill.go`) — and the ruling
  weighed that against `docs/ORCHESTRATION.md` already carrying the schema. If a
  later revision wants it, the cost and the budget are the trade to restate, not
  the availability.
- The PR carries `Cache-impact: none` (or `low` with a guard test) and
  `Documentation-impact: updated`.
- `go run ./tools/repolint` passes with new files at budget 0 — no baseline
  widening.
- `ablation.Subagent` is unchanged and the `fleet` behaviour tests still pass.
  The count is not pinned here, because it is not a constant: it is whatever
  `go test ./internal/agent/ -run 'Fleet'` reports, and it lives in
  `fleet_test.go` and `fleet_graph_test.go` plus one lease regression elsewhere
  in the package. What must hold is the claim, not the number — and the claim is
  now **asserted** rather than argued, by
  `TestExplicitIdPlanCompilesToTodaysFleetPlan`
  (`internal/agent/orchestrate_fleet_equivalence_test.go`): a spec declaring
  **explicit ids** and neither `outputs` nor a `reduce` node compiles to exactly
  the `fleetPlan` the same work expressed directly to `fleet` builds. The fleet
  side of that comparison is hand-authored rather than routed back through
  `compileItems`, which would have compared the compiler with itself. The claim
  no longer rests on P1 "extending fleet's schema additively", since that half is
  withdrawn (§13 P1): it holds because `Compile` builds fleet's own graph, which
  is the stronger reason.
- Either P3 shipped, or P0 measured at the end of P2 and the document records
  that outcome with the data. **Satisfied by cancellation.** P0 ran over 43
  recorded transcripts and reported (a) dominant but only 51.2% of it absorbable
  against an 80% gate; the verdict held at every sample filter, and §13 P0
  carries the table, the interpretation and the sample-size limitation. A
  cancelled P3 is a successful outcome, not a failure (§13 P0). The harness is
  `tools/contextgrowth/`, and re-measuring is one command.

## 17. Machine-checkable acceptance matrix

| ID | Claim | Check | Pass |
| --- | --- | --- | --- |
| A1 | `orchestrate` adds nothing to the provider-visible prefix by default | boot-boundary effect test **that asserts only after `boot.Build` has returned**, against the assembled registry: the tool-schema name set equals the pinned `UnifiedProviderToolNames()` set (`internal/boot/agent_preset.go:87`); `reg.ProviderVisible("orchestrate")` is false; `reg.Get("orchestrate")` still resolves so `tool:orchestrate` dispatches; the tool appears in `AllContractEntries` but never in the provider-visible `ContractEntries` snapshot (`internal/boot/boot.go:1620`) | **Landed** as `TestEffectOrchestrateStaysOffTheProviderSurfaceByDefault` (`internal/boot/effect_orchestrate_surface_test.go`), which asserts through `ctrl.ToolContractEntries()` / `AllToolContractEntries()` against the pinned `UnifiedProviderToolNames()` set. Byte-equal name set; `orchestrate` absent from it and from the provider-visible contract; dispatch succeeds. **The assertion point is part of the claim**: `ProviderVisible` returns `true` unconditionally while `providerVisible` is nil (`internal/tool/tool.go:364-367`) and only `applyUnifiedProviderToolSurface` (`internal/boot/tool_surface.go:7`) narrows it, so the same assertion taken at registration time is vacuously true |
| A2 | Ablation is honoured | **Landed** in a file of its own, `internal/boot/effect_orchestrate_ablation_test.go`, as `TestEffectOrchestrateIsAbsentUnderSubagentAblation`. The existing block at `internal/boot/effect_test.go:148-154` could not be extended into this: it pins schema absence rather than dispatch, and the top level never carries `task`, so it holds in both arms (§9.3) | `orchestrate` is not registered at all under `ablation.New(ablation.Subagent)`, so no dispatch path — `tool:`, `workflow:` or `task:` — can reach it |
| A3 | `ValidateOrchestration` refuses bad plans with zero side effects | Unit tests in `orchestrate_compile_test.go` over: duplicate id, empty id, **padded id** (`" a"` beside `"a"`, §7.1 rule 2), unknown `needs`, self-edge, cycle, writer outside a `sequence` position, `reduce` naming a node with no declared `Outputs`, fan-out over `Caps`, declared budget over `opts.Budget`, `tool` node naming an unregistered tool, `tool` node naming an MCP tool, `tool` node whose `Args` fail or come back `Skipped`, and a declared `outputs[].kind` outside the closed `scalar`/`list`/`ref` vocabulary (an omitted kind stays legal — the tool schema requires only `name`). §8's reference grammar is the same kind of case and lands in the same table: a reference to no declared node, a self-reference, a forward reference (a producer that is not a transitive predecessor), and a reference to a field the producer does not declare. Decode-level cases — **unknown field**, wrong version — are **landed** in `internal/agent/orchestrate_tool_test.go` (168 lines), where the tool receives raw bytes, rather than in the compiler's table, which sees an already-parsed struct: the strict decode is `dec.DisallowUnknownFields` (`internal/agent/orchestrate_tool.go:119-123`). The same file covers the wrong version and the kinds the tool does not run, both refused before a node is dispatched | Every case returns an error; no tool executed, no file written, no sub-agent started |
| A4 | Receipt honesty | Plan with an inner mutation and no verification; run through the real assembly; call `completion.Build` | Non-empty `Gaps`; verdict `partial`, not `done`. **Both halves landed.** In-package: `TestOrchestrationVerdictFollowsTheReceipts` (`internal/agent/orchestrate_run_test.go`) over a real `evidence.Ledger`, asserting both directions. At the boundary: `TestEffectOrchestrateUnverifiedWriteCannotReadDone` (`internal/boot/effect_orchestrate_receipt_test.go`) drives one headless turn whose model reaches the tool through `use_capability → tool:orchestrate`, has the node actually write a file, and asserts the **turn's own** receipt carries the unverified-change gap and does not read `done` — so the verdict is read where the user reads it, over the receipts the nodes left, not out of a test-supplied `RunOptions` |
| A5 | Scheduling consistency | Concurrent plan + `fleet` run over overlapping `write_paths`, in the shape of `internal/agent/fleet_graph_test.go:104` (`TestFleetConcurrentDirectoryClaimsPassPreflight`). **The contrast is `fleetGraph.validateConcurrentWriteClaims` (`internal/agent/fleet_graph.go:153-172`), not the public all-pairs validator**: the fleet preflight skips a pair that the plan's own ordering already serialises (`p.ordered(i,j)`), while `ValidateNonOverlappingWriteClaims` (`write_claims.go:229`) compares all pairs and is blind to edge order, so citing the latter would assert the wrong rule | No write conflict; scheduler slot counts never exceed `DefaultMaxSubagentConcurrency = 6` / `MaxSubagentConcurrencyLimit = 32`, and concurrent writers never exceed `DefaultMaxParallelWriters = 3` (`internal/agent/write_claims.go:12-21`). **In-package half landed** as `TestOrchestrationNeverExceedsTheSessionSlots` (`internal/agent/orchestrate_run_test.go`), which asserts against a `NewSubagentScheduler(6, 3)` that `scheduler.Limits()` is unchanged and peak concurrency stays within it. **The `boot` half landed** as `TestEffectOrchestratePlanStaysInsideTheSessionSlots` (`internal/boot/effect_orchestrate_slots_test.go`): one assembled session, a `parallel` plan of eight `agent` nodes reached through `use_capability`, and the measured peak concurrency stays inside the session's own 6. **The `fleet`-contrast shape has now landed too**, as `TestOrchestrationAndFleetShareTheSessionWriteClaims` (`internal/agent/orchestrate_fleet_contrast_test.go`): one session scheduler, a plan whose writer and a `fleet` whose writer both claim the same directory, with five writers and ten calls asked for against a session granting three and six — so both ceilings are pressed rather than nominal. Stating the row's rule took care, and the row's own warning applies to itself: a **directory-only** claim reserves nothing (`liveClaim.reservation`, `internal/agent/claim_live.go`), so two overlapping directory writers legitimately run at once and the exclusion arrives at `Realize`, where two live writers naming the same file are refused. Asserting that overlapping directory writers serialise would have pinned the wrong rule; `TestSharedSchedulerRefusesTheSameRealizedFile` pins the right one beside it |
| A6 | Prefix stability after opting in | Two consecutive requests with the tool provider-visible, where "opt in" is **the existing host-tool channel and nothing new**: the tool is passed in `boot.Options.ExtraTools`, which `applyUnifiedProviderToolSurface` (`internal/boot/tool_surface.go:17-20`) admits when `reg.ProviderVisible(name)` already holds. No config key, no new boot field, no new registry method | **Landed** as `TestEffectOrchestrateOptInKeepsThePrefixStable` (`internal/boot/effect_orchestrate_surface_test.go`): the tool reaches the provider request, the tool-schema name set is identical across two builds, and the system prefix — the cache-stable part — is byte-identical. The carrier is the same one `TestEffectExtraToolsReachProviderAndKeepThePrefixStable` (`internal/boot/effect_extra_tools_test.go:84`) pins for any host tool |
| A7 | Agent nodes are a host capability, not a team restriction | The `AllowAgentNodes` carrier still refuses when a host binds it false, and a member session reaches the baseline behaviour when no host does (§9.5) | **Restated, both halves landed.** The row used to read "Team isolation" and assert that a team session refuses `agent` nodes; §9.5 records why that reversed. The **mechanism** half is `TestAgentNodesAreRefusedWhenTheHostWithholdsThem` (`internal/agent/orchestrate_compile_test.go`): with `ValidateOptions{AllowAgentNodes: false}` the refusal comes from the validator and not the dispatcher, no node is dispatched, and a read-only `tool` plan is still admitted. The **parity** half is `TestEffectOrchestrateRunsAgentNodesInAMemberSession` (`internal/boot/effect_orchestrate_team_test.go`): a `boot.Build` with `Options.TeamRole: "member"` whose model asks for a writing plan through `use_capability → tool:orchestrate`, the node runs, and the file is on disk. What that test now guards is the team layer not silently losing a baseline capability — the direction the old row had backwards |

A1, A2, A4 and A6 are boundary tests and are the ones that must not be
weakened. A3 is the one that will grow. A1, A2, A4, A5 and A6 are landed at the
`boot` boundary, and A5's `fleet`-contrast shape is landed in-package. A7 is
landed at both halves in its restated form (§9.5).
**A1–A7 are now asserted in full**; what remains open is not an acceptance row
but a phase decision (§13 P0/P3) and the team gate §9.5 records below.

## 18. Compatibility summary

| Subsystem | Verdict | Basis |
| --- | --- | --- |
| Agent / subagents | Compatible by construction: an `agent` node compiles to a `ProfileExecSpec` and runs through `TaskTool.RunProfileSpec`, the same entry point `task`/`fleet` use | `internal/agent/profile_spec.go:59`, `internal/agent/task.go:700`, `scheduler.go` |
| Delegation boundary | Must not put per-call values in a profile | `internal/agent/profile_boundary_test.go`, `REASONIX.md` |
| Plan contract | **Not reused in v1, deliberately.** It is the existing plan-as-data layer, with host-stamped ids and criterion ids that key evidence; this design adds a fourth plan representation and owes the debt | §14 AL6; `internal/plancontract/doc.go:1-22`, `internal/plancontract/plan.go:37-53` |
| Team | **Parity with the baseline.** Every session binds `AllowAgentNodes: true`, a member included, because a member is its own boot at subagent depth 0 rather than a nested delegate, already reaches `fleet`'s identical fan-out, and is bounded by the same `max_subagent_depth`. The earlier `false` binding combined with P2's kind gate to admit nothing at all. The carrier stays for a host that wants to withhold agent nodes | §9.5; §19 Q4; `internal/cli/team_backend_build.go:404`, `:430`; `internal/boot/boot.go:1272` |
| Skill | The spec is data, not instructions; it must not enter the Skill loader. The shared Skill documents the schema only | `internal/skill/role_scope.go` |
| Tools | Same dispatch path, same `ReadOnly`/`PlanModeSafe`/`ContextualTool`/`EffectHint`/sandbox/fingerprint re-check | `internal/tool/tool.go` |
| Evidence / completion | Unchanged and load-bearing; a deliverable is not a receipt and never substitutes for one | `internal/completion/report.go`, `internal/evidence/` |
| Jobs | Reused as-is: a background plan run gets a job id and the existing `wait`/job-log collection, so crash recovery needs no new code | `internal/jobs/artifacts.go` |
| Checkpoint | Reused as-is, and **reached through the runner's own seam** rather than a new one: `RunOptions.Checkpoint` fires once per node (§7.3), so plan progress rides the existing transaction + atomic-JSON store and an interrupted plan verifies intact steps instead of re-running them. P1 calls the seam with nothing bound to it | `internal/checkpoint/` (`transaction.go`, `atomic_json.go`); `agent.RunOptions`, `internal/agent/orchestrate_run.go:118-130` |
| Team deliverable store | Landed, need not be depended on. Usable as an optional cross-process outlet: a node publishes a document and passes the **id** (a scalar), so §2.4 holds | §11; `internal/team/deliverable.go`, `internal/cli/team_deliverable_tools.go`, `team/skills/shared/deliverable/SKILL.md` |
| Prompt cache | Untouched by default, and the opt-in is the existing host-tool channel (`boot.Options.ExtraTools`) rather than a config key — §19 Q1 | `internal/boot/agent_preset.go:87-94`, `internal/boot/tool_surface.go:7`, `internal/boot/effect_orchestrate_surface_test.go` |

## 19. Open questions

1. **Closed.** The provider-visible opt-in is neither a config key nor a new
   session flag: it reuses `boot.Options.ExtraTools`, the host-tool channel that
   already exists and is already provider-visible by construction
   (`internal/boot/tool_surface.go:17-20`), so the tool is registered and
   dispatchable by default and becomes provider-visible only if a host passes it
   in that list. A config key would have made the prefix depend on
   `reasonix.toml` — a field a user can flip mid-session without a rebuild — and
   that is exactly the cache-first invariant `REASONIX.md` forbids. A *new*
   session flag would have been a second carrier for something `ExtraTools`
   already does, and would have moved the ordinary session's tool surface the
   moment it acquired a default. §17 A6 pins the outcome either way: two
   consecutive requests carry a byte-identical prefix.
2. Does the plan hash need to enter the session DAG for replay, or is the event
   stream sufficient? Decide in P1 with the replay harness in hand.
3. When, if ever, to relax §9.2 to fleet's disjoint-paths rule. Gate it on the
   rebinding path proving disjointness at `Validate` time — or adopt §14 AL6,
   which closes this question instead of answering it.
4. **Reopened, and answered the other way: the gate is lifted rather than
   extended to `fleet`.** The previous answer kept A7 and left `fleet` alone,
   reasoning that `fleet` fans out one flat batch and returns while
   `orchestrate` re-enters the delegation path node by node, so a team session's
   `agent` nodes were where double-orchestration would compound. The second half
   of that sentence is where it went wrong, and §9.5 records the evidence: a
   member is its own `boot.Build` at subagent depth 0, not a nested delegate, so
   its `agent` nodes delegate exactly once — there is no second level to
   compound. The asymmetry the previous answer called deliberate was therefore
   real but backwards: it withheld from `orchestrate` a fan-out the same session
   already had through `fleet`, over the same `RunProfileSpec` path, under the
   same `max_subagent_depth`.
   So the question closes by removing the difference instead of propagating it.
   `fleet` keeps no gate, `orchestrate` loses the one it had, and a team session
   reaches both exactly as a solo session does. The note that `NewFleetTool` is
   constructed in the same `addTaskTool` closure with `opts.TeamRole` in scope
   still holds and is now only of historical interest: a future revision that
   wants to gate delegation by role has the carrier for either tool, and what it
   would owe is evidence that the role is the right axis — which is the claim
   this revision found to be false.
5. **Retired without an answer, because P3 is cancelled.** The question was
   which arm owns the reduction path once P3 lands (§7.4) — its own
   `ablation.Module`, or a per-node gate inside the subagent arm. No reduction
   path is being built, so the subagent arm keeps measuring one mechanism and
   §7.4's attribution cost never arrives. If §6 reopens P3, this question
   reopens with it and is still owed before the kind gate is deleted.

The previous draft's fourth question — whether `gate` deserves its own node
kind — is closed by its own answer, and the kind is deleted (§0.1).
