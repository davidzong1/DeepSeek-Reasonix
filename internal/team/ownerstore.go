package team

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"reasonix/internal/filelock"
	"reasonix/internal/fileutil"
)

// Canonical owner storage: one directory per member, keyed by the stable pair
// (team name, member id). Display name, role, pool and proxy are editable; the
// two ids are not, so an owner directory never has to move when the roster is
// edited. The directory holds the member's transcript and every artifact
// derived from it, so one rename moves the member's whole state.
const (
	ownerMetaFile = ".meta.json" // owner metadata: revision, creation, adopted sources
	ownerMetaLock = ".meta.lock" // cross-process lock serializing metadata updates
	ownerTrashDir = "owners"     // owner trash bucket under the shared .trash root
)

// Owner key and adoption errors. Every one of these is returned rather than
// swallowed: an owner path that cannot be proven safe, or a metadata revision
// that moved under the caller, must reach the caller as a refusal.
var (
	ErrInvalidOwnerKey  = errors.New("team: owner id must be a single safe path segment")
	ErrOwnerReserved    = errors.New("team: owner id collides with a reserved team data entry")
	ErrOwnerSymlink     = errors.New("team: owner path component is a symbolic link")
	ErrOwnerNotFound    = errors.New("team: no such owner directory")
	ErrOwnerCASConflict = errors.New("team: owner metadata changed since the expected revision")
	ErrOwnerSourceTaken = errors.New("team: owner directory already holds a different revision of this source")
)

// ownerReservedNames are the entries the team data root already owns. An owner
// directory named after one of them would alias a registry file, the blackboard
// tree, or the legacy context tree, so those ids are refused instead.
var ownerReservedNames = map[string]bool{
	TeamFile:            true,
	TeamsLegacyFile:     true,
	AgentUsersFile:      true,
	BlackboardDir:       true,
	AuthzDir:            true,
	MemoryFile:          true,
	contextRootDir:      true,
	sessionDir:          true,
	trashRootDir:        true,
	deliverableCacheDir: true,
	boardDBFile:         true,
	adoptedMarkerFile:   true,
}

// ownerDeviceNames are the Windows reserved device names, refused so an owner
// directory stays representable on every platform the store runs on.
var ownerDeviceNames = []string{"CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9"}

// OwnerKey is the stable identity of one member's storage: the team name plus
// the member id. Both are validated before either becomes a path component.
type OwnerKey struct {
	TeamID   string
	MemberID string
}

// String renders the key for diagnostics and for trash entry names.
func (k OwnerKey) String() string { return k.TeamID + "/" + k.MemberID }

// OwnerSource records one legacy location folded into an owner directory. The
// pair (Path, SHA256) is the dedupe key: the same bytes from the same path are
// already adopted, while a path that now holds different bytes is a refusal
// rather than a silent clobber.
type OwnerSource struct {
	Kind      string `json:"kind"` // "session-unit"
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	AdoptedAt string `json:"adopted_at"`
}

