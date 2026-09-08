package cli

// Frontmatter team_role filter tests: the four declared behaviors a SKILL.md
// team_role value must produce in each role's assembled section, exercised
// through the production loader with a captured warning writer.

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// TestTeamRoleDeclarationFiltersSkills pins the frontmatter team_role gate
// over the disk-scanned branches: a leader declaration loads only into the
// leader section, a member declaration only into the member section, no
// declaration loads into both, and an invalid value loads into neither while
// one warning per role records the skill path and the offending value. Base
// playbooks keep loading for their own role regardless of declarations.
func TestTeamRoleDeclarationFiltersSkills(t *testing.T) {
	root := t.TempDir()
	writeRoleSkillTree(t, root, map[string]string{
		"team/skills/base/leader/SKILL.md":        "---\nname: leader\ndescription: role\n---\nLB",
		"team/skills/base/member/SKILL.md":        "---\nname: member\ndescription: role\n---\nMB",
		"team/skills/shared/leader-lock/SKILL.md": "---\nteam_role: leader\n---\nLEADER-ONLY",
		"team/skills/shared/member-lock/SKILL.md": "---\nteam_role: member\n---\nMEMBER-ONLY",
		"team/skills/shared/open/SKILL.md":        "---\nname: open\n---\nOPEN-TO-BOTH",
		"team/skills/shared/bad/SKILL.md":         "---\nteam_role: coder\n---\nINVALID-ROLE",
		"team/skills/special/leader/SKILL.md":     "---\nteam_role: leader\n---\nLS",
		"team/skills/special/member/SKILL.md":     "---\nteam_role: member\n---\nMS",
	})
	var lw, mw bytes.Buffer
	leader := teamRoleSkillPrompt(root, true, &lw)
	for _, want := range []string{"LB", "LEADER-ONLY", "OPEN-TO-BOTH", "LS"} {
		if !strings.Contains(leader, want) {
			t.Fatalf("leader prompt missing %q:\n%s", want, leader)
		}
	}
	for _, banned := range []string{"MEMBER-ONLY", "INVALID-ROLE", "MS"} {
		if strings.Contains(leader, banned) {
			t.Fatalf("leader prompt leaks %q:\n%s", banned, leader)
		}
	}
	member := teamRoleSkillPrompt(root, false, &mw)
	for _, want := range []string{"MB", "MEMBER-ONLY", "OPEN-TO-BOTH", "MS"} {
		if !strings.Contains(member, want) {
			t.Fatalf("member prompt missing %q:\n%s", want, member)
		}
	}
	for _, banned := range []string{"LEADER-ONLY", "INVALID-ROLE", "LS"} {
		if strings.Contains(member, banned) {
			t.Fatalf("member prompt leaks %q:\n%s", banned, member)
		}
	}
	badPath := filepath.Join("team", "skills", "shared", "bad", "SKILL.md")
	for _, buf := range []*bytes.Buffer{&lw, &mw} {
		out := buf.String()
		if !strings.Contains(out, "coder") || !strings.Contains(out, badPath) {
			t.Fatalf("invalid team_role must warn with the path and value, got:\n%s", out)
		}
	}
}

// TestTeamRoleDeclarationAbsentKeepsHistoricalLoad pins the compatibility
// contract: the pre-declaration tree (no team_role keys anywhere) loads exactly
// as before for both roles, and an absent writer stays silent.
func TestTeamRoleDeclarationAbsentKeepsHistoricalLoad(t *testing.T) {
	root := t.TempDir()
	writeRoleSkillTree(t, root, map[string]string{
		"team/skills/base/leader/SKILL.md":    "---\nname: leader\ndescription: role\n---\nLB",
		"team/skills/shared/neutral/SKILL.md": "---\nname: neutral\n---\nNEUTRAL",
		"team/skills/special/member/SKILL.md": "---\nname: member\n---\nMS",
	})
	leader := teamRoleSkillPrompt(root, true)
	if !strings.Contains(leader, "LB") || !strings.Contains(leader, "NEUTRAL") {
		t.Fatalf("leader prompt regressed without declarations:\n%s", leader)
	}
	if strings.Contains(leader, "MS") {
		t.Fatalf("special/member leaked into the leader prompt:\n%s", leader)
	}
	member := teamRoleSkillPrompt(root, false)
	if !strings.Contains(member, "MS") || !strings.Contains(member, "NEUTRAL") {
		t.Fatalf("member prompt regressed without declarations:\n%s", member)
	}
	if strings.Contains(member, "LB") {
		t.Fatalf("base/leader leaked into the member prompt:\n%s", member)
	}
}
