package cli

// P0: admission 6 regression — a member session missing its write authority
// refuses every turn (turnDroppedWriteAuthority). leader_assign_subtask must
// surface that diagnostically, never a ghost running task or cross-member leak.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/control"
	"reasonix/internal/team"
	teamscheduler "reasonix/internal/team/scheduler"
)

// admission6Backend refuses every turn exactly like the admission 6 refusal
// the task-driving SubmitUserTurnOrError produces for a member backend that is
// no longer writable — the end of the message carries the admission number, so
// the leader tool's surfaced error stays diagnostic.
type admission6Backend struct {
	control.SessionAPI
}

func (*admission6Backend) SubmitUserTurnOrError(input, display string) error {
	return errors.New("member backend did not accept the turn (admission 6)")
}

// newAdmission6Team wires one leader and one coder over a real SQLite store
// with a refusing coder backend — the exact §P1 execute-gate arrangement.
func newAdmission6Team(t *testing.T) (*teamTaskService, *team.SQLiteStore) {
	t.Helper()
	root := t.TempDir()
	teamStore, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := teamStore.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{
		Name: "alpha", Template: []team.MemberSlot{
			{MemberID: "lead", Leader: true, Role: team.RoleCoder, Status: team.MemberStatusActive},
			{MemberID: "coder", Role: team.RoleCoder, Status: team.MemberStatusActive},
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	board, err := team.NewSQLiteStore(context.Background(), filepath.Join(root, "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	service := newTeamTaskService(teamStore, board, "", func(team.MemberBinding) (control.SessionAPI, error) {
		return &admission6Backend{}, nil
	})
	return service.forTeam("alpha"), board
}

// TestTeamTaskServiceCoderAdmission6RefusalDiagnosable pins the acceptance
// gate: a refused turn surfaces as an ErrStartFailed with a diagnostic end —
// the "did not accept the turn" marker and the admission number at the end —
// instead of an opaque busy or a silent success.
func TestTeamTaskServiceCoderAdmission6RefusalDiagnosable(t *testing.T) {
	svc, _ := newAdmission6Team(t)
	_, err := svc.assignSubtask(context.Background(), "coder", "build the widget", "ctx")
	if err == nil {
		t.Fatal("a refused turn must surface as an error")
	}
	if !errors.Is(err, teamscheduler.ErrStartFailed) {
		t.Fatalf("err = %v, want ErrStartFailed", err)
	}
	if !strings.Contains(err.Error(), "did not accept the turn") || !strings.Contains(err.Error(), "admission 6)") {
		t.Fatalf("err = %q, want refusal marker and admission 6 suffix for diagnosis", err)
	}
}

// TestTeamTaskServiceCoderAdmission6NoGhostRunning pins the §P1 execution
// gate at the service boundary: the refused start leaves the durable task
// assigned, never a persisted running task that never executed.
func TestTeamTaskServiceCoderAdmission6NoGhostRunning(t *testing.T) {
	svc, board := newAdmission6Team(t)
	if _, err := svc.assignSubtask(context.Background(), "coder", "build the widget", ""); err == nil {
		t.Fatal("a refused turn must surface as an error")
	}
	tasks, err := board.LoadLiveTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("live = %+v, want exactly the one assigned task", tasks)
	}
	if tasks[0].Status != team.TaskStatusAssigned {
		t.Fatalf("live task = %s status, want assigned (rollback, never a ghost running)", tasks[0].Status)
	}
}

// TestTeamTaskServiceAdmission6NoCrossMemberInterference is the isolation
// contract: the refused task stays assigned on its owner and is re-dispatchable
// to another member — a refused start for one member never leaks onto another.
func TestTeamTaskServiceAdmission6NoCrossMemberInterference(t *testing.T) {
	root := t.TempDir()
	teamStore, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := teamStore.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{
		Name: "alpha", Template: []team.MemberSlot{
			{MemberID: "lead", Leader: true, Role: team.RoleCoder, Status: team.MemberStatusActive},
			{MemberID: "coder", Role: team.RoleCoder, Status: team.MemberStatusActive},
			{MemberID: "tester", Role: team.RoleTester, Status: team.MemberStatusActive},
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	board, err := team.NewSQLiteStore(context.Background(), filepath.Join(root, "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	service := newTeamTaskService(teamStore, board, "", func(b team.MemberBinding) (control.SessionAPI, error) {
		if b.MemberID == "coder" {
			return &admission6Backend{}, nil
		}
		return &taskBackendStub{}, nil
	})
	svc := service.forTeam("alpha")

	// The refusing member's start fails and stays assigned...
	if _, err := svc.assignSubtask(context.Background(), "coder", "build it", ""); err == nil {
		t.Fatal("coder's refused turn must surface")
	}
	// ...while the idle member still accepts its own task.
	if _, err := svc.assignSubtask(context.Background(), "tester", "verify it", ""); err != nil {
		t.Fatalf("tester must accept its own task despite coder's refusal: %v", err)
	}
	tasks, err := board.LoadLiveTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("live = %+v, want coder's assigned and tester's running", tasks)
	}
	for _, task := range tasks {
		switch task.AssignedMember {
		case "coder":
			if task.Status != team.TaskStatusAssigned {
				t.Fatalf("coder's task = %s, want assigned (refused start must not sneak a run)", task.Status)
			}
		case "tester":
			if task.Status != team.TaskStatusRunning {
				t.Fatalf("tester's task = %s, want running", task.Status)
			}
		default:
			t.Fatalf("unexpected task %s on %s", task.ID, task.AssignedMember)
		}
	}
}
