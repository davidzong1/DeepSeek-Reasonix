package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/boot"
	"reasonix/internal/skill"
	"reasonix/internal/team"
)

// catalogUnder names the skills whose resolved path lives under root.
func catalogUnder(skills []skill.Skill, root string) map[string]bool {
	out := map[string]bool{}
	for _, s := range skills {
		if s.Path != "" && strings.HasPrefix(s.Path, root) {
			out[s.Name] = true
		}
	}
	return out
}

// wantCatalog compares a path-scoped catalog against a wanted set,
// order-insensitively and by membership.
func wantCatalog(got map[string]bool, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, w := range want {
		if !got[w] {
			return false
		}
	}
	return true
}

// teamFixtureDoc seeds an empty registry at root/.reasonix and chdirs into
// root, mirroring writeTeamFixture for a fixture that owns a team tree.
func teamFixtureDoc(t *testing.T, root string, teams ...team.Team) {
	t.Helper()
	t.Setenv("REASONIX_STATE_HOME", filepath.Join(root, ".reasonix"))
	t.Setenv("REASONIX_HOME", "")
	store, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	doc := team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}}
	if len(teams) > 0 {
		doc.Teams = teams
	}
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
}

// memberBuildDeps assembles the member-backend dependency set over a fake pool,
// with the given launch workspace.
func memberBuildDeps(t *testing.T, workspace string) memberBackendDeps {
	t.Helper()
	return memberBackendDeps{
		ctx: t.Context(),
		users: fakePool{users: map[string]team.AgentUser{
			"leader-user": {UserID: "leader-user", Provider: "openai", Model: "gpt-5.6", BaseURL: "https://example.invalid/v1", APIKey: "test-key"},
			"member-user": {UserID: "member-user", Provider: "openai", Model: "gpt-5.6", BaseURL: "https://example.invalid/v1", APIKey: "test-key"},
		}},
		events:        make(chan memberEvent, 1),
		workspaceRoot: workspace,
		base: func() boot.Options {
			return boot.Options{SessionDir: t.TempDir(), Stderr: io.Discard}
		},
	}
}

// allMemberSkills is every catalog surface a member backend exposes.
func allMemberSkills(ctrl interface {
	AllSkills() []skill.Skill
	SlashSkills() []skill.Skill
}) []skill.Skill {
	return append(append([]skill.Skill{}, ctrl.AllSkills()...), ctrl.SlashSkills()...)
}

// TestTeamSkillsBaseFollowsStateRootPriority pins the resolution order the
// Makefile's TEAM_SKILLS_DIR mirrors: REASONIX_STATE_HOME, else REASONIX_HOME,
// else the home-based default. A split between the two would install to one
// root and read the other.
func TestTeamSkillsBaseFollowsStateRootPriority(t *testing.T) {
	state := t.TempDir()
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	t.Setenv("REASONIX_STATE_HOME", state)
	if got := teamSkillsBase(); got != state {
		t.Fatalf("REASONIX_STATE_HOME must win, got %q", got)
	}
	if got := teamSkillsTreeDir(); got != filepath.Join(state, "team", "skills") {
		t.Fatalf("team skills tree = %q", got)
	}
	t.Setenv("REASONIX_STATE_HOME", "")
	if got := teamSkillsBase(); got != home {
		t.Fatalf("REASONIX_HOME must back the user root, got %q", got)
	}
	if got := teamSkillsTreeDir(); got != filepath.Join(home, "team", "skills") {
		t.Fatalf("team skills tree under the home root = %q", got)
	}
}

// TestTeamSkillsBaseUnresolvableFailsClosed pins the degenerate case: with no
// state root at all there is no team tree address, and a role-scoped store
// reads nothing rather than following the working directory.
func TestTeamSkillsBaseUnresolvableFailsClosed(t *testing.T) {
	t.Setenv("REASONIX_STATE_HOME", "")
	t.Setenv("REASONIX_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if got := teamSkillsBase(); got != "" {
		t.Fatalf("no state root must resolve to no base, got %q", got)
	}
	if got := teamSkillsTreeDir(); got != "" {
		t.Fatalf("no base must resolve to no tree, got %q", got)
	}
	cwd := t.TempDir()
	writeManagerSkillWorkspace(t, cwd)
	t.Chdir(cwd)
	st := skill.New(skill.Options{ProjectRoot: cwd, TeamRole: "member", DisableBuiltins: true, Stderr: io.Discard})
	if got := st.List(); len(got) != 0 {
		t.Fatalf("cwd skills must not serve a team session, got %d entries", len(got))
	}
}

