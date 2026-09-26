package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// Context rescue is the last rung before a lossy truncation: an ineffective fold
// distills the main line into one bounded continuation briefing and hands it to
// the caller, which owns the rotation decision. It never mutates the session.

const (
	// contextRescueReductionRatio is the compaction yield below which folding
	// counts as ineffective. reduction_ratio is
	// (source_tokens - compacted_tokens) / source_tokens measured on one
	// provider-visible scale: a view that shrank by less than a tenth was not
	// rescued by compaction, no matter how small the surviving digest looks.
	//
	// The inverse reading — "compacted_tokens / source_tokens < 0.10" — means
	// the opposite (compaction worked extremely well) and must never trigger a
	// rescue.
	contextRescueReductionRatio = 0.10
	// contextRescueMaxBlockTokens is the hard upper bound on the injected
	// continuation message. It is measured with the calibrated provider-visible
	// estimator, never with a character-count planning budget.
	contextRescueMaxBlockTokens = 10_000
	// contextRescueTargetTokens is the summarizer output target for the
	// continuation digest. The gap to the hard bound absorbs the tag wrapper,
	// the provenance header and the pending-tool section.
	contextRescueTargetTokens = 8_000
	// contextRescueMinSqueezeTokens floors the one bounded re-compression pass
	// so the rewrite still has room for the standing constraints.
	contextRescueMinSqueezeTokens = 1_024
	// contextRescueWrapperReserveTokens is what the non-briefing part of the
	// continuation message is assumed to cost when sizing the squeeze pass.
	contextRescueWrapperReserveTokens = 1_500
)

// Continuation briefing tags. They are deliberately distinct from
// summaryTagOpen: a rescued session's first message is history from another
// session, not an in-session fold, and must not be merged or superseded by
// later compaction the way a rolling digest is.
const (
	contextRescueTagOpen  = "<context-rescue>"
	contextRescueTagClose = "</context-rescue>"
)

// ContextRescueError codes. They classify why a continuation plan could not be
// produced, so a caller can decide between retrying, falling back and
// reporting, and so telemetry can attribute failures without transcript text.
// Cancellation is deliberately not one of them: a cancelled transaction keeps
// returning context.Canceled so callers propagate it unchanged.
const (
	ContextRescueNoWindowEstimate  = "no_window_estimate"
	ContextRescueNotEligible       = "not_eligible"
	ContextRescueEmptyRegion       = "empty_region"
	ContextRescueSummaryFailed     = "summary_failed"
	ContextRescueSummaryEmpty      = "summary_empty"
	ContextRescueSummaryTruncated  = "summary_truncated"
	ContextRescueOverBudget        = "over_budget"
	ContextRescueStaleTranscript   = "stale_transcript"
	ContextRescueUntrustedEstimate = "untrusted_estimate"
)

// ContextRescueTelemetry outcomes.
const (
	contextRescueOutcomePlanned = "planned"
	contextRescueOutcomeSkipped = "skipped"
	contextRescueOutcomeFailed  = "failed"
)

// ErrContextRescuePlanned reports that a validated continuation payload was
// produced for a view that must not be sent. It is a control-flow signal, not
// a failure: the caller either continues the work in a new session carrying
// plan.Message or aborts the turn with a clear error.
var ErrContextRescuePlanned = errors.New("context rescue plan ready; the current view must not be sent")

// ContextRescueRequired is the error Prepare returns in place of a sendable
// view. It carries the certified plan itself because the ordinary call shape —
// `prepared, err := Prepare(...); if err != nil { return err }` — discards the
// PreparedContext, which would otherwise make the plan unreachable. Callers use
// ContextRescuePlanFromError, or match ErrContextRescuePlanned.
type ContextRescueRequired struct {
	Plan ContextRecoveryPlan
}

func (e *ContextRescueRequired) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("context rescue plan ready for a %d-token view (compaction removed %.2f%%, under the %.0f%% needed); the current view must not be sent",
		e.Plan.CompactedTokens, e.Plan.ReductionRatio*100, contextRescueReductionRatio*100)
}

