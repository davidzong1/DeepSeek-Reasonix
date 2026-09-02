package cli

// Leader ambient handoff: a leader's first member session (no session file
// yet) continues from the chat's own conversation, once, in one direction.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// memberTestController is a freshly built member controller the way the builder
// leaves it: its own system prompt in history, no session file, a session dir.
func memberTestController(t *testing.T, sys string) *control.Controller {
	t.Helper()
	exec := agent.New(nil, nil, agent.NewSession(sys), agent.Options{}, event.Discard)
	c := control.New(control.Options{
		Executor: exec, SessionDir: t.TempDir(), Label: "member",
		SystemPrompt: sys, DisableColdResumePrune: true,
	})
	t.Cleanup(c.Close)
	return c
}

func ambientHistory(t *testing.T) []provider.Message {
	t.Helper()
	s := agent.NewSession("ambient-sys")
	s.Add(provider.Message{Role: provider.RoleUser, Content: "ambient user"})
	s.Add(provider.Message{Role: provider.RoleAssistant, Content: "ambient reply"})
	return s.Snapshot()
}

// TestLeaderFirstAssemblyCarriesAmbientHistory pins the seed: a leader with no
// session file yet continues from the chat's own conversation, the member
// identity system prompt stays the head, and the seed is persisted so the
// file's existence — not a flag — stops a second handover.
func TestLeaderFirstAssemblyCarriesAmbientHistory(t *testing.T) {
	ctrl := memberTestController(t, "member-sys")
	path := filepath.Join(ctrl.SessionDir(), "leader.json")
	ambient := func() []provider.Message { return ambientHistory(t) }

	if err := seedMemberAmbient(ctrl, path, true, true, ambient); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := ctrl.SessionPath(); got != path {
		t.Fatalf("SessionPath = %q, want %q", got, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the seed must persist the member session file: %v", err)
	}
	h := ctrl.History()
	if len(h) != 3 {
		t.Fatalf("history = %d messages, want the member identity + ambient talk", len(h))
	}
	if h[0].Role != provider.RoleSystem || h[0].Content != "member-sys" {
		t.Fatalf("head message = %+v, want the member identity system prompt", h[0])
	}
	if h[1].Content != "ambient user" || h[2].Content != "ambient reply" {
		t.Fatalf("ambient conversation must follow the identity: %+v", h)
	}
	// The file now exists: a rebuild treating this member as fresh=false (the
	// reopen contract) must not re-seed and grow the conversation.
	if err := seedMemberAmbient(ctrl, path, false, true, ambient); err != nil {
		t.Fatalf("re-seed attempt: %v", err)
	}
	if got := len(ctrl.History()); got != 3 {
		t.Fatalf("a surviving session must not inherit twice, got %d messages", got)
	}
}

// TestLeaderFirstAssemblySkipsWhenSessionExists pins the persistence gate: a
// leader resuming an existing session (file present → fresh=false) keeps its
// own context; the ambient history never lands in it.
func TestLeaderFirstAssemblySkipsWhenSessionExists(t *testing.T) {
	ctrl := memberTestController(t, "member-sys")
	path := filepath.Join(ctrl.SessionDir(), "leader.json")
	if err := seedMemberAmbient(ctrl, path, false, true, func() []provider.Message {
		return ambientHistory(t)
	}); err != nil {
		t.Fatalf("seed should be a no-op for an existing session: %v", err)
	}
	if len(ctrl.History()) != 1 || ctrl.History()[0].Content != "member-sys" {
		t.Fatalf("a resuming leader must keep only its own history: %+v", ctrl.History())
	}
}

// TestNonLeaderFirstAssemblyDoesNotInherit pins the leader gate: a member that
// is not the team leader starts fresh even on a first, file-less assembly —
// ambient context belongs to the leader's window, never to ordinary members.
func TestNonLeaderFirstAssemblyDoesNotInherit(t *testing.T) {
	ctrl := memberTestController(t, "coder-sys")
	path := filepath.Join(ctrl.SessionDir(), "coder.json")
	if err := seedMemberAmbient(ctrl, path, true, false, func() []provider.Message {
		return ambientHistory(t)
	}); err != nil {
		t.Fatalf("seed should be a no-op for a non-leader: %v", err)
	}
	if len(ctrl.History()) != 1 || ctrl.History()[0].Content != "coder-sys" {
		t.Fatalf("a non-leader must start with only its own identity: %+v", ctrl.History())
	}
}

// TestMemberHandoffPersistFailureReturnsError pins the failure contract: a
// seed that cannot persist must surface as a build error, so the builder
// closes the controller and the registry never registers a backend pretending
// to have carried the leader's context.
func TestMemberHandoffPersistFailureReturnsError(t *testing.T) {
	ctrl := memberTestController(t, "member-sys")
	// A path whose parent is an ordinary file makes the persist mkdir fail.
	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(notDir, "leader.json")
	err := seedMemberAmbient(ctrl, path, true, true, func() []provider.Message {
		return ambientHistory(t)
	})
	if err == nil {
		t.Fatal("a seed that cannot persist must fail")
	}
}

// TestExitTeamRestoresAmbientHistoryReplaysOwnTalk pins the exit contract: a
// leader who leaves the team gets the chat's own backend back, its transcript
// rebuilt from the ambient history — never the leader member's, never a merged
// view. The seed is one-directional: what a member session took in must not
// flow back out.
func TestExitTeamRestoresAmbientHistoryReplaysOwnTalk(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	m.memberEvents = make(chan memberEvent, 8)
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return stubBackend{label: b.MemberID, history: []provider.Message{userMessage("LEAD-HISTORY")}}, nil
	}, 4)
	ambient := stubBackend{
		label:   "chat",
		history: []provider.Message{userMessage("AMB-1"), userMessage("AMB-2")},
	}
	m.ctrl = ambient // the chat's own backend, with history, before any member binds
	// No-frame: status-line rendering reads sub-ports a partial stubBackend
	// does not implement; the transcript commits on bind, not on render.

	m.switchTeamMember("lead") // enter the team on the leader
	if !m.teamSessionBound() {
		t.Fatal("switch to the leader must open the session")
	}
	if got := m.ctrl.Label(); got != "lead" {
		t.Fatalf("bound backend = %q, want the leader", got)
	}

	m.exitTeam()
	if got := m.ctrl.Label(); got != "chat" {
		t.Fatalf("exit must hand the window back to the chat's own backend, got %q", got)
	}
	joined := strings.Join(m.transcript, "\n")
	if !strings.Contains(joined, "AMB-1") || !strings.Contains(joined, "AMB-2") {
		t.Fatalf("exit must replay the ambient history in full:\n%s", joined)
	}
	if strings.Contains(joined, "LEAD-HISTORY") {
		t.Fatalf("a leader session's transcript must not leak into the restored chat:\n%s", joined)
	}
}

// TestLeaderAmbientCarryKeepsMemberIdentityAndAmbientTalk pins the splice:
// the carried transcript leads with the member's own system prompt following
// with the ambient conversation, dropping the ambient system prompt so the
// team agent never speaks as the chat that launched it.
func TestLeaderAmbientCarryKeepsMemberIdentityAndAmbientTalk(t *testing.T) {
	member := []provider.Message{
		{Role: provider.RoleSystem, Content: "member-sys"},
	}
	ambient := []provider.Message{
		{Role: provider.RoleSystem, Content: "ambient-sys"},
		{Role: provider.RoleUser, Content: "hello"},
	}
	carry := leaderAmbientCarry(ambient, member)
	if got := len(carry); got != 2 {
		t.Fatalf("carry = %d messages, want member identity + user turn", got)
	}
	if carry[0].Content != "member-sys" || carry[1].Content != "hello" {
		t.Fatalf("carry must lead with the member identity and drop the ambient system prompt: %+v", carry)
	}
	if leaderAmbientCarry(nil, member) != nil {
		t.Fatal("an empty ambient history must yield an empty carry")
	}
}
