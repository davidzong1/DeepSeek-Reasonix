package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/runtimepolicy"
	"reasonix/internal/tool"
	"reasonix/internal/tool/builtin"
)

// dispatchedOrder is the wording a real leader put in a real member's task text
// (task GPU_MPC_Team-1790041163089014593-4 on disk). As a user turn it forbids
// mutation; as a dispatched order it is a description of the audit, and the
// member still has to write its report.
const dispatchedOrder = "只读审计 W1/W2/W5 当前状态。核查 FactorHandle 是否有 solve_device、BlockTridiagDeviceSolver 是否有 host_roundtrip 证据；" +
	"不改生产代码。将最小证据写入团队 deliverable 并报告 id。"

// TestDispatchedOrderWordingDoesNotBanTheMembersWrite is the reported symptom,
// end to end: the same words must keep refusing a write on a user turn and stop
// refusing it on a host-dispatched member turn, with nothing else changed.
func TestDispatchedOrderWordingDoesNotBanTheMembersWrite(t *testing.T) {
	for _, framed := range []bool{false, true} {
		name := "user_turn_keeps_the_ban"
		if framed {
			name = "dispatched_turn_writes"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "deliverable.md")
			reg := tool.NewRegistry()
			for _, w := range builtin.ConfineWriters([]string{root}, builtin.SessionDataGuard{}, builtin.ManagedConfigPaths{}) {
				if w.Name() == "write_file" {
					reg.Add(w)
				}
			}
			args, err := json.Marshal(map[string]string{"path": target, "content": "audit evidence"})
			if err != nil {
				t.Fatal(err)
			}
			p := testutil.NewMock("fixture",
				testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "w", Name: "write_file", Arguments: string(args)}}},
				testutil.Turn{Text: "done"})
			a := New(p, reg, NewSession(""), Options{}, event.Discard)

			ctx := withNoClosedLoop(context.Background())
			if framed {
				ctx = runtimepolicy.WithDispatchFraming(ctx)
			}
			_ = a.Run(ctx, dispatchedOrder)

			_, statErr := os.Stat(target)
			switch {
			case framed && statErr != nil:
				t.Fatalf("a dispatched order's wording must not deny the member's write: %v", statErr)
			case !framed && !os.IsNotExist(statErr):
				t.Fatalf("the same words on a user turn must still deny the write: err=%v", statErr)
			}
		})
	}
}

// TestDispatchedOrderConstraintsStayEmpty pins the constraint half directly, so
// the test above cannot pass for an unrelated reason (a tool that stopped
// writing, an approval change): the framed turn's own constraints stay empty.
func TestDispatchedOrderConstraintsStayEmpty(t *testing.T) {
	a := New(nil, tool.NewRegistry(), NewSession(""), Options{}, event.Discard)
	if _, _, err := a.beginRunTurn(runtimepolicy.WithDispatchFraming(context.Background()), dispatchedOrder, pinnedRevisionPlan{}); err != nil {
		t.Fatalf("beginRunTurn: %v", err)
	}
	if !a.turn.constraints.AllowsMutation() || len(a.turn.constraints.Notes) != 0 {
		t.Fatalf("framed turn constraints = %+v, want a writable turn with no notes", a.turn.constraints)
	}

	plain := New(nil, tool.NewRegistry(), NewSession(""), Options{}, event.Discard)
	if _, _, err := plain.beginRunTurn(context.Background(), dispatchedOrder, pinnedRevisionPlan{}); err != nil {
		t.Fatalf("beginRunTurn: %v", err)
	}
	if plain.turn.constraints.AllowsMutation() {
		t.Fatalf("the same text on a user turn must still forbid mutation: %+v", plain.turn.constraints)
	}
}
