package cli

// Isolation guarantees for role-skill loading: allowlist, symlink refusal,
// name-stable shared scan, byte budget. Scope/canonical pins live in
// team_role_skill_scope_test.go.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseTeamRoleAllowlist pins the role vocabulary: only the two exact
// spellings are legal. Empty, unknown, mistyped and traversal-shaped values
// are refused before they can name a path.
func TestParseTeamRoleAllowlist(t *testing.T) {
	for _, valid := range []string{"leader", "member"} {
		role, ok := parseTeamRole(valid)
		if !ok || role != teamRole(valid) {
			t.Errorf("parseTeamRole(%q) = %q, %v; want %q, true", valid, role, ok, valid)
		}
	}
	for _, invalid := range []string{
		"", "architect", "Leader", "LEADER", " leader",
		"leader/", "leader/../member", "../member", "special/leader", ".", "..",
	} {
		if role, ok := parseTeamRole(invalid); ok {
			t.Errorf("parseTeamRole(%q) = %q, true; want refusal", invalid, role)
		}
	}
}

// TestRoleSkillPromptRefusesNonAllowlistRole loads a full tree then asks for
// roles outside the allowlist; no disk skill may be read and the result is
// empty, proving an unvalidated role never reaches the filesystem.
func TestRoleSkillPromptRefusesNonAllowlistRole(t *testing.T) {
	root := t.TempDir()
	writeRoleSkillTree(t, root, map[string]string{
		"team/skills/base/leader/SKILL.md":    "---\nname: leader\ndescription: role\n---\nLEADER-BASE",
		"team/skills/shared/team/SKILL.md":    "---\nname: team\ndescription: shared\n---\nSHARED-TEAM",
		"team/skills/special/leader/SKILL.md": "---\nname: leader\ndescription: role\n---\nLEADER-SPECIAL",
	})
	for _, bad := range []teamRole{"", "architect", "leader/../member", "../member"} {
		if got := roleSkillPrompt(root, bad); got != "" {
			t.Errorf("roleSkillPrompt(root, %q) = %q; want empty (no branch to load)", bad, got)
		}
	}
	if got := roleSkillPrompt(root, roleForLeader(true)); !strings.Contains(got, "LEADER-SPECIAL") {
		t.Errorf("allowlisted leader must load special from the same tree, got:\n%s", got)
	}
}

