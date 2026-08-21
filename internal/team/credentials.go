package team

import "errors"

// CredentialScope is a credential's declared access range (§7.5):
// undeclared is none; agent-user reads only explicitly granted references;
// team reads only the team-default reference.
type CredentialScope string

const (
	CredentialScopeNone      CredentialScope = "none"
	CredentialScopeAgentUser CredentialScope = "agent-user"
	CredentialScopeTeam      CredentialScope = "team"
)

// SecretRef is a reference into the secret store (§2.1, §3.1 K1-K3): store id
// and access scope only. Key material never appears in .reasonix, git,
// blackboard, messages, logs, or reports — a SecretRef carries no key.
type SecretRef struct {
	StoreID string          // secret-store entry id
	Scope   CredentialScope // declared access range (§7.5)
}

// CredentialResolver resolves the effective agent-user id for a member
// (§3.1): member override → team default → explicit error. It must never
// fall back to the current session's provider credentials. The chain is
// implemented in the data-and-security layer; this interface fixes the shape
// and the error contract now.
type CredentialResolver interface {
	// Resolve returns the effective agent-user id given a member's override
	// and the team default. Both empty is ErrNoCredential.
	Resolve(memberRef, teamDefault string) (string, error)
}

// ErrNoCredential reports that neither a member override nor a team default
// exists (§3.1): task creation is refused — no implicit inheritance.
var ErrNoCredential = errors.New("team: no agent-user credential (member override or team default); refusing implicit fallback")
