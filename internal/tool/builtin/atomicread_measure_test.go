package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"reasonix/internal/tool"
)

// The measurement probe behind §5.4: it renders one job three ways — the shell
// habit, the existing structured tools, the atomic pair — and asserts budgets
// rather than absolute bytes, so a shape regression still fails loudly.

// atomicMeasureSample is one measured shape: the prefix a tool surface pays per
// turn, the call arguments, and the result.
type atomicMeasureSample struct {
	name    string
	args    int
	result  int
	prefix  int
	total   int
	perTurn int
}

func (s atomicMeasureSample) String() string {
	return fmt.Sprintf("%-34s prefix=%5dB args=%5dB result=%6dB call=%6dB", s.name, s.prefix, s.args, s.result, s.total)
}

// atomicMeasurePrefix measures the provider prefix one tool surface costs per
// turn: the schema plus the description of every tool on it.
func atomicMeasurePrefix(t *testing.T, names ...string) int {
	t.Helper()
	total := 0
	for _, name := range names {
		tl, ok := tool.LookupBuiltin(name)
		if !ok {
			t.Fatalf("built-in %q is not registered", name)
		}
		total += len(tl.Schema()) + len(tl.Description())
	}
	return total
}

func atomicMeasureDist(name string, samples []int) string {
	if len(samples) == 0 {
		return name + ": (no samples)"
	}
	sorted := append([]int(nil), samples...)
	sort.Ints(sorted)
	median := sorted[len(sorted)/2]
	p90 := sorted[min(len(sorted)-1, (len(sorted)*9)/10)]
	sum := 0
	for _, v := range sorted {
		sum += v
	}
	return fmt.Sprintf("%s: median=%dB p90=%dB max=%dB sum=%dB", name, median, p90, sorted[len(sorted)-1], sum)
}

// TestAtomicReadMeasureWorkedExample1 measures the most common team job: change
// a few lines inside a large file.
func TestAtomicReadMeasureWorkedExample1(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	ctx := atomicTestContext()
	path := filepath.Join(dir, "big.go")

	// A 2000-line file with a five-line function to change in the middle.
	var b strings.Builder
	b.WriteString("package big\n\n")
	for i := 0; i < 1990; i++ {
		b.WriteString(fmt.Sprintf("// filler line %04d with some words to make it a realistic width\n", i))
	}
	b.WriteString("\nfunc Target() int {\n\treturn 1\n}\n")
	atomicWriteFile(t, path, b.String())

	// Today (bash): sed -n to peek, then a python heredoc to rewrite.
	bashArgs := len(fmt.Sprintf(`cd %s && sed -n '1991,2000p' big.go && python3 - <<'PY'
import re
p='big.go'
s=open(p).read()
s=s.replace('return 1','return 2')
open(p,'w').write(s)
PY`, dir))
	bashResult := len(fmt.Sprintf("func Target() int {\n\treturn 1\n}\n")) + 120 // plus the interpreter's chatter

	// Today (structured): read_file the whole file (the model does not know
	// which lines matter yet), then write_file the whole file back.
	readArgs := atomicMeasureJSONSize(t, map[string]any{"path": "big.go"})
	readResult := atomicReadToolResultBytesFor(t, ctx, dir, map[string]any{"path": "big.go"})
	writeArgs := atomicMeasureJSONSize(t, map[string]any{"path": "big.go", "content": b.String()})
	writeResult := len("wrote 48123 bytes to big.go")

	// New: atomic_read window, then atomic_write patch. The arguments are
	// serialized from the real objects rather than counted from a literal, so
	// the row cannot drift away from the call it claims to measure.
	pairReadArgs := atomicMeasureJSONSize(t, map[string]any{"path": "big.go", "mode": "window", "offset": 1985, "limit": 20})
	pairReadResult := atomicReadToolResultBytes(t, ctx, dir, map[string]any{"path": "big.go", "mode": "window", "offset": 1985, "limit": 20})
	pairWriteArgs := atomicMeasureJSONSize(t, map[string]any{"path": "big.go", "mode": "patch",
		"edits": []map[string]string{{"old": "return 1", "new": "return 2"}}})
	pairWriteResult := len("patch big.go 22B 3 lines 1995→1995 lines")

	prefixToday := atomicMeasurePrefix(t, "read_file", "write_file", "edit_file", "bash")
	prefixNew := atomicMeasurePrefix(t, "atomic_read", "atomic_write")

	rows := []atomicMeasureSample{
		{"bash peek + heredoc rewrite", bashArgs, bashResult, prefixToday, bashArgs + bashResult, 0},
		{"read_file + write_file", readArgs + writeArgs, readResult + writeResult, prefixToday, readArgs + writeArgs + readResult + writeResult, 0},
		{"atomic_read + atomic_write", pairReadArgs + pairWriteArgs, pairReadResult + pairWriteResult, prefixNew, pairReadArgs + pairWriteArgs + pairReadResult + pairWriteResult, 0},
	}
	for _, row := range rows {
		t.Log(row.String())
	}
	t.Logf("per-turn prefix: today=%dB (%s) new=%dB (%s) delta=%+dB",
		prefixToday, "read_file+write_file+edit_file+bash", prefixNew, "atomic_read+atomic_write", prefixNew-prefixToday)

	// The result is where the saving is: a whole-file read versus a window.
	// The window is the point of the tool, so this is the assertion that keeps
	// mode=window from drifting into a whole-file read.
	if pairReadResult*100 > readResult*40 {
		t.Errorf("atomic window read %dB is not at most 40%% of the whole-file read %dB", pairReadResult, readResult)
	}
	// And the patch arguments must be a small fraction of the whole-file rewrite.
	if pairWriteArgs*100 > writeArgs*10 {
		t.Errorf("patch args %dB are not at most 10%% of the whole-file write args %dB", pairWriteArgs, writeArgs)
	}
}

