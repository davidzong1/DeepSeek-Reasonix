package cli

import (
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// countingHistoryBackend counts History() calls, so a test can prove where the
// read happened: the refresh path moved off the Update goroutine, and this is the
// only way to observe that the frame paid nothing for it.
type countingHistoryBackend struct {
	stubBackend
	reads *atomic.Int64
	hist  []provider.Message
}

func (b countingHistoryBackend) History() []provider.Message {
	b.reads.Add(1)
	return b.hist
}

// overlayWithCountingBackend binds members whose backend reads are counted.
func overlayWithCountingBackend(t *testing.T, history map[string][]provider.Message, reads *atomic.Int64) chatTUI {
	t.Helper()
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	m.memberEvents = newMemberEventPump()
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return countingHistoryBackend{
			stubBackend: stubBackend{label: b.MemberID, history: history[b.MemberID]},
			reads:       reads, hist: history[b.MemberID],
		}, nil
	}, 4)
	return m
}

// rosterResult runs a command the frame handed back and returns the first roster
// message it carries that want accepts. The frame's own command is where the
// result has to come from — rebuilding it from window state would let a result
// the frame forgets to hand back pass silently.
func rosterResult(t *testing.T, cmd tea.Cmd, want func(teamRosterRefreshMsg) bool) teamRosterRefreshMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("the frame must hand its off-loop work back to the loop")
	}
	// Nested batches are the normal shape here: refreshTeamRoster folds each
	// step with batchCmds, so a result can sit several levels down. A command is
	// run exactly once — running one twice would perform its work twice, which for
	// a history read is the very cost these tests are counting.
	var search func(c tea.Cmd, depth int) (teamRosterRefreshMsg, bool)
	search = func(c tea.Cmd, depth int) (teamRosterRefreshMsg, bool) {
		if c == nil || depth > 8 {
			return teamRosterRefreshMsg{}, false
		}
		switch msg := c().(type) {
		case teamRosterRefreshMsg:
			if want(msg) {
				return msg, true
			}
		case tea.BatchMsg:
			for _, child := range msg {
				if found, ok := search(child, depth+1); ok {
					return found, true
				}
			}
		}
		return teamRosterRefreshMsg{}, false
	}
	if tick, ok := search(cmd, 0); ok {
		return tick
	}
	t.Fatal("the frame's command carries no such roster result")
	return teamRosterRefreshMsg{}
}

// refreshHistory drives the real cross-window path — a landed durable-history
// reload — and returns the window and the command it handed back. Driving the
// entry point rather than replayBoundHistory is what pins the wiring: the refresh
// is reached through handleHistorySyncDone in production.
func refreshHistory(t *testing.T, m chatTUI, member, stamp string) (chatTUI, tea.Cmd) {
	t.Helper()
	session := &m.teamPick.session
	session.current = member
	session.syncInFlight = stamp
	next, cmd := m.update(teamRosterRefreshMsg{sync: &historySyncDone{member: member, stamp: stamp, reloaded: true}})
	return next.(chatTUI), cmd
}

// TestMemberReplayRefreshesHistoryOffTheUpdateGoroutine is the acceptance case for
// the deferred read: a peer's append repaints the window without the frame reading
// the member's history, and the read it handed back is what carries the history to
// the loop. A follower re-reads its durable source on every History() call, so this
// read is the stall the tick used to pay on every append.
func TestMemberReplayRefreshesHistoryOffTheUpdateGoroutine(t *testing.T) {
	var reads atomic.Int64
	m := overlayWithCountingBackend(t, map[string][]provider.Message{"lead": replayHistory(60)}, &reads)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.switchTeamMember("lead")
	bound := reads.Load()
	if bound == 0 {
		t.Fatal("precondition: the bind reads inline, because it must paint before it returns")
	}

	m, cmd := refreshHistory(t, m, "lead", "stamp-2")
	if got := reads.Load(); got != bound {
		t.Fatalf("the refresh read the member's history on the Update goroutine: reads went %d -> %d", bound, got)
	}

	tick := rosterResult(t, cmd, func(c teamRosterRefreshMsg) bool { return c.read != nil })
	if got := reads.Load(); got != bound+1 {
		t.Fatalf("the refresh's command must carry the read, reads = %d want %d", got, bound+1)
	}
	if len(tick.read.history) != 60 {
		t.Fatalf("the read carries %d messages, want 60", len(tick.read.history))
	}
	// Nothing was painted while the read was in flight: the transcript the refresh
	// replaces is still on screen, not a blank window.
	if joined := strings.Join(m.transcript, "\n"); !strings.Contains(joined, "HISTORY-MSG-59") {
		t.Fatalf("the refresh must leave the previous transcript in place until its read lands:\n%s", joined)
	}

	next, _ = m.update(tick)
	m = next.(chatTUI)
	if joined := strings.Join(m.transcript, "\n"); !strings.Contains(joined, "HISTORY-MSG-59") {
		t.Fatalf("the landed read must paint the member's history:\n%s", joined)
	}
	if m.replay.load == nil {
		t.Fatal("a long history must still leave the rest of the bundle rendering off the loop")
	}
}

