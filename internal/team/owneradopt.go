package team

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"reasonix/internal/store"
)

// Adopting a legacy member history into canonical owner storage. The unit is
// the whole session artifact set, not the transcript alone: every sidecar is
// derived from the transcript path, so a copy that takes only the transcript
// silently drops the member's checkpoints and rewind history.
//
// The canonical transcript keeps its basename (team-<team>-<member>.json), so
// the derived names — <stem>.ckpt, <stem>.jobs, <stem>.inbox, <stem>.goal-state.json,
// <path>.meta, <path>.lease.json — map one to one onto the new directory and
// the stem ABI survives the move. Adoption copies; the source is never moved
// or deleted, so an interrupted adoption retries against intact data.
var (
	ErrOwnerUnitMismatch = errors.New("team: session unit does not belong to this owner key")
	ErrOwnerAdoptBusy    = errors.New("team: owner directory already holds a different history")
)

// OwnerUnit is one member's complete session artifact set, derived from a
// transcript path. Files and Dirs are the durable artifacts adoption copies;
// Ephemeral holds lock and lease files that are deliberately not copied —
// resurrecting a stale lease would hand a new process a claim it never took.
type OwnerUnit struct {
	Transcript string   // the session transcript (team-<team>-<member>.json)
	Files      []string // regular-file sidecars derived from the transcript
	Dirs       []string // directory artifacts: checkpoints, jobs, inbox
	Ephemeral  []string // lock and lease files, never adopted
}

// OwnerUnitFor enumerates the complete artifact set of one member transcript.
// An empty path has no unit. Callers must not enumerate by hand: a hand-built
// list is how a sidecar gets forgotten.
func OwnerUnitFor(transcript string) OwnerUnit {
	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		return OwnerUnit{}
	}
	unit := OwnerUnit{Transcript: transcript}
	unit.Files = append(unit.Files, store.SessionSidecarFiles(transcript)...)
	unit.Files = append(unit.Files, store.SessionCleanupPending(transcript))
	unit.Dirs = append(unit.Dirs,
		store.SessionCheckpointDir(transcript),
		store.SessionJobsDir(transcript),
		store.SessionInboxDir(transcript),
	)
	unit.Ephemeral = append(unit.Ephemeral,
		store.SessionLockFile(transcript),
		store.SessionLeaseLock(transcript),
		store.SessionLeaseInfo(transcript),
	)
	return unit
}

// OwnerAdoptOptions controls one adoption. It is deliberately not the
// registry-level AdoptOptions: adoption here copies session artifacts, it does
// not merge registries.
type OwnerAdoptOptions struct {
	// Exclusive marks a v3-exclusive controller. Goal and checkpoint sidecars
	// are v3 events there and rebindCheckpoints refuses to read legacy ones, so
	// copying them would create the second restore source that refusal prevents.
	Exclusive bool
}

// OwnerAdoptReport summarises one adoption into owner storage. Skipped names
// why nothing moved.
type OwnerAdoptReport struct {
	Source           string
	Target           string
	Skipped          string
	FilesCopied      int
	DirsCopied       int
	FilesSkipped     int // already present and identical (resume, or exclusive carve-out)
	EphemeralSkipped int
}

