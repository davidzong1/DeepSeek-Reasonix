package skill

import (
	"path/filepath"
	"strings"
	"testing"
)

// Special-branch pins for the role seam: a scoped store opens exactly
// special/<role>, admits it by declaration, and never scans the sibling
// role's branch. Fixture layout mirrors the shipped tree (<name>/SKILL.md).

// teamSpecialFixture writes the special branches role scope opens. Valid
// role-declared skills sit next to a same-branch skill declaring the other
// role (silently closed) and one declaring a non-allowlisted role (closed with
// a warning naming the file). Bodies are load-path markers.
func teamSpecialFixture(t *testing.T, proj string) {
	t.Helper()
	writeSkill(t, proj, "team/skills/special/leader/leader-tool/SKILL.md",
		"---\nname: leader-tool\ndescription: leader role skill\nteam_role: leader\n---\nTOOL-LDR")
	writeSkill(t, proj, "team/skills/special/leader/leader-silent/SKILL.md",
		"---\nname: leader-silent\ndescription: leader misdecl\nteam_role: member\n---\nSILENT-LDR")
	writeSkill(t, proj, "team/skills/special/leader/leader-broken/SKILL.md",
		"---\nname: leader-broken\ndescription: leader bad decl\nteam_role: coder\n---\nBROKEN-LDR")
	writeSkill(t, proj, "team/skills/special/member/member-tool/SKILL.md",
		"---\nname: member-tool\ndescription: member role skill\nteam_role: member\n---\nTOOL-MBR")
	writeSkill(t, proj, "team/skills/special/member/member-silent/SKILL.md",
		"---\nname: member-silent\ndescription: member misdecl\nteam_role: leader\n---\nSILENT-MBR")
	writeSkill(t, proj, "team/skills/special/member/member-broken/SKILL.md",
		"---\nname: member-broken\ndescription: member bad decl\nteam_role: coder\n---\nBROKEN-MBR")
}

// TestTeamRoleScopeMemberOpensOwnSpecialBranchOnly pins the member half of the
// special-branch contract: special/member's own skills join the catalog (with
// base and shared), a special sibling declaring the other role drops silently,
// a malformed declaration closes with a warning, and nothing under
// special/leader is ever scanned — no skill, no warning.
func TestTeamRoleScopeMemberOpensOwnSpecialBranchOnly(t *testing.T) {
	proj := t.TempDir()
	teamRoleFixture(t, proj)
	teamSpecialFixture(t, proj)
	st, warn := roleScopedStore(t, proj, TeamRoleMember)

	want := []string{"locked-member", "member", "member-tool", "open"}
	if got := projectNames(st); !equalStrings(got, want) {
		t.Fatalf("member list = %v, want %v", got, want)
	}
	tool, ok := find(st.List(), "member-tool")
	if !ok || !strings.HasSuffix(tool.Path, filepath.Join("team", "skills", "special", "member", "member-tool", "SKILL.md")) {
		t.Fatalf("member-tool must load special/member's file, got %+v", tool)
	}
	if !strings.Contains(tool.Body, "TOOL-MBR") || strings.Contains(tool.Body, "TOOL-LDR") {
		t.Fatalf("member-tool body is wrong: %q", tool.Body)
	}
	for _, name := range want {
		if _, ok := st.Read(name); !ok {
			t.Errorf("listed skill %q must be invocable by name", name)
		}
		if _, ok := st.ReadSlash("/" + name); !ok {
			t.Errorf("listed skill %q must be invocable by slash name", name)
		}
	}
	for _, closed := range []string{"leader-tool", "leader-silent", "leader-broken", "member-silent", "member-broken"} {
		if _, ok := st.Read(closed); ok {
			t.Errorf("closed skill %q must not resolve by name", closed)
		}
	}
	for _, forbidden := range []string{"leader-tool", "leader-broken", "leader-silent", "member-silent"} {
		if strings.Contains(warn.String(), forbidden) {
			t.Errorf("warnings must not mention %q, got:\n%s", forbidden, warn.String())
		}
	}
	if !strings.Contains(warn.String(), "member-broken") || !strings.Contains(warn.String(), `invalid team_role "coder"`) {
		t.Fatalf("warning must name member-broken's declaration, got:\n%s", warn.String())
	}
}

// TestTeamRoleScopeLeaderOpensOwnSpecialBranchOnly is the leader half of the
// same contract: special/leader's own skills are listed and the member's
// special branch never is.
func TestTeamRoleScopeLeaderOpensOwnSpecialBranchOnly(t *testing.T) {
	proj := t.TempDir()
	teamRoleFixture(t, proj)
	teamSpecialFixture(t, proj)
	st, warn := roleScopedStore(t, proj, TeamRoleLeader)

	want := []string{"leader", "leader-tool", "locked-leader", "open"}
	if got := projectNames(st); !equalStrings(got, want) {
		t.Fatalf("leader list = %v, want %v", got, want)
	}
	tool, ok := find(st.List(), "leader-tool")
	if !ok || !strings.HasSuffix(tool.Path, filepath.Join("team", "skills", "special", "leader", "leader-tool", "SKILL.md")) {
		t.Fatalf("leader-tool must load special/leader's file, got %+v", tool)
	}
	if !strings.Contains(tool.Body, "TOOL-LDR") || strings.Contains(tool.Body, "TOOL-MBR") {
		t.Fatalf("leader-tool body is wrong: %q", tool.Body)
	}
	for _, closed := range []string{"member-tool", "member-broken", "leader-silent", "leader-broken"} {
		if _, ok := st.Read(closed); ok {
			t.Errorf("closed skill %q must not resolve by name", closed)
		}
	}
	if !strings.Contains(warn.String(), "leader-broken") || !strings.Contains(warn.String(), `invalid team_role "coder"`) {
		t.Fatalf("warning must name leader-broken's declaration, got:\n%s", warn.String())
	}
	for _, forbidden := range []string{"member-tool", "member-broken", "member-silent"} {
		if strings.Contains(warn.String(), forbidden) {
			t.Errorf("warnings must not mention %q, got:\n%s", forbidden, warn.String())
		}
	}
}

// TestTeamRoleScopeUnscopedKeepsSpecialBranchClosed pins that the special
// branch stays on the discovery skip-list for every non-team session: an
// unscoped store over the same tree lists exactly the historical base+shared
// set and never scans special/<role> — not even to warn about its broken
// declaration.
func TestTeamRoleScopeUnscopedKeepsSpecialBranchClosed(t *testing.T) {
	proj := t.TempDir()
	teamRoleFixture(t, proj)
	teamSpecialFixture(t, proj)
	st, warn := roleScopedStore(t, proj, "")

	want := []string{"leader", "locked-leader", "locked-member", "member", "open"}
	if got := projectNames(st); !equalStrings(got, want) {
		t.Fatalf("unscoped list = %v, want %v", got, want)
	}
	for _, closed := range []string{"leader-tool", "leader-broken", "member-tool", "member-broken"} {
		if _, ok := st.Read(closed); ok {
			t.Errorf("special skill %q must stay closed for unscoped sessions", closed)
		}
	}
	if strings.Contains(warn.String(), "special") {
		t.Fatalf("unscoped store must not scan special at all, got:\n%s", warn.String())
	}
}
