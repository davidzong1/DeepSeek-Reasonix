package team

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/fileutil"
)

// ownerUsageFile is the writer-published usage observation, a sibling of
// .meta.json rather than a field inside it. Three reasons, all load-bearing:
// a malformed .meta.json makes every reader fail closed (Fingerprint reports
// Corrupt), the metadata document carries the CAS revision adoption contends
// on, and telemetry is written far more often than the history identity. Dot
// prefixed like .meta.json: control data one process publishes for another,
// not a content document.
const ownerUsageFile = ".usage.json"

// ownerUsageMaxBytes bounds what a reader will load. A larger file is not a
// usage observation, and telemetry must not be read whole into memory at poll
// rate just because something wrote garbage there.
const ownerUsageMaxBytes = 64 << 10

// OwnerUsageLastTurn mirrors the provider usage fields the cache gauges read.
// Mirrored instead of imported: package team is the storage domain and stays
// free of the agent/provider tree (see doc.go), and the published schema must
// survive a provider field reshuffle. The cli layer owns both conversions, so
// the two cannot drift apart.
type OwnerUsageLastTurn struct {
	PromptTokens     int  `json:"prompt_tokens"`
	CompletionTokens int  `json:"completion_tokens"`
	TotalTokens      int  `json:"total_tokens"`
	CacheHitTokens   int  `json:"cache_hit_tokens"`
	CacheMissTokens  int  `json:"cache_miss_tokens"`
	ReasoningTokens  int  `json:"reasoning_tokens"`
	Unknown          bool `json:"unknown,omitempty"`
	// Additive request-shape fields. They say what kind of usage the numbers
	// above describe, so a reader never reads an aggregate or an estimate as one
	// exact request. Older documents simply lack them.
	RequestCount int `json:"request_count,omitempty"`
	// RequestCountSource is the provenance of RequestCount, in the same closed
	// vocabulary MemberCacheRequest uses. Absent on a document written before it
	// existed, which is the unverified case rather than a measured one.
	RequestCountSource  string `json:"request_count_source,omitempty"`
	Estimated           bool   `json:"estimated,omitempty"`
	CacheWriteTokens    int    `json:"cache_write_tokens,omitempty"`
	ContextPromptTokens int    `json:"context_prompt_tokens,omitempty"`
	// CacheDiagnostics is the content-free prefix diagnosis of the latest
	// observed request, for the status band. Absent for a writer that has
	// published no diagnostics, which is not the same as "nothing changed".
	CacheDiagnostics *OwnerUsageLastTurnDiagnostics `json:"cache_diagnostics,omitempty"`
}

// OwnerUsageLastTurnDiagnostics is the prefix identity of the latest observed
// request: hashes and enumerations only, never prompt or schema content. The
// hashes cover the cache-stable system + tools prefix, so a reader must not
// treat them as a provider cache key.
type OwnerUsageLastTurnDiagnostics struct {
	Available             bool     `json:"available"`
	PrefixHash            string   `json:"prefix_hash,omitempty"`
	StablePrefixHash      string   `json:"stable_prefix_hash,omitempty"`
	PrefixChanged         bool     `json:"prefix_changed,omitempty"`
	StablePrefixChanged   bool     `json:"stable_prefix_changed,omitempty"`
	PrefixChangeReasons   []string `json:"prefix_change_reasons,omitempty"`
	ToolSchemaTokens      int      `json:"tool_schema_tokens_estimate,omitempty"`
	SessionContextDigest  string   `json:"session_context_digest,omitempty"`
	SessionContextReasons []string `json:"session_context_reasons,omitempty"`
}

// OwnerUsageMaintenance is the writer's cumulative maintenance spend for the
// session it owns: how many summarizer requests, provider-visible projection
// installs and rescues it paid for, and how many repeats it blocked. A cache
// rate alone cannot show this cost, so it is published beside the gauges.
//
// It is a snapshot of counters the writer owns, so a reader must treat a stale
// document as unknown rather than as zero — the same rule the gauges follow.
type OwnerUsageMaintenance struct {
	SummaryRequests    int `json:"summary_requests"`
	ProjectionInstalls int `json:"projection_installs"`
	RescueCount        int `json:"rescue_count"`
	RepeatBlocks       int `json:"repeat_blocks"`
}

// OwnerUsageJob is one background job as the follower renders it: display data
// published verbatim, so a follower never fabricates placeholder rows.
type OwnerUsageJob struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Status    string `json:"status"`
	StartedAt int64  `json:"started_at"`
}

