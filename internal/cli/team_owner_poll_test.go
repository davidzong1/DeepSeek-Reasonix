package cli

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/provider"
)

// TestRosterTickReadsTheOwnerFingerprintOnce is the guard for the tick's shared
// owner read. Two consumers need the same fingerprint — the ambient usage channel
// and the cross-window history poll — and the tick resolves it once, after the
// roster settled.
//
// Both halves are the same defect. Reading it inside each consumer is wasteful,
// but worse: the roster settles between them, so the second consumer would have
// read the fingerprint of a member the tick just rebound away from and compared
// it against the incoming member's recorded stamp — reading its own rebind as a
// remote history change. The recorded member id is therefore part of the
// assertion, not just the count.
func TestRosterTickReadsTheOwnerFingerprintOnce(t *testing.T) {
	previous := ownerFingerprintReadHook
	t.Cleanup(func() { ownerFingerprintReadHook = previous })

	m := overlayWithBackends(t, map[string][]provider.Message{
		"lead":  {userMessage("LEAD")},
		"alice": {userMessage("ALICE")},
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.switchTeamMember("lead")

	var seen []string
	ownerFingerprintReadHook = func() { seen = append(seen, m.teamPick.session.current) }

	// A plain tick: one read, naming the bound member.
	next, _ = m.Update(teamRosterRefreshMsg{tick: true, gen: m.rosterTickGen})
	m = next.(chatTUI)
	if len(seen) != 1 {
		t.Fatalf("a tick read the owner fingerprint %d times, want 1 (named %v)", len(seen), seen)
	}
	if want := m.teamPick.session.current; seen[0] != want {
		t.Fatalf("the tick's owner read named %q, want the bound member %q", seen[0], want)
	}

	// A member removed remotely: the tick rebinds to the leader, and the one read
	// must name the member now bound — never the one left behind.
	session := &m.teamPick.session
	session.current, session.members = "ghost", []string{"ghost", "alice"}
	seen = nil
	next, _ = m.Update(teamRosterRefreshMsg{tick: true, gen: m.rosterTickGen})
	m = next.(chatTUI)
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("the tick must rebind the removed member to the leader, current = %q", got)
	}
	if len(seen) != 1 {
		t.Fatalf("the rebinding tick read the owner fingerprint %d times, want 1 (named %v)", len(seen), seen)
	}
	if seen[0] != "lead" {
		t.Fatalf("the tick's owner read named %q: it read the member it was rebinding away from", seen[0])
	}
}
