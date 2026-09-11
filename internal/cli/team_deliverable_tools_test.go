package cli

// Deliverable surface tests: the member writes under its own bound identity,
// the leader reads the whole team, and neither the read nor the publish tool
// changes a read-only or plan boundary.

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/tool"
)

func deliverableToolSet(t *testing.T, tools []tool.Tool) map[string]tool.Tool {
	t.Helper()
	out := map[string]tool.Tool{}
	for _, candidate := range tools {
		out[candidate.Name()] = candidate
	}
	return out
}

func executeTool(t *testing.T, candidate tool.Tool, args string) (string, error) {
	t.Helper()
	return candidate.Execute(context.Background(), json.RawMessage(args))
}

// deliverableOf asserts the tool is the deliverable surface's own type: the
// read-only and plan-safe answers are part of that type, not of tool.Tool.
func deliverableOf(t *testing.T, candidate tool.Tool) *deliverableTool {
	t.Helper()
	d, ok := candidate.(*deliverableTool)
	if !ok {
		t.Fatalf("%s is not a deliverable tool", candidate.Name())
	}
	return d
}

// The member half publishes under its own identity and reads it back; the
// leader half only reads. Neither half exposes a member or team argument, so a
// caller cannot publish as someone else or reach another team's documents.
func TestDeliverableToolSetsSeparatePublishFromRead(t *testing.T) {
	state := t.TempDir()
	t.Setenv("REASONIX_STATE_HOME", state)

	member := deliverableToolSet(t, newMemberDeliverableTools("alpha", "m1", &bytes.Buffer{}))
	if len(member) != 3 {
		t.Fatalf("member deliverable tools = %d, want publish, read and list", len(member))
	}
	leader := deliverableToolSet(t, newLeaderDeliverableTools("alpha", "lead", &bytes.Buffer{}))
	if len(leader) != 2 {
		t.Fatalf("leader deliverable tools = %d, want read and list", len(leader))
	}
	if _, ok := leader[publishDeliverableName]; ok {
		t.Fatal("the leader tool set publishes; the deliverable write is the member's")
	}
	for _, candidate := range []tool.Tool{member[publishDeliverableName]} {
		if candidate == nil {
			t.Fatal("member set is missing the publish tool")
		}
	}
	publishTool := deliverableOf(t, member[publishDeliverableName])
	if publishTool.ReadOnly() {
		t.Fatal("publish must stay write-class: a read-only turn must not add documents")
	}
	if publishTool.PlanModeSafe() {
		t.Fatal("publish must stay plan-unsafe")
	}
	for _, name := range []string{readDeliverableName, listDeliverableName} {
		for _, set := range []map[string]tool.Tool{member, leader} {
			candidate := set[name]
			if candidate == nil {
				t.Fatalf("%s is missing from a role's tool set", name)
			}
			readTool := deliverableOf(t, candidate)
			if !readTool.ReadOnly() || !readTool.PlanModeSafe() {
				t.Fatalf("%s must stay statically read-only and plan-safe", name)
			}
			if !strings.Contains(string(candidate.Schema()), `"additionalProperties":false`) {
				t.Fatalf("%s schema accepts undeclared arguments", name)
			}
		}
	}

	out, err := executeTool(t, member[publishDeliverableName], `{"slug":"route","body":"# route\nbody"}`)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	id := ""
	for field := range strings.FieldsSeq(out) {
		if strings.HasSuffix(field, ".md") {
			id = field
			break
		}
	}
	if !strings.HasPrefix(id, "m1-route-") {
		t.Fatalf("published id %q does not carry the bound member", id)
	}
	// A member field in the arguments is not part of the schema, so the bound
	// identity is the only one a publish can carry.
	if _, err := executeTool(t, member[publishDeliverableName], `{"slug":"route","body":"# route\nbody","member":"someone-else"}`); err != nil {
		t.Fatalf("republish: %v", err)
	}
	read, err := executeTool(t, member[readDeliverableName], `{"id":"`+id+`"}`)
	if err != nil {
		t.Fatalf("member read: %v", err)
	}
	if read != "# route\nbody" {
		t.Fatalf("read = %q", read)
	}
	leaderRead, err := executeTool(t, leader[readDeliverableName], `{"id":"`+id+`"}`)
	if err != nil {
		t.Fatalf("leader read: %v", err)
	}
	if leaderRead != read {
		t.Fatalf("leader read = %q, want the member's bytes %q", leaderRead, read)
	}
	listed, err := executeTool(t, leader[listDeliverableName], `{}`)
	if err != nil {
		t.Fatalf("leader list: %v", err)
	}
	if !strings.Contains(listed, id) {
		t.Fatalf("leader list %q does not name %q", listed, id)
	}
	if _, err := executeTool(t, member[readDeliverableName], `{"id":"other-team-deadbeef0000.md"}`); err == nil {
		t.Fatal("a foreign id was read")
	}
}