// AdoptSessionUnit folds a legacy member session unit into the canonical owner
// directory, exactly once per (source path, transcript digest). Adoption is
// idempotent and resumable: the transcript is copied last and the source
// record — the completion marker — is written after every artifact landed, so
// an interrupted adoption re-copies on the next attempt rather than leaving a
// transcript with half its sidecars.
//
// A destination transcript that already holds different bytes is a refusal
// (ErrOwnerAdoptBusy), never a clobber: the canonical directory is the
// authority once written. A source whose basename is not this owner's session
// file name is refused too, so a foreign session can never be adopted as
// someone else's history.
func (s *OwnerStore) AdoptSessionUnit(ctx context.Context, key OwnerKey, unit OwnerUnit, opt OwnerAdoptOptions) (OwnerAdoptReport, error) {
	rep := OwnerAdoptReport{Source: unit.Transcript}
	if strings.TrimSpace(unit.Transcript) == "" {
		rep.Skipped = "empty session unit"
		return rep, nil
	}
	want, err := MemberSessionFile(key.TeamID, key.MemberID)
	if err != nil {
		return rep, err
	}
	if filepath.Base(unit.Transcript) != want {
		return rep, fmt.Errorf("%w: %s is not %s", ErrOwnerUnitMismatch, filepath.Base(unit.Transcript), want)
	}
	paths, _, err := s.Init(ctx, key)
	if err != nil {
		return rep, err
	}
	rep.Target = paths.Dir
	digest, err := digestRegularFile(unit.Transcript)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			rep.Skipped = "source transcript absent"
			return rep, nil
		}
		return rep, err
	}
	release, err := s.lock(ctx, paths)
	if err != nil {
		return rep, err
	}
	defer release()
	if done, err := s.alreadyAdopted(paths, unit.Transcript, digest); err != nil || done {
		if done {
			rep.Skipped = "already adopted"
		}
		return rep, err
	}
	if err := s.guardAdoptTarget(paths, digest); err != nil {
		return rep, err
	}
	root, err := s.openRoot(false)
	if err != nil {
		return rep, err
	}
	defer root.Close()
	relDir, err := filepath.Rel(s.root, paths.Dir)
	if err != nil {
		return rep, err
	}
	for _, dir := range unit.Dirs {
		if opt.Exclusive && isRestoreSourceArtifact(unit.Transcript, dir) {
			rep.FilesSkipped++
			continue
		}
		copied, err := copyDirInto(root, filepath.Join(relDir, filepath.Base(dir)), dir)
		if err != nil {
			return rep, err
		}
		if copied {
			rep.DirsCopied++
		}
	}
	for _, file := range unit.Files {
		if opt.Exclusive && isRestoreSourceArtifact(unit.Transcript, file) {
			rep.FilesSkipped++
			continue
		}
		copied, err := copyFileInto(root, filepath.Join(relDir, filepath.Base(file)), file)
		if err != nil {
			return rep, err
		}
		if copied {
			rep.FilesCopied++
		} else {
			rep.FilesSkipped++
		}
	}
	rep.EphemeralSkipped = len(unit.Ephemeral)
	// The transcript lands last: until it is there the owner has no history to
	// read, so a crash mid-adoption cannot expose a transcript whose sidecars
	// were never copied.
	if copied, err := copyFileInto(root, filepath.Join(relDir, filepath.Base(unit.Transcript)), unit.Transcript); err != nil {
		return rep, err
	} else if copied {
		rep.FilesCopied++
	}
	if err := s.recordSource(paths, OwnerSource{
		Kind: "session-unit", Path: unit.Transcript, SHA256: digest, AdoptedAt: s.stamp(),
	}); err != nil {
		return rep, err
	}
	return rep, nil
}

// AdoptMemberSessionHistory folds a member's legacy session unit into canonical
// owner storage, probing candidates in the order the caller would bind them. It
// is the migration half of the owner wiring: the caller probes the owner
// directory first and only then the historical session directories, so a member
// whose history still lives in one of those is adopted once, and every later
// launch reads the canonical copy.
//
// A canonical transcript already in place wins outright — no candidate is even
// inspected, so a stale copy in a session directory can never overwrite the
// adopted history. With nothing found anywhere the report is a skip: the member
// simply has no history yet, which is not an error.
func (s *OwnerStore) AdoptMemberSessionHistory(ctx context.Context, key OwnerKey, candidates []string, opt OwnerAdoptOptions) (OwnerAdoptReport, error) {
	paths, err := s.Paths(key)
	if err != nil {
		return OwnerAdoptReport{}, err
	}
	if _, err := digestRegularFile(paths.Transcript); err == nil {
		return OwnerAdoptReport{Target: paths.Dir, Skipped: "canonical transcript present"}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return OwnerAdoptReport{}, err
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" || sameFile(paths.Transcript, candidate) {
			continue
		}
		if _, err := os.Lstat(candidate); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return OwnerAdoptReport{}, err
		}
		// The first candidate holding the file is the one the caller would have
		// bound, so it is the one adopted — adopting a later, staler copy would
		// hand the member a history the probe order just refused.
		return s.AdoptSessionUnit(ctx, key, OwnerUnitFor(candidate), opt)
	}
	return OwnerAdoptReport{Target: paths.Dir, Skipped: "no legacy session found"}, nil
}

