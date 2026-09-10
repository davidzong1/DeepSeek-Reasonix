package cli

// Contract: the leader's dispatch context never leaks into a member turn. A
// member's own read-only constraint keeps team lifecycle writes reachable,
// while ordinary workspace writes and the explicit plan gate stay blocked.

import (
	"context"
	"encoding/json"
	"testing"

	"reasonix/internal/evidence"
	"reasonix/internal/runtimepolicy"
)

// TestMemberReportStaticMetadataKeepsExplicitGates pins the static boundary:
// member_report_result stays writable and plan-unsafe (the explicit plan gate
// and the read-only-execution gate key off these), while the query tools stay
// statically read-only so read-only turns may still inspect their own task.
func TestMemberReportStaticMetadataKeepsExplicitGates(t *testing.T) {
	report := &teamTaskTool{name: "member_report_result"}
	if report.ReadOnly() {
		t.Fatal("member_report_result static ReadOnly must stay false so explicit plan/read-only gates keep blocking it")
	}
	if report.PlanModeSafe() {
		t.Fatal("member_report_result PlanModeSafe must stay false (write-class tool for the plan gate)")
	}
	read := &teamTaskTool{name: "member_get_my_task"}
	if !read.ReadOnly() || !read.PlanModeSafe() {
		t.Fatal("member_get_my_task must stay statically read-only and plan-safe")
	}
}

// TestReportStaysWriteClassifiedUnderMemberOwnBan classifies the default
// report call the way the turn engine sees it (static ReadOnly false, hint
// known but not read-only, TeamState asserted by the engine from the tool's
// TeamLifecycleStateWriter contract) and asserts the reported verdict under the
// member's own ForbidMutation: a TeamState lifecycle write stays reachable
// (the mutation ban scopes to user state), the read query stays reachable, and
// an ordinary workspace write is still denied.
func TestReportStaysWriteClassifiedUnderMemberOwnBan(t *testing.T) {
	tt := &teamTaskTool{name: "member_report_result"}
	classify := func(t *testing.T, tt *teamTaskTool, args string, teamState bool) evidence.EffectProfile {
		t.Helper()
		hint := evidence.CallHint{Present: true, ReadOnly: tt.ReadOnly(), TeamState: teamState}
		eh := tt.EffectHint(json.RawMessage(args))
		hint.Known = eh.Known
		hint.ReadOnly = hint.ReadOnly || eh.ReadOnly
		return evidence.ClassifyEffect(evidence.EffectInput{
			ToolName: "member_report_result",
			Args:     json.RawMessage(args),
			Hint:     hint,
		})
	}
	if p := classify(t, tt, `{"operation":"report","result":"done"}`, true); !p.MutatesState() || !p.TeamState {
		t.Fatalf("report call must classify as a team state write, got %+v", p)
	}
	if p := classify(t, tt, `{"operation":"read"}`, true); p.MutatesState() {
		t.Fatalf("read call must classify read-only, got %+v", p)
	}

	eng := runtimepolicy.NewEngine(runtimepolicy.Constraints{ForbidMutation: true})
	reportProfile := classify(t, tt, `{"operation":"report","result":"done"}`, true)
	if d := eng.BeforeTool(runtimepolicy.CallContext{ToolName: "member_report_result", Profile: reportProfile}); d.Action == runtimepolicy.GuardDeny {
		t.Fatalf("team lifecycle report under ForbidMutation must be reachable, got %q", d.Message)
	}
	readProfile := classify(t, tt, `{"operation":"read"}`, true)
	if d := eng.BeforeTool(runtimepolicy.CallContext{ToolName: "member_report_result", Profile: readProfile}); d.Action == runtimepolicy.GuardDeny {
		t.Fatalf("read query under ForbidMutation must stay reachable, got %v %q", d.Action, d.Message)
	}
	// An ordinary workspace writer must stay denied by the same constraint.
	work := evidence.EffectProfile{Known: true, WorkspaceWrite: true, Reason: evidence.ReasonWorkspaceWrite}
	if d := eng.BeforeTool(runtimepolicy.CallContext{ToolName: "edit_file", Profile: work}); d.Action != runtimepolicy.GuardDeny {
		t.Fatalf("ordinary workspace write under ForbidMutation must be denied, got action=%v", d.Action)
	}
	if d := eng.BeforeTool(runtimepolicy.CallContext{ToolName: "edit_file", Profile: work}); d.Message != "blocked: the current constraints forbid state mutation" {
		t.Fatalf("ordinary write refusal message changed: %q", d.Message)
	}
}

// TestLeaderDispatchContextDoesNotPoisonMemberTurn drives the real leader tool
// Execute under a context stamped with the leader turn's ForbidMutation (as
// the agent does for every tool call, agent.go WithContext) and proves the
// member backend still accepts the dispatched turn and can complete it: the
// dispatch boundary drops the leader's policy context before the member's
// backend is touched.
func TestLeaderDispatchContextDoesNotPoisonMemberTurn(t *testing.T) {
	svc, board, backends := newConcurrencyTeam(t)
	leaderCtx := runtimepolicy.WithContext(context.Background(),
		runtimepolicy.Constraints{ForbidMutation: true, PlanModeReadOnly: true})

	assign := leaderTool(t, newLeaderTaskTools(svc, "alpha", "lead"), "leader_assign_subtask")
	if _, err := assign.Execute(leaderCtx, json.RawMessage(`{"member_name":"coder","subtask":"implement the widget"}`)); err != nil {
		t.Fatalf("leader assign under its own plan/read-only ctx must dispatch: %v", err)
	}
	if backends["coder"].submits != 1 {
		t.Fatalf("coder submits = %d, want 1 (the member backend must accept the turn)", backends["coder"].submits)
	}

	// The normal working member completes its task: report persists and the
	// board row closes, with no constraint refusal leaking from the dispatch.
	memberReport := leaderTool(t, newMemberTaskTools(svc, "alpha", "coder"), "member_report_result")
	out, err := memberReport.Execute(context.Background(), json.RawMessage(`{"result":"implemented green"}`))
	if err != nil {
		t.Fatalf("member report after a leader-plan dispatch must succeed: %v", err)
	}
	if out == "" {
		t.Fatal("member report returned no confirmation")
	}
	live, err := board.LoadLiveTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Fatalf("live = %+v, want none after the report", live)
	}
}
