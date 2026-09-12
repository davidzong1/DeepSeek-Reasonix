package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// The two runs cannot see each other's graph, so the prompt is what tells the
// provider what a call is: writerMarker rides every writing call, and
// sharedClaimMarker only the two whose declared claims overlap.
const (
	writerMarker      = "IS-WRITER"
	sharedClaimMarker = "CONTENDS-FOR-SHARED"
)

// peakCounter records how many callers are inside a region at once and the
// highest simultaneous count it ever saw.
type peakCounter struct{ now, peak atomic.Int32 }

func (c *peakCounter) enter() {
	cur := c.now.Add(1)
	for {
		old := c.peak.Load()
		if cur <= old || c.peak.CompareAndSwap(old, cur) {
			return
		}
	}
}

func (c *peakCounter) leave() { c.now.Add(-1) }

// orchFleetContrastProvider holds every call open long enough for the other run
// to reach its own writer, and counts the overlap it observes.
type orchFleetContrastProvider struct {
	hold       time.Duration
	all        peakCounter
	writers    peakCounter
	sharedRuns atomic.Int32
	writerRuns atomic.Int32
}

func (p *orchFleetContrastProvider) Name() string { return "orchestrate-fleet-contrast" }

func (p *orchFleetContrastProvider) Stream(_ context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	writer, shared := false, false
	for _, message := range req.Messages {
		if strings.Contains(message.Content, writerMarker) {
			writer = true
		}
		if strings.Contains(message.Content, sharedClaimMarker) {
			shared = true
		}
	}
	p.all.enter()
	if writer {
		p.writers.enter()
		p.writerRuns.Add(1)
	}
	if shared {
		p.sharedRuns.Add(1)
	}
	time.Sleep(p.hold)
	if writer {
		p.writers.leave()
	}
	p.all.leave()
	ch := make(chan provider.Chunk, 2)
	ch <- provider.Chunk{Type: provider.ChunkText, Text: "done"}
	ch <- provider.Chunk{Type: provider.ChunkDone}
	close(ch)
	return ch, nil
}

