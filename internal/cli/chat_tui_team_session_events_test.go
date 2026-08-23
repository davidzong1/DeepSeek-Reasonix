package cli

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/provider"
	"reasonix/internal/team"
	"reasonix/internal/team/agentruntime"
)

// cliFakeProvider is a scripted provider for assembling a real member runtime
// in cli tests: Stream returns one text chunk and closes.
type cliFakeProvider struct{}

func (cliFakeProvider) Name() string { return "fake" }

func (cliFakeProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 1)
	ch <- provider.Chunk{Type: provider.ChunkText, Text: "ok"}
	close(ch)
	return ch, nil
}

// fakeSubscription is a scripted subscription handle for event routing tests.
func fakeSubscription() agentruntime.Subscription {
	return agentruntime.Subscription{C: make(chan agentruntime.RuntimeEvent, 8), Cancel: func() {}}
}

// sessionEvent wraps one runtime event as the message the update loop sees.
func sessionEvent(team, member string, seq uint64, kind agentruntime.RuntimeEventKind, text string) teamRuntimeEventMsg {
	return teamRuntimeEventMsg{sub: fakeSubscription(), event: agentruntime.RuntimeEvent{
		Team: team, MemberID: member, Sequence: seq, Kind: kind, Text: text,
	}}
}

// TestTeamSessionEventRefreshesHistory pins §11.5: a message event for the
// current member leaves the window rendering the freshly persisted history —
// the render re-reads the store on every frame.
func TestTeamSessionEventRefreshesHistory(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})

	// A message event arrives with the assistant text already persisted.
	if err := m.teamPick.sessions.AppendMessage("alpha", "lead", team.SessionMessage{
		Kind: "agent", Text: "assistant reply", TS: "2026-08-22T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(sessionEvent("alpha", "lead", 1, agentruntime.EventMessage, "assistant reply"))
	m = next.(chatTUI)
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "assistant reply") {
		t.Fatalf("the session window should show the new history, got:\n%s", got)
	}
}

// TestTeamSessionEventNonCurrentCountsUnread pins §11.5: terminal events from
// a non-current member count as unread on the roster column, a delta never
// does, and switching to that member consumes the count.
func TestTeamSessionEventNonCurrentCountsUnread(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: 't'}) // window on lead

	next, _ := m.Update(sessionEvent("alpha", "alice", 1, agentruntime.EventDelta, "par"))
	m = next.(chatTUI)
	if got := m.teamPick.session.unread["alice"]; got != 0 {
		t.Fatalf("a delta must not count as unread, got %d", got)
	}
	next, _ = m.Update(sessionEvent("alpha", "alice", 2, agentruntime.EventMessage, "full reply"))
	m = next.(chatTUI)
	if got := m.teamPick.session.unread["alice"]; got != 1 {
		t.Fatalf("a message event should count one unread, got %d", got)
	}
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "alice 1") {
		t.Fatalf("the roster column should show the unread count, got:\n%s", got)
	}

	// Switching to alice consumes the count and shows her history.
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.teamPick.session.current; got != "alice" {
		t.Fatalf("down should switch to alice, got %q", got)
	}
	if got := m.teamPick.session.unread["alice"]; got != 0 {
		t.Fatalf("switching should consume the unread count, got %d", got)
	}
}

// TestTeamSessionEventErrorSetsSessionError pins §11.5: an error event for
// the current member surfaces in the session window, and a following message
// clears it.
func TestTeamSessionEventErrorSetsSessionError(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})

	next, _ := m.Update(sessionEvent("alpha", "lead", 1, agentruntime.EventError, "authentication failed"))
	m = next.(chatTUI)
	if got := m.teamPick.session.errMsg; got != "authentication failed" {
		t.Fatalf("the error event should set the session error, got %q", got)
	}
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "authentication failed") {
		t.Fatalf("the session window should render the error, got:\n%s", got)
	}

	next, _ = m.Update(sessionEvent("alpha", "lead", 2, agentruntime.EventMessage, "ok"))
	m = next.(chatTUI)
	if got := m.teamPick.session.errMsg; got != "" {
		t.Fatalf("a message event should clear the session error, got %q", got)
	}
}

