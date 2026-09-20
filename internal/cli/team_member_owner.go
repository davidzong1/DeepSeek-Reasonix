package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/session"
	"reasonix/internal/team"
)

// memberSessionRoots returns the directories one member's session file is looked
// for in, in probe order, plus the directory a first-entry file is created under.
// The canonical owner directory under the team data root is probed first — where
// an adopted history lives, and the authority once written — then the
// controller's own directory (what the picker resets and a same-directory launch
// writes), then the workspace's project root. The workspace root is the stable
// key, captured when the overlay opens rather than read from the process CWD.
// An unknown workspace root leaves the controller's own directory as the only
// candidate, which is the historical behavior. Probing is read-only either way:
// the file is never moved or rewritten.
func memberSessionRoots(ctrl *control.Controller, workspaceRoot string) (roots []string, create string) {
	return memberSessionRootsWithOwner(ctrl, workspaceRoot, "")
}

// memberSessionRootsWithOwner is memberSessionRoots with the canonical owner
// directory of one member folded in as the highest-priority probe — and, when
// there is one, as the create target: a member's history belongs in its owner
// directory, so that is where a first entry is written and where an adopted
// history is read from. An empty ownerDir keeps the historical behavior exactly
// (see memberSessionRoots).
func memberSessionRootsWithOwner(ctrl *control.Controller, workspaceRoot, ownerDir string) (roots []string, create string) {
	roots, create = memberSessionRootCandidates(ctrl, workspaceRoot)
	if dir := strings.TrimSpace(ownerDir); dir != "" {
		roots = append([]string{dir}, roots...)
		create = dir
	}
	return roots, create
}

// memberSessionRootCandidates is the historical probe order: the controller's own
// directory, then the workspace's project root, each contributing its versioned
// form before its logical one. A version update moves the execution store from
// <root>/sessions to <root>/sessions-v4 (session.RootForLegacyDir), so a member
// whose history predates the bump lives only under the logical root; versioned
// candidates are probed before logical ones because the versioned store is what
// this build executes on, and a stale logical copy must not shadow it.
// session.RootForLegacyDir is not injective — two logical roots can share one
// versioned root — so seen collapses the versioned side too.
//
// The create target is returned separately rather than pinned to the end of the
// probe order: a v3-exclusive controller creates under the versioned root, which
// is also its highest-priority probe, so pinning it last would deny the current
// store the priority this ordering exists to give it. A legacy controller's file
// is its execution identity, so it creates under the logical root.
func memberSessionRootCandidates(ctrl *control.Controller, workspaceRoot string) (roots []string, create string) {
	ctrlDir := strings.TrimSpace(ctrl.SessionDir())
	stable := ""
	if root := strings.TrimSpace(workspaceRoot); root != "" {
		stable = config.ProjectSessionDir(root)
	}
	if stable == "" {
		stable = config.SessionDir()
	}
	create = stable
	if ctrl.UsesExclusiveSession() {
		if versioned := session.RootForLegacyDir(stable); versioned != "" {
			create = versioned
		}
	}
	if create == "" {
		create = ctrlDir
	}
	seen := make(map[string]bool, 4)
	out := make([]string, 0, 4)
	for _, versioned := range []bool{true, false} {
		for _, root := range []string{ctrlDir, stable} {
			if root == "" {
				continue
			}
			candidate := root
			if versioned {
				candidate = session.RootForLegacyDir(root)
			}
			if candidate == "" || seen[candidate] {
				continue
			}
			seen[candidate] = true
			out = append(out, candidate)
		}
	}
	// The create target is normally one of the probes; it is appended only when it
	// is not, so a first entry always lands in a directory this build reads.
	if create != "" && !seen[create] {
		out = append(out, create)
	}
	return out, create
}

// recordMemberOwnerHistory publishes one member's canonical history identity
// into its owner metadata so a second window can tell that the history changed.
// The stem is the backend's own opaque history stamp — the same value a peer
// computes — and bump is false for the assembly-time call, which only
// establishes the identity: binding a member must not read as a change. The
// write is a locked metadata update, so it never races a concurrent owner
// writer. A nil owner store is a host without canonical storage.
func recordMemberOwnerHistory(ctx context.Context, owners *team.OwnerStore, b team.MemberBinding, ctrl memberHistoryStamper, bump bool) error {
	if owners == nil || ctrl == nil {
		return nil
	}
	key := team.OwnerKey{TeamID: b.Team, MemberID: b.MemberID}
	if err := owners.BumpHistory(ctx, key, ctrl.HistoryStamp(), bump); err != nil {
		return fmt.Errorf("member %q: record owner history: %w", b.MemberID, err)
	}
	return nil
}

// memberHistoryStamper is the narrow slice of a member backend the owner
// history record needs: its opaque durable-history identity. Every member
// backend exposes it through control.HistorySync, and it is the same value a
// second window computes for the same owner, so the two cannot drift.
type memberHistoryStamper interface {
	HistoryStamp() string
}

// adoptMemberOwnerHistory migrates one member's existing history into canonical
// owner storage and returns the owner directory to bind against: it resolves the
// member's owner key, adopts the first legacy session unit the probe order would
// have bound, and archives the pre-D5 context tree verbatim when one exists.
//
// A member with no history yet adopts nothing: Init creates the empty owner
// directory and the first entry is written there directly. Every error is
// returned, because a refused adoption is the one case where binding the legacy
// location anyway would leave the member's history in two places, one of them
// stale.
func adoptMemberOwnerHistory(ctx context.Context, ctrl *control.Controller, owners *team.OwnerStore, b team.MemberBinding, roots []string) (string, error) {
	key := team.OwnerKey{TeamID: b.Team, MemberID: b.MemberID}
	paths, _, err := owners.Init(ctx, key)
	if err != nil {
		return "", err
	}
	candidates := make([]string, 0, len(roots))
	for _, root := range roots {
		candidates = append(candidates, filepath.Join(root, b.SessionFile))
	}
	// The v3-exclusive carve-out: goal and checkpoint sidecars are v3 events
	// there, so copying the legacy ones would create the second restore source
	// the exclusive reader refuses.
	opt := team.OwnerAdoptOptions{Exclusive: ctrl != nil && ctrl.UsesExclusiveSession()}
	if _, err := owners.AdoptMemberSessionHistory(ctx, key, candidates, opt); err != nil {
		return "", fmt.Errorf("member %q: adopt session history: %w", b.MemberID, err)
	}
	legacyDir, err := owners.LegacyContextDir(key)
	if err != nil {
		return "", err
	}
	if _, err := owners.AdoptLegacyContext(ctx, key, legacyDir); err != nil {
		return "", fmt.Errorf("member %q: adopt legacy context: %w", b.MemberID, err)
	}
	return paths.Dir, nil
}
