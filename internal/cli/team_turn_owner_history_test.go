package cli

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/team"
)

// memberTurnDone builds the event one member's settled turn produces. Only a
// clean TurnDone publishes (a failed turn is a quota failover, not a committed
// append), so the sync tests drive the success shape.
func memberTurnDone() event.Event {
	return event.Event{Kind: event.TurnDone}
}

// TestTurnDonePublishesUnboundMemberOwnerHistory pins the background half of
// the cross-window contract: a member's turn commits to its own canonical owner
// whether or not the window is showing it, so a second window watching that
// owner must be able to notice the append. The window here shows lead while
// alice finishes a turn, which is exactly the case a bound-only publication
// would miss — and a missed publication is a peer stuck on a stale transcript.
func TestTurnDonePublishesUnboundMemberOwnerHistory(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.memberEvents = newMemberEventPump()
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return stubBackend{label: b.MemberID, stamp: b.MemberID + "-stem"}, nil
	}, 4)
	if _, err := m.teamBackends.bind(team.MemberBinding{Team: "alpha", MemberID: "alice"}); err != nil {
		t.Fatal(err)
	}
	m.switchTeamMember("lead")

	if got := ownerHistoryOf(t, m, "alpha", "alice"); got.Present {
		t.Fatalf("precondition: alice must start with no published identity, got %+v", got)
	}

	m.handleMemberEvent(memberEventMsg{member: "alice", ev: memberTurnDone()})
	m = waitForCockpit(t, m)

	got := ownerHistoryOf(t, m, "alpha", "alice")
	want := team.OwnerFingerprint{Present: true, Stem: "alice-stem", Generation: 1}
	if got != want {
		t.Fatalf("a background member's settled turn published %+v, want %+v", got, want)
	}
	// The window's own member is untouched: only the member whose turn settled
	// publishes, and lead's turn has not run.
	if got := ownerHistoryOf(t, m, "alpha", "lead"); got.Present {
		t.Fatalf("another member's turn published lead's history: %+v", got)
	}
}

// TestTurnDoneSkipsMemberWithoutHistoryIdentity pins the guard: a member whose
// backend cannot name its durable history is skipped rather than published with
// a made-up identity. Publishing "" would either create an owner directory for a
// member that never wrote one, or — worse — advance the generation of an owner
// whose content did not change, telling every peer to reload nothing.
func TestTurnDoneSkipsMemberWithoutHistoryIdentity(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.memberEvents = newMemberEventPump()
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return stubBackend{label: b.MemberID}, nil // no stamp: no readable identity yet
	}, 4)
	if _, err := m.teamBackends.bind(team.MemberBinding{Team: "alpha", MemberID: "alice"}); err != nil {
		t.Fatal(err)
	}
	m.switchTeamMember("lead")

	m.handleMemberEvent(memberEventMsg{member: "alice", ev: memberTurnDone()})
	m = waitForCockpit(t, m)

	if got := ownerHistoryOf(t, m, "alpha", "alice"); got.Present {
		t.Fatalf("a member with no history identity must not be published: %+v", got)
	}
}

// TestTurnDonePublishesBoundMemberExactlyOnce pins the count on the bound path:
// the bound member's settled turn advances its owner generation by exactly one.
// The bound member is the one whose controller this window holds, so it is the
// only member that could be published twice — once by the bound path and once by
// the registry path — and a double bump is not cosmetic: it makes the generation
// a lie about how many changes happened, which is what a peer compares against.
func TestTurnDonePublishesBoundMemberExactlyOnce(t *testing.T) {
	m, member, _ := ownerHistoryBoundTUI(t, true)
	before := ownerHistoryOf(t, m, "alpha", "lead")
	if !before.Present || before.Stem == "" {
		t.Fatalf("precondition: the bound member must have a published identity, got %+v", before)
	}
	stamp := member.HistoryStamp()
	if stamp == "" {
		t.Fatal("precondition: the bound member's live session must have a stamp")
	}

	m.handleMemberEvent(memberEventMsg{member: "lead", ev: memberTurnDone()})
	m = waitForCockpit(t, m)

	got := ownerHistoryOf(t, m, "alpha", "lead")
	if got.Generation != before.Generation+1 {
		t.Fatalf("one settled turn must advance the generation once: %+v -> %+v", before, got)
	}
	if got.Stem != stamp {
		t.Fatalf("the published stem = %q, want the member's current stamp %q", got.Stem, stamp)
	}
}
