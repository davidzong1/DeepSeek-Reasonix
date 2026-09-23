package builtin

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/tool"
)

func TestAtomicOpsCommitsEveryTarget(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old "+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		atomicObserve(t, ctx, nil, filepath.Join(dir, name))
	}

	raw, _ := json.Marshal(map[string]any{
		"ops": []map[string]any{
			{"path": "a.txt", "mode": "replace", "content": "new a\n"},
			{"path": "b.txt", "mode": "patch", "edits": []map[string]string{{"old": "old b.txt", "new": "new b"}}},
			{"path": "c.txt", "mode": "create", "content": "brand new\n"},
		},
	})
	out, err := w.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("ops: %v", err)
	}
	if !strings.Contains(out, "3/3 committed") {
		t.Fatalf("summary = %q", out)
	}
	if !strings.Contains(out, "tx-") {
		t.Fatalf("summary must name the txid: %q", out)
	}
	for name, want := range map[string]string{"a.txt": "new a\n", "b.txt": "new b\n", "c.txt": "brand new\n"} {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		if string(b) != want {
			t.Fatalf("%s = %q, want %q", name, b, want)
		}
	}
}

// TestAtomicOpsPrepareFailureLeavesZeroTrace is §6.2 case 8 and §6.1 invariant
// 5: a transaction whose second target fails its CAS check must leave every
// destination byte-identical, including the first one that already prepared.
func TestAtomicOpsPrepareFailureLeavesZeroTrace(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	if err := os.WriteFile(a, []byte("A0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("B0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	atomicObserve(t, ctx, nil, a)
	atomicObserve(t, ctx, nil, b)
	// A teammate moves b after the read: its CAS check must fail at prepare.
	if err := os.WriteFile(b, []byte("B1-teammate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, path := range []string{a, b} {
		raw, _ := os.ReadFile(path)
		before[path] = string(raw)
	}

	raw, _ := json.Marshal(map[string]any{
		"ops": []map[string]any{
			{"path": "a.txt", "mode": "replace", "content": "A1\n"},
			{"path": "b.txt", "mode": "replace", "content": "B1\n"},
		},
	})
	_, err := w.Execute(ctx, raw)
	assertOperationCode(t, err, tool.FSStaleVersion)
	for path, want := range before {
		got, _ := os.ReadFile(path)
		if string(got) != want {
			t.Fatalf("%s changed despite a prepare failure: %q (want %q)", path, got, want)
		}
	}
	// The staged temp files must be cleaned up too: prepare's promise is zero
	// trace, not "zero destination trace".
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".atomic-") {
			t.Fatalf("a staged temp file survived a failed prepare: %s", entry.Name())
		}
	}
}

// TestAtomicOpsPartialCommitIsReportedExactly is §6.2 case 7: when one rename
// cannot land, the paths that did commit are reported as committed, the rest as
// not committed, and the journal records the same split.
//
// The failure is injected at the commit boundary by removing the second
// target's staged temp after prepare: publish then fails for that target alone,
// which is exactly the shape of a real mid-commit rename failure (a transient
// lock that never clears, a filter driver refusing the rename). Sabotaging the
// staged file rather than the destination keeps prepare genuinely successful, so
// the case tests the commit phase and not the prepare phase again.
func TestAtomicOpsPartialCommitIsReportedExactly(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	if err := os.WriteFile(a, []byte("A0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("B0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	atomicObserve(t, ctx, nil, a)
	atomicObserve(t, ctx, nil, b)

	plan, err := w.prepareOps(ctx, []atomicOp{
		{atomicWriteParams{Path: a, Mode: "replace", Content: "A1\n"}},
		{atomicWriteParams{Path: b, Mode: "replace", Content: "B1\n"}},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(plan.ops) != 2 || plan.ops[1].tmp == "" {
		t.Fatalf("prepare did not stage both targets: %+v", plan.ops)
	}
	// Remove the second target's staged bytes: the rename has nothing to publish.
	if err := os.Remove(plan.ops[1].tmp); err != nil {
		t.Fatal(err)
	}

	out, err := w.commitOps(ctx, plan)
	if err == nil {
		t.Fatalf("a failed rename must be reported, got %q", out)
	}
	assertOperationCode(t, err, tool.FSStaleVersion)
	if !strings.Contains(err.Error(), plan.txid) {
		t.Fatalf("the report must name its txid %s: %v", plan.txid, err)
	}
	if !strings.Contains(err.Error(), "a.txt") || !strings.Contains(err.Error(), "b.txt") {
		t.Fatalf("the report must name the committed and the failed path: %v", err)
	}
	if !strings.Contains(err.Error(), "committed") || !strings.Contains(err.Error(), "not committed") {
		t.Fatalf("the report must give a per-path status: %v", err)
	}
	if got, _ := os.ReadFile(a); string(got) != "A1\n" {
		t.Fatalf("a.txt = %q, want the committed content", got)
	}
	if got, _ := os.ReadFile(b); string(got) != "B0\n" {
		t.Fatalf("b.txt = %q, want it untouched", got)
	}

	// The journal records the same split, so a reader can recover the state
	// without trusting the model's transcript.
	raw, readErr := os.ReadFile(w.journalPath(plan.txid))
	if readErr != nil {
		t.Fatalf("read journal: %v", readErr)
	}
	var journal atomicWriteJournal
	if err := json.Unmarshal(raw, &journal); err != nil {
		t.Fatalf("journal is not valid JSON: %v", err)
	}
	if len(journal.Committed) != 1 || journal.Committed[0] != a {
		t.Fatalf("journal committed = %v, want [%s]", journal.Committed, a)
	}
	if len(journal.Failed) != 1 || !strings.Contains(journal.Failed[0], b) {
		t.Fatalf("journal failed = %v, want [%s ...]", journal.Failed, b)
	}
	if journal.FinishedAt == "" {
		t.Fatal("journal must record the outcome, not only the plan")
	}
}

// TestAtomicOpsJournalIsWrittenAndGitIgnored pins §3.3's journal contract: the
// plan and outcome land under .reasonix/atomic-writes/, which the repository
// ignores, so a transaction never shows up as an untracked file.
func TestAtomicOpsJournalIsWrittenAndGitIgnored(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("A0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	atomicObserve(t, ctx, nil, path)

	raw, _ := json.Marshal(map[string]any{
		"ops": []map[string]any{{"path": "a.txt", "mode": "replace", "content": "A1\n"}},
	})
	out, err := w.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("ops: %v", err)
	}
	txid := ""
	for _, field := range strings.Fields(out) {
		if strings.HasPrefix(field, "tx-") {
			txid = strings.TrimSuffix(field, ":")
		}
	}
	if txid == "" {
		t.Fatalf("summary names no txid: %q", out)
	}
	journal := filepath.Join(dir, filepath.FromSlash(atomicOpsJournalDir), txid+".json")
	raw2, err := os.ReadFile(journal)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	var decoded atomicWriteJournal
	if err := json.Unmarshal(raw2, &decoded); err != nil {
		t.Fatalf("journal is not valid JSON: %v", err)
	}
	if decoded.TxID != txid || len(decoded.Planned) != 1 || len(decoded.Committed) != 1 {
		t.Fatalf("journal = %+v", decoded)
	}
	if decoded.FinishedAt == "" {
		t.Fatal("journal must record the outcome, not only the plan")
	}

	// The directory is ignored by the repository, so a transaction cannot show
	// up as an untracked file. Assert the ignore rule itself, from the repo root.
	assertGitIgnored(t, atomicOpsJournalDir)
}

// assertGitIgnored runs `git check-ignore` for a repo-relative path and skips
// when git is unavailable, so the case is about the ignore rule rather than the
// test host's tooling.
func assertGitIgnored(t *testing.T, rel string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	cmd := exec.Command("git", "check-ignore", "-q", rel)
	cmd.Dir = "../.."
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s is not git-ignored: %v", rel, err)
	}
}

func TestAtomicOpsRefusesAmbiguousAndImmediateModes(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("A0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	atomicObserve(t, ctx, nil, path)

	for _, tc := range []struct {
		name string
		ops  []map[string]any
		want string
	}{
		{
			name: "the same path twice",
			ops: []map[string]any{
				{"path": "a.txt", "mode": "replace", "content": "x\n"},
				{"path": "a.txt", "mode": "replace", "content": "y\n"},
			},
			want: "twice",
		},
		{
			name: "append cannot join a transaction",
			ops: []map[string]any{
				{"path": "a.txt", "mode": "append", "content": "x\n"},
			},
			want: "append cannot be part of a transaction",
		},
		{
			name: "an entry without a mode",
			ops: []map[string]any{
				{"path": "a.txt", "content": "x\n"},
			},
			want: "mode is required",
		},
		{
			name: "an entry outside the write roots",
			ops: []map[string]any{
				{"path": filepath.Join(t.TempDir(), "outside.txt"), "mode": "create", "content": "x\n"},
			},
			want: "outside the writable roots",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"ops": tc.ops})
			_, err := w.Execute(ctx, raw)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
			if b, _ := os.ReadFile(path); string(b) != "A0\n" {
				t.Fatalf("a refused transaction changed a.txt: %q", b)
			}
		})
	}
}

// TestAtomicOpsResendOnlyTheRemainder pins the recovery loop §3.3 promises: the
// paths reported as not committed can be resent on their own, and the ones that
// already landed are not disturbed.
func TestAtomicOpsResendOnlyTheRemainder(t *testing.T) {
	dir := t.TempDir()
	ctx := observedContext()
	w := atomicTestWriter(dir, nil)
	a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	for _, path := range []string{a, b} {
		if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		atomicObserve(t, ctx, nil, path)
	}
	// Simulate the first transaction having committed only a: a is already new,
	// b is not. Resending b alone must succeed.
	if err := os.WriteFile(a, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	atomicObserve(t, ctx, nil, b)
	raw, _ := json.Marshal(map[string]any{
		"ops": []map[string]any{{"path": "b.txt", "mode": "replace", "content": "new\n"}},
	})
	if _, err := w.Execute(ctx, raw); err != nil {
		t.Fatalf("resend: %v", err)
	}
	for _, path := range []string{a, b} {
		if got, _ := os.ReadFile(path); string(got) != "new\n" {
			t.Fatalf("%s = %q", path, got)
		}
	}
}
