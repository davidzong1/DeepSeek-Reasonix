package control

// @-reference sizing (R4): a reference is a pointer. Inlining a whole document
// on the model's behalf charges every later request prefix for content the turn
// may not need.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOversizedFileRefIsPointerNotBody is the token contract: an @-reference to
// a document larger than the cap resolves to a bounded preview plus the exact
// read_file call that pages the rest.
func TestOversizedFileRefIsPointerNotBody(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("line of a long document\n", 4000) // ~100 KiB, past the cap
	path := filepath.Join(dir, "long.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got, isDir, err := readFileRef(path, "")
	if err != nil || isDir {
		t.Fatalf("readFileRef = (%v, %v)", isDir, err)
	}
	if len(got) >= len(body)/4 {
		t.Fatalf("the reference must not carry the document: got %d bytes of %d", len(got), len(body))
	}
	if len(got) > maxFileRefBytes {
		t.Fatalf("the reference must stay under the cap: %d bytes", len(got))
	}
	for _, want := range []string{
		"[large file",
		"not inlined in full",
		fmt.Sprintf("%d bytes", len(body)),
		`read_file(path="` + path + `")`,
		"--- preview (first ",
		"--- end preview ---",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the note must contain %q, got:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "line of a long document") {
		t.Errorf("the preview must show the file's head, got:\n%s", got)
	}
}

// TestOversizedFileRefPreviewIsBounded pins the preview's own cap: the note is
// only useful if its size does not track the document's.
func TestOversizedFileRefPreviewIsBounded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 512*1024)), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _, err := readFileRef(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := refPreviewBytes + 512; len(got) > want {
		t.Fatalf("a 512 KiB file must not produce a %d-byte reference (want <= %d)", len(got), want)
	}
}

// TestSmallFileRefStaysVerbatim pins the boundary the cap must not move: a file
// under the cap is still injected byte-for-byte, so ordinary @file references
// behave exactly as before.
func TestSmallFileRefStaysVerbatim(t *testing.T) {
	dir := t.TempDir()
	content := "package main\n\nfunc main() {}\n"
	path := filepath.Join(dir, "small.go")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, isDir, err := readFileRef(path, "")
	if err != nil || isDir {
		t.Fatalf("readFileRef = (%v, %v)", isDir, err)
	}
	if got != content {
		t.Fatalf("a small file must be injected verbatim:\n got %q\nwant %q", got, content)
	}
}
