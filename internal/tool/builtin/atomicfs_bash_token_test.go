package builtin

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"reasonix/internal/tool"
)

// Measures the atomic pair against the shell habits it replaces. The route
// document compares against the STRUCTURED trio, a different claim. Payloads
// are measured real-session sizes; token figures are labeled estimates.

// atomicEstimateTokens mirrors internal/agent's byte-based estimate so the
// numbers here are the ones the product itself would report.
func atomicEstimateTokens(bytes int) int {
	if bytes <= 0 {
		return 0
	}
	return (bytes + 3) / 4
}

// atomicBashPayload is one realistic shell job: the command text a model would
// emit, and the bytes the shell would return.
type atomicBashPayload struct {
	name    string
	command string
	result  int
}

// atomicRealisticHeredoc builds a heredoc whose body matches a measured
// real-session size, rather than a stub that flatters the shell.
func atomicRealisticHeredoc(dir, path string, bodyBytes int) string {
	var body strings.Builder
	for body.Len() < bodyBytes {
		body.WriteString("    // a line of the file being written, at a realistic width\n")
	}
	return fmt.Sprintf("cat > %s <<'EOF'\n%sEOF", filepath.Join(dir, path), body.String())
}

// atomicRealisticPythonEdit builds the other common write shape: read the whole
// file into memory, mutate it, write it back — the read-modify-write that has no
// anchor and is why the pair exists.
func atomicRealisticPythonEdit(dir, path string, scriptBytes int) string {
	script := fmt.Sprintf(`import re
p = %q
s = open(p).read()
# ---- the part that actually changes the file ----
s = s.replace("return 1", "return 2")
open(p, "w").write(s)
`, filepath.Join(dir, path))
	for len(script) < scriptBytes {
		script += "# padding so this matches a measured real-session command\n"
	}
	return "cd " + dir + " && python3 - <<'PY'\n" + script + "PY"
}

