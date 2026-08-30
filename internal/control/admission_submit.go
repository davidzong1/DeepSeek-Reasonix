package control

import (
	"context"
	"fmt"
)

// SubmitUserTurnOrError is SubmitUserTurn with an explicit admission result:
// it errors when the session is closed, rotating, draining, missing write
// authority, or already running. The team runtime's task-driving host uses it
// so a refused turn never looks like an accepted one.
func (c *Controller) SubmitUserTurnOrError(input, display string) error {
	if res := c.runRefTurnResult(input, display); res != turnStarted {
		return fmt.Errorf("member backend did not accept the turn (admission %d)", res)
	}
	return nil
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
