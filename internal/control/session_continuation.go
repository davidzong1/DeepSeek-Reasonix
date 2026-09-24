package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// Session continuation is the non-destructive rotation a context rescue uses.
// The ordinary rotations cannot express this move: /new leaves the source in
// history but carries no main line forward, and /clear deletes the source
// outright — the exact loss a rescue is trying to avoid. A continuation keeps
// the source intact, publishes a fresh identity whose first message is a
// bounded recovery block, links the two by lineage, and hands the caller one
// member-scoped resume. See session_continuation_rotation.go for the operation.
//
// The controller owns the transaction, not the judgement. Deciding that
// ordinary compaction has stopped recovering the window, freezing the
// transcript, and tokenizer-verifying the summary all happen before a
// ContinuationPlan exists; this contract validates a plan and never recomputes
// the reduction ratio or re-derives the summary.
const (
	// ContinuationReason is the rotation reason recorded for a rescue rotation.
	// Hosts key their non-archiving behaviour off it: unlike "clear", a
	// continuation must never archive or delete the source session.
	ContinuationReason = "context_rescue"

	// ContinuationMessageBudget is the hard provider-visible ceiling for the
	// whole injected continuation message, wrapper included.
	ContinuationMessageBudget = 10_000

	// ContinuationSummaryBudget is the target the summary aims for, leaving the
	// remainder of the message ceiling to the wrapper and role framing. It is a
	// budget to report against, not a refusal: only the measured message is one.
	ContinuationSummaryBudget = 8_000

	// MaxAutomaticContinuations bounds an automatic rescue chain. A main line
	// that cannot fit after this many attempts needs a human, not a further
	// cold prefix.
	MaxAutomaticContinuations = 3
)

// Classified continuation failures, so a rescue never degrades into a silently
// lossy rotation.
var (
	// ErrContinuationUnsupported reports a controller without an exclusive v3
	// identity: the legacy path session has no non-destructive rotation, and
	// falling back to the destructive clear is exactly what must not happen.
	ErrContinuationUnsupported = errors.New("control: session continuation requires an exclusive v3 session")
	// ErrContinuationPlanInvalid reports a plan that failed admission checks.
	ErrContinuationPlanInvalid = errors.New("control: continuation plan is invalid")
	// ErrContinuationDuplicate reports a replay of a rescue that already ran.
	ErrContinuationDuplicate = errors.New("control: continuation already started for this dedup key")
	// ErrContinuationCeiling reports an attempt past MaxAutomaticContinuations.
	ErrContinuationCeiling = errors.New("control: automatic continuation ceiling reached")
	// ErrContinuationBudget reports a recovery message over its token budget.
	ErrContinuationBudget = errors.New("control: continuation message exceeds its token budget")
	// ErrContinuationStale reports that the source transcript moved past the
	// generation the plan was frozen from.
	ErrContinuationStale = errors.New("control: source transcript changed under the continuation plan")
)

// ContinuationPlan is the frozen contract between the agent-side context
// rescue and this controller-side rotation.
type ContinuationPlan struct {
	// Trigger names why the rescue fired, for diagnostics only.
	Trigger string

	// SourceTokens and CompactedTokens are the provider-visible request
	// estimates before and after the ordinary compact, in one shared tokenizer
	// 口径. They are recorded, never recomputed.
	SourceTokens    int
	CompactedTokens int

	// ReductionRatio is (SourceTokens-CompactedTokens)/SourceTokens as measured
	// by the caller.
	ReductionRatio float64

	// Summary is the structured recovery block — main-line goal, binding
	// constraints, progress, decisions, changed files, command results,
	// blockers and next step. It is the only transcript content that crosses.
	Summary string
	// SummaryHash is the caller's digest of Summary.
	SummaryHash string
	// SummaryTokens is the caller-measured token count of the summary; the
	// controller re-measures the message it injects and refuses a plan whose
	// declaration does not match what it can see.
	SummaryTokens int

	// Message is the already-rendered continuation message. The agent-side
	// rescue builds it, so the controller injects it verbatim rather than
	// wrapping Summary twice; an empty Content falls back to rendering Summary.
	Message provider.Message

	// Generation is the source event sequence the summary was frozen from, as
	// reported by Controller.SessionGeneration. A source that has moved past it
	// invalidates the plan.
	Generation uint64

	// DedupKey identifies this rescue attempt. A replay that already committed
	// its source marker is refused, so a crash between the marker and the
	// resume cannot start a second continuation.
	DedupKey string
	// Lineage is stable across a rescue chain, so the attempt ceiling counts
	// main-line rescues rather than per-session ones.
	Lineage string
	// Attempt is the 1-based rescue attempt within Lineage.
	Attempt int

	// Resume enqueues the member-scoped continuation turn. It runs exactly once
	// after the transition is committed and the rotation gate released, so the
	// resumed turn enters the ordinary admission guard. Nil leaves it idle.
	Resume func(ctx context.Context, resume ContinuationResume) error
}

