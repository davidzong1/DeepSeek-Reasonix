# knowledge_base (internal)

Team knowledge MVP: durable, searchable knowledge items per team, collected
asynchronously at the host turn tail. This directory is **additive only** — it
imports leaves (`internal/fileutil`, `internal/frontmatter`, `internal/retrieval`)
and nothing above; no host/frontend package may import back into it.

## Modules

| package | responsibility |
| --- | --- |
| `model` | Thought / SourceChunk / KnowledgeItem types, closed enums, validation, canonical + content/chunk hashes |
| `extract` | deterministic chunking, rule gate (`allow`/`deny`/`needs_llm`), pluggable budgeted LLM extractor |
| `quality` | live-vs-draft gate; L1 hash dedup; same-author supersede; cross-author conflict |
| `store` | file+frontmatter truth source under `<DataRoot>/<team-id>/items/*.md`; create-only `Put`, metadata `Transition` |
| `queue` | durable append-only `events.log` + fsynced `cursor`; at-least-once `Replay` |
| `index` | rebuildable lexical read model over live items; reuses `internal/retrieval` BM25 |
| `adapter` | `Host`/`Sink` seam the host implements (`var _ adapter.Adapter` assert) |
| `manager` | per-team facade: `New/Ingest/Query/Retire/ClearTeam/Backlog/Flush/Close` |

## Data layout

```
<DataRoot>/<team-id>/          # DataRoot default "team/knowledge_base"
├── items/<item-id>.md         # truth source: frontmatter + markdown
├── queue/events.log           # append-only, fsynced per write
├── queue/cursor               # last confirmed seq (atomic replace)
└── (index/ is in memory and rebuildable from items/)
<DataRoot>/.trash/<ts>-<team-id>   # ClearTeam destination
```

`team-id` must match `^[A-Za-z0-9_-]{1,64}$`; item/job ids are path-safe
segments only.

## Manager API

```go
m, err := manager.New(host, manager.Options{TeamID: "alpha", DataRoot: dir, LLM: provider})
jobID, err := m.Ingest(ctx, thoughts)   // returns after the event is fsynced
res, err  := m.Query(ctx, model.Query{Text: "单写队列", Limit: 10})
err        = m.Retire(ctx, []string{itemID}, model.ReasonNoLongerTrue)
err        = m.ClearTeam(ctx, "alpha", model.ScopeTeam) // team must == bound team
n, err     = m.Backlog(ctx)
err        = m.Flush(ctx)                // wait until the queue drains
err        = m.Close()
```

- `Query` degrades to a live store scan ordered by `UpdatedAt` (filters/limit
  intact) when the search index cannot tokenize or fails; reads never hard-fail
  on unindexable text.
- An optional SQLite/FTS read-model backend is deferred behind a quantified
  trigger: roughly 5k+ live items or measured directory-scan/BM25 latency (see
  `team/knowledge_base/KNOWLEDGE_BASE_DESIGN.md` §12 P3). Default stays the
  file+frontmatter truth source with the in-memory lexical index; Manager API
  is unchanged.
- `Query` has **no TeamID**: a Manager only ever addresses the team it was
  constructed with. `ClearTeam` with any other team fails closed.
- Ingest is durable-asynchronous: success means the event was appended and
  fsynced; item commit happens on the single queue consumer in the order
  *store write → index projection → cursor fsync*.
- Only `live` items are searchable. High-confidence items land live; suspect /
  low-confidence ones land `draft` and are stored but never indexed (no draft
  promotion in P0).

## Store invariants

- `Put` is **create-only**: same id + identical bytes = no-op; same id +
  different bytes = `ErrConflict`; never a silent overwrite. New versions are
  new ULID files that link the superseded id (`supersedes`/`superseded_by`).
- `Transition` changes metadata (status / version links) in place, preserving
  the body; it is the only mutation path after create.
- Content hashes are derived and cached; the cache can be rebuilt from items/.

## LLM / fail-closed

`extract.Extractor` is host-supplied. When quota disallows LLM, `needs_llm`
chunks are dropped (rule-only path). `extract.Budgeted` caps calls; provider
errors never produce partial items.

## Concurrency model

One Manager = one team = one single consumer. `Ingest`/`Retire`/`ClearTeam`
funnel through the durable queue; concurrent callers are safe. Readers query
the in-memory read model under RWMutex and never block the writer. Replay after
a crash is at-least-once and idempotent via create-only writes + L1 dedup.

> Note: `ClearTeam` moves the team dir to `.trash`. It is the single write
> linearization point: once accepted, concurrent `Ingest`/`Retire`/`ClearTeam`
> calls wait until the swap finishes and then append to the fresh queue, so no
> accepted event is ever lost into the trashed log. Only `ScopeTeam` is
> supported; any other scope fails closed.

## Test

```bash
go test ./internal/knowledge_base/...
go test -race ./internal/knowledge_base/...
go run ./tools/repolint
```
