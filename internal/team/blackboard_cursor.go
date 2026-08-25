package team

import (
	"context"
	"database/sql"
	"errors"
)

// CursorState is one consumer's persisted read position (route §2.2). A
// missing row means the first read; row existence doubles as the
// "initialized" bit the page protocol cannot carry.
type CursorState struct {
	BoardID    string
	ConsumerID string
	Generation uint64
	LastSeq    int64
}

// ErrCursorNotFound reports that the consumer has no cursor row yet: the
// first read starts from zero (route §2.3).
var ErrCursorNotFound = errors.New("team: board cursor not found: consumer has no persisted position")

// GetCursor returns one consumer's read position. A private board answers
// only its owner's cursor; the row's existence is never revealed to
// others (route §6.2).
func (s *SQLiteStore) GetCursor(ctx context.Context, boardID, consumerID string) (CursorState, error) {
	if err := s.checkAccess(boardID, Identity{MemberID: consumerID}); err != nil {
		return CursorState{}, err
	}
	var c CursorState
	var updatedAt string
	err := s.db.QueryRowContext(ctx,
		`SELECT board_id, consumer_id, generation, last_seq, updated_at
		   FROM board_cursors WHERE board_id = ? AND consumer_id = ?`,
		boardID, consumerID).Scan(&c.BoardID, &c.ConsumerID, &c.Generation, &c.LastSeq, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CursorState{}, ErrCursorNotFound
	}
	if err != nil {
		return CursorState{}, err
	}
	return c, nil
}

// SQLiteCursorStore is the member-side CursorStore over the store's
// board_cursors table (route §2.3): the member cursor and the server
// mirror are one row, so a restart continues where the member left off.
// Generation is the member window's — a window change (generation bump)
// reads as the first read and rebuilds from zero.
type SQLiteCursorStore struct {
	store      *SQLiteStore
	generation uint64
}

// NewSQLiteCursorStore returns a cursor store for one member window.
func NewSQLiteCursorStore(store *SQLiteStore, generation uint64) *SQLiteCursorStore {
	return &SQLiteCursorStore{store: store, generation: generation}
}

// LoadCursor returns the persisted position. A missing row and a cursor
// from an older generation both read as zero — the first read.
func (s *SQLiteCursorStore) LoadCursor(boardID, consumerID string) (BoardCursor, error) {
	st, err := s.store.GetCursor(context.Background(), boardID, consumerID)
	if errors.Is(err, ErrCursorNotFound) {
		return BoardCursor{}, nil
	}
	if err != nil {
		return BoardCursor{}, err
	}
	if st.Generation != s.generation {
		return BoardCursor{}, nil
	}
	return BoardCursor{
		BoardID: st.BoardID, ConsumerID: st.ConsumerID,
		LastSeq: st.LastSeq, Epoch: st.Generation,
	}, nil
}

// SaveCursor advances the persisted position. A stale-generation or
// backwards write is tolerated — the read already succeeded and the
// cursor is a recovery aid, not the read source (mirror semantics, §2.3).
func (s *SQLiteCursorStore) SaveCursor(cursor BoardCursor) error {
	err := s.store.AdvanceCursor(context.Background(), CursorUpdate{
		BoardID: cursor.BoardID, ConsumerID: cursor.ConsumerID,
		Generation: s.generation, LastSeq: cursor.LastSeq,
	})
	if errors.Is(err, ErrStaleGeneration) || errors.Is(err, ErrCursorBackwards) {
		return nil
	}
	return err
}