// ContinuationResume is what a Resume callback is handed: the two identities it
// may act on and the lineage they belong to. It deliberately carries no
// controller and no member roster — the callback closes over the one member it
// is resuming, which is what keeps a rescue from reaching a peer.
type ContinuationResume struct {
	Source       session.SessionRef
	Continuation session.SessionRef
	Lineage      string
	Attempt      int
	DedupKey     string
}

// ContinuationResult reports a committed continuation. Telemetry is the
// per-rescue record a host folds into its per-request diagnostics; it never
// carries transcript text.
type ContinuationResult struct {
	Source        session.SessionRef
	Continuation  session.SessionRef
	Lineage       string
	Attempt       int
	DedupKey      string
	MessageID     string
	SummaryHash   string
	SummaryTokens int
	MessageTokens int
	// Resumed reports whether the Resume callback was invoked and accepted.
	Resumed bool
	// Telemetry is always populated, including on the replay path.
	Telemetry ContinuationTelemetry
}

// ContinuationTelemetry is the independent rescue record. It is emitted for
// every attempt — committed, replayed, or refused — so a rescue rate can be
// read without parsing transcripts.
type ContinuationTelemetry struct {
	Trigger         string  `json:"trigger"`
	SourceTokens    int     `json:"sourceTokens"`
	CompactedTokens int     `json:"compactedTokens"`
	ReductionRatio  float64 `json:"reductionRatio"`
	SummaryTokens   int     `json:"summaryTokens"`
	MessageTokens   int     `json:"messageTokens"`
	Generation      uint64  `json:"generation"`
	Attempt         int     `json:"attempt"`
	Outcome         string  `json:"outcome"`
}

// Continuation outcomes recorded in ContinuationTelemetry.Outcome.
const (
	continuationOutcomeResumed   = "resumed"
	continuationOutcomeRotated   = "rotated_without_resume"
	continuationOutcomeReplayed  = "replayed"
	continuationOutcomeRejected  = "rejected"
	continuationOutcomeResumeErr = "resume_failed"
)

// ContinuationLineage describes a rescue to the identity-owning host. The host
// must treat DedupKey as its reservation key: two rotations carrying the same
// key are one rescue, and the controller refuses the second before asking the
// host to reserve anything. SessionID itself need not derive from DedupKey — a
// retry after a failed publication needs a fresh identity, because a session
// directory that already exists cannot be recreated.
type ContinuationLineage struct {
	Lineage         string
	DedupKey        string
	Attempt         int
	Trigger         string
	SummaryHash     string
	SummaryTokens   int
	SourceTokens    int
	CompactedTokens int
	ReductionRatio  float64
}

