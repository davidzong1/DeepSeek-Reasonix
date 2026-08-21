package tui

import (
	"testing"

	"reasonix/internal/team"
)

func member(id string, st team.MemberState) team.Member {
	return team.Member{ID: id, State: st}
}

// oneTeam is the single-team registry most navigation tests need.
func oneTeam(members ...team.Member) []TeamView {
	return []TeamView{{Name: "t", Members: members}}
}

// TestStateRankPriority pins the §2.2 contractual order and the unknown sink.
func TestStateRankPriority(t *testing.T) {
	order := []team.MemberState{
		team.MemberStateApproval,
		team.MemberStateWorking,
		team.MemberStateQuota,
		team.MemberStateDead,
		team.MemberStateIdle,
	}
	for i, st := range order {
		if got, want := stateRank(st), i; got != want {
			t.Fatalf("stateRank(%q)=%d, want %d", st, got, want)
		}
	}
	for _, st := range []team.MemberState{"", "unknown", "busy"} {
		if got, want := stateRank(st), len(order); got != want {
			t.Fatalf("stateRank(%q)=%d, want unknown sink %d", st, got, want)
		}
	}
}

func TestNewOrdersByStatePriority(t *testing.T) {
	m := New(oneTeam(
		member("c", team.MemberStateIdle),
		member("a", team.MemberStateWorking),
		member("b", team.MemberStateApproval),
		member("d", team.MemberStateQuota),
		member("e", team.MemberStateDead),
	))
	got := m.Members()
	want := []string{"b", "a", "d", "e", "c"}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("display order[%d]=%q, want %q", i, got[i].ID, id)
		}
	}
}

func TestNewBreaksTiesByID(t *testing.T) {
	m := New(oneTeam(
		member("m2", team.MemberStateIdle),
		member("m1", team.MemberStateIdle),
	))
	if got, want := m.Members()[0].ID, "m1"; got != want {
		t.Fatalf("tie-break order[0]=%q, want %q", got, want)
	}
}

// TestUnknownStateSinksToBottom keeps an unrecognized state from reordering
// the list silently ahead of known states.
func TestUnknownStateSinksToBottom(t *testing.T) {
	m := New(oneTeam(member("z", "new-state"), member("a", team.MemberStateIdle)))
	got := m.Members()
	if got[0].ID != "a" || got[1].ID != "z" {
		t.Fatalf("order = %q,%q; want a,z (unknown last)", got[0].ID, got[1].ID)
	}
}

// TestNewCopiesInput pins that the model neither aliases caller input nor
// hands out aliases of its own state.
func TestNewCopiesInput(t *testing.T) {
	in := oneTeam(member("a", team.MemberStateWorking))
	m := New(in)
	in[0].Members[0].State = team.MemberStateIdle
	in[0].Name = "mutated"
	if got := m.Members()[0].State; got != team.MemberStateWorking {
		t.Fatalf("model aliases caller input: state=%q, want working", got)
	}
	if got := m.Name(); got != "t" {
		t.Fatalf("model aliases caller team name: %q, want t", got)
	}
	m.Members()[0].ID = "mutated"
	if m.Members()[0].ID == "mutated" {
		t.Fatal("Members() hands out aliases of model state")
	}
	m.Teams()[0].Members[0].ID = "mutated"
	if m.Members()[0].ID == "mutated" {
		t.Fatal("Teams() hands out aliases of model state")
	}
}

// TestNewOpensOnTeamList pins the entry screen: clicking [ TEAM ] lands on the
// team list, where team lifecycle acts, not inside a roster.
func TestNewOpensOnTeamList(t *testing.T) {
	m := New([]TeamView{{Name: "alpha"}, {Name: "beta"}})
	if got := m.Mode(); got != ModeTeams {
		t.Fatalf("Mode()=%q, want %q", got, ModeTeams)
	}
	if got := m.TeamIndex(); got != 0 {
		t.Fatalf("TeamIndex()=%d, want 0", got)
	}
	if got := m.Name(); got != "alpha" {
		t.Fatalf("Name()=%q, want alpha", got)
	}
	if got := len(m.Teams()); got != 2 {
		t.Fatalf("Teams() length=%d, want 2", got)
	}
}