// OwnerHistory is one owner's canonical transcript identity together with a
// monotonic generation that every history mutation bumps. Stem names the live
// session the transcript currently executes as — a v3 SessionID after the
// legacy import, else the legacy branch id — so a reader that must not open the
// writer's store can still name what it is looking at. Generation is the
// change signal: it is bumped by append, branch/replace and the identity
// establishing bind, and an owner directory that is gone (clear/step-down) is
// the empty state rather than a generation.
type OwnerHistory struct {
	Stem       string `json:"stem,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

// OwnerMeta is one owner directory's metadata document. Revision is monotonic
// and is the compare-and-swap basis for concurrent writers.
type OwnerMeta struct {
	Document
	Revision  uint64        `json:"revision"`
	CreatedAt string        `json:"created_at"`
	Sources   []OwnerSource `json:"sources,omitempty"`
	History   OwnerHistory  `json:"history,omitempty"`
}

// OwnerFingerprint is one owner's read-only history identity: what a second
// window polls to notice that another runtime changed the history. Present is
// false for an owner directory that is gone — a cleared team, a stepped-down
// leader — which readers render as the empty state, never as a generation.
type OwnerFingerprint struct {
	Present    bool
	Stem       string
	Generation uint64
	// Corrupt marks a present owner whose metadata cannot be read as a
	// document. Without it an unreadable document is indistinguishable from a
	// legitimately unnamed owner, so a reader could not fail closed.
	Corrupt bool
}

// Changed reports whether other describes a different history than f.
func (f OwnerFingerprint) Changed(other OwnerFingerprint) bool {
	return f != other
}

// OwnerPaths is one owner's resolved on-disk locations.
type OwnerPaths struct {
	TeamDir    string // <root>/<teamID>
	Dir        string // <root>/<teamID>/<memberID>
	Meta       string // <root>/<teamID>/<memberID>/.meta.json
	Transcript string // <root>/<teamID>/<memberID>/team-<teamID>-<memberID>.json
}

// OwnerStore is the canonical owner storage over a team data root — a
// project's .reasonix/team or the user-global <state root>/team. Every path it
// touches is confined to that root with os.Root, and every component is
// refused when it is a symbolic link, so no key can redirect a write outside
// the root even when a link stays inside it.
type OwnerStore struct {
	root string
	now  func() time.Time
}

// NewOwnerStore returns an owner store rooted at an explicit team data dir.
func NewOwnerStore(dir string) (*OwnerStore, error) {
	abs, err := filepath.Abs(strings.TrimSpace(dir))
	if err != nil {
		return nil, fmt.Errorf("team: resolve owner store root: %w", err)
	}
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("team: owner store root must not be empty")
	}
	return &OwnerStore{root: abs, now: func() time.Time { return time.Now().UTC() }}, nil
}

// Root returns the absolute team data root this store is confined to.
func (s *OwnerStore) Root() string { return s.root }

// Paths resolves one owner's locations without touching the filesystem.
func (s *OwnerStore) Paths(key OwnerKey) (OwnerPaths, error) {
	rel, err := s.rel(key)
	if err != nil {
		return OwnerPaths{}, err
	}
	dir := filepath.Join(s.root, rel)
	file, err := MemberSessionFile(key.TeamID, key.MemberID)
	if err != nil {
		return OwnerPaths{}, err
	}
	return OwnerPaths{
		TeamDir:    filepath.Join(s.root, key.TeamID),
		Dir:        dir,
		Meta:       filepath.Join(dir, ownerMetaFile),
		Transcript: filepath.Join(dir, file),
	}, nil
}

// Init creates the owner directory at 0700 with its metadata document, and is
// idempotent: an existing owner keeps its revision and adopted sources. A
// missing directory is a member that has never been stored, never corruption.
func (s *OwnerStore) Init(ctx context.Context, key OwnerKey) (OwnerPaths, OwnerMeta, error) {
	paths, err := s.Paths(key)
	if err != nil {
		return OwnerPaths{}, OwnerMeta{}, err
	}
	rel, err := s.rel(key)
	if err != nil {
		return OwnerPaths{}, OwnerMeta{}, err
	}
	if err := s.guardComponents(rel); err != nil {
		return OwnerPaths{}, OwnerMeta{}, err
	}
	root, err := s.openRoot(true)
	if err != nil {
		return OwnerPaths{}, OwnerMeta{}, err
	}
	defer root.Close()
	if err := root.MkdirAll(rel, 0o700); err != nil {
		return OwnerPaths{}, OwnerMeta{}, fmt.Errorf("team: create owner dir: %w", err)
	}
	// MkdirAll leaves an existing directory's mode alone, so a tree an earlier
	// run left group- or world-readable is tightened explicitly.
	for _, dir := range []string{key.TeamID, rel} {
		if err := root.Chmod(dir, 0o700); err != nil {
			return OwnerPaths{}, OwnerMeta{}, fmt.Errorf("team: tighten owner dir: %w", err)
		}
	}
	meta, err := s.ensureMeta(ctx, key, paths)
	if err != nil {
		return OwnerPaths{}, OwnerMeta{}, err
	}
	return paths, meta, nil
}

// Meta returns the owner's metadata. A missing owner directory is
// ErrOwnerNotFound rather than a zero document, so a caller never mistakes an
// absent member for a freshly created one.
func (s *OwnerStore) Meta(key OwnerKey) (OwnerMeta, error) {
	paths, err := s.Paths(key)
	if err != nil {
		return OwnerMeta{}, err
	}
	meta, err := s.readMeta(paths)
	if errors.Is(err, fs.ErrNotExist) {
		return OwnerMeta{}, fmt.Errorf("%w: %s", ErrOwnerNotFound, key)
	}
	return meta, err
}

// Fingerprint returns the owner's read-only history identity without taking
// any lock: a missing directory is the empty state, and an unreadable or
// corrupt metadata document is reported as present and corrupt so a reader can
// fail closed instead of adopting an identity it cannot trust. It is the
// polling surface a second window uses, so it must never block behind the
// owner's metadata lock.
//
// The error result is reserved for a key or root the store itself cannot
// resolve — states where the store cannot even name the owner directory. A
// document that cannot be read is a fingerprint, not an error, because the
// reader has to tell the two apart to decide whether to retry or to refuse.
func (s *OwnerStore) Fingerprint(key OwnerKey) (OwnerFingerprint, error) {
	paths, err := s.Paths(key)
	if err != nil {
		return OwnerFingerprint{}, err
	}
	meta, err := s.readMeta(paths)
	if errors.Is(err, fs.ErrNotExist) {
		return OwnerFingerprint{}, nil
	}
	if err != nil {
		// The directory exists but its document cannot be read. Reporting it as
		// present-but-unnamed would alias the legitimate unnamed owner, so the
		// caller could never tell "nothing published yet" from "untrusted".
		return OwnerFingerprint{Present: true, Corrupt: true}, nil
	}
	return OwnerFingerprint{Present: true, Stem: meta.History.Stem, Generation: meta.History.Generation}, nil
}

// BumpHistory records the owner's canonical transcript identity and advances
// its generation. stem is the live session the transcript now executes as (a
// v3 SessionID, else the legacy branch id); bump is false for a call that only
// establishes the identity, so binding a member does not look like a change.
// The write goes through UpdateMeta, so it takes the owner's metadata lock and
// publishes a new revision — the CAS basis a concurrent writer sees. An absent
// owner directory is created first: a member with no history yet still needs an
// identity for a second window to compare against.
//
// An empty stem never erases a name already published. The legacy axis has no
// live transcript to name the instant a rotation returns, so a caller
// publishing right after a clear reports "" — and clearing the stem there would
// leave the owner unnamed for every peer until the next save. The stale name
// beats none; the generation still advances, so the change is not lost. A first
// identity with an empty stem stays empty rather than inventing a name.
func (s *OwnerStore) BumpHistory(ctx context.Context, key OwnerKey, stem string, bump bool) error {
	if _, _, err := s.Init(ctx, key); err != nil {
		return err
	}
	_, err := s.UpdateMeta(ctx, key, 0, func(meta *OwnerMeta) error {
		next := OwnerHistory{Stem: meta.History.Stem, Generation: meta.History.Generation, UpdatedAt: s.stamp()}
		if named := strings.TrimSpace(stem); named != "" {
			next.Stem = named
		}
		if bump {
			next.Generation++
		}
		meta.History = next
		return nil
	})
	return err
}

// UpdateMeta applies mutate to the owner metadata under the owner's
// cross-process lock and publishes the result with a revision one higher than
// the stored one. expected pins the revision the caller read: zero accepts
// whatever is stored, a non-zero mismatch returns ErrOwnerCASConflict instead
// of clobbering a concurrent writer. A mutate error publishes nothing.
func (s *OwnerStore) UpdateMeta(ctx context.Context, key OwnerKey, expected uint64, mutate func(*OwnerMeta) error) (OwnerMeta, error) {
	paths, err := s.Paths(key)
	if err != nil {
		return OwnerMeta{}, err
	}
	release, err := s.lock(ctx, paths)
	if err != nil {
		return OwnerMeta{}, err
	}
	defer release()
	current, err := s.readMeta(paths)
	if err != nil {
		return OwnerMeta{}, err
	}
	if expected != 0 && current.Revision != expected {
		return OwnerMeta{}, fmt.Errorf("%w: %s at revision %d, expected %d", ErrOwnerCASConflict, key, current.Revision, expected)
	}
	if mutate != nil {
		if err := mutate(&current); err != nil {
			return OwnerMeta{}, err
		}
	}
	current.Document = Document{SchemaVersion: SchemaVersion}
	current.Revision++
	if err := s.writeMeta(paths, current); err != nil {
		return OwnerMeta{}, err
	}
	return current, nil
}

// Delete stages the whole owner directory into the shared trash bucket and
// returns the staged path (relative to the store root). Only this member's
// directory moves: a sibling member, and every other team, is untouched. An
// absent owner is nothing to trash — ("", nil) — so a repeated delete after a
// crash is not an error.
func (s *OwnerStore) Delete(key OwnerKey) (string, error) {
	rel, err := s.rel(key)
	if err != nil {
		return "", err
	}
	if err := s.guardComponents(rel); err != nil {
		return "", err
	}
	root, err := s.openRoot(false)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	defer root.Close()
	info, err := root.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: %s", ErrOwnerSymlink, rel)
	}
	// The bucket is a directory under .trash, so the legacy team-context sweep
	// (which prefix-matches "<team>-" entries directly under .trash) can never
	// reach an owner trash entry.
	trash := filepath.Join(trashRootDir, ownerTrashDir, ownerTrashName(key, s.stamp()))
	if err := root.MkdirAll(filepath.Join(trashRootDir, ownerTrashDir), 0o700); err != nil {
		return "", fmt.Errorf("team: create owner trash bucket: %w", err)
	}
	if err := root.Rename(rel, trash); err != nil {
		return "", fmt.Errorf("team: stage owner dir: %w", err)
	}
	// The team directory is left behind empty; removing it is best-effort
	// because a concurrent member creation may have already refilled it.
	_ = root.Remove(key.TeamID)
	return filepath.ToSlash(trash), nil
}

// MemberIDs lists the owner directories stored under one team, sorted. An
// absent team directory is no members — a team that has never stored a history —
// never an error. Callers that must clear a whole team's histories enumerate
// from here rather than from the registry: a slot removed from team.json still
// owns a directory.
func (s *OwnerStore) MemberIDs(teamID string) ([]string, error) {
	if err := validateOwnerID(teamID); err != nil {
		return nil, err
	}
	root, err := s.openRoot(false)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer root.Close()
	if err := s.guardComponents(teamID); err != nil {
		return nil, err
	}
	entries, err := root.Open(teamID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer entries.Close()
	names, err := entries.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(names))
	for _, e := range names {
		if !e.IsDir() || e.Type()&os.ModeSymlink != 0 {
			continue // a file or a link is not an owner directory
		}
		ids = append(ids, e.Name())
	}
	slices.Sort(ids)
	return ids, nil
}

// SweepOwnerTrash deletes trash entries for one owner that an interrupted
// delete left behind. Matching uses the length-prefixed entry name, so clearing
// member "a-b" never removes the staged trash of member "a-b-c" or of a sibling
// team whose name shares a prefix — the defect the legacy team-context sweep
// still has with its plain "<team>-" prefix.
func (s *OwnerStore) SweepOwnerTrash(key OwnerKey) error {
	bucket := filepath.Join(s.root, trashRootDir, ownerTrashDir)
	entries, err := os.ReadDir(bucket)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	prefix := ownerTrashPrefix(key)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(bucket, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// ownerTrashName renders one owner's trash entry. The id lengths are encoded
// in front of the ids so the name is unambiguous: a plain "<team>-<member>-"
// spelling cannot be prefix-matched safely, because team "a" member "b-c" and
// team "a-b" member "c" would produce the same prefix.
func ownerTrashName(key OwnerKey, stamp string) string {
	return ownerTrashPrefix(key) + stamp
}

// ownerTrashPrefix is the exact, collision-free prefix of every trash entry
// belonging to one owner.
func ownerTrashPrefix(key OwnerKey) string {
	return fmt.Sprintf("%d-%s-%d-%s-", len(key.TeamID), key.TeamID, len(key.MemberID), key.MemberID)
}

// ensureMeta publishes the owner metadata only when it is absent, so a second
// Init never resets a revision or forgets an adopted source. The create is
// non-overwriting: a concurrent creator wins and its document is read back.
func (s *OwnerStore) ensureMeta(ctx context.Context, key OwnerKey, paths OwnerPaths) (OwnerMeta, error) {
	release, err := s.lock(ctx, paths)
	if err != nil {
		return OwnerMeta{}, err
	}
	defer release()
	if meta, err := s.readMeta(paths); err == nil {
		return meta, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return OwnerMeta{}, err
	}
	meta := OwnerMeta{
		Document:  Document{SchemaVersion: SchemaVersion},
		Revision:  1,
		CreatedAt: s.stamp(),
	}
	if err := s.writeMeta(paths, meta); err != nil {
		return OwnerMeta{}, err
	}
	return meta, nil
}

// readMeta reads and validates one owner's metadata document. A file that
// exists but does not decode is corrupt and refused loudly, never read as zero.
func (s *OwnerStore) readMeta(paths OwnerPaths) (OwnerMeta, error) {
	rel, err := filepath.Rel(s.root, paths.Meta)
	if err != nil {
		return OwnerMeta{}, err
	}
	root, err := s.openRoot(false)
	if err != nil {
		return OwnerMeta{}, err
	}
	defer root.Close()
	data, err := root.ReadFile(rel)
	if err != nil {
		return OwnerMeta{}, err
	}
	if err := checkSchemaVersion(rel, data); err != nil {
		return OwnerMeta{}, err
	}
	var meta OwnerMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return OwnerMeta{}, &CorruptFileError{Path: rel, Err: err}
	}
	return meta, nil
}

// writeMeta publishes the metadata document atomically at 0600, after the
// schema gate, so a reader sees either the previous revision or the complete
// new one and never a torn document.
func (s *OwnerStore) writeMeta(paths OwnerPaths, meta OwnerMeta) error {
	meta.Document = Document{SchemaVersion: SchemaVersion}
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(s.root, paths.Meta)
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

// lock takes the owner's metadata lock, serializing read-modify-write across
// goroutines and processes. The lock file lives inside the owner directory, so
// trashing the directory takes the lock with it.
func (s *OwnerStore) lock(ctx context.Context, paths OwnerPaths) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	release, err := filelock.Acquire(ctx, filepath.Join(paths.Dir, ownerMetaLock))
	if err != nil {
		return nil, fmt.Errorf("team: lock owner metadata: %w", err)
	}
	return release, nil
}

// openRoot opens the store root as a confined handle, optionally creating the
// root itself so a first entry can land in a team data dir that does not exist
// yet.
func (s *OwnerStore) openRoot(create bool) (*os.Root, error) {
	if create {
		if err := os.MkdirAll(s.root, 0o700); err != nil {
			return nil, fmt.Errorf("team: create owner store root: %w", err)
		}
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, fmt.Errorf("team: open owner store root: %w", err)
	}
	return root, nil
}

// guardComponents refuses a symbolic link at any component of rel below the
// store root, the root itself excluded. os.Root already blocks traversal out
// of the root, but it follows a link that stays inside it, so the link itself
// is what gets named — not only a link whose target escapes.
func (s *OwnerStore) guardComponents(rel string) error {
	root, err := s.openRoot(false)
	if err != nil {
		// An absent root has no components to guard, and the caller that needs
		// it creates it next; a symlink cannot exist in a chain just created.
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer root.Close()
	cur := ""
	for seg := range strings.SplitSeq(rel, string(filepath.Separator)) {
		if seg == "" || seg == "." {
			continue
		}
		cur = filepath.Join(cur, seg)
		info, err := root.Lstat(cur)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrOwnerSymlink, rel)
		}
	}
	return nil
}

// rel returns the owner's path relative to the store root, after validating
// both key components.
func (s *OwnerStore) rel(key OwnerKey) (string, error) {
	if err := validateOwnerID(key.TeamID); err != nil {
		return "", err
	}
	if err := validateOwnerID(key.MemberID); err != nil {
		return "", err
	}
	return filepath.Join(key.TeamID, key.MemberID), nil
}

// stamp renders the store's clock as a filesystem-safe UTC timestamp for
// trash entry names.
func (s *OwnerStore) stamp() string {
	return s.now().Format("20060102T150405.000000000Z")
}

// validateOwnerID accepts an id that is safe as a single path component of the
// team data root: non-empty, no separators or dot segments, no control
// characters, no leading or trailing dot, no portable-unsafe characters, no
// reserved device name, and not one of the entries the root already owns.
func validateOwnerID(id string) error {
	if err := validateSessionKey(id); err != nil {
		return fmt.Errorf("%w: %q", ErrInvalidOwnerKey, id)
	}
	if strings.HasPrefix(id, ".") || strings.HasSuffix(id, ".") || strings.ContainsAny(id, `<>:"|?*`) {
		return fmt.Errorf("%w: %q", ErrInvalidOwnerKey, id)
	}
	if ownerReservedNames[id] {
		return fmt.Errorf("%w: %q", ErrOwnerReserved, id)
	}
	base := strings.ToUpper(strings.SplitN(id, ".", 2)[0])
	if slices.Contains(ownerDeviceNames, base) {
		return fmt.Errorf("%w: %q", ErrInvalidOwnerKey, id)
	}
	return nil
}