// TestMemberReplayDropsAReadTheWindowMovedOnFrom pins the supersession guard on the
// read itself: a read that lands after the window bound somebody else must not
// paint the first member's history over the second one's transcript.
func TestMemberReplayDropsAReadTheWindowMovedOnFrom(t *testing.T) {
	var reads atomic.Int64
	m := overlayWithCountingBackend(t, map[string][]provider.Message{
		"lead":  replayHistory(60),
		"alice": {userMessage("ALICE")},
	}, &reads)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.switchTeamMember("lead")

	m, cmd := refreshHistory(t, m, "lead", "stamp-2")
	stale := rosterResult(t, cmd, func(c teamRosterRefreshMsg) bool { return c.read != nil })

	m.switchTeamMember("alice")
	if _, err := m.teamBackends.bind(mustBinding(t, m, "alice")); err != nil {
		t.Fatalf("bind alice: %v", err)
	}
	before := strings.Join(m.transcript, "\n")

	next, _ = m.update(stale)
	m = next.(chatTUI)
	after := strings.Join(m.transcript, "\n")
	if after != before {
		t.Fatalf("a superseded read must be dropped:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if strings.Contains(after, "HISTORY-MSG-00") || strings.Contains(after, "HISTORY-MSG-59") {
		t.Fatal("the previous member's history must not be painted onto the bound one")
	}
}

// TestRosterTickDoesNotMultiplyPerDeliveredResult pins the poll chain: only a
// genuine tick re-arms, so a result that rides the tick's message cannot leave a
// second loop running. Before the generation guard every delivered replay bundle
// and history sync armed one more chain, each paying the tick's registry read, so
// the overlay's poll rate grew with the number of history changes the team made.
func TestRosterTickDoesNotMultiplyPerDeliveredResult(t *testing.T) {
	m := overlayWithBackends(t, map[string][]provider.Message{"lead": {userMessage("x")}})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)

	rearms := func(name string, msg teamRosterRefreshMsg) bool {
		t.Helper()
		cmd := m.refreshTeamRoster(msg)
		if cmd == nil {
			return false
		}
		_, ok := cmd().(teamRosterRefreshMsg)
		if ok {
			return true
		}
		if batch, isBatch := cmd().(tea.BatchMsg); isBatch {
			for _, child := range batch {
				if child == nil {
					continue
				}
				if _, isTick := child().(teamRosterRefreshMsg); isTick {
					return true
				}
			}
		}
		t.Logf("%s: no tick in the command", name)
		return false
	}

	if !rearms("genuine tick", teamRosterRefreshMsg{tick: true, gen: m.rosterTickGen}) {
		t.Fatal("a genuine tick must continue the poll chain")
	}
	if rearms("replay result", teamRosterRefreshMsg{replay: &teamReplayReadyMsg{}}) {
		t.Fatal("a delivered replay result must not arm another poll chain")
	}
	if rearms("history sync result", teamRosterRefreshMsg{sync: &historySyncDone{}}) {
		t.Fatal("a delivered history sync must not arm another poll chain")
	}
	if rearms("stale tick", teamRosterRefreshMsg{tick: true, gen: m.rosterTickGen + 1}) {
		t.Fatal("a superseded chain's tick must be dropped, not re-armed")
	}
	// The chain a fresh session opens is the current generation.
	m.rosterTickGen = 0
	if !rearms("fresh chain", teamRosterRefreshMsg{tick: true, gen: 0}) {
		t.Fatal("the generation a session armed must stay live")
	}
}

// mustBinding resolves one member's binding for a test bind.
func mustBinding(t *testing.T, m chatTUI, member string) team.MemberBinding {
	t.Helper()
	b, err := m.teamPick.store.Binding(m.teamPick.sessionTeamName(), member)
	if err != nil {
		t.Fatalf("binding %s: %v", member, err)
	}
	return b
}
