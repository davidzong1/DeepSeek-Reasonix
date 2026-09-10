package skill

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// teamRoleFixture writes the full team/skills tree role scoping cares about:
// both base playbooks, an open shared skill, one shared skill locked to each
// role, and a shared skill whose team_role value is not on the allowlist.
// Bodies double as load-path markers (asserting a skill listed is the file a
// role playbook loader would read).
func teamRoleFixture(t *testing.T, proj string) {
	t.Helper()
	writeSkill(t, proj, "team/skills/base/leader/SKILL.md", "---\nname: leader\ndescription: leader playbook\n---\nLDR-BASE")
	writeSkill(t, proj, "team/skills/base/member/SKILL.md", "---\nname: member\ndescription: member playbook\n---\nMBR-BASE")
	writeSkill(t, proj, "team/skills/shared/open/SKILL.md", "---\nname: open\ndescription: open to both\n---\nOPEN-BODY")
	writeSkill(t, proj, "team/skills/shared/locked-leader/SKILL.md", "---\nname: locked-leader\ndescription: leader only\nteam_role: leader\n---\nLEADER-LOCKED-BODY")
	writeSkill(t, proj, "team/skills/shared/locked-member/SKILL.md", "---\nname: locked-member\ndescription: member only\nteam_role: member\n---\nMEMBER-LOCKED-BODY")
	writeSkill(t, proj, "team/skills/shared/broken/SKILL.md", "---\nname: broken\ndescription: bad declaration\nteam_role: coder\n---\nBROKEN-BODY")
}

// roleScopedStore builds a store over base's team tree — the tree a team
// session reads. It arrives through TeamSkillsRoot, the user-global root, with
// the project root pointed at an unrelated directory: a store still reading the
// project's own team/skills (or the cwd) could not find this fixture, so every
// assertion below doubles as a pin that the team tree root is the one given.
func roleScopedStore(t *testing.T, base, role string) (*Store, *bytes.Buffer) {
	t.Helper()
	var warn bytes.Buffer
	st := New(Options{
		HomeDir: t.TempDir(), ProjectRoot: t.TempDir(), TeamSkillsRoot: base,
		DisableBuiltins: true, TeamRole: role, Stderr: &warn,
	})
	return st, &warn
}

func projectNames(st *Store) []string {
	var names []string
	for _, sk := range st.List() {
		names = append(names, sk.Name)
	}
	return names
}

// TestTeamRoleScopeMemberSeesOwnBaseAndSharedOnly pins the member half of the
// /skills contract: the member's base playbook and every shared skill whose
// team_role admits a member are listed, the leader's base branch is closed at
// directory level, a shared skill locked to the leader is dropped silently,
// and a skill with a non-allowlisted team_role is closed for the member with a
// warning. Slash listing and by-name lookup resolve the same set, so what the
// UI offers and what invocation can load always agree.
func TestTeamRoleScopeMemberSeesOwnBaseAndSharedOnly(t *testing.T) {
	proj := t.TempDir()
	teamRoleFixture(t, proj)
	st, warn := roleScopedStore(t, proj, TeamRoleMember)

	want := []string{"locked-member", "member", "open"}
	if got := projectNames(st); !equalStrings(got, want) {
		t.Fatalf("member list = %v, want %v", got, want)
	}
	base, ok := find(st.List(), "member")
	if !ok || !strings.HasSuffix(base.Path, filepath.Join("team", "skills", "base", "member", "SKILL.md")) {
		t.Fatalf("member skill must load the base/member playbook file, got %+v", base)
	}
	if strings.Contains(base.Body, "LDR-BASE") || !strings.Contains(base.Body, "MBR-BASE") {
		t.Fatalf("member playbook body is wrong: %q", base.Body)
	}

	slash := st.SlashList()
	if len(slash) != len(want) {
		t.Fatalf("member slash list = %d entries, want %d", len(slash), len(want))
	}
	for _, name := range want {
		if _, ok := st.Read(name); !ok {
			t.Errorf("listed skill %q must be invocable by name", name)
		}
		if _, ok := st.ReadSlash(name); !ok {
			t.Errorf("listed skill %q must be invocable by slash name", name)
		}
	}
	for _, closed := range []string{"leader", "locked-leader", "broken"} {
		if _, ok := st.Read(closed); ok {
			t.Errorf("closed skill %q must not resolve by name", closed)
		}
		if _, ok := st.ReadSlash("/" + closed); ok {
			t.Errorf("closed skill %q must not resolve by slash name", closed)
		}
	}
	if !strings.Contains(warn.String(), `team role skill `) || !strings.Contains(warn.String(), `invalid team_role "coder"`) || strings.Contains(warn.String(), "locked-leader") {
		t.Fatalf("warning must name the broken declaration only, got:\n%s", warn.String())
	}
}