// TestTeamListNavigationClamps pins team focus movement and its clamping, and
// that switching team switches the roster the model reports.
func TestTeamListNavigationClamps(t *testing.T) {
	m := New([]TeamView{
		{Name: "alpha", Members: []team.Member{member("a1", team.MemberStateIdle)}},
		{Name: "beta", Members: []team.Member{member("b1", team.MemberStateIdle)}},
	})
	if m.Handle(EventUp) || m.TeamIndex() != 0 {
		t.Fatal("up at the top of the team list must be ignored")
	}
	if !m.Handle(EventDown) || m.TeamIndex() != 1 {
		t.Fatalf("down should focus team 1, got %d", m.TeamIndex())
	}
	if got := m.Name(); got != "beta" {
		t.Fatalf("focused team name=%q, want beta", got)
	}
	if got, ok := m.Focused(); !ok || got.ID != "b1" {
		t.Fatalf("roster should follow team focus, got %q", got.ID)
	}
	if m.Handle(EventDown) || m.TeamIndex() != 1 {
		t.Fatal("down at the bottom of the team list must be ignored")
	}
	if !m.Handle(EventUp) || m.TeamIndex() != 0 {
		t.Fatalf("up should focus team 0, got %d", m.TeamIndex())
	}
}

// TestNavigationDescendsAndReturns pins the three-level chain: team list →
// roster → member context, and back out the same way.
func TestNavigationDescendsAndReturns(t *testing.T) {
	m := New(oneTeam(member("a", team.MemberStateApproval)))
	if !m.Handle(EventSelect) || m.Mode() != ModeList {
		t.Fatalf("select on the team list should open the roster, mode=%q", m.Mode())
	}
	if !m.Handle(EventSelect) || m.Mode() != ModeContext {
		t.Fatalf("select in the roster should open the context view, mode=%q", m.Mode())
	}
	if f, ok := m.Focused(); !ok || f.ID != "a" {
		t.Fatalf("context focus=%v, want a", f.ID)
	}
	if !m.Handle(EventBack) || m.Mode() != ModeList {
		t.Fatalf("back should return to the roster, mode=%q", m.Mode())
	}
	if !m.Handle(EventBack) || m.Mode() != ModeTeams {
		t.Fatalf("back should return to the team list, mode=%q", m.Mode())
	}
	if m.Handle(EventBack) || m.Mode() != ModeTeams {
		t.Fatal("back on the team list must be ignored (the frontend closes)")
	}
}

// TestRosterNavigationClamps pins member focus clamping inside a roster.
func TestRosterNavigationClamps(t *testing.T) {
	// working sorts before idle, so focus starts on b.
	m := New(oneTeam(member("a", team.MemberStateIdle), member("b", team.MemberStateWorking)))
	m.Handle(EventSelect)
	if m.FocusIndex() != 0 {
		t.Fatalf("initial focus=%d, want 0", m.FocusIndex())
	}
	if f, ok := m.Focused(); !ok || f.ID != "b" {
		t.Fatalf("initial Focused=%v, want b", f.ID)
	}
	if !m.Handle(EventDown) || m.FocusIndex() != 1 {
		t.Fatalf("down should move focus to 1, got %d", m.FocusIndex())
	}
	if m.Handle(EventDown) || m.FocusIndex() != 1 {
		t.Fatal("down at the bottom must be ignored and stay clamped")
	}
	if !m.Handle(EventUp) || m.FocusIndex() != 0 {
		t.Fatalf("up should move focus to 0, got %d", m.FocusIndex())
	}
	if m.Handle(EventUp) || m.FocusIndex() != 0 {
		t.Fatal("up at the top must be ignored and stay clamped")
	}
}

