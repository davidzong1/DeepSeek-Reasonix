package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/tool"
)

// continuationFixture is one exclusive (v3) controller bound to a source
// session, plus the service that owns both identities. It mirrors the shape a
// Team member's backend has: one controller, one identity owner.
type continuationFixture struct {
	service  *session.Service
	ctrl     *Controller
	source   session.SessionRef
	rotation *continuationRotationHost
	runner   *recordingRunner
}

// recordingRunner stands in for the model turn so a test can prove the rescue
// actually restarted the main line, rather than only that it rotated.
type recordingRunner struct {
	mu     sync.Mutex
	inputs []string
}

func (r *recordingRunner) Run(_ context.Context, input string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inputs = append(r.inputs, input)
	return nil
}

func (r *recordingRunner) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.inputs...)
}

// continuationRotationHost stands in for the identity-owning host. It reserves
// a fresh identity per rotation and records every request it saw, which is how
// the tests assert what a host is actually told.
type continuationRotationHost struct {
	requests []SessionRotationRequest
	// reserve returns the session id for the next rotation. A nil reserve
	// allocates a fresh id per request, as a real host does: an existing session
	// directory cannot be recreated, so a retry needs a new identity.
	reserve    func(request SessionRotationRequest) string
	failCommit error
}

func (h *continuationRotationHost) hook(_ context.Context, request SessionRotationRequest) (SessionRotationPlan, error) {
	h.requests = append(h.requests, request)
	options := session.CreateOptions{Origin: session.SessionOriginNew}
	if h.reserve != nil {
		options.SessionID = h.reserve(request)
	} else {
		options.SessionID = "member-continuation-" + strconv.Itoa(len(h.requests))
	}
	commit := func(context.Context, session.SessionRef) error { return h.failCommit }
	return SessionRotationPlan{CreateOptions: options, Commit: commit}, nil
}

