package control

import "reasonix/internal/event"

// WriteAccessEscalation is one out-of-scope write prompt offered to a non-human
// decider — the team leader agent — instead of the frontend. ApprovalID is the
// id this controller's ResolveApproval accepts, so a decider answers through the
// same one-winner path a human does.
type WriteAccessEscalation struct {
	ApprovalID, Tool, Subject       string
	Directories, DisplayDirectories []string
	Justification                   string
	BroadHomeAccess                 bool
	OrdinaryPermissionNeeded        bool
	PersistAllowed                  bool
}

// WriteAccessEscalator hands one prompt to the decider. BeginWriteAccessEscalation
// runs on the blocked turn's goroutine while the prompt lock is held: it must not
// block and must not call back into this controller. The returned release runs
// exactly once, when the prompt settles — answered, timed out, or the session
// closed.
type WriteAccessEscalator interface {
	BeginWriteAccessEscalation(WriteAccessEscalation) (release func())
}

// SetWriteAccessEscalator installs the decider for this controller. Set it before
// the first turn, the same contract EnableInteractiveApproval carries: it writes
// state the turn reads without a lock. A nil escalator keeps the frontend path
// byte-identical.
func (c *Controller) SetWriteAccessEscalator(e WriteAccessEscalator) {
	if c == nil {
		return
	}
	c.writeAccess.escalator = e
}

// writeAccessEscalation projects the emitted card into the decider's view.
func (c *Controller) writeAccessEscalation(id, tool, subject string, payload *event.WriteAccessApproval) WriteAccessEscalation {
	esc := WriteAccessEscalation{ApprovalID: id, Tool: tool, Subject: subject}
	if payload == nil {
		return esc
	}
	esc.Directories = append([]string(nil), payload.Directories...)
	esc.DisplayDirectories = append([]string(nil), payload.DisplayDirectories...)
	esc.Justification = payload.Justification
	esc.BroadHomeAccess = payload.BroadHomeAccess
	esc.OrdinaryPermissionNeeded = payload.OrdinaryPermissionNeeded
	esc.PersistAllowed = payload.PersistAllowed
	return esc
}
