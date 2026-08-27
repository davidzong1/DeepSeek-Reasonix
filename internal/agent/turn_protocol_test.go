package agent

import (
	"context"
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// TestTurnProtocolReminderArmsAfterOneRepair pins the whole point of the
// reminder: a model that only finishes when the immediately preceding user
// message demands it costs two round trips on turn one, and one on every turn
// after — because the reminder now rides the turn tail.
//
// The scripted provider answers in prose whenever the reminder is absent and
// finishes properly when it is present, which is the reported behaviour of a
// gpt-class model behind an OpenAI-compatible gateway.
func TestTurnProtocolReminderArmsAfterOneRepair(t *testing.T) {
	prov := &reminderSensitiveProvider{}
	reg := tool.NewRegistry()
	reg.Add(NewFinishTool())
	a := New(prov, reg, NewSession(""), Options{}, event.Discard)

	if err := a.Run(context.Background(), "你是谁"); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if prov.calls != 2 {
		t.Fatalf("turn 1 should need one repair, got %d provider calls", prov.calls)
	}
	if a.sess.turnProtocol.armed() != true {
		t.Fatal("the repair must arm the reminder for later turns")
	}

	prov.calls = 0
	if err := a.Run(context.Background(), "再说一次"); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if prov.calls != 1 {
		t.Errorf("turn 2 must finish in one round trip, got %d provider calls", prov.calls)
	}
	if !prov.sawReminder {
		t.Error("turn 2's request must carry the reminder block")
	}
	// The reminder is turn tail, not prefix: it belongs to the user message and
	// the tool schemas are untouched by it.
	last := prov.lastRequest.Messages[len(prov.lastRequest.Messages)-1]
	if last.Role != provider.RoleUser {
		t.Errorf("the reminder must ride a user message, got role %q", last.Role)
	}
	if !strings.Contains(last.Content, "<turn-protocol>") {
		t.Errorf("the reminder must be in the user turn: %q", last.Content)
	}
	if !strings.Contains(last.Content, "再说一次") {
		t.Errorf("the user's own text must survive the injection: %q", last.Content)
	}
}

// TestTurnProtocolReminderAbsentUntilNeeded pins the cost contract: a model that
// finishes on its own never sees the block, so compliant providers pay nothing.
func TestTurnProtocolReminderAbsentUntilNeeded(t *testing.T) {
	prov := &scriptedProvider{name: "compliant", turns: [][]provider.Chunk{{
		{Type: provider.ChunkText, Text: "The work is done."},
		toolCallChunk("f1", "finish", `{"outcome":"completed"}`),
		{Type: provider.ChunkDone},
	}}}
	reg := tool.NewRegistry()
	reg.Add(NewFinishTool())
	a := New(prov, reg, NewSession(""), Options{}, event.Discard)

	if err := a.Run(context.Background(), "do the work"); err != nil {
		t.Fatal(err)
	}
	if a.sess.turnProtocol.armed() {
		t.Error("a compliant turn must not arm the reminder")
	}
	for _, req := range prov.requests {
		for _, m := range req.Messages {
			if strings.Contains(m.Content, "<turn-protocol>") {
				t.Fatalf("a compliant conversation must never carry the reminder: %q", m.Content)
			}
		}
	}
}

// TestWithTurnProtocolIdempotentAndGated pins the injector's own rules.
func TestWithTurnProtocolIdempotentAndGated(t *testing.T) {
	if got := withTurnProtocol("hello", false); got != "hello" {
		t.Errorf("an unarmed conversation must not inject: %q", got)
	}
	once := withTurnProtocol("hello", true)
	if !strings.Contains(once, "<turn-protocol>") {
		t.Fatalf("armed injection missing: %q", once)
	}
	if twice := withTurnProtocol(once, true); twice != once {
		t.Errorf("injection must be idempotent:\nonce:  %q\ntwice: %q", once, twice)
	}
	// A repair prompt already states the rule; the block must not stack onto it.
	repair := turnProtocolBlock() + "\n\nProtocol repair: finish this turn now."
	if got := withTurnProtocol(repair, true); got != repair {
		t.Errorf("must not double-state the rule: %q", got)
	}
	if got := withTurnProtocol("   ", true); got != "   " {
		t.Errorf("an empty turn gains nothing: %q", got)
	}
}

// reminderSensitiveProvider finishes only when the reminder block is present in
// the last user message, mirroring the observed model behaviour.
type reminderSensitiveProvider struct {
	calls       int
	sawReminder bool
	lastRequest provider.Request
}

func (*reminderSensitiveProvider) Name() string { return "reminder-sensitive" }

func (p *reminderSensitiveProvider) Stream(_ context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	p.calls++
	p.lastRequest = req
	demanded := false
	for _, m := range req.Messages {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "<turn-protocol>") {
			demanded = true
			p.sawReminder = true
		}
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "Protocol repair") {
			demanded = true
		}
	}
	var chunks []provider.Chunk
	if demanded {
		chunks = []provider.Chunk{
			{Type: provider.ChunkText, Text: "我是 Reasonix"},
			{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{ID: "f1", Name: "finish", Arguments: `{"outcome":"completed"}`}},
			{Type: provider.ChunkDone},
		}
	} else {
		chunks = []provider.Chunk{{Type: provider.ChunkText, Text: "我是 Reasonix"}, {Type: provider.ChunkDone}}
	}
	ch := make(chan provider.Chunk, len(chunks))
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}