// A session without a team or member identity assembles no deliverable tools at
// all, so an unbound backend can never be the one that publishes.
func TestDeliverableToolsRequireABoundIdentity(t *testing.T) {
	for _, tc := range []struct{ name, team, member string }{
		{name: "no team", team: "", member: "m1"},
		{name: "no member", team: "alpha", member: ""},
		{name: "neither", team: "", member: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := newMemberDeliverableTools(tc.team, tc.member, &bytes.Buffer{}); got != nil {
				t.Fatalf("member tools = %v, want nil", got)
			}
			if got := newLeaderDeliverableTools(tc.team, tc.member, &bytes.Buffer{}); got != nil {
				t.Fatalf("leader tools = %v, want nil", got)
			}
		})
	}
}

// A root the store cannot use — here a state root under a regular file — makes
// every call fail with the reason instead of writing somewhere no peer could
// read it.
func TestDeliverableToolSurfacesAnUnusableRoot(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("stage blocker: %v", err)
	}
	t.Setenv("REASONIX_STATE_HOME", filepath.Join(blocker, "state"))
	tools := deliverableToolSet(t, newMemberDeliverableTools("alpha", "m1", &bytes.Buffer{}))
	if len(tools) != 3 {
		t.Fatalf("tools = %d, want the member's three", len(tools))
	}
	for name, args := range map[string]string{
		publishDeliverableName: `{"slug":"doc","body":"x"}`,
		readDeliverableName:    `{"id":"m1-doc-abcdefabcdef.md"}`,
		listDeliverableName:    `{}`,
	} {
		if _, err := executeTool(t, tools[name], args); err == nil {
			t.Fatalf("%s succeeded against an unusable state root", name)
		}
	}
}

