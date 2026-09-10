package cli

// Parity pins between the role playbook the prompt loader assembles and the
// /skills catalog a role-scoped session store lists: the two surfaces must
// admit exactly the same team/skills files for the same role.

import (
	"bytes"
	"strings"
	"testing"

	"reasonix/internal/skill"
)

// TestRoleSkillPromptAndScopedCatalogAdmitSameTree loads one team/skills tree
// through both production seams — the prompt loader (teamRoleSkillPrompt,
// which feeds base/<role>, shared and special/<role> into the system prompt)
// and the role-scoped skill store backing /skills — and proves they resolve
// the same base playbook, the same shared subset and the same special branch.
// A skill declaring the other role or a non-allowlisted role — in shared or in
// the role's own special branch — stays out of both surfaces, and the invalid
// declaration warns on both.
func TestRoleSkillPromptAndScopedCatalogAdmitSameTree(t *testing.T) {
	root := t.TempDir()
	writeRoleSkillTree(t, root, map[string]string{
		"team/skills/base/leader/SKILL.md":                   "---\nname: leader\ndescription: leader playbook\n---\nLDR-BASE",
		"team/skills/base/member/SKILL.md":                   "---\nname: member\ndescription: member playbook\n---\nMBR-BASE",
		"team/skills/shared/open/SKILL.md":                   "---\nname: open\ndescription: open to both\n---\nOPEN-BODY",
		"team/skills/shared/locked-leader/SKILL.md":          "---\nname: locked-leader\ndescription: leader only\nteam_role: leader\n---\nLEADER-LOCKED-BODY",
		"team/skills/shared/locked-member/SKILL.md":          "---\nname: locked-member\ndescription: member only\nteam_role: member\n---\nMEMBER-LOCKED-BODY",
		"team/skills/shared/broken/SKILL.md":                 "---\nname: broken\ndescription: bad declaration\nteam_role: coder\n---\nBROKEN-BODY",
		"team/skills/special/leader/special-leader/SKILL.md": "---\nname: special-leader\ndescription: leader special\nteam_role: leader\n---\nSPC-LDR-BODY",
		"team/skills/special/leader/special-locked/SKILL.md": "---\nname: special-locked\ndescription: misdeclared special\nteam_role: member\n---\nSPC-LOCKED-BODY",
		"team/skills/special/leader/sp-broken/SKILL.md":      "---\nname: sp-broken\ndescription: broken special\nteam_role: coder\n---\nSPC-BROKEN-BODY",
		"team/skills/special/member/special-member/SKILL.md": "---\nname: special-member\ndescription: member special\nteam_role: member\n---\nSPC-MBR-BODY",
	})

	cases := []struct {
		name        string
		leader      bool
		wantNames   []string
		closedNames []string
		promptIn    []string
		promptOut   []string
	}{
		{name: "leader", leader: true,
			wantNames:   []string{"leader", "locked-leader", "open", "special-leader"},
			closedNames: []string{"member", "special-member", "special-locked", "sp-broken"},
			promptIn:    []string{"LDR-BASE", "OPEN-BODY", "LEADER-LOCKED-BODY", "SPC-LDR-BODY"},
			promptOut:   []string{"MBR-BASE", "MEMBER-LOCKED-BODY", "BROKEN-BODY", "SPC-MBR-BODY", "SPC-LOCKED-BODY", "SPC-BROKEN-BODY"}},
		{name: "member", leader: false,
			wantNames:   []string{"locked-member", "member", "open", "special-member"},
			closedNames: []string{"leader", "special-leader", "special-locked", "sp-broken"},
			promptIn:    []string{"MBR-BASE", "OPEN-BODY", "MEMBER-LOCKED-BODY", "SPC-MBR-BODY"},
			promptOut:   []string{"LDR-BASE", "LEADER-LOCKED-BODY", "BROKEN-BODY", "SPC-LDR-BODY", "SPC-LOCKED-BODY", "SPC-BROKEN-BODY"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var loaderWarn bytes.Buffer
			prompt := teamRoleSkillPrompt(root, tc.leader, &loaderWarn)
			for _, body := range tc.promptIn {
				if !strings.Contains(prompt, body) {
					t.Errorf("role prompt missing %q", body)
				}
			}
			for _, body := range tc.promptOut {
				if strings.Contains(prompt, body) {
					t.Errorf("role prompt must not contain %q", body)
				}
			}
			if !strings.Contains(loaderWarn.String(), "broken") || strings.Contains(loaderWarn.String(), "locked-") {
				t.Fatalf("loader warning must name only the invalid declaration, got:\n%s", loaderWarn.String())
			}

			var storeWarn bytes.Buffer
			st := skill.New(skill.Options{HomeDir: t.TempDir(), ProjectRoot: t.TempDir(), TeamSkillsRoot: root, DisableBuiltins: true, TeamRole: tc.name, Stderr: &storeWarn})
			var names []string
			for _, sk := range st.List() {
				names = append(names, sk.Name)
			}
			if strings.Join(names, ",") != strings.Join(tc.wantNames, ",") {
				t.Fatalf("scoped catalog names = %v, want %v", names, tc.wantNames)
			}
			for _, name := range tc.wantNames {
				if _, ok := st.Read(name); !ok {
					t.Errorf("catalog must resolve listed special skill %q by name", name)
				}
			}
			for _, closed := range tc.closedNames {
				if _, ok := st.Read(closed); ok {
					t.Errorf("catalog must not resolve closed skill %q", closed)
				}
			}
			if !strings.Contains(storeWarn.String(), "broken") {
				t.Fatalf("scoped store must warn about the invalid declaration, got:\n%s", storeWarn.String())
			}
			if tc.leader && !strings.Contains(storeWarn.String(), "sp-broken") {
				t.Fatalf("leader store must warn about the special-branch declaration, got:\n%s", storeWarn.String())
			}
			if !tc.leader && strings.Contains(storeWarn.String(), "special-leader") {
				t.Fatalf("member store must never scan the leader's special branch, got:\n%s", storeWarn.String())
			}
		})
	}
}
