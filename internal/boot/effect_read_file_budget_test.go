package boot

// A provider-visible tool-result cap is cache-sensitive, so it belongs at this
// boundary: the prefix must not move, and the body must still be bounded. The
// body is read off the tool result's Content — ProviderVisible is unrelated.

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

const effectReadFileProviderKind = "boot-effect-read-file-budget"

// effectReadFileTurns is the script: the model asks for the file, then answers.
// Two turns is what a tool-calling turn needs, and the second is what returns
// the tool result to the provider boundary where this test reads it.
func effectReadFileTurns(path string) []testutil.Turn {
	return []testutil.Turn{
		{ToolCalls: []provider.ToolCall{{ID: "r1", Name: "read_file", Arguments: `{"path":"` + path + `"}`}}},
		{Text: "read it"},
	}
}

// effectReadFileTool is a boot-local stand-in for the real file reader: it
// returns a body far over any preview budget, so the truncation the tool result
// passes through is the only thing under test.
type effectReadFileTool struct{ body string }

func (t *effectReadFileTool) Name() string { return "read_file" }
func (t *effectReadFileTool) Description() string {
	return "Read a file from the workspace."
}
func (t *effectReadFileTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
}
func (t *effectReadFileTool) ReadOnly() bool { return true }
func (t *effectReadFileTool) Execute(context.Context, json.RawMessage) (string, error) {
	return t.body, nil
}

func effectReadFileStage(t *testing.T) *testutil.MockProvider {
	t.Helper()
	isolateConfigHome(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	rec := testutil.NewMock("effect-read-file")
	provider.Register(effectReadFileProviderKind, func(provider.Config) (provider.Provider, error) {
		return rec, nil
	})
	writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[environment]
enabled = false

[[providers]]
name = "test-model"
kind = "`+effectReadFileProviderKind+`"
model = "x"
`)
	return rec
}

// TestEffectReadFilePreviewStaysBoundedAndKeepsThePrefixStable drives one real
// turn through a provider that asks for read_file, twice, and reads the tool
// message the model actually received.
func TestEffectReadFilePreviewStaysBoundedAndKeepsThePrefixStable(t *testing.T) {
	// The exact budget is pinned in-package. What this boundary owns is that a
	// bound is applied, the cursor survives it, and the prefix does not move.
	body := strings.Repeat("line of source text\n", 4000) // ~80 KiB, over any budget
	rec := effectReadFileStage(t)
	readTool := &effectReadFileTool{body: body}

	first := effectRunWithReadFile(t, rec, readTool, effectReadFileTurns("src/big.go"))
	second := effectRunWithReadFile(t, rec, readTool, effectReadFileTurns("src/big.go"))

	got := effectToolResultBody(t, first)
	if got == "" {
		t.Fatal("no tool result reached the provider, so the case proves nothing")
	}
	if len(got) >= len(body)/2 {
		t.Fatalf("read_file preview is %d of %d bytes; no bound was applied", len(got), len(body))
	}
	if len(got) > 32<<10 {
		t.Fatalf("read_file preview is %d bytes, over the 32 KiB provider cap", len(got))
	}
	if !strings.Contains(got, "next_offset=") {
		t.Fatalf("a bounded read carries no continuation cursor, so the model cannot page:\n%.200s", got)
	}
	// The prefix is the cache-stable part and must not move between builds.
	if a, b := effectSystemPrompt(t, first[0]), effectSystemPrompt(t, second[0]); a != b {
		t.Fatalf("system prefix differs across builds\nfirst=%q\nsecond=%q", a, b)
	}
	if a, b := toolSchemaNames(first[0].Tools), toolSchemaNames(second[0].Tools); !reflect.DeepEqual(a, b) {
		t.Fatalf("tool surface differs across builds\nfirst=%v\nsecond=%v", a, b)
	}
}

// effectRunWithReadFile builds the real stack with the stand-in reader attached
// and drives one tool-calling turn, returning the requests that run added.
func effectRunWithReadFile(t *testing.T, rec *testutil.MockProvider, readTool tool.Tool, turns []testutil.Turn) []provider.Request {
	t.Helper()
	before := len(rec.Requests())
	rec.Append(turns...)
	ctrl, err := Build(context.Background(), Options{Sink: event.Discard, ExtraTools: []tool.Tool{readTool}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()
	if err := ctrl.Run(context.Background(), "read the file"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reqs := rec.Requests()[before:]
	if len(reqs) == 0 {
		t.Fatal("no request reached the provider boundary")
	}
	return reqs
}

// effectToolResultBody returns the read_file tool message the model received.
func effectToolResultBody(t *testing.T, reqs []provider.Request) string {
	t.Helper()
	for _, req := range reqs {
		for _, m := range req.Messages {
			if m.Role == provider.RoleTool && m.Name == "read_file" {
				return m.Content
			}
		}
	}
	return ""
}