// Is keeps errors.Is(err, ErrContextRescuePlanned) true for this error, so a
// caller can test the sentinel without knowing the carrier type.
func (e *ContextRescueRequired) Is(target error) bool { return target == ErrContextRescuePlanned }

// ContextRescuePlanFromError returns the certified plan a context-rescue error
// carries. It reports false for any other error, including a classified rescue
// failure and cancellation.
func ContextRescuePlanFromError(err error) (ContextRecoveryPlan, bool) {
	var required *ContextRescueRequired
	if !errors.As(err, &required) || required == nil {
		return ContextRecoveryPlan{}, false
	}
	return required.Plan, true
}

// errContextRescueNotEligible is internal control flow: the view either fits
// under the hard ceiling after compaction or is not measurable, so the existing
// overflow ladder owns the decision and no rescue was attempted.
var errContextRescueNotEligible = errors.New("context rescue is not eligible for this view")

// ContextRescueError is a classified rescue failure. Every path that cannot
// certify a plan returns one, so a caller never has to interpret a bare error
// to know whether a session may still be rotated.
type ContextRescueError struct {
	Code string
	Err  error
}

func (e *ContextRescueError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return "context rescue: " + e.Code
	}
	return fmt.Sprintf("context rescue: %s: %v", e.Code, e.Err)
}

