package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"reasonix/internal/team"
	"reasonix/internal/team/agentruntime"
)

// teamInboxWire is the cli side of the durable command chain (§5.1): the
// board store opened with the team overlay, the bound member's unread
// leader commands riding the next submitted turn (§7), and leader wakeups
// surfacing as notices. Board access is optional — a missing or unreadable
// store disables injection, never the team UI.
type teamInboxWire struct {
	board *team.SQLiteStore
	// mu guards the caches below. They are touched from the update loop and
	// from prefetch goroutines, so every access goes through it.
	mu      sync.Mutex
	inboxes map[string]*agentruntime.BoardInbox
	// prefetched holds each member's read-ahead batch, and fetching marks the
	// members whose read is already in flight, so a burst of turns cannot pile
	// up duplicate fetches.
	prefetched map[string]teamInboxBatch
	fetching   map[string]bool
	// acks counts each member's successful acknowledgements; a read-ahead queued
	// under an older count describes commands already delivered, so it is dropped
	// rather than injected twice.
	acks map[string]int64
	// wakes owns the leader's board wakeup cursor. It is per board, like the
	// store, so a reopened overlay and the registry share one cursor owner
	// instead of advancing it twice (team_wake_dispatcher.go).
	wakes *teamWakeDispatcher
}

// teamInboxBatch is one member's read-ahead command batch: the items to fold
// into the next turn, and the watermark that acknowledges them once they are.
// ackEpoch is the member's acknowledgement count when the batch was queued: a
// batch whose epoch no longer matches was overtaken by an acknowledgement, so
// these commands are already on their way to the member and must not ride a
// second turn.
type teamInboxBatch struct {
	items    []agentruntime.InboxItem
	next     int64
	ackEpoch int64
}

func (p *teamPicker) boardStore() *team.SQLiteStore {
	if p == nil || p.board == nil {
		return nil
	}
	return p.board.board
}

// teamInboxLimit bounds one turn's injected commands; the rest wait for the
// next turn. teamBoardTimeout keeps a stalled board from blocking the turn.
const (
	teamInboxLimit     = 8
	teamBoardTimeout   = 2 * time.Second
	teamInboxWakeLimit = 16
)

// openTeamInbox opens the board store under a team data dir — a project's
// .reasonix/team or the user-global <state root>/team. A missing or
// unreadable store returns nil: the team UI never depends on the board.
func openTeamInbox(dir string) *teamInboxWire {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil
	}
	board, err := team.NewSQLiteStore(context.Background(), filepath.Join(dir, "board.db"))
	if err != nil {
		return nil
	}
	w := &teamInboxWire{board: board, inboxes: map[string]*agentruntime.BoardInbox{}, prefetched: map[string]teamInboxBatch{}}
	w.wakes = &teamWakeDispatcher{wire: w}
	return w
}

// wakeDispatcher returns this board's wake dispatcher, or nil when there is no
// board at all — a wire the host could not open. Nil receiver included: the
// caller's board may itself be nil, and a field selection on it would panic
// where a method call on the nil wire does not.
func (w *teamInboxWire) wakeDispatcher() *teamWakeDispatcher {
	if w == nil {
		return nil
	}
	return w.wakes
}

// attachSignals hands the registry's wait bus to this board's wake dispatcher.
// A wire with no registry — a host or test that opened its own — keeps draining
// for the window's notices, with nothing to publish to.
func (w *teamInboxWire) attachSignals(sig *waitBus) {
	if w == nil || w.wakes == nil {
		return
	}
	w.wakes.setSignals(sig)
}

// close releases the board store. Only the process-level teardown may call it:
// the task service and every assembled member backend hold this store, and both
// outlive the overlay, so closing it on exitTeam left them reading a closed
// database — permanently, because the registry is built once.
func (w *teamInboxWire) close() {
	if w != nil && w.board != nil {
		_ = w.board.Close()
	}
}

// resetInboxes drops the per-member inbox cache. Each inbox pins the member's
// BindRecord generation from when it was built, so a freshly opened overlay must
// re-read it; the store itself stays open for its longer-lived holders.
func (w *teamInboxWire) resetInboxes() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// The prefetch goes with the inboxes: a batch read under the previous
	// generation must not ride a turn bound to a new one.
	w.inboxes = map[string]*agentruntime.BoardInbox{}
	w.prefetched = map[string]teamInboxBatch{}
	w.acks = map[string]int64{}
}

