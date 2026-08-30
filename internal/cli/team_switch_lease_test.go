package cli

// P0: binding a member must not repoint the ambient session lease at the
// member's file — the member owns its own lease and write authority. Only the
// ambient restore follows the host lease again.

import (
	"path/filepath"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/control"
)

// TestTeamSwitchDoesNotMoveAmbientLease pins the authority/lifetime split:
// while a member is bound the ambient keeper still guards the chat's own
// session file, and leaving the team re-binds it — the member backend's own
// lease is never repointed by the host.
func TestTeamSwitchDoesNotMoveAmbientLease(t *testing.T) {
	ambientPath := filepath.Join(t.TempDir(), "ambient-session.jsonl")
	m := overlayWithBackends(t, nil)
	under, _ := m.ctrl.(*control.Controller)
	if under == nil {
		t.Fatal("overlay ambient must be a real controller for a lease test")
	}
	under.SetSessionPath(ambientPath) // production ambient always has a session file
	leases := control.NewSessionLeaseKeeper()
	defer leases.Release()
	if err := leases.Rebind(ambientPath); err != nil {
		t.Fatal(err)
	}
	m.leases = leases

	// Binding a member must not move the ambient lease off the chat's file.
	if cmd := m.switchTeamMember("lead"); cmd == nil {
		t.Fatal("a successful switch must arm the member event pump")
	}
	if got := leases.HeldPath(); got != agent.CanonicalSessionPath(ambientPath) {
		t.Fatalf("ambient lease after member bind = %q, want the chat's own %q", got, agent.CanonicalSessionPath(ambientPath))
	}
	// Leaving the team restores: the ambient keeper follows the chat backend.
	m.exitTeam()
	if got := leases.HeldPath(); got != agent.CanonicalSessionPath(ambientPath) {
		t.Fatalf("ambient lease after exit = %q, want the chat's own %q restored", got, agent.CanonicalSessionPath(ambientPath))
	}
}
