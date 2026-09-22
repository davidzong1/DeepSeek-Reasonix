package cli

import (
	"fmt"
	"strings"

	"reasonix/internal/control"
	"reasonix/internal/sessioninbox"
)

// inboxPreview is a bounded UI row for the composer shelf (never full body).
type inboxPreview struct {
	ID      string
	Preview string
	State   sessioninbox.InboxState
	Intent  sessioninbox.InboxIntent
	Pos     int
}

func (m *chatTUI) inboxSnap() sessioninbox.InboxSnapshot {
	if m == nil || m.ctrl == nil {
		return sessioninbox.InboxSnapshot{}
	}
	return m.ctrl.InboxSnapshot()
}

func (m *chatTUI) inboxPreviews() []inboxPreview {
	snap := m.inboxSnap()
	out := make([]inboxPreview, 0, len(snap.Items))
	for i, it := range snap.Items {
		out = append(out, inboxPreview{
			ID:      it.ID,
			Preview: it.Preview,
			State:   it.State,
			Intent:  it.Intent,
			Pos:     i + 1,
		})
	}
	return out
}

func (m *chatTUI) inboxQueuedCount() int {
	return len(m.inboxSnap().Items)
}

// queueIndicatorRows is the terminal height the durable queue rows occupy above
// the composer. The frame budget has to carry them: while it did not, every
// queued follow-up grew the frame one row past the terminal and the last status
// row (Git + telemetry: ctx, cache, jobs) was written off-screen — the row an
// operator watches precisely while items are waiting, and the reason a queued
// team leader lost its context band.
//
// Team-agent: added for the team leader's telemetry band. On main-v2 this file
// has no queueIndicatorRows; merges keep this helper and the one call site in
// bottomRows (chat_tui.go).
func (m *chatTUI) queueIndicatorRows() int {
	qi := m.renderQueueIndicator()
	if qi == "" {
		return 0
	}
	return strings.Count(qi, "\n") + 1
}

// enqueueFollowup persists a follow-up and kicks dispatch if the controller is
// already idle. Only clears the composer on success.
//
// The durable write is also where a team leader's interruptible wait is told
// there is input: signalling here rather than at the call sites is what makes
// every entry point carry it — the mid-turn composer, the idle-looking window
// whose backend still runs (prepareControllerTurn), and /queue's own paths. A
// signal at the call sites covered only the ones that had been remembered, so a
// leader blocked in leader_wait slept through input that was already queued.
func (m *chatTUI) enqueueFollowup(display, submit string) (sessioninbox.InboxReceipt, error) {
	if m.ctrl == nil {
		return sessioninbox.InboxReceipt{}, sessioninbox.ErrClosed
	}
	if ensurer, ok := m.ctrl.(interface{ EnsureSessionPath() }); ok {
		ensurer.EnsureSessionPath()
	}
	rec, err := m.ctrl.TryEnqueueFollowup(control.InboxRequest{
		Intent:  sessioninbox.IntentFollowup,
		Display: display,
		Raw:     submit,
		Submit:  submit,
		Source:  "cli",
	})
	if err != nil {
		return rec, err
	}
	m.signalTeamInput(submit)
	return rec, nil
}

// enqueueSteer persists then attempts mid-turn steer.
//
// Signalled like the follow-up above: /steer is the composer's own "insert into
// the running turn" gesture, so a leader waiting on the team must be released by
// it exactly as Ctrl+Enter releases it. Whether the steer is admitted mid-turn
// or falls back to a durable follow-up is the controller's call; either way the
// input is now durable and the leader's wait is what has to end for it to run.
func (m *chatTUI) enqueueSteer(display, submit string) (sessioninbox.InboxReceipt, error) {
	if m.ctrl == nil {
		return sessioninbox.InboxReceipt{}, sessioninbox.ErrClosed
	}
	if ensurer, ok := m.ctrl.(interface{ EnsureSessionPath() }); ok {
		ensurer.EnsureSessionPath()
	}
	rec, err := m.ctrl.TryEnqueueAndSteer(control.InboxRequest{
		Intent:  sessioninbox.IntentSteer,
		Display: display,
		Raw:     submit,
		Submit:  submit,
		Source:  "cli",
	})
	if err != nil {
		return rec, err
	}
	m.signalTeamInput(submit)
	return rec, nil
}

// seedInbox is a test helper to push durable queue rows.
func (m *chatTUI) seedInbox(texts ...string) {
	for _, text := range texts {
		_, _ = m.enqueueFollowup(text, text)
	}
}

// inboxBodies returns full submit texts in queue order (tests only).
func (m *chatTUI) inboxBodies() []string {
	snap := m.inboxSnap()
	out := make([]string, 0, len(snap.Items))
	for _, it := range snap.Items {
		_, env, err := m.ctrl.ReadInboxItem(it.ID)
		if err != nil {
			out = append(out, it.Preview)
			continue
		}
		out = append(out, env.SubmitText)
	}
	return out
}

// handleQueueSlash runs /queue and /steer as local commands even while running.
func (m *chatTUI) handleQueueSlash(line string) (handled bool, notice string) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false, ""
	}
	cmd := strings.ToLower(fields[0])
	args := fields[1:]
	switch cmd {
	case "/steer":
		text := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
		if text == "" {
			return true, "usage: /steer <guidance>"
		}
		body := m.expandPastedBlocks(text)
		rec, err := m.enqueueSteer(body, body)
		if err != nil {
			return true, "steer: " + err.Error()
		}
		switch rec.Disposition {
		case sessioninbox.DispositionSteerAccepted:
			return true, fmt.Sprintf("steer accepted #%s", shortID(rec.ItemID))
		case sessioninbox.DispositionQueuedFollowup:
			return true, fmt.Sprintf("steer rejected — queued as follow-up #%s", shortID(rec.ItemID))
		default:
			return true, fmt.Sprintf("queued #%s (%s)", shortID(rec.ItemID), rec.Disposition)
		}
	case "/queue":
		return true, m.runQueueCommand(args)
	default:
		return false, ""
	}
}