func (e *ContextRescueError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func newContextRescueError(code string, err error) error {
	return &ContextRescueError{Code: code, Err: err}
}

// ContextRescueCode returns the classification of a rescue failure, or "" for
// a non-rescue error (including cancellation the caller must propagate).
func ContextRescueCode(err error) string {
	var rescueErr *ContextRescueError
	if errors.As(err, &rescueErr) {
		return rescueErr.Code
	}
	return ""
}

// ContextRecoveryPlan is a validated continuation payload for a session whose
// context window is exhausted. It is a proposal only: producing a plan never
// mutates the projection, the transcript or the session, and a plan that could
// not be certified is never returned.
type ContextRecoveryPlan struct {
	// Trigger is the compaction trigger that found the over-ceiling view.
	Trigger string
	// Reason states the measured reduction that made ordinary compaction
	// ineffective. It is the "trigger reason" recorded for diagnostics.
	Reason string
	// SourceTokens and CompactedTokens are the provider-visible request
	// estimates before and after compaction, on the same scale.
	SourceTokens    int
	CompactedTokens int
	// ReductionRatio is (SourceTokens - CompactedTokens) / SourceTokens.
	ReductionRatio float64
	// Summary is the distilled main line, without the message wrapper.
	Summary string
	// SummaryHash fingerprints Summary so a continuation lineage can prove the
	// briefing was carried over unedited.
	SummaryHash string
	// SummaryTokens estimates Summary on its own; BlockTokens measures the
	// whole message that would be injected.
	SummaryTokens int
	BlockTokens   int
	// Message is the host-generated continuation message to persist as the new
	// session's first message. It is a bounded, ordinary user-role message: no
	// system prompt, tool schema or message ordering changes with it.
	Message provider.Message
	// Generation, TranscriptVersion, ProjectionVersion and CoveredCount
	// identify the exact frozen source view the plan was built from.
	Generation        uint64
	TranscriptVersion uint64
	ProjectionVersion uint64
	CoveredCount      int
	// PendingTools counts tool invocations with no recorded result in the
	// frozen view. Their outcome is unknown and must not be replayed.
	PendingTools int
	// Spans is how many summarizer requests produced Summary.
	Spans int
	// DedupKey is the lineage re-entry guard: one continuation per frozen
	// source view, so a retry over the same view cannot rotate twice.
	DedupKey string
}

// IsContextRescueMessage reports whether m is a continuation briefing injected
// by a context rescue. Exported so session owners can recognize an
// already-rescued first message without re-interpreting the tag.
func IsContextRescueMessage(m provider.Message) bool {
	return m.Role == provider.RoleUser && m.Origin == provider.MessageOriginHost &&
		strings.HasPrefix(strings.TrimLeft(m.Content, "\n "), contextRescueTagOpen)
}

// contextRescueEligible reports whether a view that ordinary compaction left at
// or above the hard input ceiling qualifies for a continuation rescue, and the
// measured reduction when it does. Both token counts must come from the same
// provider-visible estimate: comparing a summarizer input against a whole
// request would invent a ratio that measures nothing.
//
// It fails closed on an unknown window or an unusable source estimate: without
// either, "still at or above the ceiling" cannot be established, let alone the
// reduction.
func contextRescueEligible(sourceTokens, compactedTokens, hard int) (float64, bool) {
	if sourceTokens <= 0 || hard <= 0 || compactedTokens < hard {
		return 0, false
	}
	ratio := float64(sourceTokens-compactedTokens) / float64(sourceTokens)
	return ratio, ratio < contextRescueReductionRatio
}

// contextRescueBlockFits reports whether a measured continuation block is
// admissible. The bound is strict: a block of exactly
// contextRescueMaxBlockTokens is rejected.
func contextRescueBlockFits(blockTokens int) bool {
	return blockTokens > 0 && blockTokens < contextRescueMaxBlockTokens
}

// rescueOverCeiling is the single admission-boundary decision for a view that
// ordinary compaction left at or above the hard input ceiling. With the
// continuation rescue enabled it first tries to certify a recovery plan; when
// the plan is not what this transaction asked for, the existing truncation
// ladder keeps ownership of the decision.
func (m ContextManager) rescueOverCeiling(ctx context.Context, policy ContextPreparePolicy, sourceTokens, compactedTokens, hard int, cause error) (PreparedContext, error) {
	if !policy.AllowContextRescue {
		return m.rescueByTruncation(ctx, policy, hard, cause)
	}
	plan, err := m.planContextRescue(ctx, policy, sourceTokens, compactedTokens, hard)
	switch {
	case err == nil:
		// The frozen view rides along for callers that keep the PreparedContext,
		// but the plan itself travels in the error: the common call shape
		// discards this struct the moment err is non-nil.
		prepared := m.currentPrepared()
		prepared.Recovery = &plan
		m.agent.noteMaintenanceDecision(maintenanceStateRescued)
		return prepared, &ContextRescueRequired{Plan: plan}
	case errors.Is(err, errContextRescueNotEligible):
		return m.rescueByTruncation(ctx, policy, hard, cause)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return PreparedContext{}, err
	default:
		// A continuation was requested and could not be certified: report the
		// classified failure rather than silently trading the main line for a
		// lossy view the caller never agreed to.
		return PreparedContext{}, err
	}
}

// planContextRescue builds the validated continuation payload for a view that
// ordinary compaction left at or above the hard input ceiling.
func (m ContextManager) planContextRescue(ctx context.Context, policy ContextPreparePolicy, sourceTokens, compactedTokens, hard int) (ContextRecoveryPlan, error) {
	a := m.agent
	if a == nil || a.sess.conversation == nil {
		return ContextRecoveryPlan{}, newContextRescueError(ContextRescueNoWindowEstimate, errors.New("context rescue needs an active session"))
	}
	if err := ctx.Err(); err != nil {
		return ContextRecoveryPlan{}, err
	}
	ratio, eligible := contextRescueEligible(sourceTokens, compactedTokens, hard)
	tele := ContextRescueTelemetry{
		Trigger: policy.Trigger, SourceTokens: sourceTokens,
		CompactedTokens: compactedTokens, ReductionRatio: ratio,
	}
	if !eligible {
		tele.Outcome, tele.Code = contextRescueOutcomeSkipped, ContextRescueNotEligible
		a.emitContextRescueTelemetry(tele)
		return ContextRecoveryPlan{}, errContextRescueNotEligible
	}
	reason := fmt.Sprintf("ordinary compaction removed %.2f%% of the request (%d -> %d tokens) and the view is still at or above the hard ceiling %d",
		ratio*100, sourceTokens, compactedTokens, hard)

	// Freeze the view the plan describes. Everything after this point is built
	// from snap, and the plan is only returned while snap is still current.
	snap := a.snapshotExplicitCompression()
	tele.Generation = snap.generation
	region := rescueRegion(snap.visible)
	if len(region) == 0 {
		return ContextRecoveryPlan{}, a.rescueFailed(tele, ContextRescueEmptyRegion, errors.New("no main-line messages remain to continue"))
	}

	summary, spans, err := a.rescueSummary(ctx, region, policy.Instructions)
	if err != nil {
		tele.Spans = spans
		return ContextRecoveryPlan{}, a.rescueError(tele, err)
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ContextRecoveryPlan{}, a.rescueFailed(tele, ContextRescueSummaryEmpty, errors.New("continuation briefing is empty"))
	}
	tele.Spans = spans

	pending := pendingToolCalls(region)
	meta := rescueMessageMeta{
		Trigger: policy.Trigger, SourceTokens: sourceTokens, CompactedTokens: compactedTokens,
		ReductionRatio: ratio, SummaryHash: summaryContentHash(summary),
		Generation: snap.generation, TranscriptVersion: snap.transcriptVersion, PendingTools: pending,
	}
	block, err := a.measureRescueBlock(meta, summary)
	if err != nil {
		return ContextRecoveryPlan{}, a.rescueError(tele, err)
	}
	if !contextRescueBlockFits(block.blockTokens) {
		// Exactly one bounded re-compression, then re-verify. A briefing that is
		// still over the bound is reported, never trimmed into something that
		// would silently drop standing constraints.
		block, err = a.squeezeRescueBlock(ctx, meta, block)
		if err != nil {
			return ContextRecoveryPlan{}, a.rescueError(tele, err)
		}
		if !contextRescueBlockFits(block.blockTokens) {
			return ContextRecoveryPlan{}, a.rescueFailed(tele, ContextRescueOverBudget,
				fmt.Errorf("continuation block is %d tokens, at or above the %d-token bound", block.blockTokens, contextRescueMaxBlockTokens))
		}
	}
	tele.SummaryTokens, tele.BlockTokens = block.summaryTokens, block.blockTokens

	// The briefing was summarized from a frozen view. If the transcript,
	// projection or lineage moved while it ran, the digest no longer describes
	// what the caller would rotate away from.
	if !a.explicitCompressionSnapshotCurrent(snap) {
		return ContextRecoveryPlan{}, a.rescueFailed(tele, ContextRescueStaleTranscript,
			errors.New("conversation changed while the continuation briefing was generated"))
	}

	dedup := rescueDedupKey(policy.Trigger, snap.generation, snap.transcriptVersion, snap.coveredHash)
	tele.Outcome, tele.DedupKey = contextRescueOutcomePlanned, dedup
	a.emitContextRescueTelemetry(tele)
	return ContextRecoveryPlan{
		Trigger: policy.Trigger, Reason: reason,
		SourceTokens: sourceTokens, CompactedTokens: compactedTokens, ReductionRatio: ratio,
		Summary: block.summary, SummaryHash: block.summaryHash,
		SummaryTokens: block.summaryTokens, BlockTokens: block.blockTokens, Message: block.message,
		Generation: snap.generation, TranscriptVersion: snap.transcriptVersion,
		ProjectionVersion: snap.projectionVersion, CoveredCount: len(snap.canonical),
		PendingTools: len(pending), Spans: spans, DedupKey: dedup,
	}, nil
}

// rescueRegion is the frozen main line the continuation briefing must carry:
// the model-visible conversation without the parts a new session rebuilds for
// itself — leading system messages, host turn-context snapshots, and pinned
// context revisions. Existing compaction summaries stay in: the briefing
// instruction merges their facts rather than dropping them.
func rescueRegion(visible []provider.Message) []provider.Message {
	head := 0
	for head < len(visible) && visible[head].Role == provider.RoleSystem {
		head++
	}
	out := make([]provider.Message, 0, len(visible)-head)
	for _, msg := range visible[head:] {
		if msg.LocalOnly || isSessionContextMessage(msg) || IsPinnedContextRevision(msg) {
			continue
		}
		out = append(out, msg)
	}
	return out
}

// pendingToolCalls lists tool invocations in region that carry no recorded
// result. Their outcome is unknown: the continuation briefing records that
// explicitly so a later session neither assumes an outcome nor replays a call
// whose side effect may already have landed. A call with no id cannot be
// matched to a result at all and is left to the provider's own pairing repair.
func pendingToolCalls(region []provider.Message) []provider.ToolCall {
	results := make(map[string]struct{}, len(region))
	for _, msg := range region {
		if msg.Role == provider.RoleTool && msg.ToolCallID != "" {
			results[msg.ToolCallID] = struct{}{}
		}
	}
	var pending []provider.ToolCall
	for _, msg := range region {
		for _, call := range msg.ToolCalls {
			if call.ID == "" {
				continue
			}
			if _, ok := results[call.ID]; !ok {
				pending = append(pending, call)
			}
		}
	}
	return pending
}

// contextRescueInstruction is the continuation briefing contract: the same
// structured headings as an in-session fold, plus the two rules that only
// matter when the digest replaces the transcript entirely.
const contextRescueInstruction = compactionInstruction + `

This briefing is a cross-session continuation payload, not an in-session fold. The next session starts with no other history, so the briefing alone must let the work continue: keep every standing constraint, decision, file, command result and concrete next step.
Record any tool call that has no recorded result as unfinished and its outcome UNKNOWN. Never state, infer or assume such an outcome, and never instruct a replay of it.`

// contextRescueSqueezeInstruction is the single bounded rewrite allowed when
// the first briefing does not fit.
const contextRescueSqueezeInstruction = `The following is one session briefing that is too long to serve as a cross-session continuation payload. Rewrite it under the same exact headings, keeping every standing constraint, identifier, path, number, decision and next step, but compress the wording hard. Drop nothing that still governs the work. Output only the structured Markdown briefing. Do not call tools. Do not output reasoning.`

func contextRescueInstructionWithFocus(instructions string) string {
	instruction := contextRescueInstruction
	if strings.TrimSpace(instructions) != "" {
		instruction += "\n\nAdditional focus for this continuation (prioritize keeping this):\n" + strings.TrimSpace(instructions)
	}
	return instruction
}

// rescueSummary turns the frozen main line into one continuation briefing. A
// briefing one request cannot produce (output truncation, admission limit,
// provider overflow) falls back to the replay-safe fragment and tree-reduce
// path already used for over-length sessions, which keeps tool-call/result
// units whole and never leaves a partially summarized boundary.
func (a *Agent) rescueSummary(ctx context.Context, region []provider.Message, instructions string) (string, int, error) {
	instruction := contextRescueInstructionWithFocus(instructions)
	summary, err := a.summarizeBounded(ctx, region, instruction, contextRescueTargetTokens)
	if err == nil {
		return summary, 1, nil
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	if !summarySizeFailure(err) {
		return "", 0, rescueSummaryFailure(err)
	}
	res, chunkedErr := a.chunkedFoldSummary(ctx, region, instruction, nil)
	spans := max(1, res.Spans)
	if chunkedErr != nil {
		return "", spans, rescueSummaryFailure(chunkedErr)
	}
	return res.Text, spans, nil
}

// summarizeBounded is the ordinary summarizer with an explicit output ceiling,
// so a digest that must fit inside a hard block bound cannot expand to the full
// summary output budget.
func (a *Agent) summarizeBounded(ctx context.Context, region []provider.Message, instructions string, maxTokens int) (string, error) {
	req := a.summaryRequest(region, instructions)
	if maxTokens > 0 && req.MaxTokens > maxTokens {
		req.MaxTokens = maxTokens
	}
	summary, usage, err := a.runSummaryRequest(ctx, req)
	a.observeSummaryOutcome(req, usage, err)
	return summary, err
}

func rescueSummaryFailure(err error) error {
	return newContextRescueError(rescueSummaryFailureCode(err), err)
}

func rescueSummaryFailureCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errSummaryOutputTruncated):
		return ContextRescueSummaryTruncated
	default:
		return ContextRescueSummaryFailed
	}
}

