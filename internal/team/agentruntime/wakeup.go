package agentruntime

import (
	"context"
	"strconv"
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

// boardWake makes a wakeup durable on the blackboard.
type boardWake struct {
	store    team.BoardStore
	boardID  string
	identity team.Identity
}

func (w *boardWake) wake(reason string) error {
	if w.store == nil {
		return nil
	}
	_, err := w.store.Append(context.Background(), team.AppendInput{
		BoardID: w.boardID,
		EventID: "wakeup-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		Kind:    team.EventWakeup,
		Summary: reason,
		Stamped: w.identity,
	})
	return err
}
