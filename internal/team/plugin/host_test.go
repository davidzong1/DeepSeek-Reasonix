package plugin

import (
	"context"
	"errors"
	"testing"

	"reasonix/internal/team"
	"reasonix/internal/team/security"
)

var ctx = context.Background()

func testHost(t *testing.T, teamDefault *team.SecretRef, grants ...security.Grant) *Host {
	t.Helper()
	return NewHost(security.NewStaticDecider(grants...), teamDefault)
}

// spec builds a plugin with one handler per declared capability, each
// returning its args unchanged.
func spec(id string, caps ...team.Capability) Plugin {
	handlers := make(map[team.CapabilityID]Handler, len(caps))
	for _, c := range caps {
		handlers[c.ID] = func(ctx context.Context, args any) (any, error) { return args, nil }
	}
	return Plugin{ID: id, Credential: team.CredentialScopeNone, Capabilities: caps, Handlers: handlers}
}

func TestRegisterAndInvoke(t *testing.T) {
	h := testHost(t, nil, security.Grant{Role: team.RoleTester, CapID: "acme.echo", Scope: security.ScopePlugin})
	if err := h.Register(spec("acme", team.Capability{ID: "acme.echo", Kind: "tool", Scope: team.ScopePlugin, Version: "1.0.0"})); err != nil {
		t.Fatal(err)
	}
	got, err := h.Invoke(ctx, InvokeRequest{CapabilityID: "acme.echo", Role: team.RoleTester, Args: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "hi" {
		t.Fatalf("got %v, want hi", got)
	}
}

func TestRegisterRejectsDuplicatePluginID(t *testing.T) {
	h := testHost(t, nil)
	p := spec("acme", team.Capability{ID: "acme.echo", Kind: "tool"})
	if err := h.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := h.Register(p); !errors.Is(err, ErrPluginExists) {
		t.Fatalf("err = %v, want ErrPluginExists", err)
	}
}

func TestRegisterRejectsDuplicateCapabilityID(t *testing.T) {
	h := testHost(t, nil)
	if err := h.Register(spec("a", team.Capability{ID: "shared.tool", Kind: "tool"})); err != nil {
		t.Fatal(err)
	}
	err := h.Register(spec("b", team.Capability{ID: "shared.tool", Kind: "tool"}))
	if !errors.Is(err, ErrCapabilityConflict) {
		t.Fatalf("err = %v, want ErrCapabilityConflict", err)
	}
}

func TestRegisterRejectsSemanticDomainConflict(t *testing.T) {
	h := testHost(t, nil)
	if err := h.Register(spec("a", team.Capability{ID: "acme.shell.run", Kind: "tool"})); err != nil {
		t.Fatal(err)
	}
	err := h.Register(spec("b", team.Capability{ID: "acme.shell.exec", Kind: "tool"}))
	if !errors.Is(err, ErrCapabilityConflict) {
		t.Fatalf("err = %v, want ErrCapabilityConflict", err)
	}
}

func TestRegisterAllowsSameDomainWithinOnePlugin(t *testing.T) {
	h := testHost(t, nil)
	p := spec("acme",
		team.Capability{ID: "acme.shell.run", Kind: "tool"},
		team.Capability{ID: "acme.shell.exec", Kind: "tool"},
	)
	if err := h.Register(p); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterAtomicOnConflict(t *testing.T) {
	h := testHost(t, nil)
	if err := h.Register(spec("a", team.Capability{ID: "a.run", Kind: "tool"})); err != nil {
		t.Fatal(err)
	}
	err := h.Register(spec("b",
		team.Capability{ID: "a.run", Kind: "tool"},
		team.Capability{ID: "b.cmd", Kind: "tool"},
	))
	if !errors.Is(err, ErrCapabilityConflict) {
		t.Fatalf("err = %v, want ErrCapabilityConflict", err)
	}
	if _, err := h.Invoke(ctx, InvokeRequest{CapabilityID: "b.cmd", Role: team.RoleTester}); !errors.Is(err, ErrCapabilityNotRegistered) {
		t.Fatalf("half-registered b.cmd: err = %v", err)
	}
	if err := h.Register(spec("b", team.Capability{ID: "b.cmd", Kind: "tool"})); err != nil {
		t.Fatalf("re-register after failed attempt: %v", err)
	}
}

func TestRegisterRejectsDeclarationMismatch(t *testing.T) {
	h := testHost(t, nil)
	declared := []team.Capability{
		{ID: "acme.run", Kind: "tool"},
		{ID: "acme.stop", Kind: "tool"},
	}
	one := Plugin{
		ID: "acme", Credential: team.CredentialScopeNone, Capabilities: declared,
		Handlers: map[team.CapabilityID]Handler{"acme.run": nil},
	}
	if err := h.Register(one); !errors.Is(err, ErrDeclarationMismatch) {
		t.Fatalf("err = %v, want ErrDeclarationMismatch", err)
	}
	extra := Plugin{
		ID: "acme", Credential: team.CredentialScopeNone, Capabilities: declared,
		Handlers: map[team.CapabilityID]Handler{
			"acme.run":   nil,
			"acme.stop":  nil,
			"acme.ghost": nil,
		},
	}
	if err := h.Register(extra); !errors.Is(err, ErrDeclarationMismatch) {
		t.Fatalf("err = %v, want ErrDeclarationMismatch", err)
	}
}

func TestUnregisterRemovesCapabilities(t *testing.T) {
	h := testHost(t, nil, security.Grant{Role: team.RoleTester, CapID: "acme.echo", Scope: security.ScopePlugin})
	p := spec("acme", team.Capability{ID: "acme.echo", Kind: "tool"})
	if err := h.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := h.Unregister("acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Invoke(ctx, InvokeRequest{CapabilityID: "acme.echo", Role: team.RoleTester}); !errors.Is(err, ErrCapabilityNotRegistered) {
		t.Fatalf("err = %v, want ErrCapabilityNotRegistered", err)
	}
	if err := h.Register(p); err != nil {
		t.Fatalf("re-register after unregister: %v", err)
	}
	if err := h.Unregister("ghost"); !errors.Is(err, ErrPluginNotRegistered) {
		t.Fatalf("err = %v, want ErrPluginNotRegistered", err)
	}
}

func TestInvokeDeniedByDefault(t *testing.T) {
	h := testHost(t, nil) // no grants: deny-by-default
	if err := h.Register(spec("acme", team.Capability{ID: "acme.echo", Kind: "tool"})); err != nil {
		t.Fatal(err)
	}
	_, err := h.Invoke(ctx, InvokeRequest{CapabilityID: "acme.echo", Role: team.RoleTester})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
	audit := h.Audit()
	if len(audit) != 1 || audit[0].Allowed || audit[0].CapabilityID != "acme.echo" {
		t.Fatalf("audit = %+v, want one denied acme.echo entry", audit)
	}
}

func TestInvokeScopeDefaultsToPlugin(t *testing.T) {
	// Grant exists only under ScopePlugin; an empty Scope in the request must
	// default to it.
	h := testHost(t, nil, security.Grant{Role: team.RoleTester, CapID: "acme.echo", Scope: security.ScopePlugin})
	if err := h.Register(spec("acme", team.Capability{ID: "acme.echo", Kind: "tool"})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Invoke(ctx, InvokeRequest{CapabilityID: "acme.echo", Role: team.RoleTester}); err != nil {
		t.Fatal(err)
	}
}

func TestInvokeAuditsEveryCall(t *testing.T) {
	h := testHost(t, nil)
	if err := h.Register(spec("acme", team.Capability{ID: "acme.echo", Kind: "tool"})); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Invoke(ctx, InvokeRequest{CapabilityID: "acme.echo", Role: team.RoleTester}); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if _, err := h.Invoke(ctx, InvokeRequest{CapabilityID: "acme.ghost", Role: team.RoleTester}); !errors.Is(err, ErrCapabilityNotRegistered) {
		t.Fatal(err)
	}
	audit := h.Audit()
	if len(audit) != 1 {
		t.Fatalf("audit entries = %d, want 1 (unregistered calls are not decisions)", len(audit))
	}
}

func TestCredentialNoneRejectsReference(t *testing.T) {
	h := testHost(t, nil, security.Grant{Role: team.RoleTester, CapID: "acme.echo", Scope: security.ScopePlugin})
	p := spec("acme", team.Capability{ID: "acme.echo", Kind: "tool"})
	p.Credential = team.CredentialScopeNone
	if err := h.Register(p); err != nil {
		t.Fatal(err)
	}
	ref := &team.SecretRef{StoreID: "k", Scope: team.CredentialScopeAgentUser}
	_, err := h.Invoke(ctx, InvokeRequest{CapabilityID: "acme.echo", Role: team.RoleTester, Credential: ref})
	if !errors.Is(err, ErrCredentialDenied) {
		t.Fatalf("err = %v, want ErrCredentialDenied", err)
	}
}

func TestCredentialAgentUserRequiresGrant(t *testing.T) {
	granted := security.Grant{Role: team.RoleTester, CapID: "acme.echo", Scope: security.ScopePlugin}
	h := testHost(t, nil, granted)
	p := spec("acme", team.Capability{ID: "acme.echo", Kind: "tool"})
	p.Credential = team.CredentialScopeAgentUser
	if err := h.Register(p); err != nil {
		t.Fatal(err)
	}
	ref := &team.SecretRef{StoreID: "k", Scope: team.CredentialScopeAgentUser}
	if _, err := h.Invoke(ctx, InvokeRequest{CapabilityID: "acme.echo", Role: team.RoleTester, Credential: ref}); err != nil {
		t.Fatalf("granted agent-user ref denied: %v", err)
	}
	denied := &team.SecretRef{StoreID: "k", Scope: team.CredentialScopeTeam}
	if _, err := h.Invoke(ctx, InvokeRequest{CapabilityID: "acme.echo", Role: team.RoleTester, Credential: denied}); !errors.Is(err, ErrCredentialDenied) {
		t.Fatalf("err = %v, want ErrCredentialDenied", err)
	}
}

func TestCredentialAgentUserRequiresExplicitGrant(t *testing.T) {
	// The capability is granted, but the plugin-scope grant that would admit
	// the reference is not: refused.
	h := testHost(t, nil)
	p := spec("acme", team.Capability{ID: "acme.echo", Kind: "tool"})
	p.Credential = team.CredentialScopeAgentUser
	if err := h.Register(p); err != nil {
		t.Fatal(err)
	}
	ref := &team.SecretRef{StoreID: "k", Scope: team.CredentialScopeAgentUser}
	_, err := h.Invoke(ctx, InvokeRequest{CapabilityID: "acme.echo", Role: team.RoleTester, Credential: ref})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied (RBAC denies before credential check)", err)
	}
}

func TestCredentialTeamAdmitsOnlyTeamDefault(t *testing.T) {
	td := &team.SecretRef{StoreID: "team-key", Scope: team.CredentialScopeTeam}
	h := testHost(t, td, security.Grant{Role: team.RoleTester, CapID: "acme.echo", Scope: security.ScopePlugin})
	p := spec("acme", team.Capability{ID: "acme.echo", Kind: "tool"})
	p.Credential = team.CredentialScopeTeam
	if err := h.Register(p); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Invoke(ctx, InvokeRequest{CapabilityID: "acme.echo", Role: team.RoleTester, Credential: td}); err != nil {
		t.Fatalf("team-default ref denied: %v", err)
	}
	other := &team.SecretRef{StoreID: "other-key", Scope: team.CredentialScopeTeam}
	if _, err := h.Invoke(ctx, InvokeRequest{CapabilityID: "acme.echo", Role: team.RoleTester, Credential: other}); !errors.Is(err, ErrCredentialDenied) {
		t.Fatalf("err = %v, want ErrCredentialDenied", err)
	}
}

func TestCredentialTeamDeniedWithoutTeamDefault(t *testing.T) {
	h := testHost(t, nil, security.Grant{Role: team.RoleTester, CapID: "acme.echo", Scope: security.ScopePlugin})
	p := spec("acme", team.Capability{ID: "acme.echo", Kind: "tool"})
	p.Credential = team.CredentialScopeTeam
	if err := h.Register(p); err != nil {
		t.Fatal(err)
	}
	ref := &team.SecretRef{StoreID: "team-key", Scope: team.CredentialScopeTeam}
	_, err := h.Invoke(ctx, InvokeRequest{CapabilityID: "acme.echo", Role: team.RoleTester, Credential: ref})
	if !errors.Is(err, ErrCredentialDenied) {
		t.Fatalf("err = %v, want ErrCredentialDenied", err)
	}
}
