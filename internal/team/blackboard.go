package team

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Board identifiers. A private board is per-member; every other board is
// shared. The store checks access against the stamped identity, so a
// client cannot cross into another member's board.
const (
	BoardShared        = "shared"
	BoardPrivatePrefix = "private/"
)

// PrivateBoard reports whether boardID is a per-member board and returns
// the owning member id.
func PrivateBoard(boardID string) (memberID string, ok bool) {
	m, found := strings.CutPrefix(boardID, BoardPrivatePrefix)
	if !found || m == "" {
		return "", false
	}
	return m, true
}

// EventKind classifies board events (route §1.1).
type EventKind string

const (
	EventReport     EventKind = "report"
	EventConclusion EventKind = "conclusion"
	EventCheckpoint EventKind = "checkpoint"
	EventEvidence   EventKind = "evidence"
	EventAssignment EventKind = "assignment"
	EventSupersede  EventKind = "supersede"
)

// ArtifactRef points at a large payload stored outside the board; the
// board carries the pointer, never the payload (route §1.1).
type ArtifactRef struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Size   int64  `json:"size,omitempty"`
	Digest string `json:"digest,omitempty"`
}

// Identity is stamped by the server from the current window binding; a
// client-supplied identity is rejected by the caller before it reaches
// the store (route §1.2).
type Identity struct {
	MemberID   string
	Role       string
	Agent      string
	Generation uint64
}

// AppendInput is what a client may supply. Seq, MemberID, Role, Agent and
// Generation are absent from the input: the store stamps them from
// Stamped (route §1.2). Conclusion revises one topic in the same
// transaction, so a CAS conflict never leaves an orphan event.
type AppendInput struct {
	BoardID      string
	EventID      string
	ClientMsgID  string // idempotency key: replay returns the original event
	Kind         EventKind
	TaskID       TaskID
	CreatedAt    time.Time
	Summary      string
	ArtifactRefs []ArtifactRef
	Supersedes   []int64
	Stamped      Identity
	Conclusion   *ConclusionUpdate
}

// ConclusionUpdate revises one topic with optimistic concurrency: the
// store matches BaseEpoch (0 = insert-if-absent) and returns ErrConflict
// with the current revision when it changed (route §2.2).
type ConclusionUpdate struct {
	Topic     string
	BaseEpoch uint64
	Summary   string
}

// Conclusion is one topic's current revision (route §1.3 board_conclusions).
type Conclusion struct {
	BoardID  string
	TaskID   TaskID
	Topic    string
	Epoch    uint64
	EventSeq int64
	Digest   string
	Summary  string
}

// BoardEvent is one immutable fact on a board (route §1.1). The store
// stamps Seq, the identity fields and Digest; Supersedes chains revisions
// so history is never overwritten.
type BoardEvent struct {
	SchemaVersion uint16
	BoardID       string
	Seq           int64
	EventID       string
	ClientMsgID   string
	Kind          EventKind
	TaskID        TaskID
	MemberID      string
	Role          string
	Agent         string
	Generation    uint64
	CreatedAt     time.Time
	Digest        string
	Summary       string
	ArtifactRefs  []ArtifactRef
	Supersedes    []int64
}

// Filter narrows ReadAfter; zero values select everything. Stamped carries
// the caller identity for private-board access control.
type Filter struct {
	TaskID   TaskID
	Kind     EventKind
	MemberID string
	Limit    int
	Stamped  Identity
}

// Page is one ReadAfter slice with the cursor continuation (route §2.3).
type Page struct {
	Events     []BoardEvent
	NextSeq    int64 // last seq in Events; 0 when empty
	HasMore    bool
	NeedResync bool // cursor fell into an archived hole: reload the snapshot
}

// CursorUpdate advances one consumer's read cursor; advance must be
// monotonic and from the current generation (route §2.2).
type CursorUpdate struct {
	BoardID    string
	ConsumerID string
	Generation uint64
	LastSeq    int64
}

// ViewSpec selects the materialized conclusion view (route §1.2; L0-L3
// budgeting and token levels are P3).
type ViewSpec struct {
	TaskID TaskID // empty = team-wide
	Limit  int    // 0 = default 32
}

// MaterializedView is a rebuildable projection of board_conclusions, not
// a second source of truth (route §3.1).
type MaterializedView struct {
	SourceSeq   int64
	Epoch       uint64
	Digest      string
	Conclusions []Conclusion
}

// ErrConflict reports a CAS mismatch on a conclusion revision: the caller
// re-reads the current revision and merges before retrying (route §2.2).
type ErrConflict struct {
	BoardID      string
	TaskID       TaskID
	Topic        string
	CurrentEpoch uint64
	CurrentSeq   int64
}

func (e *ErrConflict) Error() string {
	return fmt.Sprintf("team: board conclusion %s/%s/%s conflict: current epoch %d (seq %d)",
		e.BoardID, e.TaskID, e.Topic, e.CurrentEpoch, e.CurrentSeq)
}

// ErrStaleGeneration rejects a write or cursor advance from an older
// window (route §2.2).
var ErrStaleGeneration = errors.New("team: board operation rejected: stale generation")

// ErrCursorBackwards rejects a cursor advance that moves backwards.
var ErrCursorBackwards = errors.New("team: board cursor rejected: non-monotonic advance")

// ErrForbidden rejects cross-boundary access: a non-owner touching a
// private board. The target's existence is never revealed (route §6.2).
var ErrForbidden = errors.New("team: board access forbidden")

// BoardStore is the durable blackboard: an append-only event log plus
// rebuildable conclusions, per-consumer cursors and archival (route §1.2).
// Events, conclusion updates and cursor advances are atomic per call.
type BoardStore interface {
	Append(ctx context.Context, in AppendInput) (BoardEvent, error)
	AppendBatch(ctx context.Context, in []AppendInput) ([]BoardEvent, error)
	ReadAfter(ctx context.Context, boardID string, afterSeq int64, f Filter) (Page, error)
	ReadView(ctx context.Context, boardID string, view ViewSpec) (MaterializedView, error)
	AdvanceCursor(ctx context.Context, c CursorUpdate) error
	GetCursor(ctx context.Context, boardID, consumerID string) (CursorState, error)
	Supersede(ctx context.Context, boardID string, ids []int64, replacement AppendInput) (BoardEvent, error)
	ArchiveBefore(ctx context.Context, boardID string, seq int64, id Identity) error
}
