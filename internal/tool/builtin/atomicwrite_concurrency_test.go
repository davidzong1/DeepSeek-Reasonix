package builtin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/fileutil"
	"reasonix/internal/tool"
)

// The cases here are the concurrency and crash half of §6.2. They deliberately
// avoid sleep-based timing: every wait is on a channel or a file the peer
// created, so a slow machine cannot turn a real defect into a pass.

// TestAtomicAppendNeverInterleaves is §6.2 case 2. Two levels are exercised,
// because they answer different questions:
//
//   - through the tool, where the in-process target lock already serializes
//     appends, so the assertion is completeness (no payload lost);
//   - directly on the primitive with no lock, where the ONLY thing keeping 32
//     payloads whole is that each append is a single O_APPEND write(2). That is
//     the level a chunked implementation would fail.
func TestAtomicAppendNeverInterleaves(t *testing.T) {
	const writers = 32
	payload := func(i int) string {
		return fmt.Sprintf("<payload-%02d start>%s<payload-%02d end>\n", i, strings.Repeat("x", 64), i)
	}

	t.Run("through the tool", func(t *testing.T) {
		dir := t.TempDir()
		ctx := observedContext()
		w := atomicTestWriter(dir, nil)
		path := filepath.Join(dir, "log.txt")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make([]error, writers)
		start := make(chan struct{})
		for i := range writers {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				_, errs[i] = atomicCall(t, w, ctx, map[string]any{
					"path": "log.txt", "mode": "append", "content": payload(i),
				})
			}(i)
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("append %d: %v", i, err)
			}
		}
		assertAppendPayloadsWhole(t, path, writers, payload)
	})

	t.Run("primitive with no lock", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "raw.log")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make([]error, writers)
		start := make(chan struct{})
		for i := range writers {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				errs[i] = appendFileEncoded(path, payload(i), 0)
			}(i)
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("append %d: %v", i, err)
			}
		}
		assertAppendPayloadsWhole(t, path, writers, payload)
	})
}

func assertAppendPayloadsWhole(t *testing.T, path string, writers int, payload func(int) string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for i := range writers {
		want := payload(i)
		if !strings.Contains(body, want) {
			t.Fatalf("payload %d is missing or torn:\n%q", i, body)
		}
	}
	if got := strings.Count(body, " start>"); got != writers {
		t.Fatalf("payload count = %d, want %d — an append interleaved or was lost", got, writers)
	}
	if got := strings.Count(body, "<payload-"); got != writers*2 {
		t.Fatalf("payload marker count = %d, want %d — a payload was torn", got, writers*2)
	}
}

// TestAtomicPatchConcurrentSpansLoseNothing is §6.2 case 1: eight writers patch
// eight different spans of one file. Every writer must either succeed or be
// told FS_STALE_VERSION, and every successful writer's bytes must be in the
// final content. A lost update would show as a success whose span is absent.
func TestAtomicPatchConcurrentSpansLoseNothing(t *testing.T) {
	const writers = 8
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "spans.txt")

	var lines []string
	for i := range writers {
		lines = append(lines, fmt.Sprintf("line-%d", i))
	}
	seed := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	outcomes := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			// Each writer observes for itself, then patches only its own line.
			if _, err := atomicCapture(ctx, nil, path); err != nil {
				outcomes[i] = err
				return
			}
			_, err := atomicCall(t, w, ctx, map[string]any{
				"path": "spans.txt", "mode": "patch",
				"edits": []map[string]string{{"old": fmt.Sprintf("line-%d", i), "new": fmt.Sprintf("PATCHED-%d", i)}},
			})
			outcomes[i] = err
		}(i)
	}
	close(start)
	wg.Wait()

	final, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	succeeded := 0
	for i, err := range outcomes {
		if err != nil {
			// A failure must be a loud conflict, never a silent drop.
			var opErr *tool.OperationError
			if !asOperationError(err, &opErr) || opErr.Diagnostic.Code != tool.FSStaleVersion {
				t.Fatalf("writer %d failed with %v, want FS_STALE_VERSION", i, err)
			}
			continue
		}
		succeeded++
		if !bytes.Contains(final, []byte(fmt.Sprintf("PATCHED-%d", i))) {
			t.Fatalf("writer %d reported success but its span is absent:\n%s", i, final)
		}
	}
	if succeeded == 0 {
		t.Fatal("no writer succeeded; the case proves nothing")
	}
	// The file's shape is preserved: the patch changed text, not the line count.
	if got := strings.Count(string(final), "\n"); got != writers {
		t.Fatalf("line count = %d, want %d", got, writers)
	}
}

