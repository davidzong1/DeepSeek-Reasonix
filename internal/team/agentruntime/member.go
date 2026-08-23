package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// ErrBusy reports a Send while the member's completion is still in flight:
// one member runs one loop at a time (route §11.3, same-member serial).
var ErrBusy = errors.New("agentruntime: member completion already in flight")

// ProviderFactory assembles the provider for one member instance from its
// config snapshot. The snapshot carries no key material (K1); the host layer
// that wires the factory resolves credentials (member AgentUserRef → team
// default → explicit error) and never falls back to the ambient session.
type ProviderFactory func(spec Spec) (provider.Provider, error)

// MemberRuntime is the per-instance execution boundary (route §11.3): Send
// submits a user message to the member's single-turn streaming completion,
// events flow through Subscribe, Stop cancels the in-flight loop. The
// instance is created already assembled — the loop starts on the first Send,
// matching lazy assembly on member switch (route §11.4).
type MemberRuntime interface {
	// Send runs one completion against the member's history, whose last user
	// message the caller already persisted (Registry.Send). Failures keep the
	// user message and emit an error event; returns ErrBusy while in flight.
	Send(prompt string) error
	// Stop cancels the in-flight loop and waits for it to finish.
	Stop() error
	// Subscribe returns the member's bounded runtime event stream; the
	// subscription's Cancel unsubscribes it.
	Subscribe() Subscription
	// Close stops the loop and closes the event source for every subscriber.
	Close()
	// Snapshot reports the runtime state and the persisted event sequence.
	Snapshot() (RuntimeState, int64)
}

// MemberAgent is the concrete member runtime: a provider-backed single-turn
// streaming loop that persists its input and output in the member's session
// store (route §11.2 MemberRuntimeAdapter). Deltas stream as events, a full
// assistant message is appended atomically to messages.jsonl, then the
// member's cursor and event sequence advance together; every failure surfaces
// as an error event with the user message retained for retry.
type MemberAgent struct {
	key    InstanceKey
	spec   Spec
	store  *team.TeamSessionStore
	prov   provider.Provider
	events *EventSource

	mu       sync.Mutex
	inFlight bool
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewMemberAgent assembles a member runtime. seq seeds the persisted event
// sequence so a restarted instance continues its monotonic counter from the
// member's cursor (route §11.3); it is never reset by re-assembly.
func NewMemberAgent(key InstanceKey, spec Spec, store *team.TeamSessionStore, prov provider.Provider, seq int64) *MemberAgent {
	src := newEventSource()
	if seq > 0 {
		src.seq = uint64(seq)
	}
	return &MemberAgent{
		key:    key,
		spec:   spec,
		store:  store,
		prov:   prov,
		events: src,
		done:   make(chan struct{}),
	}
}

// Send starts one completion loop. The caller has already persisted the user
// message (Registry.Send), so the loop reads the full history — the new user
// message is the last line — assembles the request with the role-injected
// system prompt, streams deltas, appends the complete assistant message
// atomically, and advances the member's cursor and event sequence. A second
// Send while a loop is in flight returns ErrBusy; the input message stays
// persisted history for the next run in that case.
func (m *MemberAgent) Send(prompt string) error {
	m.mu.Lock()
	if m.inFlight {
		m.mu.Unlock()
		return ErrBusy
	}
	m.inFlight = true
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.done = make(chan struct{})
	m.mu.Unlock()

	m.events.Publish(m.key, EventStarted, "")
	go m.run(ctx, prompt)
	return nil
}

// Stop cancels the in-flight loop and waits for it to end.
func (m *MemberAgent) Stop() error {
	m.mu.Lock()
	cancel := m.cancel
	done := m.done
	m.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	<-done
	return nil
}

// Subscribe returns the member's bounded event stream.
func (m *MemberAgent) Subscribe() Subscription { return m.events.Subscribe() }

// Close stops the in-flight loop and closes the event source, so no
// subscriber channel is left open after the instance is torn down.
func (m *MemberAgent) Close() {
	_ = m.Stop() // Stop only ever reports the loop's own exit; there is nothing to retry
	m.events.Close()
}

// Snapshot reports the runtime state and the persisted event sequence.
func (m *MemberAgent) Snapshot() (RuntimeState, int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := RuntimeStateStopped
	if m.inFlight {
		state = RuntimeStateRunning
	}
	c, err := m.store.ReadCursor(m.key.Team, m.key.MemberID)
	if err != nil {
		return state, 0
	}
	return state, c.Sequence
}

// run executes one completion. The event sequence persists on every exit
// path (defer), so a restarted instance never renumbers events; the message
// cursor advances only together with a persisted assistant message.
func (m *MemberAgent) run(ctx context.Context, prompt string) {
	defer func() {
		m.persistSeq(m.events.CurrentSeq())
		m.finish()
	}()

	msgs, err := m.store.Messages(m.key.Team, m.key.MemberID)
	if err != nil {
		m.events.Publish(m.key, EventError, err.Error())
		return
	}
	req, err := m.buildRequest(msgs, prompt)
	if err != nil {
		m.events.Publish(m.key, EventError, err.Error())
		return
	}

	chunks, err := m.prov.Stream(ctx, *req)
	if err != nil {
		m.events.Publish(m.key, EventError, userFacingError(err))
		return
	}
	var text strings.Builder
	for ch := range chunks {
		if ctx.Err() != nil {
			m.events.Publish(m.key, EventStopped, "")
			return
		}
		if ch.Type == provider.ChunkText && ch.Text != "" {
			text.WriteString(ch.Text)
			m.events.Publish(m.key, EventDelta, ch.Text)
		}
	}
	if ctx.Err() != nil {
		m.events.Publish(m.key, EventStopped, "")
		return
	}
	if text.Len() == 0 {
		m.events.Publish(m.key, EventDone, "")
		return
	}
	if err := m.store.AppendMessage(m.key.Team, m.key.MemberID, team.SessionMessage{
		Kind: "agent",
		Text: text.String(),
		TS:   time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		m.events.Publish(m.key, EventError, err.Error())
		return
	}
	// The message event lands first, then cursor and sequence advance
	// together — a crash between the two leaves the cursor behind, replaying
	// the completed message rather than losing it (route §11.3).
	m.events.Publish(m.key, EventMessage, text.String())
	if err := m.advanceCursor(m.events.CurrentSeq()); err != nil {
		m.events.Publish(m.key, EventError, err.Error())
		return
	}
	m.events.Publish(m.key, EventDone, "")
}

// buildRequest assembles the provider request: role-injected system prompt
// first, then the member's persisted history as user/assistant turns, and the
// submitted prompt as the final user turn when history ends differently.
// No key material ever enters the request (K1).
func (m *MemberAgent) buildRequest(msgs []team.SessionMessage, prompt string) (*provider.Request, error) {
	req := &provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: ComposeSystemPrompt(m.spec.BasePrompt, m.key, m.spec.Role)},
		},
	}
	appended := false
	for _, sm := range msgs {
		if strings.TrimSpace(sm.Text) == "" {
			continue
		}
		switch sm.Kind {
		case "user":
			req.Messages = append(req.Messages, provider.Message{Role: provider.RoleUser, Content: sm.Text})
			appended = true
		case "agent":
			req.Messages = append(req.Messages, provider.Message{Role: provider.RoleAssistant, Content: sm.Text})
		}
	}
	if !appended || req.Messages[len(req.Messages)-1].Role != provider.RoleUser {
		if strings.TrimSpace(prompt) == "" {
			return nil, errors.New("agentruntime: no user message to submit")
		}
		req.Messages = append(req.Messages, provider.Message{Role: provider.RoleUser, Content: prompt})
	}
	return req, nil
}