// rescueBlock is one assembled continuation message together with the briefing
// it carries and the measurements that decided its fate.
type rescueBlock struct {
	summary       string
	summaryHash   string
	message       provider.Message
	blockTokens   int
	summaryTokens int
}

// measureRescueBlock assembles the continuation message and measures it with
// the calibrated provider-visible estimator. Measurement fails closed: the
// character-count fallback is not CJK-aware, so an uncalibrated block could be
// far larger than 10K real tokens, and certifying it would defeat the only
// bound that matters.
func (a *Agent) measureRescueBlock(meta rescueMessageMeta, summary string) (rescueBlock, error) {
	summaryTokens, ok := a.estimateBlockTokens(HostGeneratedUserMessage(summary))
	if !ok {
		return rescueBlock{}, newContextRescueError(ContextRescueUntrustedEstimate,
			errors.New("no provider usage has calibrated the token scale for the continuation briefing"))
	}
	meta.SummaryTokens = summaryTokens
	message := formatContextRescueMessage(summary, meta)
	blockTokens, ok := a.estimateBlockTokens(message)
	if !ok {
		return rescueBlock{}, newContextRescueError(ContextRescueUntrustedEstimate,
			errors.New("no provider usage has calibrated the token scale for the continuation block"))
	}
	return rescueBlock{
		summary: summary, summaryHash: summaryContentHash(summary),
		message: message, blockTokens: blockTokens, summaryTokens: summaryTokens,
	}, nil
}

