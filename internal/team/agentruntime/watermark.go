package agentruntime

import (
	"context"
	"errors"

	"reasonix/internal/team"
)

// CursorWatermark is the blackboard consumer watermark over the store's
// board_cursors row (route §2.2): load from the persisted position, commit
// by advancing it. A missing row reads as zero (first read). The store's
// generation gate rejects a commit from a stale window (ErrStaleGeneration),
// so the watermark and the member window advance together.
type CursorWatermark struct {
	store      team.BoardStore
	boardID    string
	consumerID string
	generation uint64
}

// NewCursorWatermark returns the watermark for one consumer window.
func NewCursorWatermark(store team.BoardStore, boardID, consumerID string, generation uint64) *CursorWatermark {
	return &CursorWatermark{store: store, boardID: boardID, consumerID: consumerID, generation: generation}
}

// Load returns the persisted position; zero when the consumer never read.
func (w *CursorWatermark) Load(ctx context.Context) (int64, error) {
	c, err := w.store.GetCursor(ctx, w.boardID, w.consumerID)
	if errors.Is(err, team.ErrCursorNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return c.LastSeq, nil
}

// Commit advances the persisted position to seq.
func (w *CursorWatermark) Commit(ctx context.Context, seq int64) error {
	return w.store.AdvanceCursor(ctx, team.CursorUpdate{
		BoardID: w.boardID, ConsumerID: w.consumerID,
		Generation: w.generation, LastSeq: seq,
	})
}
