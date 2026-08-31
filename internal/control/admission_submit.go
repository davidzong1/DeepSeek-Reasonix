package control

import (
	"context"
	"fmt"
)

// SubmitUserTurnOrError is SubmitUserTurn with an explicit admission result: it
// errors when the session is closed, rotating, draining, missing write authority,
// or already running. A parked turn is deliberately NOT an error — the
// finishing-window park is an accepted turn that runs the moment the current one
// settles, and calling it a refusal made the team runtime roll back a task whose
// turn had in fact been delivered, then reject the member's own report for it.
// The reason is named, not numbered: the leader reads this text.
func (c *Controller) SubmitUserTurnOrError(input, display string) error {
	switch res := c.runRefTurnResult(input, display); res {
	case turnStarted, turnParked:
		return nil
	default:
		return fmt.Errorf("member backend did not accept the turn: %s (admission %d)", res, int(res))
	}
}

// runRefTurnResult is runRefTurn with the admission outcome surfaced.
func (c *Controller) runRefTurnResult(input, display string) admissionResult {
	return c.runGuardedResult(func(ctx context.Context) error {
		return c.runRefTurnWithResolverSync(ctx, input, input, display, "", c.ResolveRefs)
	})
}

// runGuardedResult reports what runGuarded did. The silent-dropping wrappers
// hide it; task-driving hosts need the distinction.
func (c *Controller) runGuardedResult(body func(ctx context.Context) error) admissionResult {
	return c.admitGuardedTurn(body, false, true, nil)
}
