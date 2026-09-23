package builtin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"

	udiff "github.com/aymanbagabas/go-udiff"

	"reasonix/internal/fileops"
	fileenc "reasonix/internal/fileutil/encoding"
	"reasonix/internal/tool"
)

// Shared base of the team-session atomic file pair (atomic_read / atomic_write):
// one copy of the anchor, CAS, hunk, receipt and error semantics, so the reader
// and the writer cannot disagree about what "the version you read" means.

const (
	// atomicReadBudgetBytes caps one read result. It is deliberately far below
	// read_file's formatted-byte cap: the point of the pair is that a member
	// reads a window, not a file.
	atomicReadBudgetBytes = 16 << 10
	// atomicReceiptBudgetBytes caps the write receipt line. It is bounded so a
	// rejection can never be larger than the successful receipt it replaces.
	atomicReceiptBudgetBytes = 512
	// atomicReadConflictBudgetBytes caps the hunk block a stale read/write
	// rejection carries. It is larger than the receipt budget because those
	// hunks are the model's only route back to a correct retry.
	atomicReadConflictBudgetBytes = 2 << 10
	// atomicSnapshotCacheEntries bounds the snapshot cache by entry count, and
	// atomicSnapshotCacheBytes by total bytes. Both are small on purpose: the
	// cache only has to outlive the model's next call.
	atomicSnapshotCacheEntries    = 24
	atomicSnapshotCacheEntryBytes = 1 << 20
	atomicSnapshotCacheBytes      = 4 << 20
	// atomicAutoWholeMaxLines is the size below which mode=auto returns the whole
	// file instead of an outline plus a head window.
	atomicAutoWholeMaxLines = 400
	// atomicReadMaxSourceBytes refuses a target too large to anchor in memory.
	// The anchor hashes the whole file, so an unbounded read here would turn a
	// log file into an OOM.
	atomicReadMaxSourceBytes = 16 << 20
)

// Route names for atomicAnchor. They mirror fileops' route vocabulary: the
// host's unsaved editor buffer versus the file on disk.
const (
	atomicRouteDisk    = "disk"
	atomicRouteOverlay = "overlay"
)

// atomicAnchor is one host-observed version of a target file. It is what a read
// leaves behind and what a write re-checks before it publishes. It never
// travels to the model as an authorization token: the model only ever sees
// ReadID, and the host re-derives everything else from it.
type atomicAnchor struct {
	Path        string
	Route       string
	ReadID      string
	Version     string
	ContentHash string
	Bytes       int
	Lines       int
	// Missing records an observation of Absent, which is what authorizes a
	// non-overwriting create.
	Missing bool
}

// atomicCapture reads path through the same route read_file uses (the host's
// unsaved editor buffer first, disk second), records the observation in the
// session store, and returns the anchor a later write builds on. A missing
// target yields an Absent observation and Missing=true rather than an error, so
// the caller decides whether absence is a failure (a read) or the precondition
// (a create).
func atomicCapture(ctx context.Context, overlay FileOverlay, path string) (atomicAnchor, error) {
	anchor, _, err := atomicCaptureSource(ctx, overlay, path)
	return anchor, err
}

// atomicCaptureSource is atomicCapture plus the source it observed, so a reader
// can render exactly the bytes it just anchored instead of reading the target a
// second time (which could observe a different version than the one it
// reported).
func atomicCaptureSource(ctx context.Context, overlay FileOverlay, path string) (atomicAnchor, editSource, error) {
	src, err := readEditSource(ctx, overlay, path)
	if err != nil {
		if !os.IsNotExist(err) {
			return atomicAnchor{}, editSource{}, err
		}
		fileops.FromContext(ctx).ObserveAbsent(fileops.DiskTarget(path, nil))
		return atomicAnchor{Path: path, Route: atomicRouteDisk, Missing: true}, editSource{}, nil
	}
	target, version, err := src.observation(overlay, path)
	if err != nil {
		return atomicAnchor{}, editSource{}, err
	}
	fileops.FromContext(ctx).ObservePresent(target, version)
	content := []byte(src.content)
	anchor := atomicAnchor{
		Path:        path,
		Route:       atomicSourceRoute(src),
		Version:     string(version),
		ContentHash: atomicContentHash(content),
		Bytes:       len(content),
		Lines:       atomicLineCount(content),
	}
	anchor.ReadID = atomicReadID(anchor.Route, path, version, content)
	atomicRememberSnapshot(anchor, content)
	return anchor, src, nil
}

