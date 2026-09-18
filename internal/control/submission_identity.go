package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/session"
)

// SubmissionRequest fingerprints the actual operation, never only its label.
type SubmissionRequest struct {
	ID               string              `json:"-"`
	HTTP             bool                `json:"http,omitempty"`
	Input            string              `json:"input"`
	Display          string              `json:"display,omitempty"`
	Format           string              `json:"format,omitempty"`
	Action           string              `json:"action,omitempty"`
	RecoveryID       string              `json:"recoveryId,omitempty"`
	Original         string              `json:"original,omitempty"`
	Goal             string              `json:"goal,omitempty"`
	ToolApprovalMode string              `json:"toolApprovalMode,omitempty"`
	Invocations      []InvocationRequest `json:"invocations,omitempty"`
}

// ErrSubmissionNotAccepted is only attached to failures that precede durable
// admission. Transport and flush failures must never use this disposition.
var ErrSubmissionNotAccepted = errors.New("submission not accepted")

type submissionIdentityState struct {
	mu        sync.Mutex
	pending   atomic.Pointer[session.SubmissionReceipt]
	reused    atomic.Uint64
	conflicts atomic.Uint64
	unknown   atomic.Uint64
}

func (c *Controller) appendSubmissionEvent(events []session.Event, turnID string) []session.Event {
	if pending := c.submissions.pending.Load(); pending != nil {
		receipt := *pending
		receipt.TurnID = turnID
		data, _ := json.Marshal(receipt)
		return append(events, session.Event{Kind: "submission/accepted", Optional: true, Payload: data})
	}
	return events
}

func (c *Controller) trySubmissionAdmissionLock() func() {
	if !c.submissions.mu.TryLock() {
		return nil
	}
	return sync.OnceFunc(c.submissions.mu.Unlock)
}

func (c *Controller) releaseSubmissionAdmission() {
	c.submissions.mu.Unlock()
	// A synchronous queue dispatch may have deferred while submit owned the gate.
	c.maybeDispatchInbox()
}

func submissionFingerprint(req SubmissionRequest) string {
	data, _ := json.Marshal(req)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// MatchesSubmissionReceipt validates a cold, durable receipt without creating
// a Controller or including host identity in provider-visible input.
func MatchesSubmissionReceipt(req SubmissionRequest, receipt session.SubmissionReceipt) bool {
	return req.ID == receipt.SubmissionID && receipt.Fingerprint == submissionFingerprint(req)
}

// LookupSubmission also detects accidental reuse of a key for different input.
func (c *Controller) LookupSubmission(req SubmissionRequest) (session.SubmissionReceipt, bool, error) {
	store := c.sessionEventStore()
	if store == nil || req.ID == "" {
		return session.SubmissionReceipt{}, false, nil
	}
	receipt, ok := store.Submission(req.ID)
	if ok && receipt.Fingerprint != submissionFingerprint(req) {
		c.submissions.conflicts.Add(1)
		slog.Warn("submission conflict", "submissionId", req.ID)
		return receipt, true, errors.New("submission identity conflicts with different input")
	}
	if ok {
		c.submissions.reused.Add(1)
		// The projection may already contain an asynchronously accepted batch.
		// A retry is acknowledged only after its canonical journal is durable.
		if _, err := store.Flush(context.Background()); err != nil {
			c.submissions.unknown.Add(1)
			return receipt, true, err
		}
		slog.Debug("submission already accepted", "submissionId", req.ID, "turnId", receipt.TurnID)
	}
	return receipt, ok, nil
}

// SubmitIdentified serializes identity checking with synchronous turn admission.
func (c *Controller) SubmitIdentified(req SubmissionRequest) (session.SubmissionReceipt, error) {
	if len(req.ID) > 256 || strings.ContainsAny(req.ID, "\x00\r\n") {
		return session.SubmissionReceipt{}, errors.Join(ErrSubmissionNotAccepted, errors.New("invalid submission identity"))
	}
	return c.submitIdentified(req, func() {
		switch {
		case req.Action == ProtocolRecoveryAction:
			c.submitProtocolRecoveryLocked(req.RecoveryID, req.Input)
		case req.Action == "delivery-recovery" || req.Action == FinalReadinessRecoveryAction:
			c.submitFinalReadinessRecoveryLocked(req.Display, req.Input)
		case req.Action == "shell":
			// TurnStarted persists the pending receipt synchronously before
			// runGuarded can launch the shell body.
			c.RunShell(req.Input)
		case req.HTTP:
			c.submitHTTPWithFormatLocked(req.Input, req.Display, req.Format)
		case len(req.Invocations) > 0:
			c.submitInvocationsLocked(req.Input, req.Display, req.Invocations)
		default:
			c.submitLocked(req.Input, req.Display, req.Original)
		}
	})
}

func (c *Controller) submitIdentified(req SubmissionRequest, submit func()) (session.SubmissionReceipt, error) {
	c.submissions.mu.Lock()
	defer c.releaseSubmissionAdmission()
	if receipt, ok, err := c.LookupSubmission(req); ok || err != nil {
		return receipt, err
	}
	store := c.sessionEventStore()
	if req.ID == "" {
		before, _, _ := c.currentTurnToken()
		submit()
		after, _, _ := c.currentTurnToken()
		if after == before {
			return session.SubmissionReceipt{}, errors.Join(ErrSubmissionNotAccepted, errors.New("submission was not admitted"))
		}
		return session.SubmissionReceipt{}, nil
	}
	if store == nil {
		return session.SubmissionReceipt{}, errors.Join(ErrSubmissionNotAccepted, errors.New("durable submission identity unavailable"))
	}
	if c.Running() {
		return session.SubmissionReceipt{}, errors.Join(ErrSubmissionNotAccepted, ErrTurnRunning)
	}
	receipt := &session.SubmissionReceipt{SessionID: store.ID(), SubmissionID: req.ID,
		Fingerprint: submissionFingerprint(req), MessageID: agent.NewMessageID()}
	c.submissions.pending.Store(receipt)
	defer c.submissions.pending.Store(nil)
	c.SetTurnSubmissionID(req.ID)
	submit()
	if err := c.flushSubmissionAdmission(); err != nil {
		c.submissions.unknown.Add(1)
		return session.SubmissionReceipt{}, err
	}
	accepted, ok := store.Submission(req.ID)
	if !ok {
		return session.SubmissionReceipt{}, errors.Join(ErrSubmissionNotAccepted, errors.New("submission was not durably admitted"))
	}
	return accepted, nil
}

func (c *Controller) submissionForTurn(turnID string) (session.SubmissionReceipt, bool) {
	store := c.sessionEventStore()
	if store == nil {
		return session.SubmissionReceipt{}, false
	}
	return store.SubmissionForTurn(turnID)
}

func (c *Controller) flushSubmissionAdmission() error {
	if c.submissions.pending.Load() == nil {
		return nil
	}
	_, err := c.sessionEventStore().Flush(context.Background())
	return err
}

func (c *Controller) flushSubmissionStart(kind event.Kind) error {
	if kind != event.TurnStarted {
		return nil
	}
	return c.flushSubmissionAdmission()
}

// TurnIDForSubmission exposes the synchronous admission receipt without
// depending on whether the provider is still running when the desktop call returns.
func (c *Controller) TurnIDForSubmission(submissionID string) string {
	if store := c.sessionEventStore(); store != nil {
		if receipt, ok := store.Submission(submissionID); ok {
			return receipt.TurnID
		}
	}
	ledger := c.turnEventLedger()
	if ledger == nil {
		return ""
	}
	return ledger.TurnIDForSubmission(submissionID)
}
