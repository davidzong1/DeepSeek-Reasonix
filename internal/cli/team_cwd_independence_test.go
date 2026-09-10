package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/boot"
	"reasonix/internal/team"
)

// TestMemberBackendLoadsTeamSkillsFromUserRootOutsideCWD pins the boundary the
// team skill root protects: the member's playbook comes from the user state
// root's team/skills tree whatever the launch workspace and the process cwd
// say, while the workspace root keeps driving files, sandbox and status. The
// launch workspace owns a same-shaped tree of its own, so a session that still
// rooted the tree at the workspace could not pass this.
func TestMemberBackendLoadsTeamSkillsFromUserRootOutsideCWD(t *testing.T) {
	state := t.TempDir()
	t.Setenv("REASONIX_STATE_HOME", state)
	t.Setenv("REASONIX_HOME", "")
	skillPath := filepath.Join(state, "team", "skills", "base", "leader", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const marker = "USER-ROOT-LEADER-SKILL"
	if err := os.WriteFile(skillPath, []byte("---\nname: leader\ndescription: team leader\n---\n"+marker), 0o600); err != nil {
		t.Fatal(err)
	}

	project := t.TempDir()
	writeManagerSkillWorkspace(t, project)
	t.Chdir(t.TempDir())
	sessionDir := t.TempDir()
	build := newMemberBackendBuilder(memberBackendDeps{
		ctx: t.Context(),
		users: fakePool{users: map[string]team.AgentUser{
			"leader-user": {
				UserID: "leader-user", Provider: "openai", Model: "gpt-5.6",
				BaseURL: "https://example.invalid/v1", APIKey: "test-key",
			},
		}},
		events:        make(chan memberEvent, 1),
		workspaceRoot: project,
		base: func() boot.Options {
			return boot.Options{SessionDir: sessionDir, Stderr: io.Discard}
		},
	})
	backend, err := build(team.MemberBinding{
		Team: "alpha", MemberID: "lead", Leader: true, AgentUserRef: "leader-user",
		SessionFile: filepath.Join("alpha", "lead.jsonl"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Close)

	if got := backend.WorkspaceRoot(); got != project {
		t.Fatalf("member workspace root = %q, want project root %q", got, project)
	}
	prompt := backend.SystemPrompt()
	if !strings.Contains(prompt, marker) {
		t.Fatalf("member system prompt did not load the user-global team skill")
	}
	if strings.Contains(prompt, "LDR-BASE") {
		t.Fatal("the launch workspace's own team tree must not serve the member prompt")
	}
}

func TestTeamBoardAndKnowledgeRootsIgnoreCWD(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("REASONIX_STATE_HOME", stateHome)
	t.Setenv("REASONIX_HOME", "")

	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, "team", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	launchDir := t.TempDir()
	t.Chdir(launchDir)

	roots, err := openTeamDataRoots(project)
	if err != nil {
		t.Fatal(err)
	}
	wantData := filepath.Join(stateHome, "team")
	if roots.dataDir != wantData {
		t.Fatalf("team data root = %q, want %q", roots.dataDir, wantData)
	}
	board := openTeamInbox(roots.dataDir)
	if board == nil {
		t.Fatal("open team blackboard")
	}
	t.Cleanup(board.close)
	if _, err := os.Stat(filepath.Join(wantData, "board.db")); err != nil {
		t.Fatalf("blackboard must open under the user team root: %v", err)
	}
	if got, want := teamKBDataRoot(roots.dataDir), filepath.Join(wantData, "knowledge_base"); got != want {
		t.Fatalf("knowledge root = %q, want %q", got, want)
	}
	for _, path := range []string{roots.dataDir, teamKBDataRoot(roots.dataDir)} {
		if strings.HasPrefix(path, launchDir+string(os.PathSeparator)) {
			t.Fatalf("team runtime path leaked into launch cwd: %q", path)
		}
	}
}