// TestRoleSkillSkipsSymlinkedSkillFile: a real shared skill loads, while a
// directory whose canonical SKILL.md is a symlink to an out-of-tree secret is
// refused whole — the lowercase fallback is not consulted.
func TestRoleSkillSkipsSymlinkedSkillFile(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "secret.md")
	if err := os.WriteFile(secret, []byte("SECRET-BODY"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRoleSkillTree(t, root, map[string]string{
		"team/skills/shared/ok/SKILL.md": "---\nname: ok\ndescription: shared\n---\nOK-BODY",
	})
	link := filepath.Join(root, "team", "skills", "shared", "leak", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got := teamRoleSkillPrompt(root, true)
	if !strings.Contains(got, "OK-BODY") {
		t.Errorf("real shared skill must load, got:\n%s", got)
	}
	if strings.Contains(got, "SECRET-BODY") {
		t.Errorf("symlinked skill file must be refused, got:\n%s", got)
	}
}

// TestRoleSkillSkipsSymlinkedDir: a symlinked shared/<name> pointing at an
// out-of-tree skill directory is not descended into.
func TestRoleSkillSkipsSymlinkedDir(t *testing.T) {
	root := t.TempDir()
	writeRoleSkillTree(t, root, map[string]string{
		"out/member/SKILL.md":            "---\nname: member\ndescription: role\n---\nOUTSIDE-BODY",
		"team/skills/shared/ok/SKILL.md": "---\nname: ok\ndescription: shared\n---\nOK-BODY",
	})
	link := filepath.Join(root, "team", "skills", "shared", "outside")
	if err := os.Symlink(filepath.Join(root, "out", "member"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got := teamRoleSkillPrompt(root, true)
	if !strings.Contains(got, "OK-BODY") {
		t.Errorf("real shared skill must load, got:\n%s", got)
	}
	if strings.Contains(got, "OUTSIDE-BODY") {
		t.Errorf("symlinked shared dir must be refused, got:\n%s", got)
	}
}

// TestRoleSkillRefusesSymlinkedSpecial covers both a symlinked special/<role>
// leaf and a symlinked special/ container: either redirect could resolve one
// role's special branch into the other role's (or an out-of-tree) tree, so the
// whole branch is refused.
func TestRoleSkillRefusesSymlinkedSpecial(t *testing.T) {
	t.Run("leaf", func(t *testing.T) {
		root := t.TempDir()
		writeRoleSkillTree(t, root, map[string]string{
			"team/skills/base/leader/SKILL.md":    "---\nname: leader\ndescription: role\n---\nLEADER-BASE",
			"team/skills/special/member/SKILL.md": "---\nname: member\ndescription: role\n---\nMEMBER-LEAF",
		})
		leaf := filepath.Join(root, "team", "skills", "special", "leader")
		if err := os.Symlink(filepath.Join(root, "team", "skills", "special", "member"), leaf); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if got := teamRoleSkillPrompt(root, true); strings.Contains(got, "MEMBER-LEAF") {
			t.Errorf("symlinked special/leader must not read member special, got:\n%s", got)
		}
	})
	t.Run("container", func(t *testing.T) {
		root := t.TempDir()
		writeRoleSkillTree(t, root, map[string]string{
			"other/member/SKILL.md":               "---\nname: member\ndescription: role\n---\nMEMBER-CONTAINER",
			"team/skills/base/leader/SKILL.md":    "---\nname: leader\ndescription: role\n---\nLEADER-BASE",
			"team/skills/special/leader/SKILL.md": "---\nname: leader\ndescription: role\n---\nLEADER-OWN",
		})
		if err := os.MkdirAll(filepath.Join(root, "team", "skills"), 0o755); err != nil {
			t.Fatal(err)
		}
		// Point special/ at the other tree so member skills would resolve under it.
		if err := os.RemoveAll(filepath.Join(root, "team", "skills", "special")); err != nil {
			t.Fatal(err)
		}
		cont := filepath.Join(root, "team", "skills", "special")
		if err := os.Symlink(filepath.Join(root, "other"), cont); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		got := teamRoleSkillPrompt(root, true)
		if strings.Contains(got, "MEMBER-CONTAINER") {
			t.Errorf("symlinked special/ must be refused, got:\n%s", got)
		}
		if strings.Contains(got, "LEADER-OWN") {
			t.Errorf("special/ redirect must not silently load another tree, got:\n%s", got)
		}
	})
}

// TestRoleSkillSharedScanIsRoleNeutralAndStable: every shared skill loads for
// both roles (no role tagging), in name order regardless of on-disk creation
// order, so the assembled section is byte-reproducible for a given tree.
func TestRoleSkillSharedScanIsRoleNeutralAndStable(t *testing.T) {
	root := t.TempDir()
	writeRoleSkillTree(t, root, map[string]string{
		"team/skills/shared/aa/SKILL.md": "---\nname: aa\ndescription: shared\n---\nAA-BODY",
		"team/skills/shared/bb/SKILL.md": "---\nname: bb\ndescription: shared\n---\nBB-BODY",
	})
	for _, leader := range []bool{true, false} {
		got := teamRoleSkillPrompt(root, leader)
		for _, want := range []string{"AA-BODY", "BB-BODY"} {
			if !strings.Contains(got, want) {
				t.Errorf("role=%v shared scan must load %q, got:\n%s", leader, want, got)
			}
		}
		if i, j := strings.Index(got, "AA-BODY"), strings.Index(got, "BB-BODY"); i < 0 || i > j {
			t.Errorf("role=%v shared order must be name-sorted, got:\n%s", leader, got)
		}
	}
}

// TestRoleSkillBudgetCapsSection: a single skill larger than the budget is
// dropped whole rather than truncated or partially injected, while the skills
// that fit still load and the section stays bounded.
func TestRoleSkillBudgetCapsSection(t *testing.T) {
	root := t.TempDir()
	bigBody := "BIG-SENTINEL" + strings.Repeat("x", teamRoleSkillBudget*2)
	writeRoleSkillTree(t, root, map[string]string{
		"team/skills/shared/big/SKILL.md":   "---\nname: big\ndescription: shared\n---\n" + bigBody,
		"team/skills/shared/small/SKILL.md": "---\nname: small\ndescription: shared\n---\nSMALL-BODY",
	})
	got := teamRoleSkillPrompt(root, true)
	if !strings.Contains(got, "SMALL-BODY") {
		t.Errorf("fitting skill must load, got:\n%s", got)
	}
	if strings.Contains(got, "BIG-SENTINEL") {
		t.Errorf("over-budget skill must be dropped, got %d bytes", len(got))
	}
	if len(got) > teamRoleSkillBudget {
		t.Errorf("assembled section exceeds budget: %d > %d", len(got), teamRoleSkillBudget)
	}
}
