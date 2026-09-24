package cli

// Assembly-time credential checks for one member's backend: a pool entry
// without a key fails at bind with the member and the entry named, so the
// error never reads like the ambient chat's own missing-key notice.

import (
	"strings"
	"testing"

	"reasonix/internal/netclient"
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

// TestMemberRouteBucketSeparatesRoutesWithoutNamingThem pins the contract the
// cache baseline stratifies by: one configured route yields one stable label, a
// different route yields a different one, and the label itself carries neither
// the endpoint nor the credential.
func TestMemberRouteBucketSeparatesRoutesWithoutNamingThem(t *testing.T) {
	const baseURL = "https://gw.internal.example/anthropic"
	member := team.AgentUser{
		UserID: "acct-a", Provider: "deepseek", Model: "deepseek-v4-flash[1m]",
		BaseURL: baseURL, APIKey: "sk-live-secret-value",
	}
	build := func(u team.AgentUser) string {
		t.Helper()
		r, err := newMemberProviderResolver(u, netclient.ProxySpec{})
		if err != nil {
			t.Fatalf("newMemberProviderResolver: %v", err)
		}
		return r.RouteBucket()
	}

	bucket := build(member)
	if bucket == "" || bucket == "/" {
		t.Fatalf("bucket = %q, want a label", bucket)
	}
	if again := build(member); again != bucket {
		t.Fatalf("the same route produced %q then %q, want one stable label", bucket, again)
	}
	if strings.Contains(bucket, "gw.internal.example") || strings.Contains(bucket, "sk-live") {
		t.Fatalf("bucket %q names the endpoint or the credential", bucket)
	}

	rotated := member
	rotated.APIKey = "sk-a-different-secret"
	if got := build(rotated); got != bucket {
		t.Fatalf("rotating the credential changed the bucket (%q vs %q): the credential must not be an input", got, bucket)
	}
	for _, tc := range []struct {
		name string
		user team.AgentUser
	}{
		{"another endpoint", func() team.AgentUser { u := member; u.BaseURL = "https://other.example/v1"; return u }()},
		{"another pool entry", func() team.AgentUser { u := member; u.UserID = "acct-b"; return u }()},
	} {
		if got := build(tc.user); got == bucket {
			t.Fatalf("%s must yield its own bucket, both are %q", tc.name, got)
		}
	}

	proxied, err := newMemberProviderResolver(member, netclient.ProxySpec{
		Mode: "manual", Type: "http", Username: "proxy-user", Password: "hunter2-proxy-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	proxiedBucket := proxied.RouteBucket()
	if proxiedBucket == bucket {
		t.Fatalf("a proxied route must not share the direct route's bucket (%q)", proxiedBucket)
	}
	for _, forbidden := range []string{"hunter2-proxy-secret", "proxy-user"} {
		if strings.Contains(proxiedBucket, forbidden) {
			t.Fatalf("bucket %q carries the proxy credential %q", proxiedBucket, forbidden)
		}
	}
}
