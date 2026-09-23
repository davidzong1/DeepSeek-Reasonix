package hook

// Claude-facing payload helpers, kept out of hook.go to hold its ratchets.
// Only the PayloadFormat "claude" branch reaches them, so a native hook's
// payload is unchanged by anything here.

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// fillAtomicWritePaths exposes every target of an atomic_write call as
// file_paths, and the single path as file_path. A guard written against
// Claude's Write reads .tool_input.file_path; without this an ops transaction
// leaves that field absent, so `jq -r .file_path` yields "null" and the guard
// fails OPEN. Paths are made absolute here (see absoluteHookPath) because
// file_paths is NOT in claudeAbsolutePathInputKeys; a single path is also made
// absolute by that list, since fillAtomicWritePaths runs before it.
func fillAtomicWritePaths(obj map[string]json.RawMessage, cwd string) bool {
	paths := atomicWriteTargetPaths(obj, cwd)
	if len(paths) == 0 {
		return false
	}
	body, err := json.Marshal(paths)
	if err != nil {
		return false
	}
	obj["file_paths"] = body
	return true
}

// atomicWriteTargetPaths lists the absolute targets of an atomic_write call:
// its ops[] entries when it is a transaction, otherwise its single path. The
// list is what a guard needs; a single path is additionally exposed as
// file_path by the caller so a Write-shaped guard keeps matching.
func atomicWriteTargetPaths(obj map[string]json.RawMessage, cwd string) []string {
	var ops []struct {
		Path string `json:"path"`
	}
	if raw, exists := obj["ops"]; exists {
		if err := json.Unmarshal(raw, &ops); err == nil {
			out := make([]string, 0, len(ops))
			for _, op := range ops {
				if abs := absoluteHookPath(op.Path, cwd); abs != "" {
					out = append(out, abs)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	// The key rename runs first, so a single-path call arrives as file_path.
	var path string
	if err := json.Unmarshal(obj["path"], &path); err != nil {
		if err := json.Unmarshal(obj["file_path"], &path); err != nil {
			return nil
		}
	}
	if abs := absoluteHookPath(path, cwd); abs != "" {
		return []string{abs}
	}
	return nil
}

func absoluteHookPath(path, cwd string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if filepath.IsAbs(path) || cwd == "" {
		return path
	}
	return filepath.Join(cwd, path)
}

// fillClaudeAgentDescription supplies Agent's required description when a call
// carried a prompt but no description, so an Agent-scoped guard always finds one.
func fillClaudeAgentDescription(obj map[string]json.RawMessage, fallback string) bool {
	var prompt string
	_ = json.Unmarshal(obj["prompt"], &prompt)
	if strings.TrimSpace(prompt) == "" {
		return false
	}
	var description string
	_ = json.Unmarshal(obj["description"], &description)
	if strings.TrimSpace(description) != "" {
		return false
	}
	body, err := json.Marshal(fallback)
	if err != nil {
		return false
	}
	obj["description"] = body
	return true
}

// absolutizeClaudePathKeys resolves Claude's path fields against the payload's
// cwd, so a prefix-matching guard compares absolute paths on both sides.
func absolutizeClaudePathKeys(obj map[string]json.RawMessage, cwd string) bool {
	if cwd == "" {
		return false
	}
	changed := false
	for _, key := range claudeAbsolutePathInputKeys {
		v, exists := obj[key]
		if !exists {
			continue
		}
		var p string
		if err := json.Unmarshal(v, &p); err != nil || p == "" || filepath.IsAbs(p) {
			continue
		}
		if abs, err := json.Marshal(filepath.Join(cwd, p)); err == nil {
			obj[key] = abs
			changed = true
		}
	}
	return changed
}
