package cli

// Manager-side seam pins for the role-scoped team catalog: a bound member
// window's skill surfaces read the controller's live scoped store, never a
// cwd-rooted one of their own. Admission itself is pinned elsewhere.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/skill"
	"reasonix/internal/team"
)

// writeManagerSkillWorkspace plants one role tree in workspace: base/<role>
// playbooks, shared skills admitting the leader, the member or neither, and one
// special/<role> skill per branch — the layout /skills must expose per role and
// never cross.
func writeManagerSkillWorkspace(t *testing.T, workspace string) {
	t.Helper()
	writeRoleSkillTree(t, workspace, map[string]string{
		"team/skills/base/leader/SKILL.md":             "---\nname: leader\ndescription: leader playbook\n---\nLDR-BASE",
		"team/skills/base/member/SKILL.md":             "---\nname: member\ndescription: member playbook\n---\nMBR-BASE",
		"team/skills/shared/open/SKILL.md":             "---\nname: open\ndescription: open to both\n---\nOPEN-BODY",
		"team/skills/shared/locked-leader/SKILL.md":    "---\nname: locked-leader\ndescription: leader only\nteam_role: leader\n---\nLDR-LOCKED",
		"team/skills/shared/locked-member/SKILL.md":    "---\nname: locked-member\ndescription: member only\nteam_role: member\n---\nMBR-LOCKED",
		"team/skills/special/leader/leadtool/SKILL.md": "---\nname: leadtool\ndescription: leader special\nteam_role: leader\n---\nLDR-TOOL",
		"team/skills/special/member/memtool/SKILL.md":  "---\nname: memtool\ndescription: member special\nteam_role: member\n---\nMBR-TOOL",
	})
}

// writeSkillDistractor plants a same-shape skill in an unrelated directory, so
// a manager surface that falls back to the process cwd lists it while a scoped
// surface must not.
func writeSkillDistractor(t *testing.T, dir string) {
	t.Helper()
	writeRoleSkillTree(t, dir, map[string]string{
		"team/skills/shared/cwd-only/SKILL.md": "---\nname: cwd-only\ndescription: cwd distractor\n---\nCWD-BODY",
	})
}

// namesUnder lists the skills whose resolved path lives under root.
func namesUnder(skills []skill.Skill, root string) []string {
	var names []string
	for _, sk := range skills {
		if sk.Path != "" && strings.HasPrefix(sk.Path, root) {
			names = append(names, sk.Name)
		}
	}
	return names
}

// nameSet compares a name list against a wanted set, order-insensitively.
func nameSet(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, w := range want {
		found := slices.Contains(got, w)
		if !found {
			return false
		}
	}
	return true
}

// teamManagerSeam opens the team overlay over the two-member alpha fixture
// while the process cwd is distractorDir — deliberately different from the
// workspace the member controllers are rooted at — and records that workspace
// on the picker the way onTeamButtonClick would.
// openManagerSeam opens a manager overlay bound to a fixture team with the
// process cwd parked in distractorDir, leaving the user-global team tree
// uninstalled. workspace stays the member's project root (its project
// conventions), never its team tree.
func openManagerSeam(t *testing.T, workspace, distractorDir string) chatTUI {
	t.Helper()
	writeTeamFixture(t, twoMemberTeam())
	t.Chdir(distractorDir)
	m := openTeamOverlay(t)
	if m.teamPick == nil || m.teamPick.store == nil {
		t.Fatal("overlay must open over the fixture team")
	}
	m.teamPick.workspaceRoot = workspace
	return m
}

// teamManagerSeam is openManagerSeam with the user-global role tree installed —
// the state a real team session runs in. That tree is the only one a team
// session reads.
func teamManagerSeam(t *testing.T, workspace, distractorDir string) chatTUI {
	t.Helper()
	m := openManagerSeam(t, workspace, distractorDir)
	writeManagerSkillWorkspace(t, teamSkillsBase())
	return m
}

type stubCatalogs map[string]struct{ slash, all []skill.Skill }

// bindStubMember arms the stub registry and binds one member, mirroring the
// switch tests' wiring. render must be true only while the ambient controller
// is still bound: rendering a frame reads status-line sub-ports a partial stub
// does not implement, so a re-bind over an already-bound stub skips it.
func bindStubMember(t *testing.T, m chatTUI, member string, catalogs stubCatalogs, render bool) chatTUI {
	t.Helper()
	m.memberEvents = make(chan memberEvent, 8)
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		c := catalogs[b.MemberID]
		return stubBackend{label: b.MemberID, skills: c.slash, all: c.all}, nil
	}, 4)
	m.teamPick.backends = m.teamBackends
	m.teamPick.hub = newTeamHub(m.teamPick.store, m.teamBackends, m.teamPick.session.teamName)
	if render {
		next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		m = next.(chatTUI)
	}
	if cmd := m.switchTeamMember(member); cmd == nil {
		t.Fatalf("switching to %q must bind its stub backend", member)
	}
	return m
}

