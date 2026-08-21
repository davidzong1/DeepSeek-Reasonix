package plugin

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"reasonix/internal/team"
	"reasonix/internal/team/security"
)

var (
	// ErrPluginExists reports a duplicate plugin registration.
	ErrPluginExists = errors.New("plugin: already registered")
	// ErrPluginNotRegistered reports an unregister of an unknown plugin.
	ErrPluginNotRegistered = errors.New("plugin: not registered")
	// ErrCapabilityConflict reports a capability id or semantic-domain clash;
	// the whole registration fails, never half of it (§7.2).
	ErrCapabilityConflict = errors.New("plugin: capability id or semantic domain already claimed")
	// ErrDeclarationMismatch reports declared capabilities != implemented
	// handlers (§7.1).
	ErrDeclarationMismatch = errors.New("plugin: declared capabilities do not match handlers")
	// ErrCapabilityNotRegistered reports an invocation of an unknown capability.
	ErrCapabilityNotRegistered = errors.New("plugin: capability not registered")
	// ErrDenied reports an RBAC denial at the capability boundary (§7.4).
	ErrDenied = errors.New("plugin: rbac denied")
	// ErrCredentialDenied reports a credential-scope violation (§7.5).
	ErrCredentialDenied = errors.New("plugin: credential scope denied")
)

// Handler is a plugin's callable unit (§7.1). It receives arguments and
// credential references only — never key material.
type Handler func(ctx context.Context, args any) (any, error)

// Plugin is one registration unit (§7.1): the declared capabilities, their
// handlers, and the plugin's credential scope.
type Plugin struct {
	ID           string
	Credential   team.CredentialScope // declared scope; drives §7.5 enforcement
	Capabilities []team.Capability
	Handlers     map[team.CapabilityID]Handler
}

// Host owns plugin registration, lifecycle, and capability routing (§7.1):
// every invocation passes through the security.Decider and the decision is
// recorded. There is no second path into plugin code.
type Host struct {
	mu          sync.Mutex
	decider     security.Decider
	teamDefault *team.SecretRef // team-scope credential admission (§7.5)
	plugins     map[string]Plugin
	handlers    map[team.CapabilityID]Handler
	caps        map[team.CapabilityID]team.Capability
	owners      map[team.CapabilityID]string // capability -> plugin id
	domains     map[string]string            // semantic domain -> plugin id
	audit       []AuditEntry
}

// NewHost builds a Host around the centralized decider. teamDefault is the
// team-default credential reference admitted to team-scope plugins; nil
// admits none.
func NewHost(decider security.Decider, teamDefault *team.SecretRef) *Host {
	return &Host{
		decider:     decider,
		teamDefault: teamDefault,
		plugins:     make(map[string]Plugin),
		handlers:    make(map[team.CapabilityID]Handler),
		caps:        make(map[team.CapabilityID]team.Capability),
		owners:      make(map[team.CapabilityID]string),
		domains:     make(map[string]string),
	}
}

// Register validates and installs a plugin atomically: a capability-id or
// semantic-domain conflict, or a declared/implemented mismatch, fails the
// whole registration (§7.1, §7.2).
func (h *Host) Register(p Plugin) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p.ID == "" {
		return fmt.Errorf("plugin: empty plugin id")
	}
	if _, dup := h.plugins[p.ID]; dup {
		return fmt.Errorf("%w: %s", ErrPluginExists, p.ID)
	}
	if err := matchDeclaration(p); err != nil {
		return err
	}
	for _, cap := range p.Capabilities {
		if _, dup := h.caps[cap.ID]; dup {
			return fmt.Errorf("%w: %s", ErrCapabilityConflict, cap.ID)
		}
		d := domainOf(cap.ID)
		if owner, taken := h.domains[d]; taken && owner != p.ID {
			return fmt.Errorf("%w: domain %s belongs to %s", ErrCapabilityConflict, d, owner)
		}
	}
	handlers := make(map[team.CapabilityID]Handler, len(p.Handlers))
	maps.Copy(handlers, p.Handlers)
	h.plugins[p.ID] = p
	for _, cap := range p.Capabilities {
		h.caps[cap.ID] = cap
		h.handlers[cap.ID] = handlers[cap.ID]
		h.owners[cap.ID] = p.ID
		h.domains[domainOf(cap.ID)] = p.ID
	}
	return nil
}

// Unregister removes a plugin and all of its capabilities; later invocations
// of those capabilities fail with ErrCapabilityNotRegistered.
func (h *Host) Unregister(pluginID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	p, ok := h.plugins[pluginID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrPluginNotRegistered, pluginID)
	}
	delete(h.plugins, pluginID)
	for _, cap := range p.Capabilities {
		delete(h.caps, cap.ID)
		delete(h.handlers, cap.ID)
		delete(h.owners, cap.ID)
		delete(h.domains, domainOf(cap.ID))
	}
	return nil
}

