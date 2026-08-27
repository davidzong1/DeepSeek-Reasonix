package cli

import (
	"strings"
	"testing"
)

func TestToolCard(t *testing.T) {
	cases := []struct {
		name string
		args string
		want []string
		deny []string
	}{
		{"bash", `{"command":"npm test"}`, []string{"Bash", "npm test"}, nil},
		{"read_file", `{"path":"pkg/a.go"}`, []string{"Read", "pkg/a.go"}, nil},
		{"grep", `{"pattern":"TODO","path":"."}`, []string{"Search", "TODO"}, nil},
		{"wait", `{"job_ids":["bash-1","bash-2"],"timeout_seconds":300}`, []string{"Wait", "bash-1", "bash-2"}, []string{"timeout_seconds", "300", "job_ids"}},
		{"web_fetch", `{"url":"https://x.dev"}`, []string{"Fetch", "https://x.dev"}, nil},
		{"web_search", `{"query":"latest release"}`, []string{"Search", "latest release"}, nil},
		{"use_capability", `{"action":"call","capability_id":"mcp-tool:github/search_issues","arguments":{"query":"bug"}}`, []string{"MCP", "mcp-tool:github/search_issues"}, []string{`"arguments"`, `"query"`, "bug"}},
		{"use_capability", `{"action":"list"}`, []string{"Capability", "list"}, []string{"action", "MCP"}},
	}
	for _, c := range cases {
		got := toolCard(c.name, c.args, 120)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q missing %q", c.name, got, w)
			}
		}
		for _, d := range c.deny {
			if strings.Contains(got, d) {
				t.Errorf("%s: %q should not contain raw arg %q", c.name, got, d)
			}
		}
	}
}

func TestToolCardUnknownFallsBackToName(t *testing.T) {
	if got := toolCard("frobnicate", `{}`, 80); !strings.Contains(got, "frobnicate") {
		t.Errorf("unknown tool should show its raw name, got %q", got)
	}
}

// TestToolCardLocalTaskVsExternalMCP pins the split between the local task
// tool and an external MCP tool: the builtin shows its own verb and argument,
// an mcp__ server tool shows only its raw short name.
func TestToolCardLocalTaskVsExternalMCP(t *testing.T) {
	local := toolCard("task", `{"description":"delegate review"}`, 120)
	for _, want := range []string{"Task", "delegate review"} {
		if !strings.Contains(local, want) {
			t.Errorf("local task card missing %q, got %q", want, local)
		}
	}
	mcp := toolCard("mcp__github__search_issues", `{"query":"x"}`, 120)
	if !strings.Contains(mcp, "search_issues") || strings.Contains(mcp, "github__") {
		t.Errorf("external MCP card should show the tool short name, got %q", mcp)
	}
}

// TestToolCardCapabilityClassification pins the use_capability card label to
// its target's namespace: real MCP ids keep the MCP label, local sub-agent and
// fleet/parallel task ids get their own labels, and every other local
// capability (tool:, skill:) plus target-less calls is a plain Capability.
func TestToolCardCapabilityClassification(t *testing.T) {
	cases := []struct {
		args string
		want string // the card verb
		deny []string
	}{
		{`{"capability_id":"task:subagent","arguments":{"prompt":"x"}}`, "Sub-agent", []string{"MCP"}},
		{`{"capability_id":"task:read_only_subagent","arguments":{"prompt":"x"}}`, "Sub-agent", []string{"MCP"}},
		{`{"capability_id":"task:fleet","arguments":{"tasks":[]}}`, "Fleet", []string{"MCP"}},
		{`{"capability_id":"task:parallel_tasks","arguments":{"tasks":[]}}`, "Fleet", []string{"MCP"}},
		{`{"capability_id":"tool:task","arguments":{"prompt":"x"}}`, "Capability", []string{"MCP"}},
		{`{"capability_id":"tool:grep","arguments":{"pattern":"x"}}`, "Capability", []string{"MCP"}},
		{`{"capability_id":"skill:review","arguments":{}}`, "Capability", []string{"MCP"}},
		{`{"capability_id":"mcp-tool:github/search_issues","arguments":{}}`, "MCP", nil},
		{`{"capability_id":"mcp-server:github","action":"call"}`, "MCP", nil},
		{`{"action":"list"}`, "Capability", []string{"MCP"}},
		{`{"action":"decline","reason":"no"}`, "Capability", []string{"MCP"}},
		{`not json`, "Capability", []string{"MCP"}},
	}
	for _, c := range cases {
		got := toolCard("use_capability", c.args, 120)
		if !strings.Contains(got, c.want) {
			t.Errorf("card %q: missing label %q in %q", c.args, c.want, got)
		}
		for _, d := range c.deny {
			if strings.Contains(got, d) {
				t.Errorf("card %q: should not be labeled %q, got %q", c.args, d, got)
			}
		}
	}
}
