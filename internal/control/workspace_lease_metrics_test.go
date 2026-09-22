package control

import (
	"strings"
	"testing"
)

// A controller with no lease is the shape a host sees before a session is
// booted, and it is also the shape the diagnostics export runs against when a
// session never attached a workspace lease. Both accessors must stay quiet
// instead of panicking or claiming a wait that never happened.
func TestWorkspaceLeaseMetricsSurfaceWithoutALease(t *testing.T) {
	var missing *Controller
	if got := missing.WorkspaceLeaseMetrics(); got.Waits != 0 || got.InFlight != 0 || len(got.Scopes) != 0 {
		t.Fatalf("a missing controller reported waits: %+v", got)
	}
	if got := missing.WorkspaceLeaseDiagnostics(); got != "workspace lease: unavailable" {
		t.Fatalf("diagnostics for a missing controller = %q", got)
	}

	controller := &Controller{}
	if got := controller.WorkspaceLeaseMetrics(); got.Waits != 0 || got.Timeouts != 0 || len(got.Locks) != 0 {
		t.Fatalf("controller without a lease reported waits: %+v", got)
	}
	block := controller.WorkspaceLeaseDiagnostics()
	if block != "workspace lease: unavailable" {
		t.Fatalf("diagnostics without a lease = %q", block)
	}
	if !strings.Contains(block, "workspace lease") {
		t.Fatalf("diagnostics without a lease lost its subject: %q", block)
	}
}
