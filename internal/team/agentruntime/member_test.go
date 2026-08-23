package agentruntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// fakeProvider is a scripted provider: it captures the request (for the role
// injection assertion), replays chunk text, and can fail, hang, or wait for
// cancellation.
type fakeProvider struct {
	mu     sync.Mutex
	reqs   []provider.Request
	chunks []provider.Chunk
	err    error
	hang   bool // never close the stream until ctx cancels
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan provider.Chunk)
	go func() {
		defer close(ch)
		for _, c := range f.chunks {
			select {
			case <-ctx.Done():
				return
			case ch <- c:
			}
		}
		if f.hang {
			<-ctx.Done()
		}
	}()
	return ch, nil
}

func (f *fakeProvider) lastReq() provider.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

func newTestMember(t *testing.T, prov provider.Provider) (*team.TeamSessionStore, *MemberAgent) {
	t.Helper()
	store, err := team.NewTeamSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := testKey()
	return store, NewMemberAgent(key, sharedSpec(key, "资深后端"), store, prov, 0)
}

// TestMemberAgentStreamsPersistsAndAdvancesCursor is the happy path of route
// §11.3: deltas stream, the complete assistant message lands in the member's
// history atomically, cursor and event sequence advance together.
func TestMemberAgentStreamsPersistsAndAdvancesCursor(t *testing.T) {
	prov := &fakeProvider{chunks: []provider.Chunk{{Type: provider.ChunkText, Text: "你"}, {Type: provider.ChunkText, Text: "好"}}}
	store, m := newTestMember(t, prov)
	if err := store.AppendMessage("t", "m", team.SessionMessage{Kind: "user", From: "cli", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	sub := m.Subscribe()
	if err := m.Send("hello"); err != nil {
		t.Fatal(err)
	}
	evs := drainEvents(t, sub, EventStarted, EventDelta, EventDelta, EventMessage, EventDone)

	if evs[1].Text != "你" || evs[2].Text != "好" {
		t.Fatalf("deltas = %q, %q", evs[1].Text, evs[2].Text)
	}
	if evs[3].Kind != EventMessage || evs[3].Text != "你好" {
		t.Fatalf("message event = %+v, want 你好", evs[3])
	}
	if evs[4].Kind != EventDone {
		t.Fatalf("final event = %v, want done", evs[4].Kind)
	}
	if evs[0].Sequence != 1 || evs[4].Sequence != 5 {
		t.Fatalf("sequences %d..%d, want 1..5", evs[0].Sequence, evs[4].Sequence)
	}

	msgs, err := store.Messages("t", "m")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[1].Kind != "agent" || msgs[1].Text != "你好" {
		t.Fatalf("history = %+v, want user + assistant 你好", msgs)
	}
	c, err := store.ReadCursor("t", "m")
	if err != nil {
		t.Fatal(err)
	}
	if c.Cursor != 2 {
		t.Fatalf("cursor = %+v, want Cursor 2", c)
	}
	// The final sequence lands in the loop's defer; poll for it.
	deadline := time.After(2 * time.Second)
	for c.Sequence != 5 {
		select {
		case <-deadline:
			t.Fatalf("final sequence never persisted: %+v", c)
		case <-time.After(5 * time.Millisecond):
		}
		c, _ = store.ReadCursor("t", "m")
	}
	// The finish defer releases the in-flight slot asynchronously after the
	// done event; poll for the stopped snapshot.
	stopDeadline := time.After(2 * time.Second)
	for {
		if st, _ := m.Snapshot(); st == RuntimeStateStopped {
			break
		}
		select {
		case <-stopDeadline:
			t.Fatal("snapshot never stopped after done")
		case <-time.After(5 * time.Millisecond):
		}
	}
	m.Close()
}

// TestMemberAgentInjectsRoleIntoSystemPrompt pins the role-injection contract
// (route §2.2): the request's first message is the assembled system prompt
// carrying team identity and the free-text role, and no key material rides
// the request.
func TestMemberAgentInjectsRoleIntoSystemPrompt(t *testing.T) {
	prov := &fakeProvider{chunks: []provider.Chunk{{Type: provider.ChunkText, Text: "ok"}}}
	_, m := newTestMember(t, prov)
	sub := m.Subscribe()
	if err := m.Send("hi"); err != nil {
		t.Fatal(err)
	}
	drainEvents(t, sub, EventStarted, EventDelta, EventMessage, EventDone)
	req := prov.lastReq()
	if len(req.Messages) == 0 || req.Messages[0].Role != provider.RoleSystem {
		t.Fatalf("first message = %+v, want system", req.Messages)
	}
	sys := req.Messages[0].Content
	for _, want := range []string{"你是团队 t 的成员 m。", "你的团队角色是：资深后端。"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, sys)
		}
	}
	for _, msg := range req.Messages {
		if strings.Contains(msg.Content, "sk-") || strings.Contains(msg.Content, "api_key") {
			t.Fatalf("request carries key material: %+v", msg)
		}
	}
}

