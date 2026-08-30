package scheduler

// Concurrent acceptance tests for the dual-member scheduler contract (§P2):
// two live scheduled tasks genuinely run in parallel, and a start failure on
// one member never infects the other member's in-flight start.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"reasonix/internal/team"
)

const p2AssembleDelay = 20 * time.Millisecond

// slowExecutor records concurrent executor calls; a nonzero assemble delay
// slows the Start side so the async path is observable.
type slowExecutor struct {
	delay time.Duration
	mu    sync.Mutex
	start []string
}

func (e *slowExecutor) Start(_ context.Context, task team.Task, _ team.Member) error {
	if e.delay > 0 {
		time.Sleep(e.delay)
	}
	e.mu.Lock()
	e.start = append(e.start, string(task.ID))
	e.mu.Unlock()
	return nil
}

func (e *slowExecutor) Cancel(taskID team.TaskID) error { return nil }
func (e *slowExecutor) Resume(_ context.Context, task team.Task, _ team.Member) error {
	return nil
}

// TestRuntimeSchedulerConcurrentAssignTwoMembers assigns two tasks to two
// members in parallel: both assignments return running, each executor starts
// its own task, and a slow cast on one member delays its Assign call only —
// the fleet is never serialized (async assembly).
func TestRuntimeSchedulerConcurrentAssignTwoMembers(t *testing.T) {
	exec := &slowExecutor{delay: p2AssembleDelay}
	s := NewRuntimeScheduler(exec)
	fleet := []team.Member{
		idleMember("alpha", team.RoleCoder),
		idleMember("beta", team.RoleTester),
	}
	a, b := team.Task{ID: "t1", RequireRole: team.RoleCoder}, team.Task{ID: "t2", RequireRole: team.RoleTester}
	results := make([]Assignment, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); results[0], errs[0] = s.Assign(a, fleet) }()
	go func() { defer wg.Done(); results[1], errs[1] = s.Assign(b, fleet) }()
	started := time.Now()
	wg.Wait()
	elapsed := time.Since(started)

	for i, err := range errs {
		if err != nil {
			t.Fatalf("assign #%d = %v", i, err)
		}
		if results[i].Status != StatusRunning {
			t.Fatalf("assign #%d status = %s, want running", i, results[i].Status)
		}
	}
	if len(exec.start) != 2 {
		t.Fatalf("executor started %d tasks, want 2 (%v)", len(exec.start), exec.start)
	}
	// One member's 20ms assembly delay must not serialize the other Assign:
	// both finish within ~2x the delay, nowhere near a full second.
	if elapsed > 2*p2AssembleDelay+50*time.Millisecond {
		t.Fatalf("concurrent assigns took %v, want async (the slow cast must not serialize)", elapsed)
	}
}

// gatedStartErr fails exactly one named task's start, signaling when its
// executor was reached so the test can prove that failure stays local.
type gatedStartErr struct {
	blocked chan struct{} // closed once the failing task reaches Start
	release chan struct{} // closing lets the failing Start return its error
	fail    string        // task id that fails
	mu      sync.Mutex
	started []string
}

func (e *gatedStartErr) Start(_ context.Context, task team.Task, _ team.Member) error {
	if string(task.ID) == e.fail {
		close(e.blocked)
		<-e.release
		return errSchedulerStart
	}
	e.mu.Lock()
	e.started = append(e.started, string(task.ID))
	e.mu.Unlock()
	return nil
}

func (e *gatedStartErr) Cancel(taskID team.TaskID) error { return nil }
func (e *gatedStartErr) Resume(_ context.Context, task team.Task, _ team.Member) error {
	return nil
}

var errSchedulerStart = errors.New("member backend refused the task")

// TestRuntimeSchedulerConcurrentStartFailureIsIsolated pins the isolation
// contract: while t1's start is blocked inside the executor, t2's start on a
// different member completes as running — the failure stays local and the
// synchronous member is never touched by the other member's refusal.
func TestRuntimeSchedulerConcurrentStartFailureIsIsolated(t *testing.T) {
	exec := &gatedStartErr{
		blocked: make(chan struct{}),
		release: make(chan struct{}),
		fail:    "t1",
	}
	s := NewRuntimeScheduler(exec)
	fleet := []team.Member{
		idleMember("alpha", team.RoleCoder),
		idleMember("beta", team.RoleTester),
	}
	var err1 error
	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		_, err1 = s.Assign(team.Task{ID: "t1", RequireRole: team.RoleCoder}, fleet)
	}()

	<-exec.blocked // t1's start is now blocked inside the executor
	_, err2 := s.Assign(team.Task{ID: "t2", RequireRole: team.RoleTester}, fleet)
	if err2 != nil {
		t.Fatalf("t2's start must complete while t1 is blocked, got %v", err2)
	}
	close(exec.release)
	<-done1
	if err1 == nil {
		t.Fatal("t1's start failure must surface")
	}
	if len(exec.started) != 1 || exec.started[0] != "t2" {
		t.Fatalf("executor started = %v, want only t2", exec.started)
	}
}
