package plugin

import (
	"context"
	"errors"
	"testing"

	"reasonix/internal/team"
	"reasonix/internal/team/security"
)

func viewSpec(id, capID string, render RenderFunc) View {
	return View{ID: id, Capability: team.CapabilityID(capID), Render: render}
}

func TestRegisterViewRequiresRegisteredViewKindCapability(t *testing.T) {
	h := testHost(t, nil)
	if err := h.Register(spec("acme",
		team.Capability{ID: "acme.dashboard", Kind: ViewKind},
		team.Capability{ID: "acme.run", Kind: "tool"},
	)); err != nil {
		t.Fatal(err)
	}
	hub := NewUIHub(h)
	if err := hub.RegisterView(viewSpec("dash", "acme.dashboard", func(ctx context.Context, args any) (string, error) {
		return "panel", nil
	})); err != nil {
		t.Fatalf("view-kind backing refused: %v", err)
	}
	if err := hub.RegisterView(viewSpec("run", "acme.run", nil)); !errors.Is(err, ErrViewBackingMissing) {
		t.Fatalf("err = %v, want ErrViewBackingMissing", err)
	}
	if err := hub.RegisterView(viewSpec("ghost", "acme.ghost", nil)); !errors.Is(err, ErrViewBackingMissing) {
		t.Fatalf("err = %v, want ErrViewBackingMissing", err)
	}
}

func TestOneViewPerCapability(t *testing.T) {
	h := testHost(t, nil)
	if err := h.Register(spec("acme", team.Capability{ID: "acme.dashboard", Kind: ViewKind})); err != nil {
		t.Fatal(err)
	}
	hub := NewUIHub(h)
	render := func(ctx context.Context, args any) (string, error) { return "panel", nil }
	if err := hub.RegisterView(viewSpec("dash", "acme.dashboard", render)); err != nil {
		t.Fatal(err)
	}
	if err := hub.RegisterView(viewSpec("dash2", "acme.dashboard", render)); !errors.Is(err, ErrViewAlreadyRegistered) {
		t.Fatalf("err = %v, want ErrViewAlreadyRegistered", err)
	}
	if err := hub.RegisterView(viewSpec("dash", "acme.dashboard", render)); !errors.Is(err, ErrViewAlreadyRegistered) {
		t.Fatalf("err = %v, want ErrViewAlreadyRegistered", err)
	}
}

func TestRenderGoesThroughHostRBAC(t *testing.T) {
	// Deny-by-default: the renderer must not run, and the denial lands in the
	// Host audit trail (§7.3).
	h := testHost(t, nil)
	if err := h.Register(spec("acme", team.Capability{ID: "acme.dashboard", Kind: ViewKind})); err != nil {
		t.Fatal(err)
	}
	hub := NewUIHub(h)
	ran := false
	render := func(ctx context.Context, args any) (string, error) { ran = true; return "panel", nil }
	if err := hub.RegisterView(viewSpec("dash", "acme.dashboard", render)); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Render(ctx, "dash", team.RoleTester, security.ScopePlugin, nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
	if ran {
		t.Fatal("renderer ran despite RBAC denial")
	}
	audit := h.Audit()
	if len(audit) != 1 || audit[0].Allowed || audit[0].CapabilityID != "acme.dashboard" {
		t.Fatalf("audit = %+v, want one denied acme.dashboard entry", audit)
	}
}

func TestRenderSuccess(t *testing.T) {
	h := testHost(t, nil, security.Grant{Role: team.RoleTester, CapID: "acme.dashboard", Scope: security.ScopePlugin})
	if err := h.Register(spec("acme", team.Capability{ID: "acme.dashboard", Kind: ViewKind})); err != nil {
		t.Fatal(err)
	}
	hub := NewUIHub(h)
	if err := hub.RegisterView(viewSpec("dash", "acme.dashboard", func(ctx context.Context, args any) (string, error) {
		return "panel:" + args.(string), nil
	})); err != nil {
		t.Fatal(err)
	}
	got, err := hub.Render(ctx, "dash", team.RoleTester, security.ScopePlugin, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if got != "panel:hi" {
		t.Fatalf("got %q, want panel:hi", got)
	}
}

func TestRenderUnknownView(t *testing.T) {
	h := testHost(t, nil)
	if err := h.Register(spec("acme", team.Capability{ID: "acme.dashboard", Kind: ViewKind})); err != nil {
		t.Fatal(err)
	}
	hub := NewUIHub(h)
	if _, err := hub.Render(ctx, "ghost", team.RoleTester, security.ScopePlugin, nil); !errors.Is(err, ErrViewNotRegistered) {
		t.Fatalf("err = %v, want ErrViewNotRegistered", err)
	}
}
