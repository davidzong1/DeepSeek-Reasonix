// Package security holds the team domain's credential-resolution and RBAC
// contracts (TASK.md §3.1, §7.4). Its subject types (Role, Scope, Capability,
// SecretRef) are the team root package's types — aliases only, one wire
// format, no second contract set. It depends on nothing under reasonix/
// beyond the team root, so storage and the future plugin Host can build on it
// without reaching toward any frontend.
//
// Two hard rules live here, enforced by tests:
//
//   - Credential resolution follows exactly one chain: member override →
//     team default → explicit missing error. It never falls back to the
//     current Reasonix Provider session credential (§3.1), and it never
//     writes plaintext secrets into team data — callers receive the root
//     SecretRef (a store id plus declared scope), never the secret.
//   - RBAC decisions are centralized: every (role, capability, scope) check
//     goes through one evaluator that returns an auditable Decision.
//     Deny-by-default: only explicitly granted capability-scope pairs allow.
//
// The SecretStore abstraction takes references only; the reference-to-secret
// lookup lives behind it and is owned by the storage/credential layer (P3).
package security
