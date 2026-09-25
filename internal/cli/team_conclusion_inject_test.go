package cli

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/agent/testutil"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/team"
	"reasonix/internal/tool"
)

// fakeConclusionReader scripts one member's board reads and records every
// acknowledgement, so a test can pin both halves of the delta contract without
// a durable board.
type fakeConclusionReader struct {
	mu    sync.Mutex
	pages []conclusionPage
	seen  int
	acks  []int64
}

type conclusionPage struct {
	text      string
	advanceTo int64
	err       error
}

func (r *fakeConclusionReader) Read(context.Context, string) (string, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen >= len(r.pages) {
		return "", 0, nil
	}
	page := r.pages[r.seen]
	r.seen++
	return page.text, page.advanceTo, page.err
}

func (r *fakeConclusionReader) Ack(_ context.Context, _ string, advanceTo int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.acks = append(r.acks, advanceTo)
	return nil
}

func (r *fakeConclusionReader) acknowledged() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.acks...)
}

// TestConclusionInjectEmptyPagesStillAdvanceTheCursor pins the case that has no
// text to show but a page to cross: a member whose own posts were filtered out
// of the page. Returning nothing must not leave the cursor behind, or every
// later step re-reads the same page.
func TestConclusionInjectEmptyPagesStillAdvanceTheCursor(t *testing.T) {
	reader := &fakeConclusionReader{pages: []conclusionPage{
		{text: "", advanceTo: 7},
		{text: "", advanceTo: 9},
	}}
	delta := newMemberConclusionDelta(reader, "m1")
	read, ack := delta.pair()

	for step := range 2 {
		text, err := read(context.Background())
		if err != nil || text != "" {
			t.Fatalf("step %d: read = (%q, %v), want an empty delta and no error", step, text, err)
		}
		ack() // the agent never calls this for an empty delta; prove it is a no-op
	}
	if got := reader.acknowledged(); len(got) != 2 || got[0] != 7 || got[1] != 9 {
		t.Fatalf("each empty page must be acknowledged on its own tail, got %v", got)
	}
}

// TestConclusionInjectTextIsAcknowledgedOnlyAfterDelivery pins the ordering
// rule: the cursor moves only once the text it describes has been appended, and
// a later empty read neither re-delivers the line nor re-acknowledges it.
func TestConclusionInjectTextIsAcknowledgedOnlyAfterDelivery(t *testing.T) {
	const line = "[board delta]\n- m2 topic: summary"
	reader := &fakeConclusionReader{pages: []conclusionPage{
		{text: line, advanceTo: 4},
		{text: "", advanceTo: 0},
	}}
	delta := newMemberConclusionDelta(reader, "m1")
	read, ack := delta.pair()

	text, err := read(context.Background())
	if err != nil || text != line {
		t.Fatalf("first read = (%q, %v), want the line", text, err)
	}
	if got := reader.acknowledged(); len(got) != 0 {
		t.Fatalf("the read must not acknowledge before the append: %v", got)
	}
	ack()
	if got := reader.acknowledged(); len(got) != 1 || got[0] != 4 {
		t.Fatalf("the append must acknowledge the page tail once, got %v", got)
	}
	ack()
	if got := reader.acknowledged(); len(got) != 1 {
		t.Fatalf("a repeated ack must not move the cursor again, got %v", got)
	}
	if text, err := read(context.Background()); err != nil || text != "" {
		t.Fatalf("second read = (%q, %v), want an empty delta", text, err)
	}
}

// TestConclusionInjectReadFailureIsSwallowed pins the fail-open rule: a board
// error reaches neither the prompt nor the turn, and nothing is acknowledged —
// the next step reads the same page instead of losing it.
func TestConclusionInjectReadFailureIsSwallowed(t *testing.T) {
	reader := &fakeConclusionReader{pages: []conclusionPage{
		{err: errors.New("board is down")},
	}}
	read, ack := newMemberConclusionDelta(reader, "m1").pair()
	text, err := read(context.Background())
	if err != nil {
		t.Fatalf("a read failure must be swallowed, got %v", err)
	}
	if text != "" {
		t.Fatalf("a failed read must yield no text, got %q", text)
	}
	ack()
	if got := reader.acknowledged(); len(got) != 0 {
		t.Fatalf("a failed read must not be acknowledged, got %v", got)
	}
}

// TestConclusionInjectNilReaderWritesNothing pins the off switch: no reader
// means the hook returns an empty delta and the agent appends nothing.
func TestConclusionInjectNilReaderWritesNothing(t *testing.T) {
	var reader *fakeConclusionReader
	delta := newMemberConclusionDelta(reader, "m1")
	read, ack := delta.pair()
	if text, err := read(context.Background()); err != nil || text != "" {
		t.Fatalf("a nil reader = (%q, %v), want an empty delta", text, err)
	}
	ack()
}

// conclusionInjectRun drives one two-step member turn with the installed hook
// and returns the requests the loop sent.
func conclusionInjectRun(t *testing.T, install func(*agent.Agent)) []provider.Request {
	t.Helper()
	mp := testutil.NewMock("m",
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "probe", Arguments: `{}`}}},
		testutil.Turn{Text: "done"},
	)
	reg := tool.NewRegistry()
	reg.Add(okToolForConclusionTest{})
	a := agent.New(mp, reg, agent.NewSession(""), agent.Options{}, event.Discard)
	install(a)
	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return mp.Requests()
}

type okToolForConclusionTest struct{}

func (okToolForConclusionTest) Name() string            { return "probe" }
func (okToolForConclusionTest) Description() string     { return "probe" }
func (okToolForConclusionTest) ReadOnly() bool          { return true }
func (okToolForConclusionTest) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (okToolForConclusionTest) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

