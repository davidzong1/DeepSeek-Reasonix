package boot

import (
	"os"
	"path/filepath"
	"strings"
)

// builtProjectRoot is populated by repository builds. It lets an installed CLI
// find the checkout owning the repository's team assets — the team/skills
// source the install target copies from, never the tree a session reads, which
// is user-global. Empty is valid for ordinary go test/go build calls.
var builtProjectRoot string

// ResolveWorkspaceRoot exposes Build's own project-root resolution. A host that
// needs the root *before* Build — a team member's role playbook is read at
// assembly — must call this instead of reading Options.WorkspaceRoot, which is
// empty whenever --dir was not given and would silently disable the lookup.
func ResolveWorkspaceRoot(explicit string) string { return resolveWorkspaceRoot(explicit) }

// ResolveTeamProjectRoot locates the repository owning a checkout's team
// assets: the team/skills source tree (its presence is the marker) and the
// project's legacy .reasonix/team, which adoption reads. Both are repository
// data, so neither follows the process working directory; the runtime role
// playbooks are user-global state and this root never addresses them. A
// development binary under <repo>/bin or the build-time repository root keeps
// an installed CLI attached to its source; an explicit root is only the
// fallback used by ordinary go builds and tests that do not carry repository
// metadata.
func ResolveTeamProjectRoot(explicit string) string {
	if exe, err := osExecutable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		if root := nearestTeamProject(filepath.Dir(exe)); root != "" {
			return root
		}
	}
	if root := teamProjectCandidate(builtProjectRoot); root != "" {
		return root
	}
	if root := teamProjectCandidate(explicit); root != "" {
		return root
	}
	return resolveWorkspaceRoot(explicit)
}

var osExecutable = os.Executable

func teamProjectCandidate(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	if fi, err := os.Stat(filepath.Join(abs, "team", "skills")); err == nil && fi.IsDir() {
		return filepath.Clean(abs)
	}
	return ""
}

func nearestTeamProject(start string) string {
	dir, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for {
		if root := teamProjectCandidate(dir); root != "" {
			return root
		}
		next := filepath.Dir(dir)
		if next == dir {
			return ""
		}
		dir = next
	}
}

func resolveWorkspaceRoot(explicit string) string {
	if explicit != "" {
		return explicit
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if root, ok := nearestGitRoot(wd); ok {
		return root
	}
	return wd
}

// osUserHomeDir is the user's home used as the workspace-root ceiling.
// Indirected so tests can pin a fake home without mutating the process env.
var osUserHomeDir = os.UserHomeDir

// nearestGitRoot returns the nearest ancestor of start holding a .git marker,
// refusing to climb to the user's home directory or above: ~/.reasonix is
// user-global state, never a project root, and a dotfiles repo at $HOME must
// not anchor the workspace (and with it config, team, and permission roots)
// away from the directory the user actually launched reasonix from.
func nearestGitRoot(start string) (string, bool) {
	dir, err := filepath.Abs(start)
	if err != nil {
		dir = filepath.Clean(start)
	}
	home := gitRootCeiling()
	for {
		if home != "" && dir == home {
			return "", false
		}
		if isGitMarker(filepath.Join(dir, ".git")) {
			return dir, true
		}
		next := filepath.Dir(dir)
		if next == dir {
			return "", false
		}
		dir = next
	}
}

// gitRootCeiling returns the cleaned absolute user-home path, or "" when it is
// unavailable or is itself the filesystem root (no climb to bound).
func gitRootCeiling() string {
	home, err := osUserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	home, err = filepath.Abs(home)
	if err != nil {
		return ""
	}
	home = filepath.Clean(home)
	if filepath.Dir(home) == home {
		return "" // home is the filesystem root; there is no ceiling to enforce
	}
	return home
}

func isGitMarker(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && (fi.IsDir() || fi.Mode().IsRegular())
}
