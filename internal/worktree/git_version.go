package worktree

import (
	"context"
	"strconv"
	"strings"
)

// Git capability probes for the merge-back path: `merge-tree --write-tree`
// cannot be probed without two trees, so the version is the honest signal, and
// an unreadable version is reported as unknown rather than as new enough.

// gitVersion reads the host's Git version as a bare dotted number ("2.34.1").
// ok is false when Git is missing or reports something unparseable.
func gitVersion(ctx context.Context, root string) (string, bool) {
	out, _, err := runGit(ctx, root, "version")
	if err != nil {
		return "", false
	}
	return parseGitVersion(out)
}

// parseGitVersion extracts the numeric version from `git version` output. It
// tolerates the vendor suffixes distributions add (for example
// "2.39.2 (Apple Git-145)" or "2.34.1.windows.1") and reports ok=false for
// anything without a leading major.minor.
func parseGitVersion(out string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) < 3 || fields[0] != "git" || fields[1] != "version" {
		return "", false
	}
	version := fields[2]
	major, rest, found := strings.Cut(version, ".")
	if !found {
		return "", false
	}
	if _, err := strconv.Atoi(major); err != nil {
		return "", false
	}
	minor, _, _ := strings.Cut(rest, ".")
	if _, err := strconv.Atoi(minor); err != nil {
		return "", false
	}
	return version, true
}

// versionOlderThan reports whether version precedes min. Both are dotted
// numeric versions; a version that cannot be parsed is treated as older, so an
// unreadable Git fails closed with the upgrade hint rather than proceeding.
func versionOlderThan(version, min string) bool {
	current, ok := versionParts(version)
	if !ok {
		return true
	}
	required, ok := versionParts(min)
	if !ok {
		return false
	}
	for i := range required {
		if i >= len(current) {
			return true
		}
		if current[i] != required[i] {
			return current[i] < required[i]
		}
	}
	return false
}

// versionParts parses up to the first three dotted numeric components. Only
// leading digits of each component count, so a vendor suffix is ignored:
// "2.34.1.windows.1" compares as 2.34.1 and "2.39.2 (Apple Git-145)" as 2.39.2.
func versionParts(version string) ([3]int, bool) {
	var parts [3]int
	fields := strings.Split(strings.TrimSpace(version), ".")
	if len(fields) == 0 {
		return parts, false
	}
	for i := range parts {
		if i >= len(fields) {
			return parts, true
		}
		digits := leadingDigits(fields[i])
		if digits == "" {
			return parts, false
		}
		value, err := strconv.Atoi(digits)
		if err != nil {
			return parts, false
		}
		parts[i] = value
	}
	return parts, true
}

// leadingDigits returns the leading run of ASCII digits in field, or "" when it
// starts with anything else.
func leadingDigits(field string) string {
	end := 0
	for end < len(field) && field[end] >= '0' && field[end] <= '9' {
		end++
	}
	return field[:end]
}
