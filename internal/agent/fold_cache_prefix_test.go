// Part B (TEAM_MEMBER_CACHE_BEHAVIOR_PLAN.md) B1/B3: a fold is the one context
// operation that is *expected* to rewrite the model-visible transcript, so it is
// also the one place a cache prefix can be lost by accident. These tests pin the
// cache contract of a fold: the stable prefix (system + tools) survives it
// byte-for-byte, the fold request reuses that same prefix instead of paying for
// a second shape, and the canonical transcript is never the thing that moves.
package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// foldCacheFixture builds an agent whose transcript is large enough to fold and
// whose registry carries a non-empty tool surface, so the stable prefix has both
// halves a provider cache keys on.
func foldCacheFixture(t *testing.T) (*Agent, *fakeProvider) {
	t.Helper()
	prov := &fakeProvider{reply: "durable digest"}
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "read_file", readOnly: true})
	reg.Add(fakeTool{name: "bash", readOnly: false})
	sess := NewSession("member system prefix")
	for i := range 10 {
		sess.Add(provider.Message{Role: provider.RoleUser, Content: strings.Repeat("u", 90) + string(rune('a'+i))})
		sess.Add(provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("a", 120)})
	}
	return New(prov, reg, sess, Options{ContextWindow: 3000, RecentKeep: 2, ArchiveDir: t.TempDir()}, event.Discard), prov
}

// TestFoldPreservesTheStableCachePrefix is B1's fold effect test: the system and
// tool halves of the stable prefix must be identical before and after a real
// fold, so the post-fold request is a cold *content* request and never a cold
// *prefix* request. A "system" or "tools" reason here would mean the fold itself
// invalidated the shared prefix.
func TestFoldPreservesTheStableCachePrefix(t *testing.T) {
	a, _ := foldCacheFixture(t)
	before := a.capturePrefixShape(a.providerToolSchemas())

	if err := a.CompactNow(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}

	after := a.capturePrefixShape(a.providerToolSchemas())
	if before.SystemHash != after.SystemHash {
		t.Fatalf("the fold moved the system prefix: %q → %q", before.SystemHash, after.SystemHash)
	}
	if before.ToolsHash != after.ToolsHash {
		t.Fatalf("the fold moved the tool prefix: %q → %q", before.ToolsHash, after.ToolsHash)
	}
	if before.ToolSchemaTokens != after.ToolSchemaTokens {
		t.Fatalf("the fold changed the schema token footprint: %d → %d", before.ToolSchemaTokens, after.ToolSchemaTokens)
	}
	diag := CompareShape(before, after, nil, nil)
	if slices.Contains(diag.PrefixChangeReasons, "system") || slices.Contains(diag.PrefixChangeReasons, "tools") {
		t.Fatalf("the fold reported a stable-prefix change: %v", diag.PrefixChangeReasons)
	}
	if diag.StablePrefixChanged {
		t.Fatalf("StablePrefixChanged must stay false across a fold: %+v", diag)
	}
	// The projection installed by the fold is the only thing that may have
	// moved, and it is a content change — the caller attributes it with a
	// drained content reason, never with "system"/"tools".
	if want := shortHash(map[string]string{"system": after.SystemHash, "tools": after.ToolsHash}); diag.StablePrefixHash != want {
		t.Fatalf("StablePrefixHash = %q, want %q", diag.StablePrefixHash, want)
	}
}

// TestFoldRequestReusesTheLiveToolSurface pins the cache alignment of the fold
// itself: the summarizer request must be the live prefix plus one appended
// instruction, carrying the identical tool schemas. A fold that re-derived its
// own surface would pay a full cold prefix on the maintenance request — exactly
// the cost the fold exists to avoid.
func TestFoldRequestReusesTheLiveToolSurface(t *testing.T) {
	a, _ := foldCacheFixture(t)
	live := a.providerToolSchemas()
	if len(live) == 0 {
		t.Fatal("the fixture must carry a non-empty tool surface")
	}

	region := a.modelVisibleMessages()
	if len(region) == 0 {
		t.Fatal("the fixture must carry a non-empty visible region")
	}
	foldReq := a.summaryRequest(region, "")

	if !reflect.DeepEqual(foldReq.Tools, live) {
		t.Fatalf("the fold request re-derived its tool surface\ngot  %v\nwant %v",
			toolSchemaNamesOf(foldReq.Tools), toolSchemaNamesOf(live))
	}
	// The instruction is append-only: the fold re-sends the live prefix
	// unchanged and adds exactly one host message after it.
	if len(foldReq.Messages) != len(region)+1 {
		t.Fatalf("the fold request has %d messages, want the %d-message region plus one instruction",
			len(foldReq.Messages), len(region))
	}
	if !strings.Contains(foldReq.Messages[len(foldReq.Messages)-1].Content, "Compact the preceding conversation") {
		t.Fatalf("the last fold message is not the compaction instruction: %q", foldReq.Messages[len(foldReq.Messages)-1].Content)
	}
}

