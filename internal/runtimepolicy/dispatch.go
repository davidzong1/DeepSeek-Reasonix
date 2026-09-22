package runtimepolicy

import "context"

// dispatchFramingKey marks a turn whose text is host-dispatched work rather than
// a user instruction: a team member's injected task order, the inbox commands
// and the peer-written board view that ride with it.
type dispatchFramingKey struct{}

// WithDispatchFraming marks a turn as host-dispatched work. Status: the text
// still reaches the model verbatim — only host policy derivation changes.
func WithDispatchFraming(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, dispatchFramingKey{}, true)
}

// DispatchFramed reports whether this turn's text is host-dispatched work.
//
// A dispatched text must never bind the host as policy, for two independent
// reasons. First, it is an order about work, not an instruction from the user:
// "只读审计 W1/W2/W5 当前状态" scopes the audit and says nothing about whether the
// member may hand back the deliverable it was asked to publish, yet a prefix
// match on 只读 (see hasExplicitReadOnlyClause) read it as a turn-wide mutation
// ban and refused every write-class call in the turn. Second, the same text
// carries peer-authored lines — the board view and inbox summaries are member
// reports — so parsing it lets any teammate's wording, deliberate or not,
// become another member's policy.
//
// The user's own constraints still belong to the turn that dispatched the work
// (the leader's), and the leader is told when the two disagree: see the note
// the assignment tools append under a read-only leader turn.
func DispatchFramed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	framed, _ := ctx.Value(dispatchFramingKey{}).(bool)
	return framed
}