// sameFile reports whether two paths name the same location, so a caller that
// lists the canonical transcript among its candidates does not re-adopt it onto
// itself.
func sameFile(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return absA == absB
}

// LegacyContextDir returns the member's legacy context directory under this
// store's own team data root — <root>/context/<team>/<member>, the pre-D5
// messages/state/cursor tree. It is a path, never a creation: an absent
// directory is a member that never had one, which is exactly what
// AdoptLegacyContext treats as a no-op.
func (s *OwnerStore) LegacyContextDir(key OwnerKey) (string, error) {
	if _, err := s.rel(key); err != nil {
		return "", err
	}
	return filepath.Join(s.root, contextRootDir, key.TeamID, key.MemberID), nil
}

// AdoptLegacyContext folds a member's legacy context directory
// (context/<team>/<member>/{messages.jsonl,state.json,cursor.json}) into the
// owner directory under legacy/. The bytes are preserved verbatim: the legacy
// tree has no transcript and no session identity, so interpreting it would
// invent a history format. The source is never modified or deleted.
func (s *OwnerStore) AdoptLegacyContext(ctx context.Context, key OwnerKey, contextDir string) (OwnerAdoptReport, error) {
	rep := OwnerAdoptReport{Source: contextDir}
	if strings.TrimSpace(contextDir) == "" {
		rep.Skipped = "empty context dir"
		return rep, nil
	}
	entries, err := os.ReadDir(contextDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			rep.Skipped = "source context absent"
			return rep, nil
		}
		return rep, err
	}
	paths, _, err := s.Init(ctx, key)
	if err != nil {
		return rep, err
	}
	rep.Target = paths.Dir
	release, err := s.lock(ctx, paths)
	if err != nil {
		return rep, err
	}
	defer release()
	root, err := s.openRoot(false)
	if err != nil {
		return rep, err
	}
	defer root.Close()
	relDir, err := filepath.Rel(s.root, paths.Dir)
	if err != nil {
		return rep, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue // the legacy tree is flat: three files, nothing nested
		}
		src := filepath.Join(contextDir, e.Name())
		digest, err := digestRegularFile(src)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // a symlink or a vanished entry: not adoptable
			}
			return rep, err
		}
		if done, err := s.alreadyAdopted(paths, src, digest); err != nil || done {
			if done {
				rep.FilesSkipped++
				continue
			}
			return rep, err
		}
		dst := filepath.Join(relDir, "legacy", e.Name())
		copied, err := copyFileInto(root, dst, src)
		if err != nil {
			return rep, err
		}
		if !copied {
			rep.FilesSkipped++
		} else {
			rep.FilesCopied++
		}
		if err := s.recordSource(paths, OwnerSource{
			Kind: "legacy-context", Path: src, SHA256: digest, AdoptedAt: s.stamp(),
		}); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// alreadyAdopted reports whether this exact (path, digest) source is recorded,
// and refuses a path already recorded under a different digest: the same
// legacy location holding new bytes is a different history, and adopting it
// over an existing one is a clobber, not a resume.
func (s *OwnerStore) alreadyAdopted(paths OwnerPaths, source, digest string) (bool, error) {
	meta, err := s.readMeta(paths)
	if err != nil {
		return false, err
	}
	for _, src := range meta.Sources {
		if src.Path != source {
			continue
		}
		if src.SHA256 == digest {
			return true, nil
		}
		return false, fmt.Errorf("%w: %s", ErrOwnerSourceTaken, source)
	}
	return false, nil
}

// guardAdoptTarget refuses to adopt over a canonical transcript that already
// holds different bytes, and refuses a symlinked destination path so adoption
// cannot be redirected out of the owner directory.
func (s *OwnerStore) guardAdoptTarget(paths OwnerPaths, digest string) error {
	rel, err := filepath.Rel(s.root, paths.Transcript)
	if err != nil {
		return err
	}
	if err := s.guardComponents(filepath.Dir(rel)); err != nil {
		return err
	}
	existing, err := digestRegularFile(paths.Transcript)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if existing != digest {
		return fmt.Errorf("%w: %s", ErrOwnerAdoptBusy, paths.Transcript)
	}
	return nil
}

