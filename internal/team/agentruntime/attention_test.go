package agentruntime

// Signal-side acceptance tests for TEAM_LEADER_WAIT_ROUTE.md §8.5/§8.7: a
// durable state move is reported in-process before its board write, so a slow
// board cannot delay waking a leader that is waiting on it.

import (
	"context"
	"strings"
	"testing"
	"time"

	"reasonix/internal/team"
)

// gatedBoard holds the board's wakeup append until the test releases it, so the
// order between the in-process report and that append is observable. Every
// other append — the task's own assignment and report rows — passes through: the
// gate is on the wakeup, which is the write a waiting leader must not wait for.
type gatedBoard struct {
	team.BoardStore
	entered chan string
	gate    chan struct{}
}

func (b *gatedBoard) Append(ctx context.Context, in team.AppendInput) (team.BoardEvent, error) {
	if in.Kind != team.EventWakeup {
		return team.BoardEvent{BoardID: in.BoardID, EventID: in.EventID, Kind: in.Kind, Summary: in.Summary}, nil
	}
	select {
	case b.entered <- in.Summary:
	default:
	}
	select {
	case <-b.gate:
	case <-ctx.Done():
		return team.BoardEvent{}, ctx.Err()
	}
	return team.BoardEvent{BoardID: in.BoardID, EventID: in.EventID, Kind: in.Kind, Summary: in.Summary}, nil
}

// attentionReport is one observed call to the attention hook.
type attentionReport struct {
	kind, id, summary string
}

// observeAttention registers a collecting hook and returns its channel.
func observeAttention(rt *Runtime) chan attentionReport {
	got := make(chan attentionReport, 8)
	rt.AddAttention(func(kind, id, summary string) {
		select {
		case got <- attentionReport{kind: kind, id: id, summary: summary}:
		default:
		}
	})
	return got
}

// startTestTask drives one task onto member alpha, which is what makes it
// completable: Complete only moves a task the runtime is driving.
func startTestTask(t *testing.T, rt *Runtime, id string) {
	t.Helper()
	task := team.Task{ID: team.TaskID(id), Desc: "build the widget", Status: team.TaskStatusAssigned}
	if err := rt.Start(context.Background(), task, team.Member{ID: "alpha"}); err != nil {
		t.Fatalf("start %s = %v", id, err)
	}
}

// TestRuntimeReportsCompletionWhileTheBoardWriteIsInFlight pins the ordering the
// wait route depends on: the report reaches the in-process hook while the board's
// wakeup append is still held, so a waiting leader is released by the state move
// rather than by the board's clock.
func TestRuntimeReportsCompletionWhileTheBoardWriteIsInFlight(t *testing.T) {
	board := &gatedBoard{entered: make(chan string, 4), gate: make(chan struct{})}
	rt, _ := newTestRuntime(t, board)
	wake := NewBoardWakeStamped(board, "team:T")
	rt.AddWakeup(func(reason string) error {
		return wake(reason, team.Identity{MemberID: "alpha", Generation: 1})
	})
	reports := observeAttention(rt)
	startTestTask(t, rt, "t1")

	done := make(chan error, 1)
	go func() { done <- rt.Complete("t1", "widget done") }()

	select {
	case got := <-reports:
		if got.kind != AttentionReport || got.id != "t1" || got.summary != "task t1 reported" {
			t.Fatalf("report = %+v, want the completion's kind, task id and reason", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the completion was not reported while its board write was still in flight")
	}
	// The wakeup append is gated shut, so it cannot have completed before the
	// report above: whatever the scheduler interleaving, the report is observed
	// while the board write is still pending.
	select {
	case summary := <-board.entered:
		if !strings.Contains(summary, "reported") {
			t.Fatalf("the held write = %q, want the completion's wakeup", summary)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the completion's board wakeup never reached the board")
	}
	close(board.gate)
	if err := <-done; err != nil {
		t.Fatalf("complete = %v, want it to settle once the board answered", err)
	}
}

// TestRuntimeReportsCancelAndRefusedDispatch pins the other two occurrences the
// bus carries: a canceled task and a turn a member's backend refused. Both are
// edge reports with the same reason text their durable wakeup row carries.
func TestRuntimeReportsCancelAndRefusedDispatch(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		rt, _ := newTestRuntime(t, newTestBoard(t))
		reports := observeAttention(rt)
		startTestTask(t, rt, "t1")
		if err := rt.Cancel("t1"); err != nil {
			t.Fatalf("cancel = %v", err)
		}
		got := <-reports
		if got.kind != AttentionCancel || got.id != "t1" || got.summary != "task t1 canceled" {
			t.Fatalf("report = %+v, want the cancel of t1", got)
		}
	})

	t.Run("refused dispatch", func(t *testing.T) {
		rt, _ := newTestTaskBoard(t)
		rt.agents = func(string) (AgentAPI, error) { return &refusingAgent{}, nil }
		reports := observeAttention(rt)
		task := team.Task{ID: "t1", Desc: "build the widget", Status: team.TaskStatusAssigned}
		if err := rt.Start(context.Background(), task, team.Member{ID: "alpha"}); err == nil {
			t.Fatal("a refused turn must be reported as an error to its caller")
		}
		got := <-reports
		want := "task t1 was refused by its member (busy); reassign or retry"
		if got.kind != AttentionDispatchFailed || got.id != "t1" || got.summary != want {
			t.Fatalf("report = %+v, want the refusal of t1", got)
		}
	})
}