func (m *chatTUI) runQueueCommand(args []string) string {
	if len(args) == 0 {
		args = []string{"list"}
	}
	sub := strings.ToLower(args[0])
	rest := args[1:]
	switch sub {
	case "list", "ls", "status":
		return m.renderQueueList()
	case "show":
		id, err := m.resolveQueueRef(rest)
		if err != nil {
			return err.Error()
		}
		_, env, err := m.ctrl.ReadInboxItem(id)
		if err != nil {
			return "show: " + err.Error()
		}
		return env.SubmitText
	case "edit":
		return m.editQueueItem(rest)
	case "delete", "rm", "del":
		id, err := m.resolveQueueRef(rest)
		if err != nil {
			return err.Error()
		}
		if err := m.ctrl.DeleteInboxItem(id); err != nil {
			return "delete: " + err.Error()
		}
		return "deleted #" + shortID(id)
	case "move":
		return m.moveQueueItem(rest)
	case "pause":
		if err := m.ctrl.SetInboxPaused(true); err != nil {
			return "pause: " + err.Error()
		}
		return "inbox paused"
	case "resume":
		if err := m.ctrl.SetInboxPaused(false); err != nil {
			return "resume: " + err.Error()
		}
		return "inbox resumed"
	case "retry":
		id, err := m.resolveQueueRef(rest)
		if err != nil {
			return err.Error()
		}
		if err := m.ctrl.RetryInboxItem(id); err != nil {
			return "retry: " + err.Error()
		}
		return "retry queued #" + shortID(id)
	case "refresh":
		id, err := m.resolveQueueRef(rest)
		if err != nil {
			return err.Error()
		}
		if err := m.ctrl.RefreshInboxReferences(id); err != nil {
			return "refresh: " + err.Error()
		}
		return "refs refreshed #" + shortID(id)
	default:
		return "usage: /queue list|show|edit|delete|move|pause|resume|retry|refresh"
	}
}

func (m *chatTUI) renderQueueList() string {
	snap := m.inboxSnap()
	if len(snap.Items) == 0 {
		status := "inbox empty"
		if snap.Paused {
			status += " (paused)"
		}
		return status
	}
	var b strings.Builder
	fmt.Fprintf(&b, "inbox rev=%d items=%d", snap.Revision, len(snap.Items))
	if snap.Paused {
		b.WriteString(" paused")
	}
	if snap.Recovered {
		fmt.Fprintf(&b, " recovered=%d", snap.RecoveredN)
	}
	b.WriteByte('\n')
	limit := min(len(snap.Items), 20)
	for i := range limit {
		it := snap.Items[i]
		fmt.Fprintf(&b, "  %d. [%s/%s] %s #%s\n", i+1, it.Intent, it.State, it.Preview, shortID(it.ID))
	}
	if len(snap.Items) > limit {
		fmt.Fprintf(&b, "  … and %d more (use /queue show <n>)\n", len(snap.Items)-limit)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *chatTUI) editQueueItem(args []string) string {
	if len(args) < 2 {
		return "usage: /queue edit <n|id> <text>"
	}
	id, err := m.resolveQueueRef(args[:1])
	if err != nil {
		return err.Error()
	}
	text := strings.Join(args[1:], " ")
	if _, err := m.ctrl.UpdateInboxItem(id, text, text, text); err != nil {
		return "edit: " + err.Error()
	}
	return "updated #" + shortID(id)
}

func (m *chatTUI) moveQueueItem(args []string) string {
	if len(args) < 2 {
		return "usage: /queue move <n|id> <to-index>"
	}
	id, err := m.resolveQueueRef(args[:1])
	if err != nil {
		return err.Error()
	}
	var to int
	if _, err := fmt.Sscanf(args[1], "%d", &to); err != nil {
		return "move: bad index"
	}
	if err := m.ctrl.MoveInboxItem(id, to-1); err != nil {
		return "move: " + err.Error()
	}
	return "moved #" + shortID(id)
}

func (m *chatTUI) resolveQueueRef(args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("missing item ref (index or id)")
	}
	ref := args[0]
	snap := m.inboxSnap()
	// Numeric 1-based index.
	var n int
	if _, err := fmt.Sscanf(ref, "%d", &n); err == nil && n >= 1 && n <= len(snap.Items) {
		return snap.Items[n-1].ID, nil
	}
	// Full or short id prefix.
	for _, it := range snap.Items {
		if it.ID == ref || strings.HasPrefix(it.ID, ref) {
			return it.ID, nil
		}
	}
	return "", fmt.Errorf("unknown inbox item %q", ref)
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// handleQueueReorder moves the selected item by delta (-1 up, +1 down).
func (m *chatTUI) handleQueueReorder(delta int) bool {
	if m.queueEditCursor < 0 || m.inboxSelectedID == "" || m.ctrl == nil {
		return false
	}
	to := m.queueEditCursor + delta
	if to < 0 {
		return false
	}
	if err := m.ctrl.MoveInboxItem(m.inboxSelectedID, to); err != nil {
		m.notice("move: " + err.Error())
		return true
	}
	m.queueEditCursor = to
	return true
}
