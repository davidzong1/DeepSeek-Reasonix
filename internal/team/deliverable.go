package team

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/fileutil"
)

// DeliverableMaxBytes bounds one deliverable body. The retired MCP shared
// context tools split this into a 1 MiB read cap and a 5 MiB write cap; one
// limit replaces both, so a document a member could publish is always one a
// leader can read back.
const DeliverableMaxBytes = 1 << 20

const (
	deliverableCacheDir = "cache"
	deliverableSuffix   = ".md"
	deliverableHashLen  = 12
	deliverableSlugMax  = 64
	deliverableIDMax    = 200
	// deliverableTempMark is fileutil's temp-file prefix, skipped when listing
	// so a listing racing a publish never reports a half-written document.
	deliverableTempMark = ".atomic-"
)

// Sentinel errors. A caller separates "not addressable" from "not there",
// "already different" and "the environment failed" from the error alone; the
// wrapped text names the team and id, never the body.
var (
	ErrDeliverableNoRoot      = errors.New("team: no user state dir to root team deliverables")
	ErrInvalidDeliverableID   = errors.New("team: invalid deliverable id")
	ErrInvalidDeliverableSlug = errors.New("team: invalid deliverable slug")
	ErrInvalidDeliverableName = errors.New("team: invalid team or member name")
	ErrDeliverableNotFound    = errors.New("team: no such deliverable")
	ErrDeliverableExists      = errors.New("team: deliverable exists with different content")
	ErrDeliverableTooLarge    = errors.New("team: deliverable exceeds DeliverableMaxBytes")
	ErrDeliverableSymlink     = errors.New("team: deliverable path carries a symbolic link")
	ErrDeliverableDigest      = errors.New("team: stored deliverable does not match the digest in its id")
)

// Deliverable is one stored document's identity and content fingerprint. ID is
// the stable handle every other call takes and the only locator a caller ever
// holds: the owner, the slug and the content digest are all in it, and the path
// on disk is not.
type Deliverable struct {
	ID      string    `json:"id"`
	Size    int64     `json:"size"`
	SHA256  string    `json:"sha256"`
	ModTime time.Time `json:"mod_time"`
}

// DeliverableStore is one team's immutable document store, rooted at the fixed
// user-global cache <UserStateDir>/team/cache. Each team owns one directory
// under it, which is the isolation boundary: an id reaches only its own team's
// documents, and no caller names a path at all. logf, when non-nil, receives
// one line per refused call — team, id and category, never a body.
type DeliverableStore struct {
	cacheRoot string
	logf      func(format string, args ...any)
}

// NewDeliverableStore roots a store at the fixed cache root. An unresolvable
// user state root is ErrDeliverableNoRoot rather than a relative fallback: a
// fallback would scatter team documents into whatever directory a session
// happened to start in, where no peer could find them.
func NewDeliverableStore(logf func(format string, args ...any)) (*DeliverableStore, error) {
	base := strings.TrimSpace(config.UserStateDir())
	if base == "" {
		return nil, ErrDeliverableNoRoot
	}
	return NewDeliverableStoreAt(filepath.Join(base, "team", deliverableCacheDir), logf), nil
}

// NewDeliverableStoreAt roots a store at an explicit cache directory. Tests and
// hosts that stage their own state root use it; production goes through
// NewDeliverableStore.
func NewDeliverableStoreAt(cacheRoot string, logf func(format string, args ...any)) *DeliverableStore {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &DeliverableStore{cacheRoot: cacheRoot, logf: logf}
}

// Root reports the cache directory this store writes under, for host logging.
// The per-team directories below it are an implementation detail an id already
// carries; nothing outside this component needs the path of a document.
func (s *DeliverableStore) Root() string { return s.cacheRoot }

