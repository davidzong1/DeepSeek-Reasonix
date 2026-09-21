package main

import (
	"errors"
	"path/filepath"
	"strings"

	"reasonix/internal/pathidentity"
)

func cleanDesktopPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func sameDesktopPath(a, b string) bool {
	same, err := sameDesktopPathStrict(a, b)
	return err == nil && same
}

func sameDesktopPathStrict(a, b string) (bool, error) {
	a, b = cleanDesktopPath(a), cleanDesktopPath(b)
	if a == "" || b == "" {
		return false, &pathidentity.Error{Kind: pathidentity.ErrorInvalid, Stage: "input", Err: errors.New("desktop path is empty")}
	}
	return pathidentity.Same(a, b, pathidentity.Options{FollowLeaf: true})
}

func projectRootKey(root string) string {
	root = cleanDesktopPath(root)
	if root == "" {
		return ""
	}
	identity, err := pathidentity.Resolve(root, pathidentity.Options{FollowLeaf: true})
	if err != nil {
		return ""
	}
	return identity.Key
}
