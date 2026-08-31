# Team session defect fix list

Working tracker for the team-session defect sweep. Stable IDs — refer to them
across sessions. Status: `todo` / `doing` / `done` / `deferred`.

Delete this file once the sweep lands.

## User-reported (highest priority)

| ID | Status | Defect | Root cause |
|----|--------|--------|-----------|
| U1 | done | Leader executes the work itself instead of splitting and assigning | Two causes, both fixed: (a) `teamRoleSkillPrompt` was handed the **unresolved** `boot.Options.WorkspaceRoot`, empty unless `--dir` — so the 70-line leader playbook never reached the prompt; (b) `leaderCollaborationDiscipline` item 2 carried an unbounded escape hatch ("简单任务可由 leader 自行完成") against the full solo-coding base prompt |
| U2 | done | After assigning, `leader_check_member_status` says `working` but switching to the member shows no thinking context | Two causes: (a) `checkStatus` reported **durable board state only**, with no runtime corroboration — an orphaned/parked/refused task still reads `working`; (b) an unbound member's events were dropped (only an unread counter survived) and a switch replayed just committed `History()`, so an in-flight turn was invisible |

U2 also depended on D1 and D2 below: both make a member genuinely not run while the
board still says `working`.

## P0 — confirmed reproducible

| ID | Status | Defect | Location |
|----|--------|--------|----------|
| D1 | done | A second `[TEAM]` open closes the board the task service holds → `sql: database is closed` for the rest of the process | `chat_tui_team.go` reopen path vs `bindTeamBackends` early return |
| D2 | done | `turnParked` classified as a refusal → the turn IS delivered but the task rolls back to `assigned`, the reservation is released, and the member's report is rejected with `ErrTaskUnknown`; task stays re-dispatchable → double execution | `internal/control/admission_submit.go` |
| D3 | done | `Cancel`/`Complete` on one task read-modify-write `entry.task.Status` outside `r.mu` (`-race` confirmed) → possible double terminal state, double board event, double wakeup | `internal/team/agentruntime/runtime.go` |

## P1 — functional

| ID | Status | Defect | Location |
|----|--------|--------|----------|
| D4 | done | `answerMemberPrompt` is unreachable: `focus` always indexes `current` in a bound session, so Ctrl+A/Ctrl+X never answer a background prompt — while the hint advertises them | `chat_tui_team_switch.go`, `chat_tui_team_render.go` |
| D5 | done | `teamHub.Approve/Answer` use `bind()` not `bound()`: assembles a whole controller to answer a prompt, then silently "succeeds" against an id that does not exist | `team_hub.go` |
| D6 | done (partly by design) | `assignSubtask` persists the task then fails at `scheduler.Assign` with no compensation → orphan task stuck `assigned`, reported as `working` forever | `team_task_service.go` |
| D7 | done | `leader_remove_member` / `leader_set_member_role` never `release()` the member's backend (the TUI path does) → lease + subprocesses keep running over a cleared context | `team_member_tools.go`, `team_backend_build.go` |
| D8 | done | `closeAll()` has no production caller and `m.teamBackends` is assigned once → member backends leak for the process lifetime; `closeTeamOverlay()` is an empty function | `team_backends.go`, `chat_tui_team_session.go` |
| D9 | done | After leaving the team, a still-running member's `ApprovalRequest` is silently dropped and cannot be answered | `chat_tui_team_switch.go` |

## P2 — consistency / robustness

| ID | Status | Defect |
|----|--------|--------|
| D10 | done | `report()` closes the *first* live task of the member; no `task_id` argument |
| D11 | done | `checkStatus`'s `taskByMember` overwrites, hiding all but one task per member |
| D12 | done | `fleet()` never sets `TaskRef`/real `State` → `pick`'s idle-before-busy branch is inert |
| D13 | done | `assignSubtask` does not check the target's status; `Bindings()` does not filter by status while `fleet()` does → an archived target silently redirects to a same-role sibling, or orphans |
| D14 | todo | `Start`/`Resume` hold the member reservation before `live` is written, so the whole `boot.Build` window is uncancellable (`ErrTaskUnknown`) |
| D15 | done | Submit-refusal rollback writes `assigned` bypassing `TransitionTask`, and neither `record()`s nor `wakeAll()`s → the leader never learns the dispatch failed |
| D16 | done (dead code) | `setLeaderWakeup` is dead code; `leaderIdentity` hardcodes `Generation: 1` against the runtime identity's 0 |
| D17 | done | Switching to a member does not clear `prompts[member]` → stale badge until the next `TurnDone` |
| D18 | deferred | `bind`'s in-flight join returns the leading binding's backend to a caller holding a newer fingerprint (self-heals on the next bind) |
| D19 | done | `SubmitUserTurnOrError` leaks the internal admission enum into its error text |
| D20 | deferred | `Drain` replays the whole batch (including succeeded items) on a mid-batch failure and returns count 0 |
| D21 | done | `switchTeamMember` resolves the binding from `p.model.Name()` instead of `p.session.teamName`; `teamHub.Submit` uses a construction-time team name |
| D22 | done (partly) | `AddWakeup`/`SetInjector`/`SetTaskStore` are unlocked setters on a concurrently-used struct |