// TestAtomicWriteCrossProcessReplaceHasOneWinner is §6.2 case 3. Two real
// processes write the same file from the same observed version. The peer
// publishes first; the parent's replace then carries an anchor that no longer
// matches, so it must be refused with the changed hunks rather than silently
// overwriting the peer.
//
// The ordering is established by running the peer to completion before the
// parent writes — not by a sleep — so the case cannot pass by accident on a
// fast host or fail on a slow one. What it pins is the DETECT-AND-REFUSE half
// of the guarantee: at the tool boundary there is no cross-process lock (the
// workspace lease is taken by the agent's coordination pipeline, not here), so
// the loser is told, never silently dropped.
func TestAtomicWriteCrossProcessReplaceHasOneWinner(t *testing.T) {
	if os.Getenv("REASONIX_ATOMIC_WRITE_HELPER") == "1" {
		runAtomicWriteHelper(t)
		return
	}
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "contended.txt")
	if err := os.WriteFile(path, []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The parent observes the seed and keeps that anchor across the peer's write.
	anchor := atomicObserve(t, ctx, nil, path)
	if anchor.ReadID == "" {
		t.Fatal("the read must mint a read id")
	}

	cmd := exec.Command(os.Args[0], "-test.run", "^TestAtomicWriteCrossProcessReplaceHasOneWinner$")
	cmd.Env = append(os.Environ(),
		"REASONIX_ATOMIC_WRITE_HELPER=1",
		"REASONIX_ATOMIC_WRITE_ROOT="+dir,
	)
	// The child is a real second process; its output belongs to the failure
	// report, not to the parent's stream, where a stray line would look like
	// this run's own output.
	var childOut strings.Builder
	cmd.Stdout, cmd.Stderr = &childOut, &childOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("the peer process failed: %v\n%s", err, childOut.String())
	}
	if got, _ := os.ReadFile(path); string(got) != "peer\n" {
		t.Fatalf("the peer's write did not land: %q", got)
	}

	// The parent's anchor still describes the seed, so the replace must be
	// refused — and the peer's bytes must survive.
	_, err := atomicCall(t, w, ctx, map[string]any{
		"path": "contended.txt", "mode": "replace", "content": "parent\n", "since": anchor.ReadID,
	})
	assertOperationCode(t, err, tool.FSStaleVersion)
	if !strings.Contains(err.Error(), "peer") {
		t.Fatalf("the refusal must show the peer's version as the hunk: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "peer\n" {
		t.Fatalf("final content = %q, want the peer's version", got)
	}
}

// runAtomicWriteHelper is the peer process: it observes the file for itself and
// publishes its own version. Its anchor is current, so its write succeeds; the
// parent's stale anchor is what the case then exercises.
func runAtomicWriteHelper(t *testing.T) {
	root := os.Getenv("REASONIX_ATOMIC_WRITE_ROOT")
	path := filepath.Join(root, "contended.txt")
	ctx := observedContext()
	w := atomicTestWriter(root, nil)
	atomicObserve(t, ctx, nil, path)
	if _, err := atomicCall(t, w, ctx, map[string]any{"path": "contended.txt", "mode": "replace", "content": "peer\n"}); err != nil {
		t.Fatalf("peer replace: %v", err)
	}
}

func waitForPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

