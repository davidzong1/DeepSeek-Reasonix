package agentruntime

import (
	"context"
	"strconv"
	"sync/atomic"
	"time"

	"reasonix/internal/team"
)

// WakeFunc delivers one leader-wakeup signal: a task finished, failed, or
// was canceled. The host (MCP server, TUI) injects real delivery — terminal
// injection or a bus event; nil disables wakeups. The runtime treats a
// failing wakeup as non-fatal and records it in the blackboard instead, so
// no wake path can wedge task completion.
type WakeFunc func(reason string) error

// NewBoardWake returns a WakeFunc that appends a wakeup event to the
// board — durable and observable even when no live leader window exists at
// wake time, stamped with the identity of the waker. Hosts register it with
// Runtime.AddWakeup.
func NewBoardWake(store team.BoardStore, boardID string, identity team.Identity) WakeFunc {
	w := &boardWake{store: store, boardID: boardID, identity: identity}
	return w.wake
}

// NewBoardWakeFor returns a WakeFunc whose stamp is resolved per wake rather
// than frozen at construction — the team's leader slot can be reassigned, and
// the leader-wake cursor selects by the leader member id, never the team name.
// A resolution yielding no member (leaderless team) skips the wake instead of
// writing a forbidden append.
func NewBoardWakeFor(store team.BoardStore, boardID string, identity func() team.Identity) WakeFunc {
	w := &boardWake{store: store, boardID: boardID, identityOf: identity}
	return w.wake
}

// boardWake makes a wakeup durable on the blackboard. The event id must be
// unique per wake: two reports sharing one time.Now().UnixNano() read would
// otherwise collide on the board's client-msg-id dedup and silently lose one
// leader wakeup — the sequence counter disambiguates any same-tick pair.
type boardWake struct {
	store      team.BoardStore
	boardID    string
	identity   team.Identity
	identityOf func() team.Identity // per-wake stamp; identityOf wins when set
	calls      atomic.Uint64
}

func (w *boardWake) wake(reason string) error {
	if w.store == nil {
		return nil
	}
	identity := w.identity
	if w.identityOf != nil {
		identity = w.identityOf()
	}
	if identity.MemberID == "" {
		return nil // no target (leaderless team): nothing to wake
	}
	id := "wakeup-" + strconv.FormatInt(time.Now().UnixNano(), 10) +
		"-" + strconv.FormatUint(w.calls.Add(1), 10)
	_, err := w.store.Append(context.Background(), team.AppendInput{
		BoardID:     w.boardID,
		EventID:     id,
		ClientMsgID: id, // the event id doubles as the idempotency key
		Kind:        team.EventWakeup,
		Summary:     reason,
		Stamped:     identity,
	})
	return err
}
