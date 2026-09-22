package agentruntime

import "slices"

// Attention kinds: what happened to a task, in the vocabulary a leader-side
// waiter switches on. They are edge labels, never durable state — the board
// append behind each report stays the durable record.
const (
	// AttentionReport marks a task that reached reported.
	AttentionReport = "report"
	// AttentionCancel marks a task that reached canceled.
	AttentionCancel = "cancel"
	// AttentionDispatchFailed marks a turn the member's backend refused, which
	// leaves the task assigned again for the leader to retry or reassign.
	AttentionDispatchFailed = "dispatch_failed"
)

// AttentionFunc reports one task's arrival at a terminal or attention state, at
// the moment that state is durable and before any blackboard write. kind is one
// of the Attention* labels, id names the task, and summary is the same reason
// text the durable wakeup carries, so a waiter that hears a report in-process
// can recognise the board's copy of it.
//
// Unlike WakeFunc this is not a delivery: it carries no stamp, refuses no
// caller, and must never block, read the board, or take a workspace lease. The
// report path runs it on the member's own turn goroutine, where blocking would
// delay the completion itself.
type AttentionFunc func(kind, id, summary string)

// AddAttention registers one attention report. Hosts register a report into
// their in-process wait bus; the runtime calls it in registration order, right
// after the state move is durable.
func (r *Runtime) AddAttention(fn AttentionFunc) {
	if fn == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attention = append(r.attention, fn)
}

// notifyAttention hands one durable state move to every registered report.
// A report that panics or blocks is the host's bug, not the task's: the state
// move already landed, and the durable wakeup still follows.
func (r *Runtime) notifyAttention(kind, id, summary string) {
	r.mu.Lock()
	fns := slices.Clone(r.attention)
	r.mu.Unlock()
	for _, fn := range fns {
		fn(kind, id, summary)
	}
}
