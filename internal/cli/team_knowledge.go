package cli

import (
	"path/filepath"
)

// teamKBDataRoot returns the durable team knowledge base data root for a team
// data dir: per-team knowledge lives at <dataDir>/knowledge_base/<team-id>,
// colocated with the registry, board, and member context under one team data
// dir. For the user-global default that resolves to
// <user state root>/team/knowledge_base from any working directory — the KB
// never falls under the launching cwd. An empty data dir keeps the KB off.
func teamKBDataRoot(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "knowledge_base")
}
