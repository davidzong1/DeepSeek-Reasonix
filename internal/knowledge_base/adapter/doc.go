// Package adapter is the narrow seam between the host (agent/team runtime) and
// the knowledge-base core. The core depends only on these interfaces, never on
// a host package; hosts implement them and assert conformance with a
// compile-time `var _ adapter.Adapter = (*HostImpl)(nil)`.
package adapter
