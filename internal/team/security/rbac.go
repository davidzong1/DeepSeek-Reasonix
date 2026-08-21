package security

import (
	"time"

	"reasonix/internal/team"
)

// Scope is the RBAC resource dimension (TASK.md §2.4) — the root package's
// type: one Scope, one constant set, one wire format.
type Scope = team.Scope

// Scope constants re-export the root package's five resource classes so this
// package's callers need not import the team root for values.
const (
	ScopeTeam      = team.ScopeTeam
	ScopeMember    = team.ScopeMember
	ScopeAgentUser = team.ScopeAgentUser
	ScopeStorage   = team.ScopeStorage
	ScopePlugin    = team.ScopePlugin
)

// CapabilityKind classifies what a capability does (TASK.md §7.2). It is a
// plain string — the root Capability.Kind carries no distinct type — kept
// here as a typed constant set for builders.
type CapabilityKind = string

const (
	CapabilityKindTool          CapabilityKind = "tool"
	CapabilityKindEvent         CapabilityKind = "event"
	CapabilityKindView          CapabilityKind = "view"
	CapabilityKindStorageAccess CapabilityKind = "storage-access"
)

// Capability is the auditable resource label (TASK.md §7.2) — the root
// package's type; this package adds no second shape.
type Capability = team.Capability

// Role is the RBAC subject (TASK.md §2.3) — the root package's RoleID.
type Role = team.RoleID

// Decision is the auditable outcome of one RBAC check. It records the triple,
// the verdict, and a reason; it never carries credential content (K3).
type Decision struct {
	Role       Role       `json:"role"`
	Capability Capability `json:"capability"`
	Scope      Scope      `json:"scope"`
	Allowed    bool       `json:"allowed"`
	Reason     string     `json:"reason,omitempty"`
	At         time.Time  `json:"at"`
}

// Decider is the centralized RBAC decision point (TASK.md §7.4): every
// capability invocation passes through it; plugins must not bypass it to
// reach restricted resources.
type Decider interface {
	Decide(role Role, cap Capability, scope Scope) Decision
}

// Grant grants a capability to a role within a scope. The static decider is
// deny-by-default: only explicit grants allow. The grant triple is
// (role, capability id, scope) per TASK.md §2.4; capability version is a
// declaration attribute, not an authorization dimension.
type Grant struct {
	Role  Role
	CapID team.CapabilityID
	Scope Scope
}

// StaticDecider evaluates (role, capability, scope) against a fixed grant
// table. It is the P2 minimal decider; the plugin Host (P5) composes it with
// per-plugin declarations and an auditor.
type StaticDecider struct {
	grants map[Grant]struct{}
}

// NewStaticDecider builds a decider from the given grants; duplicate grants
// collapse to one.
func NewStaticDecider(grants ...Grant) *StaticDecider {
	m := make(map[Grant]struct{}, len(grants))
	for _, g := range grants {
		m[g] = struct{}{}
	}
	return &StaticDecider{grants: m}
}

// Decide returns allow only when the exact (role, capability, scope) triple
// was granted.
func (d *StaticDecider) Decide(role Role, cap Capability, scope Scope) Decision {
	allowed := false
	reason := "not granted"
	if _, ok := d.grants[Grant{Role: role, CapID: cap.ID, Scope: scope}]; ok {
		allowed = true
		reason = "granted"
	}
	return Decision{
		Role:       role,
		Capability: cap,
		Scope:      scope,
		Allowed:    allowed,
		Reason:     reason,
		At:         time.Now(),
	}
}
