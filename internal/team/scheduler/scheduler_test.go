package scheduler

import (
	"errors"
	"strings"
	"testing"

	"reasonix/internal/team"
)

func idleMember(id string, role team.RoleID) team.Member {
	return team.Member{ID: id, Role: role, State: team.MemberStateIdle}
}

func TestAssignRecordsPendingOnly(t *testing.T) {
	// Placeholder semantics (§3.7): the assignment is a ledger entry —
	// Status pending, Note opens with ErrRuntimePending, execution never
	// claimed.
	s := NewPlaceholderScheduler()
	a, err := s.Assign(team.Task{ID: "t1", RequireRole: team.RoleCoder}, []team.Member{idleMember("m1", team.RoleCoder)})
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != StatusPending {
		t.Fatalf("status = %s, want pending", a.Status)
	}
	if !strings.Contains(a.Note, ErrRuntimePending.Error()) || !strings.Contains(a.Note, "[runtime-pending]") {
		t.Fatalf("note %q must open with %q", a.Note, ErrRuntimePending)
	}
}

func TestAssignRoleMatch(t *testing.T) {
	s := NewPlaceholderScheduler()
	fleet := []team.Member{idleMember("coder", team.RoleCoder), idleMember("tester", team.RoleTester)}
	a, err := s.Assign(team.Task{ID: "t1", RequireRole: team.RoleCoder}, fleet)
	if err != nil {
		t.Fatal(err)
	}
	if a.MemberID != "coder" {
		t.Fatalf("member = %s, want coder", a.MemberID)
	}
}

func TestAssignEmptyRequireRoleAcceptsAny(t *testing.T) {
	s := NewPlaceholderScheduler()
	a, err := s.Assign(team.Task{ID: "t1"}, []team.Member{idleMember("tester", team.RoleTester)})
	if err != nil {
		t.Fatal(err)
	}
	if a.MemberID != "tester" {
		t.Fatalf("member = %s, want tester", a.MemberID)
	}
}

func TestAssignPriorTaskAffinity(t *testing.T) {
	s := NewPlaceholderScheduler()
	fleet := []team.Member{idleMember("a", team.RoleCoder), idleMember("b", team.RoleCoder)}
	a, err := s.Assign(team.Task{ID: "t1", RequireRole: team.RoleCoder, AssignedMember: "b"}, fleet)
	if err != nil {
		t.Fatal(err)
	}
	if a.MemberID != "b" {
		t.Fatalf("member = %s, want b (affinity)", a.MemberID)
	}
}

func TestAssignLoadBalancePrefersIdle(t *testing.T) {
	busy := idleMember("a", team.RoleCoder)
	busy.State = team.MemberStateWorking
	busy.TaskRef = "other"
	s := NewPlaceholderScheduler()
	a, err := s.Assign(team.Task{ID: "t1", RequireRole: team.RoleCoder}, []team.Member{busy, idleMember("b", team.RoleCoder)})
	if err != nil {
		t.Fatal(err)
	}
	if a.MemberID != "b" {
		t.Fatalf("member = %s, want idle b", a.MemberID)
	}
}

func TestAssignNoRoleMatch(t *testing.T) {
	s := NewPlaceholderScheduler()
	_, err := s.Assign(team.Task{ID: "t1", RequireRole: team.RoleTester}, []team.Member{idleMember("c", team.RoleCoder)})
	if !errors.Is(err, ErrNoSuitableMember) {
		t.Fatalf("err = %v, want ErrNoSuitableMember", err)
	}
}

func TestAssignHasNoSideEffects(t *testing.T) {
	// Accounting only: fleet and task are read-only inputs (test ③).
	s := NewPlaceholderScheduler()
	fleet := []team.Member{idleMember("m", team.RoleCoder)}
	before := fleet[0]
	task := team.Task{ID: "t1", RequireRole: team.RoleCoder}
	beforeTask := task
	if _, err := s.Assign(task, fleet); err != nil {
		t.Fatal(err)
	}
	if fleet[0] != before {
		t.Fatal("Assign mutated the fleet")
	}
	if task != beforeTask {
		t.Fatal("Assign mutated the task")
	}
}
