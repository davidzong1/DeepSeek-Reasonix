package boot

// A5 at the boot boundary: a wide parallel plan reached through use_capability
// runs over the session's own sub-agent scheduler, so the plan cannot start more
// nodes at once than the session grants. Every slot comes from the task tool.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

const effectOrchestrateSlotsProviderKind = "boot-effect-orchestrate-slots"

var (
	effectOrchestrateSlotsOnce     sync.Once
	effectOrchestrateSlotsRecorder atomic.Pointer[effectOrchestrateSlotProvider]
)

// effectOrchestrateSlotProvider answers the turn's first round with one
// use_capability call into orchestrate and every later round with plain text,
// measuring how many streams run at once. Children run on this same provider, so
// the peak is the plan's real concurrency.
type effectOrchestrateSlotProvider struct {
	planArgs string
	hold     time.Duration

	mu   sync.Mutex
	seen int
	live int
	peak int
}

func (p *effectOrchestrateSlotProvider) Name() string { return effectOrchestrateSlotsProviderKind }

func (p *effectOrchestrateSlotProvider) Stream(_ context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	p.mu.Lock()
	p.seen++
	round := p.seen
	p.live++
	if p.live > p.peak {
		p.peak = p.live
	}
	p.mu.Unlock()
	ch := make(chan provider.Chunk, 2)
	if round == 1 {
		call := provider.ToolCall{ID: "c1", Name: tool.HostUseCapability, Arguments: p.planArgs}
		ch <- provider.Chunk{Type: provider.ChunkToolCall, ToolCall: &call}
		ch <- provider.Chunk{Type: provider.ChunkDone}
		close(ch)
		p.finish()
		return ch, nil
	}
	time.Sleep(p.hold)
	ch <- provider.Chunk{Type: provider.ChunkText, Text: "ok"}
	ch <- provider.Chunk{Type: provider.ChunkDone}
	close(ch)
	p.finish()
	return ch, nil
}

func (p *effectOrchestrateSlotProvider) finish() {
	p.mu.Lock()
	p.live--
	p.mu.Unlock()
}

func (p *effectOrchestrateSlotProvider) peakLive() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peak
}

// A5: eight independent agent nodes, one plan, one session. The peak number of
// concurrently running children stays inside the default ceiling, which is the
// same limit the assembled session applies to `fleet` and to `task`.
func TestEffectOrchestratePlanStaysInsideTheSessionSlots(t *testing.T) {
	isolateConfigHome(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[environment]
enabled = false

[[providers]]
name = "test-model"
kind = "`+effectOrchestrateSlotsProviderKind+`"
model = "x"
`)
	nodes := make([]map[string]any, 0, 8)
	for i := range 8 {
		nodes = append(nodes, map[string]any{
			"id": "n" + string(rune('1'+i)), "kind": "agent", "prompt": "look", "read_only": true,
		})
	}
	specBytes, err := json.Marshal(map[string]any{"version": 1, "mode": "parallel", "nodes": nodes})
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	probe := &effectOrchestrateSlotProvider{
		planArgs: effectOrchestratePlanArgs(t, string(specBytes)),
		hold:     40 * time.Millisecond,
	}
	effectOrchestrateSlotsRecorder.Store(probe)
	effectOrchestrateSlotsOnce.Do(func() {
		provider.Register(effectOrchestrateSlotsProviderKind, func(provider.Config) (provider.Provider, error) {
			return effectOrchestrateSlotsRecorder.Load(), nil
		})
	})
	sink := &effectOrchestrateSink{}
	ctrl, err := Build(context.Background(), Options{Sink: sink})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(ctrl.Close)
	if err := ctrl.Run(context.Background(), "run the plan"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ran := 0
	for _, e := range sink.events {
		if e.Kind == event.ToolResult && strings.HasPrefix(e.Tool.Output, "plan ") {
			ran = strings.Count(e.Tool.Output, "[agent] completed")
		}
	}
	if ran != len(nodes) {
		t.Fatalf("the plan reported %d completed nodes, want %d", ran, len(nodes))
	}
	peak := probe.peakLive()
	if peak > 6 {
		t.Fatalf("the plan ran %d nodes at once, over the session's 6", peak)
	}
	if peak < 2 {
		t.Fatalf("peak concurrency was %d, so the plan never ran in parallel", peak)
	}
	if _, auditOK := (effectOrchestrateCapture{audit: sink.audit}).lastAudit(); !auditOK {
		t.Fatal("the turn published no completion report, so the run did not reach the host boundary")
	}
}