// TestContextModeSwitchesMember pins that up/down in the context view switch
// the viewed member without leaving context mode (§3.2 member switching).
func TestContextModeSwitchesMember(t *testing.T) {
	m := New(oneTeam(member("a", team.MemberStateIdle), member("b", team.MemberStateWorking)))
	m.Handle(EventSelect)
	if !m.Handle(EventSelect) || m.Mode() != ModeContext {
		t.Fatalf("select should open context view, mode=%q", m.Mode())
	}
	if f, _ := m.Focused(); f.ID != "b" {
		t.Fatalf("context focus=%q, want b", f.ID)
	}
	if !m.Handle(EventDown) || m.Mode() != ModeContext {
		t.Fatal("down must switch member and stay in context mode")
	}
	if f, _ := m.Focused(); f.ID != "a" {
		t.Fatalf("context focus after down=%q, want a", f.ID)
	}
	if m.Handle(EventSelect) {
		t.Fatal("select inside the context view must be ignored")
	}
}

// TestSelectEmptyRosterIsIgnored keeps a team with no members from opening a
// context view over nothing.
func TestSelectEmptyRosterIsIgnored(t *testing.T) {
	m := New([]TeamView{{Name: "empty"}})
	if !m.Handle(EventSelect) || m.Mode() != ModeList {
		t.Fatalf("select should still open the empty roster, mode=%q", m.Mode())
	}
	if m.Handle(EventSelect) || m.Mode() != ModeList {
		t.Fatal("select on an empty roster must be ignored")
	}
	if _, ok := m.Focused(); ok {
		t.Fatal("empty roster must not report a focused member")
	}
}

// TestQuitChain pins the exit semantics: quit exits from any screen, back
// cancels onto the screen it was entered from, and every other event is
// ignored once reached.
func TestQuitChain(t *testing.T) {
	m := New(oneTeam(member("a", team.MemberStateIdle)))
	if !m.Handle(EventQuit) || m.Mode() != ModeQuit {
		t.Fatalf("quit from the team list should exit, mode=%q", m.Mode())
	}
	if !m.Handle(EventBack) || m.Mode() != ModeTeams {
		t.Fatalf("back should cancel onto the team list, mode=%q", m.Mode())
	}
	m.Handle(EventSelect)
	if !m.Handle(EventQuit) || m.Mode() != ModeQuit {
		t.Fatal("quit from the roster should exit")
	}
	if !m.Handle(EventBack) || m.Mode() != ModeList {
		t.Fatalf("back should cancel onto the roster, mode=%q", m.Mode())
	}
	m.Handle(EventSelect)
	if !m.Handle(EventQuit) || m.Mode() != ModeQuit {
		t.Fatal("quit from the context view should exit")
	}
	if !m.Handle(EventBack) || m.Mode() != ModeContext {
		t.Fatalf("back should cancel onto the context view, mode=%q", m.Mode())
	}
	m.Handle(EventQuit)
	for _, ev := range []Event{EventQuit, EventUp, EventDown, EventSelect} {
		if m.Handle(ev) {
			t.Fatalf("event %q in quit mode must be ignored", ev)
		}
	}
	if m.Mode() != ModeQuit {
		t.Fatal("ignored events must not change quit mode")
	}
}

// TestEmptyRegistry pins the bootstrap screen: no team registered still opens
// the team list, reports no focus, and only quit acts.
func TestEmptyRegistry(t *testing.T) {
	m := New(nil)
	if m.Mode() != ModeTeams {
		t.Fatalf("empty registry should open the team list, mode=%q", m.Mode())
	}
	if _, ok := m.FocusedTeam(); ok {
		t.Fatal("empty registry must not report a focused team")
	}
	if _, ok := m.Focused(); ok {
		t.Fatal("empty registry must not report a focused member")
	}
	if got := m.Name(); got != "" {
		t.Fatalf("Name()=%q, want empty", got)
	}
	for _, ev := range []Event{EventUp, EventDown, EventSelect, EventBack} {
		if m.Handle(ev) {
			t.Fatalf("event %q on the empty registry must be ignored", ev)
		}
	}
	if !m.Handle(EventQuit) || m.Mode() != ModeQuit {
		t.Fatal("quit on the empty registry must exit")
	}
}

func TestSelectTeamByName(t *testing.T) {
	m := New([]TeamView{{Name: "alpha"}, {Name: "beta"}})
	if !m.SelectTeam("beta") || m.Name() != "beta" {
		t.Fatalf("SelectTeam(beta) should focus beta, got %q", m.Name())
	}
	if m.SelectTeam("missing") {
		t.Fatal("SelectTeam should report an unknown name")
	}
	if got := m.Name(); got != "beta" {
		t.Fatalf("unknown name must leave focus unchanged, got %q", got)
	}
}