// squeezeRescueBlock performs the one bounded re-compression allowed for an
// over-budget block and re-measures the result. The returned block always
// describes the briefing it actually carries, so the plan can never report a
// digest it is not sending.
func (a *Agent) squeezeRescueBlock(ctx context.Context, meta rescueMessageMeta, block rescueBlock) (rescueBlock, error) {
	budget := contextRescueSqueezeBudget(block.blockTokens)
	rewritten, err := a.summarizeBounded(ctx, mergeDigestMessages([]string{block.summary}), contextRescueSqueezeInstruction, budget)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return rescueBlock{}, ctxErr
		}
		return rescueBlock{}, rescueSummaryFailure(err)
	}
	rewritten = strings.TrimSpace(rewritten)
	if rewritten == "" {
		return rescueBlock{}, newContextRescueError(ContextRescueSummaryEmpty,
			errors.New("continuation rewrite is empty"))
	}
	return a.measureRescueBlock(meta, rewritten)
}

// contextRescueSqueezeBudget sizes the rewrite so halving the briefing fits it
// under the bound with the wrapper overhead already deducted.
func contextRescueSqueezeBudget(blockTokens int) int {
	body := blockTokens - contextRescueWrapperReserveTokens
	if body < contextRescueMinSqueezeTokens {
		body = contextRescueMinSqueezeTokens
	}
	return max(contextRescueMinSqueezeTokens, min(contextRescueTargetTokens, body/2))
}

