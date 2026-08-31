package cli

// Real Team-session end-to-end concurrency acceptance (§P1/W2): leader tools
// dispatch over a real SQLiteStore, two members run at once, reports surface
// exactly once, and a busy member never leaks ErrMemberBusy onto another.

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/team"
	"reasonix/internal/tool"
)

// blockingMemberBackend is a member backend whose turn stays running until the
// test ends it — a real member turn's duration, so two members can be observed
// mid-turn at the same time (the overlap "real concurrency" is about).
type blockingMemberBackend struct {
	control.SessionAPI
	mu      sync.Mutex
	running bool
	submits int
	started chan struct{}
	cancel  chan struct{}
}

func newBlockingMemberBackend() *blockingMemberBackend {
	return &blockingMemberBackend{started: make(chan struct{}), cancel: make(chan struct{})}
}

func (b *blockingMemberBackend) SubmitUserTurnOrError(input, display string) error {
	b.mu.Lock()
	b.submits++
	b.running = true
	b.mu.Unlock()
	select {
	case <-b.started:
	default:
		close(b.started)
	}
	return nil
}

func (b *blockingMemberBackend) Submit(input string) { _ = b.SubmitUserTurnOrError(input, input) }
func (b *blockingMemberBackend) SubmitUserTurn(input, display string) {
	_ = b.SubmitUserTurnOrError(input, display)
}
func (b *blockingMemberBackend) Running() bool              { b.mu.Lock(); defer b.mu.Unlock(); return b.running }
func (b *blockingMemberBackend) Turn() int                  { return 0 }
func (b *blockingMemberBackend) Compose(text string) string { return text }
func (b *blockingMemberBackend) Close()                     {}
func (b *blockingMemberBackend) Cancel() {
	select {
	case <-b.cancel:
	default:
		close(b.cancel)
	}
}

