package agent

import (
	"fmt"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

// The read_file truncation contract, apart from the incomplete-read flow: the
// visible prefix is exactly full[:next_offset], so paging reconstructs the
// original with no gap and no duplicate — whatever the budget is set to.

func TestReadFileTruncationUsesContiguousUTF8PrefixAndExactRecovery(t *testing.T) {
	path := makeIncompleteReadFixture(t, "更新笔记助手.txt", 430, 96, 362, incompleteReadKeyRule)
	read := incompleteReadBuiltin(t)
	args := fmt.Sprintf(`{"path":%q}`, path)
	full := expectedReadOutput(t, read, args)
	if len(full) < 43*1024 {
		t.Fatalf("fixture bytes=%d, want a recoverable result of at least 43 KiB", len(full))
	}

	bounded, notice := truncateToolOutputFor(full, "read_file", "read-43k")
	if notice == "" || len(bounded) > maxToolOutputBytes || !strings.Contains(bounded, "next_offset=") {
		t.Fatalf("read_file was not bounded with a continuation cursor: bytes=%d notice=%q", len(bounded), notice)
	}
	offset, ok := readFileRecoveryOffset(bounded)
	if !ok || offset <= 0 || offset >= len(full) {
		t.Fatalf("recovery offset=%d ok=%v total=%d", offset, ok, len(full))
	}
	marker := strings.Index(bounded, "\n\n…[truncated tool=read_file ")
	if marker != offset || bounded[:marker] != full[:offset] {
		t.Fatalf("visible prefix is not exactly full[:next_offset]: marker=%d offset=%d", marker, offset)
	}
	if strings.Contains(bounded[:marker], incompleteReadKeyRule) {
		t.Fatal("key rule unexpectedly landed in the first prefix; fixture no longer exercises continuation")
	}

	// The invariant that survives any budget change: the visible prefix is
	// exactly full[:offset], so paging reconstructs the original.
	if got := full[:offset]; got != bounded[:marker] {
		t.Fatalf("the visible prefix no longer reconstructs from the cursor")
	}

	session := &Session{Messages: []provider.Message{{
		Role: provider.RoleTool, Name: "read_file", ToolCallID: "read-43k", Content: bounded, RawContent: full,
	}}}
	_, proxy := newToolResultCapabilityAgent(t, session)
	header, suffix, err := executeToolResultPage(t, proxy, "read-43k", toolResultRef("read-43k", full), offset, toolResultPageMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !header.Complete || header.Offset != offset || header.NextOffset != len(full) {
		t.Fatalf("unexpected terminal recovery header: %+v", header)
	}
	if got := full[:offset] + suffix; got != full {
		t.Fatalf("prefix + suffix did not reconstruct the original: got=%d want=%d", len(got), len(full))
	}
	if !strings.Contains(suffix, incompleteReadKeyRule) {
		t.Fatal("recovered suffix did not contain line 362's key rule")
	}
}