// estimateBlockTokens measures a continuation message with the calibrated
// provider-visible estimator, reporting false when no real usage has
// calibrated that scale.
func (a *Agent) estimateBlockTokens(msg provider.Message) (int, bool) {
	shape := a.requestCalibrationShape(provider.Request{Messages: []provider.Message{msg}})
	return a.calibratedPromptTokens(shape)
}

// rescueMessageMeta is the non-briefing content of a continuation message: the
// provenance a caller and a later session need, and the unfinished calls that
// must be marked unknown.
type rescueMessageMeta struct {
	Trigger           string
	SourceTokens      int
	CompactedTokens   int
	ReductionRatio    float64
	SummaryTokens     int
	SummaryHash       string
	Generation        uint64
	TranscriptVersion uint64
	PendingTools      []provider.ToolCall
}

// formatContextRescueMessage wraps a briefing as the first message of the
// continuation session. It stays an ordinary host-generated user message: the
// system prompt, tool schemas and message order all come from the new session's
// own first request.
func formatContextRescueMessage(summary string, meta rescueMessageMeta) provider.Message {
	var b strings.Builder
	b.WriteString(contextRescueTagOpen)
	b.WriteString("\nA previous session exhausted its context window and ordinary compaction could not reclaim enough of it. Its main line was summarized into the briefing below and the work continues here. Treat the briefing as established history and pick up from its pending work; do not re-establish what it already records.\n\n")
	fmt.Fprintf(&b, "trigger=%s source_tokens=%d compacted_tokens=%d reduction_ratio=%.4f summary_tokens=%d summary_sha=%s generation=%d transcript_version=%d\n\n",
		meta.Trigger, meta.SourceTokens, meta.CompactedTokens, meta.ReductionRatio,
		meta.SummaryTokens, meta.SummaryHash, meta.Generation, meta.TranscriptVersion)
	b.WriteString(summary)
	if len(meta.PendingTools) > 0 {
		b.WriteString("\n\n## Pending tool calls (do not replay)\nThe following invocations have no recorded result. Their outcome is unknown: re-verify before relying on them, and do not replay them, because the previous session may already have applied their effect.\n")
		for _, call := range meta.PendingTools {
			fmt.Fprintf(&b, "- %s (id %s): outcome unknown\n", call.Name, call.ID)
		}
	}
	b.WriteString("\n")
	b.WriteString(contextRescueTagClose)
	return HostGeneratedUserMessage(b.String())
}

