package security

import (
	"context"
	"errors"
	"os"
	"testing"

	"reasonix/internal/team"
)

func TestResolveCredentialOverrideWins(t *testing.T) {
	override := &team.SecretRef{StoreID: "keyring/agent-opus-5", Scope: team.CredentialScopeAgentUser}
	teamDefault := &team.SecretRef{StoreID: "keyring/team-default", Scope: team.CredentialScopeTeam}
	got, err := ResolveCredential(override, teamDefault)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != *override {
		t.Fatalf("override must win, got %+v", got)
	}
}

func TestResolveCredentialFallsBackToTeamDefault(t *testing.T) {
	teamDefault := &team.SecretRef{StoreID: "keyring/team-default", Scope: team.CredentialScopeTeam}
	got, err := ResolveCredential(nil, teamDefault)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != *teamDefault {
		t.Fatalf("team default must be used, got %+v", got)
	}
}

func TestResolveCredentialMissingIsExplicitError(t *testing.T) {
	_, err := ResolveCredential(nil, nil)
	if !errors.Is(err, team.ErrNoCredential) {
		t.Fatalf("want team.ErrNoCredential, got %v", err)
	}
	empty := &team.SecretRef{}
	if _, err := ResolveCredential(empty, empty); !errors.Is(err, team.ErrNoCredential) {
		t.Fatalf("empty refs must error, got %v", err)
	}
}

// TestNoFallbackToCurrentProvider pins the hard rule (TASK.md §3.1): even
// when a Provider session credential exists in the environment, resolution
// must not consult it — both refs missing is an explicit error, nothing more.
func TestNoFallbackToCurrentProvider(t *testing.T) {
	const envKey = "REASONIX_API_KEY"
	prev, had := os.LookupEnv(envKey)
	t.Setenv(envKey, "provider-session-secret-should-never-be-used")
	defer func() {
		if !had {
			os.Unsetenv(envKey)
		} else {
			os.Setenv(envKey, prev)
		}
	}()

	_, err := ResolveCredential(nil, nil)
	if !errors.Is(err, team.ErrNoCredential) {
		t.Fatalf("must not fall back to provider credential, got %v", err)
	}
}

func TestSecretStoreAcceptsRefsOnly(t *testing.T) {
	// Compile-time shape check: the abstract store is reference-typed; the
	// credential layer never passes plaintext through it.
	var _ SecretStore = secretStoreSpy{}
}

type secretStoreSpy struct{}

func (secretStoreSpy) Lookup(ctx context.Context, ref team.SecretRef) (string, error) {
	return "", nil
}