// InvokeRequest is one capability call: the caller's role, the RBAC resource
// scope (default ScopePlugin), arguments, and an optional credential
// reference the plugin asks to use.
type InvokeRequest struct {
	CapabilityID team.CapabilityID
	Role         security.Role
	Scope        security.Scope
	Args         any
	Credential   *team.SecretRef
}

// Invoke routes one call through the decider: deny-by-default, the decision
// is audited, and the handler runs only on allow (§7.4). Credential
// references are admitted only within the plugin's declared scope (§7.5).
func (h *Host) Invoke(ctx context.Context, req InvokeRequest) (any, error) {
	scope := req.Scope
	if scope == "" {
		scope = security.ScopePlugin
	}
	h.mu.Lock()
	cap, registered := h.caps[req.CapabilityID]
	handler := h.handlers[req.CapabilityID]
	pluginID := h.owners[req.CapabilityID]
	var err error
	if registered {
		d := h.decider.Decide(req.Role, cap, scope)
		h.audit = append(h.audit, AuditEntry{
			At:           time.Now(),
			PluginID:     pluginID,
			CapabilityID: req.CapabilityID,
			Role:         req.Role,
			Scope:        scope,
			Allowed:      d.Allowed,
			Reason:       d.Reason,
		})
		if !d.Allowed {
			err = fmt.Errorf("%w: %s: %s", ErrDenied, req.CapabilityID, d.Reason)
		} else {
			err = h.checkCredentialLocked(pluginID, req)
		}
	}
	h.mu.Unlock()
	if !registered {
		return nil, fmt.Errorf("%w: %s", ErrCapabilityNotRegistered, req.CapabilityID)
	}
	if err != nil {
		return nil, err
	}
	return handler(ctx, req.Args)
}

// Audit returns a copy of the recorded RBAC decisions, oldest first (§7.4).
func (h *Host) Audit() []AuditEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]AuditEntry, len(h.audit))
	copy(out, h.audit)
	return out
}

// capabilityLocked returns the registered capability; caller holds h.mu.
func (h *Host) capabilityLocked(id team.CapabilityID) (team.Capability, bool) {
	c, ok := h.caps[id]
	return c, ok
}

// AuditEntry is one recorded RBAC decision at the capability boundary (§7.4).
type AuditEntry struct {
	At           time.Time
	PluginID     string
	CapabilityID team.CapabilityID
	Role         security.Role
	Scope        security.Scope
	Allowed      bool
	Reason       string
}

// checkCredentialLocked admits a reference only within the plugin's declared
// scope: none admits none; agent-user admits only references explicitly
// granted by RBAC; team admits only the team-default reference (§7.5).
// Caller holds h.mu.
func (h *Host) checkCredentialLocked(pluginID string, req InvokeRequest) error {
	if req.Credential == nil {
		return nil
	}
	ref := *req.Credential
	switch h.plugins[pluginID].Credential {
	case team.CredentialScopeNone:
		return fmt.Errorf("%w: plugin %s declares no credential access", ErrCredentialDenied, pluginID)
	case team.CredentialScopeAgentUser:
		if ref.Scope != team.CredentialScopeAgentUser {
			return fmt.Errorf("%w: %s: agent-user admits only agent-user references", ErrCredentialDenied, pluginID)
		}
		if !h.decider.Decide(req.Role, h.caps[req.CapabilityID], security.ScopePlugin).Allowed {
			return fmt.Errorf("%w: %s: agent-user reference not explicitly granted", ErrCredentialDenied, pluginID)
		}
		return nil
	case team.CredentialScopeTeam:
		if h.teamDefault == nil || ref != *h.teamDefault {
			return fmt.Errorf("%w: %s: only the team-default reference is admitted", ErrCredentialDenied, pluginID)
		}
		return nil
	}
	return fmt.Errorf("%w: plugin %s declares unknown scope %q", ErrCredentialDenied, pluginID, h.plugins[pluginID].Credential)
}

// matchDeclaration verifies the declared capability set equals the handler
// keys exactly (§7.1).
func matchDeclaration(p Plugin) error {
	declared := make(map[team.CapabilityID]bool, len(p.Capabilities))
	for _, c := range p.Capabilities {
		declared[c.ID] = true
	}
	if len(p.Capabilities) != len(declared) || len(declared) != len(p.Handlers) {
		return fmt.Errorf("%w: %s declares %d capabilities, implements %d", ErrDeclarationMismatch, p.ID, len(p.Capabilities), len(p.Handlers))
	}
	for id := range p.Handlers {
		if !declared[id] {
			return fmt.Errorf("%w: %s implements undeclared capability %s", ErrDeclarationMismatch, p.ID, id)
		}
	}
	return nil
}

// domainOf returns the semantic domain of a capability id: the prefix before
// the first ".", or the whole id when there is none (§7.2).
func domainOf(id team.CapabilityID) string {
	domain, _, _ := strings.Cut(string(id), ".")
	return domain
}
