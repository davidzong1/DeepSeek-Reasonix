// Package team is the multi-agent team domain: the concepts of the team port
// (AgentUser, Role, RBAC, Member, Team, Task, Message, Blackboard, Memory) as
// typed, schema-versioned documents under .reasonix/team, plus the
// atomic-write chokepoint and the JSON read/write interface.
//
// Layering: team imports nothing under internal/cli, internal/control, or any
// frontend; frontends may import team. Credentials never leave this package
// as key material — only secret-store references do (K1-K3). The credential
// resolution shape is fixed here; its chain implementation, the RBAC policy
// engine, and the scheduler/runtime interfaces land in later phases.
//
// Reference: docs/team-mcp-port/TASK.md §2, §3.1, §3.4, §5.1.
package team
