package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/team"
)

func TestLeaderMemberToolsMutateRosterAndClearRoleContext(t *testing.T) {
	root := t.TempDir()
	store, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{
		Name: "alpha", Template: []team.MemberSlot{{MemberID: "lead", Leader: true, Status: team.MemberStatusActive}, {MemberID: "m1", Role: team.RoleCoder, Status: team.MemberStatusActive}},
	}}}); err != nil {
		t.Fatal(err)
	}
	sessions, err := team.NewTeamSessionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.AppendMessage("alpha", "m1", team.SessionMessage{Text: "old context"}); err != nil {
		t.Fatal(err)
	}
	// retired records the backend releases the roster tools must issue: a removed
	// or retagged member's controller has to stop before its context is cleared.
	retired := &retiredMembers{}
	tools := newLeaderMemberTools(store, sessions, "alpha", "lead", retired.record)
	if len(tools) != 3 {
		t.Fatalf("leader tool count = %d", len(tools))
	}
	byName := map[string]toolExecutor{}
	for _, candidate := range tools {
		byName[candidate.Name()] = candidate
	}
	if _, err := byName["leader_add_member"].Execute(context.Background(), json.RawMessage(`{"member_id":"m2","role":"tester"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := byName["leader_set_member_role"].Execute(context.Background(), json.RawMessage(`{"member_id":"m1","role":"reviewer"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".reasonix", "team", "context", "alpha", "m1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("role change should clear member context, stat err = %v", err)
	}
	if _, err := byName["leader_remove_member"].Execute(context.Background(), json.RawMessage(`{"member_id":"m2"}`)); err != nil {
		t.Fatal(err)
	}
	doc, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Teams[0].Template) != 2 {
		t.Fatalf("roster after management calls = %+v", doc.Teams[0].Template)
	}
}

func TestLeaderAndMemberTaskToolSurfaces(t *testing.T) {
	service := &teamTaskService{}
	leader := newLeaderTaskTools(service, "alpha", "lead")
	if len(leader) != 5 {
		t.Fatalf("leader task tool count = %d", len(leader))
	}
	wantLeader := []string{"leader_list_team", "leader_select_task_members", "leader_assign_subtask", "leader_assign_task_to_relevant", "leader_check_member_status"}
	for i, want := range wantLeader {
		if got := leader[i].Name(); got != want {
			t.Errorf("leader tool %d = %q, want %q", i, got, want)
		}
	}
	member := newMemberTaskTools(service, "alpha", "m1")
	if len(member) != 2 || member[0].Name() != "member_get_my_task" || member[1].Name() != "member_report_result" {
		t.Fatalf("member task tools have unexpected names")
	}
}

type toolExecutor interface {
	Name() string
	Execute(context.Context, json.RawMessage) (string, error)
}

// retiredMembers records which members had their backend released.
type retiredMembers struct{ ids []string }

func (r *retiredMembers) record(_, memberID string) { r.ids = append(r.ids, memberID) }

// TestLeaderRosterToolsRetireTheAffectedBackend pins D7: the TUI's member editor
// releases the backend on remove and on a role change, and the leader's own tools
// must too. Without it a removed member kept a live controller — session lease,
// plugin subprocesses, and a turn still writing the context just cleared — and a
// retagged member kept serving the role baked into its cache-stable prompt.
func TestLeaderRosterToolsRetireTheAffectedBackend(t *testing.T) {
	root := t.TempDir()
	store, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{
		Name: "alpha", Template: []team.MemberSlot{
			{MemberID: "lead", Leader: true, Status: team.MemberStatusActive},
			{MemberID: "m1", Role: team.RoleCoder, Status: team.MemberStatusActive},
			{MemberID: "m2", Role: team.RoleCoder, Status: team.MemberStatusActive},
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	retired := &retiredMembers{}
	byName := map[string]toolExecutor{}
	for _, candidate := range newLeaderMemberTools(store, nil, "alpha", "lead", retired.record) {
		byName[candidate.Name()] = candidate
	}
	if _, err := byName["leader_set_member_role"].Execute(context.Background(), json.RawMessage(`{"member_id":"m1","role":"tester"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := byName["leader_remove_member"].Execute(context.Background(), json.RawMessage(`{"member_id":"m2"}`)); err != nil {
		t.Fatal(err)
	}
	if len(retired.ids) != 2 || retired.ids[0] != "m1" || retired.ids[1] != "m2" {
		t.Fatalf("released backends = %v, want [m1 m2]", retired.ids)
	}
	// A refused mutation must not retire anything.
	before := len(retired.ids)
	if _, err := byName["leader_remove_member"].Execute(context.Background(), json.RawMessage(`{"member_id":"ghost"}`)); err == nil {
		t.Fatal("removing an unknown member must fail")
	}
	if len(retired.ids) != before {
		t.Fatalf("a refused remove released %v", retired.ids[before:])
	}
}
