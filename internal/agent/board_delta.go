package agent

import "context"

// BoardDeltaFunc returns the text to append to the session before the next
// sampling step, as one host-generated user-role message — host origin, never
// user (applyBoardDelta carries the reason). An empty string appends nothing,
// so the request keeps the byte-identical shape it would have had without a
// hook.
//
// It is the member-side half of the shared-conclusion blackboard: another
// member's new conclusion arrives as a short delta, and only when there is one.
// It is never a channel for reports, commands or task flow — those ride the
// durable inbox and the turn itself.
type BoardDeltaFunc func(ctx context.Context) (string, error)

// boardDeltaState pairs the delta reader with the acknowledgement that may run
// only after its text reached the session. They are one contract: an ack that
// outlived its reader would advance a cursor past text nobody ever wrote.
type boardDeltaState struct {
	read BoardDeltaFunc
	ack  func()
}

// SetBoardDelta installs the pre-sampling board delta. ack, when non-nil, runs
// only after a non-empty delta was appended to the session; an empty delta or a
// failed append leaves it uncalled, so the next step reads the same page again.
// Passing a nil fn clears the hook.
//
// This is a construction-time seam, like SetTools: the host installs it while
// assembling the backend, before any turn runs. A leader's agent must never be
// given one — nothing but the leader's own read tool may put a conclusion into
// its context.
func (a *Agent) SetBoardDelta(fn BoardDeltaFunc, ack func()) {
	if a == nil {
		return
	}
	a.boardDelta = boardDeltaState{read: fn, ack: ack}
}

// BoardDeltaInstalled reports whether a pre-sampling board delta is wired. It
// lets a host pin the member/leader split without running a turn.
func (a *Agent) BoardDeltaInstalled() bool {
	return a != nil && a.boardDelta.read != nil
}

// applyBoardDelta appends this step's board delta, if any. Every failure is a
// silent skip: a board that is down must not fail the turn, and its error text
// must never reach the prompt. An empty delta allocates no message id and marks
// no content rewrite, so the prefix stays exactly as it was.
//
// The message is stamped host-generated, not user-authored. It rides the same
// append path a steer does, but it is not a turn boundary: a user origin would
// make it start a turn in the display index, count as user intent in transcript
// classification, and let a cancelled turn's recovery preserve it as the
// user's own message.
func (a *Agent) applyBoardDelta(ctx context.Context) {
	if a == nil || a.boardDelta.read == nil {
		return
	}
	text, err := a.boardDelta.read(ctx)
	if err != nil || text == "" {
		return
	}
	if err := a.appendCommittedMessages(ctx, "board-delta", HostGeneratedUserMessage(text)); err != nil {
		return
	}
	if a.boardDelta.ack != nil {
		a.boardDelta.ack()
	}
}
