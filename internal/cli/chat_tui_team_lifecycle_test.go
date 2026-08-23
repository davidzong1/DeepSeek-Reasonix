package cli

import (
	"context"
	"errors"
	"os"
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/provider"
	"reasonix/internal/team"
	"reasonix/internal/team/agentruntime"
)

// sessionRuntimeKey is the instance key of a member inside the session window.
func sessionRuntimeKey(teamName, memberID string) agentruntime.InstanceKey {
	return agentruntime.InstanceKey{Team: teamName, MemberID: memberID}
}

// TestTeamSessionCloseStopsTeamRuntimes pins the §11.6 Esc semantics: closing
// the session window stops every member runtime the window started — the
// current member and any switched-to target — so no loop keeps writing
// context past the window. Re-entering the session restarts the target
// (stopped → running, the P4.4 idempotent Start).
func TestTeamSessionCloseStopsTeamRuntimes(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m.teamPick.runtime = agentruntime.NewRegistry(m.teamPick.sessions,
		func(spec agentruntime.Spec) (provider.Provider, error) { return cliFakeProvider{}, nil })
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // focus lead (leader)
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})         // session on lead: starts lead
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // switch to alice: starts alice
	rt := m.teamPick.runtime
	for _, id := range []string{"lead", "alice"} {
		st, err := rt.Status(sessionRuntimeKey("alpha", id))
		if err != nil || st != agentruntime.RuntimeStateRunning {
			t.Fatalf("%s before close = %v, %v; want running", id, st, err)
		}
	}

	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc}) // close the session
	for _, id := range []string{"lead", "alice"} {
		st, err := rt.Status(sessionRuntimeKey("alpha", id))
		if err != nil || st != agentruntime.RuntimeStateStopped {
			t.Fatalf("%s after close = %v, %v; want stopped", id, st, err)
		}
	}

	m = teamKey(m, tea.KeyPressMsg{Code: 't'}) // re-enter the session
	st, err := rt.Status(sessionRuntimeKey("alpha", "lead"))
	if err != nil || st != agentruntime.RuntimeStateRunning {
		t.Fatalf("lead after re-entry = %v, %v; want running", st, err)
	}
}

// TestTeamSessionRestartRetriesAssembly pins the §11.6 r retry entry: a start
// that failed at provider assembly leaves no instance; r stops (tolerating an
// absent instance) and starts again, retrying the assembly, and re-subscribes
// the freshly assembled event stream on success.
func TestTeamSessionRestartRetriesAssembly(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	calls := 0
	m.teamPick.runtime = agentruntime.NewRegistry(m.teamPick.sessions,
		func(spec agentruntime.Spec) (provider.Provider, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("provider unavailable")
			}
			return cliFakeProvider{}, nil
		})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // focus lead
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})         // enter: start fails at assembly
	rt := m.teamPick.runtime
	key := sessionRuntimeKey("alpha", "lead")
	if _, err := rt.Status(key); !errors.Is(err, agentruntime.ErrInstanceNotFound) {
		t.Fatalf("failed assembly must leave no instance, got %v", err)
	}
	if m.teamPick.session.sub != nil {
		t.Fatal("an unassembled instance must not hold a subscription")
	}

	m = teamKey(m, tea.KeyPressMsg{Code: 'r'}) // retry: Stop(absent) + Start
	st, err := rt.Status(key)
	if err != nil || st != agentruntime.RuntimeStateRunning {
		t.Fatalf("after r = %v, %v; want running", st, err)
	}
	if m.teamPick.session.sub == nil {
		t.Fatal("r must re-subscribe the assembled stream")
	}
	if m.teamPick.session.errMsg != "" {
		t.Fatalf("a successful r must clear the session error, got %q", m.teamPick.session.errMsg)
	}
}

