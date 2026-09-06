package cli

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"reasonix/internal/frontmatter"
	"reasonix/internal/skill"
)

// teamRole identifies a role that owns base and special skill branches. The
// two members of this allowlist are the only directory names used when loading
// role skills.
type teamRole string

const (
	teamRoleLeader teamRole = "leader"
	teamRoleMember teamRole = "member"
)

// parseTeamRole accepts exactly the allowlisted roles. Empty, unknown and
// traversal-shaped values are refused: none has a branch to load, and refusing
// here keeps an unvalidated string one join away from walking out of
// team/skills.
func parseTeamRole(s string) (teamRole, bool) {
	switch teamRole(s) {
	case teamRoleLeader, teamRoleMember:
		return teamRole(s), true
	default:
		return "", false
	}
}

// roleForLeader maps the backend's leader flag onto the allowlist.
func roleForLeader(leader bool) teamRole {
	if leader {
		return teamRoleLeader
	}
	return teamRoleMember
}

// teamRoleSkillBudget caps the assembled <team-role-skill> section. 16 KiB
// holds several playbooks while bounding the worst case a stray oversized
// skills tree can add to the member prefix.
const teamRoleSkillBudget = 16 << 10

// skillFileNames are the accepted skill spellings, canonical first.
var skillFileNames = []string{"SKILL.md", "skill.md"}

// teamRoleSkillPrompt loads the role playbook at backend assembly time: the
// base/<role> skill (historical by-name behaviour), then every skill under
// team/skills/shared (role-neutral) and team/skills/special/<role>. Missing
// playbooks are a no-op for workspaces that do not install them. roleSkillPrompt
// carries the allowlist, confinement and budget guarantees.
func teamRoleSkillPrompt(root string, leader bool) string {
	return roleSkillPrompt(root, roleForLeader(leader))
}

// roleSkillPrompt assembles the <team-role-skill> section for one allowlisted
// role. base is read by name through the skill store, so its whole package
// (frontmatter + body) honours project skill semantics; shared and special are
// read from disk so a same-named special skill is not shadowed by base and one
// role's special never leaks into the other's prompt. The result is byte
// reproducible for a given tree and stays under teamRoleSkillBudget.
func roleSkillPrompt(root string, role teamRole) string {
	if strings.TrimSpace(root) == "" {
		return ""
	}
	if _, ok := parseTeamRole(string(role)); !ok {
		return ""
	}
	skillsDir := filepath.Join(root, "team", "skills")
	sec := roleSkillSection{max: teamRoleSkillBudget}
	if sk, ok := skill.New(skill.Options{ProjectRoot: root, Stderr: io.Discard}).Read(string(role)); ok {
		if body := strings.TrimSpace(sk.Body); body != "" {
			sec.add(string(role), body)
		}
	}
	appendRoleSkillDir(&sec, skillsDir, filepath.Join(skillsDir, "shared"))
	appendRoleSkillDir(&sec, skillsDir, filepath.Join(skillsDir, "special", string(role)))
	if sec.b.Len() == 0 {
		return ""
	}
	return "\n\n<team-role-skill name=\"" + string(role) + "\">\n" + strings.TrimSpace(sec.b.String()) + "\n</team-role-skill>"
}

// roleSkillSection accumulates skills under a byte budget in append order. A
// block that would exceed the remaining budget is dropped whole — never
// truncated mid-playbook — so one oversized skill cannot evict the ones after
// it and the output is always deterministic.
type roleSkillSection struct {
	b   strings.Builder
	max int
}

// add writes one skill headed by its directory name when it fits the budget.
func (s *roleSkillSection) add(heading, body string) {
	block := "\n\n### " + heading + "\n\n" + body
	if s.b.Len()+len(block) > s.max {
		return
	}
	s.b.WriteString(block)
}

// appendRoleSkillDir appends the skills owned by dir, which must live below
// skillsDir. When dir is itself a skill directory (special/<role>/SKILL.md)
// that flat skill is appended; otherwise every real subdirectory skill under
// dir is appended (shared/<name>/SKILL.md). os.ReadDir returns entries sorted
// by name, so the appended order is stable.
func appendRoleSkillDir(sec *roleSkillSection, skillsDir, dir string) {
	if !confinedSkillDir(skillsDir, dir) {
		return
	}
	if skillPath := resolveSkillFile(dir); skillPath != "" {
		if body := readSkillBody(skillPath); body != "" {
			sec.add(filepath.Base(dir), body)
		}
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue // a symlinked entry is not reported as a directory
		}
		if skillPath := resolveSkillFile(filepath.Join(dir, e.Name())); skillPath != "" {
			if body := readSkillBody(skillPath); body != "" {
				sec.add(e.Name(), body)
			}
		}
	}
}

// confinedSkillDir verifies every component of dir below skillsDir is a real
// directory — not a symlink — and reports whether dir is safe to scan. A
// symlink planted anywhere in the skills tree refuses that branch instead of
// resolving through it, so a scan can never leave the role's directory.
func confinedSkillDir(skillsDir, dir string) bool {
	rel, err := filepath.Rel(skillsDir, dir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	cur := skillsDir
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			return false // missing playbook tree: a no-op
		}
		if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
			return false
		}
	}
	return true
}

// resolveSkillFile returns the path to SKILL.md (canonical) or skill.md
// (fallback) inside dir, or empty when neither exists. A symlinked spelling
// refuses the whole directory: the canonical file being a link is a stronger
// signal than a real lowercase fallback, and following it could escape the
// role's tree.
func resolveSkillFile(dir string) string {
	for _, name := range skillFileNames {
		p := filepath.Join(dir, name)
		fi, err := os.Lstat(p)
		if err != nil {
			continue // absent; try the next spelling
		}
		if fi.Mode()&fs.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			return ""
		}
		return p
	}
	return ""
}

// readSkillBody returns the frontmatter-stripped, trimmed body of a skill file,
// empty on any read or commonsense parse failure. The leading YAML block is the
// skill's metadata, not its playbook: only the body is injected into the role
// prompt.
func readSkillBody(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	_, body := frontmatter.Split(string(b))
	return strings.TrimSpace(body)
}
