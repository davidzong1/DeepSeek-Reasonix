package control

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/permission"
	"reasonix/internal/sandbox"
)

// recordingEscalator captures the cards a decider is offered and counts how
// often each release runs, so "exactly once, on every exit path" is assertable.
type recordingEscalator struct {
	got      chan WriteAccessEscalation
	releases *int32
}

func newRecordingEscalator() *recordingEscalator {
	return &recordingEscalator{got: make(chan WriteAccessEscalation, 4), releases: new(int32)}
}

func (r *recordingEscalator) BeginWriteAccessEscalation(esc WriteAccessEscalation) func() {
	r.got <- esc
	return func() { atomic.AddInt32(r.releases, 1) }
}

func escalatedController(t *testing.T, esc WriteAccessEscalator) *Controller {
	t.Helper()
	c := New(Options{
		Policy:     permission.New("allow", nil, nil, nil),
		WriteRoots: sandbox.NewWritableRootSet([]string{t.TempDir()}),
		Sink:       event.Discard,
	})
	c.SetWriteAccessEscalator(esc)
	return c
}

// requestInBackground runs the one function every out-of-scope write funnels
// through, the way the blocked turn's goroutine runs it.
func requestInBackground(ctx context.Context, c *Controller, dir string) chan approvalReply {
	out := make(chan approvalReply, 1)
	go func() {
		reply, err := c.requestWriteAccessDecision(ctx, "write_file", dir, []byte(`{}`), "outside",
			event.NormalizeWriteAccessApproval(&event.WriteAccessApproval{
				Directories:        []string{dir},
				DisplayDirectories: []string{"out"},
				Justification:      "write the generated dataset",
			}))
		if err != nil {
			out <- approvalReply{}
			return
		}
		out <- reply
	}()
	return out
}

func TestEscalationOffersTheCardAndTheDecisionUnblocksTheTurn(t *testing.T) {
	dir := canonicalWriteTestDir(t)
	esc := newRecordingEscalator()
	c := escalatedController(t, esc)

	replies := requestInBackground(context.Background(), c, dir)
	got := <-esc.got
	if got.ApprovalID == "" || got.Tool != "write_file" || got.Subject != dir {
		t.Fatalf("escalation = %+v", got)
	}
	if len(got.Directories) != 1 || got.Directories[0] != dir {
		t.Fatalf("escalation dirs = %v, want %v", got.Directories, dir)
	}
	if got.Justification != "write the generated dataset" {
		t.Fatalf("justification = %q", got.Justification)
	}

	// The decider answers through the same one-winner path a human uses.
	if err := c.ResolveApproval(got.ApprovalID, true, sandbox.ApprovalScopeOnce); err != nil {
		t.Fatal(err)
	}
	if reply := <-replies; !reply.allow {
		t.Fatalf("the blocked turn must unblock allowed, got %+v", reply)
	}
}

func TestEscalationReleaseRunsOnceOnResolve(t *testing.T) {
	dir := canonicalWriteTestDir(t)
	esc := newRecordingEscalator()
	c := escalatedController(t, esc)

	replies := requestInBackground(context.Background(), c, dir)
	got := <-esc.got
	if n := atomic.LoadInt32(esc.releases); n != 0 {
		t.Fatalf("release ran %d times before the prompt settled", n)
	}
	if err := c.ResolveApproval(got.ApprovalID, false, sandbox.ApprovalScopeOnce); err != nil {
		t.Fatal(err)
	}
	<-replies
	waitFor(t, func() bool { return atomic.LoadInt32(esc.releases) == 1 })
	if n := atomic.LoadInt32(esc.releases); n != 1 {
		t.Fatalf("release ran %d times, want exactly 1", n)
	}
}

func TestEscalationReleaseRunsOnCancelledTurn(t *testing.T) {
	dir := canonicalWriteTestDir(t)
	esc := newRecordingEscalator()
	c := escalatedController(t, esc)

	ctx, cancel := context.WithCancel(context.Background())
	replies := requestInBackground(ctx, c, dir)
	<-esc.got
	cancel()
	<-replies
	waitFor(t, func() bool { return atomic.LoadInt32(esc.releases) == 1 })
}

// TestEscalationReleaseRunsOnBoundedWait covers the one path that does not go
// through the caller's context: a controller built with an approval timeout
// settles the prompt on its own, and the release must still run exactly once.
func TestEscalationReleaseRunsOnBoundedWait(t *testing.T) {
	dir := canonicalWriteTestDir(t)
	esc := newRecordingEscalator()
	c := New(Options{
		Policy:          permission.New("allow", nil, nil, nil),
		WriteRoots:      sandbox.NewWritableRootSet([]string{t.TempDir()}),
		Sink:            event.Discard,
		ApprovalTimeout: 50 * time.Millisecond,
	})
	c.SetWriteAccessEscalator(esc)

	replies := requestInBackground(context.Background(), c, dir)
	<-esc.got
	<-replies
	waitFor(t, func() bool { return atomic.LoadInt32(esc.releases) == 1 })
}

// TestEscalationAbsentKeepsTheFrontendCard pins the regression guard: with no
// decider installed the card is emitted and answered exactly as before.
func TestEscalationAbsentKeepsTheFrontendCard(t *testing.T) {
	dir := canonicalWriteTestDir(t)
	seen := make(chan event.Event, 4)
	c := New(Options{
		Policy:     permission.New("allow", nil, nil, nil),
		WriteRoots: sandbox.NewWritableRootSet([]string{t.TempDir()}),
		Sink:       event.FuncSink(func(ev event.Event) { seen <- ev }),
	})

	replies := requestInBackground(context.Background(), c, dir)
	var card event.Approval
	deadline := time.After(2 * time.Second)
	for card.ID == "" {
		select {
		case ev := <-seen:
			if ev.Kind == event.ApprovalRequest {
				card = ev.Approval
			}
		case <-deadline:
			t.Fatal("no approval card was emitted for the frontend")
		}
	}
	if card.Kind != writeAccessKind || !card.Fresh {
		t.Fatalf("card = %+v, want a fresh write-access card", card)
	}
	if err := c.ResolveApproval(card.ID, true, sandbox.ApprovalScopeOnce); err != nil {
		t.Fatal(err)
	}
	if reply := <-replies; !reply.allow {
		t.Fatalf("frontend answer must still unblock the turn, got %+v", reply)
	}
}

// TestEscalationStaleAnswerIsSilentlyAccepted characterization-pins the
// semantics a decider must NOT rely on: answering an id whose prompt is already
// gone returns nil, not an error (resolveApprovalLocked returns early when peek
// finds no reply). Only a concurrent lost race reports "no longer pending". So
// "my decision landed" can only be established by checking the request is still
// live in the decider's own registry — never by the return value alone.
func TestEscalationStaleAnswerIsSilentlyAccepted(t *testing.T) {
	dir := canonicalWriteTestDir(t)
	esc := newRecordingEscalator()
	c := escalatedController(t, esc)

	replies := requestInBackground(context.Background(), c, dir)
	got := <-esc.got
	if err := c.ResolveApproval(got.ApprovalID, true, sandbox.ApprovalScopeOnce); err != nil {
		t.Fatal(err)
	}
	<-replies
	if err := c.ResolveApproval(got.ApprovalID, true, sandbox.ApprovalScopeOnce); err != nil {
		t.Fatalf("a replayed answer reported %v; the decider must gate on its own registry, not on this", err)
	}
}

func waitFor(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never held")
}
