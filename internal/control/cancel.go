package control

import (
	"reasonix/internal/agent"
	"reasonix/internal/event"
)

// CancelReceipt acknowledges a session-scoped Stop request. Accepted means the
// cancellation signal was processed; it does not claim that every owned
// operation has already exited.
type CancelReceipt struct {
	SessionRef       string `json:"sessionRef"`
	HeadID           string `json:"headId"`
	RuntimeEpoch     string `json:"runtimeEpoch"`
	Accepted         bool   `json:"accepted"`
	AlreadyIdle      bool   `json:"alreadyIdle"`
	RecoveryRequired bool   `json:"recoveryRequired"`
}

// CancelSession stops the activity owned by this captured controller. Callers
// do not need a turn id, and an idle cancellation is idempotently successful.
func (c *Controller) CancelSession() CancelReceipt {
	if c == nil {
		return CancelReceipt{Accepted: true, AlreadyIdle: true}
	}
	c.mu.Lock()
	alreadyIdle := c.turns.cancel == nil && !c.bodyActiveLocked() && !c.finalizingLocked()
	sessionRef := c.sessionPath
	c.mu.Unlock()
	headID := agent.BranchID(sessionRef)
	c.runtimeState.mu.Lock()
	epoch := c.runtimeState.snapshot.RuntimeEpoch
	recoveryRequired := c.runtimeState.snapshot.Phase == "recovery_required"
	c.runtimeState.mu.Unlock()
	_, runtime, exclusive := c.v3Binding()
	if exclusive && runtime != nil {
		sessionRef = runtime.Ref().SessionID
		headID = ""
	}
	token, turnID, cancelled := c.signalTurnCancelIdentity()
	if cancelled {
		alreadyIdle = false
	}
	go c.finishCancellation(token, turnID, cancelled)
	receipt := CancelReceipt{
		SessionRef: sessionRef, HeadID: headID, RuntimeEpoch: epoch,
		Accepted: true, AlreadyIdle: alreadyIdle, RecoveryRequired: recoveryRequired,
	}
	return receipt
}

// Cancel aborts the in-flight turn. A goroutine blocked awaiting approval
// unblocks via the cancelled context.
func (c *Controller) Cancel() {
	turnID, cancelled := c.cancelTurnLocked()
	c.finishCancel(turnID, cancelled)
}

// cancelLocked is retained for call sites already inside a typed prompt
// transition. Cancellation itself is independent of answer serialization.
func (c *Controller) cancelLocked() {
	turnID, cancelled := c.cancelTurnLocked()
	c.finishCancel(turnID, cancelled)
}

// cancelTurnLocked signals the turn before any observable work: the status
// emit that follows is a synchronous event barrier, and a stalled event lane
// must never keep the provider stream or a tool process alive after Stop.
func (c *Controller) cancelTurnLocked() (string, bool) {
	_, turnID, cancelled := c.signalTurnCancelIdentity()
	if !cancelled {
		return "", false
	}
	c.promptOwner.CancelTurn(turnID)
	return turnID, true
}

func (c *Controller) finishCancellation(token uint64, turnID string, cancelled bool) {
	c.mu.Lock()
	current := c.turns.token == token
	c.mu.Unlock()
	if !current {
		return
	}
	c.promptOwner.CancelTurn(turnID)
	c.finishCancel(turnID, cancelled)
}

func (c *Controller) finishCancel(turnID string, cancelled bool) {
	defer c.refreshRuntimeState(event.Event{})
	if cancelled {
		c.emitTurnStatus(event.TurnCancelling, turnID)
	}
	c.mu.Lock()
	stale := turnID != "" && c.turns.turnID != "" && c.turns.turnID != turnID
	c.mu.Unlock()
	if stale {
		return
	}
	if c.goals.active() {
		c.stopGoal(GoalStatusStopped)
	}
	if c.sessionEngineEnabled() {
		c.disarmGoalLifecycle("cancelled")
	}
}
