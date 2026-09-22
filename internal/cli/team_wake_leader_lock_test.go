package cli

// Acceptance test for the B1 fix in TEAM_MEMBER_PARALLELISM_ROUTE.md: several
// members completing at once is the common case, and the leader wakeup used to
// hold its delivery lock across the durable read that stamps the wake.

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/team"
)

// leaderStampProbe stands in for the store-backed leader read. It records how
// many resolutions are in flight at once and parks them until the test releases
// them, so a resolution taken under the delivery lock can never reach two.
type leaderStampProbe struct {
	entered chan struct{}
	release chan struct{}

	mu   sync.Mutex
	live int
	peak int
}

func (p *leaderStampProbe) resolve() team.Identity {
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
	p.mu.Unlock()
	return team.Identity{MemberID: "lead", Role: string(team.RoleCoder), Generation: 1}
}

func (p *leaderStampProbe) peakSeen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peak
}

// wakeServiceFixture builds the smallest durable service: one team with a
// leader and a member slot, its board, and the task service over both.
func wakeServiceFixture(t *testing.T) *teamTaskService {
	t.Helper()
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
	t.Cleanup(func() { _ = board.Close() })
	service := newTeamTaskService(teamStore, board, "", func(team.MemberBinding) (control.SessionAPI, error) {
		return &taskBackendStub{}, nil
	})
	return service.forTeam("alpha")
}

// TestWakeLeaderResolvesTheStampOutsideItsLock pins the fix: two wakeups racing
// each other both sit inside the stamp resolution at the same time. With the
// read under wakeMu the second could never arrive, so the second entry is the
// proof the read left the lock.
func TestWakeLeaderResolvesTheStampOutsideItsLock(t *testing.T) {
	svc := wakeServiceFixture(t)
	probe := &leaderStampProbe{entered: make(chan struct{}, 2), release: make(chan struct{})}
	svc.leaderStamp = probe.resolve
	// Establish the leader's cursor first, exactly as opening the leader window
	// does, so the wakes below are the only events it can surface.
	wire := &teamInboxWire{board: svc.board}
	if got := wire.consumeWakeups("lead"); got != nil {
		t.Fatalf("first cursor read must be quiet, got %v", got)
	}

	done := make(chan error, 2)
	go func() { done <- svc.wakeLeader("task alpha-1 reported") }()
	go func() { done <- svc.wakeLeader("task alpha-2 reported") }()

	// The timeout is a deadlock guard for the failure mode, never a timing
	// measurement: serialized code can never deliver the second entry at all.
	for i := range 2 {
		select {
		case <-probe.entered:
		case <-time.After(5 * time.Second):
			close(probe.release)
			t.Fatalf("only %d leader-stamp resolutions got past the wake lock: the durable read is still inside it", i)
		}
	}
	close(probe.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("wake delivery: %v", err)
		}
	}
	if got := probe.peakSeen(); got != 2 {
		t.Fatalf("concurrent leader-stamp resolutions = %d, want 2", got)
	}

	// The stamp still has to reach the board: moving the read must not lose the
	// identity the leader's cursor selects.
	reasons := wire.consumeWakeups("lead")
	if len(reasons) != 2 {
		t.Fatalf("leader wakeups = %d (%v), want both writes stamped with the resolved leader id", len(reasons), reasons)
	}
	for _, want := range []string{"alpha-1", "alpha-2"} {
		if !strings.Contains(strings.Join(reasons, "\n"), want) {
			t.Fatalf("wakeups %v must carry %q", reasons, want)
		}
	}
}
