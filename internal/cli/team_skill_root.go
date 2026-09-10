package cli

import (
	"path/filepath"

	"reasonix/internal/config"
)

// teamSkillsBase returns the root owning the user-global team skills tree: the
// user state dir, under which the role-scoped skill store and the role playbook
// loader both join "team/skills". Team skills are user-global state
// (config.UserStateDir: REASONIX_STATE_HOME, else REASONIX_HOME, else
// ~/.reasonix), so a member's playbooks resolve from any launching directory
// and a team's recorded workspace can never steer them. "" when no user state
// root is resolvable — a role-scoped store then reads no team tree at all
// rather than following the working directory.
func teamSkillsBase() string { return config.UserStateDir() }

// teamSkillsTreeDir returns the user-global team skills tree
// (<base>/team/skills), the one address discovery, the picker's sources pane
// and the gap diagnostic all report. "" when the base itself is unresolvable.
func teamSkillsTreeDir() string {
	base := teamSkillsBase()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "team", "skills")
}
