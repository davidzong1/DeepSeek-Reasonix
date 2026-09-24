package cachereason

import "slices"

// Kind is what one reason means for cache attribution. Every declared value has
// exactly one, and a consumer that cannot find a value must not assume either.
type Kind string

const (
	// Structural means the request's own framing changed: the system prompt, the
	// tool surface, or the session-context tail. The cause is known and there is
	// nothing to fold — the framing is what it is.
	Structural Kind = "structural"
	// Rewrite means the turn rewrote its own provider-visible content. These are
	// the reasons a fold-boundary change could act on, and the ones a report
	// names as rewrite-correlated.
	Rewrite Kind = "rewrite"
)

// The vocabulary. A producer emits one of these and nothing else; a new kind of
// change is a new value here, which is where its kind gets decided.
const (
	// System, Tools and SessionContext are reported by the agent's own shape
	// comparison, so they are never queued by a caller.
	System         = "system"
	Tools          = "tools"
	SessionContext = "session_context"

	// The authoritative leading system prompt was refreshed. All four are
	// structural: the cache-stable system prefix is exactly what moved.
	SystemPromptRefresh         = "system_prompt_refresh"
	LegacyPinnedSystemMigration = "legacy_pinned_system_migration"
	TeamRolePromptRefresh       = "team_role_prompt_refresh"
	// ManagedRuntimeActivation keeps its hyphenated spelling: it is a published
	// value, and normalizing it would orphan every record already written.
	ManagedRuntimeActivation = "managed-runtime-activation"

	// CompactAuto is the compaction/fold the reason vocabulary exists for.
	CompactAuto = "compact_auto"
	// Prune drops content without summarizing it.
	Prune = "prune"
	// Truncate is the lossy overflow rescue. It stays its own value rather than
	// being folded into CompactAuto, so a reader can tell a rescue from a fold.
	Truncate = "truncate"
	// RewindTruncate and RewindRestore are the two rewind repairs: the first
	// cuts the log back, the second restores it.
	RewindTruncate = "rewind_truncate"
	RewindRestore  = "rewind_restore"
	// GuardianMerge folds a guardian's rewrite into the session.
	GuardianMerge = "guardian_merge"
)

// kinds is the whole vocabulary with each value's kind. Adding a value here is
// the only way to add a reason, which is what keeps a producer and a consumer
// from disagreeing about one.
var kinds = map[string]Kind{
	System:                      Structural,
	Tools:                       Structural,
	SessionContext:              Structural,
	SystemPromptRefresh:         Structural,
	LegacyPinnedSystemMigration: Structural,
	TeamRolePromptRefresh:       Structural,
	ManagedRuntimeActivation:    Structural,

	CompactAuto:    Rewrite,
	Prune:          Rewrite,
	Truncate:       Rewrite,
	RewindTruncate: Rewrite,
	RewindRestore:  Rewrite,
	GuardianMerge:  Rewrite,
}

// KindOf returns what one reason value means. ok is false for a value outside
// the vocabulary, and a caller must report that as unrecognized rather than pick
// the nearest kind: mislabelling a rewrite is what the vocabulary is here to
// prevent.
func KindOf(reason string) (Kind, bool) {
	kind, ok := kinds[reason]
	return kind, ok
}

// Values returns every declared value, sorted, so a report or a test can name
// the vocabulary without repeating it.
func Values() []string {
	out := make([]string, 0, len(kinds))
	for value := range kinds {
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}