// advanceCursor moves the member's message cursor to the current history
// length and stores the event sequence together, so restart recovery never
// replays a completed assistant message. The two values persist in one
// cursor.json write (route §11.3: cursor advances atomically with the
// message).
func (m *MemberAgent) advanceCursor(seq uint64) error {
	c, err := m.store.ReadCursor(m.key.Team, m.key.MemberID)
	if err != nil {
		return err
	}
	msgs, err := m.store.Messages(m.key.Team, m.key.MemberID)
	if err != nil {
		return err
	}
	c.Cursor = len(msgs)
	c.Sequence = int64(seq)
	return m.store.WriteCursor(m.key.Team, m.key.MemberID, c)
}

// persistSeq stores only the event sequence — used on failure and cancel
// paths, where the message cursor must not move (the user message stays
// unretried history for the next run).
func (m *MemberAgent) persistSeq(seq uint64) {
	if seq == 0 {
		return
	}
	c, err := m.store.ReadCursor(m.key.Team, m.key.MemberID)
	if err != nil {
		return
	}
	c.Sequence = int64(seq)
	_ = m.store.WriteCursor(m.key.Team, m.key.MemberID, c)
}

// finish releases the in-flight slot. The loop always publishes a terminal
// event (done/error/stopped) before returning, so subscribers never hang on
// a member that is no longer responding.
func (m *MemberAgent) finish() {
	m.mu.Lock()
	m.inFlight = false
	m.cancel = nil
	done := m.done
	close(done)
	m.mu.Unlock()
}

// userFacingError strips credential-bearing detail from provider errors for
// the event stream and UI (K1): auth bodies and key fragments never leave
// the provider boundary.
func userFacingError(err error) string {
	if err == nil {
		return ""
	}
	var ae *provider.AuthError
	if errors.As(err, &ae) {
		return fmt.Sprintf("authentication failed: %s", ae.Provider)
	}
	return err.Error()
}
