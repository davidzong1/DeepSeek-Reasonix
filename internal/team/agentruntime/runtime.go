package agentruntime

import (
	"context"

	"reasonix/internal/team/events"
)

// RuntimeState is the runtime lifecycle state.
type RuntimeState string

const (
	RuntimeStateStopped RuntimeState = "stopped"
	RuntimeStateRunning RuntimeState = "running"
)

// Runtime is the single-process multi-agent runtime boundary (§3.7):
// Start launches the team's agent loop, Stop tears it down, Status reports
// lifecycle state, and Events streams member-state/wakeup events. No
// implementation body this round.
type Runtime interface {
	Start(ctx context.Context) error
	Stop() error
	Status() (RuntimeState, error)
	Events() (events.Subscription, error)
}
