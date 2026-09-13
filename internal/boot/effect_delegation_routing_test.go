package boot

// The router may suggest a delegation tool the provider schema hides. This pins
// it at the assembled boundary, where both halves are real: the catalog gains
// exactly the declared tools, and the provider-visible surface does not move.

import (
	"context"
	"testing"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/capability"
	"reasonix/internal/event"
	"reasonix/internal/tool"
)

func TestEffectDelegationToolsRouteWithoutMovingTheProviderSurface(t *testing.T) {
	isolateConfigHome(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	writeFile(t, dir, "reasonix.toml", effectOrchestrateRunConfig)
	registerBootTokenProfileTestProvider()
	setBootTokenProfileTestProvider(t, testutil.NewMock("routing", testutil.Turn{Text: "ok"}))
	ctrl, err := Build(context.Background(), Options{Sink: event.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(ctrl.Close)

	visible := ctrl.ToolContractEntries()
	routable := ctrl.RoutableToolContractEntries()
	if len(routable) != 4 {
		t.Fatalf("routable hidden tools = %d, want the four delegation tools", len(routable))
	}
	for _, e := range routable {
		switch e.Name {
		case tool.HostOrchestrate, tool.HostFleet, tool.HostParallelTasks, tool.HostTask:
		default:
			t.Fatalf("an unrelated hidden tool declared routing triggers: %s", e.Name)
		}
	}
	// The provider-visible surface is what the cache key covers; routing
	// metadata must not reach it.
	for _, e := range visible {
		if len(e.Triggers) > 0 {
			t.Fatalf("routing triggers leaked onto the provider surface: %s", e.Name)
		}
	}
	// And a delegation request actually routes, with suggest as the only policy.
	entries := append(capability.ToolEntries(visible), capability.ToolEntries(routable)...)
	got := capability.Route("analyze these files in parallel and report each separately", entries)
	if len(got.Candidates) == 0 {
		t.Fatal("a parallel request routed no delegation tool")
	}
	for _, c := range got.Candidates {
		if c.Policy != capability.AutoUseSuggest {
			t.Fatalf("routed %s with policy %q, want suggest", c.Entry.ID, c.Policy)
		}
	}
	if quiet := capability.Route("fix the typo in main.go", entries); len(quiet.Candidates) != 0 {
		t.Fatalf("an ordinary request routed %+v", quiet.Candidates)
	}
}
