package builtin

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// The write-side half of the §5.4 measurement probe: the same fixtures the
// read-side probe measures, against the shell habit and the existing tool, with
// BUDGET assertions rather than absolute bytes.

// TestAtomicWriteMeasureWorkedExample1 measures the most common team job: change
// a few lines inside a large file. The write side of it is the whole-file
// rewrite (write_file or a python heredoc) versus one anchored patch.
func TestAtomicWriteMeasureWorkedExample1(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	ctx := observedContext()
	path := filepath.Join(dir, "big.go")

	var b strings.Builder
	b.WriteString("package big\n\n")
	for i := range 1990 {
		fmt.Fprintf(&b, "// filler line %04d with some words to make it a realistic width\n", i)
	}
	b.WriteString("\nfunc Target() int {\n\treturn 1\n}\n")
	content := b.String()
	atomicWriteFile(t, path, content)

	// Today (bash): the whole file is the payload of the command itself.
	bashPayload := len(fmt.Sprintf(`python3 - <<'PY'
p='big.go'
s=open(p).read()
s=s.replace('return 1','return 2')
open(p,'w').write(s)
PY`)) + len(content)
	bashResult := 0

	// Today (structured): read_file to observe, then write_file with the whole
	// new body.
	readArgs := len(`{"path":"big.go"}`)
	readResult := atomicReadToolResultBytesFor(t, ctx, dir, map[string]any{"path": "big.go"})
	writeArgs := len(`{"path":"big.go","content":`) + len(content)
	writeResult := len("wrote 48123 bytes to big.go")

	// New: atomic_read window to observe, then one anchored patch.
	reader := atomicReadTool(t, dir)
	if _, err := reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "big.go", "mode": "window", "offset": 1985, "limit": 20})); err != nil {
		t.Fatal(err)
	}
	writer := atomicTestWriter(dir, nil)
	pairWriteArgs := atomicMeasureJSONSize(t, map[string]any{
		"path": "big.go", "mode": "patch",
		"edits": []map[string]string{{"old": "return 1", "new": "return 2"}},
	})
	receipt, err := writer.Execute(ctx, argsJSON(t, map[string]any{
		"path": "big.go", "mode": "patch",
		"edits": []map[string]string{{"old": "return 1", "new": "return 2"}},
	}))
	if err != nil {
		t.Fatalf("patch: %v", err)
	}

	t.Log(atomicMeasureSample{"bash python heredoc rewrite", bashPayload, bashResult, atomicMeasurePrefix(t, "bash"), bashPayload + bashResult, 0}.String())
	t.Log(atomicMeasureSample{"read_file + write_file", readArgs + writeArgs, readResult + writeResult, atomicMeasurePrefix(t, "read_file", "write_file"), readArgs + writeArgs + readResult + writeResult, 0}.String())
	t.Log(atomicMeasureSample{"atomic_write patch", pairWriteArgs, len(receipt), atomicMeasurePrefix(t, "atomic_write"), pairWriteArgs + len(receipt), 0}.String())

	// The patch carries a few bytes of anchors where the rewrite carries the
	// whole file: that ratio is the point of mode=patch.
	if pairWriteArgs*100 > writeArgs*2 {
		t.Errorf("patch args %dB are not at most 2%% of the whole-file write args %dB", pairWriteArgs, writeArgs)
	}
	// And the receipt never grows into a second copy of the file.
	if len(receipt) > maxPostWriteReceiptBytes+atomicReceiptBudgetBytes {
		t.Errorf("patch receipt is %dB, over the bounded-receipt budget", len(receipt))
	}
}

