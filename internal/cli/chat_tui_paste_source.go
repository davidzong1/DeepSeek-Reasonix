package cli

import (
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"reasonix/internal/control"
	"reasonix/internal/shellparse"
)

var markdownImageSourceRe = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)

type pastedImageSource struct {
	value        string
	shellDecoded bool
}

func pastedImageSources(text string) ([]pastedImageSource, bool) {
	return pastedImageSourcesForOS(text, runtime.GOOS)
}

func pastedImageSourcesForOS(text, goos string) ([]pastedImageSource, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, false
	}
	if isDataImage(trimmed) {
		return []pastedImageSource{{value: trimmed}}, true
	}
	if matches := markdownImageSourceRe.FindAllStringSubmatch(trimmed, -1); len(matches) > 0 {
		rest := strings.TrimSpace(markdownImageSourceRe.ReplaceAllString(trimmed, ""))
		if rest == "" {
			sources := make([]pastedImageSource, 0, len(matches))
			for _, m := range matches {
				sources = append(sources, pastedImageSource{value: m[1]})
			}
			return sources, true
		}
	}

	lines := nonEmptyPasteLines(trimmed)
	lineSources := rawPastedImageSources(lines)
	if len(lines) > 0 && allImageSources(lineSources, goos) {
		return lineSources, true
	}
	fields := splitPastePathTokens(trimmed)
	fieldSources := rawPastedImageSources(fields)
	if len(fields) > 1 && allImageSources(fieldSources, goos) {
		return fieldSources, true
	}
	if staticFields, malformed := shellparse.StaticFields(trimmed); malformed == "" && len(staticFields) > 1 {
		sources := make([]pastedImageSource, 0, len(staticFields))
		for _, field := range staticFields {
			sources = append(sources, pastedImageSource{value: field, shellDecoded: true})
		}
		if allImageSources(sources, goos) {
			return sources, true
		}
	}
	return nil, false
}

func rawPastedImageSources(values []string) []pastedImageSource {
	sources := make([]pastedImageSource, 0, len(values))
	for _, value := range values {
		sources = append(sources, pastedImageSource{value: value})
	}
	return sources
}

// splitPastePathTokens splits pasted text into path tokens the way a shell
// would hand them to a program: unescaped, unquoted whitespace separates
// tokens, while backslash escapes and token-leading quotes keep a path with
// spaces together. Tokens keep their original escapes/quotes so each one
// round-trips through pastedImagePath. Quotes only open at the start of a
// token, so an apostrophe inside a word ("it's") never swallows the rest of
// the text.
func splitPastePathTokens(s string) []string {
	var tokens []string
	var b strings.Builder
	var quote byte
	escaped := false
	flush := func() {
		if b.Len() > 0 {
			tokens = append(tokens, b.String())
			b.Reset()
		}
	}
	for i := range len(s) {
		ch := s[i]
		switch {
		case escaped:
			b.WriteByte(ch)
			escaped = false
		case quote != 0:
			b.WriteByte(ch)
			if ch == quote {
				quote = 0
			}
		case ch == '\\':
			b.WriteByte(ch)
			escaped = true
		case (ch == '\'' || ch == '"') && b.Len() == 0:
			b.WriteByte(ch)
			quote = ch
		case ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n':
			flush()
		default:
			b.WriteByte(ch)
		}
	}
	flush()
	return tokens
}

func nonEmptyPasteLines(text string) []string {
	var out []string
	for line := range strings.SplitSeq(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func allImageSources(sources []pastedImageSource, goos string) bool {
	if len(sources) == 0 {
		return false
	}
	for _, src := range sources {
		if !looksLikeImageSource(src, goos) {
			return false
		}
	}
	return true
}

func looksLikeImageSource(src pastedImageSource, goos string) bool {
	if isDataImage(strings.TrimSpace(src.value)) {
		return true
	}
	for _, path := range pastedPathCandidates(src.value, goos, src.shellDecoded) {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".png", ".jpg", ".jpeg", ".gif", ".webp":
			return true
		}
	}
	return false
}

func savePastedImageSource(src pastedImageSource) (string, error) {
	value := strings.TrimSpace(src.value)
	if isDataImage(value) {
		return control.SaveImageDataURL(value)
	}
	var lastErr error
	for _, path := range pastedPathCandidates(value, runtime.GOOS, src.shellDecoded) {
		if !looksLikeImagePath(path) {
			continue
		}
		saved, err := control.SaveImageFile(path)
		if err == nil {
			return saved, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("unsupported pasted image source")
}

func looksLikeImagePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	default:
		return false
	}
}

func isDataImage(src string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(src)), "data:image/")
}

// pastedImagePathForOS returns the preferred syntactic candidate with the OS
// injected so platform-specific path handling is testable everywhere.
func pastedImagePathForOS(src, goos string) (string, bool) {
	candidates := pastedPathCandidates(src, goos, false)
	if len(candidates) == 0 {
		return "", false
	}
	return candidates[0], true
}

func hasUnescapedPathWhitespace(s string) bool {
	escaped := false
	for i := range len(s) {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
			return true
		}
	}
	return false
}

// unescapeShellPath applies POSIX backslash semantics to an unquoted pasted
// path: a backslash makes the next byte literal, whatever it is — zsh and
// bash escape any byte they consider special that way (space, parens, ^,
// comma, $, ...), so a whitelist would always lag behind. A trailing
// backslash stays literal. Quoted paths and Windows paths never reach here.
func unescapeShellPath(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// pastedFileRef turns a dragged/pasted non-image file path into an @reference so
// it attaches instead of landing as literal text (and, for a POSIX path, being
// misread as a slash command). Images are handled earlier; only path-shaped
// content (a separator) that points at a real file qualifies, so an ordinary
// pasted word is left alone. Whitespace in the path is escaped so the ref
// survives @-token parsing on submit.
func pastedFileRef(content string) (string, bool) {
	path, ok := resolveExistingPastedPath(content, runtime.GOOS, false, pastedPathExists)
	if !ok || !strings.ContainsAny(path, `/\`) {
		return "", false
	}
	return "@" + control.EscapeRefPath(path), true
}