// TestTeamRoleScopeLeaderSeesOwnBaseAndSharedOnly is the leader half of the
// same contract; the two roles' listings are mirrors of each other.
func TestTeamRoleScopeLeaderSeesOwnBaseAndSharedOnly(t *testing.T) {
	proj := t.TempDir()
	teamRoleFixture(t, proj)
	st, warn := roleScopedStore(t, proj, TeamRoleLeader)

	want := []string{"leader", "locked-leader", "open"}
	if got := projectNames(st); !equalStrings(got, want) {
		t.Fatalf("leader list = %v, want %v", got, want)
	}
	if _, ok := st.Read("member"); ok {
		t.Error("leader session must not resolve the member base playbook")
	}
	if _, ok := st.Read("locked-member"); ok {
		t.Error("shared skill locked to member must not resolve in a leader session")
	}
	if !strings.Contains(warn.String(), "broken") {
		t.Fatalf("leader store must warn about the invalid declaration, got:\n%s", warn.String())
	}
}

// TestTeamRoleScopeWithoutUserTreeStaysClosed pins the cwd independence
// invariant: a role-scoped store that was given no user-global team root reads
// no team tree at all, even when the project root it was handed owns one. A
// team session's playbooks must never depend on the directory reasonix was
// launched from.
func TestTeamRoleScopeWithoutUserTreeStaysClosed(t *testing.T) {
	proj := t.TempDir()
	teamRoleFixture(t, proj)
	for _, role := range []string{TeamRoleMember, TeamRoleLeader} {
		st := New(Options{HomeDir: t.TempDir(), ProjectRoot: proj, DisableBuiltins: true, TeamRole: role, Stderr: io.Discard})
		if got := projectNames(st); len(got) != 0 {
			t.Errorf("role %q without a user team root must see no team skills, got %v", role, got)
		}
	}
}

// TestTeamRoleScopeUnscopedKeepsHistoricalDiscovery pins that ordinary (non
// team-role) sessions keep seeing the whole project team tree: both base
// playbooks and every shared skill whose declaration is on the allowlist. Only
// a malformed declaration closes a skill — for every session — with a warning.
func TestTeamRoleScopeUnscopedKeepsHistoricalDiscovery(t *testing.T) {
	proj := t.TempDir()
	teamRoleFixture(t, proj)
	var warn bytes.Buffer
	st := New(Options{HomeDir: t.TempDir(), ProjectRoot: proj, DisableBuiltins: true, Stderr: &warn})

	want := []string{"leader", "locked-leader", "locked-member", "member", "open"}
	if got := projectNames(st); !equalStrings(got, want) {
		t.Fatalf("unscoped list = %v, want %v", got, want)
	}
	if _, ok := st.Read("broken"); ok {
		t.Error("skill with a non-allowlisted team_role must be closed for unscoped sessions too")
	}
	if !strings.Contains(warn.String(), `invalid team_role "coder"`) {
		t.Fatalf("unscoped store must warn about the invalid declaration, got:\n%s", warn.String())
	}
}

// TestTeamRoleScopeRejectsNonAllowlistSessionRole pins fail-closed behavior
// for a session role outside the allowlist: it cannot be scoped to a branch,
// so the whole team tree is closed and the construction warns.
func TestTeamRoleScopeRejectsNonAllowlistSessionRole(t *testing.T) {
	proj := t.TempDir()
	teamRoleFixture(t, proj)
	st, warn := roleScopedStore(t, proj, "coder")
	if got := projectNames(st); len(got) != 0 {
		t.Fatalf("non-allowlist session role must see no team skills, got %v", got)
	}
	if !strings.Contains(warn.String(), `team role "coder" is not leader or member`) {
		t.Fatalf("construction must warn about the session role, got:\n%s", warn.String())
	}
}

// TestTeamRoleSkillsReportsInstallStateIgnoringEnabled pins the install-state
// seam the gap diagnostic asks through: every playbook the role's team scope
// admits is reported even when the user turned that playbook off — a disable is
// a preference, not an uninstall — while the other role's branch stays closed.
// A catalog-only answer would report an installed tree as empty.
func TestTeamRoleSkillsReportsInstallStateIgnoringEnabled(t *testing.T) {
	proj := t.TempDir()
	teamRoleFixture(t, proj)
	var warn bytes.Buffer
	st := New(Options{
		HomeDir: t.TempDir(), ProjectRoot: t.TempDir(), TeamSkillsRoot: proj,
		DisableBuiltins: true, TeamRole: TeamRoleLeader, Stderr: &warn,
		DisabledNames: []string{"leader"},
	})

	var names []string
	for _, sk := range st.TeamRoleSkills() {
		names = append(names, sk.Name)
	}
	if want := []string{"leader", "locked-leader", "open"}; !equalStrings(names, want) {
		t.Fatalf("install-state skills = %v, want %v", names, want)
	}
	if got := projectNames(st); !equalStrings(got, []string{"locked-leader", "open"}) {
		t.Fatalf("the disabled playbook must stay out of the listed catalog, got %v", got)
	}

	empty := New(Options{
		HomeDir: t.TempDir(), ProjectRoot: t.TempDir(), TeamSkillsRoot: t.TempDir(),
		DisableBuiltins: true, TeamRole: TeamRoleLeader, Stderr: &warn,
	})
	if got := empty.TeamRoleSkills(); len(got) != 0 {
		t.Fatalf("a root holding no team tree must report nothing installed, got %v", got)
	}
}

func equalStrings(a, b []string) bool {
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
