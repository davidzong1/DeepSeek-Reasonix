package cli

// Team members are separate agents over one workspace, so their write tool calls
// race the same cross-process lease. This file gives a team one in-process write
// token (route §4.1 L5): it only delays acquisitions, the lease stays authority.

import (
	"context"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"reasonix/internal/agent"
)

// teamWriteTokens holds one token per team and workspace root. Process wide, not
// per service: a member backend outlives the service that assembled it, and a
// reused backend must still queue against a rebuilt sibling.
var teamWriteTokens = struct {
	mu     sync.Mutex
	tokens map[string]*agent.WriteIntentToken
}{tokens: map[string]*agent.WriteIntentToken{}}

// teamWriteIntentToken returns the token all members of one team over one
// workspace share, creating it on first use. A team with no workspace root gets
// none: every intent would claim everything, a worse deal than no token.
func teamWriteIntentToken(teamName, workspaceRoot string) *agent.WriteIntentToken {
	key := teamWriteTokenKey(teamName, workspaceRoot)
	if key == "" {
		return nil
	}
	teamWriteTokens.mu.Lock()
	defer teamWriteTokens.mu.Unlock()
	token := teamWriteTokens.tokens[key]
	if token == nil {
		token = agent.NewWriteIntentToken(workspaceRoot)
		teamWriteTokens.tokens[key] = token
	}
	return token
}

// teamWriteTokenKey identifies the peers that should queue against each other:
// one team in one workspace. An unnamed team or an unknown workspace has no
// recognized peer set, so it keeps the pre-token behavior (no gate).
func teamWriteTokenKey(teamName, workspaceRoot string) string {
	team := strings.TrimSpace(teamName)
	if team == "" {
		return ""
	}
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		return ""
	}
	// Mirrors the agent's fold semantics: the same key must hold on the
	// case-insensitive platforms the write claims already fold on.
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		root = strings.ToLower(root)
	}
	return team + "\x00" + filepath.Clean(root)
}

// memberWriteIntentGate returns the gate one member's agent carries. The peer
// label is what a teammate queued behind it reads, so it names the member rather
// than a pid; a team with no token keeps the pre-token behavior.
func memberWriteIntentGate(teamName, workspaceRoot, memberID string) agent.WriteIntentGateFunc {
	token := teamWriteIntentToken(teamName, workspaceRoot)
	if token == nil {
		return nil
	}
	peer := memberWriteLabel(memberID, teamName)
	return func(ctx context.Context, intent agent.WriteIntent, onWait func(agent.WriteIntentWait)) (func(), error) {
		return token.Acquire(ctx, peer, intent, onWait)
	}
}

// codeFileExtensions are the suffixes that make a bare token read as a workspace
// path rather than as prose. The list is finite on purpose: a wrong guess would
// send the leader chasing files nobody named.
var codeFileExtensions = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".py": true, ".rs": true, ".java": true, ".kt": true, ".rb": true,
	".c": true, ".cc": true, ".cpp": true, ".h": true, ".hpp": true,
	".cs": true, ".swift": true, ".php": true, ".sh": true, ".sql": true,
	".md": true, ".json": true, ".yaml": true, ".yml": true, ".toml": true,
	".proto": true, ".css": true, ".scss": true, ".html": true, ".vue": true,
	".svelte": true, ".txt": true, ".cfg": true, ".ini": true,
}

// subtaskWriteAreas extracts the workspace paths a free-text subtask names, for
// the fan-out report. Syntax-only and conservative: a token must look like a
// repository-relative source path or it is prose. It never decides a lock — the
// token does that from the call's real arguments — so a miss costs a sentence.
func subtaskWriteAreas(subtask string) []string {
	seen := map[string]bool{}
	var areas []string
	for field := range strings.FieldsSeq(subtask) {
		candidate := trimPathPunctuation(field)
		if !looksLikeWorkspacePath(candidate) || seen[candidate] {
			continue
		}
		seen[candidate] = true
		areas = append(areas, candidate)
	}
	sort.Strings(areas)
	return areas
}

// trimPathPunctuation drops the wrappers prose puts around a path — backticks,
// quotes, brackets — and the sentence punctuation clinging to its last word,
// including a period outside a closing bracket ("internal/a.go).").
func trimPathPunctuation(field string) string {
	const wrappers = "`'\"()[]{}<>,;:!?*"
	trimmed := strings.Trim(field, wrappers+".")
	for {
		next := strings.TrimRight(strings.TrimRight(trimmed, wrappers), ".")
		if next == trimmed {
			return trimmed
		}
		trimmed = next
	}
}

// looksLikeWorkspacePath reports whether one token reads as a repository path.
// Absolute paths and anything the shell could still expand are refused, so the
// report never names a path outside the described workspace.
func looksLikeWorkspacePath(candidate string) bool {
	if candidate == "" || strings.HasPrefix(candidate, "/") || strings.HasPrefix(candidate, "~") {
		return false
	}
	if strings.Contains(candidate, "://") || strings.Contains(candidate, "\\") {
		return false
	}
	if strings.ContainsAny(candidate, "*?[]{}$`'\"<>|&") {
		return false
	}
	if strings.Count(candidate, "/") == 0 {
		return false
	}
	cleaned := path.Clean(candidate)
	if cleaned == "." || strings.HasPrefix(cleaned, "../") {
		return false
	}
	return codeFileExtensions[strings.ToLower(path.Ext(cleaned))]
}

// fanoutWriteAreas classifies one fan-out's shared subtask for the leader: the
// areas it names. Every member receives that same subtask, so the paths it names
// are shared by construction — the contention the leader can still avoid.
func fanoutWriteAreas(subtask string, members int) string {
	if members < 2 {
		return ""
	}
	areas := subtaskWriteAreas(subtask)
	if len(areas) == 0 {
		return ""
	}
	shown := areas
	if len(shown) > 4 {
		shown = shown[:4]
	}
	report := "; the subtask names " + strings.Join(shown, ", ")
	if len(areas) > len(shown) {
		report += " (+" + strconv.Itoa(len(areas)-len(shown)) + " more)"
	}
	return report + ", so every member that writes them queues on the team write token" +
		" — dispatch members on distinct files to keep them concurrent"
}
