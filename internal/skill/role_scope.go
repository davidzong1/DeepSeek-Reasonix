package skill

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Team role allowlist for team/skills: Options.TeamRole and every team_role
// declaration resolve to one of these roles or to nothing (unscoped).
const (
	TeamRoleLeader = "leader"
	TeamRoleMember = "member"
)

// teamRoleFrontmatterKey is the SKILL.md key a skill uses to declare the team
// role it belongs to. The frontmatter parser lowercases keys, so the const is
// already canonical.
const teamRoleFrontmatterKey = "team_role"

// ParseTeamRole accepts exactly the allowlisted spellings. Empty, unknown,
// whitespace-shaped and traversal-shaped values are refused: a role is joined
// into paths, so nothing but the canonical spelling may pass.
func ParseTeamRole(s string) (string, bool) {
	switch s {
	case TeamRoleLeader, TeamRoleMember:
		return s, true
	default:
		return "", false
	}
}

// TeamRoleAllows reports whether a team_role declaration admits role. A blank
// declaration admits both; an allowlisted value admits only itself; any other
// value admits nobody. The declared value is trimmed, mirroring the prompt
// loader's treatment of frontmatter values.
func TeamRoleAllows(declared, role string) (admit, invalid bool) {
	declared = strings.TrimSpace(declared)
	if declared == "" {
		return true, false
	}
	if r, ok := ParseTeamRole(declared); ok {
		return r == role, false
	}
	return false, true
}

// teamTreeExclusion returns the team tree a session whose TeamRole is off the
// allowlist must close wholesale: no branch can be scoped for it, and an
// unscoped tree would show both roles' skills.
func teamTreeExclusion(teamSkillsRoot, projectRoot string) string {
	return teamSkillsDirIn(teamTreeBase(teamSkillsRoot, projectRoot))
}

// resolveTeamSkillsRoot absolutizes the user-global team root option, so every
// team tree path built from it is comparable with discovered skill paths. ""
// stays "".
func resolveTeamSkillsRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	if abs, err := filepath.Abs(root); err == nil {
		return abs
	}
	return root
}

// teamSkillsDirIn returns the team convention skills tree under base — the
// join every team tree path is built from, so list and load address the same
// directory. "" when base is empty.
func teamSkillsDirIn(base string) string {
	if base == "" {
		return ""
	}
	return filepath.Join(base, "team", SkillsDirname)
}

// teamTreeBase is the root owning this store's team tree: the user-global
// TeamSkillsRoot when given, else the project root's own tree.
func teamTreeBase(teamSkillsRoot, projectRoot string) string {
	if teamSkillsRoot != "" {
		return teamSkillsRoot
	}
	return projectRoot
}

// teamSkillsDir returns the team skills tree this store reads. A store given
// TeamSkillsRoot reads the user-global tree and nothing else; a role-scoped
// store without one reads no team tree at all, so a member's skills can never
// depend on the directory reasonix happened to be launched from. Only an
// unscoped store keeps the project's own team convention tree.
func (s *Store) teamSkillsDir() string {
	if s.teamSkillsRoot != "" {
		return teamSkillsDirIn(s.teamSkillsRoot)
	}
	if s.teamRole != "" {
		return ""
	}
	return teamSkillsDirIn(s.projectRoot)
}

// TeamRoleSkills returns every playbook this store's team scope admits,
// ignoring the enabled filter: "is a role playbook installed here" must not be
// answered by the user's disable list. The scan is the one List runs over the
// same tree, so admission rules (role branches, team_role declarations,
// symlinks) agree with loading by construction. nil when this store reads no
// team tree.
func (s *Store) TeamRoleSkills() []Skill {
	if s == nil || s.disableDiscovery {
		return nil
	}
	dir := s.teamSkillsDir()
	if dir == "" {
		return nil
	}
	r := discoveryRoot{Root: Root{Dir: dir, Scope: ScopeProject, Status: pathStatus(dir)}}
	if r.Status != StatusOK {
		return nil
	}
	return s.discoverRoot(r)
}

// teamBaseDir is the branch whose siblings are role directories (base/leader,
// base/member). Under role scope only the session role's sibling may load.
func (s *Store) teamBaseDir() string {
	if s.teamRole == "" {
		return ""
	}
	return filepath.Join(s.teamSkillsDir(), "base")
}

// teamSharedDir is the role-neutral branch; a skill there may narrow itself
// with a team_role declaration, so declarations are enforced on every scan.
// "" when the store reads no team tree, which admits every file.
func (s *Store) teamSharedDir() string {
	tree := s.teamSkillsDir()
	if tree == "" {
		return ""
	}
	return filepath.Join(tree, "shared")
}

// teamSpecialBranchDir is <teamSkills>/special, whose siblings are role
// directories (special/leader, special/member) holding role-only skills. It
// opens only under role scope; unscoped sessions keep the branch on the
// discovery skip-list, as before Options.TeamRole.
func (s *Store) teamSpecialBranchDir() string {
	if s.teamRole == "" {
		return ""
	}
	return filepath.Join(s.teamSkillsDir(), "special")
}

// teamRoleSpecialDir is the session role's own special branch,
// <teamSkills>/special/<role>, the subtree a role store scans. Empty for
// unscoped stores, which never open the branch.
func (s *Store) teamRoleSpecialDir() string {
	branch := s.teamSpecialBranchDir()
	if branch == "" {
		return ""
	}
	return filepath.Join(branch, s.teamRole)
}

// opensTeamSpecialBranch reports whether descending into a directory entry
// named "special" is legal here: only the special branch directly under the
// role-scoped team skills tree opens. Every other "special" name — in the
// other conventions, personal/global roots, or unscoped stores — keeps the
// discovery skip-list it shares with assets-style content directories.
func (s *Store) opensTeamSpecialBranch(dir, name string) bool {
	if s.teamRole == "" || name != "special" {
		return false
	}
	return filepath.Clean(dir) == s.teamSkillsDir()
}

// admitsTeamSkill enforces one file's team_role declaration against the store
// scope. A declaration is read only where the directory layout cannot narrow
// the file to a role already: the shared branch (both roles' tree) and, under
// role scope, the role's own special branch — the same files the prompt loader
// admits by declaration, so a role store and its role prompt agree. base/<role>
// files are name-scoped to the role already and their declaration is never
// read (same rule as the prompt loader). A blank declaration admits both
// roles; an allowlisted value admits only the declared role when the store is
// scoped, and everything when it is not; any other value closes the skill for
// every session with a warning naming the file and the offending value.
func (s *Store) admitsTeamSkill(path string, fm map[string]string) bool {
	if !underDir(path, s.teamSharedDir()) && !underDir(path, s.teamRoleSpecialDir()) {
		return true
	}
	declared := strings.TrimSpace(fm[teamRoleFrontmatterKey])
	if s.teamRole == "" {
		if _, invalid := TeamRoleAllows(declared, ""); !invalid {
			return true
		}
	} else if admit, invalid := TeamRoleAllows(declared, s.teamRole); !invalid {
		return admit
	}
	fmt.Fprintf(s.stderr, "team role skill %s: invalid team_role %q (want leader or member)\n", path, declared)
	return false
}

// underDir reports whether path lies at or below dir, with no component
// escaping it.
func underDir(path, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}