// TestAtomicVsBashTokenComparison is the headline comparison. Every row is a
// real job rendered both ways; the shell rows use measured real-session sizes.
func TestAtomicVsBashTokenComparison(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	ctx := atomicTestContext()

	// A 2000-line source file with a five-line function to change in the middle.
	var big strings.Builder
	big.WriteString("package big\n\n")
	for i := 0; i < 1990; i++ {
		big.WriteString(fmt.Sprintf("// filler line %04d with some words to make it a realistic width\n", i))
	}
	big.WriteString("\nfunc Target() int {\n\treturn 1\n}\n")
	path := filepath.Join(dir, "big.go")
	atomicWriteFile(t, path, big.String())
	wholeFile := len(big.String())

	// The anchors both sides need before they can change anything.
	readWindowArgs := atomicMeasureJSONSize(t, map[string]any{"path": "big.go", "mode": "window", "offset": 1985, "limit": 20})
	readWindowResult := atomicReadToolResultBytes(t, ctx, dir, map[string]any{"path": "big.go", "mode": "window", "offset": 1985, "limit": 20})
	bashPeek := fmt.Sprintf("cd %s && sed -n '1985,2005p' big.go", dir)
	bashPeekResult := readWindowResult // the same lines come back either way

	patchArgs := atomicMeasureJSONSize(t, map[string]any{"path": "big.go", "mode": "patch",
		"edits": []map[string]string{{"old": "return 1", "new": "return 2"}}})
	patchResult := len("patch big.go 22B 3 lines 1995→1995 lines")

	// The shell write, at each measured real-session size.
	heredocMedian := atomicRealisticHeredoc(dir, "big.go", 976)
	heredocP90 := atomicRealisticHeredoc(dir, "big.go", 2610)
	pythonEdit := atomicRealisticPythonEdit(dir, "big.go", 11671)

	prefixPair := atomicMeasurePrefix(t, "atomic_read", "atomic_write")
	prefixBash := atomicMeasurePrefix(t, "bash")

	type row struct {
		name                string
		readArgs, readRes   int
		writeArgs, writeRes int
		prefix              int
	}
	rows := []row{
		{"bash heredoc (median 976B body)", len(bashPeek), bashPeekResult, len(heredocMedian), 0, prefixBash},
		{"bash heredoc (p90 2610B body)", len(bashPeek), bashPeekResult, len(heredocP90), 0, prefixBash},
		{"bash python read-modify-write", len(bashPeek), bashPeekResult, len(pythonEdit), 120, prefixBash},
		{"atomic pair (window + patch)", readWindowArgs, readWindowResult, patchArgs, patchResult, prefixPair},
	}

	t.Logf("fixture: %d-line file, %d bytes", atomicLineCount([]byte(big.String())), wholeFile)
	t.Logf("%-34s %8s %8s %9s %9s %10s", "shape", "args", "result", "call", "tok(est)", "+prefix")
	var prev int
	for i, r := range rows {
		call := r.readArgs + r.readRes + r.writeArgs + r.writeRes
		t.Logf("%-34s %8d %8d %9d %9d %10d", r.name, r.readArgs+r.writeArgs, r.readRes+r.writeRes, call, atomicEstimateTokens(call), call+r.prefix)
		if i > 0 {
			t.Logf("%-34s %8s %8s %9s %9s %+9d%%", "  vs previous", "", "", "", "", (call-prev)*100/max(prev, 1))
		}
		prev = call
	}

	atomic := rows[len(rows)-1]
	atomicCall := atomic.readArgs + atomic.readRes + atomic.writeArgs + atomic.writeRes
	for _, shell := range rows[:len(rows)-1] {
		shellCall := shell.readArgs + shell.readRes + shell.writeArgs + shell.writeRes
		t.Logf("atomic is %+d%% vs %s (%dB vs %dB)", (atomicCall-shellCall)*100/max(shellCall, 1), shell.name, atomicCall, shellCall)
	}

	// The claim this file makes: the pair is cheaper than the shell habits at
	// every measured payload size, counting the read BOTH sides need.
	for _, shell := range rows[:len(rows)-1] {
		shellCall := shell.readArgs + shell.readRes + shell.writeArgs + shell.writeRes
		if atomicCall >= shellCall {
			t.Errorf("%s: atomic pair costs %dB, not less than the shell form's %dB", shell.name, atomicCall, shellCall)
		}
	}

	// The symbol locator's row (§6.2): same file and function as the pair above,
	// but the model already knows which symbol to change, so no numbered window
	// has to come back first — that window is the only saving measured here.
	symbolArgs := atomicMeasureJSONSize(t, map[string]any{
		"path": "big.go", "mode": "patch", "symbol": "Target",
		"content": "func Target() int {\n\treturn 2\n}\n",
	})
	symbolWriter := atomicWrite{workDir: dir, roots: realRoots([]string{dir})}
	symbolReceipt, err := symbolWriter.Execute(ctx, argsJSON(t, map[string]any{
		"path": "big.go", "mode": "patch", "symbol": "Target",
		"content": "func Target() int {\n\treturn 2\n}\n",
	}))
	if err != nil {
		t.Fatalf("symbol patch: %v", err)
	}
	symbolCall := symbolArgs + len(symbolReceipt)
	t.Logf("%-34s %8d %8d %9d %9d %10d", "atomic symbol patch (no read)", symbolArgs, len(symbolReceipt), symbolCall, atomicEstimateTokens(symbolCall), symbolCall+prefixPair)
	t.Logf("symbol patch is %+d%% vs the window+patch pair (%dB vs %dB); the saved bytes are exactly the window",
		(symbolCall-atomicCall)*100/max(atomicCall, 1), symbolCall, atomicCall)
	// Byte-pinned gate: locating by symbol must cost less than locating by a
	// window read plus an edit anchor, and the receipt must never carry either the
	// replaced body or the new content back into the context.
	if symbolCall >= atomicCall {
		t.Errorf("symbol patch costs %dB, not less than the window+patch pair's %dB", symbolCall, atomicCall)
	}
	for _, leak := range []string{"return 1", "return 2"} {
		if strings.Contains(symbolReceipt, leak) {
			t.Errorf("the symbol receipt echoed content (%q):\n%s", leak, symbolReceipt)
		}
	}
}