// TestTeamSessionEventStaleDropped pins §11.5: events from a closed stream
// (zero event), a foreign team, or an unknown member are dropped — they never
// count unread, set errors, or crash the loop.
func TestTeamSessionEventStaleDropped(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})

	for _, ev := range []teamRuntimeEventMsg{
		{sub: fakeSubscription(), event: agentruntime.RuntimeEvent{}},         // closed stream
		sessionEvent("other-team", "lead", 1, agentruntime.EventMessage, "x"), // foreign team
		sessionEvent("alpha", "ghost", 1, agentruntime.EventMessage, "x"),     // unknown member
	} {
		next, _ := m.Update(ev)
		m = next.(chatTUI)
	}
	if got := m.teamPick.session.unread["ghost"]; got != 0 {
		t.Fatalf("a foreign member must not count unread, got %d", got)
	}
	if got := m.teamPick.session.errMsg; got != "" {
		t.Fatalf("stale events must not set errors, got %q", got)
	}
}

// TestTeamSessionEventAfterCloseDropped pins the teardown boundary: an event
// delivered after the session window closed is dropped without touching the
// roster or the composer.
func TestTeamSessionEventAfterCloseDropped(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	m.input.SetValue("chat draft")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc}) // close the session

	next, _ := m.Update(sessionEvent("alpha", "alice", 1, agentruntime.EventMessage, "late"))
	m = next.(chatTUI)
	if m.teamPick.session.active {
		t.Fatal("a late event must not reopen the session")
	}
	if got := m.input.Value(); got != "chat draft" {
		t.Fatalf("a late event must not touch the composer: %q", got)
	}
}

// TestTeamSessionRenderShowsComposer pins §11.4 rendering: the focused
// composer renders its draft with the send hint, the browsing state renders
// the compose hint instead.
func TestTeamSessionRenderShowsComposer(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})

	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "Enter compose") {
		t.Fatalf("browsing should render the compose hint, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // focus the composer
	m = typeTeamName(m, "hello")
	got := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"hello", "Enter send", "Shift+Enter newline"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the composer should render %q, got:\n%s", want, got)
		}
	}
}

// TestTeamSessionSubscribeRebindOnSwitch pins the §11.5 rebind: entering the
// session subscribes the leader's stream; switching members cancels the old
// subscription (its channel closes) and arms the target's.
func TestTeamSessionSubscribeRebindOnSwitch(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m.teamPick.runtime = agentruntime.NewRegistry(m.teamPick.sessions,
		func(spec agentruntime.Spec) (provider.Provider, error) { return cliFakeProvider{}, nil })
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: 't'}) // subscribes lead

	first := m.teamPick.session.sub
	if first == nil {
		t.Fatal("entering the session should subscribe the leader's stream")
	}
	old := *first
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // switch to alice: rebind
	second := m.teamPick.session.sub
	if second == nil {
		t.Fatal("switching should subscribe the target's stream")
	}
	if _, ok := <-old.C; ok {
		t.Fatal("the old subscription must be cancelled on switch")
	}
}

// TestTeamSessionCloseCancelsSubscription pins the §11.5 teardown: closing
// the session cancels the live subscription so no goroutine or message leaks
// past the overlay.
func TestTeamSessionCloseCancelsSubscription(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m.teamPick.runtime = agentruntime.NewRegistry(m.teamPick.sessions,
		func(spec agentruntime.Spec) (provider.Provider, error) { return cliFakeProvider{}, nil })
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})

	sub := *m.teamPick.session.sub
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc}) // close the session
	if _, ok := <-sub.C; ok {
		t.Fatal("closing the session must cancel the subscription")
	}
	if m.teamPick.session.sub != nil {
		t.Fatal("closing the session must drop the subscription handle")
	}
}