// inject folds the member's unread durable commands into text and
// acknowledges them (write-before-commit: a failed Ack leaves the watermark
// behind, so the batch replays on the next turn). The command block mirrors
// agentruntime.InjectTask's inbox link, so the model sees one format
// wherever the chain is assembled. Any board failure returns text unchanged.
//
// The read is normally served from prefetch, so a submit — a keystroke path —
// does no board read at all; only a turn the read-ahead did not cover falls
// back to reading inline.
func (w *teamInboxWire) inject(member, text string) string {
	if w == nil || w.board == nil {
		return text
	}
	inbox := w.inboxFor(member)
	if inbox == nil {
		return text
	}
	batch, ok := w.takePrefetched(member)
	if !ok {
		if batch, ok = fetchTeamInbox(inbox); !ok {
			return text
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), teamBoardTimeout)
	defer cancel()
	if err := inbox.Ack(ctx, batch.next); err != nil {
		return text
	}
	// The acknowledgement is recorded before the next read-ahead starts: it is
	// what stops a read that began earlier from queueing these same commands.
	w.noteAck(member)
	w.prefetch(member)
	return commandInboxText(batch.items, text)
}

// fetchTeamInbox reads one batch synchronously. It is the historical path,
// kept for the turns a prefetch did not cover.
func fetchTeamInbox(inbox *agentruntime.BoardInbox) (teamInboxBatch, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), teamBoardTimeout)
	defer cancel()
	items, next, err := inbox.Fetch(ctx, -1, teamInboxLimit)
	if err != nil || len(items) == 0 {
		return teamInboxBatch{}, false
	}
	return teamInboxBatch{items: items, next: next}, true
}

// commandInboxText renders one batch as the block folded into the turn text.
func commandInboxText(items []agentruntime.InboxItem, text string) string {
	var b strings.Builder
	b.WriteString("[command inbox] (generation " + strconv.FormatUint(items[0].Generation, 10) + ")\n")
	for _, item := range items {
		b.WriteString("[task: " + string(item.TaskID) + "] " + item.Summary + "\n")
	}
	b.WriteString("\n" + text)
	return b.String()
}

// prefetch reads the member's unread commands ahead of the next submit, so the
// turn path usually does no board read at all. It never blocks its caller — the
// read rides its own goroutine — and one in-flight read per member is enough,
// so turns arriving faster than the board cannot pile up fetches.
func (w *teamInboxWire) prefetch(member string) {
	if w == nil || w.board == nil || member == "" {
		return
	}
	w.mu.Lock()
	_, ready := w.prefetched[member]
	skip := ready || w.fetching[member]
	// The acknowledgement epoch is captured when the read is QUEUED, not when it
	// is filed: an acknowledgement landing mid-read consumes the commands this
	// batch is about to return, so it must be stamped with the older count.
	epoch := w.acks[member]
	if !skip {
		if w.fetching == nil {
			w.fetching = map[string]bool{}
		}
		w.fetching[member] = true
	}
	w.mu.Unlock()
	if skip {
		return
	}
	go func() {
		defer w.endPrefetch(member)
		inbox := w.inboxFor(member)
		if inbox == nil {
			return
		}
		batch, ok := fetchTeamInbox(inbox)
		if ok {
			batch.ackEpoch = epoch
			w.storePrefetched(member, inbox, batch)
		}
	}()
}

// noteAck records one successful acknowledgement, which invalidates every
// read-ahead that started before it.
func (w *teamInboxWire) noteAck(member string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.acks == nil {
		w.acks = map[string]int64{}
	}
	w.acks[member]++
}

// endPrefetch clears the in-flight marker so a later turn can refill.
func (w *teamInboxWire) endPrefetch(member string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.fetching, member)
}

// storePrefetched files one read-ahead batch unless it lost its meaning while
// the read ran: its inbox was replaced — an overlay reopen resets the cache, so
// a batch read under the old bindings must not ride a turn bound to new ones.
// The batch already carries the acknowledgement epoch it was queued under
// (prefetch), which is what takePrefetched re-checks: a read that raced an
// acknowledgement describes commands that acknowledgement already consumed.
func (w *teamInboxWire) storePrefetched(member string, inbox *agentruntime.BoardInbox, batch teamInboxBatch) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if current, ok := w.inboxes[member]; !ok || current != inbox {
		return
	}
	if w.prefetched == nil {
		w.prefetched = map[string]teamInboxBatch{}
	}
	w.prefetched[member] = batch
}