// TestTeamBoundSkillStoreMirrorsMemberWorkspaceScope pins the authoring-store
// seam: while the window is bound inside a team session, skillStore() — /skills
// new, /skills paths and the picker's sources pane — reads the user-global team
// tree with the bound member's TeamRole, no matter where the process cwd is or
// where the member's project root points. The listed tree is exactly the role's
// base + admitted shared + special/<role> set, each listed skill resolves by
// name the way run_skill reads it, the other role's skills and the cwd
// distractor never appear, and closing the session restores the plain cwd store.
func TestTeamBoundSkillStoreMirrorsMemberWorkspaceScope(t *testing.T) {
	workspace := t.TempDir()
	distractor := t.TempDir()
	writeSkillDistractor(t, distractor)

	m := bindStubMember(t, teamManagerSeam(t, workspace, distractor), "alice", stubCatalogs{}, true)
	tree := teamSkillsBase()
	if got := namesUnder(m.skillStore().List(), tree); !nameSet(got, "locked-member", "member", "memtool", "open") {
		t.Fatalf("member mirror store = %v, want {locked-member member memtool open}", got)
	}
	st := m.skillStore()
	for _, name := range []string{"locked-member", "member", "memtool", "open"} {
		if _, ok := st.Read(name); !ok {
			t.Errorf("member mirror must resolve listed skill %q (Read/run_skill source)", name)
		}
	}
	for _, closed := range []string{"leader", "locked-leader", "leadtool", "cwd-only"} {
		if _, ok := st.Read(closed); ok {
			t.Errorf("member mirror must not resolve %q", closed)
		}
	}
	for _, other := range []string{distractor, workspace} {
		for _, name := range namesUnder(st.List(), other) {
			t.Errorf("member mirror store must not read %s, got %q", other, name)
		}
	}
	if rootsUnder(st.Roots(), tree) == "" {
		t.Error("member mirror store roots must include the user-global team tree")
	}
	if rootsUnder(st.Roots(), filepath.Join(workspace, "team", "skills")) != "" {
		t.Error("member mirror store roots must not address the member workspace's own team tree")
	}

	// Switching to the leader re-scopes the same seam; switching back stays
	// scoped. The mirror store is rebuilt per call, so each member's window
	// sees exactly its own catalog.
	m = bindStubMember(t, m, "lead", stubCatalogs{}, false)
	if got := namesUnder(m.skillStore().List(), tree); !nameSet(got, "leader", "leadtool", "locked-leader", "open") {
		t.Fatalf("leader mirror store = %v, want {leader leadtool locked-leader open}", got)
	}
	if _, ok := m.skillStore().Read("memtool"); ok {
		t.Error("leader mirror must not resolve the member's special skill")
	}

	// Closing the session hands the manager back the ambient cwd store: the
	// process cwd is authoritative again, distractor and all.
	m.closeSession()
	ambient := m.skillStore()
	if got := namesUnder(ambient.List(), distractor); !nameSet(got, "cwd-only") {
		t.Fatalf("ambient store after close = %v, want {cwd-only}", got)
	}
	for _, name := range namesUnder(ambient.List(), tree) {
		t.Errorf("ambient store after close must not read the team tree, got %q", name)
	}
}

// rootsUnder returns the first root dir under prefix, or "".
func rootsUnder(roots []skill.Root, prefix string) string {
	for _, r := range roots {
		if strings.HasPrefix(r.Dir, prefix) {
			return r.Dir
		}
	}
	return ""
}

// anyPrefix reports whether any dir lives under prefix.
func anyPrefix(dirs []string, prefix string) bool {
	for _, d := range dirs {
		if strings.HasPrefix(d, prefix) {
			return true
		}
	}
	return false
}

