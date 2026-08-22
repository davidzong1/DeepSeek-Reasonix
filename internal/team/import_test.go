package team

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mcpFixture is a synthetic mult_agent_mcp teams_data.json matching the
// documented shape (§2.1): dict-keyed teams and agent_users, runtime-state
// fields mixed in to pin that they are dropped, and one multi-provider
// agent_user to pin the per-provider record split (§2.3).
const mcpFixture = `{
  "teams": {
    "alpha": {
      "default_agent": "claude",
      "default_agent_user": "au-1",
      "proxy": {"enabled": true, "host": "127.0.0.1", "port": 7890},
      "members": {
        "m1": {"role": "coder", "agent": "codex", "model": "gpt-5"},
        "m2": {"role": "tester", "agent": "claude", "model": "claude-opus-5"},
        "m3": {"role": "leader", "agent": "claude"},
        "m4": {"role": "reviewer", "agent": "claude", "model": "no-such-model"}
      },
      "tmux_window_state": "dropped runtime state",
      "last_task": "also dropped",
      "leader_checkpoint": {"epoch": 1}
    }
  },
  "agent_users": {
    "au-1": {
      "provider": "anthropic",
      "model": "claude-opus-5",
      "base_url": "https://api.anthropic.com",
      "effort": "high",
      "anthropic_api_key": "sk-ant-fake-AAAAAAAA",
      "anthropic_model": "claude-opus-5",
      "openai_api_key": "sk-org-fake-BBBBBBBB",
      "openai_model": "gpt-5"
    },
    "au-2": {
      "provider": "openai",
      "model": "gpt-5",
      "codex_model": "gpt-5-codex"
    }
  }
}`

const mcpKeyA = "sk-ant-fake-AAAAAAAA"
const mcpKeyB = "sk-org-fake-BBBBBBBB"

func writeMCPSource(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "teams_data.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestImportFromMCPMapsTeamsAndPool(t *testing.T) {
	ts, root := newTeamStore(t)
	src := writeMCPSource(t, mcpFixture)
	report, err := ts.ImportFromMCP(src, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.TeamsCreated != 1 || report.MembersImported != 4 || report.AgentUsersCreated != 3 {
		t.Fatalf("counts wrong: %+v", report)
	}
	// Pool: au-1 keeps its key as the primary anthropic record; the openai
	// group splits into au-1@openai; au-2's codex group normalizes onto openai
	// (NormalizeProvider) as its primary record. No credentials by default.
	pool, err := NewAgentUsersStore(root)
	if err != nil {
		t.Fatal(err)
	}
	users, err := pool.ListAgentUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 3 {
		t.Fatalf("pool size = %d, want 3: %+v", len(users), users)
	}
	byID := map[string]AgentUser{}
	for _, u := range users {
		byID[u.UserID] = u
	}
	if u := byID["au-1"]; u.Provider != "anthropic" || u.Model != "claude-opus-5" || u.APIKey != "" {
		t.Fatalf("primary record: %+v", u)
	}
	if u := byID["au-1@openai"]; u.Provider != "openai" || u.Model != "gpt-5" || u.APIKey != "" {
		t.Fatalf("split record: %+v", u)
	}
	if u := byID["au-2"]; u.Provider != ProviderOpenAI || u.Model != "gpt-5-codex" {
		t.Fatalf("codex-normalized record: %+v", u)
	}
	// Team: default agent, proxy, default ref, and member agent types land;
	// m1's "gpt-5" binds the split openai record, m2's "claude-opus-5" binds
	// the primary, m4's model matches nothing and stays unresolved.
	doc, _, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	team := doc.Teams[0]
	if team.AgentType != "claude" || team.DefaultAgentUserRef != "au-1" {
		t.Fatalf("team config: %+v", team)
	}
	if team.Proxy == nil || !team.Proxy.Enabled || team.Proxy.Address != "127.0.0.1:7890" {
		t.Fatalf("team proxy: %+v", team.Proxy)
	}
	refs := map[string]string{}
	for _, m := range team.Template {
		refs[m.MemberID] = m.AgentUserRef
	}
	if refs["m1"] != "au-1@openai" {
		t.Fatalf("m1 ref = %q, want au-1@openai", refs["m1"])
	}
	if refs["m2"] != "au-1" {
		t.Fatalf("m2 ref = %q, want au-1", refs["m2"])
	}
	if refs["m3"] != "" {
		t.Fatalf("m3 (no model) ref = %q, want empty", refs["m3"])
	}
	// Leader separation: the source's role "leader" lands on the standalone
	// Leader property with the business role left empty; every other member
	// keeps its role and stays a regular member.
	leaders := map[string]bool{}
	roles := map[string]RoleID{}
	for _, m := range team.Template {
		leaders[m.MemberID] = m.Leader
		roles[m.MemberID] = m.Role
	}
	if !leaders["m3"] || roles["m3"] != "" {
		t.Fatalf("m3 should be the standalone leader with an empty role, got leader=%v role=%q", leaders["m3"], roles["m3"])
	}
	for _, id := range []string{"m1", "m2", "m4"} {
		if leaders[id] {
			t.Fatalf("%s must not be a leader", id)
		}
	}
	if roles["m1"] != RoleCoder || roles["m2"] != RoleTester || roles["m4"] != RoleReviewer {
		t.Fatalf("business roles lost: %+v", roles)
	}
	if refs["m4"] != "" {
		t.Fatalf("m4 (unresolvable model) ref = %q, want empty", refs["m4"])
	}
	if report.UnresolvedRefs != 1 {
		t.Fatalf("UnresolvedRefs = %d, want 1", report.UnresolvedRefs)
	}
	if report.CredentialFieldsSkipped != 2 || report.CredentialFields != 0 {
		t.Fatalf("credential counts: %+v", report)
	}
}

// TestImportFromMCPDefaultSkipsKeys pins the K1 default: without
// ImportCredentials no key material may land anywhere in .reasonix.
func TestImportFromMCPDefaultSkipsKeys(t *testing.T) {
	ts, root := newTeamStore(t)
	src := writeMCPSource(t, mcpFixture)
	if _, err := ts.ImportFromMCP(src, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{filepath.Join(".reasonix", "team", TeamFile),
		filepath.Join(".reasonix", "team", AgentUsersFile)} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), mcpKeyA) || strings.Contains(string(data), mcpKeyB) {
			t.Fatalf("%s leaked key material: %s", rel, data)
		}
	}
}

