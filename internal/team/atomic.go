package team

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"reasonix/internal/fileutil"
)

// writeMu serializes every team write (§3.4): CompareAndSwap compares the
// stored document immediately before its publish, so no other write through
// the chokepoint can interleave in-process. Plain Save shares the lock so a
// non-CAS writer can never slip between a CAS compare and its rename.
var writeMu sync.Mutex

// AtomicWrite is the single chokepoint for .reasonix/team JSON writes (§3.4):
// the project-relative path is validated (no absolute paths, no .. segments,
// nothing escaping the project root), then published via temp + fsync +
// rename at 0600 with parent-dir fsync. A returned error always means the
// destination was not published.
func AtomicWrite(projectRoot, path string, data []byte) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	return atomicWriteLocked(projectRoot, path, data)
}

// atomicWriteLocked is AtomicWrite with writeMu held; callers must hold it.
func atomicWriteLocked(projectRoot, path string, data []byte) error {
	full, err := safePath(projectRoot, path)
	if err != nil {
		return err
	}
	return fileutil.AtomicWriteFileStrict(full, data, 0o600)
}

// safePath resolves a project-relative path under an absolute project root,
// refusing absolute paths, .. segments, and any result outside the root.
func safePath(projectRoot, path string) (string, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("team: path %q must be project-relative", path)
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("team: path %q escapes the project root", path)
	}
	full := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("team: path %q escapes the project root", path)
	}
	return full, nil
}
