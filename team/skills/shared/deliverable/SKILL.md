---
name: deliverable
description: Publish and read this team's deliverable documents. A member publishes under its own identity; every member and the leader read the whole team's documents by stable id.
---

# Team Deliverables

Work that the leader must read whole — a technical route, a design note, a
generated report, a long diff summary — does not belong in a chat message or in
a compressed context. Publish it as a deliverable: one document in the team's
own store, addressed by an id that survives the session that wrote it.

## Tools

| Tool | Who may call it | Effect |
| --- | --- | --- |
| `member_publish_deliverable` | member | writes one document as your own member id |
| `member_read_deliverable` | member and leader | reads one document by id |
| `member_list_deliverables` | member and leader | lists the team's documents |

- **Publish** takes `slug` (lowercase `[a-z0-9][a-z0-9._-]*`, at most 64
  characters) and `body` (at most 1 MiB). It returns the document's id.
- **Read** takes `id` and returns the stored body byte for byte.
- **List** takes no arguments and returns each id with its size and digest.

## Ids

An id looks like `<member>-<slug>-<sha256 first 12 hex>.md`: the owner, the slug
and the **first 12 hex characters** of the body's sha256, then `.md`. The digest
is truncated for a readable file name and the store re-checks those 12
characters on every read, so a document edited outside the tools fails instead
of reading as the published one. An id is stable, safe to quote in a report or a
message, and names no location: the store decides where the bytes live, and
another team's ids are not reachable from yours.

Publishing the same body again is a no-op that returns the same id. Publishing
different bytes for the same slug returns a **new** id and leaves the earlier
document readable, so a reader holding an old id never loses what it read.

Quote the id in the **text** of your report instead of pasting the document in:
`member_report_result` takes a result string (and an optional `artifact_path`
for a file in the shared context area), not a deliverable id — write something
like `result="route published; deliverable_id coder-claude-route-1a2b3c4d5e6f.md"`
and leave `artifact_path` for files that really are paths.

Two members publishing at the same moment do not need to coordinate: each
publish lands as one atomically renamed file and the two bodies simply get two
ids — nothing is lost and no report needs re-sending. The store does not lock
across processes, so a publish and a listing running side by side may briefly
miss each other; retry the listing rather than treating the gap as a lost
document.

## Where the boundary is

- A member publishes only as itself. The member id is bound when your session is
  assembled and there is no argument for it, so no call can publish as someone
  else and none is needed.
- Every member of the team, the leader included, reads and lists all of the
  team's documents. One team's store is isolated from every other team's.
- The publish tool is a write, so a read-only or plan task cannot publish.
  Reading and listing stay available in those tasks.
- Publishing is not reporting. `member_report_result` is still the formal
  handoff; publish first, then report carrying the id.

## Errors

- `invalid deliverable slug` — fix the slug: lowercase letters, digits, dot,
  underscore and dash only, starting with a letter or digit, at most 64
  characters.
- `invalid team or member name` — the team or the member id is not a single
  plain name; report it rather than retrying, since a session's identity is
  bound at assembly and not something your call can correct.
- `invalid deliverable id` — use an id you got back from publish or list; a
  path, a nested name or a name without the 12-hex digest is not an id.
- `deliverable exceeds DeliverableMaxBytes` (1 MiB) — publish a summary and keep
  the bulk as its own document, or split it.
- `no such deliverable` — this team has no document with that id.
- `deliverable exists with different content` — the id already holds other
  bytes (placed or edited outside the tools); list the team and use the current
  id.
- `deliverable path carries a symbolic link` — refuse and report it; do not work
  around it.
- `stored deliverable does not match the digest in its id` — the file changed
  outside the tools; report it as a defect with the id.
- `no user state dir to root team deliverables` — this session has no user state
  root, so there is nowhere a peer could read the document. Report the blocker
  instead of writing a file yourself.

## Example

```
member_publish_deliverable(slug="route", body="# Route\n\n1. ...")
  -> published coder-claude-route-1a2b3c4d5e6f.md (2048 bytes, sha256 1a2b3c...)

member_report_result(result="route published; deliverable_id coder-claude-route-1a2b3c4d5e6f.md")

leader: member_read_deliverable(id="coder-claude-route-1a2b3c4d5e6f.md")
leader: member_list_deliverables()
```