// TestImportFromMCPWithCredentialsPins0600 pins the decided path: opted-in
// credentials land in agent_users.json at 0600 and never surface in the
// report.
func TestImportFromMCPWithCredentialsPins0600(t *testing.T) {
	ts, root := newTeamStore(t)
	src := writeMCPSource(t, mcpFixture)
	report, err := ts.ImportFromMCP(src, ImportOptions{ImportCredentials: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.CredentialFields != 2 || report.CredentialFieldsSkipped != 0 {
		t.Fatalf("credential counts: %+v", report)
	}
	info, err := os.Stat(filepath.Join(root, ".reasonix", "team", AgentUsersFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("agent_users.json perm = %o, want 600", perm)
	}
	data, err := os.ReadFile(filepath.Join(root, ".reasonix", "team", AgentUsersFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), mcpKeyA) {
		t.Fatal("opted-in credentials must land in agent_users.json")
	}
	// The report itself must never carry key material (K2).
	if strings.Contains(reportStr(report), mcpKeyA) || strings.Contains(reportStr(report), mcpKeyB) {
		t.Fatalf("report leaked key material: %+v", report)
	}
}

func reportStr(r *ImportReport) string {
	return strings.Join(r.Backups, "\n")
}

func TestImportFromMCPIsIdempotent(t *testing.T) {
	ts, _ := newTeamStore(t)
	src := writeMCPSource(t, mcpFixture)
	first, err := ts.ImportFromMCP(src, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	doc1, _, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	second, err := ts.ImportFromMCP(src, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if second.TeamsCreated != 0 || second.TeamsUpdated != 1 ||
		second.MembersImported != 0 || second.AgentUsersCreated != 0 {
		t.Fatalf("re-import must add nothing: %+v", second)
	}
	doc2, _, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(doc1.Teams) != len(doc2.Teams) || len(doc1.Teams[0].Template) != len(doc2.Teams[0].Template) {
		t.Fatalf("re-import changed the registry: %+v -> %+v", doc1.Teams[0], doc2.Teams[0])
	}
	// Backups were written before each import, including for the pool.
	if len(first.Backups) != 0 || len(second.Backups) != 2 {
		t.Fatalf("backup counts: first=%v second=%v", first.Backups, second.Backups)
	}
	for _, bak := range second.Backups {
		info, err := os.Stat(bak)
		if err != nil {
			t.Fatalf("backup %s missing: %v", bak, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("backup %s perm = %o, want 600", bak, perm)
		}
	}
}

func TestImportFromMCPKeepsLocalEdits(t *testing.T) {
	ts, _ := newTeamStore(t)
	doc := validDoc()
	doc.Teams[0].AgentType = "codex" // local edit must survive the merge
	if err := ts.Save(doc); err != nil {
		t.Fatal(err)
	}
	src := writeMCPSource(t, mcpFixture)
	if _, err := ts.ImportFromMCP(src, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	got, _, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Teams[0].AgentType != "codex" {
		t.Fatalf("local AgentType clobbered: %q", got.Teams[0].AgentType)
	}
	if got.Teams[0].Name != "alpha" {
		t.Fatalf("existing team not merged: %+v", got.Teams[0])
	}
	ids := map[string]bool{}
	for _, m := range got.Teams[0].Template {
		ids[m.MemberID] = true
	}
	if !ids["m1"] || !ids["m2"] {
		t.Fatalf("missing imported members: %+v", got.Teams[0].Template)
	}
}

func TestImportFromMCPBadSources(t *testing.T) {
	ts, _ := newTeamStore(t)
	if _, err := ts.ImportFromMCP(filepath.Join(t.TempDir(), "nope.json"), ImportOptions{}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing source: err = %v, want fs.ErrNotExist", err)
	}
	bad := writeMCPSource(t, `{"teams": [`)
	if _, err := ts.ImportFromMCP(bad, ImportOptions{}); !strings.Contains(err.Error(), "teams_data.json") {
		t.Fatalf("corrupt source must name the path: %v", err)
	}
	empty := writeMCPSource(t, `{}`)
	if _, err := ts.ImportFromMCP(empty, ImportOptions{}); !strings.Contains(err.Error(), "no teams or agent_users") {
		t.Fatalf("empty source: err = %v", err)
	}
}

func TestImportFromMCPWithExistingLegacyFile(t *testing.T) {
	ts, root := newTeamStore(t)
	writeTeamFile(t, root, TeamsLegacyFile,
		`{"schema_version":1,"teams":[{"Name":"old","Template":[],"DefaultAgentUserRef":""}]}`)
	src := writeMCPSource(t, mcpFixture)
	report, err := ts.ImportFromMCP(src, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.TeamsCreated != 1 {
		t.Fatalf("legacy team must stay and alpha be created: %+v", report)
	}
	doc, legacy, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if legacy || len(doc.Teams) != 2 {
		t.Fatalf("post-import load wrong: legacy=%v teams=%d", legacy, len(doc.Teams))
	}
}

// TestImportFromMCPDshGroupNormalizesDeepSeek pins the legacy import alias:
// a source dsh_* group lands as the canonical deepseek value, so new imports
// never reintroduce the old name.
func TestImportFromMCPDshGroupNormalizesDeepSeek(t *testing.T) {
	ts, root := newTeamStore(t)
	src := writeMCPSource(t, `{
	  "teams": {},
	  "agent_users": {
	    "au-1": {
	      "provider": "dsh",
	      "model": "deepseek-v4-flash",
	      "dsh_api_key": "sk-ds-fake-CCCCCCCC",
	      "dsh_model": "deepseek-v4-flash"
	    }
	  }
	}`)
	report, err := ts.ImportFromMCP(src, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.AgentUsersCreated != 1 {
		t.Fatalf("AgentUsersCreated = %d, want 1: %+v", report.AgentUsersCreated, report)
	}
	pool, err := NewAgentUsersStore(root)
	if err != nil {
		t.Fatal(err)
	}
	users, err := pool.ListAgentUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Provider != ProviderDeepSeek || users[0].Model != "deepseek-v4-flash" {
		t.Fatalf("dsh group should normalize to deepseek, got %+v", users)
	}
}
