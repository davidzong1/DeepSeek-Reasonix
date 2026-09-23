package agentruntime

import (
	"context"
	"strings"
	"testing"

	"reasonix/internal/team"
)

// framedStubAgent is a backend that can take a host-dispatched turn: it records
// which submit the runtime chose, so "the task text is host-framed" is checked
// at the call the runtime actually makes rather than inferred.
type framedStubAgent struct {
	stubAgent
	framed []string
}

func (s *framedStubAgent) SubmitUserTurnFramedOrError(input, display string) error {
	s.framed = append(s.framed, input)
	return nil
}

// narrowStubAgent is a backend with only the narrow AgentAPI surface: the
// task must still be driven, on the plain submit.
type narrowStubAgent struct{ stubAgent }

func startOne(t *testing.T, api AgentAPI) *team.Task {
	t.Helper()
	rt := NewRuntime(func(string) (AgentAPI, error) { return api, nil }, newTestBoard(t), "team:T",
		func(memberID string) team.Identity { return team.Identity{MemberID: memberID, Generation: 1} })
	task := team.Task{ID: "t1", Desc: "只读审计 W1/W2/W5 当前状态", Status: team.TaskStatusAssigned}
	if err := rt.Start(context.Background(), task, team.Member{ID: "alpha", State: team.MemberStateIdle}); err != nil {
		t.Fatalf("Start = %v", err)
	}
	return &task
}

// TestRuntimeStartFramesDispatchedTurns pins the seam the member's policy
// depends on: a task order is submitted through the framed entry, so its wording
// (the leader's "只读审计…" and any peer-written board line the injection carries)
// cannot become the member's constraints.
func TestRuntimeStartFramesDispatchedTurns(t *testing.T) {
	api := &framedStubAgent{}
	task := startOne(t, api)
	if len(api.framed) != 1 {
		t.Fatalf("framed submits = %d, want exactly 1 (plain=%v)", len(api.framed), api.submitted)
	}
	if len(api.submitted) != 0 {
		t.Fatalf("a framing backend must not fall back to the plain submit: %v", api.submitted)
	}
	if !strings.Contains(api.framed[0], "[task: t1]") || !strings.Contains(api.framed[0], task.Desc) {
		t.Fatalf("framed input = %q, want the injected task context", api.framed[0])
	}
}

// TestRuntimeStartFallsBackForANarrowBackend keeps the port honest: a backend
// that cannot frame still drives the task, on the historical submit.
func TestRuntimeStartFallsBackForANarrowBackend(t *testing.T) {
	api := &narrowStubAgent{}
	startOne(t, api)
	if len(api.submitted) != 1 {
		t.Fatalf("submits = %d, want the turn delivered once", len(api.submitted))
	}
	if !strings.Contains(api.submitted[0], "[task: t1]") {
		t.Fatalf("input = %q, want the injected task context", api.submitted[0])
	}
}

// TestRuntimeResumeFramesDispatchedTurns covers the second dispatch entry: a
// resumed task is the same kind of host work order.
func TestRuntimeResumeFramesDispatchedTurns(t *testing.T) {
	api := &framedStubAgent{}
	rt := NewRuntime(func(string) (AgentAPI, error) { return api, nil }, newTestBoard(t), "team:T",
		func(memberID string) team.Identity { return team.Identity{MemberID: memberID, Generation: 1} })
	task := team.Task{ID: "t2", Desc: "finish it", Status: team.TaskStatusAssigned}
	if err := rt.Resume(context.Background(), task, team.Member{ID: "alpha", State: team.MemberStateIdle}); err != nil {
		t.Fatalf("Resume = %v", err)
	}
	if len(api.framed) != 1 || !strings.Contains(api.framed[0], "[resumed]") {
		t.Fatalf("framed submits = %v, want the resumed task framed", api.framed)
	}
	if len(api.submitted) != 0 {
		t.Fatalf("Resume fell back to the plain submit: %v", api.submitted)
	}
}
