package team

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/fileutil"
)

// cacheRequestFile is one member's bounded per-request cache log, a sibling of
// .usage.json in the owner directory. A sibling rather than a field: .usage.json
// is a whole-document snapshot that every poll replaces, so it can carry the
// latest request but never a history of them.
const cacheRequestFile = ".cache_requests.jsonl"

// cacheRequestMaxBytes bounds one member's log. After an append pushes the file
// past it, the log is compacted to the newest retained records, so the file
// stays proportional to recent observation instead of to member lifetime.
const cacheRequestMaxBytes = 2 << 20

// cacheRequestReadMaxBytes is the ceiling a reader will load. The writer only
// ever leaves the file within one batch of cacheRequestMaxBytes; anything
// larger is not this log, and telemetry must not be read whole into memory just
// because something wrote garbage there.
const cacheRequestReadMaxBytes = 8 << 20

// cacheRequestRetention is how long a record stays eligible. It is applied at
// compaction, so a member that stops being observed ages out on its next append
// rather than pinning its directory forever.
const cacheRequestRetention = 7 * 24 * time.Hour

// MemberCacheRequest is one provider request observed for one Team member. It
// mirrors what the agent and provider reported for that request; package team
// is the storage domain and stays free of the agent/provider tree (see doc.go),
// so the cli layer maps the event into this shape.
//
// The record deliberately carries no prompt, completion, tool argument or
// deliverable text: the cache diagnosis needs request size, cache split and
// prefix identity, none of which require content.
type MemberCacheRequest struct {
	Document
	// RequestID correlates this sample with the emitted usage event.
	// RequestIDSource says which identity it is ("turn_event" when the event
	// carried a turn sequence, else "writer").
	RequestID       string `json:"request_id"`
	RequestIDSource string `json:"request_id_source"`
	// ObservedAt is when the usage event was received, UTC. It is distinct from
	// the publish heartbeat .usage.json carries.
	ObservedAt string `json:"observed_at"`
	TeamID     string `json:"team_id"`
	MemberID   string `json:"member_id"`
	// Provider/ModelRef/RouteBucket locate the model and route. RouteBucket is a
	// deliberately coarse, non-identifying label; account and endpoint values
	// never reach this record.
	Provider    string `json:"provider,omitempty"`
	ModelRef    string `json:"model_ref,omitempty"`
	RouteBucket string `json:"route_bucket,omitempty"`
	// TurnID and SessionSequence map the emitting event's turn identity. They
	// are empty when the event carried none, which is recorded, not invented.
	TurnID          string `json:"turn_id,omitempty"`
	SessionSequence uint64 `json:"session_sequence,omitempty"`
	// SessionID is the only field here that groups requests into a session. Empty
	// means observed outside a turn: a coverage gap, never a session of its own.
	// SessionRequestSeq restarts when a member backend is rebuilt: not monotonic within one SessionID.
	SessionID string `json:"session_id,omitempty"`
	// SessionRequestSeq counts observed requests within one writer's session.
	SessionRequestSeq int `json:"session_request_seq,omitempty"`
	// SecondsSincePrevRequest and HasPrevRequest are per writer session. A first
	// request has no interval; it is reported as a first request, never as a
	// zero-second gap.
	SecondsSincePrevRequest float64 `json:"seconds_since_prev_request,omitempty"`
	HasPrevRequest          bool    `json:"has_prev_request,omitempty"`
	// PromptTokens is the provider's billable input, which for a multi-attempt
	// aggregate is the sum of every attempt. ContextPromptTokens is the settled
	// attempt's own prompt size and is what the report buckets on when present.
	PromptTokens        int `json:"prompt_tokens"`
	ContextPromptTokens int `json:"context_prompt_tokens,omitempty"`
	CacheHitTokens      int `json:"cache_hit_tokens"`
	// CacheMissTokens is always a reported number: a response carrying no cache
	// read has no split and leaves the baseline. CacheHitTokens == 0 therefore
	// reads as "no cache read was reported", never as a cold start.
	CacheMissTokens int `json:"cache_miss_tokens"`
	// CacheWriteTokens is a subset of CacheMissTokens, never an addition to it.
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	RequestCount     int `json:"request_count"`
	// RequestCountSource names where RequestCount came from. The compatibility
	// rule "zero means one" cannot express "nobody measured it", so a reader that
	// must not treat an unmeasured request as a single one reads this instead.
	RequestCountSource string `json:"request_count_source,omitempty"`
	UsageUnknown       bool   `json:"usage_unknown,omitempty"`
	UsageEstimated     bool   `json:"usage_estimated,omitempty"`
	UsageSource        string `json:"usage_source,omitempty"`
	FinishReason       string `json:"finish_reason,omitempty"`
	ContextUsed        int    `json:"context_used,omitempty"`
	ContextWindow      int    `json:"context_window,omitempty"`
	// AccountingValid is false when the token fields are negative or the cache
	// split does not close against the prompt. The values themselves are stored
	// unmodified: a quality anomaly is reported, never silently corrected.
	AccountingValid  bool     `json:"accounting_valid"`
	AccountingIssues []string `json:"accounting_issues,omitempty"`
	// DiagnosticsAvailable distinguishes "the agent produced no diagnostics" from
	// "the diagnostics were all zero". The prefix fields below are meaningless
	// when it is false.
	DiagnosticsAvailable     bool     `json:"diagnostics_available"`
	PrefixHash               string   `json:"prefix_hash,omitempty"`
	StablePrefixHash         string   `json:"stable_prefix_hash,omitempty"`
	PrefixChanged            bool     `json:"prefix_changed,omitempty"`
	StablePrefixChanged      bool     `json:"stable_prefix_changed,omitempty"`
	PrefixChangeReasons      []string `json:"prefix_change_reasons,omitempty"`
	ToolSchemaTokensEstimate int      `json:"tool_schema_tokens_estimate,omitempty"`
	SessionContextDigest     string   `json:"session_context_digest,omitempty"`
	SessionContextReasons    []string `json:"session_context_reasons,omitempty"`
}