// TestAtomicWriteMeasureWorkedExample3 measures the multi-file job §5.2 calls
// out: three files, one call. The byte story is secondary — the win is one
// round and one lease acquisition instead of three of each — so the probe
// measures the call count and the bytes side by side and asserts both.
func TestAtomicWriteMeasureWorkedExample3(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	ctx := observedContext()
	writer := atomicTestWriter(dir, nil)

	targets := []string{"a.go", "b.go", "c.go"}
	for _, name := range targets {
		path := filepath.Join(dir, name)
		atomicWriteFile(t, path, "package p\n\nfunc Old() int {\n\treturn 1\n}\n")
		if _, err := atomicCapture(ctx, nil, path); err != nil {
			t.Fatal(err)
		}
	}

	// Today: three separate edit calls, three lease acquisitions, three rounds.
	perCall := atomicMeasureJSONSize(t, map[string]any{
		"path": "a.go", "old_string": "return 1", "new_string": "return 2",
	})
	todayArgs := perCall * len(targets)
	todayResult := len("edited a.go") * len(targets)

	// New: one transaction. Its payload carries all three bodies, so it is
	// larger than one call and smaller than three (no per-call envelope).
	ops := make([]map[string]any, 0, len(targets))
	for _, name := range targets {
		ops = append(ops, map[string]any{
			"path": name, "mode": "patch",
			"edits": []map[string]string{{"old": "return 1", "new": "return 2"}},
		})
	}
	pairArgs := atomicMeasureJSONSize(t, map[string]any{"ops": ops})
	receipt, err := writer.Execute(ctx, argsJSON(t, map[string]any{"ops": ops}))
	if err != nil {
		t.Fatalf("ops: %v", err)
	}

	t.Log(atomicMeasureSample{"3 x edit_file", todayArgs, todayResult, atomicMeasurePrefix(t, "edit_file"), todayArgs + todayResult, 0}.String())
	t.Log(atomicMeasureSample{"1 x atomic_write ops", pairArgs, len(receipt), atomicMeasurePrefix(t, "atomic_write"), pairArgs + len(receipt), 0}.String())
	t.Logf("rounds: today=%d new=%d   lease acquisitions: today=%d new=%d", len(targets), 1, len(targets), 1)

	// One round and one lease acquisition, stated as an assertion so a change
	// that splits the transaction back into per-file calls fails here.
	if !strings.Contains(receipt, "3/3 committed") {
		t.Fatalf("the transaction did not commit all three: %q", receipt)
	}
	// The receipt is one bounded block, not three unbounded echoes.
	if len(receipt) > atomicReceiptBudgetBytes*4 {
		t.Errorf("ops receipt is %dB, over the bounded budget", len(receipt))
	}
	// The envelope's per-entry "mode" field makes the payload marginally LARGER
	// than three terse edit calls. Recorded honestly: the win is 2 fewer rounds
	// and 2 fewer lease acquisitions, not bytes.
	if pairArgs > todayArgs+96 {
		t.Errorf("ops args %dB cost more than %dB over three separate calls' %dB", pairArgs, 96, todayArgs)
	}
	t.Logf("payload overhead of the transaction envelope: %+dB (paid back in 2 rounds)", pairArgs-todayArgs)
}

// TestAtomicWriteMeasureReceiptBudget pins the write-side receipt shape for
// every mode: the provider-visible result stays bounded and never carries the
// file, so a successful write costs the same as a failed one.
func TestAtomicWriteMeasureReceiptBudget(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	ctx := observedContext()
	writer := atomicTestWriter(dir, nil)

	var big strings.Builder
	for i := range 2000 {
		fmt.Fprintf(&big, "line %04d with a realistic amount of content on it\n", i)
	}
	content := big.String()
	path := filepath.Join(dir, "big.txt")
	atomicWriteFile(t, path, content)
	if _, err := atomicCapture(ctx, nil, path); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"create", map[string]any{"path": "new.txt", "mode": "create", "content": content}},
		{"replace", map[string]any{"path": "big.txt", "mode": "replace", "content": content}},
		{"append", map[string]any{"path": "big.txt", "mode": "append", "content": "one more line\n"}},
		{"patch", map[string]any{"path": "big.txt", "mode": "patch", "edits": []map[string]string{{"old": "line 0001", "new": "LINE 0001"}}}},
		{"delete", map[string]any{"path": "big.txt", "mode": "delete"}},
	} {
		// Each mode is measured against a current observation, exactly as the
		// model would call it: the previous mode's write moved the file on.
		if _, err := atomicCapture(ctx, nil, path); err != nil {
			t.Fatal(err)
		}
		out, err := writer.Execute(ctx, argsJSON(t, tc.args))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		t.Log(atomicMeasureDist(tc.name+" receipt", []int{len(out)}))
		if len(out) > maxPostWriteReceiptBytes+atomicReceiptBudgetBytes {
			t.Errorf("%s receipt is %dB, over the bounded budget", tc.name, len(out))
		}
		if strings.Contains(out, "line 1500 with a realistic") {
			t.Errorf("%s receipt echoed the file:\n%s", tc.name, out)
		}
	}
}

