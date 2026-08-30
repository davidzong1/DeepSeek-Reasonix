package agentruntime

// Concurrent acceptance tests for the dual-member execution contract (§P2):
// two members run turns in parallel on independent backends, and teardown
// or a slow assembly never touches another member's slot.

import (
	"context"
	"sync"
	"testing"

	"reasonix/internal/team"
)

// TestRuntimeTwoMembersConcurrentStartsIndependent drives alpha and beta in
// parallel from a nil board (in-memory runtime): both assemble, both submit
// exactly one injected turn, and neither double-drives the other.
func TestRuntimeTwoMembersConcurrentStartsIndependent(t *testing.T) {
	rt, agents := newTestRuntime(t, nil)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, id := range []string{"alpha", "beta"} {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			task := team.Task{ID: team.TaskID("t-" + id), Status: team.TaskStatusAssigned}
			errs[i] = rt.Start(context.Background(), task, team.Member{ID: id})
		}(i, id)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent start err #%d = %v", i, err)
		}
	}
	for _, id := range []string{"alpha", "beta"} {
		if got := len(agents[id].submitted); got != 1 {
			t.Fatalf("%s submitted %d turns, want exactly 1", id, got)
		}
	}
}

// TestRuntimeCompleteOneMemberKeepsOtherOwned pins slot independence: a report
// on alpha's task drops alpha's reservation only — beta's task stays live, and
// afterwards each member can take a fresh task.
func TestRuntimeCompleteOneMemberKeepsOtherOwned(t *testing.T) {
	rt, agents := newTestRuntime(t, nil)
	for _, tc := range []struct{ id, task string }{
		{"alpha", "t1"}, {"beta", "t2"},
	} {
		if err := rt.Start(context.Background(), team.Task{ID: team.TaskID(tc.task), Status: team.TaskStatusAssigned}, team.Member{ID: tc.id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := rt.Complete("t1", "done"); err != nil {
		t.Fatal(err)
	}
	if own := rt.byMember["beta"]; own != "t2" {
		t.Fatalf("beta must still own t2, got %q (alpha's report leaked across)", own)
	}
	if err := rt.Complete("t2", "done"); err != nil {
		t.Fatalf("beta's report must still succeed, got %v", err)
	}
	// Both members are free again and can take fresh tasks.
	for _, id := range []string{"alpha", "beta"} {
		if err := rt.Start(context.Background(), team.Task{ID: team.TaskID("t3-" + id), Status: team.TaskStatusAssigned}, team.Member{ID: id}); err != nil {
			t.Fatalf("%s restart after both reports = %v", id, err)
		}
		if got := len(agents[id].submitted); got != 2 {
			t.Fatalf("%s submitted %d turns, want 2", id, got)
		}
	}
}

// slowFor makes the agents lookup stall exactly the named member, standing in
// for a backend whose assembly (boot.Build) is in flight.
type slowFor struct {
	block map[string]bool
	mu    sync.Mutex
	wait  *sync.WaitGroup
	inner map[string]*stubAgent
}

func (s *slowFor) get(member string) (AgentAPI, error) {
	s.mu.Lock()
	blocked := s.block[member]
	s.mu.Unlock()
	if blocked {
		s.wait.Wait()
	}
	if _, ok := s.inner[member]; !ok {
		s.inner[member] = &stubAgent{}
	}
	return s.inner[member], nil
}

// TestRuntimeAssemblyDelayDoesNotBlockOtherMember pins the async-assembly
// contract: beta's start completes while alpha's agent lookup is still
// blocked — a slow member assembly never serializes the fleet.
func TestRuntimeAssemblyDelayDoesNotBlockOtherMember(t *testing.T) {
	var release sync.WaitGroup
	release.Add(1)
	agents := &slowFor{block: map[string]bool{"alpha": true}, wait: &release, inner: map[string]*stubAgent{}}
	rt := NewRuntime(agents.get, nil, "team:T", nil)

	alphaDone := make(chan error, 1)
	go func() {
		alphaDone <- rt.Start(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusAssigned}, team.Member{ID: "alpha"})
	}()

	betaDone := make(chan error, 1)
	go func() {
		betaDone <- rt.Start(context.Background(), team.Task{ID: "t2", Status: team.TaskStatusAssigned}, team.Member{ID: "beta"})
	}()

	select {
	case err := <-betaDone:
		if err != nil {
			t.Fatalf("beta start while alpha assembles = %v", err)
		}
	case err := <-alphaDone:
		t.Fatalf("alpha finished (%v) before beta even started — assembly serialized the fleet", err)
	}

	release.Done()
	if err := <-alphaDone; err != nil {
		t.Fatalf("alpha start after release = %v", err)
	}
	if got := len(agents.inner["beta"].submitted); got != 1 {
		t.Fatalf("beta submitted %d turns, want 1", got)
	}
}