// TestAtomicVsBashAppendAndMultiFile records the two jobs where the pair does
// NOT win on bytes. Both are honest losses: the append carries the same payload
// with more quoting, and ops adds a mode field per entry in exchange for turns.
// A comparison that omitted them would be picking its rows.
func TestAtomicVsBashAppendAndMultiFile(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	ctx := atomicTestContext()
	reader := atomicReadTool(t, dir)
	atomicWriteFile(t, filepath.Join(dir, "evidence.md"), "# Evidence\n")
	atomicWriteFile(t, filepath.Join(dir, "a.go"), "package a\n")
	atomicWriteFile(t, filepath.Join(dir, "b.go"), "package b\n")
	atomicWriteFile(t, filepath.Join(dir, "c.go"), "package c\n")

	// Append: 20 lines of evidence, measured real-session heredoc size.
	payload := strings.Repeat("evidence line with a realistic amount of detail\n", 20)
	heredoc := fmt.Sprintf("cat >> evidence.md <<'EOF'\n%sEOF", payload)
	appendArgs := atomicMeasureJSONSize(t, map[string]any{"path": "evidence.md", "mode": "append", "content": payload})
	if _, err := reader.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "evidence.md"})); err != nil {
		t.Fatal(err)
	}
	writer := atomicWrite{workDir: dir, roots: realRoots([]string{dir})}
	receipt, err := writer.Execute(ctx, atomicReadArgs(t, map[string]any{"path": "evidence.md", "mode": "append", "content": payload}))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%-34s args=%5d result=%4d call=%5d tok(est)=%4d", "bash cat >> heredoc", len(heredoc), 0, len(heredoc), atomicEstimateTokens(len(heredoc)))
	t.Logf("%-34s args=%5d result=%4d call=%5d tok(est)=%4d", "atomic_write append", appendArgs, len(receipt), appendArgs+len(receipt), atomicEstimateTokens(appendArgs+len(receipt)))
	t.Logf("append is %+dB vs the heredoc — recorded as a LOSS, not a win", appendArgs+len(receipt)-len(heredoc))

	// Multi-file: three edits in one turn, versus one transaction.
	var bashArgs int
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		bashArgs += len(fmt.Sprintf("cd %s && sed -i 's/package %s/package %s/' %s\n", dir, name[:1], strings.ToUpper(name[:1]), name))
	}
	ops := make([]map[string]any, 0, 3)
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		ops = append(ops, map[string]any{"path": name, "mode": "patch",
			"edits": []map[string]string{{"old": "package " + name[:1], "new": "package " + strings.ToUpper(name[:1])}}})
	}
	opsArgs := atomicMeasureJSONSize(t, map[string]any{"ops": ops})
	// The three bash calls are three model turns; ops is one. Count both the
	// bytes and the turns, because turns are the expensive unit.
	t.Logf("%-34s args=%5d turns=3 leases=3 tok(est)=%4d", "bash 3x sed -i", bashArgs, atomicEstimateTokens(bashArgs))
	t.Logf("%-34s args=%5d turns=1 leases=1 tok(est)=%4d", "atomic_write ops (3 files)", opsArgs, atomicEstimateTokens(opsArgs))
	t.Logf("ops is %+dB in arguments, but 2 fewer turns and 2 fewer lease acquisitions", opsArgs-bashArgs)
}

