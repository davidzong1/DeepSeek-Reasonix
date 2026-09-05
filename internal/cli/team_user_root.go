package cli

import (
	"fmt"
	"path/filepath"

	"reasonix/internal/config"
	"reasonix/internal/team"
)

// teamDataRoots is the default team surface for a chat/CLI session: the
// registry, member-context, and board stores live under the user-global team
// data root <user state dir>/team (config.UserStateDir), so teams and history
// resolve from any working directory. A project's legacy .reasonix/team — when
// REASONIX_HOME is not explicitly isolating — is folded in exactly once as a
// read-only source (team.AdoptProjectInto); the source tree is never written
// or deleted. Only when no user state root is resolvable does the surface fall
// back to the project-rooted store.
type teamDataRoots struct {
	store    *team.TeamStore        // registry + agent-user pool
	sessions *team.TeamSessionStore // member context / session selection
	dataDir  string                 // team data dir hosting board.db and the files above
	note     string                 // adoption outcome for a notice; "" when uneventful
}

// openTeamDataRoots resolves and opens the default team stores for a session
// rooted at cwd. The registry and member context live under the user-global
// team data root, so teams and history resolve from any working directory. The
// project's own .reasonix/team — when REASONIX_HOME is not explicitly
// isolating — is adopted into the user store once; an explicit REASONIX_HOME
// routes the user root there and never reads the project tree. Only a missing
// user state root (no REASONIX_STATE_HOME, REASONIX_HOME, or home directory)
// keeps the project-rooted store. No ancestor walk is performed: the only
// project file consulted is cwd's own .reasonix/team.
func openTeamDataRoots(cwd string) (*teamDataRoots, error) {
	projectDir, _ := team.TeamRoot(cwd)
	if userDir := teamUserDataDir(); userDir != "" {
		if roots, ok := openUserTeamData(userDir, projectDir); ok {
			return roots, nil
		}
	}
	store, err := team.NewTeamStore(cwd)
	if err != nil {
		return nil, err
	}
	sessions, err := team.NewTeamSessionStore(cwd)
	if err != nil {
		return nil, err
	}
	return &teamDataRoots{store: store, sessions: sessions, dataDir: projectDir}, nil
}

// teamUserDataDir returns the user-global team data root, <user state dir>/team
// under config.UserStateDir — REASONIX_STATE_HOME, else REASONIX_HOME, else the
// home-based ~/.reasonix. That is the default read/write root for teams, so a
// session lists and edits the same registry from any working directory. "" only
// when no user state root is resolvable; callers then keep the project-rooted
// store. Legacy-project adoption is governed separately by REASONIX_HOME (an
// explicitly isolating home never imports the project tree).
func teamUserDataDir() string {
	if base := config.UserStateDir(); base != "" {
		return filepath.Join(base, "team")
	}
	return ""
}

// openUserTeamData builds the user-rooted stores, reporting ok=false only when
// the user data dir is unusable (which sends the caller to the project
// fallback). The store is opened without a workspace anchor, so Root() reports
// the team data dir itself — <user state root>/team, never the launching cwd —
// and hosts place sibling runtime data (e.g. the knowledge base) beside it
// user-globally. Adoption is best-effort: a failure surfaces as a note and
// never blocks the user store from opening.
func openUserTeamData(userDir, projectDir string) (*teamDataRoots, bool) {
	store, err := team.NewTeamStoreAt("", userDir)
	if err != nil {
		return nil, false
	}
	sessions, err := team.NewTeamSessionStoreDir(userDir)
	if err != nil {
		return nil, false
	}
	return &teamDataRoots{
		store:    store,
		sessions: sessions,
		dataDir:  userDir,
		note:     adoptLegacyTeams(userDir, projectDir),
	}, true
}

// adoptLegacyTeams folds the project's legacy .reasonix/team into the user
// store once (see team.AdoptProjectInto) and returns a one-line outcome for
// the caller to surface. "" means nothing noteworthy happened.
func adoptLegacyTeams(userDir, projectDir string) string {
	rep, err := team.AdoptProjectInto(userDir, projectDir, team.AdoptOptions{
		AllowLegacy: config.IsolatedHomeDir() == "",
	})
	if err != nil {
		return "legacy team adoption failed: " + err.Error()
	}
	if rep.Skipped != "" || (rep.TeamsCreated == 0 && rep.AgentUsersCreated == 0) {
		return ""
	}
	return fmt.Sprintf("adopted %d legacy team(s) from %s", rep.TeamsCreated, projectDir)
}