// Every refused call logs exactly one structured line to the host's writer, and
// no line carries the body or a host path. The refusals exercised are the ones
// the store really refuses: an argument that is not addressable, a planted
// symbolic link, and a document edited outside the tools.
func TestDeliverableToolLogsRefusalsWithoutBodyOrPath(t *testing.T) {
	state := t.TempDir()
	t.Setenv("REASONIX_STATE_HOME", state)
	var logged bytes.Buffer
	tools := deliverableToolSet(t, newMemberDeliverableTools("alpha", "m1", &logged))
	cacheRoot := filepath.Join(state, "team", "cache")
	const secret = "SENTINEL-BODY"

	// One line per refusal, naming the category and never the body or a path.
	refusal := func(t *testing.T, name, args string) {
		t.Helper()
		logged.Reset()
		if _, err := executeTool(t, tools[name], args); err == nil {
			t.Fatalf("%s succeeded, so nothing was refused", name)
		}
		lines := strings.Split(strings.TrimSpace(logged.String()), "\n")
		if len(lines) != 1 {
			t.Fatalf("%s logged %d lines, want exactly one: %q", name, len(lines), logged.String())
		}
		if strings.Contains(lines[0], secret) {
			t.Fatalf("%s logged the body: %s", name, lines[0])
		}
		if strings.Contains(lines[0], state) {
			t.Fatalf("%s logged a host path: %s", name, lines[0])
		}
		if !strings.Contains(lines[0], "alpha") {
			t.Fatalf("%s logged no team: %s", name, lines[0])
		}
	}

	refusal(t, publishDeliverableName, `{"slug":"Bad Slug","body":"`+secret+`"}`)
	refusal(t, readDeliverableName, `{"id":"../escape.md"}`)
	refusal(t, publishDeliverableName, `{"slug":"ok"`)

	logged.Reset()
	published, err := executeTool(t, tools[publishDeliverableName], `{"slug":"doc","body":"fine"}`)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if logged.Len() != 0 {
		t.Fatalf("a successful publish logged %q", logged.String())
	}
	id := ""
	for field := range strings.FieldsSeq(published) {
		if strings.HasSuffix(field, ".md") {
			id = field
			break
		}
	}

	// A list of a team that published nothing is an answer, not a refusal.
	logged.Reset()
	if _, err := executeTool(t, tools[listDeliverableName], `{}`); err != nil {
		t.Fatalf("list: %v", err)
	}
	if logged.Len() != 0 {
		t.Fatalf("a listing logged %q", logged.String())
	}

	// Edited outside the tools: the digest in the id no longer matches.
	if err := os.WriteFile(filepath.Join(cacheRoot, "alpha", id), []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	refusal(t, readDeliverableName, `{"id":"`+id+`"}`)

	// A symbolic link where the team directory belongs.
	outside := t.TempDir()
	if err := os.RemoveAll(filepath.Join(cacheRoot, "alpha")); err != nil {
		t.Fatalf("remove team dir: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(cacheRoot, "alpha")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	refusal(t, publishDeliverableName, `{"slug":"doc","body":"fine"}`)
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("read the link target: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("the link target received %v", entries)
	}
}

// installTeamSkills copies the repository's playbooks into a staged user state
// root the way `make install-team-skills` does, so the assertions below read the
// tree a session actually resolves rather than the repository itself.
func installTeamSkills(t *testing.T, repoRoot, stateRoot string) {
	t.Helper()
	src := filepath.Join(repoRoot, "team", "skills")
	dst := filepath.Join(stateRoot, "team", "skills")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if err != nil {
		t.Fatalf("install team skills: %v", err)
	}
}

// The shared playbook is installed into the user state root and is visible to
// both roles there, which is what makes one document the interface for the whole
// team. It carries no team_role declaration, so neither role is closed out.
func TestDeliverableSkillIsVisibleToBothRoles(t *testing.T) {
	repo := filepath.Join("..", "..")
	state := t.TempDir()
	installTeamSkills(t, repo, state)
	t.Setenv("REASONIX_STATE_HOME", state)

	leader := teamRoleSkillPrompt(state, true)
	member := teamRoleSkillPrompt(state, false)
	for role, prompt := range map[string]string{"leader": leader, "member": member} {
		if !strings.Contains(prompt, "member_publish_deliverable") || !strings.Contains(prompt, "member_read_deliverable") {
			t.Fatalf("%s prompt does not carry the installed deliverable playbook:\n%s", role, prompt)
		}
	}
	installed, err := os.ReadFile(filepath.Join(state, "team", "skills", "shared", "deliverable", "SKILL.md"))
	if err != nil {
		t.Fatalf("read the installed playbook: %v", err)
	}
	shipped, err := os.ReadFile(filepath.Join(repo, "team", "skills", "shared", "deliverable", "SKILL.md"))
	if err != nil {
		t.Fatalf("read the shipped playbook: %v", err)
	}
	if string(installed) != string(shipped) {
		t.Fatal("the installed playbook differs from the repository's")
	}
	text := string(installed)
	if strings.Contains(text, "team_role:") {
		t.Fatal("the shared playbook declares a team_role, closing it for one role")
	}
	if !strings.Contains(text, "1 MiB") {
		t.Fatal("the playbook must state the body limit the store enforces")
	}
}
