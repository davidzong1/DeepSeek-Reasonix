# Orchestration: declarative plans over the session's own dispatch

Status: **shipped and closed at phase 2** — spec, compiler, runner and the host
tool ship. `agent` nodes run; `tool` and `reduce` nodes are compiled and run by
the library but are refused at the host tool's boundary (see
[Current limits](#current-limits)). Phase 3 would have lifted that boundary and
is **cancelled**: the pre-registered measurement behind it found that only ~23%
of this workload's tool-result growth is shaped the way a closed operator could
absorb, against an 80% gate. The measurement, its data and its limits are in the
design note; the harness is `tools/contextgrowth/`. The design and its rationale live in
[`agent_architecture_decision.md`](agent_architecture_decision.md); this
document describes the shipped API.

**What this is not.** It is not a context-saving feature. The measurement behind
that claim ran and the answer was no: only ~23% of this workload's tool-result
growth is shaped the way a closed operator could absorb, against an 80% gate, so
phase 3 was cancelled and `tool`/`reduce` nodes stay unreachable from this tool.
Where `orchestrate` earns its keep is narrower and worth stating plainly — it is
a plan format with **in-band permission claims** (`read_only` as a claim the host
conjoins with its own answer, not a grant) and **id-level handoffs**
(`$nodes.<id>.<field>` resolves to a receipt id, never to a body). `fleet`
expresses the same graph less richly and can express neither. If you want fleet
with more ergonomics, this is it; if you came for the token savings, they are not
here.

The feature adds no runtime. A plan is data, and every node it runs is a call
the session would have made itself: `agent` nodes go through `task`'s
`RunProfileSpec`, and the scheduler, write claims, transcripts, checkpoint
store and evidence ledger are the session's existing ones rather than copies.

## The spec

`orchestrate` takes one object with one required field, `spec`. The decoder
rejects unknown keys (`DisallowUnknownFields`), so a typo is an error rather
than a silently ignored declaration.

```json
{
  "spec": {
    "version": 1,
    "mode": "sequence",
    "nodes": [
      {"id": "survey", "kind": "agent", "prompt": "List the call sites of parseNodeRefs.",
       "outputs": [{"name": "sites", "kind": "list"}], "read_only": true},
      {"id": "patch", "kind": "agent", "needs": ["survey"],
       "prompt": "Apply the rename to the sites $nodes.survey.sites named.",
       "write_paths": ["internal/agent"]}
    ]
  }
}
```

| Field | Meaning |
| --- | --- |
| `version` | Only `1` is accepted. |
| `mode` | `sequence`, `parallel`, `pipeline` — see [Modes](#modes). |
| `nodes[]` | The plan, in declaration order. One to 64 nodes. |
| `reduce` | Optional `{operator}`, for `fanout_reduce`. |
| `caps` | Optional request to **narrow** the session's ceilings. It can never widen one. |

A node carries `id`, `kind`, an optional `needs` list, an optional `profile`,
`prompt`, `outputs`, `max_tokens`, `write_paths` and `read_only`. A `tool` node
additionally names a `tool` and its `args`.

There is no field for a condition, a loop, a computed edge, or a script. That
absence is the anti-drift mechanism: a plan that needs one of them is a plan
the format cannot express, rather than a plan that half-expresses it.

### References

A node addresses an earlier node's result as `$nodes.<id>.<field>`, appearing
anywhere inside that node's JSON arguments. Three rules hold:

- `<id>` must name a declared node that is a **transitive predecessor** — a
  forward reference and a self-reference are both refused.
- `<field>` must be named in that producer's own `outputs`. An undeclared field
  is a refusal, never an empty string.
- What crosses the boundary is the **id**, never the body. A producer's text
  stays host-side; a downstream node receives a handle it can cite. A producer
  that returns no reference leaves the consumer a named refusal rather than a
  literal `$nodes.a.out` handed to a tool as if it were an id.

## Modes

| Mode | Shape | Writers |
| --- | --- | --- |
| `sequence` | A total order: every pair of nodes is ordered by the edges | Allowed, because the order is provably serial |
| `parallel` | No node declares `needs` | Refused |
| `pipeline` | A chain: at most one `needs` per node | Refused |
| `fanout_reduce` | Producers, then one `reduce` node over them | Refused |

A `read_only: true` declaration is a **claim, not a grant**. The effective
permission is the conjunction of that claim and the session's own answer, so a
node cannot widen what the session allows. A writer outside a `sequence` is
refused before anything starts, because only there do the edges make the order
total.

## Running a plan

```go
import "reasonix/internal/agent"

plan, err := agent.Compile(spec, agent.ValidateOptions{
    AllowAgentNodes: true,          // bound at assembly, never read from the spec
    Budget:          budget,        // declared ceiling; Validate reads no spend
    Tools:           registry,      // consulted read-only, for tool nodes
})
if err != nil {
    return err                     // every refusal happens here, before dispatch
}
options := agent.NewRunOptions(taskTool, registry)
options.BudgetCheck = agent.BindBudgetCheck(a)     // this turn's spend predicate
options.Checkpoint  = func(r agent.OrchestrationNodeResult) { /* progress */ }
result, err := agent.RunOrchestration(ctx, plan, options)
```

- `Compile` is pure and calls `ValidateOrchestration` first, so there is no
  path to a `Plan` that skipped validation. `ValidateOrchestration` itself
  reads no disk, executes nothing and reads no spend.
- `Plan` is immutable and hashable. Every accessor returns a deep copy, the
  hash goes into the plan's events, and `Plan.Items()` publishes the **compiled**
  view — the effective `ReadOnly`, the edges the graph was built from — while
  `Plan.Nodes()` returns the spec as declared. Permission decisions come from
  `Items()`; reading `read_only` back from `Nodes()` would treat the claim as
  the verdict.
- `RunOptions` seams are optional. A nil `RunAgent`/`RunTool` refuses the nodes
  needing it, a nil `BudgetCheck` reads as unbounded, and a nil `Checkpoint`
  records nothing. Substituting a field adds policy to the session's own path,
  never a second mechanism.

### Budget

`ValidateOptions.Budget` is a **declared** ceiling: validation sums the nodes'
`max_tokens` and refuses a plan whose own numbers do not fit. It reads no
spend. Real spend is checked per dispatch through `RunOptions.BudgetCheck`,
which the assembly binds to the turn's own `taskBudgetLimit` +
`runBudget.exceeded` — the runner reads the predicate, never the arithmetic. A
cut stops the plan, files every unreached node as `skipped`, names the axis in
`OrchestrationResult.BudgetAxis`, and returns an error naming it too.

### Cancel, retry, skip

- **Cancellation** stops the plan; a dispatched node publishes `cancelled` and
  a node the graph never reached reads `skipped`. Nothing is left reading as
  `pending` when a run ends.
- **Retry** is exactly one replay, and only for a node that is side-effect-free.
  A node that already spent its attempt is refused, so no failure loops here.
- **Failure** is isolating: a node's dependents are skipped and the rest of the
  plan is unaffected.

### Checkpoint and progress

`Checkpoint` is called **once per node**, when its terminal report is filed —
after publication, and again during the terminal reconcile for a node the graph
settled without dispatching. The host tool renders each call as the existing
bounded `ToolResultPreview` event (`orchestrate/<node-id>`) rather than
inventing an event kind. Nothing is persisted by the runner itself: transcripts,
the checkpoint store and `read_subagent_result` are the session's.

Every node's report also carries bounds and proof, and both are visible in the
tool's own result:

| Field | Meaning |
| --- | --- |
| `StartedAt` / `EndedAt` | Unix-millisecond bounds, set when the node is dispatched and when its report is filed. A node the graph settled without running gets both at settlement, so no node reads as one that never existed. |
| `DurationMs()` | `EndedAt - StartedAt`, or 0 when the node was never dispatched. |
| `ReceiptCount` | How many receipts **the turn's ledger** recorded while this node was in flight. It is the ledger's count, not the runner's bookkeeping, so a node that completed and left no proof reports `0`. |
| `Receipts` | The ids this node's work published, which is what a downstream reference resolves against. |

The tool's result is one bounded line per node —
`- w [agent] completed receipts=1 12ms` — and the progress preview carries the
same numbers on the event's own `StartedAt`/`EndedAt`/`DurationMs` fields. No
node body reaches either channel.

Reading a receipt gap at the boundary: the assembled controller publishes the
host's completion report through its audit sink
(`event.CompletionReportAudit` — verdict, gap count and gap kinds, content-free).
That is the seam a caller outside `internal/agent` reads the A4 claim from; the
in-package half reads `OrchestrationResult.Completion` directly.

### Receipts and evidence

Node work leaves ordinary receipts, and a sub-agent's receipts merge into the
caller's ledger through the same path `task` uses. `OrchestrationResult.Completion`
is `completion.Build` over that ledger, so a plan that changed the workspace and
never verified it cannot read as `done`. The verdict is taken from receipts, not
from the runner's bookkeeping — a plan reporting "all nodes completed" says
nothing about whether the work was proven.

## Reaching the tool

`orchestrate` is registered beside `task`, `parallel_tasks`, `fleet` and
`read_subagent_result`, and is **not** in the provider-visible tool surface.
The provider-visible prefix stays byte-identical, which is what keeps the
prefix cache warm.

It is reached through the existing capability channel:

```
use_capability {action: "call", capability_id: "tool:orchestrate", arguments: {spec: {…}}}
```

Two consequences worth knowing:

- **`workflow:orchestrate` becomes addressable the moment the tool registers.**
  `workflow:` is an existing capability prefix resolving to a registry tool, so
  this is free rather than added. It is also the one place the feature's surface
  collides with the name the design says it must not become: `orchestrate` is a
  delegation plan, not a workflow engine, and a request framed as a workflow
  should be answered with these four modes or refused — not extended until the
  word fits.
- **Opting it into the provider surface reuses the existing host-tool channel**
  (`boot.Options.ExtraTools`, or whatever a host registers). There is no
  separate config key, and opting in is a deliberate act: the tool schema and
  the prefix then change, and the prefix-stability guard covers the case.

Under `ablation.Subagent` the tool is **not registered at all**, so no dispatch
path — `tool:`, `workflow:` or `task:` — can reach it. That is structural rather
than a call-time gate.

## Team sessions

A team session runs plans exactly as any other session does, a **member**
included. `AllowAgentNodes` is bound `true` at assembly for every role, so an
`agent` node is admitted in a member session, a leader session and a solo
session alike.

This is a reversal of an earlier restriction, and worth stating because the old
behaviour was worse than a narrow capability — it was none at all. A team
session used to bind `AllowAgentNodes: false`, which combined with the host
tool's kind gate (only `agent` nodes are admitted) to leave an **empty** set: an
`agent` spec was refused by the validator, a `tool` or `reduce` spec by the kind
gate, so every spec was refused. It covered the leader too, because
`opts.TeamRole` is `"leader"` or `"member"` and never the empty string the gate
compared against.

Three facts settled the reversal:

- A member is its **own session**, assembled by its own `boot.Build`, whose root
  agent runs at subagent depth 0. It is not a nested sub-agent of the leader, so
  its plan delegates exactly once — the same as a solo session's.
- A member already reaches `task`, `parallel_tasks` and `fleet`, and `fleet`
  fans out sub-agents through the same path an `agent` node uses. The gate
  withheld one spelling of a capability the session already had.
- Recursive delegation is bounded by `max_subagent_depth` (default 2), enforced
  per dispatch in every session alike.

The mechanism is still there for a host that wants it: `AllowAgentNodes` remains
a constructor field bound at assembly and is never read from the spec, the tool
schema or the call arguments, so a plan cannot argue its way in. A host that
binds it `false` gets a validator refusal naming the session, and no node is
dispatched.

What a team session still cannot run is only what nothing can run: `tool` and
`reduce` nodes, refused by the kind gate in **every** session. That is parity,
not a team restriction.

## Current limits

- **`tool` and `reduce` nodes are refused by the host tool.** The compiler and
  runner support them (`fanout_reduce` with `select`/`map`/`filter`/`count`,
  which keep ids and never content), but `orchestrate`'s boundary admits only
  `agent` nodes and names the refusal. Typed `FieldKind` propagation across
  kinds and the wider operator set belonged to phase 3, which is **cancelled**,
  so this is the shipped shape rather than a staging post. A declared `kind` is
  still held to `scalar`/`list`/`ref`; what does not happen is propagation
  between a producer and the operator consuming it.
- **`tool` and `reduce` support is reserve, not rot.** The compiler and runner
  keep them and their tests keep passing, because they are the written record of
  what the four operators mean; the library API still runs them. They are
  unreachable from *this tool* on purpose. If a plan needs one, it needs a
  different phase decision — not a workaround.
- **`fanout_reduce` is not in the tool's `mode` enum**, so it is reachable only
  through the library API (`Compile` + `RunOrchestration`) today.
- **No team restriction.** A member session runs `agent` plans like a solo one;
  only the kind gate applies, and it applies everywhere (above).
- **`caps` narrows, never widens.** `max_nodes` is over `64`, `max_parallel`
  over `32`, `max_writers` over `3` — the session's own ceilings, refused when a
  request exceeds them.
- **Concurrency is the session's.** Plans acquire the session scheduler's slots
  (6 at once, 3 writers by default) and cannot raise them; a plan asking for
  more is refused at validation.
- **A plan is one call, not a durable job.** The runner holds no queue of its
  own, and a run that outlives the turn is cancelled with it.

## Validator refusals

Every one of these happens before anything is dispatched, and a rejected plan
leaves nothing behind — no tool executed, no file written, no sub-agent
started.

| Refusal | Example |
| --- | --- |
| Unknown field | Any key the schema does not describe |
| Wrong version | `"version": 2` |
| Bad id | `""`, a padded `" a"` beside `"a"`, an id outside `[A-Za-z0-9._-]{1,64}` |
| Bad graph | Duplicate id, unknown `needs`, self-edge, cycle |
| Unordered pair | Two `sequence` nodes with no path between them |
| Writer outside a sequence | A writing node in `parallel` or `pipeline` |
| Bad reference | `$nodes.ghost.out`, a self-reference, a forward reference, an undeclared field |
| `reduce` without fields | A `reduce` node over a producer that declares no `outputs` |
| Unknown output kind | An `outputs[].kind` outside `scalar`, `list`, `ref`. An omitted kind stays legal |
| Output with no name | An `outputs[]` entry whose `name` is missing or empty. The schema requires it, and an unnamed field is unaddressable |
| Over its own cap | `caps` above the session ceiling, or `max_tokens` over the declared budget |
| `tool` node that cannot run | An unregistered tool, an MCP-served tool, arguments that fail validation or `Skipped` |
| `agent` node where withheld | Any `agent` node when a host binds `AllowAgentNodes` false. No role binds it false today |

## Tests

- `internal/agent/orchestrate_compile_test.go` — validator and compiler cases.
- `internal/agent/orchestrate_run_test.go` — the runner: modes, references,
  budget cut, cancellation, failure, retry, skip, the per-node checkpoint, and
  the verdict-follows-receipts rule over real receipts.
- `internal/agent/orchestrate_tool_test.go` — the host tool's own boundary:
  strict decode, wrong version, refused node kinds, and the bounded result.
- `internal/boot/effect_orchestrate_surface_test.go` — A1 and A6: the tool is
  registered and dispatchable while the provider-visible surface stays pinned,
  and opting in keeps the prefix stable across consecutive requests.
- `internal/boot/effect_orchestrate_ablation_test.go` — A2: the subagent
  ablation removes the tool structurally.
- `internal/boot/effect_orchestrate_receipt_test.go` — A4: one headless turn
  whose model reaches the tool through `use_capability`, a node that really
  writes, and the host's completion audit refusing to call it done.
- `internal/boot/effect_orchestrate_slots_test.go` — A5: a wide parallel plan
  over the assembled session scheduler, whose measured peak stays inside 6.
- `internal/agent/orchestrate_fleet_equivalence_test.go` — P1's equivalence: an
  explicit-id plan with no `outputs` and no `reduce` node compiles to exactly the
  `fleetPlan` the same work expressed directly to `fleet` builds.
- `internal/agent/orchestrate_fleet_contrast_test.go` — A5's `fleet` contrast: a
  plan and a `fleet` run concurrently over overlapping `write_paths` on one
  scheduler, and the same-file exclusion that makes the overlap safe.
- `internal/boot/effect_orchestrate_team_test.go` — A7's parity half: a member
  session *runs* a writing plan through `use_capability`, and the file lands.
- `internal/agent/orchestrate_node_observability_test.go` — the per-node
  contract: timing bounds on the report and the event, and a receipt count taken
  from the turn's ledger.