// atomicAnchorFor resolves the anchor a write compares against.
//
// since == "" means "the version this session last observed": the session store
// is authoritative for which version that is, and the snapshot cache supplies
// the bytes to diff against when the file has moved on. A non-empty since must
// name a read this host actually performed — a model-invented id resolves to
// nothing and is refused rather than trusted.
func atomicAnchorFor(ctx context.Context, overlay FileOverlay, path, since string) (atomicAnchor, error) {
	since = strings.TrimSpace(since)
	if since == "" {
		return atomicAnchorFromObservation(ctx, overlay, path)
	}
	if !atomicReadIDShape(since) {
		return atomicAnchor{}, atomicNotObservedError(path, "the since value is not a read id this host issues; read the file with atomic_read, then retry")
	}
	if anchor, ok := atomicCachedAnchor(since, path); ok {
		return anchor, nil
	}
	return atomicAnchor{}, atomicNotObservedError(path, "that read id is no longer available in this session; read the file with atomic_read, then retry")
}

// atomicAnchorFromObservation builds the anchor for the session's last
// observation of path. When the target still matches that observation the
// anchor is the current version; when it has moved on, the anchor keeps
// describing what the model saw, so the CAS check reports the drift instead of
// silently writing over a teammate's edit.
func atomicAnchorFromObservation(ctx context.Context, overlay FileOverlay, path string) (atomicAnchor, error) {
	src, err := readEditSource(ctx, overlay, path)
	if err != nil {
		if !os.IsNotExist(err) {
			return atomicAnchor{}, err
		}
		return atomicAnchor{}, atomicNotObservedError(path, "the file does not exist; create it with atomic_write mode=create, or read it with atomic_read first")
	}
	target, version, err := src.observation(overlay, path)
	if err != nil {
		return atomicAnchor{}, err
	}
	observed := fileops.FromContext(ctx).Get(target)
	if observed.Kind != fileops.Present {
		return atomicAnchor{}, atomicNotObservedError(path, "read any current window of this file with atomic_read, then retry")
	}
	route := atomicSourceRoute(src)
	content := []byte(src.content)
	if observed.Version == version {
		// A matching version is not proof of matching bytes: it is metadata
		// only, and a same-size rewrite inside one clock tick collides on all of
		// it. The cached bytes this session read are the authority.
		if cached, ok := atomicCachedSnapshot(path, string(observed.Version)); ok {
			if atomicContentHash(cached) != atomicContentHash(content) {
				anchor := atomicAnchor{
					Path:        path,
					Route:       route,
					Version:     string(observed.Version),
					ContentHash: atomicContentHash(cached),
					Bytes:       len(cached),
					Lines:       atomicLineCount(cached),
				}
				anchor.ReadID = atomicReadID(route, path, observed.Version, cached)
				return anchor, nil
			}
		}
		anchor := atomicAnchor{
			Path:        path,
			Route:       route,
			Version:     string(version),
			ContentHash: atomicContentHash(content),
			Bytes:       len(content),
			Lines:       atomicLineCount(content),
		}
		anchor.ReadID = atomicReadID(route, path, version, content)
		return anchor, nil
	}
	// The observed version is older than the bytes on the target. Describe the
	// observed version; an empty ContentHash means "known stale, bytes not
	// recoverable", which the recheck reports as a conflict without a hunk.
	anchor := atomicAnchor{Path: path, Route: route, Version: string(observed.Version)}
	if cached, ok := atomicCachedSnapshot(path, string(observed.Version)); ok {
		anchor.ContentHash = atomicContentHash(cached)
		anchor.Bytes = len(cached)
		anchor.Lines = atomicLineCount(cached)
		anchor.ReadID = atomicReadID(route, path, observed.Version, cached)
	}
	return anchor, nil
}

// atomicRecheck re-reads the source and compares it with the anchor before a
// write publishes. It returns the current bytes on success — the writer splices
// on exactly those bytes — and an FSStaleVersion conflict carrying the changed
// hunks when the target moved on.
func atomicRecheck(ctx context.Context, overlay FileOverlay, path string, anchor atomicAnchor) ([]byte, error) {
	_, current, err := atomicRecheckSource(ctx, overlay, path, anchor)
	return current, err
}

// atomicRecheckSource is atomicRecheck plus the read route. A replace or patch
// must write back to the same store it read from and re-encode with the same
// encoding, so the caller needs the whole editSource, not just its bytes —
// re-reading through readEditSource a second time would both cost a second read
// and race the first one.
func atomicRecheckSource(ctx context.Context, overlay FileOverlay, path string, anchor atomicAnchor) (editSource, []byte, error) {
	src, err := readEditSource(ctx, overlay, path)
	if err != nil {
		if !os.IsNotExist(err) {
			return editSource{}, nil, err
		}
		if anchor.Missing {
			return editSource{}, nil, nil
		}
		return editSource{}, nil, atomicConflict(path, nil, nil, anchor)
	}
	current := []byte(src.content)
	if anchor.Missing {
		return editSource{}, nil, atomicConflict(path, nil, current, anchor)
	}
	if anchor.ContentHash == "" || atomicContentHash(current) != anchor.ContentHash {
		return editSource{}, nil, atomicConflict(path, atomicShownBytes(anchor), current, anchor)
	}
	return src, current, nil
}