// takePrefetched consumes the member's read-ahead batch, if one is waiting and
// still current: a batch queued before the last acknowledgement describes
// commands that acknowledgement already delivered, so it is dropped and the
// caller falls back to reading the board.
func (w *teamInboxWire) takePrefetched(member string) (teamInboxBatch, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	batch, ok := w.prefetched[member]
	if !ok {
		return teamInboxBatch{}, false
	}
	delete(w.prefetched, member)
	if batch.ackEpoch != w.acks[member] {
		return teamInboxBatch{}, false
	}
	return batch, true
}

// inboxFor returns the member's board inbox, built once from the server's
// persisted BindRecord generation (§4.1): the server window is the gate, so
// a stale local window never drains commands it cannot answer for. An
// unbound member has no inbox and no injection.
func (w *teamInboxWire) inboxFor(member string) *agentruntime.BoardInbox {
	w.mu.Lock()
	existing, ok := w.inboxes[member]
	w.mu.Unlock()
	if ok {
		return existing
	}
	ctx, cancel := context.WithTimeout(context.Background(), teamBoardTimeout)
	defer cancel()
	records, err := w.board.LoadBindings(ctx)
	if err != nil {
		return nil
	}
	generation, found := uint64(0), false
	for _, rec := range records {
		if rec.MemberID == member {
			generation, found = rec.Generation, true
			break
		}
	}
	if !found {
		return nil
	}
	inbox := agentruntime.NewBoardInbox(w.board, team.BoardShared, member, generation)
	w.mu.Lock()
	defer w.mu.Unlock()
	if again, ok := w.inboxes[member]; ok {
		return again // a concurrent caller won the build; keep one inbox per member
	}
	if w.inboxes == nil {
		w.inboxes = map[string]*agentruntime.BoardInbox{}
	}
	w.inboxes[member] = inbox
	return inbox
}

// consumeWakeups reads the board's leader-wakeup events since the leader's
// last cursor and returns their summaries, advancing the cursor with the
// read — a wakeup surfaces once. A leader with no cursor yet establishes
// one without replaying history, so the first open after a leader change is
// quiet.
//
// It is the wake dispatcher's read primitive, never a consumer's: two readers
// of one cursor are one reader too many, and the second would find the page the
// first advanced past. Consumers call teamWakeDispatcher.drain.
func (w *teamInboxWire) consumeWakeups(leader string) []string {
	if w == nil || w.board == nil || leader == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), teamBoardTimeout)
	defer cancel()
	pos, err := w.board.GetCursor(ctx, team.BoardShared, leader)
	if err != nil {
		_ = w.board.AdvanceCursor(ctx, team.CursorUpdate{
			BoardID: team.BoardShared, ConsumerID: leader, LastSeq: 0,
		})
		return nil
	}
	page, err := w.board.ReadAfter(ctx, team.BoardShared, pos.LastSeq, team.Filter{
		Kind:    team.EventWakeup,
		Limit:   teamInboxWakeLimit,
		Stamped: team.Identity{MemberID: leader},
	})
	if err != nil || len(page.Events) == 0 {
		return nil
	}
	reasons := make([]string, 0, len(page.Events))
	last := pos.LastSeq
	for _, ev := range page.Events {
		reasons = append(reasons, ev.Summary)
		if ev.Seq > last {
			last = ev.Seq
		}
	}
	if err := w.board.AdvanceCursor(ctx, team.CursorUpdate{
		BoardID: team.BoardShared, ConsumerID: leader, Generation: pos.Generation, LastSeq: last,
	}); err != nil {
		return nil
	}
	return reasons
}

// injectTeamTurn folds the bound member's unread durable commands into the
// model input of the next turn (§5.1/§7): the inbox rides the turn, never
// the composer text or the event stream, and only the bound member's own
// commands reach its context. Non-member turns pass through untouched.
func (m *chatTUI) injectTeamTurn(text string) string {
	if m.teamPick == nil || !m.teamPick.session.active || m.teamPick.board == nil {
		return text
	}
	return m.teamPick.board.inject(m.teamPick.session.current, text)
}