// TestFoldKeepsTheCanonicalTranscriptAndOneSummary covers B3's other half: a
// fold is a projection install, so the canonical transcript must survive
// verbatim and the provider-visible view must carry exactly one summary. Two
// summaries would mean the previous fold's output was re-summarized rather than
// appended to, which is both a correctness and a cache-prefix defect.
func TestFoldKeepsTheCanonicalTranscriptAndOneSummary(t *testing.T) {
	a, _ := foldCacheFixture(t)
	canonical := a.sess.conversation.Snapshot()

	if err := a.CompactNow(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if got := a.sess.conversation.Snapshot(); !reflect.DeepEqual(got, canonical) {
		t.Fatalf("the fold rewrote the canonical transcript\ngot  %d messages\nwant %d", len(got), len(canonical))
	}

	summaries := 0
	for _, m := range a.modelVisibleMessages() {
		if isCompactionSummary(m) {
			summaries++
		}
	}
	if summaries != 1 {
		t.Fatalf("provider-visible summaries = %d, want exactly 1", summaries)
	}
}

// TestFoldInstallsAtMostOneToolSchemaChange pins the budget the plan allows a
// fold: at most one tool-schema change, and with schema pruning absent it must
// be zero. It is stated against the fold boundary rather than a bare hash
// comparison so a future pruning implementation has a test to satisfy.
func TestFoldInstallsAtMostOneToolSchemaChange(t *testing.T) {
	a, _ := foldCacheFixture(t)
	before := a.capturePrefixShape(a.providerToolSchemas())
	if err := a.CompactNow(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}
	after := a.capturePrefixShape(a.providerToolSchemas())
	if before.ToolsHash != after.ToolsHash {
		t.Fatalf("with schema pruning off a fold must change the tool surface zero times: %q → %q",
			before.ToolsHash, after.ToolsHash)
	}
}

func toolSchemaNamesOf(schemas []provider.ToolSchema) []string {
	out := make([]string, 0, len(schemas))
	for _, s := range schemas {
		out = append(out, s.Name+":"+string(json.RawMessage(s.Parameters))[:min(8, len(s.Parameters))])
	}
	return out
}

// nativeToolSearchFake is a provider that reports native tool-search support, so
// providerToolSchemas emits the deferred MCP tail this test is about.
type nativeToolSearchFake struct{ fakeProvider }

func (p *nativeToolSearchFake) NativeToolSearchAvailable() bool { return true }

// TestDeferredMCPTailOrderIsCanonical pins the one place the wire tool array and
// the hashed tool array could disagree. The tail is assembled from registry
// insertion order but hashed after normalizeToolSchemas sorts, so a same-content
// re-registration — RemovePrefix + Add moves a server's whole block to the tail
// — would change the provider's bytes while ToolsHash and StablePrefixChanged
// stayed silent. MCP list_changed and on-demand server registration both do
// exactly that at runtime, so the tail order must not depend on registration.
func TestDeferredMCPTailOrderIsCanonical(t *testing.T) {
	restore := provider.SetNativeToolSearchPreviewForTest(true)
	defer restore()

	build := func(order ...string) []provider.ToolSchema {
		reg := tool.NewRegistry()
		reg.Add(fakeTool{name: "read_file", readOnly: true})
		for _, name := range order {
			reg.Add(fakeTool{name: name, readOnly: true})
		}
		// The boot allowlist never names an MCP tool, so both stay deferred.
		reg.SetProviderVisibleTools([]string{"read_file"})
		a := New(&nativeToolSearchFake{}, reg, NewSession("sys"), Options{}, event.Discard)
		return a.providerToolSchemas()
	}

	forward := build("mcp__a__x", "mcp__b__y")
	reverse := build("mcp__b__y", "mcp__a__x")
	if !reflect.DeepEqual(toolSchemaNamesOf(forward), toolSchemaNamesOf(reverse)) {
		t.Fatalf("the deferred tail follows registration order\ngot  %v\nwant %v",
			toolSchemaNamesOf(forward), toolSchemaNamesOf(reverse))
	}
	// The tail is real: both deferred tools are on the wire.
	if len(forward) != 3 {
		t.Fatalf("expected the visible tool plus two deferred MCP tools, got %v", toolSchemaNamesOf(forward))
	}
	if got := toolSchemaNamesOf(forward)[1:]; !slices.Equal(got, []string{"mcp__a__x:{\"type\":", "mcp__b__y:{\"type\":"}) {
		t.Fatalf("deferred tail is not in name order: %v", got)
	}
}

// TestProjectionRewriteReasonsOnlyComeFromInstalls pins the boundary that keeps
// the attribution honest: the receipt re-emits in context_receipt.go and
// session_checkpoint.go republish an install that already happened, and neither
// may queue a reason — that would report a rewrite on a request that never saw
// one.
func TestProjectionRewriteReasonsOnlyComeFromInstalls(t *testing.T) {
	installed := &ContextMaintenanceReceipt{Status: "applied", Action: maintenanceActionSummary}
	for _, tc := range []struct {
		action string
		want   string
	}{
		{maintenanceActionSummary, "compact_auto"},
		{maintenanceActionPrune, "prune"},
		{maintenanceActionTruncate, "truncate"},
		{"noop", ""},
		{"", ""},
		{"something_new", ""},
	} {
		if got := projectionRewriteReason(tc.action); got != tc.want {
			t.Errorf("projectionRewriteReason(%q) = %q, want %q", tc.action, got, tc.want)
		}
	}

	// A re-emit of the same stored receipt must stay silent.
	sess := NewSession("sys")
	a := New(&fakeProvider{reply: "ok"}, tool.NewRegistry(), sess, Options{}, event.Discard)
	a.emitContextMaintenance(installed)
	if reasons := sess.DrainContentRewriteReasons(); len(reasons) != 0 {
		t.Fatalf("re-emitting a stored receipt queued %v; want none", reasons)
	}
	a.noteProjectionRewrite(installed)
	if reasons := sess.DrainContentRewriteReasons(); !slices.Equal(reasons, []string{"compact_auto"}) {
		t.Fatalf("the install site queued %v; want [compact_auto]", reasons)
	}
	// The same cause twice in one drain window collapses.
	a.noteProjectionRewrite(installed)
	a.noteProjectionRewrite(installed)
	if reasons := sess.DrainContentRewriteReasons(); !slices.Equal(reasons, []string{"compact_auto"}) {
		t.Fatalf("repeated installs queued %v; want one [compact_auto]", reasons)
	}
}
