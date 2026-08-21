package security

import "testing"

func TestStaticDeciderAllowsExactGrant(t *testing.T) {
	d := NewStaticDecider(Grant{
		Role:  Role("leader"),
		CapID: "team.task.assign",
		Scope: ScopeTeam,
	})
	got := d.Decide(Role("leader"), Capability{ID: "team.task.assign"}, ScopeTeam)
	if !got.Allowed {
		t.Fatalf("exact grant must allow, got %+v", got)
	}
	if got.Reason != "granted" {
		t.Fatalf("want reason granted, got %q", got.Reason)
	}
}

func TestStaticDeciderDeniesByDefault(t *testing.T) {
	d := NewStaticDecider(Grant{Role: Role("leader"), CapID: "team.task.assign", Scope: ScopeTeam})
	got := d.Decide(Role("coder"), Capability{ID: "team.task.assign"}, ScopeTeam)
	if got.Allowed {
		t.Fatalf("ungranted role must be denied, got %+v", got)
	}
}

func TestStaticDeciderDeniesWrongScope(t *testing.T) {
	d := NewStaticDecider(Grant{Role: Role("leader"), CapID: "team.task.assign", Scope: ScopeTeam})
	got := d.Decide(Role("leader"), Capability{ID: "team.task.assign"}, ScopeMember)
	if got.Allowed {
		t.Fatalf("wrong scope must be denied, got %+v", got)
	}
}

func TestStaticDeciderDeniesWrongCapability(t *testing.T) {
	d := NewStaticDecider(Grant{Role: Role("leader"), CapID: "team.task.assign", Scope: ScopeTeam})
	got := d.Decide(Role("leader"), Capability{ID: "team.member.remove"}, ScopeTeam)
	if got.Allowed {
		t.Fatalf("wrong capability must be denied, got %+v", got)
	}
}

func TestDecisionRecordsAuditTrail(t *testing.T) {
	d := NewStaticDecider(Grant{Role: Role("reviewer"), CapID: "team.blackboard.read", Scope: ScopeStorage})
	got := d.Decide(Role("reviewer"), Capability{ID: "team.blackboard.read", Kind: CapabilityKindStorageAccess, Version: "1"}, ScopeStorage)
	if !got.Allowed {
		t.Fatalf("want allow, got %+v", got)
	}
	if got.Role != "reviewer" || got.Capability.ID != "team.blackboard.read" || got.Scope != ScopeStorage {
		t.Fatalf("decision must carry the full triple, got %+v", got)
	}
	if got.At.IsZero() {
		t.Fatal("decision must be timestamped")
	}
}