// TestReloadKeepsFocusAndScreen pins that re-reading the registry after a write
// leaves the user where they were instead of at the top level.
func TestReloadKeepsFocusAndScreen(t *testing.T) {
	m := New([]TeamView{
		{Name: "alpha", Members: []team.Member{member("a1", team.MemberStateIdle)}},
		{Name: "beta", Members: []team.Member{member("b1", team.MemberStateIdle), member("b2", team.MemberStateIdle)}},
	})
	m.Handle(EventDown)   // focus beta
	m.Handle(EventSelect) // open its roster
	m.Handle(EventDown)   // focus b2
	m.Handle(EventSelect) // open b2's context view

	m.Reload([]TeamView{
		{Name: "alpha", Members: []team.Member{member("a1", team.MemberStateIdle)}},
		{Name: "beta", Members: []team.Member{member("b1", team.MemberStateIdle), member("b2", team.MemberStateIdle), member("b3", team.MemberStateIdle)}},
	})
	if got := m.Mode(); got != ModeContext {
		t.Fatalf("reload should keep the context view, mode=%q", got)
	}
	if got := m.Name(); got != "beta" {
		t.Fatalf("reload should keep team focus, got %q", got)
	}
	if f, ok := m.Focused(); !ok || f.ID != "b2" {
		t.Fatalf("reload should keep member focus on b2, got %q", f.ID)
	}
	if got := len(m.Members()); got != 3 {
		t.Fatalf("reload should show the new roster size, got %d", got)
	}
}

// TestReloadStepsOutWhenFocusVanishes pins the graceful fallbacks: a deleted
// member drops to the roster, a deleted team drops to the team list.
func TestReloadStepsOutWhenFocusVanishes(t *testing.T) {
	m := New([]TeamView{
		{Name: "alpha", Members: []team.Member{member("a1", team.MemberStateIdle)}},
		{Name: "beta", Members: []team.Member{member("b1", team.MemberStateIdle)}},
	})
	m.Handle(EventDown)
	m.Handle(EventSelect)
	m.Handle(EventSelect)

	m.Reload([]TeamView{{Name: "alpha", Members: []team.Member{member("a1", team.MemberStateIdle)}}, {Name: "beta"}})
	if got := m.Mode(); got != ModeList {
		t.Fatalf("a deleted member should drop to the roster, mode=%q", got)
	}
	if got := m.Name(); got != "beta" {
		t.Fatalf("team focus should survive, got %q", got)
	}

	m.Reload([]TeamView{{Name: "alpha", Members: []team.Member{member("a1", team.MemberStateIdle)}}})
	if got := m.Mode(); got != ModeTeams {
		t.Fatalf("a deleted team should drop to the team list, mode=%q", got)
	}
	if got := m.Name(); got != "alpha" {
		t.Fatalf("focus should fall onto the first team, got %q", got)
	}
}

// TestReloadIntoEmptyRegistry pins deleting down to nothing: the model reports
// no focus and stays on the team list rather than dangling on a stale index.
func TestReloadIntoEmptyRegistry(t *testing.T) {
	m := New(oneTeam(member("a", team.MemberStateIdle)))
	m.Handle(EventSelect)
	m.Reload(nil)
	if got := m.Mode(); got != ModeTeams {
		t.Fatalf("mode=%q, want %q", got, ModeTeams)
	}
	if _, ok := m.FocusedTeam(); ok {
		t.Fatal("empty reload must not report a focused team")
	}
	if got := m.Members(); got != nil {
		t.Fatalf("empty reload should report no roster, got %v", got)
	}
}

// TestReloadFromQuitDoesNotResumeQuit keeps a stale confirmation from coming
// back after the registry is re-read.
func TestReloadFromQuitDoesNotResumeQuit(t *testing.T) {
	m := New(oneTeam(member("a", team.MemberStateIdle)))
	m.Handle(EventQuit)
	m.Reload(oneTeam(member("a", team.MemberStateIdle)))
	if got := m.Mode(); got != ModeTeams {
		t.Fatalf("reload from quit should land on the team list, mode=%q", got)
	}
}