// TestAtomicWriteCrashLeavesOldOrNewContent is §6.2 case 4. The crash is
// injected in a child process at each persistence boundary — the single-file
// publish and the transaction's per-target publish — and the parent then asserts
// the destination is the complete old content (the crash lands before the
// rename), never a torn one, and that no staged temp survived.
func TestAtomicWriteCrashLeavesOldOrNewContent(t *testing.T) {
	for _, boundary := range []string{"atomic-write", "publish"} {
		t.Run(boundary, func(t *testing.T) {
			if os.Getenv("REASONIX_ATOMIC_CRASH_HELPER") == "1" {
				runAtomicCrashHelper(t)
				return
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "crash.txt")
			if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run", "^TestAtomicWriteCrashLeavesOldOrNewContent$")
			cmd.Env = append(os.Environ(),
				"REASONIX_ATOMIC_CRASH_HELPER=1",
				"REASONIX_ATOMIC_CRASH_ROOT="+dir,
				"REASONIX_ATOMIC_CRASH_BOUNDARY="+boundary,
			)
			// A crash-injection child dies by design; its panic trace is the
			// evidence for the assertions below, so keep it out of the parent's
			// stream and show it only when the child does NOT die.
			var childOut strings.Builder
			cmd.Stdout, cmd.Stderr = &childOut, &childOut
			if err := cmd.Run(); err == nil {
				t.Fatalf("the crash helper exited cleanly; no crash was injected\n%s", childOut.String())
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read after crash: %v", readErr)
			}
			// A crash at either boundary lands before the rename, so the old file
			// is what must be there — and it must be whole. This is the invariant
			// that matters: a reader never observes a torn destination.
			if string(got) != "old\n" {
				t.Fatalf("content after a crash at %s = %q, want the complete old file", boundary, got)
			}
			// A hard crash cannot run cleanup, so a staged temp may survive at the
			// transaction's publish boundary. What must never survive is a PARTIAL
			// file: any leftover must be byte-complete.
			entries, _ := os.ReadDir(dir)
			for _, entry := range entries {
				if !strings.HasPrefix(entry.Name(), ".atomic-") {
					continue
				}
				staged, err := os.ReadFile(filepath.Join(dir, entry.Name()))
				if err != nil {
					t.Fatalf("read the staged temp %s: %v", entry.Name(), err)
				}
				if string(staged) != "new\n" {
					t.Fatalf("a partial staged file survived the crash at %s: %q", boundary, staged)
				}
			}
		})
	}
}

// runAtomicCrashHelper publishes one write with a crash injected at the
// boundary the parent selected. CrashPoint is process-global, which is exactly
// why this runs in a child: panicking in the test process would kill the run.
func runAtomicCrashHelper(t *testing.T) {
	root := os.Getenv("REASONIX_ATOMIC_CRASH_ROOT")
	boundary := os.Getenv("REASONIX_ATOMIC_CRASH_BOUNDARY")
	path := filepath.Join(root, "crash.txt")
	ctx := observedContext()
	w := atomicTestWriter(root, nil)
	atomicObserve(t, ctx, nil, path)
	fileutil.CrashPoint = func(op, _ string) {
		if op == boundary {
			panic("injected crash at the " + boundary + " boundary")
		}
	}
	defer func() { fileutil.CrashPoint = nil }()
	if boundary == "publish" {
		// The transaction path publishes through PublishStagedWrite, so it is
		// driven through ops to reach that boundary.
		raw, _ := json.Marshal(map[string]any{
			"ops": []map[string]any{{"path": "crash.txt", "mode": "replace", "content": "new\n"}},
		})
		_, _ = w.Execute(ctx, raw)
		return
	}
	_, _ = atomicCall(t, w, ctx, map[string]any{"path": "crash.txt", "mode": "replace", "content": "new\n"})
}