## Session 2 verification

All fixes below were run in an isolated sandbox (`/tmp/rx-typecheck`) because the
repo's own tree is mid-merge and `internal/agent` does not compile. The sandbox is
the full working tree with a scratch conflict resolution; `internal/team` was also
verified in the real repo, which builds on its own.

- `go test ./internal/cli/ ./internal/team/... -race -count=1` — all green
- `go vet ./internal/cli/ ./internal/team/... ./internal/boot/` — clean
- `gofmt -l` — clean

Landed: U1 (a+b), U2 (a+b), D3, D10, D11, D13, D15, D17, D21, and D22 in part
(the `wake` slice is now lock-guarded; `SetInjector`/`SetTaskStore` stay
construction-time setters).
Two P2 entries were reclassified rather than "fixed":
- D6: an undispatched row stays live on purpose — assigned is re-dispatchable and
  three existing regression tests pin it. The defect was the *reporting*, fixed by
  U2a: a row nothing drives now reads `queued`, never `working`.
- Still open after session 2: D14, D18, D20, and D16's cosmetic half.

## Session 3

Landed: D1, D2, D4, D5, D7, D8, D9, D12, D16 (dead code), D19.

Decisions worth keeping:
- **D2** was the inverse of the ghost it was meant to prevent. `turnParked` means
  the turn was ACCEPTED and runs when the current one settles, so treating it as a
  refusal rolled back a task whose turn had been delivered and then rejected the
  member's own report. Parked is now success; a real refusal names its reason
  instead of printing an enum index (D19).
- **D1/D8**: the board now belongs to `teamBackends`, because their holders share
  its lifetime. An overlay reopen reuses it and only resets the per-member inbox
  cache (each inbox pins that member's BindRecord generation). `closeTeamResources`
  is the one teardown, called after the TUI releases the terminal — nothing closed
  the member backends at all before, so every one leaked its lease to process exit.
- **D4**: the dead selector is gone. A bound session moves focus and current
  together, so Ctrl+A/Ctrl+X now answer whichever member is waiting, and the hint
  names them. `TestBoundSessionFocusTracksCurrent` pins the invariant.
- **D5**: answering routes through `bound()`, never `bind()`. A decision may not
  assemble a controller — a fresh one holds no such prompt id and "succeeded"
  against nothing.
- **D16**: `leaderIdentity`'s hardcoded `Generation: 1` was left alone on purpose.
  `ReadAfter` filters on `Filter.Kind`/`MemberID`; `Stamped` only feeds
  `checkAccess`, so the value is cosmetic and changing it would poke board
  authorization for no behavioural gain.

Still open: D14 (the launch window is uncancellable while `boot.Build` runs),
D18 (`bind`'s in-flight join hands back the leading binding's backend), D20
(`Drain` replays a whole batch on a mid-batch failure).

### Verification

`go test ./internal/cli/ ./internal/team/... ./internal/control/ -race` green;
`go vet` clean; `gofmt -l` clean; `go run ./tools/repolint` reports **only** the
three pre-existing `internal/provider/anthropic/` findings, byte-identical to the
same run with every session-2/3 change reverted (struct-state 117, test-file-size
69641). No baseline was widened.

To stay inside the ratchet, `cli.go`'s post-Run teardown was extracted to
`chat_tui_teardown.go` (`closeAfterTUI`): that file and `chat_tui.go` are both
recorded debt sitting exactly on their budgets, so they cannot grow by even one
line. The extraction shrank `cli.go` by 27 lines and gave the teardown a home.

### Merge-blocked items the resolver still owns

1. `internal/boot/testdata/golden/prefix_shape.json` still has conflict markers —
   `TestGoldenBaselineNoExtensions` fails on them.
2. `internal/cli/cli_test.go` lost its `net/http/httptest` import in the automatic
   merge; lines ~1679/1702 use it.
3. `internal/provider/anthropic/anthropic.go` arrives over the struct-state
   ceiling (19 scalars). Per REASONIX.md that has to be grouped by lifetime, not
   baselined away.
