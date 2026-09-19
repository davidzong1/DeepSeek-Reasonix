package cli

// Byte-stability guard for the team member/Leader prompt identity: the
// composition below is what team_backend_build.go folds into
// boot.Options.SystemPromptIdentity, and so into every request's prefix.

import (
	"strings"
	"testing"

	"reasonix/internal/team"
)

// memberPromptIdentity is the production composition, named so the guard cannot
// drift away from the call site it covers.
func memberPromptIdentity(b team.MemberBinding, skillsRoot string) string {
	return memberSystemPromptIdentity(b) + teamRoleSkillPrompt(skillsRoot, b.Leader)
}

// memberPromptFixtureTree is a full role tree: both base playbooks, two shared
// skills, and a special playbook per role, so a leak across roles or an
// unstable scan order is visible.
func memberPromptFixtureTree(t *testing.T, root string) {
	t.Helper()
	writeRoleSkillTree(t, root, map[string]string{
		"team/skills/base/leader/SKILL.md":    "---\nname: leader\ndescription: role\n---\nLEADER-BASE",
		"team/skills/base/member/SKILL.md":    "---\nname: member\ndescription: role\n---\nMEMBER-BASE",
		"team/skills/shared/a/SKILL.md":       "---\nname: a\ndescription: shared\n---\nSHARED-A",
		"team/skills/shared/b/SKILL.md":       "---\nname: b\ndescription: shared\n---\nSHARED-B",
		"team/skills/special/leader/SKILL.md": "---\nname: leader\ndescription: role\n---\nLEADER-SPECIAL",
		"team/skills/special/member/SKILL.md": "---\nname: member\ndescription: role\n---\nMEMBER-SPECIAL",
	})
}

func leaderBinding() team.MemberBinding {
	return team.MemberBinding{Team: "alpha", MemberID: "lead", Role: "leader", Leader: true}
}

// TestMemberPromptIdentityIsByteStableAcrossAssemblies is the guard itself: two
// assemblies of the same binding over the same tree must compose identical
// bytes, and the result must actually carry the member identity and its role
// playbook — a deterministic empty string would pass a stability check alone.
func TestMemberPromptIdentityIsByteStableAcrossAssemblies(t *testing.T) {
	root := t.TempDir()
	memberPromptFixtureTree(t, root)
	b := leaderBinding()

	first := memberPromptIdentity(b, root)
	second := memberPromptIdentity(b, root)
	if first != second {
		t.Fatalf("member prompt identity is not byte-stable across assemblies:\nfirst  (%d bytes) %q\nsecond (%d bytes) %q",
			len(first), first, len(second), second)
	}
	for _, want := range []string{`"lead"`, `"alpha"`, "leader", "LEADER-BASE", "SHARED-A", "SHARED-B", "LEADER-SPECIAL"} {
		if !strings.Contains(first, want) {
			t.Errorf("member prompt identity %q is missing %q", first, want)
		}
	}
	// The role tree is role-scoped: the member's playbooks never enter a
	// leader's prefix, which would both leak and destabilise it.
	for _, unwanted := range []string{"MEMBER-BASE", "MEMBER-SPECIAL"} {
		if strings.Contains(first, unwanted) {
			t.Errorf("leader prompt identity leaked the member playbook %q:\n%s", unwanted, first)
		}
	}
}

// TestMemberPromptIdentityMovesWithEveryInput keeps the guard non-vacuous: each
// input the composition reads must move the bytes, so a future refactor that
// ignores one is caught rather than silently stable.
func TestMemberPromptIdentityMovesWithEveryInput(t *testing.T) {
	root := t.TempDir()
	memberPromptFixtureTree(t, root)
	base := memberPromptIdentity(leaderBinding(), root)

	for _, tc := range []struct {
		name string
		b    team.MemberBinding
	}{
		{"member id", team.MemberBinding{Team: "alpha", MemberID: "dev", Role: "leader", Leader: true}},
		{"team", team.MemberBinding{Team: "beta", MemberID: "lead", Role: "leader", Leader: true}},
		{"role", team.MemberBinding{Team: "alpha", MemberID: "lead", Role: "coder", Leader: true}},
		{"leader slot", team.MemberBinding{Team: "alpha", MemberID: "lead", Role: "leader"}},
	} {
		if got := memberPromptIdentity(tc.b, root); got == base {
			t.Errorf("%s change did not move the member prompt identity", tc.name)
		}
	}
	// An unset role renders the explicit hint instead of leaving it implied.
	unset := memberPromptIdentity(team.MemberBinding{Team: "alpha", MemberID: "lead", Leader: true}, root)
	if !strings.Contains(unset, "not configured") {
		t.Errorf("unset role must render the explicit hint, got %q", unset)
	}

	other := t.TempDir()
	writeRoleSkillTree(t, other, map[string]string{
		"team/skills/shared/a/SKILL.md": "---\nname: a\ndescription: shared\n---\nSHARED-A-CHANGED",
	})
	if changed := memberPromptIdentity(leaderBinding(), other); changed == base {
		t.Error("a skill body change did not move the member prompt identity")
	}
}

// TestMemberRoleSkillScanIsStableUnderBudgetDrops pins the drop path: an
// over-budget block is dropped whole and deterministically, and dropping it
// never evicts the skills after it — the two failure modes that would make one
// member's prefix depend on scan timing rather than its tree.
func TestMemberRoleSkillScanIsStableUnderBudgetDrops(t *testing.T) {
	root := t.TempDir()
	oversized := strings.Repeat("x", teamRoleSkillBudget+1024)
	writeRoleSkillTree(t, root, map[string]string{
		"team/skills/shared/a_oversized/SKILL.md": "---\nname: a_oversized\ndescription: shared\n---\n" + oversized,
		"team/skills/shared/b_after/SKILL.md":     "---\nname: b_after\ndescription: shared\n---\nAFTER-OVERSIZE",
		"team/skills/shared/c_last/SKILL.md":      "---\nname: c_last\ndescription: shared\n---\nLAST-SKILL",
	})
	first := teamRoleSkillPrompt(root, true)
	second := teamRoleSkillPrompt(root, true)
	if first != second {
		t.Fatalf("role skill scan is not byte-stable:\nfirst  %q\nsecond %q", first, second)
	}
	if strings.Contains(first, strings.Repeat("x", 64)) {
		t.Error("an over-budget block must be dropped whole, not truncated into the section")
	}
	for _, want := range []string{"AFTER-OVERSIZE", "LAST-SKILL"} {
		if !strings.Contains(first, want) {
			t.Errorf("dropping an over-budget block evicted %q:\n%s", want, first)
		}
	}
}
