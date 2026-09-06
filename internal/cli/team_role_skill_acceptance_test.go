package cli

// Team-role skill acceptance additions: cross-checks pinning the assembled
// <team-role-skill> contract (ordering, framing, empty-tree tolerance) that the
// isolation suite leaves implicit. Each case runs the production loader.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRoleSkillPromptOrderingBaseSharedSpecial pins the assembly order inside
// one role prompt: base, then shared, then that role's special. Reordering
// would change which playbook shadows which, so the relative order is a
// contract, not a cosmetic detail.
func TestRoleSkillPromptOrderingBaseSharedSpecial(t *testing.T) {
	root := t.TempDir()
	writeRoleSkillTree(t, root, map[string]string{
		"team/skills/base/leader/SKILL.md":    "---\nname: leader\ndescription: role\n---\nLEADER-BASE",
		"team/skills/special/leader/SKILL.md": "---\nname: leader\ndescription: role\n---\nLEADER-SPECIAL",
		"team/skills/shared/a/SKILL.md":       "---\nname: a\ndescription: shared\n---\nSHARED-A",
		"team/skills/shared/b/SKILL.md":       "---\nname: b\ndescription: shared\n---\nSHARED-B",
	})
	got := teamRoleSkillPrompt(root, true)
	for _, s := range []string{"LEADER-BASE", "SHARED-A", "SHARED-B", "LEADER-SPECIAL"} {
		if !strings.Contains(got, s) {
			t.Fatalf("leader prompt missing %q, got:\n%s", s, got)
		}
	}
	if !(roleIdx(got, "LEADER-BASE") < roleIdx(got, "SHARED-A") &&
		roleIdx(got, "SHARED-A") < roleIdx(got, "SHARED-B") &&
		roleIdx(got, "SHARED-B") < roleIdx(got, "LEADER-SPECIAL")) {
		t.Errorf("leader ordering violated, got:\n%s", got)
	}
}

// TestRoleSkillPromptWrapperNamesRole pins the framing contract: the block is
// wrapped in <team-role-skill name="<role>">, and the member block never
// carries the leader's role name (and vice versa).
func TestRoleSkillPromptWrapperNamesRole(t *testing.T) {
	root := t.TempDir()
	writeRoleSkillTree(t, root, map[string]string{
		"team/skills/base/leader/SKILL.md":    "---\nname: leader\ndescription: role\n---\nLB",
		"team/skills/base/member/SKILL.md":    "---\nname: member\ndescription: role\n---\nMB",
		"team/skills/special/leader/SKILL.md": "---\nname: leader\ndescription: role\n---\nLS",
		"team/skills/special/member/SKILL.md": "---\nname: member\ndescription: role\n---\nMS",
	})
	l := teamRoleSkillPrompt(root, true)
	if !strings.Contains(l, `<team-role-skill name="leader">`) {
		t.Errorf("leader prompt must declare name=\"leader\", got:\n%s", l)
	}
	if strings.Contains(l, `name="member"`) {
		t.Errorf("leader prompt must not declare name=\"member\", got:\n%s", l)
	}
	m := teamRoleSkillPrompt(root, false)
	if !strings.Contains(m, `<team-role-skill name="member">`) {
		t.Errorf("member prompt must declare name=\"member\", got:\n%s", m)
	}
	if strings.Contains(m, `name="leader"`) {
		t.Errorf("member prompt must not declare name=\"leader\", got:\n%s", m)
	}
}

// TestRoleSkillPromptToleratesEmptyAndMissingTrees pins that a workspace with
// no team skill tree (or an empty one) yields an empty block, never a partial
// tag or a panic — the loader stays a no-op for the optional playbooks.
func TestRoleSkillPromptToleratesEmptyAndMissingTrees(t *testing.T) {
	if got := teamRoleSkillPrompt(t.TempDir(), true); got != "" {
		t.Errorf("missing team/skills tree must yield empty prompt, got:\n%q", got)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "team", "skills", "shared", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := teamRoleSkillPrompt(root, false); got != "" {
		t.Errorf("empty skill dirs must yield empty prompt, got:\n%q", got)
	}
	if got := teamRoleSkillPrompt("", true); got != "" {
		t.Errorf("blank root must yield empty prompt, got:\n%q", got)
	}
}

// roleIdx returns the index of substr in s, or a large number when absent (so
// ordering assertions fail loudly on a missing marker).
func roleIdx(s, substr string) int {
	i := strings.Index(s, substr)
	if i < 0 {
		return 1 << 30
	}
	return i
}
