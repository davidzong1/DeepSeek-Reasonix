package team

import (
	"context"
	"time"
)

// BindingPersister is the durable half of the binding registry (route
// §4.3): the registry stays the single-writer authority in memory, the
// store keeps every record change across server restarts.
type BindingPersister interface {
	SaveBinding(ctx context.Context, rec BindRecord) error
	LoadBindings(ctx context.Context) ([]BindRecord, error)
}

// SaveBinding upserts one member's record into board_bindings. Unbound
// records stay, so a restarted server replays the unbind instead of
// reporting ErrNotBound for a member that already released its task.
func (s *SQLiteStore) SaveBinding(ctx context.Context, rec BindRecord) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO board_bindings (member_id, leader_id, generation, status, task_id, bound_at)
		 VALUES (?,?,?,?,?,?)
		 ON CONFLICT(member_id) DO UPDATE SET
		   leader_id = excluded.leader_id, generation = excluded.generation,
		   status = excluded.status, task_id = excluded.task_id,
		   bound_at = excluded.bound_at`,
		rec.MemberID, rec.LeaderID, rec.Generation, string(rec.Status),
		string(rec.TaskID), rec.BoundAt.Format(time.RFC3339Nano))
	return err
}

// LoadBindings returns every persisted record, ordered by member id. A
// member missing from the result was never bound.
func (s *SQLiteStore) LoadBindings(ctx context.Context) ([]BindRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT member_id, leader_id, generation, status, task_id, bound_at
		   FROM board_bindings ORDER BY member_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BindRecord
	for rows.Next() {
		var rec BindRecord
		var status, taskID, boundAt string
		if err := rows.Scan(&rec.MemberID, &rec.LeaderID, &rec.Generation,
			&status, &taskID, &boundAt); err != nil {
			return nil, err
		}
		rec.Status = BindStatus(status)
		rec.TaskID = TaskID(taskID)
		if rec.BoundAt, err = time.Parse(time.RFC3339Nano, boundAt); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

var _ BindingPersister = (*SQLiteStore)(nil)