// TestTeamDeleteMemberStopsTeamBeforeClear pins §11.6 on the member-delete
// path: the team's runtimes stop before the removal — a leftover running
// instance is stopped by the delete — and the slot disappears from the
// registry.
func TestTeamDeleteMemberStopsTeamBeforeClear(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m.teamPick.runtime = agentruntime.NewRegistry(m.teamPick.sessions,
		func(spec agentruntime.Spec) (provider.Provider, error) { return cliFakeProvider{}, nil })
	rt := m.teamPick.runtime
	// A runtime left running outside the session window (focused member alice).
	key := sessionRuntimeKey("alpha", "alice")
	if _, err := rt.Start(context.Background(), agentruntime.Spec{Key: key}); err != nil {
		t.Fatal(err)
	}

	m = teamKey(m, tea.KeyPressMsg{Code: 'd'})          // arm the member delete
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // confirm
	if st, err := rt.Status(key); err != nil || st != agentruntime.RuntimeStateStopped {
		t.Fatalf("deleted member's runtime = %v, %v; want stopped", st, err)
	}
	doc := readStoredTeamDoc(t)
	if len(doc.Teams) != 1 || len(doc.Teams[0].Template) != 1 || doc.Teams[0].Template[0].MemberID == "alice" {
		t.Fatalf("member not deleted: %+v", doc.Teams[0].Template)
	}
}

// TestTeamLeaderResetStopsTeamBeforeClear pins §11.6 on the k step-down path:
// the team's runtimes stop before the contexts are cleared, then the leader
// flag publishes off and the member directories are gone.
func TestTeamLeaderResetStopsTeamBeforeClear(t *testing.T) {
	leaderTeamFixture(t)
	m := openRoster(t)
	m.teamPick.runtime = agentruntime.NewRegistry(m.teamPick.sessions,
		func(spec agentruntime.Spec) (provider.Provider, error) { return cliFakeProvider{}, nil })
	rt := m.teamPick.runtime
	key := sessionRuntimeKey("T", "alpha") // the leader
	if _, err := rt.Start(context.Background(), agentruntime.Spec{Key: key}); err != nil {
		t.Fatal(err)
	}

	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // warning → exact id
	m = typeTeamName(m, "alpha")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // exact id → directory list
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // final confirm: clear

	if st, err := rt.Status(key); err != nil || st != agentruntime.RuntimeStateStopped {
		t.Fatalf("leader's runtime after reset = %v, %v; want stopped", st, err)
	}
	doc := readStoredTeamDoc(t)
	if doc.Teams[0].Template[0].Leader {
		t.Fatal("the leader flag should be off after the reset")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	ss, err := team.NewTeamSessionStore(cwd)
	if err != nil {
		t.Fatal(err)
	}
	dirs, err := ss.MemberDirs("T")
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 0 {
		t.Fatalf("the team context root should be gone, got dirs %v", dirs)
	}
}

// TestTeamOverlayCloseStopsEveryInstance pins the overlay-exit boundary: when
// the overlay closes, the registry closes too — every remaining instance,
// including a second team's session the window never stopped, is stopped, so
// no loop or subscriber outlives the overlay (§11.6).
func TestTeamOverlayCloseStopsEveryInstance(t *testing.T) {
	writeTeamFixture(t,
		team.Team{Name: "alpha", Template: []team.MemberSlot{
			{MemberID: "lead", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive},
		}},
		team.Team{Name: "beta", Template: []team.MemberSlot{
			{MemberID: "b2", Role: team.RoleCoder, Status: team.MemberStatusActive},
		}})
	m := openTeamOverlay(t)
	m.teamPick.runtime = agentruntime.NewRegistry(m.teamPick.sessions,
		func(spec agentruntime.Spec) (provider.Provider, error) { return cliFakeProvider{}, nil })
	rt := m.teamPick.runtime
	for _, key := range []agentruntime.InstanceKey{
		sessionRuntimeKey("alpha", "lead"), sessionRuntimeKey("beta", "b2"),
	} {
		if _, err := rt.Start(context.Background(), agentruntime.Spec{Key: key}); err != nil {
			t.Fatal(err)
		}
	}

	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc}) // close the session window back to the team list
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc}) // close the overlay
	if m.teamPick != nil {
		t.Fatal("esc should close the overlay")
	}
	for _, key := range []agentruntime.InstanceKey{
		sessionRuntimeKey("alpha", "lead"), sessionRuntimeKey("beta", "b2"),
	} {
		st, err := rt.Status(key)
		if err != nil || st != agentruntime.RuntimeStateStopped {
			t.Fatalf("%s after overlay close = %v, %v; want stopped", key.MemberID, st, err)
		}
	}
}
