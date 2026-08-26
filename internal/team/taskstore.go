package team

import (
	"context"
	"database/sql"
	"errors"
)

// TaskStore persists team tasks across restarts (§3.6 recovery). SaveTask
// upserts the task's current lifecycle; LoadLiveTasks returns the tasks a
// kill/reopen must re-drive — assigned or running, never terminal. The store
// is the task truth; the blackboard records are observability.
type TaskStore interface {
	SaveTask(ctx context.Context, task Task) error
	LoadTask(ctx context.Context, id TaskID) (Task, error)
	LoadLiveTasks(ctx context.Context) ([]Task, error)
}

// ErrTaskNotFound reports a LoadTask for an id the store never saved.
var ErrTaskNotFound = errors.New("team: task not found")

// SaveTask upserts one task. The write is idempotent on task_id, so a
// replaying recovery loop can never duplicate a task.
func (s *SQLiteStore) SaveTask(ctx context.Context, task Task) error {
	return s.inTx(ctx, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `
			INSERT INTO team_tasks (task_id, require_role, description, context_ref,
				expected, report_ref, checkpoint_ref, status, assigned_member, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(task_id) DO UPDATE SET
				require_role = excluded.require_role,
				description = excluded.description,
				context_ref = excluded.context_ref,
				expected = excluded.expected,
				report_ref = excluded.report_ref,
				checkpoint_ref = excluded.checkpoint_ref,
				status = excluded.status,
				assigned_member = excluded.assigned_member,
				created_at = excluded.created_at`,
			task.ID, task.RequireRole, task.Desc, task.ContextRef, task.Expected,
			task.ReportRef, task.CheckpointRef, task.Status, task.AssignedMember, task.CreatedAt)
		return err
	})
}

// LoadTask returns one task by id.
func (s *SQLiteStore) LoadTask(ctx context.Context, id TaskID) (Task, error) {
	var t Task
	err := s.db.QueryRowContext(ctx, `
		SELECT task_id, require_role, description, context_ref, expected,
			report_ref, checkpoint_ref, status, assigned_member, created_at
		FROM team_tasks WHERE task_id = ?`, id).
		Scan(&t.ID, &t.RequireRole, &t.Desc, &t.ContextRef, &t.Expected,
			&t.ReportRef, &t.CheckpointRef, &t.Status, &t.AssignedMember, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, err
	}
	return t, nil
}

// LoadLiveTasks returns every task in a live state (assigned or running) —
// the kill/reopen recovery set. Terminal states are never re-driven.
func (s *SQLiteStore) LoadLiveTasks(ctx context.Context) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT task_id, require_role, description, context_ref, expected,
			report_ref, checkpoint_ref, status, assigned_member, created_at
		FROM team_tasks WHERE status IN (?, ?) ORDER BY created_at`,
		TaskStatusAssigned, TaskStatusRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.RequireRole, &t.Desc, &t.ContextRef, &t.Expected,
			&t.ReportRef, &t.CheckpointRef, &t.Status, &t.AssignedMember, &t.CreatedAt); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// Compile-time proof: the store's board database carries the task truth.
var _ TaskStore = (*SQLiteStore)(nil)
