package control

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/session"
)

func TestLegacySubmissionReportsSynchronousAdmission(t *testing.T) {
	for name, req := range map[string]SubmissionRequest{
		"http":           {HTTP: true, Input: "hello"},
		"retired action": {Action: FinalReadinessRecoveryAction, Input: "continue"},
	} {
		t.Run(name, func(t *testing.T) {
			c := newOwnedTestController(t, Options{Runner: noOpTurnRunner{}, Sink: event.Discard})
			if _, err := c.SubmitIdentified(req); err != nil {
				t.Fatal(err)
			}
			c.Close()
			if _, err := c.SubmitIdentified(req); !errors.Is(err, ErrSubmissionNotAccepted) {
				t.Fatalf("submission after close error = %v, want ErrSubmissionNotAccepted", err)
			}
		})
	}
}

func TestShellSubmissionIsDurableBeforeCommandDispatchAndDeduplicated(t *testing.T) {
	var ctrl *Controller
	var dispatches atomic.Int32
	var admitted atomic.Bool
	done := make(chan struct{}, 1)
	verified := make(chan bool, 1)
	req := SubmissionRequest{ID: "shell-once", Action: "shell", Input: "echo fixture", Display: "echo fixture"}
	sink := event.FuncSink(func(ev event.Event) {
		if ev.Kind == event.TurnStarted {
			receipt, found := ctrl.sessionEventStore().Submission(req.ID)
			snapshot := ctrl.sessionEventStore().Snapshot()
			admitted.Store(found && MatchesSubmissionReceipt(req, receipt) && snapshot.DurableSequence >= snapshot.EventSequence)
		}
		if ev.Kind == event.ToolDispatch {
			dispatches.Add(1)
			verified <- admitted.Load()
		}
		if ev.Kind == event.TurnDone {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})
	ctrl = newOwnedTestController(t, Options{SessionPath: filepath.Join(t.TempDir(), "shell.jsonl"), Sink: sink})
	defer ctrl.Close()
	first, err := ctrl.SubmitIdentified(req)
	if err != nil {
		t.Fatal(err)
	}
	if !<-verified {
		t.Fatal("shell command dispatched before durable receipt")
	}
	<-done
	second, err := ctrl.SubmitIdentified(req)
	if err != nil || second != first || dispatches.Load() != 1 {
		t.Fatalf("duplicate shell: %v %+v dispatches=%d", err, second, dispatches.Load())
	}
}

func TestSubmissionIdentityDurableAndConflicting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	c := newOwnedTestController(t, Options{SessionPath: path, Sink: event.Discard})
	request := SubmissionRequest{ID: "request-1", Input: "hello", Display: "hello"}
	runs := 0
	receipt, err := c.submitIdentified(request, func() {
		runs++
		if err := c.prepareTurnAdmission(func(context.Context) error { return nil })(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.TurnID == "" || receipt.MessageID == "" {
		t.Fatalf("incomplete receipt: %+v", receipt)
	}
	retry, err := c.submitIdentified(request, func() { runs++ })
	if err != nil || retry != receipt || runs != 1 {
		t.Fatalf("retry=%+v runs=%d err=%v", retry, runs, err)
	}
	request.Input = "different"
	if _, err := c.submitIdentified(request, func() { runs++ }); err == nil {
		t.Fatal("conflicting request accepted")
	}
	if err := c.emitTurnEventChecked(event.Event{Kind: event.TurnDone, Status: event.TurnCompleted}); err != nil {
		t.Fatal(err)
	}
	c.Close()
	reopened := newOwnedTestController(t, Options{SessionPath: path, Sink: event.Discard})
	defer reopened.Close()
	request.Input = "hello"
	recovered, found, err := reopened.LookupSubmission(request)
	if err != nil || !found || recovered != receipt {
		t.Fatalf("recovery: %+v %v %v", recovered, found, err)
	}
}

func TestSubmissionIdentityConcurrentPublicAdmission(t *testing.T) {
	c := newOwnedTestController(t, Options{SessionPath: filepath.Join(t.TempDir(), "session.jsonl"), Sink: event.Discard})
	defer c.Close()
	req := SubmissionRequest{ID: "concurrent", Input: "/mcp__definitely_missing", Display: "request"}
	const callers = 12
	start := make(chan struct{})
	receipts := make(chan session.SubmissionReceipt, callers)
	errs := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Go(func() {
			<-start
			receipt, err := c.SubmitIdentified(req)
			receipts <- receipt
			errs <- err
		})
	}
	close(start)
	group.Wait()
	close(receipts)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first session.SubmissionReceipt
	for receipt := range receipts {
		if first.SubmissionID == "" {
			first = receipt
		}
		if receipt != first || receipt.TurnID == "" {
			t.Fatalf("different admission: %+v / %+v", first, receipt)
		}
	}
	if receipt, found, err := c.LookupSubmission(req); err != nil || !found || receipt != first {
		t.Fatalf("lookup: %+v %v %v", receipt, found, err)
	}
}

func TestSubmissionIdentityInterruptedAdmissionDoesNotReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	c := newOwnedTestController(t, Options{SessionPath: path, Sink: event.Discard})
	req := SubmissionRequest{ID: "accepted-before-body", Input: "a side effect"}
	first, err := c.submitIdentified(req, func() {
		// Persist admission without ever invoking the returned execution body.
		_ = c.prepareTurnAdmission(func(context.Context) error { t.Fatal("body ran"); return nil })
	})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	reopened := newOwnedTestController(t, Options{SessionPath: path, Sink: event.Discard})
	defer reopened.Close()
	got, err := reopened.submitIdentified(req, func() { t.Fatal("uncertain accepted input was replayed") })
	if err != nil || got != first {
		t.Fatalf("retry: %+v %v", got, err)
	}
	for _, changed := range []SubmissionRequest{
		{ID: req.ID, Input: req.Input, Original: "different edit"},
		{ID: req.ID, Input: req.Input, Invocations: []InvocationRequest{{Name: "different"}}},
		{ID: req.ID, Input: req.Input, ToolApprovalMode: "yolo"},
	} {
		if _, _, err := reopened.LookupSubmission(changed); err == nil {
			t.Fatal("execution options omitted from fingerprint")
		}
	}
}
