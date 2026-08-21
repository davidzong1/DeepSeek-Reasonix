// Package scheduler assigns tasks to fleet members (TASK.md §3.5) by the
// strategy order: role match → load balance → prior-task affinity. The
// interface is live; the runtime stage supplies the real implementation.
// This round ships a placeholder that records assignments only and never
// claims execution (§3.7): the ledger entry is visible to logs/blackboard,
// marked [runtime-pending].
package scheduler
