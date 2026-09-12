package boot

// A4 at the boot boundary: a plan reached through use_capability runs a real
// writing node whose change the host's own completion audit refuses to call
// done.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// effectOrchestrateRunConfig is the smallest workable session for a plan: one
// model, no environment probing, and a workspace the plan can write into.
const effectOrchestrateRunConfig = `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[environment]
enabled = false

[[providers]]
name = "test-model"
kind = "` + bootTokenProfileTestProviderKind + `"
model = "x"
`

// effectOrchestrateCapture gathers the whole turn: provider requests, every
// event the assembled controller emitted, and the host's completion audits.
type effectOrchestrateCapture struct {
	requests []provider.Request
	events   []event.Event
	audit    []event.CompletionReportAudit
}

// lastAudit is the turn's own completion report, content-free: a verdict, gap
// counts and gap kinds, no node bodies and no file contents.
func (c effectOrchestrateCapture) lastAudit() (event.CompletionReportAudit, bool) {
	if len(c.audit) == 0 {
		return event.CompletionReportAudit{}, false
	}
	return c.audit[len(c.audit)-1], true
}

// toolOutput is the concatenated text of every tool result the turn fed back
// to the model, which is where a plan's node lines end up.
func (c effectOrchestrateCapture) toolOutput(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, e := range c.events {
		if e.Kind == event.ToolResult {
			b.WriteString(e.Tool.Output)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func mustWorkingDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return dir
}

// effectOrchestrateSink records the turn's events and opts in to the host's
// completion audit, which is the seam a receipt gap is readable from.
type effectOrchestrateSink struct {
	events []event.Event
	audit  []event.CompletionReportAudit
}

func (s *effectOrchestrateSink) Emit(e event.Event) { s.events = append(s.events, e) }

func (s *effectOrchestrateSink) RecordCompletionReport(a event.CompletionReportAudit) {
	s.audit = append(s.audit, a)
}

// effectOrchestratePlanArgs renders one orchestrate call as use_capability
// takes it, through the same channel a provider-visible tool would not need.
func effectOrchestratePlanArgs(t *testing.T, specJSON string) string {
	t.Helper()
	args, err := json.Marshal(map[string]any{
		"action":        "call",
		"capability_id": "tool:" + tool.HostOrchestrate,
		"arguments":     map[string]any{"spec": json.RawMessage(specJSON)},
	})
	if err != nil {
		t.Fatalf("marshal orchestrate args: %v", err)
	}
	return string(args)
}

// effectOrchestrateRun drives one headless turn whose scripted model calls
// use_capability → tool:orchestrate, and returns everything the turn left.
func effectOrchestrateRun(t *testing.T, turns ...testutil.Turn) (effectOrchestrateCapture, *testutil.MockProvider) {
	t.Helper()
	isolateConfigHome(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	writeFile(t, dir, "reasonix.toml", effectOrchestrateRunConfig)
	registerBootTokenProfileTestProvider()
	prov := testutil.NewMock("orchestrate-plan", turns...)
	setBootTokenProfileTestProvider(t, prov)
	sink := &effectOrchestrateSink{}
	ctrl, err := Build(context.Background(), Options{Sink: sink})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(ctrl.Close)
	if err := ctrl.Run(context.Background(), "run the plan"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return effectOrchestrateCapture{requests: prov.Requests(), events: sink.events, audit: sink.audit}, prov
}

// A4: a plan that reached the tool through use_capability and changed the
// workspace without verifying it cannot come back done. The verdict is the
// turn's own receipt over the receipts the nodes left, read at the boundary the
// user reads it from.
func TestEffectOrchestrateUnverifiedWriteCannotReadDone(t *testing.T) {
	target := "sub/note.md"
	// The node's own write is real: write_file is already in the child's
	// provider surface, and the plan's write_paths only narrow what it may touch.
	writeArgs := `{"path":"` + target + `","content":"plan note\n"}`
	spec := `{"version":1,"mode":"sequence","nodes":[
		{"id":"w","kind":"agent","prompt":"Write the plan note with write_file.","write_paths":["sub/"]}]}`
	capture, prov := effectOrchestrateRun(t,
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "c1", Name: tool.HostUseCapability, Arguments: effectOrchestratePlanArgs(t, spec)}}},
		// The plan's node runs as a sub-agent on this same provider, so these two
		// turns are the child's: one tool call, then its final answer.
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "w1", Name: "write_file", Arguments: writeArgs}}},
		testutil.Turn{Text: "wrote the note"},
		testutil.Turn{Text: "done"},
	)
	// The plan is absent from the provider surface, so reaching it at all proves
	// the capability channel carried it.
	for _, req := range capture.requests {
		if requestHasTool(req, tool.HostOrchestrate) {
			t.Fatalf("orchestrate leaked into the provider surface: %v", toolSchemaNames(req.Tools))
		}
	}
	if got := capture.toolOutput(t); !strings.Contains(got, "w [agent] completed") {
		t.Fatalf("the plan's node never ran through the tool:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(mustWorkingDir(t), target)); err != nil {
		t.Fatalf("the writing node left no file, so the case would prove nothing: %v", err)
	}
	audit, ok := capture.lastAudit()
	if !ok {
		t.Fatal("the host published no completion report for the turn")
	}
	if audit.Verdict == "done" {
		t.Fatalf("a plan that wrote %s and verified nothing read as done", target)
	}
	if audit.Changes == 0 {
		t.Fatal("the completion report counted no changes, so the gap would prove nothing")
	}
	if audit.Gaps == 0 || !slices.Contains(audit.GapKinds, "unverified_change") {
		t.Fatalf("the completion report carries %d gaps %v, want the unverified change", audit.Gaps, audit.GapKinds)
	}
	if len(prov.Requests()) < 2 {
		t.Fatalf("the model was asked %d times; the plan result never returned to it", len(prov.Requests()))
	}
}