// TestConclusionInjectMemberHookRidesTheNextStep pins the wiring end to end: a
// member's executor carries the delta, and the text lands as the last message of
// the step it was read for.
func TestConclusionInjectMemberHookRidesTheNextStep(t *testing.T) {
	const line = "[board delta]\n- m2 topic: summary"
	reader := &fakeConclusionReader{pages: []conclusionPage{
		{text: line, advanceTo: 3},
		{text: "", advanceTo: 0},
	}}
	reqs := conclusionInjectRun(t, func(a *agent.Agent) {
		if !installConclusionDelta(a, reader, "m1") {
			t.Fatal("a member with a reader must install the hook")
		}
		if !a.BoardDeltaInstalled() {
			t.Fatal("the hook must be observable after installation")
		}
	})
	if len(reqs) != 2 {
		t.Fatalf("the probe should sample twice, got %d requests", len(reqs))
	}
	first := reqs[0].Messages
	if n := len(first); n == 0 || first[n-1].Content != line {
		t.Fatalf("the delta must be the last message of the first request, got %+v", first)
	}
	if got := reader.acknowledged(); len(got) != 1 || got[0] != 3 {
		t.Fatalf("the delivered line must be acknowledged once, got %v", got)
	}
}

// memberExecutorOf reaches the Agent behind an assembled member backend. The
// builder hands back the driving port, which deliberately does not carry the
// executor; the hook is only observable on the concrete leased backend.
func memberExecutorOf(t *testing.T, backend control.SessionAPI) *agent.Agent {
	t.Helper()
	leased, ok := backend.(memberLeasedBackend)
	if !ok {
		t.Fatalf("an assembled member backend must be a leased backend, got %T", backend)
	}
	ctrl, ok := leased.SessionAPI.(*control.Controller)
	if !ok {
		t.Fatalf("the leased backend wraps %T, want *control.Controller", leased.SessionAPI)
	}
	return ctrl.Executor()
}

// TestConclusionInjectBuilderHooksMembersOnly pins the split at the one place a
// member backend is assembled: a member's executor carries the delta and a
// leader's does not, with the same deps. Asserting it through the builder is
// what makes the boundary a property of the call graph rather than of a
// convention a later refactor could drop.
func TestConclusionInjectBuilderHooksMembersOnly(t *testing.T) {
	reader := &fakeConclusionReader{}
	for _, tc := range []struct {
		name   string
		leader bool
		want   bool
	}{
		{name: "alice", leader: false, want: true},
		{name: "lead", leader: true, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("REASONIX_STATE_HOME", t.TempDir())
			t.Setenv("REASONIX_HOME", "")
			deps := memberBuildDeps(t, t.TempDir())
			deps.conclusions = reader
			backend, err := newMemberBackendBuilder(deps)(team.MemberBinding{
				Team: "alpha", MemberID: tc.name, Leader: tc.leader, AgentUserRef: "member-user",
				SessionFile: filepath.Join("alpha", tc.name+".jsonl"),
			})
			if err != nil {
				t.Fatalf("assembling the member %q: %v", tc.name, err)
			}
			t.Cleanup(backend.Close)
			if got := memberExecutorOf(t, backend).BoardDeltaInstalled(); got != tc.want {
				t.Fatalf("member %q board delta installed = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestConclusionInjectBuilderWithoutReaderKeepsEveryHookOff pins the no-board
// build: with no reader wired, even a member's executor stays hook-free, so a
// host that never opened a board sends exactly the requests it always did.
func TestConclusionInjectBuilderWithoutReaderKeepsEveryHookOff(t *testing.T) {
	t.Setenv("REASONIX_STATE_HOME", t.TempDir())
	t.Setenv("REASONIX_HOME", "")
	deps := memberBuildDeps(t, t.TempDir())
	backend, err := newMemberBackendBuilder(deps)(team.MemberBinding{
		Team: "alpha", MemberID: "alice", AgentUserRef: "member-user",
		SessionFile: filepath.Join("alpha", "alice.jsonl"),
	})
	if err != nil {
		t.Fatalf("assembling the member: %v", err)
	}
	t.Cleanup(backend.Close)
	if memberExecutorOf(t, backend).BoardDeltaInstalled() {
		t.Fatal("a build with no reader must not install a board delta")
	}
}
func TestConclusionInjectLeaderHookStaysNil(t *testing.T) {
	reader := &fakeConclusionReader{pages: []conclusionPage{{text: "[board delta]\n- m2 topic: summary", advanceTo: 3}}}
	reqs := conclusionInjectRun(t, func(a *agent.Agent) {
		if installConclusionDelta(a, reader, "") {
			t.Fatal("an empty member id must not install a hook")
		}
		if a.BoardDeltaInstalled() {
			t.Fatal("a leader's executor must keep a nil board delta")
		}
	})
	for _, req := range reqs {
		for _, m := range req.Messages {
			if strings.Contains(m.Content, "[board delta]") {
				t.Fatalf("a leader's request carried a conclusion delta: %q", m.Content)
			}
		}
	}
	if got := reader.acknowledged(); len(got) != 0 {
		t.Fatalf("a leader must not advance a member cursor, got %v", got)
	}
	if installConclusionDelta(nil, reader, "m1") {
		t.Fatal("a nil executor must not report an install")
	}
	if installConclusionDelta(agent.New(nil, nil, agent.NewSession(""), agent.Options{}, event.Discard), nil, "m1") {
		t.Fatal("a nil reader must not report an install")
	}
}
