package shellrun

import (
	"strings"
	"unicode/utf8"
)

// completePrefix drops only a final partial rune. Interior invalid bytes retain
// their existing behavior; transport must not manufacture invalid boundary bytes.
func completePrefix(p []byte) []byte {
	start := len(p) - 1
	for start >= 0 && !utf8.RuneStart(p[start]) {
		start--
	}
	if start >= 0 && !utf8.FullRune(p[start:]) {
		return p[:start]
	}
	return p
}

func completeTail(p []byte) []byte {
	for len(p) > 0 && !utf8.RuneStart(p[0]) {
		p = p[1:]
	}
	return completePrefix(p)
}

// Flush finishes a progress stream after the owner has drained the child,
// including cancellation. An unfinished rune is represented once, not silently
// lost or emitted as several invalid JSON strings.
func (w *progressWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.emit == nil || w.truncated {
		return
	}
	w.writeUTF8(nil, true)
}

func (w *progressWriter) writeUTF8(p []byte, final bool) {
	// At most one additional rune is needed to detect truncation. Do not copy
	// an arbitrarily large Write when joining it to a pending character.
	p = p[:min(len(p), max(0, w.limit-w.forwarded)+utf8.UTFMax)]
	if len(w.pending) > 0 {
		p = append(w.pending, p...)
		w.pending = nil
	}
	var out strings.Builder
	truncated := false
	for len(p) > 0 {
		if !utf8.FullRune(p) {
			if !final {
				w.pending = append([]byte(nil), p...)
				break
			}
			p = []byte(string(utf8.RuneError))
		}
		r, size := utf8.DecodeRune(p)
		encodedSize := size
		if r == utf8.RuneError && size == 1 {
			encodedSize = 3
		}
		if w.forwarded+out.Len()+encodedSize > w.limit {
			truncated = true
			break
		}
		out.WriteRune(r)
		p = p[size:]
	}
	if out.Len() > 0 {
		w.forwarded += out.Len()
		w.emit(out.String())
	}
	if truncated {
		w.truncated = true
		w.pending = nil
		if w.marker != "" {
			w.emit(w.marker)
		}
	}
}
