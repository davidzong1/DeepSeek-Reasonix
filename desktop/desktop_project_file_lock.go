package main

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"reasonix/internal/filelock"
)

const desktopProjectsFileLockTimeout = 2 * time.Second

func acquireDesktopProjectsFileLock() (func(), error) {
	dir := desktopConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), desktopProjectsFileLockTimeout)
	defer cancel()
	return filelock.Acquire(ctx, filepath.Join(dir, desktopProjectsFile)+".lock")
}

// updateProjectsFileCrossProcessLocked requires desktopProjectsFileMu and the
// desktop-projects cross-process file lock.
func updateProjectsFileCrossProcessLocked(mutator func(*desktopProjectFile) (bool, error)) error {
	f := loadProjectsFile()
	changed, err := mutator(&f)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return saveProjectsFile(f)
}
