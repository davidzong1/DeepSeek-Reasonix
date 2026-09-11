package cli

import (
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/sandbox"
	"reasonix/internal/team"
)

type escalationResolution struct {
	member, id string
	allow      bool
	scope      sandbox.ApprovalScope
}

// escalationBackend records what the decider asked of it. Value receivers mean
// the registry hands out a copy per bind; the shared slices aggregate.
type escalationBackend struct {
	control.SessionAPI
	member   string
	mu       *sync.Mutex
	resolved *[]escalationResolution
	turns    *[]string
}

func (b escalationBackend) ResolveApproval(id string, allow bool, scope sandbox.ApprovalScope) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	*b.resolved = append(*b.resolved, escalationResolution{member: b.member, id: id, allow: allow, scope: scope})
	return nil
}

func (b escalationBackend) SubmitUserTurnOrError(input, display string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	*b.turns = append(*b.turns, input)
	return nil
}

// escalationRig wires a real store and registry behind the service, the same
// trio the window assembles. withLeader false models a team nobody can escalate
// to.
func escalationRig(t *testing.T, withLeader bool) (*writeAccessEscalations, *[]escalationResolution, *[]string, *team.TeamStore) {
	t.Helper()
	store, err := team.NewTeamStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	slots := []team.MemberSlot{{MemberID: "coder", Role: team.RoleCoder, Status: team.MemberStatusActive}}
	if withLeader {
		slots = append([]team.MemberSlot{{MemberID: "lead", Leader: true, Status: team.MemberStatusActive, Role: team.RoleCoder}}, slots...)
	}
	if err := store.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{
		Name: "alpha", Template: slots,
	}}}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	resolved, turns := new([]escalationResolution), new([]string)
	backends := newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return escalationBackend{member: b.MemberID, mu: &mu, resolved: resolved, turns: turns}, nil
	}, 4)
	svc := newWriteAccessEscalations(store)
	svc.setHub(newTeamHub(store, backends, "alpha"))
	// The member is assembled because it is mid-turn — that is what raised the
	// prompt. A decider answers a live backend; assembling one is refused.
	binding, err := store.Binding("alpha", "coder")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backends.bind(binding); err != nil {
		t.Fatal(err)
	}
	return svc, resolved, turns, store
}

func escalationFor(member, approvalID, dir string) control.WriteAccessEscalation {
	return control.WriteAccessEscalation{
		ApprovalID: approvalID, Tool: "write_file", Subject: dir,
		Directories: []string{dir}, Justification: "write the generated dataset",
	}
}

func TestLeaderApprovalToolsAreLeaderOnly(t *testing.T) {
	svc, _, _, _ := escalationRig(t, true)
	if got := newLeaderApprovalTools(svc, "alpha", "lead", true); len(got) != 2 {
		t.Errorf("leader tools = %d, want the list and resolve pair", len(got))
	}
	if got := newLeaderApprovalTools(svc, "alpha", "coder", false); got != nil {
		t.Error("a member must not receive the approval surface: it would answer its own escalation")
	}
	if got := newLeaderApprovalTools(nil, "alpha", "lead", true); got != nil {
		t.Error("no service means no tool")
	}
}

