package team

// Capability is a declared capability (§7.2): id, kind, scope, version. Kinds
// are tool, event, view, storage; ids are globally unique and mutually
// exclusive within a semantic domain.
type Capability struct {
	ID      CapabilityID
	Kind    string // tool | event | view | storage
	Scope   Scope
	Version string
}

// Decision is an RBAC judgement with an audit trail (§7.4): whether access
// was granted and why, so every denial is explainable and replayable.
type Decision struct {
	Allow  bool
	Reason string
}

// Authorizer is the minimal RBAC judgement surface (§2.4, §7.4). The policy
// engine is implemented in the data-and-security layer; the plugin host
// enforces this call at the capability boundary.
type Authorizer interface {
	Authorize(role RoleID, capability CapabilityID, scope Scope) (Decision, error)
}