// TestTeamMemberCatalogComesFromUserRoot is the real-path regression for the
// fixed team skill root: the bound member's catalog and playbook come from
// <user state dir>/team/skills — never from the launch workspace, the process
// cwd, or a same-shaped tree elsewhere — while its workspace root, which drives
// files, sandbox and status, stays the launch workspace. The leader sees
// base/leader, admitted shared and special/leader skills; the member sees only
// its own role's.
func TestTeamMemberCatalogComesFromUserRoot(t *testing.T) {
	state := t.TempDir()
	t.Setenv("REASONIX_STATE_HOME", state)
	t.Setenv("REASONIX_HOME", "")
	writeManagerSkillWorkspace(t, state)

	// The launch workspace owns a full team tree of its own, and the process
	// runs somewhere else entirely — neither may contribute a team skill.
	workspace := t.TempDir()
	writeManagerSkillWorkspace(t, workspace)
	foreign := t.TempDir()
	writeSkillDistractor(t, foreign)
	t.Chdir(foreign)

	build := newMemberBackendBuilder(memberBuildDeps(t, workspace))
	leader, err := build(team.MemberBinding{
		Team: "alpha", MemberID: "lead", Leader: true, AgentUserRef: "leader-user",
		SessionFile: filepath.Join("alpha", "lead.jsonl"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(leader.Close)
	if got := leader.WorkspaceRoot(); got != workspace {
		t.Fatalf("the member workspace must stay the launch workspace, got %q", got)
	}
	if got := catalogUnder(allMemberSkills(leader), state); !wantCatalog(got, "leader", "locked-leader", "leadtool", "open") {
		t.Fatalf("leader catalog from the user root = %v", got)
	}
	for _, other := range []string{workspace, foreign} {
		if got := catalogUnder(allMemberSkills(leader), other); len(got) != 0 {
			t.Errorf("leader catalog must not read %s, got %v", other, got)
		}
	}
	if got := leader.SystemPrompt(); !strings.Contains(got, "LDR-BASE") {
		t.Fatal("the leader playbook must load from the user-global team root")
	}

	member, err := build(team.MemberBinding{
		Team: "alpha", MemberID: "alice", AgentUserRef: "member-user",
		SessionFile: filepath.Join("alpha", "alice.jsonl"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(member.Close)
	if got := catalogUnder(allMemberSkills(member), state); !wantCatalog(got, "member", "locked-member", "memtool", "open") {
		t.Fatalf("member catalog from the user root = %v", got)
	}
	if got := member.SystemPrompt(); !strings.Contains(got, "MBR-BASE") {
		t.Fatal("the member playbook must load from the user-global team root")
	}
	if got := member.SystemPrompt(); strings.Contains(got, "LDR-BASE") {
		t.Fatal("the leader's playbook must never enter a member's prompt")
	}
}

// TestTeamMemberCatalogWithoutUserTreeStaysClosed pins the missing-tree
// behavior: an uninstalled team tree yields no team skills and no playbook,
// even when the launch workspace owns a full tree — the session degrades to
// project skills alone instead of silently reading the wrong team.
func TestTeamMemberCatalogWithoutUserTreeStaysClosed(t *testing.T) {
	state := t.TempDir()
	t.Setenv("REASONIX_STATE_HOME", state)
	t.Setenv("REASONIX_HOME", "")
	workspace := t.TempDir()
	writeManagerSkillWorkspace(t, workspace)
	t.Chdir(t.TempDir())

	build := newMemberBackendBuilder(memberBuildDeps(t, workspace))
	leader, err := build(team.MemberBinding{
		Team: "alpha", MemberID: "lead", Leader: true, AgentUserRef: "leader-user",
		SessionFile: filepath.Join("alpha", "lead.jsonl"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(leader.Close)
	if got := catalogUnder(allMemberSkills(leader), state); len(got) != 0 {
		t.Fatalf("no installed tree must serve no team skill, got %v", got)
	}
	if got := catalogUnder(allMemberSkills(leader), workspace); len(got) != 0 {
		t.Fatalf("a workspace-owned tree must not serve a team session, got %v", got)
	}
	if got := leader.SystemPrompt(); strings.Contains(got, "LDR-BASE") {
		t.Fatal("an uninstalled tree must contribute no playbook")
	}
}

// TestTeamCreateRecordsNoSkillRoot pins the writer side: creating a team
// records no skill root at all — in-tree or elsewhere — so the registry can
// never re-acquire the coupling to a launch directory.
func TestTeamCreateRecordsNoSkillRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "team", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	teamFixtureDoc(t, root)
	t.Chdir(root)

	m := openTeamOverlay(t)
	if m.teamPick == nil || m.teamPick.store == nil {
		t.Fatal("overlay must open over the empty registry")
	}
	for _, name := range []string{"alpha", "beta"} {
		if err := m.teamPick.addTeam(name); err != nil {
			t.Fatal(err)
		}
	}
	doc, _, err := m.teamPick.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Teams) != 2 {
		t.Fatalf("want two teams, got %d", len(doc.Teams))
	}
	for _, tm := range doc.Teams {
		if tm.WorkspaceRoot != "" {
			t.Errorf("team %q must record no skill root, got %q", tm.Name, tm.WorkspaceRoot)
		}
	}
}

// TestTeamBoundSkillMirrorReadsUserRoot pins the manager-side mirror: while the
// window is bound to a member, the mirror store — the surface /skills new,
// paths and the picker's sources pane read — addresses the user-global team
// tree with the bound member's role, even when the overlay opened in a foreign
// workspace and the team document carries a stale recorded root.
func TestTeamBoundSkillMirrorReadsUserRoot(t *testing.T) {
	stale := t.TempDir()
	writeManagerSkillWorkspace(t, stale)
	recorded := twoMemberTeam()
	recorded.WorkspaceRoot = stale
	writeTeamFixture(t, recorded)
	state := teamSkillsBase()
	writeManagerSkillWorkspace(t, state)

	foreign := t.TempDir()
	writeSkillDistractor(t, foreign)
	m := openTeamOverlay(t)
	if m.teamPick == nil {
		t.Fatal("overlay must open")
	}
	m.teamPick.workspaceRoot = foreign // the manager really launched here
	m = bindStubMember(t, m, "alice", stubCatalogs{}, true)

	if got := namesUnder(m.skillStore().List(), state); !nameSet(got, "locked-member", "member", "memtool", "open") {
		t.Fatalf("member mirror over the user root = %v", got)
	}
	for _, other := range []string{foreign, stale} {
		if got := namesUnder(m.skillStore().List(), other); len(got) != 0 {
			t.Errorf("member mirror must not read %s, got %q", other, got)
		}
	}
	if rootsUnder(m.skillStore().Roots(), state) == "" {
		t.Error("mirror roots must include the user-global team tree")
	}
	for _, other := range []string{foreign, stale} {
		// The mirror keeps the member's project conventions at its own launch
		// workspace; only the team tree must come from the user root.
		if tree := filepath.Join(other, "team", "skills"); rootsUnder(m.skillStore().Roots(), tree) != "" {
			t.Errorf("the mirror must not address the team tree at %s", tree)
		}
	}

	m = bindStubMember(t, m, "lead", stubCatalogs{}, false)
	if got := namesUnder(m.skillStore().List(), state); !nameSet(got, "leader", "leadtool", "locked-leader", "open") {
		t.Fatalf("leader mirror over the user root = %v", got)
	}
	if msg := m.teamSkillGapDiagnostic(); msg != "" {
		t.Fatalf("an installed tree must keep the gap diagnostic silent, got %q", msg)
	}
}

// TestTeamSkillGapDiagnosticNamesUserTree pins the rewritten diagnostic: a
// bound member with no installed tree is told the user-global destination and
// the install recipe, never a launch directory to run reasonix from. The tree
// counts as installed only once a playbook its role can load is there, so a
// bare directory — the shell an interrupted install leaves — keeps the hint.
func TestTeamSkillGapDiagnosticNamesUserTree(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	m = bindStubMember(t, m, "alice", stubCatalogs{}, false)

	msg := m.teamSkillGapDiagnostic()
	want := filepath.Join(teamSkillsBase(), "team", "skills")
	if !strings.Contains(msg, want) {
		t.Fatalf("the gap diagnostic must name %s, got %q", want, msg)
	}
	if !strings.Contains(msg, "make install-team-skills") {
		t.Fatalf("the gap diagnostic must name the install recipe, got %q", msg)
	}
	if strings.Contains(msg, "start reasonix from") {
		t.Fatalf("the gap diagnostic must not point at a launch directory, got %q", msg)
	}

	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}
	if msg := m.teamSkillGapDiagnostic(); msg == "" {
		t.Fatal("a bare tree that loads no playbook must keep the gap diagnostic up")
	}
	writeRoleSkillTree(t, teamSkillsBase(), map[string]string{
		"team/skills/base/member/SKILL.md": "---\nname: member\ndescription: member playbook\n---\nMBR-BASE",
	})
	if msg := m.teamSkillGapDiagnostic(); msg != "" {
		t.Fatalf("once the bound role's playbook loads the diagnostic must go silent, got %q", msg)
	}
}
