package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	fileencoding "reasonix/internal/fileutil/encoding"
	"strings"
)

// DefaultSystemPrompt is used when config provides none.
const DefaultSystemPrompt = `You are Reasonix, a coding agent.
Use the available tools when they help you complete the user's request.
Keep changes focused and responses concise.`

// UserDecisionPolicy is appended to every system prompt, including user-custom
// prompts, so custom personas cannot accidentally remove the `ask` UI contract.
const UserDecisionPolicy = `User-owned choices: when a consequential decision has no safe, obvious default, call the ask tool so the user can choose. Otherwise proceed with a sensible reversible default. Do not ask in prose when ask is available. In non-interactive runs, state the assumption and take the safest reversible path.`

// LanguagePolicy is the auto fallback appended to the system prompt when no
// concrete UI language is resolved. It is static English text, so it stays part
// of the cache-stable prefix and avoids per-turn language injection.
const LanguagePolicy = `Reply in the same language the user is using in their most recent message: ` +
	`if they write in Chinese answer in Chinese, in English answer in English, and switch ` +
	`whenever they switch. Let this also guide the language you think in. Always keep code, ` +
	`identifiers, file paths, shell commands, and technical terms in their original form — never translate them.`

// ResolveSystemPrompt returns the system prompt, reading system_prompt_file if set.
func (c *Config) ResolveSystemPrompt() (string, error) {
	return c.ResolveSystemPromptForRoot(".")
}

// ResolveSystemPromptForRoot is like ResolveSystemPrompt but resolves a relative
// system_prompt_file against root. Desktop tabs pass their workspace root here so
// prompt files are project-scoped even when the process cwd is elsewhere. A path
// inherited from user config may fall back to Reasonix home, while a path chosen
// by project config is confined to the workspace and never probes user files.
func (c *Config) ResolveSystemPromptForRoot(root string) (string, error) {
	path := c.Agent.SystemPromptFile
	if path == "" {
		return c.InlineSystemPrompt(), nil
	}

	if c.systemPromptFileSource == promptFileSourceProject {
		if filepath.IsAbs(path) || !filepath.IsLocal(filepath.Clean(path)) {
			return "", fmt.Errorf("project system_prompt_file %q must be a relative path within the workspace", path)
		}
		candidate := filepath.Join(resolveRoot(root), path)
		b, err := readProjectSystemPromptFile(root, path)
		if err != nil {
			return "", newSystemPromptFileError(path, []string{candidate}, []error{err})
		}
		return strings.TrimSpace(string(b)), nil
	}

	if filepath.IsAbs(path) {
		b, err := fileencoding.ReadFileUTF8(path)
		if err != nil {
			return "", newSystemPromptFileError(path, []string{path}, []error{err})
		}
		return strings.TrimSpace(string(b)), nil
	}

	candidates := []string{filepath.Join(resolveRoot(root), path)}
	if home := ReasonixHomeDir(); home != "" {
		homeCandidate := filepath.Join(home, path)
		if filepath.Clean(homeCandidate) != filepath.Clean(candidates[0]) {
			candidates = append(candidates, homeCandidate)
		}
	}
	readErrors := make([]error, 0, len(candidates))
	for _, candidate := range candidates {
		b, err := fileencoding.ReadFileUTF8(candidate)
		if err == nil {
			return strings.TrimSpace(string(b)), nil
		}
		readErrors = append(readErrors, fmt.Errorf("%s: %w", candidate, err))
	}
	return "", newSystemPromptFileError(path, candidates, readErrors)
}

func readProjectSystemPromptFile(root, path string) ([]byte, error) {
	workspace, err := filepath.Abs(resolveRoot(root))
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	rootHandle, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, fmt.Errorf("open workspace root %q: %w", workspace, err)
	}
	defer rootHandle.Close()
	f, err := rootHandle.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return fileencoding.DecodeToUTF8(b), nil
}

func newSystemPromptFileError(configured string, candidates []string, readErrors []error) error {
	allMissing := len(readErrors) > 0
	for _, err := range readErrors {
		if !errors.Is(err, fs.ErrNotExist) {
			allMissing = false
			break
		}
	}
	return &systemPromptFileError{
		configured: configured,
		candidates: append([]string(nil), candidates...),
		errors:     append([]error(nil), readErrors...),
		allMissing: allMissing,
	}
}

// InlineSystemPrompt returns the configured system_prompt, or DefaultSystemPrompt
// when unset. It is the fallback when system_prompt_file cannot be read.
func (c *Config) InlineSystemPrompt() string {
	if strings.TrimSpace(c.Agent.SystemPrompt) == "" {
		return DefaultSystemPrompt
	}
	return c.Agent.SystemPrompt
}