func newContinuationFixture(t *testing.T) *continuationFixture {
	t.Helper()
	t.Chdir(t.TempDir())
	service, err := session.NewService("local", session.NewFilesystemPersistence(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "member-source", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	host := &continuationRotationHost{}
	runner := &recordingRunner{}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	ctrl := newOwnedTestController(t, Options{
		Executor: exec, Runner: runner, Sink: event.Discard, SessionService: service,
		SessionRuntime: runtime, ExclusiveSession: true, OnSessionRotation: host.hook,
	})
	return &continuationFixture{service: service, ctrl: ctrl, source: runtime.Ref(), rotation: host, runner: runner}
}

// advance appends one committed message so the source has a non-zero
// generation and a transcript a summary can describe.
func (f *continuationFixture) advance(t *testing.T, text string) {
	t.Helper()
	_, runtime, _ := f.ctrl.v3Binding()
	message := provider.Message{ID: agent.NewMessageID(), Role: provider.RoleUser, Content: text}
	payload, err := json.Marshal(map[string]any{"message": message})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().AppendBatch(t.Context(), "advance:"+message.ID,
		[]session.Event{{Kind: "message/complete", Payload: payload}}); err != nil {
		t.Fatal(err)
	}
}

func (f *continuationFixture) plan(t *testing.T, summary string) ContinuationPlan {
	t.Helper()
	return ContinuationPlan{
		Trigger:         "hard_input_ceiling_after_compaction",
		SourceTokens:    180_000,
		CompactedTokens: 175_000,
		ReductionRatio:  0.03,
		Summary:         summary,
		SummaryHash:     continuationDigest(summary),
		SummaryTokens:   continuationTextTokens(summary),
		Generation:      f.ctrl.SessionGeneration(),
		DedupKey:        "rescue-1",
		Lineage:         "lineage-member-source",
		Attempt:         1,
	}
}

func TestContinuationRotationPreservesSourceAndSeedsOneRecoveryMessage(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	host := f.rotation
	host.reserve = func(SessionRotationRequest) string { return "member-continuation" }

	summary := "goal: finish the port\nnext: run the acceptance gate"
	plan := f.plan(t, summary)
	var resumed []ContinuationResume
	plan.Resume = func(_ context.Context, resume ContinuationResume) error {
		resumed = append(resumed, resume)
		return nil
	}

	result, err := f.ctrl.ContinueSessionFromPlan(t.Context(), plan)
	if err != nil {
		t.Fatalf("ContinueSessionFromPlan: %v", err)
	}
	if result.Continuation.SessionID != "member-continuation" {
		t.Fatalf("continuation = %+v", result.Continuation)
	}
	if !result.Resumed || len(resumed) != 1 {
		t.Fatalf("resume calls = %d, result.Resumed = %v", len(resumed), result.Resumed)
	}
	if resumed[0].Source != f.source || resumed[0].Continuation != result.Continuation {
		t.Fatalf("resume handed wrong identities: %+v", resumed[0])
	}

	// The source transcript is the whole point: it must still be readable and
	// still carry everything it had.
	source, err := f.service.Open(t.Context(), f.source)
	if err != nil {
		t.Fatalf("source session is no longer openable: %v", err)
	}
	defer func() { _ = source.Release(context.Background()) }()
	sourceText := source.Runtime().Session().ExecutionSnapshot().Projection
	if !hasMessageContent(sourceText.ModelMessages, "main line work") {
		t.Fatal("source transcript lost its content")
	}
	assertSourceMarkerRecorded(t, f.service, f.source, "lineage-member-source")

	// The continuation opens with exactly one host-generated recovery message.
	continuation, err := f.service.Open(t.Context(), result.Continuation)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = continuation.Release(context.Background()) }()
	// ExecutionSnapshot carries only the provider-visible model view, so the
	// full snapshot is what exposes the canonical transcript.
	projection := continuation.Runtime().Session().Snapshot().Projection
	if len(projection.ModelMessages) != 2 {
		t.Fatalf("continuation model messages = %d, want system + one recovery message", len(projection.ModelMessages))
	}
	// Origin is local transcript identity, so it is asserted on the canonical
	// transcript: ModelMessages is the provider-facing projection and strips it
	// by design.
	if len(projection.Messages) != 2 {
		t.Fatalf("continuation transcript = %d messages, want 2", len(projection.Messages))
	}
	opening := projection.Messages[1]
	if opening.Role != provider.RoleUser || opening.Origin != provider.MessageOriginHost {
		t.Fatalf("recovery message = %+v, want host-generated user message", opening)
	}
	if got := projection.ModelMessages[1]; got.ID != opening.ID || !strings.Contains(got.Content, summary) {
		t.Fatalf("provider-visible recovery message = %+v", got)
	}
	if !strings.Contains(opening.Content, plan.SummaryHash) || !strings.Contains(opening.Content, plan.Trigger) {
		t.Fatal("recovery message does not carry its lineage metadata")
	}
	if strings.Contains(opening.Content, "main line work") {
		t.Fatal("recovery message replayed source transcript instead of summarising it")
	}

	// Lineage is asserted by the controller even though the host did not set it.
	assertParentLink(t, f.service, result.Continuation, f.source.SessionID)

	// The controller is now bound to the continuation and it is writable, which
	// is what "the main line can start sampling again" reduces to at this layer:
	// the next turn has a live, writable execution session to land in.
	if ref, ok := f.ctrl.SessionRef(); !ok || ref != result.Continuation {
		t.Fatalf("controller is not bound to the continuation: %+v %v", ref, ok)
	}
	follow := provider.Message{ID: agent.NewMessageID(), Role: provider.RoleUser, Content: "continue the port"}
	followPayload, err := json.Marshal(map[string]any{"message": follow})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ctrl.appendSessionBatch(t.Context(), f.ctrl.sessionEventStore(), session.Batch{
		OperationID: "follow-up", Events: []session.Event{{Kind: "message/complete", Payload: followPayload}},
	}); err != nil {
		t.Fatalf("continuation session refused the follow-on turn: %v", err)
	}
	after := f.ctrl.sessionEventStore().Snapshot().Projection.ModelMessages
	if len(after) != 3 || !strings.Contains(after[2].Content, "continue the port") {
		t.Fatalf("follow-on message did not land in the continuation: %+v", after)
	}
	// The rotation gate is free again. The resume runs after it is released,
	// and a gate still held would deadlock the resumed turn against its own
	// rotation — which is exactly the failure this ordering exists to prevent.
	if err := f.ctrl.beginRotation(); err != nil {
		t.Fatalf("rotation gate is still held after the rescue: %v", err)
	}
	f.ctrl.endRotation()
}

func TestContinuationRotationRejectsRunningTurnAndReleasesTheAttempt(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	f.rotation.reserve = func(SessionRotationRequest) string { return "member-continuation" }
	plan := f.plan(t, "goal: finish")

	// A body the rotation gate treats as a live turn.
	f.ctrl.mu.Lock()
	f.ctrl.turns.phase = session.RuntimeRunning
	f.ctrl.mu.Unlock()

	if _, err := f.ctrl.ContinueSessionFromPlan(t.Context(), plan); !errors.Is(err, errTurnRunningRotation) {
		t.Fatalf("running turn error = %v, want errTurnRunningRotation", err)
	}
	if !IsSessionRotationBusy(errTurnRunningRotation) {
		t.Fatal("a caller cannot classify the refusal as retryable")
	}
	if ref, _ := f.ctrl.SessionRef(); ref != f.source {
		t.Fatal("a refused rescue moved the session anyway")
	}

	// The refused attempt released its reservation: the same plan retries.
	f.ctrl.mu.Lock()
	f.ctrl.turns.phase = session.RuntimeIdle
	f.ctrl.mu.Unlock()
	result, err := f.ctrl.ContinueSessionFromPlan(t.Context(), plan)
	if err != nil {
		t.Fatalf("retry after the turn converged: %v", err)
	}
	if result.Continuation.SessionID != "member-continuation" {
		t.Fatalf("continuation = %+v", result.Continuation)
	}
}

func TestContinuationRotationReplayDoesNotRotateTwice(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	reserved := 0
	f.rotation.reserve = func(SessionRotationRequest) string {
		reserved++
		return "member-continuation"
	}
	plan := f.plan(t, "goal: finish")

	first, err := f.ctrl.ContinueSessionFromPlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.ctrl.ContinueSessionFromPlan(t.Context(), plan)
	if err != nil {
		t.Fatalf("replay of the same attempt: %v", err)
	}
	if second.Continuation != first.Continuation {
		t.Fatalf("replay rotated again: %+v then %+v", first.Continuation, second.Continuation)
	}
	if second.Telemetry.Outcome != continuationOutcomeReplayed {
		t.Fatalf("replay outcome = %q", second.Telemetry.Outcome)
	}
	if len(f.rotation.requests) != 1 {
		t.Fatalf("host saw %d rotation requests, want 1", len(f.rotation.requests))
	}
}

func TestContinuationRotationRecoversWhenResumeFailed(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	f.rotation.reserve = func(SessionRotationRequest) string { return "member-continuation" }
	plan := f.plan(t, "goal: finish")

	resumeCalls := 0
	plan.Resume = func(context.Context, ContinuationResume) error {
		resumeCalls++
		if resumeCalls == 1 {
			return errors.New("member backend refused the turn")
		}
		return nil
	}
	result, err := f.ctrl.ContinueSessionFromPlan(t.Context(), plan)
	if err == nil {
		t.Fatal("a failed resume must surface")
	}
	if result.Continuation.SessionID == "" {
		t.Fatal("the committed continuation was not reported alongside the resume failure")
	}
	if result.Resumed {
		t.Fatal("result claims a resume that failed")
	}

	// The crash-recovery path: the rescue already ran, so replaying it must
	// resume against the existing continuation instead of rotating again.
	replayed, err := f.ctrl.ContinueSessionFromPlan(t.Context(), plan)
	if err != nil {
		t.Fatalf("replay after resume failure: %v", err)
	}
	if replayed.Continuation != result.Continuation {
		t.Fatalf("replay rotated again: %+v", replayed.Continuation)
	}
	if !replayed.Resumed {
		t.Fatal("replay did not retry the resume")
	}
	if len(f.rotation.requests) != 1 {
		t.Fatalf("host saw %d rotation requests, want 1", len(f.rotation.requests))
	}
}

func TestContinuationReplayReportFailureCarriesTheContinuation(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	f.rotation.reserve = func(SessionRotationRequest) string { return "member-continuation" }
	// A resume that never succeeds: the rotation commits, the follow-on turn
	// does not, so every caller keeps retrying against the same continuation.
	plan := f.plan(t, "goal: finish")
	plan.Resume = func(context.Context, ContinuationResume) error { return errors.New("member backend refused") }

	committed, err := f.ctrl.ContinueSessionFromPlan(t.Context(), plan)
	if err == nil {
		t.Fatal("a failed resume must surface")
	}
	// The replay of an attempt whose resume never succeeded must still name the
	// continuation it already published, or the caller has nothing to retry.
	replayed, err := f.ctrl.ContinueSessionFromPlan(t.Context(), plan)
	if err == nil {
		t.Fatal("a failed replay resume must surface")
	}
	if replayed.Continuation != committed.Continuation || replayed.Continuation.SessionID == "" {
		t.Fatalf("failed replay reported %+v, want the committed continuation", replayed.Continuation)
	}
	if replayed.Resumed {
		t.Fatal("failed replay claims a resume that did not happen")
	}
	if len(f.rotation.requests) != 1 {
		t.Fatalf("host saw %d rotation requests, want 1", len(f.rotation.requests))
	}
}

func TestContinuationRotationRefusesDuplicateAcrossControllerInstances(t *testing.T) {
	f := newContinuationFixture(t)
	f.advance(t, "main line work")
	f.rotation.reserve = func(SessionRotationRequest) string { return "member-continuation" }
	plan := f.plan(t, "goal: finish")
	if _, err := f.ctrl.ContinueSessionFromPlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}

	// A restarted process has the same source session and no in-memory ledger.
	// The durable marker on the source is what has to stop the second rotation,
	// and it has to stop it before the host reserves anything.
	rebuilt := newContinuationFixtureOver(t, f)
	retry := plan
	retry.Resume = nil
	_, err := rebuilt.ctrl.ContinueSessionFromPlan(t.Context(), retry)
	if !errors.Is(err, ErrContinuationDuplicate) {
		t.Fatalf("cross-instance replay error = %v, want ErrContinuationDuplicate", err)
	}
	if len(rebuilt.rotation.requests) != 0 {
		t.Fatalf("host reserved %d identities for a replayed rescue", len(rebuilt.rotation.requests))
	}
	if ref, _ := rebuilt.ctrl.SessionRef(); ref != rebuilt.source {
		t.Fatal("a refused replayed rescue moved the session")
	}
}

