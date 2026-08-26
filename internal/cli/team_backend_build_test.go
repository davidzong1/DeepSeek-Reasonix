package cli

// Assembly-time credential checks for one member's backend: a pool entry
// without a key fails at bind with the member and the entry named, so the
// error never reads like the ambient chat's own missing-key notice.

import (
	"strings"
	"testing"

	"reasonix/internal/team"
)

// TestMemberCredentialErrorNamesSource pins the credential contract: a pool
// entry with no API key and no secret-store ref is refused with the member id
// and the agent-user id in the message — never a bare ambient-style
// "DeepSeek key missing" that points at the chat's own configuration. A key
// or a secret-store ref suppresses the check (the entry declares a credential
// source of its own).
func TestMemberCredentialErrorNamesSource(t *testing.T) {
	b := team.MemberBinding{Team: "alpha", MemberID: "lead", AgentUserRef: "u1"}
	err := memberCredentialError(b, team.AgentUser{UserID: "u1", Provider: "openai", Model: "gpt-5.6"})
	if err == nil {
		t.Fatal("a keyless entry must be refused")
	}
	for _, want := range []string{"lead", "u1", "no API key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to name %q", err, want)
		}
	}
	for _, ok := range []team.AgentUser{
		{UserID: "u1", Provider: "openai", Model: "gpt-5.6", APIKey: "sk"},
		{UserID: "u2", Provider: "anthropic", Model: "claude-opus-5", SecretRef: team.SecretRef{StoreID: "kv/team-alpha"}},
	} {
		if err := memberCredentialError(b, ok); err != nil {
			t.Errorf("entry %q must pass the credential check: %v", ok.UserID, err)
		}
	}
}

// TestMemberBuilderMissingKeyFailsBeforeAssembly pins the builder wiring: the
// credential check runs at assembly time, before any boot assembly, so a
// keyless member surfaces a named error instead of building a backend whose
// first request dies with an authentication-shaped failure.
func TestMemberBuilderMissingKeyFailsBeforeAssembly(t *testing.T) {
	deps := memberBackendDeps{users: fakePool{users: map[string]team.AgentUser{
		"u1": {UserID: "u1", Provider: "openai", Model: "gpt-5.6"},
	}}}
	_, err := newMemberBackendBuilder(deps)(team.MemberBinding{Team: "alpha", MemberID: "lead", AgentUserRef: "u1"})
	if err == nil {
		t.Fatal("a keyless entry must fail assembly")
	}
	if !strings.Contains(err.Error(), "lead") || !strings.Contains(err.Error(), "u1") {
		t.Errorf("the assembly error must name the member and the entry: %q", err)
	}
}
