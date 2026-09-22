package sessionui

import (
	"context"
	"encoding/json"
)

// ConflictPayloads makes losing CAS writes recoverable after renderer restart.
func (s *Store) ConflictPayloads(ctx context.Context, kind, key string) ([]json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.open(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM conflicts WHERE kind=? AND key=? ORDER BY id DESC`, kind, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(payload))
	}
	return out, rows.Err()
}