// TestMemberAgentFailureKeepsUserMessageAndEmitsError pins §11.3.3: a
// provider failure keeps the persisted user message (retryable history),
// does not append an assistant message, and emits an error event; the event
// sequence still persists so a restart never renumbers.
func TestMemberAgentFailureKeepsUserMessageAndEmitsError(t *testing.T) {
	prov := &fakeProvider{err: errors.New("network down")}
	store, m := newTestMember(t, prov)
	if err := store.AppendMessage("t", "m", team.SessionMessage{Kind: "user", From: "cli", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	sub := m.Subscribe()
	if err := m.Send("hello"); err != nil {
		t.Fatal(err)
	}
	evs := drainEvents(t, sub, EventStarted, EventError)
	if evs[1].Text != "network down" {
		t.Fatalf("error text = %q, want network down", evs[1].Text)
	}
	msgs, _ := store.Messages("t", "m")
	if len(msgs) != 1 || msgs[0].Kind != "user" {
		t.Fatalf("history after failure = %+v, want the user message only", msgs)
	}
	// The event sequence persists asynchronously in the loop's defer; poll
	// until it lands.
	deadline := time.After(2 * time.Second)
	var c team.SessionCursor
	for {
		c, _ = store.ReadCursor("t", "m")
		if c.Sequence == 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("event sequence never persisted: %+v", c)
		case <-time.After(5 * time.Millisecond):
		}
	}
	if c.Cursor != 0 {
		t.Fatalf("cursor after failure = %+v, want Cursor 0 (unconsumed)", c)
	}
	m.Close()
}

// TestMemberAgentErrorEventNeverLeaksAuthBody pins K1: the AuthError body
// (which can echo masked key fragments) never enters the event stream.
func TestMemberAgentErrorEventNeverLeaksAuthBody(t *testing.T) {
	ae := &provider.AuthError{Provider: "deepseek", KeyEnv: "API_KEY", Status: 401, Body: "token expired MASKED-SECRET"}
	if got := userFacingError(ae); strings.Contains(got, "MASKED-SECRET") {
		t.Fatalf("auth body leaked into error text: %q", got)
	}
}

// TestMemberAgentStopCancelsInflightLoop pins Ctrl+C semantics (§11.4): a
// hanging provider stream is cancelled, the loop terminates with a stopped
// event, and a subsequent Send works again.
func TestMemberAgentStopCancelsInflightLoop(t *testing.T) {
	prov := &fakeProvider{hang: true}
	store, m := newTestMember(t, prov)
	store.AppendMessage("t", "m", team.SessionMessage{Kind: "user", From: "cli", Text: "long"})
	sub := m.Subscribe()
	if err := m.Send("long"); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	evs := drainEvents(t, sub, EventStarted, EventStopped)
	if evs[1].Kind != EventStopped {
		t.Fatalf("event = %v, want stopped", evs[1].Kind)
	}
	// The slot is free again; the hang script applies only to the first run.
	prov.hang = false
	prov.chunks = []provider.Chunk{{Type: provider.ChunkText, Text: "fast"}}
	if err := m.Send("again"); err != nil {
		t.Fatalf("Send after Stop: %v", err)
	}
	drainEvents(t, sub, EventStarted, EventDelta, EventMessage, EventDone)
	m.Close()
}

// TestMemberAgentSendSerialPerMember pins same-member serialization (§11.3):
// a second Send while a loop is in flight returns ErrBusy.
func TestMemberAgentSendSerialPerMember(t *testing.T) {
	prov := &fakeProvider{hang: true}
	_, m := newTestMember(t, prov)
	sub := m.Subscribe()
	if err := m.Send("one"); err != nil {
		t.Fatal(err)
	}
	if err := m.Send("two"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Send err = %v, want ErrBusy", err)
	}
	m.Stop()
	m.Close()
	_ = sub
}

// TestMemberAgentsAreIsolated pins the isolation contract: two members with
// the same AgentUserRef never share events, history, or cursors.
func TestMemberAgentsAreIsolated(t *testing.T) {
	store, err := team.NewTeamSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keyA := InstanceKey{Team: "t", MemberID: "a"}
	keyB := InstanceKey{Team: "t", MemberID: "b"}
	mA := NewMemberAgent(keyA, sharedSpec(keyA, "rA"), store, &fakeProvider{chunks: []provider.Chunk{{Type: provider.ChunkText, Text: "from-a"}}}, 0)
	mB := NewMemberAgent(keyB, sharedSpec(keyB, "rB"), store, &fakeProvider{chunks: []provider.Chunk{{Type: provider.ChunkText, Text: "from-b"}}}, 0)

	subA := mA.Subscribe()
	if err := store.AppendMessage("t", "a", team.SessionMessage{Kind: "user", From: "cli", Text: "qA"}); err != nil {
		t.Fatal(err)
	}
	if err := mA.Send("qA"); err != nil {
		t.Fatal(err)
	}
	evs := drainEvents(t, subA, EventStarted, EventDelta, EventMessage, EventDone)
	for _, ev := range evs {
		if ev.MemberID != "a" {
			t.Fatalf("A's stream carries member %q", ev.MemberID)
		}
	}
	msgsB, _ := store.Messages("t", "b")
	if len(msgsB) != 0 {
		t.Fatalf("B saw A's history: %+v", msgsB)
	}
	cB, _ := store.ReadCursor("t", "b")
	if cB.Cursor != 0 || cB.Sequence != 0 {
		t.Fatalf("B cursor moved by A's run: %+v", cB)
	}
	mA.Close()
	mB.Close()
}
