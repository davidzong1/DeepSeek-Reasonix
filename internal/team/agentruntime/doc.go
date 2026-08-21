// Package agentruntime is the single-process multi-agent runtime boundary
// (TASK.md §3.7): lifecycle start/stop, status query, and a status-event
// source. The runtime itself is deferred (NG1) — this round ships the
// interface only, with no implementation body.
//
// Once the runtime lands it drives the member state machine that the
// scheduler (§3.5) targets: scheduler.Assign produces the ledger entry,
// the runtime executes it and emits member-state events on the bus
// (internal/team/events).
package agentruntime
