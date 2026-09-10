package boot

// Effect tests assert final-boundary behavior through the real Build stack
// (see effect_test.go). This file pins the /skills role seam on every public
// catalog surface of a team backend built with Options.TeamRole.

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/skill"
)

const effectTeamRoleProviderKind = "boot-effect-team-role"

// effectTeamRoleFixture writes a team/skills tree under root whose bodies and
// declarations exercise every admission rule the role seam enforces.
func effectTeamRoleFixture(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"team/skills/base/leader/SKILL.md":                   "---\nname: leader\ndescription: leader playbook\n---\nLDR-BASE",
		"team/skills/base/member/SKILL.md":                   "---\nname: member\ndescription: member playbook\n---\nMBR-BASE",
		"team/skills/shared/open/SKILL.md":                   "---\nname: open\ndescription: open to both\n---\nOPEN-BODY",
		"team/skills/shared/locked-leader/SKILL.md":          "---\nname: locked-leader\ndescription: leader only\nteam_role: leader\n---\nLEADER-LOCKED-BODY",
		"team/skills/shared/locked-member/SKILL.md":          "---\nname: locked-member\ndescription: member only\nteam_role: member\n---\nMEMBER-LOCKED-BODY",
		"team/skills/shared/broken/SKILL.md":                 "---\nname: broken\ndescription: bad declaration\nteam_role: coder\n---\nBROKEN-BODY",
		"team/skills/special/leader/special-leader/SKILL.md": "---\nname: special-leader\ndescription: leader special\nteam_role: leader\n---\nSP-LDR-BODY",
		"team/skills/special/member/special-member/SKILL.md": "---\nname: special-member\ndescription: member special\nteam_role: member\n---\nSP-MBR-BODY",
		"team/skills/special/leader/sp-broken/SKILL.md":      "---\nname: sp-broken\ndescription: broken special\nteam_role: coder\n---\nSP-BROKEN-BODY",
	}
	for rel, body := range files {
		writeFile(t, root, rel, body)
	}
}

// effectBuildTeamSkills drives the real Build stack: workspace is the launch
// workspace (Options.WorkspaceRoot, and where the config lives), teamSkillsRoot
// the user-global team root owning team/skills, teamRole the session's role.
func effectBuildTeamSkills(t *testing.T, workspace, teamSkillsRoot, teamRole string) *control.Controller {
	t.Helper()
	writeFile(t, workspace, "reasonix.toml", `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[[providers]]
name = "test-model"
kind = "`+effectTeamRoleProviderKind+`"
model = "x"
`)
	ctrl, err := Build(context.Background(), Options{
		Sink: event.Discard, WorkspaceRoot: workspace, TeamSkillsRoot: teamSkillsRoot,
		TeamRole: teamRole, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("Build(TeamRole=%q): %v", teamRole, err)
	}
	t.Cleanup(func() { ctrl.Close() })
	return ctrl
}

// effectTeamSkillNames filters a catalog to the team tree's own skills, which
// is what role scope constrains; built-ins and unrelated roots stay noise.
func effectTeamSkillNames(skills []skill.Skill) []string {
	var names []string
	for _, sk := range skills {
		if strings.Contains(sk.Path, string(filepath.Separator)+"team"+string(filepath.Separator)) {
			names = append(names, sk.Name)
		}
	}
	return names
}

// TestEffectTeamRoleScopesTeamSkillsCatalog drives the real Build stack for
// each role plus an unscoped session and asserts the controller surfaces
// /skills reads from — the model list, the slash list and the management list
// — all agree on the role's tree: base/<role>, every shared skill whose
// team_role admits the role, the role's own special/<role> skills, and nothing
// from the other role's branches or a malformed declaration. The team tree is
// the user-global one while the launch workspace owns a same-shaped tree of its
// own, so the catalog also pins that neither the workspace root nor the process
// cwd contributes a team skill.
func TestEffectTeamRoleScopesTeamSkillsCatalog(t *testing.T) {
	provider.Register(effectTeamRoleProviderKind, func(provider.Config) (provider.Provider, error) {
		return &effectRecordingProvider{}, nil
	})
	isolateConfigHome(t)
	userRoot := robustTempDir(t)
	effectTeamRoleFixture(t, userRoot)
	launch := robustTempDir(t)
	writeFile(t, launch, "team/skills/base/member/cwd-member/SKILL.md", "---\nname: cwd-member\ndescription: launch workspace playbook\n---\nCWD-MBR")
	writeFile(t, launch, "team/skills/base/leader/cwd-leader/SKILL.md", "---\nname: cwd-leader\ndescription: launch workspace playbook\n---\nCWD-LDR")
	t.Chdir(launch)

	member := effectBuildTeamSkills(t, launch, userRoot, "member")
	wantMember := []string{"locked-member", "member", "open", "special-member"}
	if got := effectTeamSkillNames(member.Skills()); !stringsEqual(got, wantMember) {
		t.Errorf("member Skills() = %v, want %v", got, wantMember)
	}
	if got := effectTeamSkillNames(member.SlashSkills()); !stringsEqual(got, wantMember) {
		t.Errorf("member SlashSkills() = %v, want %v", got, wantMember)
	}
	if got := effectTeamSkillNames(member.AllSkills()); !stringsEqual(got, wantMember) {
		t.Errorf("member AllSkills() must stay role-scoped too, got %v", got)
	}

	leader := effectBuildTeamSkills(t, launch, userRoot, "leader")
	wantLeader := []string{"leader", "locked-leader", "open", "special-leader"}
	if got := effectTeamSkillNames(leader.Skills()); !stringsEqual(got, wantLeader) {
		t.Errorf("leader Skills() = %v, want %v", got, wantLeader)
	}

	// An unscoped session reads the whole user tree; it is also the one session
	// shape that still discovers its own project's team tree, so it launches
	// from a directory owning none and contributes no distractor.
	unscoped := effectBuildTeamSkills(t, robustTempDir(t), userRoot, "")
	if got := effectTeamSkillNames(unscoped.Skills()); len(got) != 5 {
		t.Errorf("unscoped Skills() must keep the historical whole-tree view without special, got %v", got)
	}
	for _, sk := range append(append(member.Skills(), leader.Skills()...), unscoped.Skills()...) {
		if sk.Name == "broken" || sk.Name == "sp-broken" {
			t.Error("a malformed team_role declaration must stay closed in every session")
		}
		if sk.Name == "cwd-member" || sk.Name == "cwd-leader" {
			t.Errorf("the launch workspace's own team tree must not serve %q", sk.Name)
		}
	}
}

func stringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