// validateContinuationPlan enforces the admission rules the controller owns:
// the plan must be internally consistent, within budget, and inside the
// attempt ceiling. It never re-derives the ratio or the summary.
func validateContinuationPlan(plan ContinuationPlan) error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrContinuationPlanInvalid, fmt.Sprintf(format, args...))
	}
	if strings.TrimSpace(plan.Summary) == "" {
		return invalid("recovery summary is empty")
	}
	if strings.TrimSpace(plan.SummaryHash) == "" {
		return invalid("summary hash is required")
	}
	if want := continuationDigest(plan.Summary); want != plan.SummaryHash {
		return invalid("summary hash does not match the summary")
	}
	if strings.TrimSpace(plan.DedupKey) == "" {
		return invalid("dedup key is required")
	}
	if strings.TrimSpace(plan.Lineage) == "" {
		return invalid("lineage is required")
	}
	if plan.Attempt < 1 {
		return invalid("attempt must be positive")
	}
	if plan.Attempt > MaxAutomaticContinuations {
		return fmt.Errorf("%w: attempt %d of %d", ErrContinuationCeiling, plan.Attempt, MaxAutomaticContinuations)
	}
	if plan.SourceTokens < 0 || plan.CompactedTokens < 0 {
		return invalid("token counts must not be negative")
	}
	if plan.ReductionRatio < 0 || plan.ReductionRatio > 1 {
		return invalid("reduction ratio %v is outside [0,1]", plan.ReductionRatio)
	}
	if plan.SummaryTokens < 0 {
		return invalid("summary token count must not be negative")
	}
	// A summary that alone exceeds the whole-message ceiling cannot fit whatever
	// the wrapper costs, so the declaration is impossible rather than merely over
	// target. The rendered-message measurement is the authority for the rest.
	if plan.SummaryTokens > ContinuationMessageBudget {
		return fmt.Errorf("%w: declared summary is %d tokens, over the %d-token message ceiling",
			ErrContinuationBudget, plan.SummaryTokens, ContinuationMessageBudget)
	}
	return nil
}

// continuationDigest is the hash the frozen contract requires of a summary.
func continuationDigest(summary string) string {
	sum := sha256.Sum256([]byte(summary))
	return hex.EncodeToString(sum[:])
}

// continuationTextTokens is this package's conservative, tokenizer-independent
// estimate, matching the cross-language approximation the agent side uses. The
// controller needs its own copy so it can refuse an over-budget message without
// importing an unexported helper.
func continuationTextTokens(s string) int {
	if s == "" {
		return 0
	}
	byBytes := (len(s) + 3) / 4
	if runes := utf8.RuneCountInString(s); runes > byBytes {
		return runes
	}
	return byBytes
}

// continuationMessageTokenEstimate sizes the whole injected message the way the
// provider will see it: role framing, the wrapper, and the summary body.
func continuationMessageTokenEstimate(message provider.Message) int {
	total := 4 // chat-message framing overhead
	total += continuationTextTokens(message.Content)
	total += continuationTextTokens(message.ReasoningContent)
	for _, call := range message.ToolCalls {
		total += 8
		total += continuationTextTokens(call.ID)
		total += continuationTextTokens(call.Name)
		total += continuationTextTokens(call.Arguments)
	}
	return total
}

const (
	continuationTagOpen  = "<context-continuation>"
	continuationTagClose = "</context-continuation>"
)

// continuationMessageID mints the continuation message identity in the
// transcript's own id format. It is deliberately random rather than derived
// from the plan: the projection treats a repeated stable message id as a
// damaged log, so a rescue that somehow reached the seed step twice must
// produce two ids, not one colliding pair. Duplicate rescues are refused
// earlier and durably, by the source marker and the host's reservation.
func continuationMessageID() string {
	return agent.NewMessageID()
}

// continuationMessage is the one message a continuation session opens with. A
// plan that already carries a rendered briefing keeps it byte-for-byte; only a
// plan without one gets the wrapper below. Either way the message stays an
// ordinary host-generated user message, so the new session's cache-stable
// prefix is exactly that of any other fresh session.
func continuationMessage(plan ContinuationPlan, source session.SessionRef) provider.Message {
	if strings.TrimSpace(plan.Message.Content) != "" {
		message := plan.Message
		message.Role = provider.RoleUser
		// Host origin is what marks this as host-generated protocol content
		// rather than the member's own input; a caller that omitted it does not
		// get to have its briefing read as an instruction.
		message.Origin = provider.MessageOriginHost
		if message.ID == "" {
			message.ID = continuationMessageID()
		}
		return message
	}
	return renderContinuationMessage(plan, source)
}

