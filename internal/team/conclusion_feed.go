package team

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Conclusion feed constants (route §3.1). The caps are per rune and an over-cap
// post is refused, never clipped: a truncated conclusion that looks stored is
// worse than a refusal the member can act on, and the long text belongs in a
// deliverable the summary can quote by id.
const (
	conclusionTopicMaxRunes   = 48
	conclusionSummaryMaxRunes = 160
	conclusionDeltaMaxItems   = 8
	conclusionListLimit       = 16
	conclusionDeltaHeader     = "[board delta]"
)

// conclusionConsumer names one member's conclusion cursor. The prefix keeps it
// off the instruction inbox (consumer = member id) and the leader wake cursor
// (consumer = leader id): all three read the same board with different
// semantics, and sharing one cursor would make each reader consume the other's
// rows.
func conclusionConsumer(memberID string) string { return "conclusions:" + memberID }

// PostConclusion records one member's conclusion on the shared board and
// revises its topic in the same transaction (route §3.2). A topic already on
// the board is revised with one CAS retry, so a concurrent post to the same
// topic loses cleanly instead of forking a second row.
//
// It appends no wakeup and touches no task state: a conclusion is a finding for
// other members' next thinking step, not a report and not a leader signal.
func PostConclusion(ctx context.Context, store BoardStore, id Identity, taskID TaskID, topic, summary string) (Conclusion, error) {
	if store == nil {
		return Conclusion{}, fmt.Errorf("team: post conclusion: no board store")
	}
	topic, summary = strings.TrimSpace(topic), strings.TrimSpace(summary)
	if err := validateConclusion(id, topic, summary); err != nil {
		return Conclusion{}, err
	}
	in := AppendInput{
		BoardID:     BoardShared,
		ClientMsgID: conclusionMsgID(id.MemberID, topic, summary),
		Kind:        EventConclusion,
		TaskID:      taskID,
		CreatedAt:   time.Now().UTC(),
		Summary:     topic + "\n" + summary,
		Stamped:     id,
		Conclusion:  &ConclusionUpdate{Topic: topic, BaseEpoch: 0, Summary: summary},
	}
	ev, err := store.Append(ctx, in)
	if err != nil {
		// A topic already on the board answers ErrConflict with its current
		// epoch — the store's own race-free read of it. The retry therefore
		// needs no separate read that a concurrent revision could invalidate
		// between the two calls.
		var conflict *ErrConflict
		if !errors.As(err, &conflict) {
			return Conclusion{}, err
		}
		in.Conclusion.BaseEpoch = conflict.CurrentEpoch
		if ev, err = store.Append(ctx, in); err != nil {
			return Conclusion{}, err
		}
	}
	return currentConclusion(ctx, store, ev, topic, summary), nil
}

// currentConclusion returns the topic's revision as the board holds it. Reading
// back rather than deriving it from this call's CAS keeps the answer true on the
// replay path: an identical post returns the original event, so the epoch this
// call started from says nothing about the row's current one. A board that
// cannot answer falls back to the event's own revision — the write already
// landed, and failing here would report a stored conclusion as an error.
func currentConclusion(ctx context.Context, store BoardStore, ev BoardEvent, topic, summary string) Conclusion {
	view, err := store.ReadView(ctx, BoardShared, ViewSpec{TaskID: ev.TaskID, Limit: conclusionListLimit})
	if err == nil {
		for _, c := range view.Conclusions {
			if c.Topic == topic {
				return c
			}
		}
	}
	return Conclusion{
		BoardID: ev.BoardID, TaskID: ev.TaskID, Topic: topic, Epoch: 1,
		EventSeq: ev.Seq, Digest: ev.Digest, Summary: summary, MemberID: ev.MemberID,
	}
}

// validateConclusion refuses an unusable post before any write: a blank field
// has nothing to record, and an over-cap one is a document, not a conclusion.
func validateConclusion(id Identity, topic, summary string) error {
	if strings.TrimSpace(id.MemberID) == "" {
		return fmt.Errorf("team: post conclusion: member id is required")
	}
	if topic == "" {
		return fmt.Errorf("team: post conclusion: topic is required")
	}
	if summary == "" {
		return fmt.Errorf("team: post conclusion: summary is required")
	}
	if n := utf8.RuneCountInString(topic); n > conclusionTopicMaxRunes {
		return fmt.Errorf("team: post conclusion: topic is %d characters, over the %d limit", n, conclusionTopicMaxRunes)
	}
	if n := utf8.RuneCountInString(summary); n > conclusionSummaryMaxRunes {
		return fmt.Errorf("team: post conclusion: summary is %d characters, over the %d limit", n, conclusionSummaryMaxRunes)
	}
	return nil
}

// conclusionMsgID is the idempotency key of one post: the member, the topic and
// the summary's digest. An identical replay therefore returns the original
// event instead of allocating a second seq for the same finding.
func conclusionMsgID(memberID, topic, summary string) string {
	sum := sha256.Sum256([]byte(summary))
	return memberID + "\x00" + topic + "\x00" + hex.EncodeToString(sum[:16])
}

