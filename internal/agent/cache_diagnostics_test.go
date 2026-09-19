package agent

import (
	"context"
	"slices"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/sessioncontext"
	"reasonix/internal/tool"
)

type cacheDiagProvider struct {
	chunks [][]provider.Chunk
	calls  int
}

func (p *cacheDiagProvider) Name() string { return "cache-diag" }

func (p *cacheDiagProvider) Stream(_ context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	chunks := p.chunks[p.calls]
	p.calls++
	ch := make(chan provider.Chunk, len(chunks))
	for _, chunk := range chunks {
		ch <- chunk
	}
	close(ch)
	return ch, nil
}

func TestRunPopulatesCacheDiagnosticsOnUsageEvents(t *testing.T) {
	prov := &cacheDiagProvider{chunks: [][]provider.Chunk{
		{
			{Type: provider.ChunkText, Text: "first"},
			{Type: provider.ChunkUsage, Usage: &provider.Usage{
				PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110,
				CacheHitTokens: 0, CacheMissTokens: 100,
			}},
		},
		{
			{Type: provider.ChunkText, Text: "second"},
			{Type: provider.ChunkUsage, Usage: &provider.Usage{
				PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110,
				CacheHitTokens: 80, CacheMissTokens: 20,
			}},
		},
	}}
	reg := tool.NewRegistry()
	var diagnostics []*event.CacheDiagnostics
	sink := event.FuncSink(func(e event.Event) {
		if e.Kind == event.Usage {
			diagnostics = append(diagnostics, e.CacheDiagnostics)
		}
	})
	session := NewSession("stable system")
	session.IncrementRewrite()
	a := New(prov, reg, session, Options{}, sink)

	if err := a.Run(context.Background(), "one"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	reg.Add(fakeTool{name: "read_file", readOnly: true})
	if err := a.Run(context.Background(), "two"); err != nil {
		t.Fatalf("second Run: %v", err)
	}

	if len(diagnostics) != 2 {
		t.Fatalf("got %d usage diagnostics, want 2", len(diagnostics))
	}
	first, second := diagnostics[0], diagnostics[1]
	if first == nil || second == nil {
		t.Fatalf("diagnostics should be populated on every usage event: first=%v second=%v", first, second)
	}
	if first.PrefixChanged {
		t.Fatalf("first usage should not report a changed prefix: %+v", first)
	}
	if first.CacheMissTokens != 100 || first.CacheHitTokens != 0 {
		t.Fatalf("first cache tokens = hit %d miss %d, want hit 0 miss 100", first.CacheHitTokens, first.CacheMissTokens)
	}
	if !second.PrefixChanged {
		t.Fatalf("second usage should report the tool prefix change: %+v", second)
	}
	if len(second.PrefixChangeReasons) != 1 || second.PrefixChangeReasons[0] != "tools" {
		t.Fatalf("second change reasons = %v, want [tools]", second.PrefixChangeReasons)
	}
	if second.CacheHitTokens != 80 || second.CacheMissTokens != 20 {
		t.Fatalf("second cache tokens = hit %d miss %d, want hit 80 miss 20", second.CacheHitTokens, second.CacheMissTokens)
	}
	if first.ToolsHash == second.ToolsHash {
		t.Fatalf("tool hash should change after registering a tool: %q", first.ToolsHash)
	}
}

// TestStablePrefixHashIgnoresSessionContextTail pins the one thing PrefixHash
// cannot express: a tail-only change. PrefixHash folds the turn-context digest
// in, so it moves on every snapshot change; StablePrefixHash is the reference a
// real provider cache keys on and must stay put, which is what makes a low
// reported hit rate attributable to the tail rather than to the prefix.
func TestStablePrefixHashIgnoresSessionContextTail(t *testing.T) {
	prov := &cacheDiagProvider{chunks: [][]provider.Chunk{
		{{Type: provider.ChunkText, Text: "one"}, {Type: provider.ChunkUsage, Usage: &provider.Usage{PromptTokens: 20, CacheMissTokens: 20}}},
		{{Type: provider.ChunkText, Text: "two"}, {Type: provider.ChunkUsage, Usage: &provider.Usage{PromptTokens: 20, CacheHitTokens: 10, CacheMissTokens: 10}}},
	}}
	var diagnostics []*event.CacheDiagnostics
	sink := event.FuncSink(func(e event.Event) {
		if e.Kind == event.Usage {
			diagnostics = append(diagnostics, e.CacheDiagnostics)
		}
	})
	a := New(prov, tool.NewRegistry(), NewSession("stable"), Options{}, sink)
	first := sessioncontext.Build(sessioncontext.Sections{Workspace: "/w", BackgroundMemory: "fact"})
	second := sessioncontext.Build(sessioncontext.Sections{Workspace: "/w", BackgroundMemory: "changed fact"})
	for i, snapshot := range []sessioncontext.Snapshot{first, second} {
		ctx := WithTurnContextBundle(context.Background(), TurnContextBundle{Executor: snapshot})
		if err := a.Run(ctx, []string{"first", "second"}[i]); err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
	}
	if len(diagnostics) != 2 || diagnostics[0] == nil || diagnostics[1] == nil {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	firstDiag, secondDiag := diagnostics[0], diagnostics[1]
	if firstDiag.PrefixHash == "" || firstDiag.StablePrefixHash == "" {
		t.Fatalf("both hashes must be populated: %+v", firstDiag)
	}
	if !secondDiag.PrefixChanged || !slices.Contains(secondDiag.PrefixChangeReasons, "session_context") {
		t.Fatalf("a snapshot change must still report session_context: %+v", secondDiag)
	}
	if secondDiag.PrefixHash == firstDiag.PrefixHash {
		t.Fatalf("PrefixHash must move with the tail: %q", secondDiag.PrefixHash)
	}
	if secondDiag.StablePrefixHash != firstDiag.StablePrefixHash {
		t.Fatalf("StablePrefixHash moved on a tail-only change: %q then %q",
			firstDiag.StablePrefixHash, secondDiag.StablePrefixHash)
	}
	if secondDiag.StablePrefixChanged {
		t.Fatalf("a tail-only change must not report a stable-prefix change: %+v", secondDiag)
	}
}

// TestStablePrefixChangedTracksTheSystemPrefix is the positive half of the
// pair: a real prefix move must set it, so the field cannot pass by always
// reporting false.
func TestStablePrefixChangedTracksTheSystemPrefix(t *testing.T) {
	prov := &cacheDiagProvider{chunks: [][]provider.Chunk{
		{{Type: provider.ChunkText, Text: "one"}, {Type: provider.ChunkUsage, Usage: &provider.Usage{PromptTokens: 20, CacheMissTokens: 20}}},
		{{Type: provider.ChunkText, Text: "two"}, {Type: provider.ChunkUsage, Usage: &provider.Usage{PromptTokens: 20, CacheMissTokens: 20}}},
	}}
	var diagnostics []*event.CacheDiagnostics
	sink := event.FuncSink(func(e event.Event) {
		if e.Kind == event.Usage {
			diagnostics = append(diagnostics, e.CacheDiagnostics)
		}
	})
	reg := tool.NewRegistry()
	a := New(prov, reg, NewSession("stable"), Options{}, sink)
	if err := a.Run(context.Background(), "one"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	reg.Add(fakeTool{name: "read_file", readOnly: true})
	if err := a.Run(context.Background(), "two"); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if len(diagnostics) != 2 || diagnostics[0] == nil || diagnostics[1] == nil {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if diagnostics[0].StablePrefixChanged {
		t.Fatalf("first turn must not report a stable-prefix change: %+v", diagnostics[0])
	}
	if !diagnostics[1].StablePrefixChanged {
		t.Fatalf("registering a tool must report a stable-prefix change: %+v", diagnostics[1])
	}
	if diagnostics[1].StablePrefixHash == diagnostics[0].StablePrefixHash {
		t.Fatalf("StablePrefixHash must move with the tool schemas: %q", diagnostics[1].StablePrefixHash)
	}
}

// TestSessionCacheMatchesPerTurnDiagnostics pins the two usage scopes together:
// the session aggregate the status line divides is the sum of the per-turn
// diagnostics. The precondition is that one stream emits at most one ChunkUsage
// — every in-tree adapter does (openai, anthropic, auxiliary recovery), because
// agent.go folds each ChunkUsage into the session counters while the turn value
// keeps the last one. A second ChunkUsage per stream would double-count the
// session side with no dedup guard, so this test documents the boundary rather
// than defending it.
func TestSessionCacheMatchesPerTurnDiagnostics(t *testing.T) {
	prov := &cacheDiagProvider{chunks: [][]provider.Chunk{
		{{Type: provider.ChunkText, Text: "cold"}, {Type: provider.ChunkUsage, Usage: &provider.Usage{
			PromptTokens: 100, CacheHitTokens: 0, CacheMissTokens: 100,
		}}},
		{{Type: provider.ChunkText, Text: "warm"}, {Type: provider.ChunkUsage, Usage: &provider.Usage{
			PromptTokens: 100, CacheHitTokens: 80, CacheMissTokens: 20,
		}}},
	}}
	var diagnostics []*event.CacheDiagnostics
	sink := event.FuncSink(func(e event.Event) {
		if e.Kind == event.Usage {
			diagnostics = append(diagnostics, e.CacheDiagnostics)
		}
	})
	a := New(prov, tool.NewRegistry(), NewSession("stable"), Options{}, sink)
	for _, input := range []string{"one", "two"} {
		if err := a.Run(context.Background(), input); err != nil {
			t.Fatalf("Run %q: %v", input, err)
		}
	}
	if len(diagnostics) != 2 {
		t.Fatalf("got %d usage diagnostics, want 2", len(diagnostics))
	}
	// Cold start: nothing cacheable existed yet, so a miss is not a prefix move.
	if diagnostics[0].CacheHitTokens != 0 || diagnostics[0].CacheMissTokens != 100 {
		t.Fatalf("cold turn = hit %d miss %d, want hit 0 miss 100",
			diagnostics[0].CacheHitTokens, diagnostics[0].CacheMissTokens)
	}
	if diagnostics[0].PrefixChanged || diagnostics[0].StablePrefixChanged {
		t.Fatalf("a cold start must not report a prefix change: %+v", diagnostics[0])
	}
	// Warm turn: same prefix, so the hit is real and nothing changed.
	if diagnostics[1].CacheHitTokens != 80 || diagnostics[1].CacheMissTokens != 20 {
		t.Fatalf("warm turn = hit %d miss %d, want hit 80 miss 20",
			diagnostics[1].CacheHitTokens, diagnostics[1].CacheMissTokens)
	}
	if diagnostics[1].PrefixChanged || diagnostics[1].StablePrefixChanged {
		t.Fatalf("an unchanged prefix must not report a change: %+v", diagnostics[1])
	}
	var wantHit, wantMiss int
	for _, diag := range diagnostics {
		wantHit += diag.CacheHitTokens
		wantMiss += diag.CacheMissTokens
	}
	gotHit, gotMiss := a.SessionCache()
	if gotHit != wantHit || gotMiss != wantMiss {
		t.Fatalf("SessionCache() = hit %d miss %d, want the per-turn sum hit %d miss %d",
			gotHit, gotMiss, wantHit, wantMiss)
	}
}