// TestTeamBoundPickerAndRescanReadControllerCatalog pins the picker seam: the
// list opened by /skills and refreshed by the rescan key (and after a delete)
// stays on the bound controller's live scoped catalog — not on a cwd-rooted
// store the manager builds itself, which would drop special/<role> and admit
// the other role's tree. The stub catalog is deliberately one skill richer than
// the on-disk tree (the phantom the real scoped store would never produce is a
// stand-in for any live divergence), proving the picker asks the controller.
// Switching members re-scopes it; closing the session restores the cwd store.
func TestTeamBoundPickerAndRescanReadControllerCatalog(t *testing.T) {
	workspace := t.TempDir()
	writeManagerSkillWorkspace(t, workspace)
	distractor := t.TempDir()
	writeSkillDistractor(t, distractor)

	memberAll := []skill.Skill{
		{Name: "locked-member", Path: filepath.Join(workspace, "team", "skills", "shared", "locked-member", "SKILL.md")},
		{Name: "member", Path: filepath.Join(workspace, "team", "skills", "base", "member", "SKILL.md")},
		{Name: "memtool", Path: filepath.Join(workspace, "team", "skills", "special", "member", "memtool", "SKILL.md")},
		{Name: "open", Path: filepath.Join(workspace, "team", "skills", "shared", "open", "SKILL.md")},
		{Name: "phantom-member", Path: filepath.Join(workspace, "team", "skills", "shared", "phantom-member", "SKILL.md")},
	}
	leaderAll := []skill.Skill{
		{Name: "leader", Path: filepath.Join(workspace, "team", "skills", "base", "leader", "SKILL.md")},
		{Name: "leadtool", Path: filepath.Join(workspace, "team", "skills", "special", "leader", "leadtool", "SKILL.md")},
		{Name: "locked-leader", Path: filepath.Join(workspace, "team", "skills", "shared", "locked-leader", "SKILL.md")},
		{Name: "open", Path: filepath.Join(workspace, "team", "skills", "shared", "open", "SKILL.md")},
	}
	m := bindStubMember(t, teamManagerSeam(t, workspace, distractor), "alice", stubCatalogs{
		"alice": {slash: memberAll, all: memberAll},
		"lead":  {slash: leaderAll, all: leaderAll},
	}, true)

	m.openSkillPicker()
	if m.skillPick == nil {
		t.Fatal("openSkillPicker must arm the picker")
	}
	if names := pickerNames(m); !nameSet(names, "locked-member", "member", "memtool", "open", "phantom-member") {
		t.Fatalf("picker over the member = %v, want the member's controller catalog", names)
	}
	if dirs := pickerRootDirs(m); !anyPrefix(dirs, workspace) || anyPrefix(dirs, distractor) {
		t.Fatalf("picker sources must be the member workspace's roots, got %v", dirs)
	}

	// The rescan keeps the controller catalog: the old path replaced the picker
	// with the manager's own cwd store (dropping memtool and the phantom,
	// offering the distractor).
	m.refreshSkillPickerData()
	if names := pickerNames(m); !nameSet(names, "locked-member", "member", "memtool", "open", "phantom-member") {
		t.Fatalf("rescan over the member = %v, want the member's controller catalog", names)
	}
	if slash := slashNames(m); !nameSet(slash, "locked-member", "member", "memtool", "open", "phantom-member") {
		t.Fatalf("completion snapshot after rescan = %v, want the member's slash catalog", slash)
	}

	// Switching members re-scopes picker and completion snapshot.
	m = bindStubMember(t, m, "lead", stubCatalogs{
		"alice": {slash: memberAll, all: memberAll},
		"lead":  {slash: leaderAll, all: leaderAll},
	}, false)
	m.refreshSkillPickerData()
	if names := pickerNames(m); !nameSet(names, "leader", "leadtool", "locked-leader", "open") {
		t.Fatalf("rescan over the leader = %v, want the leader's controller catalog", names)
	}
	for _, name := range []string{"memtool", "phantom-member", "cwd-only"} {
		for _, n := range append(pickerNames(m), slashNames(m)...) {
			if n == name {
				t.Fatalf("the leader's surfaces must not carry %q", name)
			}
		}
	}

	// Closing the session restores the ambient cwd store: rescan reads the
	// process cwd again — the distractor is back, no member-scoped name
	// survives. Builtins and user-global skills ride along, so assert membership.
	m.closeSession()
	m.refreshSkillPickerData()
	for _, name := range []string{"cwd-only"} {
		if !hasName(pickerNames(m), name) || !hasName(slashNames(m), name) {
			t.Fatalf("ambient surfaces after close must carry %q: picker=%v snapshot=%v", name, pickerNames(m), slashNames(m))
		}
	}
	for _, name := range []string{"memtool", "leadtool", "phantom-member", "locked-leader"} {
		if hasName(pickerNames(m), name) || hasName(slashNames(m), name) {
			t.Fatalf("ambient surfaces after close must not carry member-scoped %q: picker=%v snapshot=%v", name, pickerNames(m), slashNames(m))
		}
	}
}

// hasName reports whether names contains want.
func hasName(names []string, want string) bool {
	return slices.Contains(names, want)
}

// pickerNames lists the names of the open picker's skill list.
func pickerNames(m chatTUI) []string {
	var names []string
	for _, sk := range m.skillPick.skills {
		names = append(names, sk.Name)
	}
	return names
}