// Request-count provenance. The vocabulary is closed and additive: a reader
// that meets a value it does not know must treat the count as unverified, which
// is the safe direction for a sample that may enter a rate. "The usage said
// one" and "the usage said nothing, so one was assumed" are different facts,
// and the compatibility rule "zero means one" cannot express the second.
const (
	// RequestCountObserved means the emitted usage carried a count a producer had
	// actually measured — the HTTP attempt count of the stream it came from.
	RequestCountObserved = "observed"
	// RequestCountDefaulted means the usage carried no count and the writer stored
	// the compatibility default of one request.
	RequestCountDefaulted = "defaulted"
	// RequestCountUnrecorded means the source recorded no provenance at all: a
	// document written before this field existed, or a foreign line. The count
	// beside it is an assumption the reader cannot audit.
	RequestCountUnrecorded = "unrecorded"
)

// RequestCountVerified reports whether this record's request count is a
// measurement. Only a verified count of exactly one qualifies a sample as a
// single provider request; everything else is a disclosed exclusion.
func (r MemberCacheRequest) RequestCountVerified() bool {
	return r.RequestCountSource == RequestCountObserved
}

// RequestCountSourceOf names the provenance of one usage's request count from
// the two facts the producer has: the count it will store and whether that
// count was measured. It lives here rather than in the writer so the vocabulary
// has one owner, and the cli layer — the only place the provider tree and this
// package meet — passes the primitives in.
func RequestCountSourceOf(count int, observed bool) string {
	if observed && count > 0 {
		return RequestCountObserved
	}
	return RequestCountDefaulted
}

// Accounting reports whether the record's token fields are internally
// consistent, and names every way they are not. Issues are additive labels, so
// a reader can exclude a sample by reason instead of by a single boolean.
func (r MemberCacheRequest) Accounting() (bool, []string) {
	var issues []string
	fields := []struct {
		name  string
		value int
	}{
		{"prompt_tokens", r.PromptTokens},
		{"context_prompt_tokens", r.ContextPromptTokens},
		{"cache_hit_tokens", r.CacheHitTokens},
		{"cache_miss_tokens", r.CacheMissTokens},
		{"cache_write_tokens", r.CacheWriteTokens},
		{"completion_tokens", r.CompletionTokens},
		{"request_count", r.RequestCount},
	}
	for _, f := range fields {
		if f.value < 0 {
			issues = append(issues, "negative:"+f.name)
		}
	}
	if split := r.CacheHitTokens + r.CacheMissTokens; split > 0 && r.PromptTokens > 0 && split > r.PromptTokens {
		issues = append(issues, "split_exceeds_prompt")
	}
	return len(issues) == 0, issues
}

// stampAccounting records the accounting verdict on the record. It is applied
// on the write path so a sample can never be stored with an unset verdict that
// a reader would have to guess at.
func (r *MemberCacheRequest) stampAccounting() {
	r.AccountingValid, r.AccountingIssues = r.Accounting()
}