// DeliverableDir is a team's own directory, relative to the cache root. The
// team name is validated with the same one-segment rule that keeps a session
// key inside its context root, so no team can name a directory outside the
// cache and no two teams can share one.
func DeliverableDir(teamName string) (string, error) {
	if err := validateSessionKey(teamName); err != nil {
		return "", fmt.Errorf("%w: team %q", ErrInvalidDeliverableName, teamName)
	}
	return teamName, nil
}

// DeliverableID validates an id a caller holds: one plain segment naming a
// published document, ending in a content digest. A nested or absolute path, a
// dot segment, a control character or a temporary file is refused, so an id can
// only reach a document this component published.
func DeliverableID(id string) (string, error) {
	if id == "" || len(id) > deliverableIDMax {
		return "", fmt.Errorf("%w: %q is empty or exceeds %d bytes", ErrInvalidDeliverableID, id, deliverableIDMax)
	}
	if err := validateSessionKey(id); err != nil {
		return "", fmt.Errorf("%w: %q is not a single plain segment", ErrInvalidDeliverableID, id)
	}
	if !strings.HasSuffix(id, deliverableSuffix) {
		return "", fmt.Errorf("%w: %q does not end in %s", ErrInvalidDeliverableID, id, deliverableSuffix)
	}
	stem := strings.TrimSuffix(id, deliverableSuffix)
	cut := strings.LastIndex(stem, "-")
	if cut < 0 || len(stem)-cut-1 != deliverableHashLen || !isLowerHex(stem[cut+1:]) {
		return "", fmt.Errorf("%w: %q does not end in a %d-hex content digest", ErrInvalidDeliverableID, id, deliverableHashLen)
	}
	return id, nil
}

// DeliverableSlug validates a publishable name. The allowlist is deliberately
// narrower than a path segment's: a slug names a document for a reader, never a
// location, so uppercase, separators, leading punctuation and control
// characters are all refused rather than folded.
func DeliverableSlug(slug string) (string, error) {
	if len(slug) == 0 || len(slug) > deliverableSlugMax || !deliverableSlugRe.MatchString(slug) {
		return "", fmt.Errorf("%w: %q must match [a-z0-9][a-z0-9._-]{0,%d}", ErrInvalidDeliverableSlug, slug, deliverableSlugMax-1)
	}
	return slug, nil
}

// DeliverableDigest is the content handle an id carries and Read re-checks:
// lowercase hex sha256 of the stored bytes, so the id changes whenever the
// document does.
func DeliverableDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// Publish stores one document and returns its id. The id is derived from the
// owner, the slug and the content digest: publishing the same bytes again is a
// no-op that returns the same id, and new content gets a new id while the
// earlier document stays readable. A body over DeliverableMaxBytes is refused
// before any filesystem work.
func (s *DeliverableStore) Publish(teamName, member, slug string, body []byte) (Deliverable, error) {
	rel, err := s.relPath(teamName, member, slug, body)
	if err != nil {
		return Deliverable{}, err
	}
	id := path.Base(rel)
	if len(body) > DeliverableMaxBytes {
		return Deliverable{}, s.refuse(fmt.Errorf("%w: %s is %d bytes", ErrDeliverableTooLarge, id, len(body)), teamName, id)
	}
	writeMu.Lock()
	err = s.publishLocked(rel, teamName, id, body)
	writeMu.Unlock()
	if err != nil {
		return Deliverable{}, err
	}
	return s.Stat(teamName, id)
}

// publishLocked runs one publish with the team write lock held, so no other
// in-process writer can interleave between the existence check, the guards and
// the rename. A process outside this one can still swap a directory on the path
// inside that window — userspace cannot pin a path across it — so the parent
// directory is resolved again immediately before the write, and a document is
// re-checked against the digest in its id on every read. A swap that lands is
// therefore a detected failure, never a silent write or a wrong read.
func (s *DeliverableStore) publishLocked(rel, teamName, id string, body []byte) error {
	cur, _, rerr := s.readConfined(rel)
	switch {
	case rerr == nil:
		if !bytes.Equal(cur, body) {
			return s.refuse(fmt.Errorf("%w: %s", ErrDeliverableExists, id), teamName, id)
		}
		return nil
	case !errors.Is(rerr, fs.ErrNotExist):
		return s.refuse(rerr, teamName, id)
	}
	if err := s.prepare(rel, teamName, id); err != nil {
		return err
	}
	if err := s.confineParent(rel); err != nil {
		return s.refuse(err, teamName, id)
	}
	if err := atomicWriteLocked(s.cacheRoot, rel, body); err != nil {
		return s.refuse(err, teamName, id)
	}
	return nil
}