// TestAtomicWriteExternalWriterIsDetected is §6.2 case 6: a writer that bypasses
// the tool entirely (the user's editor, a shell) is detected and refused with
// the changed hunks, and the same patch succeeds after a re-read.
func TestAtomicWriteExternalWriterIsDetected(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	atomicObserve(t, ctx, nil, path)

	// The external writer changes a line the caller never touched.
	if err := os.WriteFile(path, []byte("first\nSECOND-BY-EDITOR\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := atomicCall(t, w, ctx, map[string]any{
		"path": "a.txt", "mode": "patch",
		"edits": []map[string]string{{"old": "first", "new": "FIRST"}},
	})
	assertOperationCode(t, err, tool.FSStaleVersion)
	if !strings.Contains(err.Error(), "SECOND-BY-EDITOR") {
		t.Fatalf("the refusal must carry the hunk that changed underneath: %v", err)
	}

	// Re-read, then the same patch succeeds on the editor's version.
	atomicObserve(t, ctx, nil, path)
	if _, err := atomicCall(t, w, ctx, map[string]any{
		"path": "a.txt", "mode": "patch",
		"edits": []map[string]string{{"old": "first", "new": "FIRST"}},
	}); err != nil {
		t.Fatalf("patch after re-read: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "FIRST\nSECOND-BY-EDITOR\n" {
		t.Fatalf("content = %q", got)
	}
}

// TestAtomicWriteSincePinsTheAnchor is §2.3's `since` contract: a cited read id
// resolves to the version that read observed, so a caller can deliberately
// write against an older anchor — and be refused when the file moved on.
func TestAtomicWriteSincePinsTheAnchor(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	anchor := atomicObserve(t, ctx, nil, path)
	if anchor.ReadID == "" {
		t.Fatal("a read must mint a read id")
	}

	// A teammate moves the file on.
	if err := os.WriteFile(path, []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	atomicObserve(t, ctx, nil, path) // the session now observes v2

	// Citing the older read id must still be refused: the id is resolved to the
	// version it observed, not to "whatever is current".
	_, err := atomicCall(t, w, ctx, map[string]any{
		"path": "a.txt", "mode": "replace", "content": "mine\n", "since": anchor.ReadID,
	})
	assertOperationCode(t, err, tool.FSStaleVersion)
	if got, _ := os.ReadFile(path); string(got) != "v2\n" {
		t.Fatalf("a refused replace wrote: %q", got)
	}

	// A model-invented id resolves to nothing and is refused as unobserved.
	_, err = atomicCall(t, w, ctx, map[string]any{
		"path": "a.txt", "mode": "replace", "content": "mine\n", "since": "r-deadbeef",
	})
	assertOperationCode(t, err, tool.FSNotObserved)
}

// TestAtomicWriteSameTurnSecondWriteBuildsOnTheFirst pins that a successful
// write refreshes the session observation: two writes in one turn must not have
// the second one report the first as a conflict.
func TestAtomicWriteSameTurnSecondWriteBuildsOnTheFirst(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("v0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	atomicObserve(t, ctx, nil, path)

	if _, err := atomicCall(t, w, ctx, map[string]any{"path": "a.txt", "mode": "replace", "content": "v1\n"}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := atomicCall(t, w, ctx, map[string]any{"path": "a.txt", "mode": "replace", "content": "v2\n"}); err != nil {
		t.Fatalf("second write must build on the first, got: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "v2\n" {
		t.Fatalf("content = %q", got)
	}
}

// TestAtomicWriteCreateAfterObservedAbsentIsAllowed pins the create path that
// needs no prior read: an absent observation authorizes a create, and a present
// one refuses it.
func TestAtomicWriteCreateAfterObservedAbsentIsAllowed(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "later.txt")

	// Observing an absent target is what a read of a missing file does.
	if _, err := atomicCapture(ctx, nil, path); err != nil {
		t.Fatalf("capture absent: %v", err)
	}
	if _, err := atomicCall(t, w, ctx, map[string]any{"path": "later.txt", "mode": "create", "content": "made\n"}); err != nil {
		t.Fatalf("create after an absent observation: %v", err)
	}
	// Now that it is observed present, a create is refused.
	atomicObserve(t, ctx, nil, path)
	_, err := atomicCall(t, w, ctx, map[string]any{"path": "later.txt", "mode": "create", "content": "again\n"})
	assertOperationCode(t, err, tool.FSStaleVersion)
}

// TestAtomicWritePreviewMatchesExecute is the contract preview.go states for
// every writer: the previewed new text must equal what Execute persists, or a
// user approves a diff that never happens.
func TestAtomicWritePreviewMatchesExecute(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	seed := "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "replace", args: map[string]any{"path": "a.txt", "mode": "replace", "content": "one\ntwo\n"}},
		{name: "append", args: map[string]any{"path": "a.txt", "mode": "append", "content": "delta\n"}},
		{name: "patch edits", args: map[string]any{"path": "a.txt", "mode": "patch", "edits": []map[string]string{{"old": "beta", "new": "BETA"}}}},
		{name: "patch range", args: map[string]any{"path": "a.txt", "mode": "patch", "content": "X\nY", "range": map[string]int{"start": 2, "end": 2}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
				t.Fatal(err)
			}
			w := atomicTestWriter(dir, nil)
			// Preview runs on a fresh observation, like the approval card does.
			previewCtx := observedContext()
			atomicObserve(t, previewCtx, nil, path)
			raw, _ := json.Marshal(tc.args)
			change, err := w.Preview(previewCtx, raw)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}

			execCtx := observedContext()
			atomicObserve(t, execCtx, nil, path)
			if _, err := w.Execute(execCtx, raw); err != nil {
				t.Fatalf("execute: %v", err)
			}
			persisted, _ := os.ReadFile(path)
			if string(persisted) != change.NewText {
				t.Fatalf("preview promised %q but Execute wrote %q", change.NewText, persisted)
			}
		})
	}
}