// renderContinuationMessage builds the fallback wrapper for a plan that carries
// only the summary body.
func renderContinuationMessage(plan ContinuationPlan, source session.SessionRef) provider.Message {
	var body strings.Builder
	body.WriteString(continuationTagOpen)
	body.WriteString("\nThis session continues an earlier conversation whose context window was exhausted.\n")
	body.WriteString("The block below is a handover written by the host, not new user input.\n")
	fmt.Fprintf(&body, "source session: %s\n", source.SessionID)
	fmt.Fprintf(&body, "source generation: %d\n", plan.Generation)
	fmt.Fprintf(&body, "lineage: %s\n", plan.Lineage)
	fmt.Fprintf(&body, "attempt: %d\n", plan.Attempt)
	fmt.Fprintf(&body, "trigger: %s\n", plan.Trigger)
	fmt.Fprintf(&body, "summary hash: %s\n", plan.SummaryHash)
	fmt.Fprintf(&body, "summary tokens: %d\n", plan.SummaryTokens)
	body.WriteString("\n--- recovery block ---\n")
	body.WriteString(plan.Summary)
	body.WriteString("\n--- end recovery block ---\n")
	body.WriteString("Resume the main-line task from this block. Items marked unknown were not\n")
	body.WriteString("recovered; verify them before acting on them rather than assuming them.\n")
	body.WriteString(continuationTagClose)

	return provider.Message{
		ID:      continuationMessageID(),
		Role:    provider.RoleUser,
		Origin:  provider.MessageOriginHost,
		Content: body.String(),
	}
}

// continuationMarkerOperationID is the durable identity of the source-side
// marker. Deriving it from the plan makes a replayed rescue write the same
// logical batch, which the session's own operation dedup turns into a no-op.
func continuationMarkerOperationID(plan ContinuationPlan) string {
	return "session-continuation:" + plan.Lineage + ":" + plan.DedupKey
}

// continuationMarkerPayload is the durable audit record written into the source
// session. It is a "diagnostic" event: optional, excluded from the
// provider-visible projection, and safe for older readers to ignore.
func continuationMarkerPayload(plan ContinuationPlan, source, continuation session.SessionRef, messageTokens int) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"type":              "context-continuation-v1",
		"trigger":           plan.Trigger,
		"lineage":           plan.Lineage,
		"attempt":           plan.Attempt,
		"dedupKey":          plan.DedupKey,
		"summaryHash":       plan.SummaryHash,
		"summaryTokens":     plan.SummaryTokens,
		"messageTokens":     messageTokens,
		"sourceTokens":      plan.SourceTokens,
		"compactedTokens":   plan.CompactedTokens,
		"reductionRatio":    plan.ReductionRatio,
		"generation":        plan.Generation,
		"sourceSessionId":   source.SessionID,
		"continuationId":    continuation.SessionID,
		"continuationFirst": true,
	})
}

// continuationLedger is the in-process half of rescue bookkeeping: it refuses a
// second start for one attempt, and counts committed continuations per lineage
// so a chain that keeps thrashing stops. Durable anti-duplication stays where it
// belongs — on the source session's marker and on the host's reservation.
type continuationLedger struct {
	mu      sync.Mutex
	entries map[string]continuationEntry
	commits map[string]int
}

type continuationEntry struct {
	result ContinuationResult
	// pending marks an attempt this process is applying right now. A concurrent
	// caller with the same plan must be refused rather than racing the rotation
	// gate, which would otherwise let both reach beginRotation.
	pending bool
}

// continuationReservation is what begin decided for one plan.
type continuationReservation int

const (
	// continuationReserved means this call owns the attempt.
	continuationReserved continuationReservation = iota
	// continuationReplay means a committed result already exists for the key.
	continuationReplay
	// continuationBusy means the same attempt is mid-rotation in this process.
	continuationBusy
)