// newContinuationFixtureOver builds a second controller over the same source
// identity, the way a restarted process would attach to it.
func newContinuationFixtureOver(t *testing.T, previous *continuationFixture) *continuationFixture {
	t.Helper()
	binding, err := previous.service.Open(t.Context(), previous.source)
	if err != nil {
		t.Fatal(err)
	}
	runtime := binding.Runtime()
	t.Cleanup(func() { _ = binding.Release(context.Background()) })
	host := &continuationRotationHost{}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	ctrl := newOwnedTestController(t, Options{
		Executor: exec, Sink: event.Discard, SessionService: previous.service, SessionRuntime: runtime,
		ExclusiveSession: true, OnSessionRotation: host.hook,
	})
	return &continuationFixture{service: previous.service, ctrl: ctrl, source: previous.source, rotation: host}
}

func hasMessageContent(messages []provider.Message, text string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, text) {
			return true
		}
	}
	return false
}

// sourceMarkerScan asserts the durable half of the rescue record: the source's
// own committed log carries the marker, and the marker never reaches the
// provider-visible projection. The second half matters as much as the first —
// a diagnostic that leaked into ModelMessages would be replayed to the model.
func assertSourceMarkerRecorded(t *testing.T, service *session.Service, ref session.SessionRef, lineage string) {
	t.Helper()
	opened, err := service.Open(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opened.Release(context.Background()) }()
	store := opened.Runtime().Session()
	page, err := store.AcceptedPage(context.Background(), 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, commit := range page.Commits {
		for _, ev := range commit.Events {
			if ev.Kind != "diagnostic" || !bytes.Contains(ev.Payload, []byte("context-continuation-v1")) {
				continue
			}
			var marker struct {
				Type     string `json:"type"`
				Lineage  string `json:"lineage"`
				DedupKey string `json:"dedupKey"`
			}
			if err := json.Unmarshal(ev.Payload, &marker); err != nil {
				t.Fatalf("continuation marker is not readable: %v", err)
			}
			if marker.Type == "context-continuation-v1" && marker.Lineage == lineage {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("source %s carries no durable continuation marker for lineage %s", ref.SessionID, lineage)
	}
	for _, message := range store.ExecutionSnapshot().Projection.ModelMessages {
		if strings.Contains(message.Content, "context-continuation-v1") {
			t.Fatal("the continuation marker leaked into the provider-visible projection")
		}
	}
}

// assertParentLink reads the immutable session header, which is where lineage
// has to live: it is the only record that survives the controller.
func assertParentLink(t *testing.T, service *session.Service, ref session.SessionRef, wantParent string) {
	t.Helper()
	dir, err := service.SessionDir(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "header.json"))
	if err != nil {
		t.Fatal(err)
	}
	var header struct {
		ParentSessionID string `json:"parentSessionId"`
	}
	if err := json.Unmarshal(body, &header); err != nil {
		t.Fatal(err)
	}
	if header.ParentSessionID != wantParent {
		t.Fatalf("continuation parent = %q, want %q", header.ParentSessionID, wantParent)
	}
}
