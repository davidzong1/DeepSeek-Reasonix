package team

import (
	"fmt"
	"path/filepath"
)

// SchemaVersion is the .reasonix/team data schema version (§3.4). Every
// document carries it; Store.Load refuses files with other versions, so a
// mismatch fails loudly instead of misreading (migration lands later).
const SchemaVersion = 1

// Document is embedded by every .reasonix/team schema document; the
// schema_version header is enforced by Store.Load.
type Document struct {
	SchemaVersion int `json:"schema_version"`
}

// TeamDoc is the teams.json document (§2.5): the team registry.
type TeamDoc struct {
	Document
	Teams []Team `json:"teams"`
}

// canonicalize gives TeamDoc one encoded form: an absent registry and an empty
// roster encode as [] rather than null, so a file written before any team
// existed still compares equal to the same registry in memory (§3.4 CAS).
func (d *TeamDoc) canonicalize() {
	if d.Teams == nil {
		d.Teams = []Team{}
	}
	for i := range d.Teams {
		if d.Teams[i].Template == nil {
			d.Teams[i].Template = []MemberSlot{}
		}
	}
}

// AgentUsersDoc is the agent_users.json document (§2.1): the top-level
// credential-and-config registry, referenced but never owned by any team.
type AgentUsersDoc struct {
	Document
	AgentUsers []AgentUser `json:"agent_users"`
}

// MemoryDoc is the memory.json document (§2.9): layered long-term memory.
type MemoryDoc struct {
	Document
	Entries []MemoryEntry `json:"entries"`
}

// BlackboardDoc is one immutable blackboard revision file (§2.8): rev-N.json
// under BlackboardDir. Every write publishes a new revision, so history is
// replayable and rollback is a pointer change, never a rewrite.
type BlackboardDoc struct {
	Document
	Entry BlackboardEntry `json:"entry"`
}

// File paths inside the .reasonix/team directory (v1 layout). TeamsFile is a
// compatibility alias for TeamFile — callers predate the rename and keep
// resolving the primary path.
const (
	TeamFile        = "team.json"  // primary team registry document (§2.5)
	TeamsFile       = TeamFile     // pre-rename name, same primary file
	TeamsLegacyFile = "teams.json" // v1-era name: read-only fallback, migration source
	AgentUsersFile  = "agent_users.json"
	BlackboardDir   = "blackboard" // rev-N.json revision files
	MemoryFile      = "memory.json"
)

// TeamRoot resolves the .reasonix/team directory for a project root,
// verifying the root is absolute and the result stays inside it, so a bad
// root can never escape the project (safePath).
func TeamRoot(projectRoot string) (string, error) {
	return safePath(projectRoot, filepath.Join(".reasonix", "team"))
}

// BlackboardRevFile returns the rev-N.json path under BlackboardDir for a
// blackboard revision, refusing rev < 1 so revision files stay canonical.
func BlackboardRevFile(rev int) (string, error) {
	if rev < 1 {
		return "", fmt.Errorf("team: blackboard rev must be >= 1, got %d", rev)
	}
	return filepath.Join(BlackboardDir, fmt.Sprintf("rev-%d.json", rev)), nil
}