// TestAtomicVsBashReadShapes measures the read half on its own, since that is
// where the pair's advantage is largest and where bash has no equivalent at all
// (`sed -n` is a window read with no anchor and no audit story).
func TestAtomicVsBashReadShapes(t *testing.T) {
	atomicResetSnapshotCache()
	dir := t.TempDir()
	ctx := atomicTestContext()

	var big strings.Builder
	big.WriteString("package big\n\n")
	for i := 0; i < 1500; i++ {
		big.WriteString(fmt.Sprintf("func Handler%04d(ctx context.Context) error {\n\treturn nil\n}\n\n// section %04d with a realistic comment width\n", i, i))
	}
	atomicWriteFile(t, filepath.Join(dir, "big.go"), big.String())
	whole := len(big.String())

	reader := atomicReadTool(t, dir)
	type shape struct {
		name string
		args map[string]any
	}
	shapes := []shape{
		{"atomic_read auto", map[string]any{"path": "big.go"}},
		{"atomic_read outline", map[string]any{"path": "big.go", "mode": "outline"}},
		{"atomic_read window 100", map[string]any{"path": "big.go", "mode": "window", "offset": 100, "limit": 100}},
		{"atomic_read tail", map[string]any{"path": "big.go", "mode": "tail"}},
	}
	t.Logf("fixture: %d bytes whole file", whole)
	var sizes []int
	var windowOut string
	for _, s := range shapes {
		out, err := reader.Execute(ctx, atomicReadArgs(t, s.args))
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if s.name == "atomic_read window 100" {
			windowOut = out
		}
		sizes = append(sizes, len(out))
		t.Logf("%-30s result=%6dB tok(est)=%5d  (%.1f%% of the whole file)",
			s.name, len(out), atomicEstimateTokens(len(out)), float64(len(out))*100/float64(whole))
	}
	// bash's equivalents: cat the whole file, or sed the same window (computed
	// from the delivered lines, minus the reader's "N→" numbering).
	catResult := whole // cat emits the file verbatim
	windowResult := len(windowOut)
	windowLines := strings.Split(strings.TrimRight(windowOut, "\n"), "\n")
	sedWindowResult := 0
	for _, line := range windowLines {
		if arrow := strings.Index(line, "→"); arrow > 0 {
			line = line[arrow+len("→"):]
		}
		sedWindowResult += len(line) + 1
	}
	t.Logf("%-30s result=%6dB tok(est)=%5d  (100%% of the whole file)", "bash cat big.go", catResult, atomicEstimateTokens(catResult))
	t.Logf("%-30s result=%6dB tok(est)=%5d  (same window, unnumbered)", "bash sed -n window", sedWindowResult, atomicEstimateTokens(sedWindowResult))
	t.Logf("window read: atomic=%dB vs bash sed=%dB (%+d%%) — near parity; both return the same lines, and",
		windowResult, sedWindowResult, (windowResult-sedWindowResult)*100/max(sedWindowResult, 1))
	t.Logf("the reader's extra bytes are the line numbers that make the window citable.")
	t.Logf("whole-file read: atomic auto=%dB vs bash cat=%dB — the pair's win is refusing to return the file",
		sizes[0], catResult)

	sorted := append([]int(nil), sizes...)
	sort.Ints(sorted)
	if sorted[len(sorted)-1] > atomicReadBudgetBytes {
		t.Errorf("a read shape returned %dB, over the %d budget", sorted[len(sorted)-1], atomicReadBudgetBytes)
	}
}

// TestAtomicVsBashPrefixCost states the per-turn surface cost, which is paid on
// EVERY turn regardless of what the model calls. bash stays on both surfaces, so
// this is a 4-tool to 3-tool swap, not 4 to 2.
func TestAtomicVsBashPrefixCost(t *testing.T) {
	trio := atomicMeasurePrefix(t, "read_file", "write_file", "edit_file")
	pair := atomicMeasurePrefix(t, "atomic_read", "atomic_write")
	bash := atomicMeasurePrefix(t, "bash")
	t.Logf("bash alone                     = %5dB (%4d tok est)", bash, atomicEstimateTokens(bash))
	t.Logf("today: trio + bash             = %5dB (%4d tok est)", trio+bash, atomicEstimateTokens(trio+bash))
	t.Logf("new:   pair + bash             = %5dB (%4d tok est)", pair+bash, atomicEstimateTokens(pair+bash))
	t.Logf("per-turn delta                 = %+5dB (%+4d tok est)", pair-trio, atomicEstimateTokens(pair)-atomicEstimateTokens(trio))
	t.Logf("NOTE: this is a provider-surface narrowing. bash is on BOTH surfaces,")
	t.Logf("      so the delta is trio->pair, and no bash schema is removed.")
	if pair >= trio {
		t.Fatalf("the pair costs %dB, not less than the %dB trio it replaces", pair, trio)
	}
}

// TestAtomicVsBashBashIsNotRemoved pins the premise the whole comparison rests
// on: the team surface keeps bash. If a future change removed it, "the atomic
// pair replaces bash" would become a different (and untested) claim.
func TestAtomicVsBashBashIsNotRemoved(t *testing.T) {
	if _, ok := tool.LookupBuiltin("bash"); !ok {
		t.Fatal("bash must stay registered; the pair narrows the file tools, not the shell")
	}
	// And the shell schema is untouched by the swap.
	bashTool, _ := tool.LookupBuiltin("bash")
	if len(bashTool.Schema()) == 0 {
		t.Fatal("bash schema is empty")
	}
	t.Logf("bash stays on the team surface: schema %dB + description %dB",
		len(bashTool.Schema()), len(bashTool.Description()))
}