// begin reserves the dedup key for the attempt. The in-flight marker is
// released by abandon on every path that does not commit, so a reservation
// cannot outlive the call that made it.
func (l *continuationLedger) begin(plan ContinuationPlan) (ContinuationResult, continuationReservation) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = map[string]continuationEntry{}
	}
	if l.commits == nil {
		l.commits = map[string]int{}
	}
	key := continuationLedgerKey(plan)
	if entry, ok := l.entries[key]; ok {
		if entry.pending {
			return ContinuationResult{}, continuationBusy
		}
		return entry.result, continuationReplay
	}
	l.entries[key] = continuationEntry{pending: true}
	return ContinuationResult{}, continuationReserved
}

// committed reports how many continuations lineage has already spent.
func (l *continuationLedger) committed(lineage string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.commits[lineage]
}

// finish records the committed result so a later replay answers from the ledger
// instead of rotating again, and charges the lineage one attempt.
func (l *continuationLedger) finish(plan ContinuationPlan, result ContinuationResult) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = map[string]continuationEntry{}
	}
	if l.commits == nil {
		l.commits = map[string]int{}
	}
	key := continuationLedgerKey(plan)
	if prior, ok := l.entries[key]; !ok || prior.pending {
		l.commits[plan.Lineage]++
	}
	l.entries[key] = continuationEntry{result: result}
}

// abandon releases a reservation whose rotation never committed, so a retry of
// the same plan can proceed.
func (l *continuationLedger) abandon(plan ContinuationPlan) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := continuationLedgerKey(plan)
	if entry, ok := l.entries[key]; ok && entry.pending {
		delete(l.entries, key)
	}
}

// continuationLineageOperation is the durable type tag of the record that
// carries a rescue chain's identity into the session it continues in.
const continuationLineageOperation = "context-continuation-lineage-v1"

// continuationLineageRecord is the chain identity a continuation session
// inherits. Reading it back from the session is what lets the second rescue of
// a chain count against the first one's ceiling instead of starting over.
type continuationLineageRecord struct {
	Type     string `json:"type"`
	Lineage  string `json:"lineage"`
	Attempt  int    `json:"attempt"`
	DedupKey string `json:"dedupKey"`
	Source   string `json:"sourceSessionId"`
}

// continuationLineageEvents wraps the chain identity as seed events for the
// child session. It is a diagnostic: durable, and excluded from the
// provider-visible projection.
func continuationLineageEvents(plan ContinuationPlan, source session.SessionRef) []session.Event {
	if strings.TrimSpace(plan.Lineage) == "" {
		return nil
	}
	payload, err := json.Marshal(continuationLineageRecord{
		Type: continuationLineageOperation, Lineage: plan.Lineage, Attempt: plan.Attempt,
		DedupKey: plan.DedupKey, Source: source.SessionID,
	})
	if err != nil {
		return nil
	}
	return []session.Event{{Kind: "diagnostic", Optional: true, Payload: payload}}
}

// readContinuationLineage reports the chain identity the session inherited, if
// it is itself a continuation. The record is written with the session's first
// batch, so the scan is bounded to the opening commits.
func readContinuationLineage(ctx context.Context, store *session.Session) (string, int, bool) {
	if store == nil {
		return "", 0, false
	}
	page, err := store.AcceptedPage(ctx, 0, continuationLineageScanLimit)
	if err != nil {
		return "", 0, false
	}
	for _, commit := range page.Commits {
		for _, ev := range commit.Events {
			if ev.Kind != "diagnostic" || len(ev.Payload) == 0 {
				continue
			}
			var record continuationLineageRecord
			if err := json.Unmarshal(ev.Payload, &record); err != nil {
				continue
			}
			if record.Type == continuationLineageOperation && record.Lineage != "" {
				return record.Lineage, record.Attempt, true
			}
		}
	}
	return "", 0, false
}

// continuationLineageScanLimit bounds the lineage scan to a session's opening.
const continuationLineageScanLimit = 8

// continuationLedgerKey identifies one rescue attempt within a chain.
func continuationLedgerKey(plan ContinuationPlan) string {
	return plan.Lineage + "\x00" + plan.DedupKey
}
