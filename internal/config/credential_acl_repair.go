package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"reasonix/internal/sandbox"
)

// readCredentialFile keeps successful reads independent of sandbox ACL locks.
// Repair is attempted only for a denied read of the global credential store.
func readCredentialFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil || !os.IsPermission(err) || runtimeGOOS != "windows" {
		return data, err
	}
	credentials := strings.TrimSpace(UserCredentialsPath())
	if path == "" || credentials == "" || !samePath(path, credentials) {
		return nil, err
	}
	// RuntimeForbidReadRoots resolved links before older Windows builds recorded
	// the deny marker. Resolve only on the denied path so the ordinary read stays
	// lock-free and avoids an extra filesystem round trip.
	repairPath := path
	if real, evalErr := filepath.EvalSymlinks(path); evalErr == nil {
		repairPath = real
	}
	if repairErr := sandbox.RepairLegacyCredentialDeny(repairPath); repairErr != nil {
		slog.Warn("config: legacy credential ACL repair failed", "path", path, "err", repairErr)
		return nil, repairErr
	}
	return os.ReadFile(path)
}
