package team

import (
	"errors"
	"testing"
)

func TestLeaderMemberManagementRequiresLeader(t *testing.T) {
	ts, _ := newTeamStore(t)
	if err := ts.Save(TeamDoc{Document: Document{SchemaVersion: SchemaVersion}, Teams: []Team{{
		Name: "alpha", Template: []MemberSlot{{MemberID: "lead", Leader: true}, {MemberID: "m1", Role: RoleCoder}},
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := ts.LeaderAddMember("alpha", "m1", MemberSlot{MemberID: "m2", Role: RoleTester}); err == nil {
		t.Fatal("non-leader add should be refused")
	}
	if err := ts.LeaderAddMember("alpha", "lead", MemberSlot{MemberID: "m2", Role: RoleTester}); err != nil {
		t.Fatal(err)
	}
	if err := ts.LeaderSetMemberRole("alpha", "lead", "m2", RoleReviewer); err != nil {
		t.Fatal(err)
	}
	if err := ts.LeaderRemoveMember("alpha", "lead", "m2"); err != nil {
		t.Fatal(err)
	}
	if err := ts.LeaderSetMemberRole("alpha", "lead", "missing", RoleCoder); !errors.Is(err, ErrMemberNotFound) {
		t.Fatalf("missing member role update = %v", err)
	}
}