// Read returns a document's bytes and re-checks that they still match the
// digest its id carries, so a document edited behind this component's back
// fails loudly instead of reading as if it were the published one.
func (s *DeliverableStore) Read(teamName, id string) ([]byte, error) {
	rel, clean, err := s.byID(teamName, id)
	if err != nil {
		return nil, err
	}
	body, _, rerr := s.readConfined(rel)
	if rerr != nil {
		return nil, s.refuse(missingAs(rerr, clean), teamName, clean)
	}
	if err := checkDigest(clean, body); err != nil {
		return nil, s.refuse(err, teamName, clean)
	}
	return body, nil
}

// Stat returns one document's metadata without reading the body out to the
// caller. The digest is still verified, so metadata and content agree.
func (s *DeliverableStore) Stat(teamName, id string) (Deliverable, error) {
	rel, clean, err := s.byID(teamName, id)
	if err != nil {
		return Deliverable{}, err
	}
	body, mod, rerr := s.readConfined(rel)
	if rerr != nil {
		return Deliverable{}, s.refuse(missingAs(rerr, clean), teamName, clean)
	}
	if err := checkDigest(clean, body); err != nil {
		return Deliverable{}, s.refuse(err, teamName, clean)
	}
	return newDeliverable(clean, body, mod), nil
}

// List returns every document this team owns, ordered by id. A team that never
// published is an empty list, not an error; a symbolic link in the tree is
// skipped rather than followed, so a listing can never leave the team
// directory.
func (s *DeliverableStore) List(teamName string) ([]Deliverable, error) {
	dir, err := DeliverableDir(teamName)
	if err != nil {
		return nil, s.refuse(err, teamName, "")
	}
	root := filepath.Join(s.cacheRoot, dir)
	out := []Deliverable{}
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() || strings.HasPrefix(d.Name(), deliverableTempMark) {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || strings.ContainsRune(rel, filepath.Separator) {
			return nil // nested below the team directory: not where a publish lands
		}
		// A file this component could not have published — a stray hand-placed
		// note, another tool's artifact — is left out rather than failing the
		// team's whole listing, as is one that vanished mid-walk.
		id, ierr := DeliverableID(rel)
		if ierr != nil {
			return nil
		}
		item, serr := s.Stat(teamName, id)
		if serr != nil {
			if errors.Is(serr, ErrDeliverableNotFound) {
				return nil
			}
			return serr // a tampered document or a planted link still fails the listing
		}
		out = append(out, item)
		return nil
	})
	if walkErr != nil {
		return nil, s.refuse(walkErr, teamName, "")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

var deliverableSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func newDeliverable(id string, body []byte, mod time.Time) Deliverable {
	return Deliverable{ID: id, Size: int64(len(body)), SHA256: DeliverableDigest(body), ModTime: mod}
}

// isLowerHex reports whether s is lowercase hexadecimal; the digest in an id is
// compared in that form, so a differently cased id is not the same document.
func isLowerHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return s != ""
}

// checkDigest refuses a document whose bytes no longer hash to the digest its
// id carries: the id is a promise about the content, so a broken promise is a
// refusal, not a read.
func checkDigest(id string, body []byte) error {
	sum := DeliverableDigest(body)
	if !strings.HasPrefix(sum, idDigest(id)) {
		return fmt.Errorf("%w: %s holds sha256 %s", ErrDeliverableDigest, id, sum)
	}
	return nil
}

