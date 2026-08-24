package team

import (
	"errors"
	"strings"
	"testing"
)

func TestTeamStoreSetTeamAgentType(t *testing.T) {
	ts, _ := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	if err := ts.SetTeamAgentType("alpha", "claude"); err != nil {
		t.Fatal(err)
	}
	if err := ts.SetTeamAgentType("alpha", "codex"); err != nil {
		t.Fatal(err)
	}
	if err := ts.SetTeamAgentType("alpha", " custom-agent "); !errors.Is(err, ErrInvalidAgent) {
		t.Fatalf("surrounding whitespace: err = %v, want ErrInvalidAgent", err)
	}
	if err := ts.SetTeamAgentType("alpha", "bad\x01agent"); !errors.Is(err, ErrInvalidAgent) {
		t.Fatalf("control character: err = %v, want ErrInvalidAgent", err)
	}
	if err := ts.SetTeamAgentType("ghost", "claude"); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("missing team: err = %v, want ErrTeamNotFound", err)
	}
	// Empty clears back to legacy behavior.
	if err := ts.SetTeamAgentType("alpha", ""); err != nil {
		t.Fatal(err)
	}
	doc, _, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Teams[0].AgentType; got != "" {
		t.Fatalf("cleared agent type = %q, want empty", got)
	}
}

func TestTeamStoreSetMemberAgentType(t *testing.T) {
	ts, _ := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	if err := ts.SetMemberAgentType("alpha", "m1", "codex"); err != nil {
		t.Fatal(err)
	}
	if err := ts.SetMemberAgentType("alpha", "m1", "bad\x1fagent"); !errors.Is(err, ErrInvalidAgent) {
		t.Fatalf("control character: err = %v, want ErrInvalidAgent", err)
	}
	if err := ts.SetMemberAgentType("alpha", "ghost", "claude"); !errors.Is(err, ErrMemberNotFound) {
		t.Fatalf("missing member: err = %v, want ErrMemberNotFound", err)
	}
	if err := ts.SetMemberAgentType("ghost", "m1", "claude"); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("missing team: err = %v, want ErrTeamNotFound", err)
	}
	doc, _, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Teams[0].Template[0].AgentType; got != "codex" {
		t.Fatalf("member agent type = %q, want codex", got)
	}
}

// TestTeamStoreSetMemberLeader pins the standalone leader property: set and
// clear persist through the CAS loop, a legacy role-encoded leader keeps
// resolving until the explicit flag is set, and a missing team or member is
// refused.
func TestTeamStoreSetMemberLeader(t *testing.T) {
	ts, _ := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	// Seed a legacy leader encoding so the CAS write path touches it.
	doc, _, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	doc.Teams[0].Template[0].Role = RoleLeader
	if err := ts.Save(doc); err != nil {
		t.Fatal(err)
	}
	if err := ts.SetMemberLeader("alpha", "m1", true); err != nil {
		t.Fatal(err)
	}
	doc, _, err = ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !doc.Teams[0].Template[0].IsLeader() {
		t.Fatalf("m1 should be the leader after SetMemberLeader(true): %+v", doc.Teams[0].Template[0])
	}
	if err := ts.SetMemberLeader("alpha", "m1", false); err != nil {
		t.Fatal(err)
	}
	doc, _, err = ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if doc.Teams[0].Template[0].IsLeader() {
		t.Fatalf("m1 should not be the leader after SetMemberLeader(false): %+v", doc.Teams[0].Template[0])
	}
	if err := ts.SetMemberLeader("alpha", "ghost", true); !errors.Is(err, ErrMemberNotFound) {
		t.Fatalf("missing member: err = %v, want ErrMemberNotFound", err)
	}
	if err := ts.SetMemberLeader("ghost", "m1", true); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("missing team: err = %v, want ErrTeamNotFound", err)
	}
}

func TestTeamStoreBindAgentUser(t *testing.T) {
	// Seed the pool through a store on the same root; NewTeamStore already
	// wired the TeamStore to that pool for binding validation.
	ts, root := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	au, err := NewAgentUsersStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := au.AddAgentUser(AgentUser{UserID: "au-1", Provider: "anthropic", Model: "claude-opus-5"}); err != nil {
		t.Fatal(err)
	}
	if err := ts.BindAgentUser("alpha", "m1", "au-1"); err != nil {
		t.Fatal(err)
	}
	if err := ts.BindAgentUser("alpha", "m1", "ghost"); !errors.Is(err, ErrAgentUserNotFound) {
		t.Fatalf("missing ref: err = %v, want ErrAgentUserNotFound", err)
	}
	if err := ts.BindAgentUser("alpha", "m1", "  "); !errors.Is(err, ErrAgentUserNotFound) {
		t.Fatalf("empty ref: err = %v, want ErrAgentUserNotFound", err)
	}
	if err := ts.BindAgentUser("alpha", "ghost", "au-1"); !errors.Is(err, ErrMemberNotFound) {
		t.Fatalf("missing member: err = %v, want ErrMemberNotFound", err)
	}
	if err := ts.BindAgentUser("ghost", "m1", "au-1"); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("missing team: err = %v, want ErrTeamNotFound", err)
	}
	if err := ts.UnbindAgentUser("alpha", "m1"); err != nil {
		t.Fatal(err)
	}
	doc, _, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Teams[0].Template[0].AgentUserRef; got != "" {
		t.Fatalf("unbound ref = %q, want empty", got)
	}
}

