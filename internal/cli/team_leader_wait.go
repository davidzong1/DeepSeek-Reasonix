package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"reasonix/internal/tool"
)

// The waiting primitive's bounds, both in the schema and enforced by the host.
// A wait is event-driven, so it is meant to be long: the floor is also what an
// unset argument takes, and the ceiling keeps the retired tmux flow's contract
// (leader_sleep(max_seconds=3600)) reachable when a caller asks for it.
const (
	leaderWaitMinTimeout     = 1200 * time.Second
	leaderWaitDefaultTimeout = leaderWaitMinTimeout
	leaderWaitMaxTimeout     = 3600 * time.Second
)

// waitKindTimeout labels the one event the wait itself produces: nothing the
// leader cared about arrived before its deadline. Every other kind comes from a
// producer and is named there — agentruntime's Attention* labels for a task's
// state moves, and the cli's own waitKind* for the occurrences only the host
// knows about.
const waitKindTimeout = "timeout"

// errLeaderWaitClosed reports a wait whose bus was torn down underneath it. It
// is an error, not a wakeup: nothing happened for the leader to act on.
var errLeaderWaitClosed = errors.New("cli: leader wait ended because the team registry closed")

// awaitLeaderWait blocks until a reason the leader cares about is observable or
// the context ends, and returns the reasons as one batch.
//
// afterSeq is the waiter's own high-water mark: bus-delivered events carry a
// monotonic Seq, and anything at or below it was already reported to this
// leader. The bus hands its held window to every subscription, so a fresh
// subscription sees those events again; this is what stops the replay being
// reported twice.
//
// A deadline is an ordinary end: the timeout event comes back with a nil error.
// Only a cancelled context returns an error — that is the operator taking the
// turn away, and it must never reach the model as a member finishing.
func awaitLeaderWait(ctx context.Context, sig WaitSignal, team string, afterSeq uint64) ([]WaitEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var wakeups <-chan WaitEvent
	if sig != nil {
		if sub := sig.Subscribe(team); sub != nil {
			defer sub.Close()
			wakeups = sub.C()
		}
	}
	for {
		select {
		case ev, ok := <-wakeups:
			if !ok {
				// A closed channel is the teardown signal, never an event: the bus
				// closes every subscription when the registry that owns it goes away.
				return nil, errLeaderWaitClosed
			}
			events := afterWaitSeq(append([]WaitEvent{ev}, drainWaitSubscription(wakeups)...), afterSeq)
			if len(events) > 0 {
				return events, nil
			}
			// Everything delivered was the held window this leader already
			// reported. Keep waiting rather than answering with a false empty.
		case <-ctx.Done():
			return waitEndingOnContextDone(ctx)
		}
	}
}

// afterWaitSeq drops the held events this waiter has already reported, keeping
// the arrival order of what is left.
func afterWaitSeq(events []WaitEvent, afterSeq uint64) []WaitEvent {
	kept := events[:0]
	for _, ev := range events {
		if ev.Seq > afterSeq {
			kept = append(kept, ev)
		}
	}
	return kept
}

// drainWaitSubscription takes the rest of the burst already delivered, without
// waiting for another event. Returning the batch is what keeps one wakeup from
// costing one tool call per member that finished in the same moment.
func drainWaitSubscription(wakeups <-chan WaitEvent) []WaitEvent {
	var out []WaitEvent
	for {
		select {
		case ev, ok := <-wakeups:
			if !ok {
				return out
			}
			out = append(out, ev)
		default:
			return out
		}
	}
}

// waitEndingOnContextDone renders the two ways a context ends. A deadline is the
// ordinary timeout a wait reports with a nil error; a cancellation stays an
// error, because the operator ending the turn is not a member reporting.
func waitEndingOnContextDone(ctx context.Context) ([]WaitEvent, error) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return []WaitEvent{{Kind: waitKindTimeout, Summary: "no team event arrived before the timeout"}}, nil
	}
	return nil, ctx.Err()
}

// leaderWaitTool is the leader's one waiting primitive. It blocks inside its own
// Execute and returns the wakeup reasons as the tool result, so the leader's
// next sample already carries them and no round-trip is spent asking for status.
//
// It has no poll arm by design: the wake dispatcher owns the leader's board
// cursor and publishes what it drains into the bus, so the wait reads no board
// state of its own and needs no timer to notice a wakeup.
//
// Only a leader backend is assembled with it: waiting is the leader's job, and a
// member has a task to run instead.
type leaderWaitTool struct {
	*teamTaskTool
	// signal resolves the registry's wait bus at execution time. The registry is
	// built after the service the tool is assembled around, so the bus is read
	// per call rather than captured once.
	signal func() WaitSignal
	// afterSeq is the highest bus sequence this leader has reported. The bus
	// hands its held window to every subscription, so this is what keeps a
	// replayed wakeup from being reported twice.
	afterSeq atomic.Uint64
}

// ReadOnly reports the wait as a reader: it changes no workspace file and no
// team state, so it must never be handed a workspace write lease.
func (t *leaderWaitTool) ReadOnly() bool { return true }

// PlanModeSafe is a deliberate bypass of the planning boundary, stated rather
// than derived from ReadOnly. The wait only observes — it writes no workspace
// file and no team state — and a leader that is planning the work it is waiting
// on would otherwise be stranded by a boundary that exists to stop writers.
//
// The mechanism is the classifier, not the boundary: report Safe and
// planmode.Policy.Decide returns unblocked (internal/agent/execute_one.go
// translates this report before it consults the policy).
func (t *leaderWaitTool) PlanModeSafe() bool { return true }