// AppendCacheRequests appends observed requests to one member's bounded log.
// One O_APPEND write lands the whole batch, so a crash mid-batch leaves at most
// a torn trailing line that readers skip. No lock is taken: the log has exactly
// one writer (the process that won the member's session bind), and a reader
// sees the previous file or the complete new one.
//
// The append is best-effort by contract — a caller that cannot record telemetry
// must still serve the member — so the error is returned for logging and must
// not be surfaced as member state.
func (s *OwnerStore) AppendCacheRequests(ctx context.Context, key OwnerKey, recs ...MemberCacheRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(recs) == 0 {
		return nil
	}
	rel, err := s.cacheRequestRel(key)
	if err != nil {
		return err
	}
	if err := s.guardComponents(filepath.Dir(rel)); err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, rec := range recs {
		rec.Document = Document{SchemaVersion: SchemaVersion}
		rec.stampAccounting()
		line, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	root, err := s.openRoot(true)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Dir(rel), 0o700); err != nil {
		return fmt.Errorf("team: create owner dir for request log: %w", err)
	}
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(buf.Bytes())
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	return s.compactCacheRequests(root, rel)
}

// ReadCacheRequests returns one member's retained requests, oldest first. Every
// "cannot tell" answer — absent log, unreadable or oversized document — is an
// empty result rather than an error, exactly as ReadUsage answers: telemetry is
// optional and no reader may fail closed on it. A returned error is a genuine
// I/O fault the caller may log.
func (s *OwnerStore) ReadCacheRequests(ctx context.Context, key OwnerKey) ([]MemberCacheRequest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rel, err := s.cacheRequestRel(key)
	if err != nil {
		return nil, nil // an invalid key has no telemetry, and saying so is not an error
	}
	root, err := s.openRoot(false)
	if err != nil {
		return nil, nil // no team data root: nothing recorded yet
	}
	defer root.Close()
	info, err := root.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > cacheRequestReadMaxBytes {
		return nil, nil
	}
	f, err := root.Open(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	return decodeCacheRequests(f)
}

// decodeCacheRequests reads one log. A malformed line — a torn tail from a crash
// mid-append, or a hand-edited file — is skipped, never read as a partial
// record and never failing the whole read.
func decodeCacheRequests(r io.Reader) ([]MemberCacheRequest, error) {
	var out []MemberCacheRequest
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec MemberCacheRequest
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, sc.Err()
}

// compactCacheRequests brings the log back under its ceiling once an append has
// pushed it past. Retention runs first, then the oldest records are dropped by
// whole halves until the encoded result fits, so one compaction always restores
// headroom instead of being re-triggered by the next append.
//
// The replace is atomic, so a concurrent reader sees the previous log or the
// complete compacted one. Compaction only ever removes the oldest records; it
// never rewrites a retained record.
func (s *OwnerStore) compactCacheRequests(root *os.Root, rel string) error {
	info, err := root.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Size() <= cacheRequestMaxBytes {
		return nil
	}
	f, err := root.Open(rel)
	if err != nil {
		return err
	}
	recs, rerr := decodeCacheRequests(f)
	if cerr := f.Close(); rerr == nil {
		rerr = cerr
	}
	if rerr != nil {
		return rerr
	}
	kept := retainedCacheRequests(recs, s.now())
	for len(kept) > 1 && encodedCacheRequestBytes(kept) > cacheRequestMaxBytes {
		kept = kept[len(kept)/2:]
	}
	var buf bytes.Buffer
	for _, rec := range kept {
		line, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return fileutil.AtomicWriteFileStrict(filepath.Join(s.root, rel), buf.Bytes(), 0o600)
}

// retainedCacheRequests drops the records that may no longer be kept: one whose
// observation time cannot be parsed, and one older than the retention window.
// A record with no usable timestamp can never be aged, so keeping it would make
// the log unbounded in the only dimension the policy controls.
func retainedCacheRequests(recs []MemberCacheRequest, now time.Time) []MemberCacheRequest {
	cutoff := now.Add(-cacheRequestRetention)
	kept := make([]MemberCacheRequest, 0, len(recs))
	for _, rec := range recs {
		at, ok := rec.observed()
		if !ok || at.Before(cutoff) {
			continue
		}
		kept = append(kept, rec)
	}
	return kept
}

// observed parses the record's observation stamp.
func (r MemberCacheRequest) observed() (time.Time, bool) {
	raw := strings.TrimSpace(r.ObservedAt)
	if raw == "" {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

// encodedCacheRequestBytes estimates the log size of a record set without
// building it, so the retention loop can decide before allocating.
func encodedCacheRequestBytes(recs []MemberCacheRequest) int {
	total := 0
	for _, rec := range recs {
		data, err := json.Marshal(rec)
		if err != nil {
			continue
		}
		total += len(data) + 1
	}
	return total
}

// cacheRequestRel resolves one member's request log, root-relative, through the
// same id validation every other owner path goes through.
func (s *OwnerStore) cacheRequestRel(key OwnerKey) (string, error) {
	rel, err := s.rel(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(rel, cacheRequestFile), nil
}
