package control

import (
	"context"
	"strings"
	"testing"

	"reasonix/internal/runtimepolicy"
)

// dispatchOrder is the wording a real leader used on disk (task
// GPU_MPC_Team-1790041163089014593-4). As a *user* turn it is a read-only
// instruction; as a dispatched order it is nothing of the kind.
const dispatchOrder = "只读审计 W1/W2/W5 当前状态。核查 FactorHandle 是否有 solve_device、BlockTridiagDeviceSolver 是否有 host_roundtrip 证据；不改生产代码。" +
	"将最小证据写入团队 deliverable 并报告 id。"

// TestFramedDispatchTurnDerivesNoConstraints pins where a member's false
// read-only turn was born: the turn text is parsed here, in the same method a
// user turn goes through. The same words must keep banning mutation on a user
// turn and stop banning it on a host-dispatched one.
func TestFramedDispatchTurnDerivesNoConstraints(t *testing.T) {
	c := New(Options{})
	defer c.Close()

	plain := c.withPlannerTurnMetadata(context.Background(), dispatchOrder, false, 0)
	got, ok := runtimepolicy.FromContext(plain)
	if !ok || !got.ForbidMutation || got.AllowsMutation() {
		t.Fatalf("a user turn saying the same words must still forbid mutation: %+v ok=%v", got, ok)
	}

	framed := c.withPlannerTurnMetadata(runtimepolicy.WithDispatchFraming(context.Background()), dispatchOrder, false, 0)
	got, ok = runtimepolicy.FromContext(framed)
	if !ok {
		t.Fatal("the framed turn must still publish its (empty) constraints")
	}
	if !got.AllowsMutation() || got.ForbidMutation || got.PlanModeReadOnly {
		t.Fatalf("a dispatched order's wording must not become the member's constraints: %+v", got)
	}
	if len(got.Notes) != 0 {
		t.Fatalf("a dispatched order must leave no constraint notes: %v", got.Notes)
	}
}

// TestFramedDispatchTurnKeepsPlanModeReadOnly keeps plan mode in charge of an
// ordinary dispatched turn: framing drops what the *text* would have said, never
// the state the session is actually in.
func TestFramedDispatchTurnKeepsPlanModeReadOnly(t *testing.T) {
	c := New(Options{})
	defer c.Close()
	c.SetPlanMode(true)

	framed := c.withPlannerTurnMetadata(runtimepolicy.WithDispatchFraming(context.Background()), dispatchOrder, false, 0)
	got, ok := runtimepolicy.FromContext(framed)
	if !ok || !got.PlanModeReadOnly || !got.ForbidMutation {
		t.Fatalf("plan mode must keep a framed turn read-only: %+v ok=%v", got, ok)
	}
}

// TestSubmitUserTurnFramedReportsRefusals pins the framed entry's admission
// contract to the plain one's: a closed session refuses with the reason named,
// so the task runtime can still persist the failure instead of a ghost task.
func TestSubmitUserTurnFramedReportsRefusals(t *testing.T) {
	c := New(Options{})
	c.Close()
	err := c.SubmitUserTurnFramedOrError("只读审计 t1", "只读审计 t1")
	if err == nil {
		t.Fatal("a closed session must refuse the framed turn")
	}
	got := err.Error()
	if !strings.Contains(got, "session is closed") || !strings.Contains(got, "did not accept the turn") {
		t.Fatalf("err = %q, want the same refusal shape the plain submit reports", got)
	}
}
