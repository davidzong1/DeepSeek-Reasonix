package control

import (
	"fmt"
	"strings"
)

// maxFileRefBytes caps how much of an @-referenced file is injected into a
// message, so "@somehuge.log" can't blow the context window. A file past the
// cap becomes a bounded preview plus a pointer (see refPreviewBytes).
const maxFileRefBytes = 16 * 1024

// refPreviewBytes is how much of an over-cap file's head is inlined. It is large
// enough to recognize what the file is and small enough that a leader who
// references a long document pays for a pointer rather than the document.
const refPreviewBytes = 4 * 1024

// oversizedFileRefNote is what an over-cap file reference resolves to instead of
// its full body. The head is inlined as a preview so the model can recognize the
// document, and the note names the exact tool call that reads the rest.
//
// The note reports the file's size rather than its line count: a line count
// would mean reading the whole file, which is the cost this note exists to
// avoid.
func oversizedFileRefNote(displayPath, preview string, total int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[large file %s: %d bytes — not inlined in full]\n", displayPath, total)
	fmt.Fprintf(&b, "Read it with read_file(path=%q) when you need the rest; the call accepts offset/limit to page.\n", displayPath)
	fmt.Fprintf(&b, "--- preview (first %d bytes) ---\n", len(preview))
	b.WriteString(preview)
	b.WriteString("\n--- end preview ---")
	return b.String()
}
