package boot

// A7's parity half after §9.5 lifted the gate: every session binds
// AllowAgentNodes true, so what this asserts is a member session reaching the
// baseline behaviour, read back through use_capability.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

const effectOrchestrateTeamTarget = "sub/team-note.md"

// TestEffectOrchestrateRunsAgentNodesInAMemberSession drives a member session
// whose model asks for a writing plan, and asserts the plan actually ran there.
// A member is a peer session at subagent depth 0, not a nested delegate, so it
// delegates exactly once — the same as a solo session — and withholding this
// would cost the team layer a baseline feature for no safety property that
// max_subagent_depth does not already provide.
func TestEffectOrchestrateRunsAgentNodesInAMemberSession(t *testing.T) {
	isolateConfigHome(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	workspace := filepath.Join(dir, "workspace")
	writeFile(t, workspace, "reasonix.toml", effectOrchestrateRunConfig)
	writeFile(t, dir, "team/skills/base/member/SKILL.md",
		"---\nname: member\ndescription: member playbook\n---\nMBR-BASE")
	registerBootTokenProfileTestProvider()
	spec := `{"version":1,"mode":"sequence","nodes":[
		{"id":"w","kind":"agent","prompt":"Write the note with write_file.","write_paths":["sub/"]}]}`
	writeArgs := `{"path":"` + effectOrchestrateTeamTarget + `","content":"member note\n"}`
	prov := testutil.NewMock("orchestrate-team",
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "c1", Name: tool.HostUseCapability, Arguments: effectOrchestratePlanArgs(t, spec)}}},
		// The node runs as a sub-agent on this same provider, so these two turns
		// are the child's: its write, then its answer.
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "w1", Name: "write_file", Arguments: writeArgs}}},
		testutil.Turn{Text: "wrote the note"},
		testutil.Turn{Text: "understood"},
	)
	setBootTokenProfileTestProvider(t, prov)
	sink := &effectOrchestrateSink{}
	ctrl, err := Build(context.Background(), Options{
		Sink: sink, WorkspaceRoot: workspace, TeamSkillsRoot: dir,
		TeamRole: "member", Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(ctrl.Close)
	if err := ctrl.Run(context.Background(), "run the plan"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var answered []string
	for _, e := range sink.events {
		if e.Kind == event.ToolResult && e.Tool.Name == tool.HostUseCapability {
			answered = append(answered, e.Tool.Output+" "+e.Tool.Err)
		}
	}
	if len(answered) == 0 {
		t.Fatal("the member session never answered the plan call")
	}
	joined := strings.Join(answered, "\n")
	if strings.Contains(joined, "agent nodes are not available in this session") {
		t.Fatalf("the member session still refuses agent nodes: %q", joined)
	}
	if !strings.Contains(joined, "w [agent] completed") {
		t.Fatalf("the plan's node never ran in the member session: %q", joined)
	}
	if _, err := os.Stat(filepath.Join(workspace, effectOrchestrateTeamTarget)); err != nil {
		t.Fatalf("the writing node left no file, so the case would prove nothing: %v", err)
	}
	if len(prov.Requests()) < 2 {
		t.Fatalf("the model was asked %d times; the plan result never returned to it", len(prov.Requests()))
	}
}