// recordSource appends one adopted source to the owner metadata. The revision
// advances, so a concurrent reader can tell the metadata moved.
func (s *OwnerStore) recordSource(paths OwnerPaths, source OwnerSource) error {
	meta, err := s.readMeta(paths)
	if err != nil {
		return err
	}
	for _, src := range meta.Sources {
		if src.Path == source.Path && src.SHA256 == source.SHA256 {
			return nil // idempotent: the same source is recorded once
		}
	}
	meta.Sources = append(meta.Sources, source)
	meta.Revision++
	return s.writeMeta(paths, meta)
}

// isRestoreSourceArtifact reports whether path is a legacy goal or checkpoint
// artifact of transcript — the two the v3-exclusive reader refuses to treat as
// a restore source.
func isRestoreSourceArtifact(transcript, path string) bool {
	return path == store.SessionGoalState(transcript) || path == store.SessionCheckpointDir(transcript)
}

// digestRegularFile returns the hex SHA-256 of a regular file, refusing a
// symbolic link or any other non-regular entry.
func digestRegularFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s is not a regular file", ErrOwnerSymlink, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyFileInto copies src to the root-relative destination, returning whether
// it wrote. A destination already holding identical bytes is left alone, so a
// retried adoption resumes instead of rewriting. The write is atomic: readers
// see the previous file or the complete new one, never a partial copy.
func copyFileInto(root *os.Root, rel, src string) (bool, error) {
	info, err := os.Lstat(src)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil // an optional sidecar that was never written
		}
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("%w: %s is not a regular file", ErrOwnerSymlink, src)
	}
	same, err := sameContentAt(root, rel, src, info.Size())
	if err != nil {
		return false, err
	}
	if same {
		return false, nil
	}
	if err := root.MkdirAll(filepath.Dir(rel), 0o700); err != nil {
		return false, fmt.Errorf("team: create owner dir for copy: %w", err)
	}
	in, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer in.Close()
	tmp := rel + ".adopt.tmp"
	out, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false, fmt.Errorf("team: create adopt temp: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = root.Remove(tmp)
		return false, err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = root.Remove(tmp)
		return false, err
	}
	if err := out.Close(); err != nil {
		_ = root.Remove(tmp)
		return false, err
	}
	if err := root.Rename(tmp, rel); err != nil {
		_ = root.Remove(tmp)
		return false, fmt.Errorf("team: publish adopted file: %w", err)
	}
	return true, nil
}

// copyDirInto copies a whole directory artifact (checkpoints, jobs, inbox)
// into the root-relative destination. Every level is created at 0700 and a
// symbolic link anywhere in the source refuses the copy.
func copyDirInto(root *os.Root, rel, srcDir string) (bool, error) {
	info, err := os.Lstat(srcDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%w: %s", ErrOwnerSymlink, srcDir)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("%w: %s is not a directory", ErrOwnerSymlink, srcDir)
	}
	if err := root.MkdirAll(rel, 0o700); err != nil {
		return false, fmt.Errorf("team: create owner dir artifact: %w", err)
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return false, err
	}
	wrote := false
	for _, e := range entries {
		childSrc := filepath.Join(srcDir, e.Name())
		childRel := filepath.Join(rel, e.Name())
		if e.IsDir() {
			copied, err := copyDirInto(root, childRel, childSrc)
			if err != nil {
				return wrote, err
			}
			wrote = wrote || copied
			continue
		}
		copied, err := copyFileInto(root, childRel, childSrc)
		if err != nil {
			return wrote, err
		}
		wrote = wrote || copied
	}
	return wrote, nil
}

// sameContentAt reports whether the root-relative destination already holds
// exactly the bytes of src. Size is compared first so an unchanged artifact
// costs one stat rather than a full read.
func sameContentAt(root *os.Root, rel, src string, size int64) (bool, error) {
	info, err := root.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%w: %s", ErrOwnerSymlink, rel)
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return false, nil
	}
	want, err := digestRegularFile(src)
	if err != nil {
		return false, err
	}
	got, err := digestAt(root, rel)
	if err != nil {
		return false, err
	}
	return want == got, nil
}

// digestAt hashes one root-relative regular file.
func digestAt(root *os.Root, rel string) (string, error) {
	f, err := root.Open(rel)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
