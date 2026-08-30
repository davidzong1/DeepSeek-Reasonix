package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"reasonix/internal/control"
	"reasonix/internal/team"
	teamscheduler "reasonix/internal/team/scheduler"
)

type taskBackendStub struct {
	control.SessionAPI
	submits int
}

func (s *taskBackendStub) SubmitUserTurn(string, string) { s.submits++ }
func (s *taskBackendStub) SubmitUserTurnOrError(input, display string) error {
	s.submits++
	return nil
}
func (s *taskBackendStub) Running() bool              { return false }
func (s *taskBackendStub) Turn() int                  { return 0 }
func (s *taskBackendStub) Compose(text string) string { return text }
func (s *taskBackendStub) Close()                     {}

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

// TestTeamTaskServiceConcurrentLeaderAssignMemberReport pins the P2 concurrent
// contract: a leader assigning a subtask and a member reporting its turn can
// run simultaneously — two different members, two live tasks, two wakeups —
// without the §P1 member reservation or the registry mutex serializing them
// into a lost report. Each task converges to reported exactly once.
func TestTeamTaskServiceConcurrentLeaderAssignMemberReport(t *testing.T) {
	root := t.TempDir()
	teamStore, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := teamStore.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{
		Name: "alpha", Template: []team.MemberSlot{
			{MemberID: "lead", Leader: true, Status: team.MemberStatusActive},
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
	defer board.Close()
	backends := map[string]*taskBackendStub{"coder": {}, "tester": {}}
	service := newTeamTaskService(teamStore, board, "", func(b team.MemberBinding) (control.SessionAPI, error) {
		if st, ok := backends[b.MemberID]; ok {
			return st, nil
		}
		return nil, fmt.Errorf("no stub for %s", b.MemberID)
	})
	svc := service.forTeam("alpha")
	wire := &teamInboxWire{board: board}
	if got := wire.consumeWakeups("lead"); got != nil { // establish the cursor first
		t.Fatalf("first cursor read must be quiet, got %v", got)
	}

	errc := make(chan error, 2)
	assignments := make([]teamscheduler.Assignment, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	// Leader's agent assigns two members concurrently.
	go func() {
		defer wg.Done()
		a, err := svc.assignSubtask(context.Background(), "coder", "build the widget", "ctx")
		assignments[0] = a
		errc <- err
	}()
	go func() {
		defer wg.Done()
		a, err := svc.assignSubtask(context.Background(), "tester", "verify the widget", "ctx")
		assignments[1] = a
		errc <- err
	}()
	wg.Wait()
	for i := 0; i < 2; i++ {
		if err := <-errc; err != nil {
			t.Fatalf("concurrent assign: %v", err)
		}
	}
	if backends["coder"].submits != 1 || backends["tester"].submits != 1 {
		t.Fatalf("both members must receive exactly their own turn: coder=%d tester=%d", backends["coder"].submits, backends["tester"].submits)
	}

	// While the leader is (still) assigning/reporting, both members report —
	// the report path must not collide with the leader's live-task writes.
	var rwg sync.WaitGroup
	rwg.Add(2)
	go func() { defer rwg.Done(); _, _ = svc.report("coder", "built green") }()
	go func() { defer rwg.Done(); _, _ = svc.report("tester", "verified green") }()
	rwg.Wait()

	for i, id := range []string{"coder", "tester"} {
		if assignments[i].TaskID == "" {
			t.Fatalf("assignment for %s missing", id)
		}
		task, err := board.LoadTask(context.Background(), assignments[i].TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status != team.TaskStatusReported {
			t.Fatalf("%s task = %s, want reported (concurrent report lost)", id, task.Status)
		}
	}
	reasons := wire.consumeWakeups("lead")
	if len(reasons) != 2 {
		t.Fatalf("wakeups = %d (%v), want both reports surfaced once", len(reasons), reasons)
	}
}

// TestTeamTaskServiceReportWakesLeader pins the P1-2 wakeup chain: a member
// report reaches the durable board wake stream stamped with the team identity,
// which is exactly the identity the TUI's consumeWakeups(leader) filter selects
// (chat_tui_team_inbox.go). The leader's cursor must surface the report reason.
func TestTeamTaskServiceReportWakesLeader(t *testing.T) {
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
	if _, err := leaderService.assignSubtask(context.Background(), "coder", "do the work", "ctx"); err != nil {
		t.Fatal(err)
	}
	// A wake cursor is established first (the leader opened the overlay).
	wire := &teamInboxWire{board: board}
	if got := wire.consumeWakeups("lead"); got != nil {
		t.Fatalf("first cursor read must be quiet, got %v", got)
	}

	memberService := service.forTeam("alpha")
	if _, err := memberService.report("coder", "done"); err != nil {
		t.Fatal(err)
	}

	reasons := wire.consumeWakeups("lead")
	if len(reasons) != 1 || !strings.Contains(reasons[0], "reported") {
		t.Fatalf("leader wakeups = %v, want the report reason stamped with the team identity", reasons)
	}
	// The report wakeup surfaced once: a second read is quiet again.
	if again := wire.consumeWakeups("lead"); len(again) != 0 {
		t.Fatalf("wakeup must surface once, got %v", again)
	}
}

// TestTeamTaskServiceReportWakesLeaderByNameDiffers pins the identity contract
// for a team whose name differs from its leader's member id (§P1 wakeup
// identity): the wakeup the member report appends is stamped with the LEADER's
// member id — the identity the TUI's consumeWakeups(p.firstLeader()) selects —
// never the team name. The regression is that the report would be stamped with
// the team name and the leader cursor would read nothing.
func TestTeamTaskServiceReportWakesLeaderByNameDiffers(t *testing.T) {
	root := t.TempDir()
	teamStore, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	// Team "alpha", leader member id "boss": the team name and the leader id
	// must not be assumed equal anywhere in the wakeup contract.
	if err := teamStore.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{
		Name: "alpha", Template: []team.MemberSlot{
			{MemberID: "boss", Leader: true, Status: team.MemberStatusActive, Role: team.RoleCoder},
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
	if _, err := leaderService.assignSubtask(context.Background(), "coder", "do the work", "ctx"); err != nil {
		t.Fatal(err)
	}
	// The leader window opens and establishes its cursor under the leader id.
	wire := &teamInboxWire{board: board}
	if got := wire.consumeWakeups("boss"); got != nil {
		t.Fatalf("first cursor read must be quiet, got %v", got)
	}
	if _, err := service.forTeam("alpha").report("coder", "done"); err != nil {
		t.Fatal(err)
	}
	reasons := wire.consumeWakeups("boss")
	if len(reasons) != 1 || !strings.Contains(reasons[0], "reported") {
		t.Fatalf("leader wakeups under the leader id = %v, want the report reason stamped with the leader member id (team name differ)", reasons)
	}
	if again := wire.consumeWakeups("boss"); len(again) != 0 {
		t.Fatalf("wakeup must surface once, got %v", again)
	}
}
