// Package team is the multi-agent team domain: the concepts of the team port
// (AgentUser, Role, RBAC, Member, Team, Task, Message, Blackboard, Memory) as
// typed, schema-versioned documents under .reasonix/team, plus the
// atomic-write chokepoint and the JSON read/write interface.
//
// Layering: team imports nothing under internal/cli, internal/control, or any
// frontend; frontends may import team. Key material never leaves this package
// outside 0600-protected documents (K1): AgentUser.APIKey is a plaintext
// credential the atomic write chokepoint alone makes safe. The UI may render
// it plaintext on explicit user request, but nothing here logs or reports it
// (K2/K3). SecretRef remains the store-backed alternative. The credential
// resolution shape is fixed here; its chain implementation, the RBAC policy
// engine, and the scheduler/runtime interfaces land in later phases.
//
// Reference: docs/team-mcp-port/TASK.md §2, §3.1, §3.4, §5.1.
package team
