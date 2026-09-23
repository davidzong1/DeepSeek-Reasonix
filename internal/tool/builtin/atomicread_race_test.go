package builtin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A reader must never observe a torn file. Every read in this loop either sees
// the complete old content or the complete new one, because the writer publishes
// with temp+fsync+rename — there is no state in which half the new bytes are
// visible.
//
// The test is deliberately a real race (no sleeps, no artificial gates): the
// writer rewrites the whole file while readers hammer it, and each reader
// checks that what it decoded is one of the two whole contents.
func TestAtomicReadNeverTearsAgainstAtomicReplace(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "hot.txt")
	oldContent := strings.Repeat("old line\n", 200)
	newContent := strings.Repeat("new line\n", 200)
	atomicWriteFile(t, path, oldContent)

	oldHash := atomicTestHash(oldContent)
	newHash := atomicTestHash(newContent)
	reader := atomicReadTool(t, dir)

	stop := make(chan struct{})
	writerDone := make(chan struct{})
	bad := make(chan string, 8)

	go func() {
		defer close(writerDone)
		writer := atomicWrite{workDir: dir, roots: realRoots([]string{dir})}
		// Each iteration runs in its own observation context, the way a fresh
		// turn would: the writer re-reads what it is about to replace.
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			ctx := atomicTestContext()
			if _, err := reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "hot.txt"})); err != nil {
				continue
			}
			content := newContent
			if i%2 == 0 {
				content = oldContent
			}
			// Replace publishes atomically; a reader either sees the whole old
			// file or the whole new one. A refusal (a concurrent teammate moved
			// the anchor) is legitimate and is not a tear.
			_, _ = writer.Execute(ctx, atomicReadArgs(t, map[string]any{
				"path": "hot.txt", "mode": "replace", "content": content,
			}))
		}
	}()

	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			ctx := atomicTestContext()
			for range 60 {
				out, err := reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "hot.txt"}))
				if err != nil {
					continue
				}
				body := atomicTestBody(out)
				hash := atomicTestHash(body)
				if hash != oldHash && hash != newHash {
					select {
					case bad <- fmt.Sprintf("read %d bytes that are neither version (hash %s)", len(body), hash[:12]):
					default:
					}
					return
				}
			}
		}()
	}
	readers.Wait()
	close(stop)
	<-writerDone

	select {
	case msg := <-bad:
		t.Fatal(msg)
	default:
	}
}

// Two readers of the same file in one turn must both see a whole file, and
// neither may disturb the other's anchor.
func TestAtomicReadConcurrentReadersShareAnAnchor(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	atomicWriteFile(t, path, strings.Repeat("shared line\n", 120))
	ctx := atomicTestContext()
	reader := atomicReadTool(t, dir)

	var wg sync.WaitGroup
	ids := make([]string, 8)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, err := reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt"}))
			if err != nil {
				t.Errorf("reader %d: %v", i, err)
				return
			}
			ids[i] = atomicHeaderReadID(t, out)
		}(i)
	}
	wg.Wait()
	for i, id := range ids {
		if !atomicReadIDShape(id) {
			t.Fatalf("reader %d produced no read id: %q", i, id)
		}
		if id != ids[0] {
			t.Fatalf("readers of one unchanged file disagreed: %q vs %q", id, ids[0])
		}
	}
}

// Concurrent appends must land whole and all of them must land: the payload
// count is the proof that the single-write append is not interleaving with
// itself. A read running alongside either delivers a whole version or refuses
// loudly — it may never hand back a torn one.
func TestAtomicReadConcurrentAppendKeepsEveryPayloadWhole(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	atomicWriteFile(t, path, "first\n")
	reader := atomicReadTool(t, dir)

	const writers = 24
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := atomicTestContext()
			writer := atomicWrite{workDir: dir, roots: realRoots([]string{dir})}
			if _, err := writer.Execute(ctx, atomicReadArgs(t, map[string]any{
				"path": "a.txt", "mode": "append", "content": fmt.Sprintf("payload-%02d\n", i),
			})); err != nil {
				t.Errorf("append %d: %v", i, err)
			}
			// A read of a file that is being appended to is allowed to refuse
			// (FS_STALE_VERSION) — but never to succeed with partial content.
			out, err := reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "a.txt"}))
			if err != nil {
				return
			}
			for _, line := range strings.Split(atomicTestBody(out), "\n") {
				if line == "" || line == "first" || strings.HasPrefix(line, "payload-") {
					continue
				}
				t.Errorf("a concurrent read returned a torn line: %q", line)
				return
			}
		}(i)
	}
	wg.Wait()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != writers+1 {
		t.Fatalf("file holds %d lines, want %d (one per append plus the original)", len(lines), writers+1)
	}
	seen := map[string]bool{}
	for _, line := range lines[1:] {
		if !strings.HasPrefix(line, "payload-") || len(line) != len("payload-00") {
			t.Fatalf("a torn or partial append reached the file: %q", line)
		}
		if seen[line] {
			t.Fatalf("duplicate payload %q", line)
		}
		seen[line] = true
	}
}

func atomicTestHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// atomicTestBody strips the header, the numbered-line prefixes and the paging
// trailer from a read result, recovering the source text the reader delivered.
func atomicTestBody(output string) string {
	lines := strings.Split(output, "\n")
	if len(lines) > 0 && strings.HasPrefix(lines[0], "read ") {
		lines = lines[1:]
	}
	var body []string
	for _, line := range lines {
		if strings.HasPrefix(line, "[more lines below;") {
			break
		}
		if arrow := strings.Index(line, "→"); arrow > 0 {
			body = append(body, line[arrow+len("→"):])
		}
	}
	if len(body) == 0 {
		return ""
	}
	return strings.Join(body, "\n") + "\n"
}