// OwnerUsage is one writer's usage observation of one member, published for
// readers in other processes. It is a whole-document replacement with no
// revision: at most one process publishes for an owner (the one that won the
// session bind), and the atomic replace means a reader sees the previous
// document or the complete new one.
type OwnerUsage struct {
	Document
	// PublishedAt is the store clock at publication. Readers treat it as the
	// liveness signal: a stale document means the writer stopped, and the
	// numbers must then be hidden rather than shown as current.
	PublishedAt string `json:"published_at"`
	// ContextUsed/ContextWindow feed the context gauge; CompactRatio is the
	// auto-compaction threshold the gauge measures headroom against.
	ContextUsed   int     `json:"context_used"`
	ContextWindow int     `json:"context_window"`
	CompactRatio  float64 `json:"compact_ratio,omitempty"`
	// LastTurn is the most recent provider usage; its cache fields are what the
	// turn cache-hit rate is computed from.
	LastTurn *OwnerUsageLastTurn `json:"last_turn,omitempty"`
	// CacheHit/CacheMiss are the session totals the average rate uses.
	CacheHit  int `json:"session_cache_hit"`
	CacheMiss int `json:"session_cache_miss"`
	// Jobs are the writer's running background jobs.
	Jobs []OwnerUsageJob `json:"jobs,omitempty"`
	// Maintenance is the writer's cumulative maintenance spend. Absent on a
	// document written before it existed, which is unknown rather than zero.
	Maintenance *OwnerUsageMaintenance `json:"maintenance,omitempty"`
}

// Published reports the publication instant. ok is false for a document whose
// stamp cannot be parsed — a reader must treat that as unknown, never as fresh.
func (u OwnerUsage) Published() (time.Time, bool) {
	raw := strings.TrimSpace(u.PublishedAt)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "20060102T150405.000000000Z"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// Fresh reports whether this observation is recent enough to render. A stamp in
// the future is a clock skew between writers, not an old reading: it clamps to
// age zero. An unparsable stamp is never fresh — for a gauge, the fail-closed
// direction is to hide, not to show.
func (u OwnerUsage) Fresh(now time.Time, ttl time.Duration) bool {
	published, ok := u.Published()
	if !ok {
		return false
	}
	return max(now.Sub(published), 0) <= ttl
}

// WriteUsage publishes one owner's usage observation. No lock is taken: the
// document is replaced atomically and only the session's writer publishes, so a
// lock would add nothing but contention against the metadata CAS on a path
// that publishes far more often. Guarded like writeMeta — every directory
// component is refused when it is a symbolic link — and published through the
// atomic-write chokepoint so a reader never sees a torn document.
func (s *OwnerStore) WriteUsage(ctx context.Context, key OwnerKey, usage OwnerUsage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rel, err := s.usageRel(key)
	if err != nil {
		return err
	}
	usage.Document = Document{SchemaVersion: SchemaVersion}
	data, err := json.Marshal(usage)
	if err != nil {
		return err
	}
	if err := checkSchemaVersion(rel, data); err != nil {
		return err
	}
	if err := s.guardComponents(filepath.Dir(rel)); err != nil {
		return err
	}
	return fileutil.AtomicWriteFileStrict(filepath.Join(s.root, rel), data, 0o600)
}

// ReadUsage reads one owner's usage observation. Every "cannot tell" answer —
// absent file, absent owner directory, oversized document, unreadable or
// schema-mismatched JSON — returns (zero, false, nil): telemetry is optional,
// and no reader may fail closed on it. A returned error is reserved for a
// genuine I/O fault, which callers may log but must not surface as state.
//
// Confinement is os.Root's, exactly as readMeta does it: the reader adds no
// symlink walk of its own, because it must not cost more than the metadata
// poll it rides beside.
func (s *OwnerStore) ReadUsage(ctx context.Context, key OwnerKey) (OwnerUsage, bool, error) {
	if err := ctx.Err(); err != nil {
		return OwnerUsage{}, false, err
	}
	rel, err := s.usageRel(key)
	if err != nil {
		return OwnerUsage{}, false, nil // an invalid key has no telemetry, and saying so is not an error
	}
	root, err := s.openRoot(false)
	if err != nil {
		return OwnerUsage{}, false, nil // no team data root: nothing published yet
	}
	defer root.Close()
	info, err := root.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return OwnerUsage{}, false, nil
		}
		return OwnerUsage{}, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > ownerUsageMaxBytes {
		return OwnerUsage{}, false, nil
	}
	data, err := root.ReadFile(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return OwnerUsage{}, false, nil
		}
		return OwnerUsage{}, false, err
	}
	if err := checkSchemaVersion(rel, data); err != nil {
		return OwnerUsage{}, false, nil
	}
	var usage OwnerUsage
	if err := json.Unmarshal(data, &usage); err != nil {
		return OwnerUsage{}, false, nil
	}
	return usage, true, nil
}

// usageRel resolves one owner's usage document, root-relative, through the same
// id validation every other owner path goes through.
func (s *OwnerStore) usageRel(key OwnerKey) (string, error) {
	rel, err := s.rel(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(rel, ownerUsageFile), nil
}
