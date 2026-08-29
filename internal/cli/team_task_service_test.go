package cli

import (
	"context"
	"path/filepath"
	"testing"

	"reasonix/internal/control"
	"reasonix/internal/team"
)

type taskBackendStub struct {
	control.SessionAPI
	submits int
}

func (s *taskBackendStub) SubmitUserTurn(string, string) { s.submits++ }
func (s *taskBackendStub) Running() bool                 { return false }
func (s *taskBackendStub) Turn() int                     { return 0 }
func (s *taskBackendStub) Compose(text string) string    { return text }
func (s *taskBackendStub) Close()                        {}

func TestTeamTaskServiceAssignAndReport(t *testing.T) {
	root := t.TempDir()
	teamStore, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := teamStore.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{
		Name: "alpha", Template: []team.MemberSlot{
			{MemberID: "lead", Leader: true, Status: team.MemberStatusActive},
			{MemberID: "coder", Role: team.RoleCoder, Status: team.MemberStatusActive},
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	board, err := team.NewSQLiteStore(context.Background(), filepath.Join(root, "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer board.Close()
	backend := &taskBackendStub{}
	service := newTeamTaskService(teamStore, board, "", func(team.MemberBinding) (control.SessionAPI, error) {
		return backend, nil
	})
	leaderService := service.forTeam("alpha")
	assignment, err := leaderService.assignSubtask(context.Background(), "coder", "implement the change", "context")
	if err != nil {
		t.Fatal(err)
	}
	if assignment.MemberID != "coder" || assignment.Status != "running" || backend.submits != 1 {
		t.Fatalf("assignment=%+v submits=%d", assignment, backend.submits)
	}
	task, err := board.LoadTask(context.Background(), assignment.TaskID)
	if err != nil || task.Status != team.TaskStatusRunning {
		t.Fatalf("persisted task=%+v err=%v", task, err)
	}
	// Member backends resolve the same per-team service through forTeam. The
	// shared runtime is required for a report to close a task started by the
	// leader backend.
	memberService := service.forTeam("alpha")
	if memberService != leaderService {
		t.Fatalf("same team should reuse task service: got %p want %p", memberService, leaderService)
	}
	if _, err := memberService.report("coder", "implemented and tested"); err != nil {
		t.Fatal(err)
	}
	task, err = board.LoadTask(context.Background(), assignment.TaskID)
	if err != nil || task.Status != team.TaskStatusReported {
		t.Fatalf("reported task=%+v err=%v", task, err)
	}
}
