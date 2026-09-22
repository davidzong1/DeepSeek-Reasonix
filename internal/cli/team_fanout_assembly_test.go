package cli

// Acceptance tests for the B5 fix in TEAM_MEMBER_PARALLELISM_ROUTE.md: one
// leader dispatch to N members used to pay N serial boot.Builds, so the Nth
// member only started after the first N-1 had assembled.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/team"
	teamscheduler "reasonix/internal/team/scheduler"
)

// fanoutBackend is a minimal member backend the fan-out can drive: it accepts
// the turn and reports idle status, so a bound backend is never counted busy.
type fanoutBackend struct {
	control.SessionAPI
	mu      sync.Mutex
	submits int
}

func (b *fanoutBackend) SubmitUserTurnOrError(string, string) error {
	b.mu.Lock()
	b.submits++
	b.mu.Unlock()
	return nil
}
func (b *fanoutBackend) Cancel()                              {}
func (b *fanoutBackend) Running() bool                        { return false }
func (b *fanoutBackend) Turn() int                            { return 0 }
func (b *fanoutBackend) Compose(text string) string           { return text }
func (b *fanoutBackend) Close()                               {}
func (b *fanoutBackend) RuntimeStatus() control.RuntimeStatus { return control.RuntimeStatus{} }

func (b *fanoutBackend) submitCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.submits
}

// assemblyProbe stands in for boot.Build. It records how many assemblies are in
// flight at once and parks each one until the test releases it, so a serial
// fan-out can never reach a peak above one.
type assemblyProbe struct {
	entered chan struct{}
	release chan struct{}

	mu      sync.Mutex
	live    int
	peak    int
	failFor map[string]bool
	built   map[string]*fanoutBackend
}

func (p *assemblyProbe) build(b team.MemberBinding) (control.SessionAPI, error) {
	p.mu.Lock()
	p.live++
	if p.live > p.peak {
		p.peak = p.live
	}
	p.mu.Unlock()
	p.entered <- struct{}{}
	<-p.release
	p.mu.Lock()
	p.live--
	fail := p.failFor[b.MemberID]
	if p.built == nil {
		p.built = map[string]*fanoutBackend{}
	}
	backend := &fanoutBackend{}
	p.built[b.MemberID] = backend
	p.mu.Unlock()
	if fail {
		return nil, errors.New("no credential for " + b.MemberID)
	}
	return backend, nil
}

func (p *assemblyProbe) peakSeen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peak
}

func (p *assemblyProbe) backendFor(memberID string) *fanoutBackend {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.built[memberID]
}

