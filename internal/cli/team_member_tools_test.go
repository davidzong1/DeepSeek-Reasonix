package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/team"
	"reasonix/internal/tool"
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

func TestMemberReportResultSupportsReadAndWriteModes(t *testing.T) {
	var report tool.Tool
	for _, candidate := range newMemberTaskTools(&teamTaskService{}, "alpha", "m1") {
		if candidate.Name() == "member_report_result" {
			report = candidate
			break
		}
	}
	if report == nil {
		t.Fatal("member_report_result tool is missing")
	}
	if report.ReadOnly() {
		t.Fatal("member_report_result must remain writable by default")
	}
	hinter, ok := report.(interface {
		EffectHint(json.RawMessage) tool.EffectHint
	})
	if !ok {
		t.Fatal("member_report_result must expose an argument-aware effect hint")
	}
	if hint := hinter.EffectHint(json.RawMessage(`{"operation":"read"}`)); !hint.Known || !hint.ReadOnly {
		t.Fatalf("read operation hint = %+v, want known read-only", hint)
	}
	if hint := hinter.EffectHint(json.RawMessage(`{"operation":"report"}`)); !hint.Known || hint.ReadOnly {
		t.Fatalf("report operation hint = %+v, want known writable", hint)
	}
}

func TestLeaderAndMemberTaskToolSurfaces(t *testing.T) {
	service := &teamTaskService{}
	leader := newLeaderTaskTools(service, "alpha", "lead")
	wantLeader := []string{"leader_list_team", "leader_select_task_members", "leader_assign_subtask", "leader_assign_task_to_relevant", "leader_check_member_status", "leader_retry_task", "leader_cancel_task", "leader_reassign_task", "leader_authz_log", "team_knowledge_recall", "team_knowledge_expire"}
	if len(leader) != len(wantLeader) {
		t.Fatalf("leader task tool count = %d, want %d", len(leader), len(wantLeader))
	}
	for i, want := range wantLeader {
		if got := leader[i].Name(); got != want {
			t.Errorf("leader tool %d = %q, want %q", i, got, want)
		}
	}
	member := newMemberTaskTools(service, "alpha", "m1")
	wantMember := []string{"member_get_my_task", "member_report_result", "member_set_approval_mode", "team_knowledge_recall"}
	if len(member) != 4 {
		t.Fatalf("member task tool count = %d", len(member))
	}
	for i, want := range wantMember {
		if got := member[i].Name(); got != want {
			t.Errorf("member tool %d = %q, want %q", i, got, want)
		}
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

// TestMemberSetApprovalModeAndLeaderAuthzLogTools pins the end-to-end tool
// surface: the member tool switches only its bound member's persisted mode, the
// leader tool renders the recorded decisions newest first, and the service
// refuses a leader slot through the same store gate the tools execute on.
func TestMemberSetApprovalModeAndLeaderAuthzLogTools(t *testing.T) {
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
	service := newTeamTaskService(store, nil, "alpha", nil)
	byName := map[string]toolExecutor{}
	for _, candidate := range append(newMemberTaskTools(service, "alpha", "m1"), newLeaderTaskTools(service, "alpha", "lead")...) {
		byName[candidate.Name()] = candidate
	}
	if _, err := byName["member_set_approval_mode"].Execute(context.Background(), json.RawMessage(`{"mode":"manual"}`)); err != nil {
		t.Fatal(err)
	}
	doc, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if doc.Teams[0].Template[1].ApprovalMode != team.ApprovalModeManual {
		t.Fatalf("member tool must persist its own mode, slot = %+v", doc.Teams[0].Template[1])
	}
	// The service refuses a leader slot: the store gate is the last line.
	if _, err := service.setApprovalMode("lead", team.ApprovalModeManual); err == nil {
		t.Fatal("service must refuse switching the leader to manual")
	}
	if err := store.AppendAuthz("alpha", team.AuthzEntry{TS: "t1", Member: "m1", Source: "auto", Allow: true, ID: "a1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendAuthz("alpha", team.AuthzEntry{TS: "t2", Member: "m1", Source: "leader", Allow: false, ID: "a2"}); err != nil {
		t.Fatal(err)
	}
	out, err := byName["leader_authz_log"].Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "allow") || !strings.Contains(out, "deny") || strings.Index(out, "a2") > strings.Index(out, "a1") {
		t.Fatalf("leader_authz_log must render decisions newest first:\n%s", out)
	}
	ro, ok := byName["leader_authz_log"].(interface{ ReadOnly() bool })
	if !ok || !ro.ReadOnly() {
		t.Fatal("leader_authz_log must be read-only")
	}
}

// TestLeaderAddMemberFullAttributes pins the member-create surface: the tool
// carries the whole non-leader attribute set — role, agent-user pin, launch
// type, proxy override, or a dedicated custom pool (a pin and a pool are
// mutually exclusive, like the editor) — while agent-user inputs left out
// default to the binding strategy (no pin, no pool: the member inherits the
// team pool head), and the leader property is refused outright in both
// encodings.
func TestLeaderAddMemberFullAttributes(t *testing.T) {
	root := t.TempDir()
	store, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{
		Name: "alpha", Template: []team.MemberSlot{{MemberID: "lead", Leader: true, Status: team.MemberStatusActive}},
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddAgentUser(team.AgentUser{UserID: "au-1"}); err != nil {
		t.Fatal(err)
	}
	tools := newLeaderMemberTools(store, nil, "alpha", "lead", (&retiredMembers{}).record)
	byName := map[string]toolExecutor{}
	for _, candidate := range tools {
		byName[candidate.Name()] = candidate
	}
	// A pinned add carries the role, binding, launch type and proxy override.
	if _, err := byName["leader_add_member"].Execute(context.Background(), json.RawMessage(`{"member_id":"m2","role":"tester","agent_user_ref":"au-1","agent_type":"codex","proxy_enabled":true}`)); err != nil {
		t.Fatal(err)
	}
	// A pool add assigns the member its own custom pool.
	if _, err := byName["leader_add_member"].Execute(context.Background(), json.RawMessage(`{"member_id":"m3","role":"reviewer","agent_user_pool":["au-1"]}`)); err != nil {
		t.Fatal(err)
	}
	// Agent-user inputs left out default to the binding strategy.
	if _, err := byName["leader_add_member"].Execute(context.Background(), json.RawMessage(`{"member_id":"m4"}`)); err != nil {
		t.Fatal(err)
	}
	// The leader property is leader-only: its legacy role encoding is refused.
	if _, err := byName["leader_add_member"].Execute(context.Background(), json.RawMessage(`{"member_id":"m5","role":"leader"}`)); err == nil || !strings.Contains(err.Error(), "leader property") {
		t.Fatalf("role leader: err = %v, want the leader-property refusal", err)
	}
	// A custom pool must name existing pool entries.
	if _, err := byName["leader_add_member"].Execute(context.Background(), json.RawMessage(`{"member_id":"m6","agent_user_pool":["ghost"]}`)); !errors.Is(err, team.ErrAgentUserNotFound) {
		t.Fatalf("ghost pool ref: err = %v, want ErrAgentUserNotFound", err)
	}
	doc, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	tpl := doc.Teams[0].Template
	if len(tpl) != 4 {
		t.Fatalf("template = %d members, want 4 (lead, m2, m3, m4)", len(tpl))
	}
	pinned := tpl[1]
	if pinned.MemberID != "m2" || pinned.Role != team.RoleTester || pinned.AgentUserRef != "au-1" || pinned.AgentType != "codex" || pinned.Status != team.MemberStatusActive {
		t.Fatalf("pinned member wrong: %+v", pinned)
	}
	if pinned.ProxyEnabled == nil || !*pinned.ProxyEnabled {
		t.Fatalf("proxy override not persisted: %+v", pinned.ProxyEnabled)
	}
	if pinned.PoolMode != "" || len(pinned.AgentUserPool) != 0 {
		t.Fatalf("a pinned member must not carry a custom pool: %+v", pinned.AgentUserPool)
	}
	pooled := tpl[2]
	if pooled.PoolMode != team.MemberPoolCustom || len(pooled.AgentUserPool) != 1 || pooled.AgentUserPool[0] != "au-1" || pooled.AgentUserRef != "" {
		t.Fatalf("custom pool not persisted: %+v", pooled)
	}
	plain := tpl[3]
	if plain.AgentUserRef != "" || plain.PoolMode != "" || len(plain.AgentUserPool) != 0 || plain.Role != "" || plain.AgentType != "" {
		t.Fatalf("defaulted member must stay unbound and inheriting: %+v", plain)
	}
}