// TestAtomicReadMeasureWorkedExample3 measures the append shape: 20 lines of
// evidence. The byte win here is small — the interesting property is that the
// payload is bounded and the result never echoes the file.
func TestAtomicReadMeasureWorkedExample3(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	ctx := atomicTestContext()
	path := filepath.Join(dir, "evidence.md")
	atomicWriteFile(t, path, "# Evidence\n")

	payload := strings.Repeat("evidence line with a realistic amount of detail\n", 20)

	// The heredoc's payload is the same bytes the tool carries; only the shell
	// scaffolding differs, which is what the two rows are meant to compare.
	bashArgs := len(fmt.Sprintf(`cat >> evidence.md <<'EOF'
%sEOF`, payload))
	bashResult := 0

	pairArgs := atomicMeasureJSONSize(t, map[string]any{"path": "evidence.md", "mode": "append", "content": payload})
	reader := atomicReadTool(t, dir)
	if _, err := reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "evidence.md"})); err != nil {
		t.Fatal(err)
	}
	writer := atomicWrite{workDir: dir, roots: realRoots([]string{dir})}
	receipt, err := writer.Execute(ctx, atomicReadArgs(t, map[string]any{
		"path": "evidence.md", "mode": "append", "content": payload,
	}))
	if err != nil {
		t.Fatal(err)
	}
	pairResult := len(receipt)

	t.Log(atomicMeasureSample{"bash cat >> heredoc", bashArgs, bashResult, atomicMeasurePrefix(t, "bash"), bashArgs + bashResult, 0}.String())
	t.Log(atomicMeasureSample{"atomic_write append", pairArgs, pairResult, atomicMeasurePrefix(t, "atomic_write"), pairArgs + pairResult, 0}.String())

	// The receipt must be a bounded line, never the file.
	if pairResult > atomicReceiptBudgetBytes {
		t.Errorf("append receipt is %dB, over the %d budget", pairResult, atomicReceiptBudgetBytes)
	}
	if strings.Contains(receipt, "evidence line") {
		t.Errorf("the append receipt echoed the payload: %q", receipt)
	}
	// The shell form and the tool form carry the same payload, so the bytes are
	// close by construction; the tool's win is the lease scope and the audit
	// story, not the payload. Assert only that it is not worse.
	if pairArgs > bashArgs+64 {
		t.Errorf("append args %dB are worse than the heredoc's %dB by more than quoting overhead", pairArgs, bashArgs)
	}
}

