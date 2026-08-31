package cli

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
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
	board   *team.SQLiteStore
	inboxes map[string]*agentruntime.BoardInbox
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

// openTeamInbox opens the board store under the team root. A missing or
// unreadable store returns nil: the team UI never depends on the board.
func openTeamInbox(cwd string) *teamInboxWire {
	root, err := team.TeamRoot(cwd)
	if err != nil {
		return nil
	}
	board, err := team.NewSQLiteStore(context.Background(), filepath.Join(root, "board.db"))
	if err != nil {
		return nil
	}
	return &teamInboxWire{board: board, inboxes: map[string]*agentruntime.BoardInbox{}}
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
	if w != nil {
		w.inboxes = map[string]*agentruntime.BoardInbox{}
	}
}

// inject folds the member's unread durable commands into text and
// acknowledges them (write-before-commit: a failed Ack leaves the watermark
// behind, so the batch replays on the next turn). The command block mirrors
// agentruntime.InjectTask's inbox link, so the model sees one format
// wherever the chain is assembled. Any board failure returns text unchanged.
func (w *teamInboxWire) inject(member, text string) string {
	if w == nil || w.board == nil {
		return text
	}
	inbox := w.inboxFor(member)
	if inbox == nil {
		return text
	}
	ctx, cancel := context.WithTimeout(context.Background(), teamBoardTimeout)
	defer cancel()
	items, next, err := inbox.Fetch(ctx, -1, teamInboxLimit)
	if err != nil || len(items) == 0 {
		return text
	}
	if err := inbox.Ack(ctx, next); err != nil {
		return text
	}
	var b strings.Builder
	b.WriteString("[command inbox] (generation " + strconv.FormatUint(items[0].Generation, 10) + ")\n")
	for _, item := range items {
		b.WriteString("[task: " + string(item.TaskID) + "] " + item.Summary + "\n")
	}
	b.WriteString("\n" + text)
	return b.String()
}

// inboxFor returns the member's board inbox, built once from the server's
// persisted BindRecord generation (§4.1): the server window is the gate, so
// a stale local window never drains commands it cannot answer for. An
// unbound member has no inbox and no injection.
func (w *teamInboxWire) inboxFor(member string) *agentruntime.BoardInbox {
	if existing, ok := w.inboxes[member]; ok {
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
	w.inboxes[member] = inbox
	return inbox
}

// consumeWakeups reads the board's leader-wakeup events since the leader's
// last cursor and returns their summaries, advancing the cursor with the
// read — a wakeup surfaces once. A leader with no cursor yet establishes
// one without replaying history, so the first open after a leader change is
// quiet.
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