func TestTeamStoreMemberWritePolicy(t *testing.T) {
	ts, _ := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	// Default is open: any caller may create and delete members.
	if err := ts.AddMember("alpha", MemberSlot{MemberID: "m2", Status: MemberStatusActive}); err != nil {
		t.Fatalf("default policy must allow AddMember: %v", err)
	}
	if err := ts.SetMemberWritePolicy(MemberWriteLeaderOnly); err != nil {
		t.Fatal(err)
	}
	if err := ts.AddMember("alpha", MemberSlot{MemberID: "m3", Status: MemberStatusActive}); !errors.Is(err, ErrLeaderOnly) {
		t.Fatalf("leader-only AddMember: err = %v, want ErrLeaderOnly", err)
	}
	if err := ts.DeleteMember("alpha", "m2"); !errors.Is(err, ErrLeaderOnly) {
		t.Fatalf("leader-only DeleteMember: err = %v, want ErrLeaderOnly", err)
	}
	// Non-create/delete writes stay open, and no refused write touched disk.
	if err := ts.SetMemberStatus("alpha", "m1", MemberStatusDisabled); err != nil {
		t.Fatalf("SetMemberStatus must stay open: %v", err)
	}
	if err := ts.AddTeam(Team{Name: "beta"}); err != nil {
		t.Fatalf("AddTeam must stay open: %v", err)
	}
	doc, _, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Teams[0].Template) != 2 {
		t.Fatalf("template size = %d, want 2 (refused adds must not land)", len(doc.Teams[0].Template))
	}
	if err := ts.SetMemberWritePolicy(MemberWritePolicy(99)); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("unknown policy: err = %v, want ErrInvalidPolicy", err)
	}
	if err := ts.SetMemberWritePolicy(MemberWriteOpen); err != nil {
		t.Fatal(err)
	}
	if err := ts.AddMember("alpha", MemberSlot{MemberID: "m3", Status: MemberStatusActive}); err != nil {
		t.Fatalf("re-opened policy must allow AddMember: %v", err)
	}
}

// TestValidateAgentTypeWhitelist pins §7.5: the two known launch types pass,
// empty inherits, and anything else must be one plain command word — a launch
// type must never be able to carry arguments, a path, or shell metacharacters
// into whatever eventually spawns it.
func TestValidateAgentTypeWhitelist(t *testing.T) {
	for _, ok := range []string{"", AgentTypeClaude, AgentTypeCodex, "my-agent", "agent_2", "run.sh"} {
		if err := validateAgentType(ok); err != nil {
			t.Errorf("validateAgentType(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"claude --dangerously-skip-permissions", // an argument list
		"claude;rm -rf /",                       // a command chain
		"/usr/bin/claude",                       // a path
		`claude|tee x`, "claude&", "claude`id`", "claude$(id)", "claude>out",
		" claude", "claude\n", "claude\x00",
		"agent name", // whitespace
		strings.Repeat("a", agentTypeMaxLen+1),
	} {
		if err := validateAgentType(bad); !errors.Is(err, ErrInvalidAgent) {
			t.Errorf("validateAgentType(%q) = %v, want ErrInvalidAgent", bad, err)
		}
	}
}

// TestSetAgentTypeRefusesUnsafeValues pins the whitelist at the store boundary:
// both setters go through it, so a dangerous value never reaches team.json.
func TestSetAgentTypeRefusesUnsafeValues(t *testing.T) {
	s, _ := newTeamStore(t)
	if err := s.AddTeam(Team{Name: "alpha", Template: []MemberSlot{
		{MemberID: "lead", Status: MemberStatusActive},
	}}); err != nil {
		t.Fatal(err)
	}
	const bad = "claude; rm -rf /"
	if err := s.SetTeamAgentType("alpha", bad); !errors.Is(err, ErrInvalidAgent) {
		t.Errorf("SetTeamAgentType = %v, want ErrInvalidAgent", err)
	}
	if err := s.SetMemberAgentType("alpha", "lead", bad); !errors.Is(err, ErrInvalidAgent) {
		t.Errorf("SetMemberAgentType = %v, want ErrInvalidAgent", err)
	}
	doc, _, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Teams[0].AgentType; got != "" {
		t.Errorf("a refused team launch type must not persist, got %q", got)
	}
	if got := doc.Teams[0].Template[0].AgentType; got != "" {
		t.Errorf("a refused member launch type must not persist, got %q", got)
	}
	if err := s.SetMemberAgentType("alpha", "lead", AgentTypeCodex); err != nil {
		t.Fatalf("a whitelisted type must persist: %v", err)
	}
}
