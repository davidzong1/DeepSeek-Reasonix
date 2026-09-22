package cli

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"reasonix/internal/team"
)

// memberCockpit runs one member's durable bookkeeping off the Update goroutine.
//
// A settled turn publishes "this member's history changed" into the member's
// canonical owner metadata: a read of the member's own history identity plus a
// locked read-modify-write on the owner document, both of which the store
// performs under a cross-process lock. That used to happen inside the event
// handler, so the Update goroutine paid a store write per member per turn — the
// class of cost the decoupling plan moved out of the frame path (§36).
//
// One worker per member: a member's commands run in order on their own
// goroutine, so one member's store never delays another's and a member's own
// publications cannot interleave. The worker only *reports*: the result is
// applied on the Update goroutine, where the roster tick collects it (see
// collectCockpitResults).
type memberCockpit struct {
	mu      sync.Mutex
	workers map[string]*cockpitWorker
	results chan cockpitResult
	closed  bool
}

// cockpitCommand is one member's unit of durable bookkeeping.
type cockpitCommand struct {
	owners  *team.OwnerStore
	binding team.MemberBinding
	stamper memberHistoryStamper
	// bump advances the owner generation: true for a history change, false for
	// the assembly-time call that only establishes the identity.
	bump bool
	// requireIdentity skips the command when the member cannot name its own
	// history yet: an invented identity would create an owner for a member that
	// never wrote one, or advance a generation that did not change.
	requireIdentity bool
	// errKey names a failure the window reports once per cause.
	errKey string
}

// cockpitResult is one command's outcome, ready to apply on the Update goroutine.
// An empty errMsg is a success.
type cockpitResult struct {
	errKey string
	errMsg string
}

const (
	// cockpitCommandBacklog bounds one member's queued commands. Commands arrive
	// one per settled turn, so the backlog is slack for a burst.
	cockpitCommandBacklog = 16
	// cockpitResultBacklog bounds the reports waiting to be collected. The tick
	// drains it every second, so it only has to hold a burst of settled turns.
	cockpitResultBacklog = 128
)

func newMemberCockpit() *memberCockpit {
	return &memberCockpit{
		workers: map[string]*cockpitWorker{},
		results: make(chan cockpitResult, cockpitResultBacklog),
	}
}

// submit hands one command to its member's worker, starting it on first use. It
// reports false when the cockpit is closed, and the caller then runs the command
// itself.
func (c *memberCockpit) submit(cmd cockpitCommand) bool {
	if c == nil || cmd.binding.MemberID == "" {
		return false
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false
	}
	worker := c.workers[cmd.binding.MemberID]
	if worker == nil {
		worker = &cockpitWorker{
			cmds:    make(chan cockpitCommand, cockpitCommandBacklog),
			done:    make(chan struct{}),
			results: c.results,
		}
		c.workers[cmd.binding.MemberID] = worker
		go worker.loop()
	}
	c.mu.Unlock()
	worker.pending.Add(1)
	select {
	case worker.cmds <- cmd:
	default:
		// Saturated: the caller is the Update goroutine, so the command is never
		// run here and never dropped. A one-off out-of-order run is safe — the
		// owner store's own lock is what serializes writers.
		go worker.run(cmd)
	}
	return true
}

// drain takes every report finished so far, without waiting. An empty result is
// the common case: a publication only reports on completion.
func (c *memberCockpit) drain() []cockpitResult {
	if c == nil {
		return nil
	}
	var out []cockpitResult
	for {
		select {
		case res := <-c.results:
			out = append(out, res)
		default:
			return out
		}
	}
}

// awaitIdle waits for every worker's queued command to settle. It exists for
// callers that cannot wait for a tick — tests asserting on the store right after
// a publication — and for teardown, which must not outlive a write in flight.
func (c *memberCockpit) awaitIdle(timeout time.Duration) bool {
	if c == nil {
		return true
	}
	deadline := time.Now().Add(timeout)
	for {
		if c.idle() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// idle reports whether no command is queued or running on any member's worker.
func (c *memberCockpit) idle() bool {
	c.mu.Lock()
	workers := make([]*cockpitWorker, 0, len(c.workers))
	for _, worker := range c.workers {
		workers = append(workers, worker)
	}
	c.mu.Unlock()
	for _, worker := range workers {
		if worker.pending.Load() != 0 {
			return false
		}
	}
	return true
}

// close stops every worker. A command already running finishes — it is a store
// write, and dropping it mid-flight is what the store's lock exists to prevent —
// but no queued command starts and no new one is accepted.
func (c *memberCockpit) close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	for _, worker := range c.workers {
		close(worker.done)
	}
	c.workers = map[string]*cockpitWorker{}
}

// cockpitWorker is one member's serial executor.
type cockpitWorker struct {
	cmds    chan cockpitCommand
	done    chan struct{}
	results chan<- cockpitResult
	// pending counts commands queued or running, so awaitIdle can tell a settled
	// worker from a busy one without inspecting channels.
	pending atomic.Int64
}

// loop runs commands in arrival order until the cockpit closes. A command
// already taken from the queue always finishes: it is a durable write.
func (w *cockpitWorker) loop() {
	for {
		select {
		case <-w.done:
			return
		case cmd := <-w.cmds:
			w.run(cmd)
		}
	}
}

// run performs one command and reports it. The report waits for room in the
// result backlog: a report the window never sees is a failure it cannot tell the
// user about.
func (w *cockpitWorker) run(cmd cockpitCommand) {
	defer w.pending.Add(-1)
	res := runOwnerHistoryPublication(context.Background(), cmd)
	select {
	case w.results <- res:
	case <-w.done:
	}
}

// runOwnerHistoryPublication performs one publication: read the member's own
// history identity, then record it as that member's canonical owner identity. It
// is a plain function of its command so a worker can run it off the Update
// goroutine, and a window with no cockpit can run it inline.
func runOwnerHistoryPublication(ctx context.Context, cmd cockpitCommand) cockpitResult {
	stamp := ""
	if cmd.stamper != nil {
		stamp = strings.TrimSpace(cmd.stamper.HistoryStamp())
	}
	if cmd.requireIdentity && stamp == "" {
		return cockpitResult{errKey: cmd.errKey}
	}
	if err := recordMemberOwnerHistoryWith(ctx, cmd.owners, cmd.binding, stamp, cmd.bump); err != nil {
		return cockpitResult{errKey: cmd.errKey, errMsg: err.Error()}
	}
	return cockpitResult{errKey: cmd.errKey}
}