// idDigest returns the content digest an id carries: the sha256 prefix between
// its last dash and the file suffix.
func idDigest(id string) string {
	stem := strings.TrimSuffix(id, deliverableSuffix)
	return stem[strings.LastIndex(stem, "-")+1:]
}

// missingAs turns a plain absence into the not-found error callers branch on,
// leaving every other failure as it is.
func missingAs(err error, id string) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrDeliverableNotFound, id)
	}
	return err
}

// relPath validates one publish and returns its cache-relative path:
// <team>/<member>-<slug>-<digest prefix>.md. Every component is a validated
// single segment, so the joined result can only name a file inside that team's
// own directory.
func (s *DeliverableStore) relPath(teamName, member, slug string, body []byte) (string, error) {
	dir, err := DeliverableDir(teamName)
	if err != nil {
		return "", s.refuse(err, teamName, "")
	}
	if err := validateSessionKey(member); err != nil {
		return "", s.refuse(fmt.Errorf("%w: member %q", ErrInvalidDeliverableName, member), teamName, "")
	}
	clean, err := DeliverableSlug(slug)
	if err != nil {
		return "", s.refuse(err, teamName, "")
	}
	name := member + "-" + clean + "-" + DeliverableDigest(body)[:deliverableHashLen] + deliverableSuffix
	if _, err := DeliverableID(name); err != nil {
		return "", s.refuse(fmt.Errorf("%w: %q", ErrInvalidDeliverableID, name), teamName, "")
	}
	rel := path.Join(dir, name)
	if _, err := safePath(s.cacheRoot, rel); err != nil {
		return "", s.refuse(err, teamName, name)
	}
	return rel, nil
}

// byID validates one lookup and returns its cache-relative path. The id is
// validated before it is joined, so a traversal attempt is refused by the id
// rule itself rather than by a containment check afterwards.
func (s *DeliverableStore) byID(teamName, id string) (string, string, error) {
	dir, err := DeliverableDir(teamName)
	if err != nil {
		return "", "", s.refuse(err, teamName, id)
	}
	clean, err := DeliverableID(id)
	if err != nil {
		return "", "", s.refuse(err, teamName, id)
	}
	rel := path.Join(dir, clean)
	if _, err := safePath(s.cacheRoot, rel); err != nil {
		return "", "", s.refuse(err, teamName, clean)
	}
	return rel, clean, nil
}

// prepare creates the path's directory chain at 0700 and then refuses a
// symbolic link anywhere below the cache root. It runs with the team write lock
// held, so no other in-process writer can interleave; the resolved-parent check
// that follows narrows the remaining cross-process window.
func (s *DeliverableStore) prepare(rel, teamName, id string) error {
	if err := s.ensureDir(rel); err != nil {
		return s.refuse(err, teamName, id)
	}
	if err := s.guardPath(rel); err != nil {
		return s.refuse(err, teamName, id)
	}
	return nil
}

// confineParent resolves rel's parent directory and refuses a result that is
// not the cache root or below it. Resolving is what sees a directory swapped
// for a symlink after the segment walk; the walk is what names it.
func (s *DeliverableStore) confineParent(rel string) error {
	root, err := filepath.EvalSymlinks(s.cacheRoot)
	if err != nil {
		return fmt.Errorf("%w: resolve deliverable root: %w", ErrDeliverableSymlink, err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Join(s.cacheRoot, filepath.FromSlash(path.Dir(rel))))
	if err != nil {
		return fmt.Errorf("%w: resolve deliverable dir: %w", ErrDeliverableSymlink, err)
	}
	inside, err := filepath.Rel(root, parent)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s resolves outside the cache root", ErrDeliverableSymlink, rel)
	}
	return nil
}

