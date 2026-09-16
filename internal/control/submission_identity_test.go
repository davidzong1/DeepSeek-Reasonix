package control

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/session"
)

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