func TestEscalationResolvesThroughTheHubAndAudits(t *testing.T) {
	svc, resolved, _, store := escalationRig(t, true)
	release := svc.begin("alpha", "coder", escalationFor("coder", "3", "/srv/data"))
	if release == nil {
		t.Fatal("a team with a leader must queue the request")
	}
	pending := svc.list("alpha")
	if len(pending) != 1 || pending[0].RequestID != "coder:3" {
		t.Fatalf("pending = %+v, want one entry keyed by member:approval", pending)
	}

	out, err := svc.resolve("alpha", "coder:3", true, sandbox.ApprovalScopeSession)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "allowed") || !strings.Contains(out, "coder:3") {
		t.Errorf("resolve reply = %q", out)
	}
	got := *resolved
	if len(got) != 1 || got[0].member != "coder" || got[0].id != "3" || !got[0].allow {
		t.Fatalf("resolved = %+v, want coder's own prompt answered allowed", got)
	}
	if got[0].scope != sandbox.ApprovalScopeSession {
		t.Errorf("scope = %q, want session", got[0].scope)
	}
	if entries := svc.list("alpha"); len(entries) != 0 {
		t.Errorf("a decided request must leave the queue, got %+v", entries)
	}

	rows, err := store.AuthzEntries("alpha", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("ledger rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Source != team.AuthzSourceLeaderAgent || row.Kind != team.AuthzKindWriteAccess {
		t.Errorf("row = %+v, want a distinguishable leader_agent write-access decision", row)
	}
	if row.RequestID != "coder:3" || row.Scope != "session" || len(row.Dirs) != 1 {
		t.Errorf("row detail = %+v", row)
	}
}

func TestEscalationReleaseRetiresTheRequest(t *testing.T) {
	svc, resolved, _, store := escalationRig(t, true)
	release := svc.begin("alpha", "coder", escalationFor("coder", "3", "/srv/data"))
	// Any other route settling the prompt runs this; the leader must not then be
	// handed a decision nobody is waiting on.
	release()
	if entries := svc.list("alpha"); len(entries) != 0 {
		t.Fatalf("released request still queued: %+v", entries)
	}
	if _, err := svc.resolve("alpha", "coder:3", true, sandbox.ApprovalScopeOnce); err == nil {
		t.Error("resolving a released request must fail rather than audit a decision that never happened")
	}
	if len(*resolved) != 0 {
		t.Error("nothing should have reached the member backend")
	}
	rows, err := store.AuthzEntries("alpha", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("a request settled elsewhere must leave no leader_agent row, got %+v", rows)
	}
}

func TestEscalationRefusesProjectScopeAndUnknownRequests(t *testing.T) {
	svc, resolved, _, _ := escalationRig(t, true)
	svc.begin("alpha", "coder", escalationFor("coder", "3", "/srv/data"))

	if _, err := svc.resolve("alpha", "coder:3", true, sandbox.ApprovalScopeProject); err == nil {
		t.Error("project scope writes the workspace config; it must stay on the operator's card")
	}
	if entries := svc.list("alpha"); len(entries) != 1 {
		t.Error("a refused scope must leave the request pending")
	}
	if _, err := svc.resolve("alpha", "coder:99", true, sandbox.ApprovalScopeOnce); err == nil {
		t.Error("an unknown request id must be refused")
	}
	if len(*resolved) != 0 {
		t.Errorf("no refusal may reach the member backend, got %+v", *resolved)
	}
}

func TestEscalationSkipsATeamWithoutALeader(t *testing.T) {
	svc, _, turns, _ := escalationRig(t, false)
	if release := svc.begin("alpha", "coder", escalationFor("coder", "3", "/srv/data")); release != nil {
		t.Error("with nobody to ask, the operator's card must stay the only decider")
	}
	if entries := svc.list("alpha"); len(entries) != 0 {
		t.Errorf("nothing may be queued for a team with no leader, got %+v", entries)
	}
	if len(*turns) != 0 {
		t.Error("no turn may be driven on a member when there is no leader to wake")
	}
}

func TestEscalationWakeNamesTheQueue(t *testing.T) {
	svc, _, turns, _ := escalationRig(t, true)
	svc.begin("alpha", "coder", escalationFor("coder", "3", "/srv/data"))
	waitForCondition(t, func() bool { return len(*turns) > 0 })
	wake := (*turns)[0]
	for _, want := range []string{"coder:3", "/srv/data", "leader_resolve_member_approval"} {
		if !strings.Contains(wake, want) {
			t.Errorf("wake text must carry %q so the leader can act:\n%s", want, wake)
		}
	}
}

func waitForCondition(t *testing.T, done func() bool) {
	t.Helper()
	for range 400 {
		if done() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never held")
}
