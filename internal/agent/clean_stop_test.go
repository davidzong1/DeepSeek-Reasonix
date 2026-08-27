package agent

import (
	"context"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// stopUsage is a provider-reported clean stop: the model says it is done rather
// than having been cut off.
func stopUsage(reason string) provider.Chunk {
	return provider.Chunk{Type: provider.ChunkUsage, Usage: &provider.Usage{
		FinishReason: reason, PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15,
	}}
}

// TestCleanStopWithVisibleAnswerEndsTurnWithoutRepair pins the accommodation: a
// model that answers and stops cleanly, without volunteering the finish tool, is
// accepted in one round trip. Before this, every such turn cost a repair round —
// observed on every turn with a gpt-class model behind an OpenAI-compatible
// gateway, which complied only when the preceding message demanded it.
func TestCleanStopWithVisibleAnswerEndsTurnWithoutRepair(t *testing.T) {
	a, prov := finishProtocolAgent([]provider.Chunk{
		{Type: provider.ChunkText, Text: "我是 Reasonix"},
		stopUsage("stop"),
		{Type: provider.ChunkDone},
	})

	if err := a.Run(context.Background(), "你是谁"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if prov.call != 1 {
		t.Fatalf("a clean stop with a visible answer must not be repaired, got %d provider calls", prov.call)
	}
	// The outcome stays undeclared rather than being invented: reporting a blocked
	// turn as "completed" would be worse than reporting nothing.
	if got := a.TurnFinishOutcome(); got != "" {
		t.Errorf("outcome = %q, want undeclared", got)
	}
}

// TestCleanStopStillRepairsDegenerateTurns pins what the relaxation must not
// swallow. The guard exists to stop a truncated or answerless response from being
// committed as the final answer, so each of these still costs its one repair.
func TestCleanStopStillRepairsDegenerateTurns(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first []provider.Chunk
	}{
		{"no visible answer", []provider.Chunk{stopUsage("stop"), {Type: provider.ChunkDone}}},
		{"truncated by length", []provider.Chunk{
			{Type: provider.ChunkText, Text: "half an ans"}, stopUsage("length"), {Type: provider.ChunkDone},
		}},
		{"tool_calls finish reason but no call", []provider.Chunk{
			{Type: provider.ChunkText, Text: "an answer"}, stopUsage("tool_calls"), {Type: provider.ChunkDone},
		}},
		{"no usage reported at all", []provider.Chunk{
			{Type: provider.ChunkText, Text: "an answer"}, {Type: provider.ChunkDone},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, prov := finishProtocolAgent(tc.first, []provider.Chunk{
				{Type: provider.ChunkText, Text: "the answer"},
				toolCallChunk("f1", "finish", `{"outcome":"completed"}`),
				{Type: provider.ChunkDone},
			})
			if err := a.Run(context.Background(), "do the work"); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if prov.call != 2 {
				t.Errorf("this turn must still be repaired, got %d provider calls", prov.call)
			}
		})
	}
}

// TestCleanStopDoesNotShadowAnHonestFinish pins that the compliant path is
// untouched: a model that answers and calls finish still declares its outcome,
// and the accommodation never fires for it.
func TestCleanStopDoesNotShadowAnHonestFinish(t *testing.T) {
	a, prov := finishProtocolAgent([]provider.Chunk{
		{Type: provider.ChunkText, Text: "Progress is blocked on an external dependency."},
		toolCallChunk("f1", "finish", `{"outcome":"blocked"}`),
		stopUsage("tool_calls"),
		{Type: provider.ChunkDone},
	})
	if err := a.Run(context.Background(), "do the work"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if prov.call != 1 {
		t.Fatalf("provider calls = %d, want 1", prov.call)
	}
	if got := a.TurnFinishOutcome(); got != FinishBlocked {
		t.Errorf("a declared outcome must survive, got %q", got)
	}
}

// TestCleanStopEndsTurnGuard pins the predicate's own three conditions, so a
// later edit cannot widen it by accident.
func TestCleanStopEndsTurnGuard(t *testing.T) {
	answered := &turnRuntime{pendingFinalAnswer: true}
	clean := &provider.Usage{FinishReason: "stop"}
	if !cleanStopEndsTurn(answered, clean) {
		t.Fatal("visible answer + clean stop + no finish call must be accepted")
	}
	for _, tc := range []struct {
		name  string
		state *turnRuntime
		usage *provider.Usage
	}{
		{"no answer", &turnRuntime{}, clean},
		{"finish already counted", &turnRuntime{pendingFinalAnswer: true, finishCalls: 1}, clean},
		{"not a clean stop", answered, &provider.Usage{FinishReason: "length"}},
		{"no usage", answered, nil},
		{"nil state", nil, clean},
	} {
		if cleanStopEndsTurn(tc.state, tc.usage) {
			t.Errorf("%s must not be accepted", tc.name)
		}
	}
}

// TestTextOnlyProviderUnaffected pins that a provider without the finish tool
// keeps its existing compatibility path, which never consulted this predicate.
func TestTextOnlyProviderUnaffected(t *testing.T) {
	prov := &scriptedProvider{name: "text-only-clean-stop", turns: [][]provider.Chunk{{
		{Type: provider.ChunkText, Text: "A plain answer."},
		stopUsage("stop"),
		{Type: provider.ChunkDone},
	}}}
	a := New(prov, tool.NewRegistry(), NewSession(""), Options{}, event.Discard)
	if err := a.Run(context.Background(), "answer me"); err != nil {
		t.Fatal(err)
	}
	if prov.call != 1 {
		t.Errorf("provider calls = %d, want 1", prov.call)
	}
}
