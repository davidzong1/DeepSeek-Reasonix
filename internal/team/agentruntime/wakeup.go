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

// StampedWakeFunc delivers one leader-wakeup signal whose stamp the caller
// already resolved. It is WakeFunc with the identity hoisted out of delivery:
// resolving the team's current leader itself means reading the durable team
// document, and a host that delivers under its own lock would then hold that
// lock across a JSON read — serializing every member that completes at the
// same moment, which is the burst the lock exists to let through. Resolving
// the stamp per wake, before the lock, keeps delivery re-targeted at a
// reassigned leader without moving the read into the critical section.
type StampedWakeFunc func(reason string, stamp team.Identity) error

// NewBoardWake returns a WakeFunc that appends a wakeup event to the
// board — durable and observable even when no live leader window exists at
// wake time, stamped with the identity of the waker. Hosts register it with
// Runtime.AddWakeup.
func NewBoardWake(store team.BoardStore, boardID string, identity team.Identity) WakeFunc {
	w := &boardWake{store: store, boardID: boardID, identity: identity}
	return w.wake
}

// NewBoardWakeStamped returns the board-backed delivery whose stamp is the
// caller's: the host resolves the team's current leader slot itself — per wake,
// so a leader change still re-targets delivery — and passes it in. A zero stamp
// means no leader, and the wake is skipped rather than appended anonymously,
// which the board forbids.
func NewBoardWakeStamped(store team.BoardStore, boardID string) StampedWakeFunc {
	w := &boardWake{store: store, boardID: boardID}
	return w.wakeStamped
}

// boardWake makes a wakeup durable on the blackboard. The event id must be
// unique per wake: two reports sharing one time.Now().UnixNano() read would
// otherwise collide on the board's client-msg-id dedup and silently lose one
// leader wakeup — the sequence counter disambiguates any same-tick pair.
type boardWake struct {
	store    team.BoardStore
	boardID  string
	identity team.Identity
	calls    atomic.Uint64
}

func (w *boardWake) wake(reason string) error {
	return w.wakeStamped(reason, w.identity)
}

// wakeStamped appends one wakeup event stamped with identity. A nil store or a
// stamp naming no member (leaderless team) makes the wake a no-op: there is no
// one to wake, and an anonymous append would be forbidden by the board.
func (w *boardWake) wakeStamped(reason string, identity team.Identity) error {
	if w.store == nil || identity.MemberID == "" {
		return nil
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
