package tui

import (
	"testing"

	"reasonix/internal/team"
)

// leaderMember is a leader-flagged roster member for pinning tests.
func leaderMember(id string, st team.MemberState) team.Member {
	m := member(id, st)
	m.Leader = true
	return m
}

// TestNewPinsLeadersAboveState pins the leaders-first rule (§11.4): a leader
// outranks every non-leader regardless of state, so a working leader sits
// above an approval non-leader.
func TestNewPinsLeadersAboveState(t *testing.T) {
	m := New(oneTeam(
		member("approval", team.MemberStateApproval),
		leaderMember("working", team.MemberStateWorking),
	))
	got := m.Members()
	if got[0].ID != "working" || got[1].ID != "approval" {
		t.Fatalf("a leader must outrank state priority, got %q,%q", got[0].ID, got[1].ID)
	}
}

// TestNewOrdersLeadersByStateThenID pins leader-to-leader order: leaders keep
// the state-priority tie-break, so the roster's head is deterministic even
// with several leaders.
func TestNewOrdersLeadersByStateThenID(t *testing.T) {
	m := New(oneTeam(
		leaderMember("l1", team.MemberStateWorking),
		leaderMember("l2", team.MemberStateApproval),
		member("a", team.MemberStateIdle),
	))
	got := m.Members()
	want := []string{"l2", "l1", "a"}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("order[%d]=%q, want %q", i, got[i].ID, id)
		}
	}
	if got := got[0].State; got != team.MemberStateApproval {
		t.Fatalf("the approval leader should head the roster, got state %q", got)
	}
}

// TestNewLeaderTieBreakByID pins equal-state leaders: the ID tie-break keeps
// the pinned group stable instead of a last-write-wins order.
func TestNewLeaderTieBreakByID(t *testing.T) {
	m := New(oneTeam(
		leaderMember("z", team.MemberStateIdle),
		leaderMember("a", team.MemberStateIdle),
	))
	got := m.Members()
	if got[0].ID != "a" || got[1].ID != "z" {
		t.Fatalf("equal-state leaders must sort by ID, got %q,%q", got[0].ID, got[1].ID)
	}
}

// TestNewLeaderPinDoesNotReorderRanks pins the non-leader sub-order: inserting
// leaders on top leaves the state-priority order of the rest untouched.
func TestNewLeaderPinDoesNotReorderRanks(t *testing.T) {
	in := []team.Member{
		leaderMember("lead", team.MemberStateIdle),
		member("a", team.MemberStateApproval),
		member("z", team.MemberStateDead),
		member("m", team.MemberStateIdle),
	}
	m := New(oneTeam(in...))
	got := m.Members()
	want := []string{"lead", "a", "z", "m"}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("order[%d]=%q, want %q", i, got[i].ID, id)
		}
	}
}

// TestFocusMemberPinsByIdentity pins the focus helper the session entry uses:
// focus follows member id across the pinned order, and an unknown id leaves
// focus unchanged.
func TestFocusMemberPinsByIdentity(t *testing.T) {
	m := New(oneTeam(
		leaderMember("lead", team.MemberStateIdle),
		member("alice", team.MemberStateIdle),
	))
	if !m.FocusMember("alice") || m.FocusIndex() != 1 {
		t.Fatalf("FocusMember(alice) should focus index 1, got %d", m.FocusIndex())
	}
	if m.FocusMember("ghost") {
		t.Fatal("FocusMember must report an unknown member")
	}
	if f, _ := m.Focused(); f.ID != "alice" {
		t.Fatalf("an unknown focus must leave the member unchanged, got %q", f.ID)
	}
}

// TestReloadKeepsFocusWhenLeaderOrderShifts pins the reload contract under
// pinning: a re-sorted roster (a leader appears above the focused member)
// still restores the focused member by identity, never by stale index.
func TestReloadKeepsFocusWhenLeaderOrderShifts(t *testing.T) {
	m := New(oneTeam(member("alice", team.MemberStateIdle)))
	m.Handle(EventSelect) // open the roster on alice
	m.FocusMember("alice")
	m.Reload(oneTeam(
		leaderMember("lead", team.MemberStateIdle),
		member("alice", team.MemberStateIdle),
	))
	if f, ok := m.Focused(); !ok || f.ID != "alice" {
		t.Fatalf("reload must keep focus on alice past the pinned leader, got %q", f.ID)
	}
	if got := m.Members()[0].ID; got != "lead" {
		t.Fatalf("the leader must pin on top after reload, got %q", got)
	}
}
