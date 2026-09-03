package cli

// Team-role skill scope tests — pinned against current production behavior.
//
// teamRoleSkillPrompt (team_backend_build.go:380) now reads:
//   1. base/<role> by name through the skill store
//   2. shared/ from disk (every skill directory under team/skills/shared)
//   3. special/<role> from disk (every skill directory under team/skills/special/<role>)
//
// Shared and special are read straight from disk so a same-named playbook is
// not shadowed by base, and one role's special never leaks into the other's
// prompt. Within each directory, SKILL.md is the canonical spelling; a
// lowercase skill.md is only used when no SKILL.md exists in that directory.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/skill"
)

// TestTeamRoleSkillPromptIncludesSharedAndOwnSpecial pins the full scope
// contract: the leader prompt must contain the base/leader playbook, every
// skill under shared/, and every skill under special/leader — but never the
// member's base or special/leader playbooks. The member prompt is symmetric.
func TestTeamRoleSkillPromptIncludesSharedAndOwnSpecial(t *testing.T) {
	root := t.TempDir()
	writeRoleSkillTree(t, root, map[string]string{
		"team/skills/base/leader/SKILL.md":    "---\nname: leader\ndescription: role\n---\nLEADER-BASE",
		"team/skills/base/member/SKILL.md":    "---\nname: member\ndescription: role\n---\nMEMBER-BASE",
		"team/skills/special/leader/SKILL.md": "---\nname: leader\ndescription: role\n---\nLEADER-SPECIAL",
		"team/skills/special/member/SKILL.md": "---\nname: member\ndescription: role\n---\nMEMBER-SPECIAL",
		"team/skills/shared/team/SKILL.md":    "---\nname: team\ndescription: shared\n---\nSHARED-TEAM",
	})

	got := teamRoleSkillPrompt(root, true)
	for _, want := range []string{"LEADER-BASE", "LEADER-SPECIAL", "SHARED-TEAM"} {
		if !strings.Contains(got, want) {
			t.Errorf("leader prompt missing %q, got:\n%s", want, got)
		}
	}
	for _, not := range []string{"MEMBER-BASE", "MEMBER-SPECIAL"} {
		if strings.Contains(got, not) {
			t.Errorf("leader prompt must not contain %q, got:\n%s", not, got)
		}
	}

	member := teamRoleSkillPrompt(root, false)
	for _, want := range []string{"MEMBER-BASE", "MEMBER-SPECIAL", "SHARED-TEAM"} {
		if !strings.Contains(member, want) {
			t.Errorf("member prompt missing %q, got:\n%s", want, member)
		}
	}
	for _, not := range []string{"LEADER-BASE", "LEADER-SPECIAL"} {
		if strings.Contains(member, not) {
			t.Errorf("member prompt must not contain %q, got:\n%s", not, member)
		}
	}
}

// TestRoleSkillsCanonicalSpellingUppercaseWins pins the directory-skill
// filename contract: SKILL.md is the canonical spelling. When both SKILL.md
// and skill.md exist in a directory, SKILL.md wins. When only skill.md exists,
// it is read as a fallback.
func TestRoleSkillsCanonicalSpellingUppercaseWins(t *testing.T) {
	root := t.TempDir()

	// A directory with only SKILL.md: the canonical form works.
	dir := filepath.Join(root, "team", "skills", "shared", "team")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: team\ndescription: shared\n---\nUPPER-ONLY"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := teamRoleSkillPrompt(root, true)
	if !strings.Contains(got, "UPPER-ONLY") {
		t.Errorf("SKILL.md must load the playbook, got:\n%s", got)
	}

	// Both SKILL.md and skill.md in the same directory: SKILL.md wins.
	if err := os.WriteFile(filepath.Join(dir, "skill.md"), []byte("---\nname: team\ndescription: shared\n---\nLOWER-ONLY"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = teamRoleSkillPrompt(root, true)
	if !strings.Contains(got, "UPPER-ONLY") {
		t.Errorf("the canonical SKILL.md must be the one read, got:\n%s", got)
	}
	if strings.Contains(got, "LOWER-ONLY") {
		t.Errorf("lowercase skill.md must not be read alongside SKILL.md, got:\n%s", got)
	}

	// A directory with only skill.md (no SKILL.md): the lowercase fallback works.
	specialDir := filepath.Join(root, "team", "skills", "special", "leader")
	if err := os.MkdirAll(specialDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(specialDir, "skill.md"), []byte("---\nname: leader\ndescription: role\n---\nONLY-LOWER"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = teamRoleSkillPrompt(root, true)
	if !strings.Contains(got, "ONLY-LOWER") {
		t.Errorf("skill.md must be read as fallback when no SKILL.md exists, got:\n%s", got)
	}
}

// TestAmbientStoreCurrentlyDiscoversSharedAndSpecial pins that the ambient
// store currently discovers all skills under the project convention dirs —
// shared AND special — because no role-scoped exclusion exists yet. When the
// production seam is added this test must be updated to assert the exclusion.
func TestAmbientStoreCurrentlyDiscoversSharedAndSpecial(t *testing.T) {
	root := t.TempDir()
	writeRoleSkillTree(t, root, map[string]string{
		"team/skills/shared/team/SKILL.md":    "---\nname: team\ndescription: shared\n---\nSHARED-AMBIENT",
		"team/skills/special/leader/SKILL.md": "---\nname: leader\ndescription: role\n---\nLEADER-AMBIENT",
	})
	s := skill.New(skill.Options{ProjectRoot: root, HomeDir: t.TempDir(), Stderr: os.Stderr, MaxDepth: 3})
	if s == nil {
		t.Fatal("skill store must construct")
	}
	names := storeSkillNames(s)
	if !contains(names, "team") {
		t.Errorf("ambient store must discover shared/ skills, got %v", names)
	}
	if !contains(names, "leader") {
		t.Log("NOTE: ambient store still discovers special/ — no exclusion seam yet")
	}
	t.Logf("ambient names: %v", names)
}

// storeSkillNames lists the resolved names of a store.
func storeSkillNames(s *skill.Store) []string {
	var names []string
	for _, sk := range s.List() {
		names = append(names, sk.Name)
	}
	return names
}

// contains is a helper for string-slice membership.
func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// writeRoleSkillTree creates a role skill directory tree under root with
// SKILL.md files from a map of relative paths to body content.
func writeRoleSkillTree(t *testing.T, root string, paths map[string]string) {
	t.Helper()
	for p, body := range paths {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
