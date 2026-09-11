package cli

import (
	"fmt"
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

// parseTeamRole accepts exactly the allowlisted roles. Empty, unknown and
// traversal-shaped values are refused: none has a branch to load, and refusing
// here keeps an unvalidated string one join away from walking out of
// team/skills.
func parseTeamRole(s string) (teamRole, bool) {
	r, ok := skill.ParseTeamRole(s)
	if !ok {
		return "", false
	}
	return teamRole(r), true
}

// roleForLeader maps the backend's leader flag onto the allowlist.
func roleForLeader(leader bool) teamRole {
	if leader {
		return teamRole(skill.TeamRoleLeader)
	}
	return teamRole(skill.TeamRoleMember)
}

// teamRoleSkillBudget caps the assembled <team-role-skill> section. 32 KiB
// holds several playbooks while bounding the worst case a stray oversized
// skills tree can add to the member prefix.
const teamRoleSkillBudget = 32 << 10

// skillFileNames are the accepted skill spellings, canonical first.
var skillFileNames = []string{"SKILL.md", "skill.md"}

// teamRoleFrontmatterKey is the SKILL.md frontmatter key a skill uses to
// declare the team role it belongs to. Keys are lowercased by the frontmatter
// parser, so the const is already the canonical form.
const teamRoleFrontmatterKey = "team_role"

// teamRoleAllows reports whether one skill file's declared team_role admits
// the loading role; the admission rule lives in the skill package so the
// prompt loader and the session skill store never disagree about a shared
// skill. Absent or blank declares both; a declared role admits only itself; a
// non-allowlisted value is invalid — the file is dropped for both roles and
// the warning records the skill path and the offending value.
func teamRoleAllows(meta map[string]string, path string, role teamRole, warn io.Writer) bool {
	declared := strings.TrimSpace(meta[teamRoleFrontmatterKey])
	admit, invalid := skill.TeamRoleAllows(declared, string(role))
	if invalid {
		fmt.Fprintf(warn, "team role skill %s: invalid team_role %q (want leader or member)\n", path, declared)
	}
	return admit
}

// teamRoleSkillPrompt loads the role playbook at backend assembly time: the
// base/<role> skill (historical by-name behaviour), then every skill under
// team/skills/shared (role-neutral) and team/skills/special/<role>. Missing
// playbooks are a no-op for workspaces that do not install them. warn, when
// given, receives one line per skill dropped — for an invalid team_role
// declaration, or for one that would not fit the section budget.
// roleSkillPrompt carries the allowlist, confinement and budget guarantees.
func teamRoleSkillPrompt(root string, leader bool, warn ...io.Writer) string {
	return roleSkillPrompt(root, roleForLeader(leader), warn...)
}

// roleSkillPrompt assembles the <team-role-skill> section for one allowlisted
// role. base is read by name through the skill store, so its whole package
// (frontmatter + body) honours project skill semantics; shared and special are
// read from disk so a same-named special skill is not shadowed by base and one
// role's special never leaks into the other's prompt. Every disk-scanned skill
// is admitted by its own team_role declaration (teamRoleAllows); base is
// name-scoped to the loading role already, so its declaration is never read.
// The result is byte reproducible for a given tree and stays under
// teamRoleSkillBudget. An absent warn writer keeps the historical silence.
func roleSkillPrompt(root string, role teamRole, warn ...io.Writer) string {
	if strings.TrimSpace(root) == "" {
		return ""
	}
	if _, ok := parseTeamRole(string(role)); !ok {
		return ""
	}
	w := io.Discard
	if len(warn) > 0 && warn[0] != nil {
		w = warn[0]
	}
	skillsDir := filepath.Join(root, "team", "skills")
	sec := roleSkillSection{max: teamRoleSkillBudget}
	if sk, ok := skill.New(skill.Options{ProjectRoot: root, Stderr: io.Discard}).Read(string(role)); ok {
		if body := strings.TrimSpace(sk.Body); body != "" {
			sec.add(string(role), body, w)
		}
	}
	appendRoleSkillDir(&sec, skillsDir, filepath.Join(skillsDir, "shared"), role, w)
	appendRoleSkillDir(&sec, skillsDir, filepath.Join(skillsDir, "special", string(role)), role, w)
	if sec.b.Len() == 0 {
		return ""
	}
	return "\n\n<team-role-skill name=\"" + string(role) + "\">\n" + strings.TrimSpace(sec.b.String()) + "\n</team-role-skill>"
}

// roleSkillSection accumulates skills under a byte budget in append order. A
// block that would exceed the remaining budget is dropped whole — never
// truncated mid-playbook — so one oversized skill cannot evict the ones after
// it and the output is always deterministic. Dropping is reported through warn;
// the budget is otherwise invisible.
type roleSkillSection struct {
	b   strings.Builder
	max int
}

// add writes one skill headed by its directory name when it fits the budget. An
// over-budget block is dropped with a warning rather than silently: skills are
// appended base → shared → special, so the loss lands on the role-specific ones
// last, and only the warning says a playbook stopped reaching the model at all.
func (s *roleSkillSection) add(heading, body string, warn io.Writer) {
	block := "\n\n### " + heading + "\n\n" + body
	if over := s.b.Len() + len(block) - s.max; over > 0 {
		fmt.Fprintf(warn, "team role skill %s: dropped, %d bytes over the %d-byte section budget\n", heading, over, s.max)
		return
	}
	s.b.WriteString(block)
}

// appendRoleSkillDir appends the skills owned by dir, which must live below
// skillsDir. When dir is itself a skill directory (special/<role>/SKILL.md)
// that flat skill is appended; otherwise every real subdirectory skill under
// dir is appended (shared/<name>/SKILL.md). os.ReadDir returns entries sorted
// by name, so the appended order is stable. Every file is admitted by its
// team_role declaration against the loading role.
func appendRoleSkillDir(sec *roleSkillSection, skillsDir, dir string, role teamRole, warn io.Writer) {
	if !confinedSkillDir(skillsDir, dir) {
		return
	}
	if skillPath := resolveSkillFile(dir); skillPath != "" {
		appendRoleSkillFile(sec, skillPath, filepath.Base(dir), role, warn)
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
			appendRoleSkillFile(sec, skillPath, e.Name(), role, warn)
		}
	}
}

// appendRoleSkillFile appends one skill file when its team_role declaration
// admits the loading role; an illegal declaration is dropped with a warning,
// and a declaration for the other role is dropped silently — declarations
// close skills the way a directory does.
func appendRoleSkillFile(sec *roleSkillSection, path, heading string, role teamRole, warn io.Writer) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	meta, body := frontmatter.Split(string(b))
	body = strings.TrimSpace(body)
	if body == "" || !teamRoleAllows(meta, path, role, warn) {
		return
	}
	sec.add(heading, body, warn)
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
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
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