// ensureDir creates every directory on rel's parent chain at 0700 and re-chmods
// each level, the cache root included: MkdirAll leaves an existing directory's
// mode alone, so a cache tree an earlier run (or a caller's own mkdir) left
// group- or world-readable would otherwise stay so. Nothing above the cache
// root is touched — that state root belongs to the rest of the team data.
func (s *DeliverableStore) ensureDir(rel string) error {
	for _, dir := range append([]string{s.cacheRoot}, s.dirChain(rel)...) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("team: create deliverable dir: %w", err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("team: tighten deliverable dir: %w", err)
		}
	}
	return nil
}

// dirChain returns every directory from the cache root down to rel's parent.
func (s *DeliverableStore) dirChain(rel string) []string {
	var out []string
	cur := s.cacheRoot
	for seg := range strings.SplitSeq(path.Dir(rel), "/") {
		cur = filepath.Join(cur, seg)
		out = append(out, cur)
	}
	return out
}

// guardPath refuses a symbolic link at any component below the cache root, the
// cache root itself excluded. A link on the path would redirect the rename a
// publish lands with, so the link itself — not only a link that leaves the
// root — is what gets named.
func (s *DeliverableStore) guardPath(rel string) error {
	cur := s.cacheRoot
	for seg := range strings.SplitSeq(rel, "/") {
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrDeliverableSymlink, rel)
		}
	}
	return nil
}

// readConfined reads one stored document under both guards: no symbolic link
// below the cache root, and the opened descriptor must still be the file the
// path names. The cap is enforced on the descriptor too, so a file that grew
// after the stat is refused rather than buffered.
func (s *DeliverableStore) readConfined(rel string) ([]byte, time.Time, error) {
	if err := s.guardPath(rel); err != nil {
		return nil, time.Time{}, err
	}
	f, err := fileutil.OpenFileBeneath(s.cacheRoot, filepath.FromSlash(rel))
	if err != nil {
		return nil, time.Time{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	if !fi.Mode().IsRegular() {
		return nil, time.Time{}, fmt.Errorf("%w: %s is not a regular file", ErrInvalidDeliverableID, rel)
	}
	if fi.Size() > DeliverableMaxBytes {
		return nil, time.Time{}, fmt.Errorf("%w: %s is %d bytes", ErrDeliverableTooLarge, rel, fi.Size())
	}
	body, err := io.ReadAll(io.LimitReader(f, DeliverableMaxBytes+1))
	if err != nil {
		return nil, time.Time{}, err
	}
	if len(body) > DeliverableMaxBytes {
		return nil, time.Time{}, fmt.Errorf("%w: %s grew past the cap while opening", ErrDeliverableTooLarge, rel)
	}
	return body, fi.ModTime(), nil
}

// refuse logs one line per rejected call and returns the error unchanged, so a
// log line exists exactly when a call failed. The line carries the team, the id
// and the category only: the body never appears, and neither does a path the
// error text may quote — the caller keeps that detail in the returned error.
func (s *DeliverableStore) refuse(err error, teamName, id string) error {
	if err == nil {
		return nil
	}
	s.logf("refused team=%q id=%q kind=%s", teamName, id, deliverableKind(err))
	return err
}

// deliverableKind classifies a refusal for the log line, which stays greppable
// without restating the error text.
func deliverableKind(err error) string {
	switch {
	case errors.Is(err, ErrDeliverableNoRoot):
		return "no-root"
	case errors.Is(err, ErrInvalidDeliverableID), errors.Is(err, ErrInvalidDeliverableName):
		return "invalid"
	case errors.Is(err, ErrInvalidDeliverableSlug):
		return "invalid-slug"
	case errors.Is(err, ErrDeliverableNotFound):
		return "not-found"
	case errors.Is(err, ErrDeliverableExists):
		return "exists"
	case errors.Is(err, ErrDeliverableTooLarge):
		return "too-large"
	case errors.Is(err, ErrDeliverableSymlink):
		return "symlink"
	case errors.Is(err, ErrDeliverableDigest):
		return "digest"
	default:
		return "io"
	}
}