// ReadConclusionDelta returns the conclusions another member posted since this
// member's cursor, rendered for the next thinking step, plus the seq the caller
// acknowledges once that text reached the session (route §3.2).
//
// A member with no cursor yet gets one at the board's tail and no text: a member
// joining an established board must not be handed every conclusion ever posted.
// That first write is this function's own, so the caller must not acknowledge
// it. An empty text with a positive advanceTo means the page held only this
// member's own posts — the cursor still has to move, or every later step
// re-reads the same page forever.
func ReadConclusionDelta(ctx context.Context, store *SQLiteStore, memberID string) (string, int64, error) {
	if store == nil || strings.TrimSpace(memberID) == "" {
		return "", 0, fmt.Errorf("team: read conclusion delta: member id is required")
	}
	consumer := conclusionConsumer(memberID)
	pos, err := store.GetCursor(ctx, BoardShared, consumer)
	if errors.Is(err, ErrCursorNotFound) {
		tail, tailErr := boardTailSeq(ctx, store)
		if tailErr != nil {
			return "", 0, tailErr
		}
		if err := store.AdvanceCursor(ctx, CursorUpdate{
			BoardID: BoardShared, ConsumerID: consumer, LastSeq: tail,
		}); err != nil {
			return "", 0, err
		}
		return "", 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	// The row's existence, not its value, is what separates "first read" from
	// "read since zero": a tail of 0 is a legitimate position that must keep
	// serving later conclusions rather than being re-initialized.
	page, err := store.ReadAfter(ctx, BoardShared, pos.LastSeq, Filter{
		Kind: EventConclusion, Limit: conclusionDeltaMaxItems,
		Stamped: Identity{MemberID: memberID},
	})
	if err != nil {
		return "", 0, err
	}
	lines := make([]string, 0, len(page.Events))
	for _, ev := range page.Events {
		if ev.MemberID == memberID {
			continue // the member's own post: its tool receipt already carried it
		}
		topic, body := conclusionTopicBody(ev.Summary)
		lines = append(lines, ConclusionLine(ev.MemberID, topic, body))
	}
	if len(lines) == 0 {
		return "", page.NextSeq, nil
	}
	return conclusionDeltaHeader + "\n" + strings.Join(lines, "\n"), page.NextSeq, nil
}

// AckConclusionDelta moves one member's conclusion cursor to advanceTo. A
// non-positive target is a no-op, and a backwards move is refused by the store
// (ErrCursorBackwards), so a caller can never un-read a conclusion.
func AckConclusionDelta(ctx context.Context, store *SQLiteStore, memberID string, advanceTo int64) error {
	if store == nil || strings.TrimSpace(memberID) == "" {
		return fmt.Errorf("team: ack conclusion delta: member id is required")
	}
	if advanceTo <= 0 {
		return nil
	}
	consumer := conclusionConsumer(memberID)
	pos, err := store.GetCursor(ctx, BoardShared, consumer)
	if err != nil && !errors.Is(err, ErrCursorNotFound) {
		return err
	}
	// The stored generation is carried forward so a re-read after a window
	// change is not refused as stale; a missing row inserts at generation 0.
	return store.AdvanceCursor(ctx, CursorUpdate{
		BoardID: BoardShared, ConsumerID: consumer,
		Generation: pos.Generation, LastSeq: advanceTo,
	})
}

// ListConclusions returns the board's current conclusions, one per topic, for a
// reader that asked for them — the leader's read tool. It advances no cursor:
// the member deltas own their own positions, and a leader read must not consume
// a member's unread page.
func ListConclusions(ctx context.Context, store BoardStore, id Identity, limit int) ([]Conclusion, error) {
	if store == nil {
		return nil, fmt.Errorf("team: list conclusions: no board store")
	}
	if strings.TrimSpace(id.MemberID) == "" {
		return nil, fmt.Errorf("team: list conclusions: member id is required")
	}
	if limit <= 0 || limit > conclusionListLimit {
		limit = conclusionListLimit
	}
	view, err := store.ReadView(ctx, BoardShared, ViewSpec{Limit: limit})
	if err != nil {
		return nil, err
	}
	return view.Conclusions, nil
}

// boardTailSeq is the board's current last seq, or 0 when it holds no events.
// It is the one query ReadAfter cannot answer: an empty page reports NextSeq 0,
// which is indistinguishable from "the board is empty".
func boardTailSeq(ctx context.Context, store *SQLiteStore) (int64, error) {
	var seq int64
	if err := store.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq),0) FROM board_events WHERE board_id = ?`,
		BoardShared).Scan(&seq); err != nil {
		return 0, err
	}
	return seq, nil
}

// conclusionTopicBody splits a conclusion event's summary back into its topic
// and body: the board event has no topic column, so PostConclusion stores them
// newline-joined. An event written without one — the blackboard CLI, or any
// writer that only had a summary — has no recoverable topic, and its whole
// summary is the body.
func conclusionTopicBody(summary string) (topic, body string) {
	topic, body, ok := strings.Cut(summary, "\n")
	if !ok {
		return "", summary
	}
	return topic, body
}

// ConclusionLine renders one conclusion the way every reader shows it — the
// member delta and the leader's read tool share this, so the two can never drift
// into different shapes. No seq, epoch or digest appears: the model cannot act
// on them and they change on every revision.
func ConclusionLine(memberID, topic, summary string) string {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return "- " + memberID + ": " + summary
	}
	return "- " + memberID + " " + topic + ": " + summary
}
