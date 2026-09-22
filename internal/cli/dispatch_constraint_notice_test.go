package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/runtimepolicy"
)

// TestAssignCarriesTheReadOnlyNotice pins the replacement for the silent
// contradiction: a member's turn is host-framed, so the leader's order wording
// never binds the member — which means a leader dispatching implementation work
// from a read-only turn must be told. The notice is attributable to the
// constraint: the same call under a plain turn carries no note at all.
func TestAssignCarriesTheReadOnlyNotice(t *testing.T) {
	cases := []struct {
		name    string
		ctx     context.Context
		want    bool
		reasons string
	}{
		{name: "plain_turn", ctx: context.Background(), want: false},
		{name: "read_only_turn", ctx: runtimepolicy.WithContext(context.Background(),
			runtimepolicy.Constraints{ForbidMutation: true}), want: true, reasons: "forbids mutation"},
		{name: "plan_mode", ctx: runtimepolicy.WithContext(context.Background(),
			runtimepolicy.Constraints{PlanModeReadOnly: true, ForbidMutation: true}), want: true, reasons: "plan mode is read-only"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"_subtask", func(t *testing.T) {
			svc, _, backends := newConcurrencyTeam(t)
			assign := leaderTool(t, newLeaderTaskTools(svc, "alpha", "lead"), "leader_assign_subtask")
			out, err := assign.Execute(tc.ctx, json.RawMessage(`{"member_name":"coder","subtask":"只读审计 t1 当前状态"}`))
			if err != nil {
				t.Fatalf("assign = %v", err)
			}
			checkNotice(t, out, tc.want, tc.reasons)
			if backends["coder"].submits != 1 {
				t.Fatalf("coder submits = %d, want the dispatch to still land", backends["coder"].submits)
			}
		})
		t.Run(tc.name+"_fanout", func(t *testing.T) {
			svc, _, _ := newConcurrencyTeam(t)
			assign := leaderTool(t, newLeaderTaskTools(svc, "alpha", "lead"), "leader_assign_task_to_relevant")
			out, err := assign.Execute(tc.ctx, json.RawMessage(`{"task":"只读审计 t1 当前状态"}`))
			if err != nil {
				t.Fatalf("fan-out = %v", err)
			}
			checkNotice(t, out, tc.want, tc.reasons)
		})
	}
}

func checkNotice(t *testing.T, out string, want bool, reasons string) {
	t.Helper()
	if got := strings.Contains(out, "\nnote: "); got != want {
		t.Fatalf("notice present = %v, want %v (out=%q)", got, want, out)
	}
	if !want {
		return
	}
	if !strings.Contains(out, reasons) {
		t.Fatalf("notice must name the state (%q), out=%q", reasons, out)
	}
	if !strings.Contains(out, "host-framed") {
		t.Fatalf("notice must say why the member is not bound, out=%q", out)
	}
}
