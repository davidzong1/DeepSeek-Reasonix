package team

import (
	"errors"
	"testing"
)

func TestMemberSessionFileRefusesEscapingKeys(t *testing.T) {
	for _, tc := range []struct{ team, member string }{
		{"", "m"}, {"t", ""}, {"a/b", "m"}, {"t", "m/../x"}, {"t", ".."}, {"t", "."},
	} {
		if _, err := MemberSessionFile(tc.team, tc.member); !errors.Is(err, ErrInvalidSessionKey) {
			t.Errorf("MemberSessionFile(%q,%q) err = %v, want ErrInvalidSessionKey", tc.team, tc.member, err)
		}
	}
	got, err := MemberSessionFile("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if got != "team-alpha-lead.json" {
		t.Errorf("session file = %q", got)
	}
}

// TestBindingsResolveTeamDefaults pins the one fallback callers must not
// reimplement: a member with no AgentUserRef or AgentType inherits the team's
// default, and an overriding member keeps its own.
func TestBindingsResolveTeamDefaults(t *testing.T) {
	s, _ := newTeamStore(t)
	if err := s.AddTeam(Team{
		Name:                "alpha",
		DefaultAgentUserRef: "pool-default",
		AgentType:           "claude",
		Template: []MemberSlot{
			{MemberID: "lead", Role: RoleCoder, Leader: true, Status: MemberStatusActive},
			{MemberID: "alice", Role: RoleTester, Status: MemberStatusActive,
				AgentUserRef: "pool-own", AgentType: "codex"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Bindings("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("bindings = %d, want 2", len(got))
	}
	if got[0].AgentUserRef != "pool-default" || got[0].AgentType != "claude" {
		t.Errorf("unbound member should inherit the team default, got %+v", got[0])
	}
	if !got[0].Leader {
		t.Error("the leader slot must report Leader")
	}
	if got[0].SessionFile != "team-alpha-lead.json" {
		t.Errorf("session file = %q", got[0].SessionFile)
	}
	if got[1].AgentUserRef != "pool-own" || got[1].AgentType != "codex" {
		t.Errorf("an overriding member must keep its own, got %+v", got[1])
	}
	if got[1].Leader {
		t.Error("a non-leader slot must not report Leader")
	}

	one, err := s.Binding("alpha", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if one.MemberID != "alice" || one.Role != RoleTester {
		t.Errorf("Binding = %+v", one)
	}
	if _, err := s.Binding("alpha", "nobody"); !errors.Is(err, ErrMemberNotFound) {
		t.Errorf("unknown member err = %v, want ErrMemberNotFound", err)
	}
	if _, err := s.Bindings("nope"); !errors.Is(err, ErrTeamNotFound) {
		t.Errorf("unknown team err = %v, want ErrTeamNotFound", err)
	}
}
