package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Attribution is by role, and raw_content wins over the bounded wire content so
// a projection cannot undercount growth.
func TestAttributionSplitsByRole(t *testing.T) {
	p := writeTranscript(t,
		`{"role":"user","content":"12345"}`,
		`{"role":"assistant","content":"ab","reasoning_content":"cde"}`,
		`{"role":"tool","name":"read_file","content":"xx","raw_content":"xxxx"}`,
	)
	s, err := readSession(p, false)
	if err != nil {
		t.Fatal(err)
	}
	if s.AssistBytes != 5 {
		t.Fatalf("assistant bytes = %d, want 2 content + 3 reasoning", s.AssistBytes)
	}
	if s.ToolBytes != 2 {
		t.Fatalf("tool bytes = %d, want the 2-byte provider-visible content", s.ToolBytes)
	}
	// The other question, asked deliberately.
	local, err := readSession(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if local.ToolBytes != 4 {
		t.Fatalf("local-original bytes = %d, want the 4-byte raw_content", local.ToolBytes)
	}
	if s.Messages != 3 {
		t.Fatalf("messages = %d, want 3 (the user line counts as a message, not as growth)", s.Messages)
	}
}

// A one-off read is not fan-out; the same name at the threshold is.
func TestFanoutNeedsRepetitionOrDelegation(t *testing.T) {
	once := writeTranscript(t, `{"role":"tool","name":"read_file","content":"aaaa"}`)
	s, err := readSession(once, false)
	if err != nil {
		t.Fatal(err)
	}
	if s.FanoutBytes != 0 {
		t.Fatalf("a single read counted %d fan-out bytes", s.FanoutBytes)
	}
	lines := []string{}
	for range fanoutRepeats {
		lines = append(lines, `{"role":"tool","name":"grep","content":"aaaa"}`)
	}
	s, err = readSession(writeTranscript(t, lines...), false)
	if err != nil {
		t.Fatal(err)
	}
	if s.FanoutBytes != s.ToolBytes || s.ToolBytes == 0 {
		t.Fatalf("%d of %d bytes counted as fan-out at the threshold", s.FanoutBytes, s.ToolBytes)
	}
}

// A delegation result is fan-out shaped however few times it appears.
func TestDelegationResultIsAlwaysFanout(t *testing.T) {
	s, err := readSession(writeTranscript(t, `{"role":"tool","name":"fleet","content":"aaaaaa"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if s.FanoutBytes != 6 || s.DelegatedBytes != 6 {
		t.Fatalf("a direct fleet result: fanout=%d delegated=%d, want 6 and 6",
			s.FanoutBytes, s.DelegatedBytes)
	}
}

// A delegation reached through the capability channel is resolved by the
// capability_id, not by the calling tool's name — §13 P0's prose names this
// case and the bare name alone would miss it.
func TestCapabilityCallResolvesItsDelegationTarget(t *testing.T) {
	s, err := readSession(writeTranscript(t,
		`{"role":"assistant","tool_calls":[{"id":"c1","name":"use_capability","arguments":"{\"capability_id\":\"tool:task\"}"}]}`,
		`{"role":"tool","name":"use_capability","tool_call_id":"c1","content":"aaaaa"}`,
	), false)
	if err != nil {
		t.Fatal(err)
	}
	if s.DelegatedBytes != 5 {
		t.Fatalf("a tool:task call through use_capability delegated %d bytes, want 5", s.DelegatedBytes)
	}
	// The same shape pointing at a non-delegation tool must not count.
	s, err = readSession(writeTranscript(t,
		`{"role":"assistant","tool_calls":[{"id":"c1","name":"use_capability","arguments":"{\"capability_id\":\"session:list\"}"}]}`,
		`{"role":"tool","name":"use_capability","tool_call_id":"c1","content":"aaaaa"}`,
	), false)
	if err != nil {
		t.Fatal(err)
	}
	if s.DelegatedBytes != 0 {
		t.Fatalf("a non-delegation capability delegated %d bytes", s.DelegatedBytes)
	}
}

// The display-only sentinel is not a call, so it is neither a tool result nor
// anything else this harness counts.
func TestLocalOnlySentinelIsNotAToolResult(t *testing.T) {
	s, err := readSession(writeTranscript(t,
		`{"role":"tool","name":"__reasonix_local_only__","content":"aaaaaaaaaa"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if s.ToolBytes != 0 || s.ToolCounts["__reasonix_local_only__"] != 0 {
		t.Fatalf("the display-only sentinel was counted: tool=%d counts=%v", s.ToolBytes, s.ToolCounts)
	}
}

// The rule is the gate, stated over both conditions rather than either.
func TestShipsRequiresDominanceAndAbsorbability(t *testing.T) {
	cases := []struct {
		name             string
		tool, assist, fo int64
		want             bool
	}{
		{"a dominant and absorbable", 900, 100, 800, true},
		{"a dominant, under the gate", 900, 100, 700, false},
		{"b dominant though absorbable", 100, 900, 100, false},
		{"exactly at the gate", 1000, 0, 800, true},
	}
	for _, c := range cases {
		v := verdict{Sessions: 1, ToolBytes: c.tool, AssistBytes: c.assist, FanoutBytes: c.fo}
		if got := v.ships(); got != c.want {
			t.Fatalf("%s: ships=%v (share a=%.2f absorb=%.2f), want %v",
				c.name, got, v.shareA(), v.absorbable(), c.want)
		}
	}
}
