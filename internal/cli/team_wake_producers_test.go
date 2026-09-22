package cli

// Producer acceptance tests for TEAM_LEADER_WAIT_ROUTE.md §5.3: the occurrences
// only the host knows about — an authorization request and undeliverable
// composer input — reach a waiting leader through the same bus.

import (
	"strings"
	"testing"
)

// TestEscalationRequestWakesAWaitingLeader pins the escalation producer: a
// member's out-of-scope write is queued for the leader's decision and reported
// in-process, so a leader already waiting sees the request in the result of its
// wait instead of at its next step.
func TestEscalationRequestWakesAWaitingLeader(t *testing.T) {
	svc, _, _, _ := escalationRig(t, true)
	bus := newWaitBus()
	defer bus.close()
	svc.setSignals(bus)
	waiter := bus.Subscribe("alpha")
	defer waiter.Close()

	if release := svc.begin("alpha", "coder", escalationFor("coder", "3", "/srv/data")); release == nil {
		t.Fatal("a team with a leader must queue the request")
	}
	got := receiveWaitEvent(t, waiter)
	if got.Kind != waitKindEscalation || got.ID != "coder:3" || got.Team != "alpha" {
		t.Fatalf("event = %+v, want the escalation naming its request id", got)
	}
	if !strings.Contains(got.Summary, "coder") || !strings.Contains(got.Summary, "coder:3") {
		t.Fatalf("summary = %q, want the member and the request the answer names", got.Summary)
	}
}

// TestComposerInputWakesAWaitingLeader pins the composer producer: the text a
// busy leader's window could not deliver is queued durably and reported, so the
// wait ends and the queue runs instead of the input waiting on a turn that has
// no reason to end.
func TestComposerInputWakesAWaitingLeader(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	closeBoardOn(t, m)
	m.teamBackends = newTeamBackends(nil, 0)
	bus := m.teamBackends.signals()
	waiter := bus.Subscribe("alpha")
	defer waiter.Close()

	m.signalTeamInput("run the tests again\nand again")
	got := receiveWaitEvent(t, waiter)
	if got.Kind != waitKindInput || got.Team != "alpha" || got.ID != "lead" {
		t.Fatalf("event = %+v, want the leader's own input", got)
	}
	if !strings.Contains(got.Summary, "run the tests again") || strings.Contains(got.Summary, "and again") {
		t.Fatalf("summary = %q, want the first composer line", got.Summary)
	}
}

// TestComposerInputOfANonLeaderStaysOffTheBus pins the scope: only the leader's
// window speaks for the team's wait. A member's window typing into its own turn
// must not wake the leader, whose wait is about the members it dispatched.
func TestComposerInputOfANonLeaderStaysOffTheBus(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	closeBoardOn(t, m)
	m.teamBackends = newTeamBackends(nil, 0)
	waiter := m.teamBackends.signals().Subscribe("alpha")
	defer waiter.Close()

	m.teamPick.session.current = "alice"
	m.signalTeamInput("hello")
	select {
	case ev := <-waiter.C():
		t.Fatalf("a member's input woke the leader's wait: %+v", ev)
	default:
	}
}

// TestComposerInputWithoutASessionStaysOffTheBus pins the other scope: a window
// with no team session has no leader to wake, and its composer is an ordinary
// chat turn.
func TestComposerInputWithoutASessionStaysOffTheBus(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	closeBoardOn(t, m)
	m.teamBackends = newTeamBackends(nil, 0)
	waiter := m.teamBackends.signals().Subscribe("alpha")
	defer waiter.Close()

	m.teamPick.session.active = false
	m.signalTeamInput("hello")
	select {
	case ev := <-waiter.C():
		t.Fatalf("a window with no session signalled the leader: %+v", ev)
	default:
	}
}
