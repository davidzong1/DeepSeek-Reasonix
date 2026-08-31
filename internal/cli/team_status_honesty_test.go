package cli

// Status honesty (U2a) and dispatch-target correctness (D10/D13): what the
// leader reads must distinguish a member that is executing from a durable row
// nothing is driving, and a report must close the task the member actually ran.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/team"
)

// TestCheckStatusDistinguishesDrivenFromQueued is the U2 reporting contract: a
// task the runtime is driving reads as working, and a durable row nothing is
// driving reads as queued — never as working. Rendering every live row as
// "working" is what made the leader wait on a member that had been handed
// nothing, with no way to tell the two apart.
func TestCheckStatusDistinguishesDrivenFromQueued(t *testing.T) {
	svc, _, backends := newConcurrencyTeam(t)
	assign := leaderTool(t, newLeaderTaskTools(svc, "alpha", "lead"), "leader_assign_subtask")

	if _, err := assign.Execute(context.Background(), json.RawMessage(`{"member_name":"coder","subtask":"build it"}`)); err != nil {
		t.Fatalf("first assign: %v", err)
	}
	if !backends["coder"].Running() {
		t.Fatal("coder must actually be running")
	}
	status, err := svc.checkStatus("coder")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "working task=") {
		t.Fatalf("a driven task must read as working:\n%s", status)
	}

	// The second assign is refused (member busy) but its row is durable and
	// re-dispatchable. It must not read as a second unit of work in progress.
	if _, err := assign.Execute(context.Background(), json.RawMessage(`{"member_name":"coder","subtask":"build it again"}`)); err == nil {
		t.Fatal("a busy member must refuse the second dispatch")
	}
	status, err = svc.checkStatus("coder")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "queued task=") || !strings.Contains(status, "not dispatched") {
		t.Fatalf("an undispatched row must read as queued:\n%s", status)
	}
	if strings.Count(status, "working task=") != 1 {
		t.Fatalf("exactly one task is actually running:\n%s", status)
	}
}

// TestReportClosesTheDrivenTask pins D10: with one running and one queued task
// the report closes the running one — the task the member executed — instead of
// whichever row the durable query returned first.
func TestReportClosesTheDrivenTask(t *testing.T) {
	svc, board, _ := newConcurrencyTeam(t)
	assign := leaderTool(t, newLeaderTaskTools(svc, "alpha", "lead"), "leader_assign_subtask")
	if _, err := assign.Execute(context.Background(), json.RawMessage(`{"member_name":"coder","subtask":"the real work"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := assign.Execute(context.Background(), json.RawMessage(`{"member_name":"coder","subtask":"refused extra"}`)); err == nil {
		t.Fatal("the second dispatch must be refused")
	}
	live, err := board.LoadLiveTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var running team.TaskID
	for _, task := range live {
		if task.Status == team.TaskStatusRunning {
			running = task.ID
		}
	}
	if running == "" {
		t.Fatalf("expected one running task, got %+v", live)
	}

	out, err := svc.report("coder", "", "built green")
	if err != nil {
		t.Fatalf("the driven task must disambiguate the report: %v", err)
	}
	if !strings.Contains(out, string(running)) {
		t.Fatalf("report closed the wrong task: %q, want %s", out, running)
	}

	// An explicit id that the member does not own is refused, not silently
	// redirected onto whatever it does own.
	if _, err := svc.report("coder", "no-such-task", "x"); err == nil {
		t.Fatal("an unknown task_id must be refused")
	}
}

// TestAssignSubtaskRefusesNonAssignableMember pins D13: the fleet excludes
// archived, disabled and leader slots, and pick() only honours the requested
// member when it is in the fleet — so dispatching to an excluded member used to
// start the work on a same-role sibling, or persist a row nothing could pick up.
// It must be refused before anything is written.
func TestAssignSubtaskRefusesNonAssignableMember(t *testing.T) {
	svc, board, _ := newConcurrencyTeam(t)
	if err := svc.teamStore.SetMemberStatus("alpha", "coder", team.MemberStatusArchived); err != nil {
		t.Fatal(err)
	}
	_, err := svc.assignSubtask(context.Background(), "coder", "build it", "")
	if err == nil {
		t.Fatal("dispatching to an archived member must be refused")
	}
	if !strings.Contains(err.Error(), "not an assignable member") {
		t.Fatalf("err = %v, want the non-assignable refusal", err)
	}
	live, loadErr := board.LoadLiveTasks(context.Background())
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(live) != 0 {
		t.Fatalf("a refused dispatch must write nothing, got %+v", live)
	}
}

// TestFleetReportsWhoIsActuallyBusy pins D12: the fleet handed the scheduler
// every member as idle with an empty TaskRef, so pick()'s idle-before-busy branch
// could never run and a role could be dispatched onto the one member already
// working. Only a driven task counts — a queued row is not work in progress.
func TestFleetReportsWhoIsActuallyBusy(t *testing.T) {
	svc, _, _ := newConcurrencyTeam(t)
	fleet, err := svc.fleet()
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range fleet {
		if member.State != team.MemberStateIdle || member.TaskRef != "" {
			t.Fatalf("nothing dispatched yet, but %s reads %s/%q", member.ID, member.State, member.TaskRef)
		}
	}

	assign := leaderTool(t, newLeaderTaskTools(svc, "alpha", "lead"), "leader_assign_subtask")
	if _, err := assign.Execute(context.Background(), json.RawMessage(`{"member_name":"coder","subtask":"build it"}`)); err != nil {
		t.Fatal(err)
	}
	fleet, err = svc.fleet()
	if err != nil {
		t.Fatal(err)
	}
	var seen int
	for _, member := range fleet {
		switch member.ID {
		case "coder":
			seen++
			if member.State != team.MemberStateWorking || member.TaskRef == "" {
				t.Fatalf("the driven member must read working with its task, got %s/%q", member.State, member.TaskRef)
			}
		case "tester":
			seen++
			if member.State != team.MemberStateIdle || member.TaskRef != "" {
				t.Fatalf("an untouched member must stay idle, got %s/%q", member.State, member.TaskRef)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("fleet must carry both workers, saw %d", seen)
	}
}