// rescueDedupKey is the lineage re-entry guard: one continuation per frozen
// source view. It keys on the view's identity, not on the briefing text, so a
// retry that observes the same transcript, generation and covered prefix cannot
// rotate the session a second time.
func rescueDedupKey(trigger string, generation, transcriptVersion uint64, coveredHash string) string {
	if coveredHash == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%s", trigger, generation, transcriptVersion, coveredHash)))
	return hex.EncodeToString(sum[:8])
}

// ContextRescueTelemetry is the independent observability record for one
// continuation-rescue attempt. Transcript content is deliberately omitted;
// hashes and counts are what the rescue's own cost and outcome are read from,
// separately from the session's aggregate cache and context accounting.
type ContextRescueTelemetry struct {
	Trigger         string  `json:"trigger"`
	Outcome         string  `json:"outcome"` // planned|skipped|failed
	Code            string  `json:"code,omitempty"`
	SourceTokens    int     `json:"source_tokens"`
	CompactedTokens int     `json:"compacted_tokens"`
	ReductionRatio  float64 `json:"reduction_ratio"`
	SummaryTokens   int     `json:"summary_tokens"`
	BlockTokens     int     `json:"block_tokens"`
	Spans           int     `json:"spans"`
	Generation      uint64  `json:"generation"`
	DedupKey        string  `json:"dedup_key,omitempty"`
}

func (a *Agent) rescueError(tele ContextRescueTelemetry, err error) error {
	var rescueErr *ContextRescueError
	if errors.As(err, &rescueErr) {
		return a.rescueFailed(tele, rescueErr.Code, rescueErr.Err)
	}
	return a.rescueFailed(tele, ContextRescueSummaryFailed, err)
}

func (a *Agent) rescueFailed(tele ContextRescueTelemetry, code string, err error) error {
	tele.Outcome, tele.Code = contextRescueOutcomeFailed, code
	a.emitContextRescueTelemetry(tele)
	return newContextRescueError(code, err)
}

func (a *Agent) emitContextRescueTelemetry(t ContextRescueTelemetry) {
	if a == nil {
		return
	}
	detail := fmt.Sprintf("trigger=%s outcome=%s src=%d compacted=%d reduction=%.4f summary=%d block=%d spans=%d generation=%d",
		t.Trigger, t.Outcome, t.SourceTokens, t.CompactedTokens, t.ReductionRatio,
		t.SummaryTokens, t.BlockTokens, t.Spans, t.Generation)
	if t.Code != "" {
		detail += " code=" + t.Code
	}
	if t.DedupKey != "" {
		detail += " dedup=" + t.DedupKey
	}
	if t.Outcome == contextRescueOutcomeFailed {
		slog.Warn("agent: context rescue failed", "detail", detail)
	}
	a.svc.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: "context rescue telemetry", Detail: detail})
}
