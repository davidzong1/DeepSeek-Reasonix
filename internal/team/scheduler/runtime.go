package scheduler

import (
	"context"
	"errors"
	"fmt"

	"reasonix/internal/team"
)

// Executor runs one assigned task on a member's agent backend (§3.5). The
// scheduler stays a strategy layer: it picks the member and hands the task
// to the executor, which owns agent start/cancel/resume against the real
// runtime. The runtime stage (internal/team/agentruntime) implements this
// interface; the placeholder never does.
type Executor interface {
	Start(ctx context.Context, task team.Task, member team.Member) error
	Cancel(taskID team.TaskID) error
	Resume(ctx context.Context, task team.Task, member team.Member) error
}

var (
	// ErrNoExecutor reports an assignment with no runtime wired in.
	ErrNoExecutor = errors.New("scheduler: no executor")
	// ErrStartFailed reports an executor that refused or failed to start the
	// task: the assignment is not recorded as running.
	ErrStartFailed = errors.New("scheduler: task start failed")
)

// Status beyond the placeholder ledger (§3.7): a live assignment is running,
// or terminal through cancel or failure.
const (
	StatusRunning  Status = "running"
	StatusCanceled Status = "canceled"
	StatusFailed   Status = "failed"
)

// RuntimeScheduler is the live scheduler (§3.7): strategy pick plus real
// execution. Assign starts the task on the picked member and reports the
// outcome in the ledger entry; a refused or failed start returns an error
// and writes no entry. An optional durable store persists restores that end
// failed, so a kill/reopen never re-drives them.
type RuntimeScheduler struct {
	exec  Executor
	store team.TaskStore
}

// NewRuntimeScheduler returns a scheduler that executes through exec.
func NewRuntimeScheduler(exec Executor) *RuntimeScheduler {
	return &RuntimeScheduler{exec: exec}
}

// SetTaskStore installs the durable task store for failed restores. Live
// states are persisted by the executor; only the terminal failed/canceled
// restores are written here.
func (s *RuntimeScheduler) SetTaskStore(store team.TaskStore) {
	if store != nil {
		s.store = store
	}
}

// Assign picks a member by the §3.5 strategy order and starts the task for
// real. Status is running, never a fake pending: the executor ran. The caller's
// context reaches the executor, because the writes it gates — the durable
// "running" row and the member's turn submission — belong to the caller that
// asked for the assignment, not to the scheduler: passing context.Background()
// here made a contended board write impossible to abandon for its whole
// busy_timeout window. A nil context is treated as uncancellable.
func (s *RuntimeScheduler) Assign(ctx context.Context, task team.Task, fleet []team.Member) (Assignment, error) {
	if s.exec == nil {
		return Assignment{}, ErrNoExecutor
	}
	m, ok := pick(task, fleet)
	if !ok {
		return Assignment{}, fmt.Errorf("%w: task %s requires role %q", ErrNoSuitableMember, task.ID, task.RequireRole)
	}
	if err := s.exec.Start(orBackground(ctx), task, m); err != nil {
		return Assignment{}, fmt.Errorf("%w: %w", ErrStartFailed, err)
	}
	return Assignment{
		TaskID:   task.ID,
		MemberID: m.ID,
		Status:   StatusRunning,
		Note:     fmt.Sprintf("%s started on %s (role %s, state %s)", task.ID, m.ID, m.Role, m.State),
	}, nil
}

// orBackground keeps a nil caller context from reaching the executor's durable
// writes, where it would panic instead of reporting that nothing was
// cancellable to begin with.
func orBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// Cancel stops one running task through the executor and records the ledger
// outcome; a task unknown to the executor is an error, not a silent no-op.
func (s *RuntimeScheduler) Cancel(taskID team.TaskID) (Assignment, error) {
	if s.exec == nil {
		return Assignment{}, ErrNoExecutor
	}
	if err := s.exec.Cancel(taskID); err != nil {
		return Assignment{}, fmt.Errorf("scheduler: cancel %s: %w", taskID, err)
	}
	return Assignment{TaskID: taskID, Status: StatusCanceled, Note: string(taskID) + " canceled"}, nil
}

// Restore re-drives interrupted executions after a restart (§4 recovery): a
// task persisted in a live state is resumed on its member when the member is
// still in the fleet, else marked failed. No fake pending — every restored
// task ends running or failed. The caller's context reaches the executor and
// the failed-restore write for the same reason Assign's does.
func (s *RuntimeScheduler) Restore(ctx context.Context, tasks []team.Task, fleet []team.Member) ([]Assignment, error) {
	if s.exec == nil {
		return nil, ErrNoExecutor
	}
	ctx = orBackground(ctx)
	byID := make(map[string]team.Member, len(fleet))
	for _, m := range fleet {
		byID[m.ID] = m
	}
	restored := make([]Assignment, 0, len(tasks))
	for _, t := range tasks {
		switch t.Status {
		case team.TaskStatusAssigned, team.TaskStatusRunning:
			m, ok := byID[t.AssignedMember]
			if !ok {
				s.persistRestoreFailure(ctx, t, "member "+t.AssignedMember+" no longer in fleet")
				restored = append(restored, Assignment{TaskID: t.ID, Status: StatusFailed,
					Note: string(t.ID) + " failed: member " + t.AssignedMember + " no longer in fleet"})
				continue
			}
			if err := s.exec.Resume(ctx, t, m); err != nil {
				s.persistRestoreFailure(ctx, t, err.Error())
				restored = append(restored, Assignment{TaskID: t.ID, Status: StatusFailed,
					Note: string(t.ID) + " failed to resume: " + err.Error()})
				continue
			}
			restored = append(restored, Assignment{TaskID: t.ID, MemberID: m.ID, Status: StatusRunning,
				Note: string(t.ID) + " resumed on " + m.ID})
		}
	}
	return restored, nil
}

// persistRestoreFailure writes a failed restore to the durable store through
// the migration map: a running task fails, an assigned task cancels — the
// map has no assigned -> failed edge. Best-effort: the ledger Assignment
// already records the failure, and the store stays live for a later attempt.
func (s *RuntimeScheduler) persistRestoreFailure(ctx context.Context, t team.Task, reason string) {
	if s.store == nil {
		return
	}
	to := team.TaskStatusFailed
	if t.Status == team.TaskStatusAssigned {
		to = team.TaskStatusCanceled
	}
	if err := team.TransitionTask(t.Status, to); err != nil {
		return
	}
	t.Status = to
	_ = s.store.SaveTask(ctx, t)
}
