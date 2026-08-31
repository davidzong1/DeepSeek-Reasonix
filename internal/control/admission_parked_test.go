package control

import (
	"context"
	"strings"
	"testing"
)

// TestSubmitUserTurnOrErrorAcceptsParkedTurn pins the team runtime's execution
// gate: a turn that parks in the finishing window has been ACCEPTED — it runs as
// soon as the current turn settles — so reporting it as a refusal made the
// runtime roll a delivered task back to assigned, release the member
// reservation, and then reject the member's own report with ErrTaskUnknown.
func TestSubmitUserTurnOrErrorAcceptsParkedTurn(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	c := New(Options{Sink: holdFinishingWindow(release, entered, nil)})
	t.Cleanup(func() { c.Close() })

	c.runGuarded(func(context.Context) error { return nil })
	<-entered // the controller is now inside its finishing window

	if err := c.SubmitUserTurnOrError("do the work", "do the work"); err != nil {
		t.Fatalf("a parked turn is accepted, not refused: %v", err)
	}
	close(release)
	waitIdleAdmission(t, c)
}

// TestSubmitUserTurnOrErrorNamesTheRefusal pins the diagnostic: the leader reads
// this string, so a genuine refusal says why in words. The numeric admission
// stays for cross-referencing the guard.
func TestSubmitUserTurnOrErrorNamesTheRefusal(t *testing.T) {
	c := New(Options{})
	c.Close()
	err := c.SubmitUserTurnOrError("do the work", "do the work")
	if err == nil {
		t.Fatal("a closed session must refuse the turn")
	}
	if !strings.Contains(err.Error(), "session is closed") {
		t.Fatalf("err = %q, want the reason named", err)
	}
	if !strings.Contains(err.Error(), "did not accept the turn") {
		t.Fatalf("err = %q, want the refusal marker the runtime matches on", err)
	}
}

// TestAdmissionResultStringsAreDistinct keeps the reason text usable: every
// outcome needs its own words, or two different refusals read identically.
func TestAdmissionResultStringsAreDistinct(t *testing.T) {
	seen := map[string]admissionResult{}
	for _, res := range []admissionResult{
		turnStarted, turnParked, turnDroppedRunning, turnDroppedRotating,
		turnDroppedClosed, turnDroppedDraining, turnDroppedWriteAuthority,
	} {
		text := res.String()
		if text == "" || strings.Contains(text, "unknown") {
			t.Errorf("admission %d has no reason text", int(res))
		}
		if prev, dup := seen[text]; dup {
			t.Errorf("admission %d and %d share the text %q", int(prev), int(res), text)
		}
		seen[text] = res
	}
}
