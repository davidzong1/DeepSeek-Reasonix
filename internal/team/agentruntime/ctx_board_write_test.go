package agentruntime

// Acceptance tests for the B2 fix in TEAM_MEMBER_PARALLELISM_ROUTE.md: the
// runtime's task-path board write used to run on context.Background(), so a
// member's write could hold a peer's completion for the board's busy_timeout.

import (
	"context"
	"errors"
	"testing"
	"time"

	"reasonix/internal/team"
)

// boardWrite is one observed write: the context the board was handed and, read
// while it was still live, how long that context had left to run.
type boardWrite struct {
	ctx context.Context
	ttl time.Duration
}

// blockingBoard is a BoardStore whose Append reports the context it was handed
// and completes only when that context does — standing in for a board
// serialized behind another member's write. Every other method is never called
// by the paths under test, so the embedded interface stays nil.
type blockingBoard struct {
	team.BoardStore
	got chan boardWrite
}

func (b *blockingBoard) Append(ctx context.Context, _ team.AppendInput) (team.BoardEvent, error) {
	ttl := time.Duration(-1)
	if deadline, ok := ctx.Deadline(); ok {
		ttl = time.Until(deadline)
	}
	select {
	case b.got <- boardWrite{ctx: ctx, ttl: ttl}:
	default:
	}
	<-ctx.Done()
	return team.BoardEvent{}, ctx.Err()
}

// TestRuntimeBoardWriteBorrowsTheCallersContext pins the cancellation half: the
// task state write runs under the caller's context, so a window that stops its
// turn abandons the write instead of waiting out the board.
func TestRuntimeBoardWriteBorrowsTheCallersContext(t *testing.T) {
	board := &blockingBoard{got: make(chan boardWrite, 8)}
	rt, _ := newTestRuntime(t, board)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- rt.Start(ctx, team.Task{ID: "t1", Status: team.TaskStatusAssigned}, team.Member{ID: "alpha"})
	}()

	got := <-board.got
	if got.ctx == context.Background() {
		t.Fatal("the board write ran on context.Background(): the caller cannot abandon it")
	}
	cancel()
	if !errors.Is(got.ctx.Err(), context.Canceled) {
		t.Fatalf("cancelling the caller left the board write alive: err = %v", got.ctx.Err())
	}
	// The write is observability: abandoning it costs the event, never the task
	// state move, so the start still settles as the runtime reports it.
	if err := <-done; err != nil {
		t.Fatalf("start under a cancelled caller = %v, want the runtime's own verdict", err)
	}
}

// TestRuntimeBoardWriteIsBoundedWithoutACallerContext pins the other half:
// Complete has no caller context by design — a report has to land even when its
// turn is already gone — so its board write runs under the runtime's own
// ceiling instead of the board's uncancellable clock.
func TestRuntimeBoardWriteIsBoundedWithoutACallerContext(t *testing.T) {
	board := &blockingBoard{got: make(chan boardWrite, 8)}
	rt, _ := newTestRuntime(t, board)
	rt.writeTimeout = 30 * time.Millisecond
	if err := rt.Start(context.Background(), team.Task{ID: "t1", Status: team.TaskStatusAssigned}, team.Member{ID: "alpha"}); err != nil {
		t.Fatal(err)
	}
	<-board.got // the running event's write, already bounded by the same ceiling

	if err := rt.Complete("t1", "done"); err != nil {
		t.Fatal(err)
	}
	got := <-board.got
	if got.ttl <= 0 {
		t.Fatal("the report write's context carries no deadline: it rides the board's own clock")
	}
	if got.ttl > rt.writeTimeout {
		t.Fatalf("the report write was granted %v, want within the runtime's %v ceiling", got.ttl, rt.writeTimeout)
	}
}
