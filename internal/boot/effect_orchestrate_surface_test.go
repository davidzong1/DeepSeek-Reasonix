package boot

// A1/A6 at the boot boundary: orchestrate registers and dispatches without
// entering the provider-visible surface, and opting it in leaves the
// cache-stable prefix unchanged. A2 lives in effect_orchestrate_ablation_test.go.

import (
	"context"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"reasonix/internal/ablation"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

const effectOrchestrateProviderKind = "boot-effect-orchestrate"

// effectOrchestrateRecorder is the recorder the single registration serves.
// provider.Register refuses a duplicate kind, so the factory is installed once
// and later tests point it at a recorder of their own.
var (
	effectOrchestrateOnce     sync.Once
	effectOrchestrateRecorder atomic.Pointer[effectRecordingProvider]
)

// effectOrchestrateStage writes the fixture and points the provider factory at a
// fresh recorder.
func effectOrchestrateStage(t *testing.T) *effectRecordingProvider {
	t.Helper()
	isolateConfigHome(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	rec := &effectRecordingProvider{}
	effectOrchestrateRecorder.Store(rec)
	effectOrchestrateOnce.Do(func() {
		provider.Register(effectOrchestrateProviderKind, func(provider.Config) (provider.Provider, error) {
			return effectOrchestrateRecorder.Load(), nil
		})
	})
	writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[environment]
enabled = false

[[providers]]
name = "test-model"
kind = "`+effectOrchestrateProviderKind+`"
model = "x"
`)
	return rec
}

func effectOrchestrateBuild(t *testing.T, extra []tool.Tool, arm ablation.Set) *control.Controller {
	t.Helper()
	ctrl, err := Build(context.Background(), Options{Sink: event.Discard, ExtraTools: extra, Ablation: arm})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(ctrl.Close)
	return ctrl
}

// A1: the tool is registered and dispatchable, and the provider-visible surface
// stays the pinned set restricted to what this build registered. The assertion
// is taken after Build returned: ProviderVisible reports true unconditionally
// while the registry's allowlist is still nil, so the same check taken at
// registration time would be vacuously true.
func TestEffectOrchestrateStaysOffTheProviderSurfaceByDefault(t *testing.T) {
	rec := effectOrchestrateStage(t)
	ctrl := effectOrchestrateBuild(t, nil, ablation.Set{})
	if err := ctrl.Run(context.Background(), "reply ok"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	visible := map[string]bool{}
	for _, entry := range ctrl.ToolContractEntries() {
		visible[entry.Name] = true
	}
	all := map[string]bool{}
	for _, entry := range ctrl.AllToolContractEntries() {
		all[entry.Name] = true
	}
	if !all[tool.HostOrchestrate] {
		t.Fatal("orchestrate is not registered, so tool:orchestrate could not dispatch")
	}
	if visible[tool.HostOrchestrate] {
		t.Fatal("orchestrate entered the provider-visible surface by default")
	}
	reqs := rec.requests()
	if len(reqs) == 0 {
		t.Fatal("no request reached the provider boundary")
	}
	if toolNames(reqs[0])[tool.HostOrchestrate] {
		t.Fatalf("the provider request carries the tool: %v", toolSchemaNames(reqs[0].Tools))
	}
	for name := range visible {
		if !slices.Contains(UnifiedProviderToolNames(), name) {
			t.Fatalf("the provider surface exposes %q, which is not in the pinned set", name)
		}
	}
	for _, name := range UnifiedProviderToolNames() {
		if all[name] != visible[name] {
			t.Fatalf("pinned tool %q: registered=%v visible=%v, want the same", name, all[name], visible[name])
		}
	}
}

// A6: opting the tool in reuses the host-tool channel the other registrations
// use, and two consecutive requests then carry a byte-identical prefix.
func TestEffectOrchestrateOptInKeepsThePrefixStable(t *testing.T) {
	rec := effectOrchestrateStage(t)
	optIn := func() provider.Request {
		before := len(rec.requests())
		ctrl := effectOrchestrateBuild(t, []tool.Tool{&effectExtraTool{name: tool.HostOrchestrate}}, ablation.Set{})
		if err := ctrl.Run(context.Background(), "reply ok"); err != nil {
			t.Fatalf("Run: %v", err)
		}
		reqs := rec.requests()[before:]
		if len(reqs) == 0 {
			t.Fatal("no request reached the provider boundary")
		}
		return reqs[0]
	}
	first, second := optIn(), optIn()
	if !toolNames(first)[tool.HostOrchestrate] {
		t.Fatalf("the opted-in tool never reached the provider surface: %v", toolSchemaNames(first.Tools))
	}
	if got, want := toolSchemaNames(first.Tools), toolSchemaNames(second.Tools); !reflect.DeepEqual(got, want) {
		t.Fatalf("tool surface is not stable across requests\nfirst=%v\nsecond=%v", got, want)
	}
	if got, want := effectSystemPrompt(t, first), effectSystemPrompt(t, second); got != want {
		t.Fatalf("system prefix differs across requests\nfirst=%q\nsecond=%q", got, want)
	}
}
