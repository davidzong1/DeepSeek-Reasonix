package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/fileops"
	"reasonix/internal/tool"
	"reasonix/internal/tool/builtin"
	"reasonix/internal/workspacelease"
)

// §6.2 case 10 (a path-bound sub-agent's write_paths boundary) and §6.1
// invariant 6 (the lease domain is the files a call names, not the workspace)
// both rest on extractWritePathsFromArgs reporting EVERY target of a call.

func TestExtractAtomicWritePathsCoversPathAndOps(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name string
		args string
		want []string
	}{
		{
			name: "single path",
			args: `{"path":"docs/a.md","mode":"replace","content":"x"}`,
			want: []string{filepath.Join(root, "docs/a.md")},
		},
		{
			name: "ops reports every entry",
			args: `{"ops":[{"path":"a.go","mode":"replace","content":"x"},{"path":"b.go","mode":"patch","edits":[{"old":"a","new":"b"}]},{"path":"sub/c.go","mode":"create","content":"y"}]}`,
			want: []string{filepath.Join(root, "a.go"), filepath.Join(root, "b.go"), filepath.Join(root, "sub/c.go")},
		},
		{
			name: "absolute paths pass through",
			args: `{"ops":[{"path":"/tmp/x.txt","mode":"create","content":"x"}]}`,
			want: []string{"/tmp/x.txt"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractWritePathsFromArgs("atomic_write", root, json.RawMessage(tc.args))
			if err != nil {
				t.Fatalf("extract: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("paths = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("paths = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestExtractAtomicWritePathsRejectsAnUnnameableTarget pins the fail-closed half:
// a call whose target cannot be named must be refused here rather than getting a
// partial claim that would leave one target unguarded.
func TestExtractAtomicWritePathsRejectsAnUnnameableTarget(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name string
		args string
	}{
		{name: "no path and no ops", args: `{"mode":"replace","content":"x"}`},
		{name: "ops entry without a path", args: `{"ops":[{"mode":"replace","content":"x"}]}`},
		{name: "blank path", args: `{"path":"   ","mode":"replace","content":"x"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := extractWritePathsFromArgs("atomic_write", root, json.RawMessage(tc.args)); err == nil {
				t.Fatal("an unnameable target must be refused")
			}
		})
	}
}

// TestAtomicWriteIsPathBoundByWritePaths is §6.2 case 10: a sub-agent with a
// declared write_paths claim may write inside it and nowhere else, and an ops
// transaction that names any out-of-claim path is refused as a whole — not
// partially applied.
func TestAtomicWriteIsPathBoundByWritePaths(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(docs, "a.md")
	outside := filepath.Join(root, "secret.md")
	for _, path := range []string{inside, outside} {
		if err := os.WriteFile(path, []byte("seed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	claim, err := NormalizeWritePaths(root, []string{"docs"})
	if err != nil {
		t.Fatal(err)
	}

	reg := tool.NewRegistry()
	writer := builtin.Workspace{Dir: root, WriteRoots: []string{root}}.Tools("atomic_write")[0]
	reg.Add(writer)
	bound, removed := BindWritePaths(reg, claim, root, false)
	if len(removed) != 0 {
		t.Fatalf("removed = %v, want none: atomic_write is path-scoped", removed)
	}
	scoped, ok := bound.Get("atomic_write")
	if !ok {
		t.Fatal("the bound registry dropped atomic_write")
	}

	// Inside the claim: allowed. create is used because it is the mode that
	// needs no prior read — the claim, not the anchor, is what this case tests.
	insideArgs, _ := json.Marshal(map[string]any{"path": "docs/new.md", "mode": "create", "content": "ok\n"})
	if _, err := scoped.Execute(context.Background(), insideArgs); err != nil {
		t.Fatalf("an in-claim write was refused: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(docs, "new.md")); string(got) != "ok\n" {
		t.Fatalf("in-claim content = %q", got)
	}

	// Outside the claim: refused, and the target is untouched.
	outsideArgs, _ := json.Marshal(map[string]any{"path": "secret.md", "mode": "create", "content": "leak\n"})
	_, err = scoped.Execute(context.Background(), outsideArgs)
	if err == nil || !strings.Contains(err.Error(), "write_paths") {
		t.Fatalf("out-of-claim error = %v", err)
	}
	if got, _ := os.ReadFile(outside); string(got) != "seed\n" {
		t.Fatalf("an out-of-claim write landed: %q", got)
	}

	// A transaction that names one out-of-claim path is refused whole.
	opsArgs, _ := json.Marshal(map[string]any{"ops": []map[string]any{
		{"path": "docs/new2.md", "mode": "create", "content": "ok\n"},
		{"path": "secret.md", "mode": "create", "content": "leak\n"},
	}})
	if _, err := scoped.Execute(context.Background(), opsArgs); err == nil || !strings.Contains(err.Error(), "write_paths") {
		t.Fatalf("an ops call naming an out-of-claim path must be refused: %v", err)
	}
	if got, _ := os.ReadFile(outside); string(got) != "seed\n" {
		t.Fatalf("a refused ops transaction wrote out of claim: %q", got)
	}
	if _, statErr := os.Stat(filepath.Join(docs, "new2.md")); !os.IsNotExist(statErr) {
		t.Fatalf("a refused ops transaction still created its in-claim target: %v", statErr)
	}
}

// TestAtomicWriteLeaseScopeIsTheFileNotTheWorkspace is §6.1 invariant 6. Two
// independent owners write two different files in the same workspace: neither
// blocks, because the lease domain a call takes is the file it names. A peer
// taking the SAME file does block — that is what makes the scope a real claim
// rather than a no-op.
//
// The two peers are separate Owners because one Owner's own holds are
// re-entrant and ordered (HoldWriteForPaths refuses to acquire out of slot
// order), so a single Owner could not express "two writers contending".
func TestAtomicWriteLeaseScopeIsTheFileNotTheWorkspace(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a.txt"), filepath.Join(root, "b.txt")
	for _, path := range []string{a, b} {
		if err := os.WriteFile(path, []byte("seed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lockDir := filepath.Join(t.TempDir(), "locks")
	owner, err := workspacelease.New(root, lockDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := workspacelease.New(root, lockDir, nil)
	if err != nil {
		t.Fatal(err)
	}

	releaseA, err := owner.HoldWriteForPaths(context.Background(), []string{a})
	if err != nil {
		t.Fatalf("hold a: %v", err)
	}
	defer releaseA()

	// The peer writes a DIFFERENT file: it must not wait on a's file lease.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	releaseB, err := peer.HoldWriteForPaths(ctx, []string{b})
	if err != nil {
		t.Fatalf("a peer writing b was blocked by a's file lease: %v", err)
	}
	releaseB()

	// The same file does block: the peer must time out rather than proceed.
	blockedCtx, blockedCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer blockedCancel()
	if release, err := peer.HoldWriteForPaths(blockedCtx, []string{a}); err == nil {
		release()
		t.Fatal("a peer acquired the same file's lease while it was held")
	}

	// And the hold's declared scope names the file, not the workspace.
	if state := owner.State(); state.HeldScope != "file" {
		t.Fatalf("held scope = %q, want \"file\" (a whole-workspace hold would serialize every peer)", state.HeldScope)
	}
}

// TestAtomicWriteDeclaresEveryWriteTarget pins the declaration the coordination
// pipeline reads: DeclareWriteAccess must name the parent directory of every
// target, including each ops entry's.
func TestAtomicWriteDeclaresEveryWriteTarget(t *testing.T) {
	root := t.TempDir()
	writer := builtin.Workspace{Dir: root, WriteRoots: []string{root}}.Tools("atomic_write")[0]
	declarer, ok := writer.(tool.WriteAccessDeclarer)
	if !ok {
		t.Fatal("atomic_write must declare its write access")
	}
	args, _ := json.Marshal(map[string]any{"ops": []map[string]any{
		{"path": "docs/a.md", "mode": "create", "content": "x"},
		{"path": "src/b.go", "mode": "create", "content": "y"},
	}})
	decl, err := declarer.DeclareWriteAccess(args)
	if err != nil {
		t.Fatalf("DeclareWriteAccess: %v", err)
	}
	want := map[string]bool{filepath.Join(root, "docs"): true, filepath.Join(root, "src"): true}
	if len(decl.Directories) != len(want) {
		t.Fatalf("declared %v, want the two parent directories %v", decl.Directories, want)
	}
	for _, dir := range decl.Directories {
		abs := dir
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, abs)
		}
		if !want[abs] {
			t.Fatalf("declared an unexpected directory %s (want %v)", dir, want)
		}
	}
}

// TestAtomicWriteDeclareEvidenceTargetFailsClosed pins the evidence contract: a
// target the host cannot observe must produce no evidence rather than stale
// evidence, so the write is refused upstream instead of publishing blind.
func TestAtomicWriteDeclareEvidenceTargetFailsClosed(t *testing.T) {
	root := t.TempDir()
	writer := builtin.Workspace{Dir: root, WriteRoots: []string{root}}.Tools("atomic_write")[0]
	declarer, ok := writer.(tool.EvidenceDeclarer)
	if !ok {
		t.Fatal("atomic_write must declare evidence")
	}
	ctx := fileops.WithStore(context.Background(), fileops.NewStore())
	args, _ := json.Marshal(map[string]any{"path": "nope.txt", "mode": "replace", "content": "x"})
	if _, err := declarer.DeclareEvidenceTarget(ctx, args); err == nil {
		t.Fatal("evidence for a missing target must fail, not succeed")
	}
}

// TestAtomicWritePlanModeIsUnsafe pins the phase opt-out: a plan-mode turn must
// not be able to mutate the workspace through the atomic pair.
func TestAtomicWritePlanModeIsUnsafe(t *testing.T) {
	root := t.TempDir()
	writer := builtin.Workspace{Dir: root, WriteRoots: []string{root}}.Tools("atomic_write")[0]
	classifier, ok := writer.(tool.PlanModeClassifier)
	if !ok {
		t.Fatal("atomic_write must declare its plan-mode stance")
	}
	if classifier.PlanModeSafe() {
		t.Fatal("atomic_write must not be plan-mode safe")
	}
	if writer.ReadOnly() {
		t.Fatal("atomic_write must not be read-only")
	}
}

var _ = errors.Is