// newConcurrencyTeam wires a team with one leader and two workers over a real
// SQLiteStore board and per-member blocking backends, exactly the shape a host
// opens for a Team session.
func newConcurrencyTeam(t *testing.T) (*teamTaskService, *team.SQLiteStore, map[string]*blockingMemberBackend) {
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
			{MemberID: "tester", Role: team.RoleTester, Status: team.MemberStatusActive},
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	board, err := team.NewSQLiteStore(context.Background(), filepath.Join(root, "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	backends := map[string]*blockingMemberBackend{
		"coder":  newBlockingMemberBackend(),
		"tester": newBlockingMemberBackend(),
	}
	service := newTeamTaskService(teamStore, board, "", func(b team.MemberBinding) (control.SessionAPI, error) {
		if bk, ok := backends[b.MemberID]; ok {
			return bk, nil
		}
		return nil, fmt.Errorf("no stub for %s", b.MemberID)
	})
	return service.forTeam("alpha"), board, backends
}

// leaderTool finds one leader task tool by name so a test drives the real
// Agent-facing Execute path instead of reimplementing it.
func leaderTool(t *testing.T, tools []tool.Tool, name string) *teamTaskTool {
	t.Helper()
	for _, tl := range tools {
		if tt, ok := tl.(*teamTaskTool); ok && tt.name == name {
			return tt
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}

// TestTeamE2EAssignTaskToRelevantRunsBothMembersAndReportsBoth is the parallel
// dispatch contract: both workers start (each once), run at once, both reports
// surface as leader wakeups exactly once — none lost, none repeated.
func TestTeamE2EAssignTaskToRelevantRunsBothMembersAndReportsBoth(t *testing.T) {
	svc, board, backends := newConcurrencyTeam(t)
	wire := &teamInboxWire{board: board}
	if got := wire.consumeWakeups("lead"); got != nil {
		t.Fatalf("first cursor read must be quiet, got %v", got)
	}
	relevant := leaderTool(t, newLeaderTaskTools(svc, "alpha", "lead"), "leader_assign_task_to_relevant")
	if _, err := relevant.Execute(context.Background(), json.RawMessage(`{"task":"build and verify the widget","required_roles":"coder,tester"}`)); err != nil {
		t.Fatalf("leader_assign_task_to_relevant: %v", err)
	}

	// Every selected member's backend started exactly once.
	for _, id := range []string{"coder", "tester"} {
		if backends[id].submits != 1 {
			t.Fatalf("%s submits = %d, want exactly 1", id, backends[id].submits)
		}
	}
	// Both are mid-turn at the same instant: the overlap is the concurrency.
	if !backends["coder"].Running() || !backends["tester"].Running() {
		t.Fatalf("both members must be running at once: coder=%v tester=%v",
			backends["coder"].Running(), backends["tester"].Running())
	}

	// Both members report concurrently; the report path must not collide.
	var wg sync.WaitGroup
	var rep1, rep2 error
	wg.Add(2)
	go func() { defer wg.Done(); _, rep1 = svc.report("coder", "", "built green") }()
	go func() { defer wg.Done(); _, rep2 = svc.report("tester", "", "verified green") }()
	wg.Wait()
	if rep1 != nil || rep2 != nil {
		t.Fatalf("concurrent reports: coder=%v tester=%v", rep1, rep2)
	}
	live, err := board.LoadLiveTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Fatalf("live = %+v, want none after both report", live)
	}
	reasons := wire.consumeWakeups("lead")
	if len(reasons) != 2 {
		t.Fatalf("leader wakeups = %d (%v), want both reports surfaced exactly once", len(reasons), reasons)
	}
	for _, r := range reasons {
		if !strings.Contains(r, "reported") {
			t.Fatalf("wakeup %q not a report", r)
		}
	}
	if again := wire.consumeWakeups("lead"); len(again) != 0 {
		t.Fatalf("wakeups must surface once, got %v", again)
	}
}

// TestTeamE2EBusyMemberDoesNotLeakOntoOthers pins ErrMemberBusy isolation: a
// second start on a running member fails alone, while an idle member still
// accepts its own task — busy never serializes or poisons another dispatch.
func TestTeamE2EBusyMemberDoesNotLeakOntoOthers(t *testing.T) {
	svc, _, backends := newConcurrencyTeam(t)
	assign := leaderTool(t, newLeaderTaskTools(svc, "alpha", "lead"), "leader_assign_subtask")
	if _, err := assign.Execute(context.Background(), json.RawMessage(`{"member_name":"coder","subtask":"build the widget"}`)); err != nil {
		t.Fatalf("first assign to coder: %v", err)
	}
	if !backends["coder"].Running() {
		t.Fatal("coder must be running after the first assign")
	}
	if backends["tester"].Running() {
		t.Fatal("tester must stay idle while coder runs")
	}

	// Re-starting the busy member surfaces ErrMemberBusy, not a silent drop.
	_, err := assign.Execute(context.Background(), json.RawMessage(`{"member_name":"coder","subtask":"build it again"}`))
	if err == nil || !strings.Contains(err.Error(), "already running a task") {
		t.Fatalf("double-start on the busy member must surface ErrMemberBusy, got %v", err)
	}

	// The idle member is unaffected: it accepts and starts its own task.
	if _, err := assign.Execute(context.Background(), json.RawMessage(`{"member_name":"tester","subtask":"verify the build"}`)); err != nil {
		t.Fatalf("the idle member must accept its own task despite coder being busy: %v", err)
	}
	if backends["coder"].submits != 1 || backends["tester"].submits != 1 {
		t.Fatalf("submits must stay 1 each: coder=%d tester=%d", backends["coder"].submits, backends["tester"].submits)
	}

	// Both members report so the runtime drops both live tasks (test hygiene).
	if _, err := svc.report("coder", "", "built"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.report("tester", "", "verified"); err != nil {
		t.Fatal(err)
	}
}

// TestTeamE2EMemberReservationIsPerMember pins the §P1 reservation scope: two
// members assigned back-to-back each own exactly one live task under their own
// member id — the runtime's member reservation never crosses members.
func TestTeamE2EMemberReservationIsPerMember(t *testing.T) {
	svc, board, _ := newConcurrencyTeam(t)
	assign := leaderTool(t, newLeaderTaskTools(svc, "alpha", "lead"), "leader_assign_subtask")
	if _, err := assign.Execute(context.Background(), json.RawMessage(`{"member_name":"coder","subtask":"task one"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := assign.Execute(context.Background(), json.RawMessage(`{"member_name":"tester","subtask":"task two"}`)); err != nil {
		t.Fatal(err)
	}
	live, err := board.LoadLiveTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 {
		t.Fatalf("live = %+v, want both members' tasks live under their own member", live)
	}
	seen := map[string]bool{}
	for _, task := range live {
		if task.AssignedMember != "coder" && task.AssignedMember != "tester" {
			t.Fatalf("task %s owned by %s, want coder or tester", task.ID, task.AssignedMember)
		}
		if seen[task.AssignedMember] {
			t.Fatalf("member %s owns two live tasks", task.AssignedMember)
		}
		seen[task.AssignedMember] = true
	}
}

// TestTeamBackendsConcurrentDistinctBindsOverlapAssembly pins the W1 registry
// contract: two members' first assembly runs at once — a slow boot for one
// never serializes the fleet (a serialized registry can never show this).
func TestTeamBackendsConcurrentDistinctBindsOverlapAssembly(t *testing.T) {
	var (
		enteredMu sync.Mutex
		entered   []string
		bothIn    = make(chan struct{})
		release   = make(chan struct{})
		bothOnce  sync.Once
	)
	build := func(b team.MemberBinding) (control.SessionAPI, error) {
		enteredMu.Lock()
		entered = append(entered, b.MemberID)
		n := len(entered)
		enteredMu.Unlock()
		if n == 2 {
			bothOnce.Do(func() { close(bothIn) })
		}
		<-release
		return &taskBackendStub{}, nil
	}
	r := newTeamBackends(build, 4)
	var wg sync.WaitGroup
	var err1, err2 error
	wg.Add(2)
	go func() { defer wg.Done(); _, err1 = r.bind(team.MemberBinding{Team: "alpha", MemberID: "coder"}) }()
	go func() { defer wg.Done(); _, err2 = r.bind(team.MemberBinding{Team: "alpha", MemberID: "tester"}) }()
	select {
	case <-bothIn:
	case <-time.After(2 * time.Second):
		t.Fatal("two distinct members must enter assembly before either bind returns — W1 regression (assembly serialized)")
	}
	close(release)
	wg.Wait()
	if err1 != nil || err2 != nil {
		t.Fatalf("concurrent binds: coder=%v tester=%v", err1, err2)
	}
}

// TestTeamBackendsSameMemberConcurrentBindSharesOneBuild pins the W1 in-flight
// guard: a second bind for an assembling member joins that call — exactly one
// build reaches the fleet, and both binds return the same backend.
func TestTeamBackendsSameMemberConcurrentBindSharesOneBuild(t *testing.T) {
	firstEntered := make(chan struct{})
	release := make(chan struct{})
	var enters atomic.Int32
	build := func(b team.MemberBinding) (control.SessionAPI, error) {
		if enters.Add(1) == 1 {
			close(firstEntered)
		}
		<-release
		return &taskBackendStub{}, nil
	}
	r := newTeamBackends(build, 4)
	var wg sync.WaitGroup
	var err1, err2 error
	wg.Add(2)
	go func() { defer wg.Done(); _, err1 = r.bind(team.MemberBinding{Team: "alpha", MemberID: "coder"}) }()
	<-firstEntered // the first build is now in-flight
	go func() { defer wg.Done(); _, err2 = r.bind(team.MemberBinding{Team: "alpha", MemberID: "coder"}) }()
	close(release)
	wg.Wait()
	if enters.Load() != 1 {
		t.Fatalf("same-member concurrent binds must join one build, got %d assemblies", enters.Load())
	}
	if err1 != nil || err2 != nil {
		t.Fatalf("shared build binds: first=%v second=%v", err1, err2)
	}
}