// TeamLifecycleStateWriter is false: the wait writes nothing at all. ReadOnly is
// what keeps it reachable across read-only boundaries, so claiming a team-state
// write here would describe an effect that never happens.
func (t *leaderWaitTool) TeamLifecycleStateWriter() bool { return false }

// EffectHint names the wait as a known reader, so the host never routes it
// through its unknown-tool handling.
func (t *leaderWaitTool) EffectHint(json.RawMessage) tool.EffectHint {
	return tool.EffectHint{Known: true, ReadOnly: true}
}

// ClassifyCall pins the wait to a serial batch. ReadOnly alone is not enough:
// the batch partitioner falls back to the target's ReadOnly when a tool has no
// classifier, which would run the wait concurrently with any neighbouring
// reader and let the step continue past the wait it was meant to block on.
func (t *leaderWaitTool) ClassifyCall(json.RawMessage) tool.CallClass {
	return tool.CallClass{Known: true, ReadOnly: true, ParallelSafe: false}
}

func (t *leaderWaitTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		TimeoutSeconds int `json:"timeout_seconds"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("leader_wait: invalid arguments: %w", err)
		}
	}
	timeout, err := leaderWaitTimeout(p.TimeoutSeconds)
	if err != nil {
		return "", err
	}
	var sig WaitSignal
	if t.signal != nil {
		sig = t.signal()
	}
	team, leaderID := "", ""
	if t.teamTaskTool != nil {
		team, leaderID = t.teamName, t.memberID
	}
	// Lines typed while the leader was working are handed over before the wait
	// blocks: no write lease is held, so accepting them is safe. A line arriving
	// later is steered by the composer and wakes this call instead.
	if out, done := t.deliverHeldInput(sig, team, leaderID); done {
		return out, nil
	}
	if bus, ok := sig.(*waitBus); ok {
		bus.enterWait(team, leaderID)
		defer bus.leaveWait(team, leaderID)
		if out, done := t.deliverHeldInput(sig, team, leaderID); done {
			return out, nil
		}
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	events, err := awaitLeaderWait(waitCtx, sig, team, t.afterSeq.Load())
	if err != nil {
		return "", err
	}
	t.observe(events)
	return formatLeaderWaitResult(events, time.Since(started)), nil
}

// deliverHeldInput admits lines the composer held for this leader. The tool
// result only says that guidance is queued; the lines themselves are written
// as user messages at the next step boundary. When the running turn cannot
// accept them, the lines are put back and this wait blocks as usual.
func (t *leaderWaitTool) deliverHeldInput(sig WaitSignal, team, leaderID string) (string, bool) {
	bus, ok := sig.(*waitBus)
	if !ok {
		return "", false
	}
	held := bus.takeHeld(team, leaderID)
	if len(held) == 0 {
		return "", false
	}
	var missed []string
	for _, text := range held {
		if !bus.admit(team, leaderID, text) {
			missed = append(missed, text)
		}
	}
	if len(missed) == len(held) {
		for _, text := range missed {
			bus.holdInput(team, leaderID, text)
		}
		return "", false
	}
	for _, text := range missed {
		bus.holdInput(team, leaderID, text)
	}
	return formatLeaderWaitResult([]WaitEvent{{
		Kind:    waitKindInput,
		ID:      leaderID,
		Summary: "user guidance is queued and will be applied at the next step",
	}}, 0), true
}

// observe advances the high-water mark past everything just reported, so the
// bus's held window is never reported to this leader a second time.
func (t *leaderWaitTool) observe(events []WaitEvent) {
	highest := t.afterSeq.Load()
	for _, ev := range events {
		if ev.Seq > highest {
			highest = ev.Seq
		}
	}
	if current := t.afterSeq.Load(); highest > current {
		t.afterSeq.CompareAndSwap(current, highest)
	}
}

// leaderWaitTimeout validates the caller's request against the bounds. Zero
// means "unset" and takes the default; below the minimum or over the ceiling is
// refused here as well as in the schema, because a host must not depend on the
// provider having validated the arguments for it.
func leaderWaitTimeout(seconds int) (time.Duration, error) {
	if seconds == 0 {
		return leaderWaitDefaultTimeout, nil
	}
	if seconds < int(leaderWaitMinTimeout.Seconds()) || time.Duration(seconds)*time.Second > leaderWaitMaxTimeout {
		return 0, fmt.Errorf("leader_wait: timeout_seconds must be between %d and %d",
			int(leaderWaitMinTimeout.Seconds()), int(leaderWaitMaxTimeout.Seconds()))
	}
	return time.Duration(seconds) * time.Second, nil
}

// formatLeaderWaitResult renders the tool result: the reasons first, so the
// leader reads what woke it before it reads how long it waited.
func formatLeaderWaitResult(events []WaitEvent, waited time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d event(s) after %s", len(events), waited.Round(time.Second))
	for _, ev := range events {
		b.WriteString("\n- [")
		b.WriteString(ev.Kind)
		b.WriteString("]")
		if ev.ID != "" {
			b.WriteString(" ")
			b.WriteString(ev.ID)
		}
		if summary := strings.TrimSpace(ev.Summary); summary != "" {
			b.WriteString(" ")
			b.WriteString(summary)
		}
	}
	return b.String()
}

// leaderWaitSignalSource resolves the registry's wait bus for one task service.
// The service holds the bus late-bound — it is built alongside the first overlay,
// before the registry that owns the bus exists — so the tool reads it per call.
func leaderWaitSignalSource(service *teamTaskService) func() WaitSignal {
	return func() WaitSignal {
		if service == nil {
			return nil
		}
		bus := service.signals.Load()
		if bus == nil {
			return nil
		}
		return bus
	}
}