// atomicSourceRoute names the store an editSource was read from.
func atomicSourceRoute(src editSource) string {
	if src.overlay {
		return atomicRouteOverlay
	}
	return atomicRouteDisk
}

// atomicReadBytes reads path through the read route (overlay first, disk
// second) and returns decoded UTF-8 bytes. It is the read half of the CAS pair,
// so a writer never compares its anchor against a differently-decoded source
// than the reader observed.
func atomicReadBytes(ctx context.Context, overlay FileOverlay, path string) ([]byte, error) {
	if overlay != nil {
		if buffered, ok := overlay.ReadTextFile(ctx, path); ok {
			return []byte(buffered), nil
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > atomicReadMaxSourceBytes {
		return nil, fmt.Errorf("%s is larger than the %d-byte atomic read limit", path, atomicReadMaxSourceBytes)
	}
	enc, _ := fileenc.Detect(data)
	return fileenc.Decode(data, enc), nil
}

// atomicShownBytes recovers the exact bytes an anchor describes, for the
// conflict diff. It resolves by read id and verifies the result against the
// anchor's own content hash, because a path+version lookup alone is not enough:
// two writes that land inside one filesystem timestamp tick share a version,
// and the path index then names the newer bytes. Returning nil is the honest
// answer — a conflict without a hunk is still a refusal, whereas a diff against
// the wrong side would be a lie.
func atomicShownBytes(anchor atomicAnchor) []byte {
	if anchor.ContentHash == "" {
		return nil
	}
	if anchor.ReadID != "" {
		if content, ok := atomicCachedContent(anchor.ReadID); ok && atomicContentHash(content) == anchor.ContentHash {
			return content
		}
	}
	content, ok := atomicCachedSnapshot(anchor.Path, anchor.Version)
	if !ok || atomicContentHash(content) != anchor.ContentHash {
		return nil
	}
	return content
}

// atomicReadID derives the opaque id a read leaves in its result header and a
// later write cites as `since`. It is content-addressed, so the same bytes at
// the same version always yield the same id and a fabricated one never
// resolves. It contains no host-internal state beyond the target's own path,
// and it is never an authorization input.
//
// Two different reads of the same version collapse to one id. That is what
// makes `since` a content reference rather than a call reference: a member that
// read the file twice, or two members that read the same bytes, all cite the
// same anchor — and a version the host has never served still cannot be named,
// because the id is only ever minted from bytes the host read itself.
func atomicReadID(route, path string, version fileops.Version, content []byte) string {
	h := sha256.New()
	h.Write([]byte("reasonix/atomic-read/v1\x00"))
	h.Write([]byte(route))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write([]byte(version))
	h.Write([]byte{0})
	h.Write([]byte(atomicContentHash(content)))
	return "r-" + hex.EncodeToString(h.Sum(nil)[:8])
}

func atomicReadIDShape(id string) bool {
	if !strings.HasPrefix(id, "r-") || len(id) <= 2 {
		return false
	}
	for _, r := range id[2:] {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// atomicDeltaHunks renders the changed regions between two byte snapshots as
// unified-diff hunks with three lines of context, clipped to budget. It returns
// nil when nothing changed.
func atomicDeltaHunks(oldBytes, newBytes []byte, budget int) []string {
	if string(oldBytes) == string(newBytes) {
		return nil
	}
	text, err := udiff.ToUnified("before", "after", string(oldBytes), udiff.Strings(string(oldBytes), string(newBytes)), 3)
	if err != nil || strings.TrimSpace(text) == "" {
		return atomicClipHunks(strings.Split(string(newBytes), "\n"), budget)
	}
	var hunks []string
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		// Drop the ---/+++ file header pair: the caller already names the path.
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			continue
		}
		hunks = append(hunks, line)
	}
	return atomicClipHunks(hunks, budget)
}

// atomicClipHunks keeps whole hunks while they fit the budget and stops at the
// first one that does not, appending a marker that says how much was withheld.
// A clipped diff is still a diff: truncating mid-hunk would emit lines that do
// not correspond to any real edit. A hunk larger than the entire budget is the
// one case where the output is honestly partial, so it is clipped on a rune
// boundary instead.
func atomicClipHunks(hunks []string, budget int) []string {
	if budget <= 0 {
		return nil
	}
	var kept []string
	used := 0
	for i, hunk := range hunks {
		marker := ""
		if remaining := len(hunks) - i - 1; remaining > 0 {
			marker = fmt.Sprintf("…[%d more diff line(s) withheld]…", remaining)
		}
		// Every element costs its bytes plus one separator byte, so the joined
		// result stays strictly inside the budget.
		if used+len(hunk)+1+len(marker)+1 <= budget {
			kept = append(kept, hunk)
			used += len(hunk) + 1
			continue
		}
		if room := budget - used - len(marker) - 1; room > 0 {
			kept = append(kept, atomicClipUTF8(hunk, room))
		}
		if marker != "" {
			kept = append(kept, marker)
		}
		return kept
	}
	return kept
}

// atomicConflict builds the FSStaleVersion error a lost update produces. The
// cause carries the hunks between what the model saw and what is on the target
// now, so the model re-reads and retries instead of guessing.
func atomicConflict(path string, shown, current []byte, anchor atomicAnchor) error {
	cause := fmt.Errorf("%w: %s", ErrFileChanged, path)
	if len(current) > 0 {
		if hunks := atomicDeltaHunks(shown, current, atomicReadConflictBudgetBytes); len(hunks) > 0 {
			cause = fmt.Errorf("%w: %s\n%s", ErrFileChanged, path, strings.Join(hunks, "\n"))
		}
	}
	return &tool.OperationError{
		Diagnostic: tool.OperationDiagnostic{
			Code:             tool.FSStaleVersion,
			Path:             path,
			ExpectedSnapshot: anchor.ReadID,
			Recovery:         "the file changed after you read it; read it again with atomic_read, then retry — or use append, which never loses an update",
			Retryable:        true,
			RetryBudget:      2,
		},
		Cause: cause,
	}
}

// atomicNotObservedError is the "read before you write" rejection. Every mode
// that replaces existing bytes needs one, so the shape is shared.
func atomicNotObservedError(path, recovery string) error {
	return &tool.OperationError{
		Diagnostic: tool.OperationDiagnostic{
			Code:     tool.FSNotObserved,
			Path:     path,
			Recovery: recovery,
		},
		Cause: fmt.Errorf("no current observation of %s in this session", path),
	}
}

// atomicReceiptLine renders one bounded receipt line. bytes and lines are
// omitted when zero, so a caller that only has span counts produces exactly
// `patch path (+12 -3) 2140→2149 lines`.
func atomicReceiptLine(kind, path string, bytes, lines int, extra ...string) string {
	parts := make([]string, 0, 4+len(extra))
	parts = append(parts, kind, path)
	if bytes > 0 {
		parts = append(parts, fmt.Sprintf("%dB", bytes))
	}
	if lines > 0 {
		parts = append(parts, fmt.Sprintf("%d lines", lines))
	}
	for _, item := range extra {
		if strings.TrimSpace(item) != "" {
			parts = append(parts, item)
		}
	}
	return atomicClipUTF8(strings.Join(parts, " "), atomicReceiptBudgetBytes)
}

// atomicClipUTF8 clips text to budget bytes on a rune boundary, marking the
// cut. It never splits a multi-byte sequence: a torn rune is exactly the kind
// of corruption the pair exists to prevent.
func atomicClipUTF8(text string, budget int) string {
	if budget <= 0 || len(text) <= budget {
		return text
	}
	marker := "…[clipped]…"
	if budget <= len(marker) {
		return clipUTF8Prefix(marker, budget)
	}
	return clipUTF8Prefix(text, budget-len(marker)) + marker
}

func atomicContentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// atomicLineCount counts lines the way a reader numbers them: a trailing
// newline terminates the last line rather than starting an empty one.
func atomicLineCount(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	count := strings.Count(string(content), "\n")
	if content[len(content)-1] != '\n' {
		count++
	}
	return count
}

// atomicSplitLines splits decoded content into the lines a reader numbers.
func atomicSplitLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if last := len(lines) - 1; last >= 0 && lines[last] == "" {
		lines = lines[:last]
	}
	return lines
}

// Snapshot cache: the bytes behind a read id. Keyed by a content-addressed id,
// so an entry shared across sessions is byte-identical by construction, and a
// miss is reported as "read it again" rather than as a silent success.

type atomicSnapshot struct {
	readID  string
	path    string
	version string
	route   string
	content []byte
}

// atomicSnapshotCache keeps the bytes behind a read id so `since` and the
// conflict diff can resolve them after a teammate has already moved the file
// on. byID answers a cited read id; byPath answers "what did the model see at
// this version" for the since="" observation route. order is oldest-first for
// eviction, and an entry is counted once no matter how many indexes name it.
type atomicSnapshotCache struct {
	mu     sync.Mutex
	byID   map[string]*atomicSnapshot
	byPath map[string]*atomicSnapshot
	order  []*atomicSnapshot
	total  int
}

var atomicSnapshots = atomicSnapshotCache{byID: map[string]*atomicSnapshot{}, byPath: map[string]*atomicSnapshot{}}

func atomicRememberSnapshot(anchor atomicAnchor, content []byte) {
	if anchor.ReadID == "" || len(content) > atomicSnapshotCacheEntryBytes {
		return
	}
	c := &atomicSnapshots
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := &atomicSnapshot{
		readID:  anchor.ReadID,
		path:    anchor.Path,
		version: anchor.Version,
		route:   anchor.Route,
		content: append([]byte(nil), content...),
	}
	// A different read id at the same path+version (two writes inside one
	// timestamp tick) takes over only the path index; dropping the older entry
	// would strand a `since` the model still holds.
	if previous := c.byID[entry.readID]; previous != nil {
		c.removeLocked(previous)
	}
	if previous := c.byPath[atomicSnapshotPathKey(entry.path, entry.version)]; previous != nil {
		delete(c.byPath, atomicSnapshotPathKey(entry.path, entry.version))
	}
	c.byID[entry.readID] = entry
	c.byPath[atomicSnapshotPathKey(entry.path, entry.version)] = entry
	c.order = append(c.order, entry)
	c.total += len(entry.content)
	c.evictLocked()
}

func atomicSnapshotPathKey(path, version string) string {
	return path + "\x00" + version
}

// removeLocked drops one entry and every index key that still names it.
func (c *atomicSnapshotCache) removeLocked(entry *atomicSnapshot) {
	if entry == nil {
		return
	}
	if c.byID[entry.readID] == entry {
		delete(c.byID, entry.readID)
	}
	key := atomicSnapshotPathKey(entry.path, entry.version)
	if c.byPath[key] == entry {
		delete(c.byPath, key)
	}
	kept := c.order[:0]
	for _, item := range c.order {
		if item != entry {
			kept = append(kept, item)
		}
	}
	c.order = kept
	c.total -= len(entry.content)
	if c.total < 0 {
		c.total = 0
	}
}

func (c *atomicSnapshotCache) evictLocked() {
	for (len(c.order) > atomicSnapshotCacheEntries || c.total > atomicSnapshotCacheBytes) && len(c.order) > 0 {
		c.removeLocked(c.order[0])
	}
}

func atomicCachedAnchor(readID, path string) (atomicAnchor, bool) {
	atomicSnapshots.mu.Lock()
	defer atomicSnapshots.mu.Unlock()
	entry, ok := atomicSnapshots.byID[readID]
	if !ok || entry.path != path {
		return atomicAnchor{}, false
	}
	return atomicSnapshotAnchor(entry), true
}

// atomicCachedContent returns the bytes a read id was minted from. It is the
// exact lookup delta needs: resolving by path+version could hand back a
// different read's bytes when two versions collide.
func atomicCachedContent(readID string) ([]byte, bool) {
	atomicSnapshots.mu.Lock()
	defer atomicSnapshots.mu.Unlock()
	entry, ok := atomicSnapshots.byID[readID]
	if !ok {
		return nil, false
	}
	return entry.content, true
}

func atomicCachedSnapshot(path, version string) ([]byte, bool) {
	atomicSnapshots.mu.Lock()
	defer atomicSnapshots.mu.Unlock()
	entry, ok := atomicSnapshots.byPath[atomicSnapshotPathKey(path, version)]
	if !ok {
		return nil, false
	}
	return entry.content, true
}

func atomicSnapshotAnchor(entry *atomicSnapshot) atomicAnchor {
	return atomicAnchor{
		Path:        entry.path,
		Route:       entry.route,
		ReadID:      entry.readID,
		Version:     entry.version,
		ContentHash: atomicContentHash(entry.content),
		Bytes:       len(entry.content),
		Lines:       atomicLineCount(entry.content),
	}
}

// atomicResetSnapshotCache drops every cached snapshot. Tests use it to keep
// the process-global cache from coupling otherwise independent cases.
func atomicResetSnapshotCache() {
	c := &atomicSnapshots
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byID = map[string]*atomicSnapshot{}
	c.byPath = map[string]*atomicSnapshot{}
	c.order = nil
	c.total = 0
}