// TestAtomicReadMeasureBudgets asserts the shape of every read mode against a
// budget, so a regression that turns a bounded mode into a whole-file read
// fails here rather than in a token bill. The baselines are what the equivalent
// read_file call would deliver for the same job.
func TestAtomicReadMeasureBudgets(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	ctx := atomicTestContext()

	// The two fixtures §5.4 names: a small file and a large one.
	smallText := strings.Repeat("small fixture line\n", 100)
	atomicWriteFile(t, filepath.Join(dir, "small.txt"), smallText)

	var big strings.Builder
	big.WriteString("package big\n\n")
	for i := 0; i < 1500; i++ {
		big.WriteString(fmt.Sprintf("func Handler%04d(ctx context.Context) error {\n\treturn nil\n}\n\n// section %04d with a realistic comment width\n", i, i))
	}
	largeText := big.String()
	atomicWriteFile(t, filepath.Join(dir, "large.go"), largeText)

	reader := atomicReadTool(t, dir)
	// read_file with no window delivers the whole file; a window delivers the
	// numbered window plus its paging hint.
	wholeFile := len(largeText)
	windowBaseline := atomicReadToolResultBytesFor(t, ctx, dir, map[string]any{"path": "large.go", "offset": 0, "limit": 100})

	for _, tc := range []struct {
		name     string
		args     map[string]any
		baseline int
		// ratio is the fraction of the baseline this mode may spend. A whole
		// file (auto on a small file) is allowed to be slightly larger than the
		// raw bytes because every line carries a number.
		ratio float64
	}{
		{"auto (small, whole file)", map[string]any{"path": "small.txt"}, len(smallText), 1.6},
		{"auto (large, outline+head)", map[string]any{"path": "large.go"}, wholeFile, 0.15},
		{"outline (large)", map[string]any{"path": "large.go", "mode": "outline"}, wholeFile, 0.05},
		{"window 100 (large)", map[string]any{"path": "large.go", "mode": "window", "offset": 100, "limit": 100}, windowBaseline, 1.2},
		{"tail (large)", map[string]any{"path": "large.go", "mode": "tail"}, windowBaseline, 1.2},
	} {
		out, err := reader.Execute(ctx, atomicReadArgs(t, tc.args))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		t.Log(atomicMeasureDist(tc.name, []int{len(out)}))
		if len(out) > atomicReadBudgetBytes {
			t.Errorf("%s returned %dB, over the %d budget", tc.name, len(out), atomicReadBudgetBytes)
		}
		if float64(len(out)) > float64(tc.baseline)*tc.ratio {
			t.Errorf("%s returned %dB, more than %.0f%% of the %dB baseline it replaces",
				tc.name, len(out), tc.ratio*100, tc.baseline)
		}
	}
	t.Logf("large fixture: whole file %dB, read_file window %dB", wholeFile, windowBaseline)
}

// TestAtomicReadMeasurePrefixBudget pins §5.3: the atomic pair must cost less
// prefix than the trio it replaces on the team surface, or the substitution
// would be a net token increase.
func TestAtomicReadMeasurePrefixBudget(t *testing.T) {
	trio := atomicMeasurePrefix(t, "read_file", "write_file", "edit_file")
	pair := atomicMeasurePrefix(t, "atomic_read", "atomic_write")
	t.Logf("trio (read_file+write_file+edit_file) = %dB", trio)
	t.Logf("pair (atomic_read+atomic_write)        = %dB", pair)
	t.Logf("net per member per turn                = %+dB", pair-trio)
	if pair >= trio {
		t.Fatalf("the atomic pair costs %dB, not less than the %dB trio it replaces", pair, trio)
	}
}

// atomicReadToolResultBytes measures the atomic reader's own result for one
// call.
func atomicReadToolResultBytes(t *testing.T, ctx context.Context, dir string, args map[string]any) int {
	t.Helper()
	out, err := atomicReadTool(t, dir).Execute(ctx, argsJSON(t, args))
	if err != nil {
		t.Fatalf("atomic_read %v: %v", args, err)
	}
	return len(out)
}

// atomicReadToolResultBytesFor measures what read_file would deliver for the
// same job, so a budget can be stated against the tool this one replaces.
func atomicReadToolResultBytesFor(t *testing.T, ctx context.Context, dir string, args map[string]any) int {
	t.Helper()
	out, err := readFile{workDir: dir}.Execute(ctx, argsJSON(t, args))
	if err != nil {
		t.Fatalf("read_file %v: %v", args, err)
	}
	return len(out)
}

// atomicMeasureJSONSize reports the serialized size of an args object, so the
// probe measures what the model would actually emit.
func atomicMeasureJSONSize(t *testing.T, v any) int {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return len(raw)
}