// A5's fleet contrast: an orchestration plan and a fleet run over overlapping
// write_paths, on one session's scheduler. Each plan's preflight
// (fleetPlan.validateConcurrentWriteClaims) serialises only the pairs its own
// edges order, and neither run appears in the other's graph, so nothing below
// the scheduler can order these two writers. Both must still finish inside the
// session's ceilings: a directory claim is optimistic by construction
// (liveClaim.reservation is empty until a file is realized), so the contrast is
// that coexistence is allowed while the ceilings are not raised.
func TestOrchestrationAndFleetShareTheSessionWriteClaims(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"shared", "a", "b", "c"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	prov := &orchFleetContrastProvider{hold: 60 * time.Millisecond}
	scheduler := NewSubagentScheduler(6, 3)
	registry := tool.NewRegistry()
	registry.Add(orchReadOnlyTool{})
	task := NewTaskTool(prov, nil, registry, 20, 0, 0, 0, 0, 0, 0, 0.0, "", "sys", nil, 0, "", "", nil).
		WithTranscripts(mustSubagentStore(t), root, "base", "high").
		WithScheduler(scheduler)

	plan, err := Compile(spec(ModeSequence,
		NodeSpec{ID: "w", Kind: NodeAgent, Prompt: writerMarker + " " + sharedClaimMarker + " orchestrate writer",
			WritePaths: []string{"shared/"}},
		NodeSpec{ID: "r", Kind: NodeAgent, Needs: []string{"w"}, Prompt: "orchestrate reader", ReadOnly: true},
	), ValidateOptions{AllowAgentNodes: true})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	writing := func(id, dir string) string {
		return `{"prompt":"` + writerMarker + ` fleet writer ` + id + `","write_paths":["` + dir + `/"]}`
	}
	items := []string{
		`{"prompt":"` + writerMarker + ` ` + sharedClaimMarker + ` fleet writer shared","write_paths":["shared/"]}`,
		writing("a", "a"), writing("b", "b"), writing("c", "c"),
	}
	for i := range 5 {
		items = append(items, `{"prompt":"fleet reader `+strconv.Itoa(i)+`","read_only":true}`)
	}
	fleetArgs := json.RawMessage(`{"tasks":[` + strings.Join(items, ",") + `]}`)

	var wg sync.WaitGroup
	var planErr, fleetErr error
	var planResult OrchestrationResult
	wg.Add(2)
	go func() {
		defer wg.Done()
		ctx := withCallContext(WithParentSession(context.Background(), "contrast-session"), "orch-call", event.Discard, nil, false)
		planResult, planErr = RunOrchestration(ctx, plan, NewRunOptions(task, nil))
	}()
	go func() {
		defer wg.Done()
		ctx := withCallContext(WithParentSession(context.Background(), "contrast-session"), "fleet-call", event.Discard, nil, false)
		_, fleetErr = NewFleetTool(task).Execute(ctx, fleetArgs)
	}()
	wg.Wait()

	if planErr != nil {
		t.Fatalf("the plan did not survive a concurrent fleet: %v", planErr)
	}
	if fleetErr != nil {
		t.Fatalf("the fleet did not survive a concurrent plan: %v", fleetErr)
	}
	if !planResult.Completed() {
		t.Fatalf("the plan did not complete beside the fleet: %+v", planResult.Nodes)
	}
	if runs := prov.sharedRuns.Load(); runs < 2 {
		t.Fatalf("only %d call claimed the shared directory, so the two runs never contended", runs)
	}
	// Five writers and ten calls were asked for against a session granting
	// three and six, so the two ceilings below are pressed rather than nominal.
	if runs := prov.writerRuns.Load(); runs < 5 {
		t.Fatalf("only %d of the 5 declared writers ran, so the writer ceiling was never pressed", runs)
	}
	total, writers := scheduler.Limits()
	if total != 6 || writers != 3 {
		t.Fatalf("the runs changed the session's ceilings to %d/%d", total, writers)
	}
	if peak := prov.all.peak.Load(); peak > 6 {
		t.Fatalf("the two runs reached %d concurrent calls, over the session's 6", peak)
	}
	if peak := prov.writers.peak.Load(); peak > 3 {
		t.Fatalf("the two runs reached %d concurrent writers, over the session's 3", peak)
	}
}

// The exclusion that makes the coexistence above safe, taken at the seam the
// two runs share. A declared directory reserves nothing, so both claims go
// live; the refusal arrives when the second writer realizes a file the first
// already holds, which is the only point at which either run could corrupt the
// other's work.
func TestSharedSchedulerRefusesTheSameRealizedFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "shared"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "shared", "notes.md")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirClaim, err := NormalizeWritePaths(root, []string{"shared/"})
	if err != nil {
		t.Fatal(err)
	}
	fileClaim, err := NormalizeWritePaths(root, []string{"shared/notes.md"})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewSubagentScheduler(6, 3)
	releaseA, planClaim, err := scheduler.AcquireWithID(context.Background(),
		AcquireRequest{Writer: true, WritePaths: dirClaim, Label: "plan node"})
	if err != nil {
		t.Fatalf("the plan's directory claim was refused: %v", err)
	}
	defer releaseA()
	releaseB, fleetClaim, err := scheduler.AcquireWithID(context.Background(),
		AcquireRequest{Writer: true, WritePaths: dirClaim, Label: "fleet item"})
	if err != nil {
		t.Fatalf("a concurrent directory claim must go live, not queue: %v", err)
	}
	defer releaseB()
	if err := scheduler.Realize(planClaim, fileClaim); err != nil {
		t.Fatalf("the first writer could not realize its own file: %v", err)
	}
	if err := scheduler.Realize(fleetClaim, fileClaim); err == nil {
		t.Fatal("two live writers realized the same file; the second must be refused")
	}
}
