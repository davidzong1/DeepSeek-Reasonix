package plugin

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"reasonix/internal/team"
	"reasonix/internal/team/security"
)

// ViewKind is the capability kind that backs a UIHub view (§7.3).
const ViewKind = "view"

var (
	// ErrViewAlreadyRegistered reports a duplicate view or a second view on
	// the same backing capability (§7.3).
	ErrViewAlreadyRegistered = errors.New("plugin: view already registered")
	// ErrViewNotRegistered reports a render of an unknown view.
	ErrViewNotRegistered = errors.New("plugin: view not registered")
	// ErrViewBackingMissing reports a view whose capability is not registered
	// or is not a view-kind capability (§7.3).
	ErrViewBackingMissing = errors.New("plugin: view capability not registered or not a view kind")
)

// RenderFunc renders view content from args.
type RenderFunc func(ctx context.Context, args any) (string, error)

// View is one registered view (§7.3): a view-kind capability plus its
// renderer.
type View struct {
	ID         string
	Capability team.CapabilityID // view-kind capability backing this view
	Render     RenderFunc
}

// UIHub owns view registration and rendering (§7.3). A view must be backed by
// a registered view-kind capability, one view per capability, and rendering
// routes through the Host's RBAC check so UI access lands in the audit trail.
// Plugins cannot bypass the hub to mount terminals or files.
type UIHub struct {
	host  *Host
	mu    sync.Mutex
	views map[string]View
}

// NewUIHub builds a hub over the given Host.
func NewUIHub(host *Host) *UIHub {
	return &UIHub{host: host, views: make(map[string]View)}
}

// RegisterView registers a view backed by an existing view-kind capability.
// One view per capability; any violation refuses the registration (§7.3).
func (u *UIHub) RegisterView(v View) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if _, dup := u.views[v.ID]; dup {
		return fmt.Errorf("%w: %s", ErrViewAlreadyRegistered, v.ID)
	}
	cap, ok := u.host.capabilityLocked(v.Capability)
	if !ok || cap.Kind != ViewKind {
		return fmt.Errorf("%w: %s", ErrViewBackingMissing, v.Capability)
	}
	for _, existing := range u.views {
		if existing.Capability == v.Capability {
			return fmt.Errorf("%w: %s already backs %s", ErrViewAlreadyRegistered, v.Capability, existing.ID)
		}
	}
	u.views[v.ID] = v
	return nil
}

// Render renders the view, routing the call through the Host's decider (role,
// backing capability, scope) so the RBAC decision is audited (§7.3).
func (u *UIHub) Render(ctx context.Context, viewID string, role security.Role, scope security.Scope, args any) (string, error) {
	u.mu.Lock()
	v, ok := u.views[viewID]
	u.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrViewNotRegistered, viewID)
	}
	if _, err := u.host.Invoke(ctx, InvokeRequest{CapabilityID: v.Capability, Role: role, Scope: scope, Args: args}); err != nil {
		return "", err
	}
	return v.Render(ctx, args)
}
