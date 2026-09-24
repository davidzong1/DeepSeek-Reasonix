package cli

import (
	"context"
	"strings"
	"sync/atomic"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/nilutil"
	"reasonix/internal/team"
)

// conclusionDeltaReader is the member-side read of the shared board's
// conclusions: one page of text and the cursor position that page reached.
//
// It exists so the member-side hook can be assembled and tested without the
// durable board in hand; boardConclusionReader below is the only implementation
// the host builds.
type conclusionDeltaReader interface {
	Read(ctx context.Context, memberID string) (text string, advanceTo int64, err error)
	Ack(ctx context.Context, memberID string, advanceTo int64) error
}

// boardConclusionReader is the durable reader: Part A's conclusion feed over the
// board store the team session already opened. A nil store reads as an empty
// board rather than a failure, so a host with no board installs no hook at all
// (installConclusionDelta refuses it) instead of one that errors every step.
type boardConclusionReader struct{ store *team.SQLiteStore }

// conclusionDeltaReaderFor wraps one board store as a member-side reader.
func conclusionDeltaReaderFor(store *team.SQLiteStore) conclusionDeltaReader {
	if store == nil {
		return nil
	}
	return boardConclusionReader{store: store}
}

func (r boardConclusionReader) Read(ctx context.Context, memberID string) (string, int64, error) {
	return team.ReadConclusionDelta(ctx, r.store, memberID)
}

func (r boardConclusionReader) Ack(ctx context.Context, memberID string, advanceTo int64) error {
	return team.AckConclusionDelta(ctx, r.store, memberID, advanceTo)
}

// memberConclusionDelta is one member's pre-sampling board delta: the text the
// agent appends before a sampling step, and the cursor move that may only follow
// a successful append.
//
// The two halves are deliberately not one call. Reading early and acknowledging
// late is what makes the delta at-least-once: a step that fails to write the
// message leaves the cursor where it was, so the next step reads the same page
// rather than dropping it. Acknowledging inside the read would lose the text on
// exactly the failure the append can report.
type memberConclusionDelta struct {
	reader   conclusionDeltaReader
	memberID string

	// pending is the cursor position the last non-empty read is owed. The read
	// publishes it and the ack consumes it; atomic because the pair lives on the
	// agent, which does not promise the two halves run on one goroutine.
	pending atomic.Int64
}

// newMemberConclusionDelta pairs one reader with the member it reads for.
func newMemberConclusionDelta(reader conclusionDeltaReader, memberID string) *memberConclusionDelta {
	return &memberConclusionDelta{reader: reader, memberID: memberID}
}

// read is the pre-sampling half. Every failure path is a silent skip: a board
// that cannot answer must not fail the turn, and its error text must never
// reach the prompt.
func (d *memberConclusionDelta) read(ctx context.Context) (string, error) {
	if d == nil || nilutil.IsNil(d.reader) {
		return "", nil
	}
	text, advanceTo, err := d.reader.Read(ctx, d.memberID)
	if err != nil {
		return "", nil
	}
	if text == "" {
		// Nothing displayable in this page: the member's own posts were
		// filtered out, or every row on it was. The cursor still has to move,
		// or every later step re-reads the same page forever.
		if advanceTo > 0 {
			d.acknowledge(ctx, advanceTo)
		}
		return "", nil
	}
	d.pending.Store(advanceTo)
	return text, nil
}

// ack is the post-append half, run by the agent only once the text it returned
// reached the session. An acknowledgement that ran any earlier would move the
// cursor past a message that never got written.
func (d *memberConclusionDelta) ack() {
	if d == nil {
		return
	}
	advanceTo := d.pending.Swap(0)
	if advanceTo <= 0 {
		return
	}
	// The append already succeeded, so the cursor is bookkeeping, not delivery:
	// a bounded context keeps a stalled board from hanging the turn, and a
	// failed ack costs one duplicate delta rather than an error the model sees.
	ctx, cancel := context.WithTimeout(context.Background(), teamBoardTimeout)
	defer cancel()
	d.acknowledge(ctx, advanceTo)
}

func (d *memberConclusionDelta) acknowledge(ctx context.Context, advanceTo int64) {
	_ = d.reader.Ack(ctx, d.memberID, advanceTo)
}

// pair returns the two halves SetBoardDelta takes, so the agent owns the
// ordering between them.
func (d *memberConclusionDelta) pair() (agent.BoardDeltaFunc, func()) {
	return d.read, d.ack
}

// installConclusionDelta wires one member's pre-sampling board delta onto its
// executor. This is the whole member-side binding: it reports false and leaves
// the executor untouched when there is nothing to wire — no executor, no
// reader, or no member id.
//
// A leader must never reach this. Nothing but a leader's own read tool may put a
// conclusion into its context, so its executor keeps a nil hook and its
// requests stay byte-identical to a build with no board.
func installConclusionDelta(exec *agent.Agent, reader conclusionDeltaReader, memberID string) bool {
	if exec == nil || nilutil.IsNil(reader) || strings.TrimSpace(memberID) == "" {
		return false
	}
	delta := newMemberConclusionDelta(reader, memberID)
	exec.SetBoardDelta(delta.pair())
	return true
}

// installMemberConclusionDelta is the builder's one-line seam: it resolves the
// role and the executor, then defers the decision to installConclusionDelta. A
// leader is refused here rather than at the install, so the exclusion is a
// property of the assembly call graph and not of a convention at each call.
func installMemberConclusionDelta(deps memberBackendDeps, b team.MemberBinding, ctrl *control.Controller) bool {
	if b.Leader || ctrl == nil {
		return false
	}
	return installConclusionDelta(ctrl.Executor(), deps.conclusions, b.MemberID)
}
