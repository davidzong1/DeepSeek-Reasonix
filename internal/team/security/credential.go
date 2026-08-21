package security

import (
	"context"

	"reasonix/internal/team"
)

// ResolveCredential resolves the credential chain for a member: member-level
// override, then team default (TASK.md §3.1). Both missing is an explicit
// error — team.ErrNoCredential — and the current Reasonix Provider session
// credential is never consulted (绝不回退当前 Reasonix Provider 会话凭证).
func ResolveCredential(override, teamDefault *team.SecretRef) (team.SecretRef, error) {
	if override != nil && override.StoreID != "" {
		return *override, nil
	}
	if teamDefault != nil && teamDefault.StoreID != "" {
		return *teamDefault, nil
	}
	return team.SecretRef{}, team.ErrNoCredential
}

// SecretStore abstracts the secret backend. It accepts references only and
// never writes plaintext into team data; the concrete implementation lands
// with the storage layer (P3). Resolution itself must not read through the
// store: a missing ref is an error before any lookup.
type SecretStore interface {
	// Lookup returns the secret value referenced by ref.
	Lookup(ctx context.Context, ref team.SecretRef) (string, error)
}
