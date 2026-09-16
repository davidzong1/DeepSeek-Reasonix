package control

import (
	"context"

	"reasonix/internal/event"
	"reasonix/internal/session"
)

// admissionResult classifies what runGuarded did with a turn body.
type admissionResult int

const (
	turnStarted admissionResult = iota
	turnParked
	turnDroppedRunning
	turnDroppedRotating
	turnDroppedClosed
	turnDroppedDraining // generation no longer published after rebuild
	turnDroppedWriteAuthority
)

// String names the outcome for an error a human reads. A bare enum index told a
// team leader nothing about why its member refused the work.
func (r admissionResult) String() string {
	switch r {
	case turnStarted:
		return "started"
	case turnParked:
		return "parked until the current turn settles"
	case turnDroppedRunning:
		return "already running a turn"
	case turnDroppedRotating:
		return "session is being switched"
	case turnDroppedClosed:
		return "session is closed"
	case turnDroppedDraining:
		return "session generation is draining"
	case turnDroppedWriteAuthority:
		return "session is no longer writable"
	}
	return "unknown admission outcome"
}

// runGuarded runs body under a fresh context, guarding concurrent turns.
// Finishing-window arrivals park instead of dropping (see admissionResult).
func (c *Controller) runGuarded(body func(ctx context.Context) error) admissionResult {
	return c.admitGuardedTurn(body, false, true, nil, nil)
}

// runGuardedOrPark admits like runGuarded but parks the body while another
// turn is running instead of using the deliberately-silent running drop.
// Reserved for inputs that are the user's own words (the steer fallback):
// the FIFO drain in finishGuardedTurn delivers them the moment the current
// turn finishes.
func (c *Controller) runGuardedOrPark(body func(ctx context.Context) error) admissionResult {
	return c.admitGuardedTurn(body, true, true, nil, nil)
}

// runGuardedInbox admits a durable item without parking it in volatile memory.
// onStart runs after admission is reserved and before its goroutine can finish.
func (c *Controller) runGuardedInbox(body func(ctx context.Context) error, onStart func()) admissionResult {
	if !c.submissions.mu.TryLock() {
		return turnDroppedRunning
	}
	defer c.submissions.mu.Unlock()
	return c.admitGuardedTurn(body, false, false, onStart, nil)
}

func (c *Controller) runGuardedGoalRound(reservation *goalRoundReservation, body func(ctx context.Context) error) admissionResult {
	c.submissions.mu.Lock()
	defer c.submissions.mu.Unlock()
	return c.admitGuardedTurn(body, false, false, nil, reservation)
}

func (c *Controller) admitGuardedTurn(body func(ctx context.Context) error, parkWhileRunning, parkWhileFinishing bool, onStart func(), goalRound *goalRoundReservation) admissionResult {
	if err := c.ensureWriteAuthorityReady(); err != nil {
		c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "input was not accepted: this session is no longer writable — reopen it and try again"})
		return turnDroppedWriteAuthority
	}
	if ledger := c.turnEventLedger(); ledger != nil && ledger.CurrentStatus() == event.TurnRecoveryRequired {
		c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: ErrRecoveryRequired.Error()})
		return turnDroppedWriteAuthority
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return turnDroppedClosed
	}
	if c.rejectDrainingGenerationLocked() {
		c.mu.Unlock()
		c.emitDrainingNotice()
		return turnDroppedDraining
	}
	if c.rotating {
		c.mu.Unlock()
		c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "input was not accepted: the session is being switched — please resend"})
		return turnDroppedRotating
	}
	if c.turns.phase == session.RuntimeRecoveryRequired {
		c.mu.Unlock()
		c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: ErrRecoveryRequired.Error()})
		return turnDroppedWriteAuthority
	}
	kind := queuedUser
	if goalRound != nil {
		kind = queuedGoal
	}
	item := queuedTurn{kind: kind, body: body, onStart: onStart, goalRound: goalRound}
	switch c.turns.phase {
	case session.RuntimeRunning:
		if parkWhileRunning || c.turns.cancelRequested {
			c.queueTurnLocked(item)
			c.mu.Unlock()
			return turnParked
		}
		c.mu.Unlock()
		return turnDroppedRunning
	case session.RuntimeCancelling:
		c.queueTurnLocked(item)
		c.mu.Unlock()
		return turnParked
	case session.RuntimeFinalizing:
		if !parkWhileFinishing {
			c.mu.Unlock()
			return turnDroppedRunning
		}
		c.queueTurnLocked(item)
		c.mu.Unlock()
		return turnParked
	}
	ctx, cancel, admitted := c.startTurnLocked(context.Background(), item)
	if !admitted {
		c.mu.Unlock()
		c.emitDrainingNotice()
		return turnDroppedDraining
	}
	c.mu.Unlock()
	if onStart != nil {
		onStart()
	}
	c.refreshRuntimeState(event.Event{})
	c.spawnGuardedTurn(ctx, cancel, body, goalRound)
	return turnStarted
}
