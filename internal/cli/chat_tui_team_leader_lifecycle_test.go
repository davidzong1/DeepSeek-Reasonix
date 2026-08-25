package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/team"
)

// TestAssignFocusedLeaderSetsLeaderWhenNone pins the l key's assign-only
// contract: on a leaderless team the focused member becomes the leader, and
// the reload makes the t session gate see it immediately.
func TestAssignFocusedLeaderSetsLeaderWhenNone(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "T", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
		{MemberID: "m1", Role: team.RoleTester, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	if err := m.teamPick.assignFocusedLeader(); err != nil {
		t.Fatalf("assign on a leaderless team should succeed: %v", err)
	}
	doc := readStoredTeamDoc(t)
	if !doc.Teams[0].Template[0].Leader {
		t.Fatal("the focused member should be the leader after assign")
	}
}

// TestAssignFocusedLeaderRefusedWhenLeaderExists pins the gate: a team with a
// leader refuses assigning another, naming the holder, and writes nothing.
func TestAssignFocusedLeaderRefusedWhenLeaderExists(t *testing.T) {
	leaderTeamFixture(t)
	before := storedTeamBytes(t)
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // focus m1
	err := m.teamPick.assignFocusedLeader()
	if err == nil || !strings.Contains(err.Error(), "already has a leader") {
		t.Fatalf("assigning a second leader should refuse, got %v", err)
	}
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("a refused assign must not write team.json")
	}
}

// TestAssignFocusedLeaderIdempotent: re-assigning the team's existing leader
// is a no-op that leaves the registry byte-identical.
func TestAssignFocusedLeaderIdempotent(t *testing.T) {
	leaderTeamFixture(t)
	before := storedTeamBytes(t)
	m := openRoster(t)
	if err := m.teamPick.assignFocusedLeader(); err != nil { // focus is alpha, the leader
		t.Fatalf("re-assigning the leader should be a no-op: %v", err)
	}
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("an idempotent assign must not rewrite team.json")
	}
}

// TestStepDownLeaderStrictOrder pins the k contract end to end: the session
// window closes, the leader flag falls, and the team context root is gone,
// while a blackboard file under the team tree survives — it is shared data,
// not leader context.
func TestStepDownLeaderStrictOrder(t *testing.T) {
	leaderTeamFixture(t)
	board := filepath.Join(".reasonix", "team", "board.db")
	if err := os.WriteFile(board, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := openRoster(t)
	m.teamPick.session = sessionState{active: true, teamName: "T"} // a bound session window
	if err := m.teamPick.stepDownLeader("T", "alpha"); err != nil {
		t.Fatalf("step-down on the leader should succeed: %v", err)
	}
	doc := readStoredTeamDoc(t)
	if doc.Teams[0].Template[0].Leader {
		t.Fatal("the leader flag should be off after step-down")
	}
	if m.teamPick.session.active {
		t.Fatal("the session window should be closed after step-down")
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
	if _, err := os.Stat(board); err != nil {
		t.Fatalf("the blackboard file must survive step-down: %v", err)
	}
}

// TestStepDownLeaderRefusesNonLeader: a non-leader member id aborts before
// anything stops or clears, and nothing is written.
func TestStepDownLeaderRefusesNonLeader(t *testing.T) {
	leaderTeamFixture(t)
	before := storedTeamBytes(t)
	m := openRoster(t)
	err := m.teamPick.stepDownLeader("T", "m1")
	if err == nil || !strings.Contains(err.Error(), "not the leader") {
		t.Fatalf("a non-leader must be refused, got %v", err)
	}
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("a refused step-down must not write team.json")
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
	if len(dirs) != 2 {
		t.Fatalf("a refused step-down must not clear contexts, got dirs %v", dirs)
	}
}

// TestStepDownLeaderFailureLeavesLeaderFlag pins write-before-commit: a clear
// failure — the trash root replaced by a file breaks the sweep — aborts
// before the leader flag is published off, and the context root is untouched.
func TestStepDownLeaderFailureLeavesLeaderFlag(t *testing.T) {
	leaderTeamFixture(t)
	before := storedTeamBytes(t)
	trash := filepath.Join(".reasonix", "team", ".trash")
	if err := os.WriteFile(trash, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := openRoster(t)
	if err := m.teamPick.stepDownLeader("T", "alpha"); err == nil {
		t.Fatal("a failed clear must abort the step-down")
	}
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("a failed clear must not publish the leader flag off")
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
	if len(dirs) != 2 {
		t.Fatalf("a failed clear must leave the context root alone, got dirs %v", dirs)
	}
}

// TestLiveTeamCount counts per-team assembled backends; the step-down stop
// step uses it to prove the release completed. Nil values are fine: the count
// only reads the registry keys.
func TestLiveTeamCount(t *testing.T) {
	var api control.SessionAPI
	r := newTeamBackends(nil, 0)
	r.live[backendKey("T", "alpha")] = api
	r.live[backendKey("T", "m1")] = api
	r.live[backendKey("U", "x")] = api
	if got := r.liveTeamCount("T"); got != 2 {
		t.Fatalf("T should hold 2 live backends, got %d", got)
	}
	if got := r.liveTeamCount("U"); got != 1 {
		t.Fatalf("U should hold 1 live backend, got %d", got)
	}
	if got := r.liveTeamCount("V"); got != 0 {
		t.Fatalf("V should hold none, got %d", got)
	}
}