// fanoutFixture builds a three-member team plus the leader over one board, with
// the registry's assemblies routed through probe.
func fanoutFixture(t *testing.T, probe *assemblyProbe) (*teamTaskService, *team.SQLiteStore) {
	t.Helper()
	root := t.TempDir()
	teamStore, err := team.NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	slots := []team.MemberSlot{{MemberID: "lead", Leader: true, Status: team.MemberStatusActive}}
	for _, id := range []string{"m1", "m2", "m3"} {
		slots = append(slots, team.MemberSlot{MemberID: id, Role: team.RoleCoder, Status: team.MemberStatusActive})
	}
	if err := teamStore.Save(team.TeamDoc{Document: team.Document{SchemaVersion: team.SchemaVersion}, Teams: []team.Team{{
		Name: "alpha", Template: slots,
	}}}); err != nil {
		t.Fatal(err)
	}
	board, err := team.NewSQLiteStore(context.Background(), filepath.Join(root, "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = board.Close() })
	registry := newTeamBackends(probe.build, defaultMaxTeamBackends)
	return newTeamTaskService(teamStore, board, "", registry.bind).forTeam("alpha"), board
}

// TestAssignSubtasksAssemblesMembersInParallel pins the fan-out: all three
// members' assemblies are in flight at the same time. A serial dispatch can
// never deliver the second entry while the first build is still parked, so the
// arrival of the third is the proof; the timeout is a deadlock guard, never a
// timing measurement.
func TestAssignSubtasksAssemblesMembersInParallel(t *testing.T) {
	probe := &assemblyProbe{entered: make(chan struct{}, 3), release: make(chan struct{})}
	svc, board := fanoutFixture(t, probe)
	members := []string{"m1", "m2", "m3"}

	done := make(chan error, 1)
	go func() {
		_, errs := svc.assignSubtasks(context.Background(), members, "build the widget", "ctx")
		done <- errors.Join(errs...)
	}()

	for i := range members {
		select {
		case <-probe.entered:
		case <-time.After(5 * time.Second):
			close(probe.release)
			t.Fatalf("only %d member assemblies started together: the fan-out still boots one member at a time", i)
		}
	}
	close(probe.release)
	if err := <-done; err != nil {
		t.Fatalf("concurrent fan-out: %v", err)
	}
	if got := probe.peakSeen(); got != len(members) {
		t.Fatalf("assembly peak = %d, want all %d members assembling at once", got, len(members))
	}
	for _, id := range members {
		backend := probe.backendFor(id)
		if backend == nil || backend.submitCount() != 1 {
			t.Fatalf("member %s must receive exactly its own turn", id)
		}
	}
	live, err := board.LoadLiveTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != len(members) {
		t.Fatalf("durable tasks = %d (%v), want one per member", len(live), live)
	}
	for _, task := range live {
		if !svc.driving(task.ID) {
			t.Fatalf("task %s is durable but nothing drives it", task.ID)
		}
	}
}

// TestAssignSubtasksKeepsSiblingsWhenOneMemberRefuses pins failure isolation: a
// member whose assembly is refused fails its own dispatch and nothing else — the
// siblings that started stay running, and the refused member's row stays live
// and re-dispatchable rather than being rolled back.
func TestAssignSubtasksKeepsSiblingsWhenOneMemberRefuses(t *testing.T) {
	released := make(chan struct{})
	close(released)
	probe := &assemblyProbe{
		entered: make(chan struct{}, 3), release: released,
		failFor: map[string]bool{"m2": true},
	}
	svc, board := fanoutFixture(t, probe)

	assignments, errs := svc.assignSubtasks(context.Background(), []string{"m1", "m2", "m3"}, "build the widget", "ctx")

	if errs[1] == nil {
		t.Fatal("the refused member must report its own failure")
	}
	if !errors.Is(errs[1], teamscheduler.ErrStartFailed) {
		t.Fatalf("refused assembly = %v, want it to wrap ErrStartFailed", errs[1])
	}
	for _, i := range []int{0, 2} {
		if errs[i] != nil {
			t.Fatalf("member %s was rolled back by its sibling's failure: %v", []string{"m1", "m2", "m3"}[i], errs[i])
		}
		if assignments[i].TaskID == "" || !svc.driving(assignments[i].TaskID) {
			t.Fatalf("member %s must keep its running task, got %+v", []string{"m1", "m2", "m3"}[i], assignments[i])
		}
	}

	// The refused member's row is durable and undriven: exactly what the serial
	// dispatch left behind, and what the leader's retry re-drives.
	live, err := board.LoadLiveTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var refused *team.Task
	for i := range live {
		if live[i].AssignedMember == "m2" {
			refused = &live[i]
		}
	}
	if refused == nil {
		t.Fatal("the refused member must still own a durable row the leader can retry")
	}
	if refused.Status != team.TaskStatusAssigned {
		t.Fatalf("refused row status = %s, want assigned (re-dispatchable)", refused.Status)
	}
	if svc.driving(refused.ID) {
		t.Fatalf("task %s must not be reported as driving: its dispatch was refused", refused.ID)
	}
	if got := len(live); got != 3 {
		t.Fatalf("durable tasks = %d, want all three rows (%s)", got, fmt.Sprint(live))
	}
}