// slashNames lists the completion snapshot names.
func slashNames(m chatTUI) []string {
	var names []string
	for _, sk := range m.skills {
		names = append(names, sk.Name)
	}
	return names
}

// pickerRootDirs lists the source-pane root dirs of the open picker.
func pickerRootDirs(m chatTUI) []string {
	var dirs []string
	for _, r := range m.skillPick.roots {
		dirs = append(dirs, r.dir)
	}
	return dirs
}

// TestTeamBoundSkillGapDiagnosticNamesUninstalledTree pins the manager-side
// hint for the missing-install case: a bound member whose user-global team tree
// yields no playbook for its role silently lists no team skill, so the
// diagnostic names that destination and the install recipe — never a launch
// directory. A tree that exists but loads nothing keeps the hint (an empty
// skeleton is an interrupted install, not an install), and any ambient
// (unbound) window stays silent.
func TestTeamBoundSkillGapDiagnosticNamesUninstalledTree(t *testing.T) {
	workspace := t.TempDir()
	writeManagerSkillWorkspace(t, workspace) // a workspace-owned tree must not satisfy the diagnostic
	distractor := t.TempDir()
	writeSkillDistractor(t, distractor)
	solo := []skill.Skill{{Name: "solo", Path: filepath.Join(workspace, ".agents", "solo", "SKILL.md")}}
	m := bindStubMember(t, openManagerSeam(t, workspace, distractor), "alice", stubCatalogs{
		"alice": {slash: solo, all: solo},
	}, true)
	tree := teamSkillsTreeDir()
	if msg := m.teamSkillGapDiagnostic(); !strings.Contains(msg, tree) || !strings.Contains(msg, "make install-team-skills") {
		t.Fatalf("the uninstalled user tree must be named, got %q", msg)
	}
	m.openSkillPicker() // the notice path must not disturb the picker
	if m.skillPick == nil {
		t.Fatal("openSkillPicker must still arm the picker over an uninstalled tree")
	}

	// A skeleton with no playbook — what an interrupted install leaves — still
	// loads nothing, so it must keep nagging. The workspace's own copy planted
	// above must not satisfy it either: the bound member reads the user tree.
	if err := os.MkdirAll(filepath.Join(tree, "base", "member"), 0o755); err != nil {
		t.Fatal(err)
	}
	if msg := m.teamSkillGapDiagnostic(); !strings.Contains(msg, tree) {
		t.Fatalf("an empty skeleton must keep naming %s, got %q", tree, msg)
	}

	// A partial install: the other role's branch is populated, alice's own
	// branch and the shared branch hold nothing loadable.
	writeRoleSkillTree(t, teamSkillsBase(), map[string]string{
		"team/skills/base/leader/SKILL.md": "---\nname: leader\ndescription: leader playbook\n---\nLDR-BASE",
	})
	if msg := m.teamSkillGapDiagnostic(); msg == "" {
		t.Fatal("the other role's playbook alone must not count as an installed tree")
	}

	// A playbook the bound role can actually load — here the shared branch,
	// which admits both roles — is what makes the tree installed.
	writeRoleSkillTree(t, teamSkillsBase(), map[string]string{
		"team/skills/shared/open/SKILL.md": "---\nname: open\ndescription: open to both\n---\nOPEN-BODY",
	})
	if msg := m.teamSkillGapDiagnostic(); msg != "" {
		t.Fatalf("a playbook admitted to the bound role must silence the diagnostic, got %q", msg)
	}
	m.closeSession()
	if msg := m.teamSkillGapDiagnostic(); msg != "" {
		t.Fatalf("outside a bound session the diagnostic must stay silent, got %q", msg)
	}
}

// TestTeamBoundSkillScopeSurvivesPickerWithoutStore pins the nil seam: a bound
// window whose picker never opened its store has no binding to mirror, so the
// scope must report none and every skill surface must fall back to the plain
// cwd store instead of dereferencing the missing store.
func TestTeamBoundSkillScopeSurvivesPickerWithoutStore(t *testing.T) {
	workspace := t.TempDir()
	distractor := t.TempDir()
	writeSkillDistractor(t, distractor)
	m := bindStubMember(t, openManagerSeam(t, workspace, distractor), "alice", stubCatalogs{}, false)
	m.teamPick.store = nil

	if _, _, _, ok := m.teamSkillStoreScope(); ok {
		t.Fatal("a picker without a store has no scope to mirror")
	}
	if st := m.skillStore(); st == nil {
		t.Fatal("the authoring store must fall back, not fail")
	}
	if msg := m.teamSkillGapDiagnostic(); msg != "" {
		t.Fatalf("the diagnostic must not read a scope it cannot resolve, got %q", msg)
	}
}
