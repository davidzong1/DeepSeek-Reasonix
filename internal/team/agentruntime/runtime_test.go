package agentruntime

import (
	"context"
	"testing"

	"reasonix/internal/team/events"
)

// fakeRuntime pins the Runtime interface shape: no implementation body
// ships this round (§3.7), so the contract is compile-time only.
type fakeRuntime struct{}

func (fakeRuntime) Start(context.Context) error   { return nil }
func (fakeRuntime) Stop() error                   { return nil }
func (fakeRuntime) Status() (RuntimeState, error) { return RuntimeStateStopped, nil }
func (fakeRuntime) Events() (events.Subscription, error) {
	return nil, nil
}

var _ Runtime = fakeRuntime{}

func TestRuntimeStates(t *testing.T) {
	if RuntimeStateStopped != "stopped" || RuntimeStateRunning != "running" {
		t.Fatalf("runtime states drifted: %q, %q", RuntimeStateStopped, RuntimeStateRunning)
	}
}
