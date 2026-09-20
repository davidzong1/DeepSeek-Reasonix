//go:build darwin

package pathidentity

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
	"golang.org/x/text/unicode/norm"
)

const pathconfCaseSensitive = 11

func platformIdentityKey(path string) (string, error) {
	parent, err := closestExistingDirectory(path)
	if err != nil {
		return "", err
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(parent, &stat); err != nil {
		return "", err
	}
	fsType := strings.TrimRight(string(stat.Fstypename[:]), "\x00")
	if fsType == "apfs" || fsType == "hfs" {
		path = norm.NFD.String(path)
	}
	caseSensitive, err := syscall.Pathconf(parent, pathconfCaseSensitive)
	if err != nil {
		return "", err
	}
	if caseSensitive == 0 {
		path = strings.ToLower(path)
	}
	return path, nil
}

func closestExistingDirectory(path string) (string, error) {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err == nil && info.IsDir() {
			return current, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if filepath.Dir(current) == current {
			return "", os.ErrNotExist
		}
	}
}