// TestAtomicWriteMeasurePrefixBudget is §5.3 stated as a gate on the write side:
// the pair must cost less provider prefix than the trio it replaces.
func TestAtomicWriteMeasurePrefixBudget(t *testing.T) {
	trio := atomicMeasurePrefix(t, "read_file", "write_file", "edit_file")
	pair := atomicMeasurePrefix(t, "atomic_read", "atomic_write")
	t.Logf("trio (read_file+write_file+edit_file) = %dB", trio)
	t.Logf("pair (atomic_read+atomic_write)        = %dB", pair)
	t.Logf("net per member per turn                = %+dB", pair-trio)
	if pair >= trio {
		t.Fatalf("the atomic pair costs %dB, not less than the %dB trio it replaces", pair, trio)
	}
	// The write half alone must also fit the byte budget the route froze it at,
	// so a description that quietly grows is caught here.
	const frozenAtomicWriteBytes = 926
	got := atomicMeasurePrefix(t, "atomic_write")
	if got > frozenAtomicWriteBytes+16 {
		t.Errorf("atomic_write schema+description = %dB, over the frozen %dB budget", got, frozenAtomicWriteBytes)
	}
	t.Logf("atomic_write schema+description        = %dB (frozen budget %dB)", got, frozenAtomicWriteBytes)
}

// TestAtomicWriteMeasureModeArgs pins the argument shape of each mode against
// the shell form it replaces, so the "use instead of" claim in the description
// stays measurable rather than aspirational.
func TestAtomicWriteMeasureModeArgs(t *testing.T) {
	payload := strings.Repeat("evidence line with a realistic amount of detail\n", 20)

	rows := []struct {
		name  string
		shell int
		args  map[string]any
	}{
		{
			name:  "append vs cat >> heredoc",
			shell: len("cat >> evidence.md <<'EOF'\n" + payload + "EOF"),
			args:  map[string]any{"path": "evidence.md", "mode": "append", "content": payload},
		},
		{
			name:  "patch vs sed -i",
			shell: len(`sed -i 's/return 1/return 2/' big.go`),
			args:  map[string]any{"path": "big.go", "mode": "patch", "edits": []map[string]string{{"old": "return 1", "new": "return 2"}}},
		},
		{
			name:  "replace vs cat > heredoc",
			shell: len("cat > f.txt <<'EOF'\n" + payload + "EOF"),
			args:  map[string]any{"path": "f.txt", "mode": "replace", "content": payload},
		},
	}
	for _, row := range rows {
		got := atomicMeasureJSONSize(t, row.args)
		t.Log(atomicMeasureSample{row.name, got, 0, atomicMeasurePrefix(t, "atomic_write"), got, 0}.String())
		t.Logf("  shell form for the same job: %dB", row.shell)
		if got > row.shell+256 {
			t.Errorf("%s: tool args %dB are worse than the shell form's %dB by more than the JSON envelope", row.name, got, row.shell)
		}
	}
}
