package agentruntime

import (
	"context"

	"reasonix/internal/team"
)

// InboxItem is one durable command for a member: the board event's summary
// plus the identity the runtime stamps into the injected context. The board
// log is the durable store; this struct is only the read shape.
type InboxItem struct {
	EventID    string
	Seq        int64
	TaskID     team.TaskID
	MemberID   string
	Generation uint64
	Summary    string
}

// Watermark is one consumer's persisted read position (route §2.2). Load
// returns where the consumer last committed; Commit advances it after the
// batch was processed, so a crash re-reads from the last commit.
type Watermark interface {
	Load(ctx context.Context) (int64, error)
	Commit(ctx context.Context, seq int64) error
}

// BoardInbox is the durable command inbox over the blackboard event log
// (route §5.1): leader commands for one member are board events read from
// the consumer's watermark and acknowledged by advancing it. Dispatch
// assignments do not pass through the inbox — the scheduler pushes those
// straight to the runtime. Events stamped with another generation are
// skipped, not consumed — a stale window must not drain commands that
// belong to the current window.
type BoardInbox struct {
	store      team.BoardStore
	boardID    string
	consumerID string
	generation uint64
	watermark  Watermark
}

// NewBoardInbox returns an inbox reading one member's commands from the
// board. The watermark shares the store's board_cursors row, so a restart
// continues where the member left off.
func NewBoardInbox(store team.BoardStore, boardID, consumerID string, generation uint64) *BoardInbox {
	return &BoardInbox{
		store:      store,
		boardID:    boardID,
		consumerID: consumerID,
		generation: generation,
		watermark:  NewCursorWatermark(store, boardID, consumerID, generation),
	}
}

// Fetch returns the unread commands for the member, oldest first, and the
// seq to commit once they were processed. A negative afterSeq starts from
// the persisted watermark. Kind and member are filtered server-side;
// generation is filtered here, because the log carries both windows'
// history.
func (b *BoardInbox) Fetch(ctx context.Context, afterSeq int64, limit int) ([]InboxItem, int64, error) {
	if afterSeq < 0 {
		pos, err := b.watermark.Load(ctx)
		if err != nil {
			return nil, 0, err
		}
		afterSeq = pos
	}
	page, err := b.store.ReadAfter(ctx, b.boardID, afterSeq, team.Filter{
		Kind:     team.EventCommand,
		MemberID: b.consumerID,
		Limit:    limit,
		Stamped:  team.Identity{MemberID: b.consumerID, Generation: b.generation},
	})
	if err != nil {
		return nil, 0, err
	}
	items := make([]InboxItem, 0, len(page.Events))
	for _, ev := range page.Events {
		if ev.Generation != b.generation {
			continue
		}
		items = append(items, InboxItem{
			EventID: ev.EventID, Seq: ev.Seq, TaskID: ev.TaskID,
			MemberID: ev.MemberID, Generation: ev.Generation, Summary: ev.Summary,
		})
	}
	return items, page.NextSeq, nil
}

// Ack commits the processed position. Advancing from a stale generation is
// rejected by the store (ErrStaleGeneration): the window changed, and the
// new window builds its own inbox.
func (b *BoardInbox) Ack(ctx context.Context, seq int64) error {
	return b.watermark.Commit(ctx, seq)
}
