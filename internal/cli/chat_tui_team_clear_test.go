package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/team"
)

// clearHistoryFixture seeds a two-team registry whose first team has a leader
// and a regular member, each with a canonical owner directory and a legacy
// session file, plus one member of the second team. It returns the opened
// roster — whose picker owns the session directory the legacy half lives in —
// plus the seeded owner paths per (team, member) and the legacy session paths,
// so a test can assert both storage halves survived or went.
func clearHistoryFixture(t *testing.T) (chatTUI, map[string]team.OwnerPaths, map[string]string) {
	t.Helper()
	writeTeamFixture(t,
		team.Team{Name: "alpha", Template: []team.MemberSlot{
			{MemberID: "lead", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive},
			{MemberID: "alice", Role: team.RoleCoder, Status: team.MemberStatusActive},
		}},
		team.Team{Name: "beta", Template: []team.MemberSlot{
			{MemberID: "x1", Status: team.MemberStatusActive},
		}},
	)
	m := openRoster(t)
	// A real session directory, so the legacy half of the clear has somewhere
	// to delete from: the fixture controller carries none.
	m.teamPick.sessionDir = t.TempDir()
	owners := ownerStoreOf(t, m)
	ownerPaths := map[string]team.OwnerPaths{}
	for _, key := range []team.OwnerKey{
		{TeamID: "alpha", MemberID: "lead"},
		{TeamID: "alpha", MemberID: "alice"},
		{TeamID: "beta", MemberID: "x1"},
	} {
		ownerPaths[key.String()] = seedOwnerUnit(t, owners, key.TeamID, key.MemberID)
	}
	legacy := map[string]string{}
	for _, key := range []team.OwnerKey{{TeamID: "alpha", MemberID: "lead"}, {TeamID: "alpha", MemberID: "alice"}} {
		name, err := team.MemberSessionFile(key.TeamID, key.MemberID)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(m.teamPick.sessionDir, name)
		seedMemberSession(t, path)
		legacy[key.String()] = path
	}
	return m, ownerPaths, legacy
}

// walkTeamClear drives the c confirmation from the roster to its terminal
// stage, typing typedName at the name stage.
func walkTeamClear(m chatTUI, typedName string) chatTUI {
	m = teamKey(m, tea.KeyPressMsg{Code: 'c'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeTeamName(m, typedName)
	return teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
}

// TestTeamClearNonLeaderRefused pins the leader gate: c on a non-leader is
// refused with a message, opens no confirmation, and deletes nothing.
func TestTeamClearNonLeaderRefused(t *testing.T) {
	m, owners, legacy := clearHistoryFixture(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // focus alice, a regular member
	m = teamKey(m, tea.KeyPressMsg{Code: 'c'})

	if got := m.teamPick.teamClear.kind; got != teamClearNone {
		t.Fatalf("c on a non-leader must not open a confirmation, kind = %v", got)
	}
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Only the leader can clear team histories") {
		t.Fatalf("c on a non-leader should be refused with a message, got:\n%s", got)
	}
	for key, paths := range owners {
		if _, err := os.Stat(paths.Dir); err != nil {
			t.Fatalf("a refused c must not touch %s: %v", key, err)
		}
	}
	for key, path := range legacy {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("a refused c must not touch the legacy file %s: %v", key, err)
		}
	}
}

// TestTeamClearWarnsWithTeamScope pins the first stage's promise: pressing c on
// the leader states the team-wide scope — every member including the leader —
// and names what survives, before any name can be typed.
func TestTeamClearWarnsWithTeamScope(t *testing.T) {
	m, _, _ := clearHistoryFixture(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'c'})

	if got := m.teamPick.teamClear.kind; got != teamClearWarn {
		t.Fatalf("c on the leader should arm the warning stage, kind = %v", got)
	}
	got := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"clear histories", "alpha", "leader's", "untouched", "Enter continue"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the warning must state the team scope (%q missing):\n%s", want, got)
		}
	}
}

// TestTeamClearEscCancelsEveryStage pins the zero-write cancel: Esc at the
// warning and at the name stage returns to the roster with both storage halves
// exactly where they were.
func TestTeamClearEscCancelsEveryStage(t *testing.T) {
	m, owners, legacy := clearHistoryFixture(t)

	m = teamKey(m, tea.KeyPressMsg{Code: 'c'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := m.teamPick.teamClear.kind; got != teamClearNone {
		t.Fatalf("esc should cancel the warning stage, kind = %v", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 'c'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeTeamName(m, "alpha")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := m.teamPick.teamClear.kind; got != teamClearNone {
		t.Fatalf("esc should cancel the name stage, kind = %v", got)
	}
	if got := ansi.Strip(m.renderTeamPicker()); strings.Contains(got, "clear histories ·") {
		t.Fatalf("no confirmation should render after esc, got:\n%s", got)
	}
	for key, paths := range owners {
		if _, err := os.Stat(paths.Dir); err != nil {
			t.Fatalf("a cancelled c must not clear %s: %v", key, err)
		}
	}
	for key, path := range legacy {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("a cancelled c must not clear the legacy file %s: %v", key, err)
		}
	}
}

// TestTeamClearTimeoutCancelsOnNextKey pins the 30s half of the contract: a
// stage that sits past the deadline cancels on the next keypress with nothing
// deleted.
func TestTeamClearTimeoutCancelsOnNextKey(t *testing.T) {
	m, owners, _ := clearHistoryFixture(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'c'})
	m.teamPick.teamClear.entered = m.teamPick.teamClear.entered.Add(-(leaderResetTimeout + time.Second))

	next, _ := m.handleTeamPickerKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(chatTUI)
	if got := m.teamPick.teamClear.kind; got != teamClearNone {
		t.Fatalf("a stale stage should cancel on the next key, kind = %v", got)
	}
	for key, paths := range owners {
		if _, err := os.Stat(paths.Dir); err != nil {
			t.Fatalf("a timed-out c must not clear %s: %v", key, err)
		}
	}
}

// TestTeamClearWrongNameRefused pins the exact-match gate: a name that is not
// the team's keeps the confirmation on the name stage, reports the mismatch,
// and deletes nothing.
func TestTeamClearWrongNameRefused(t *testing.T) {
	m, owners, legacy := clearHistoryFixture(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'c'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	m = typeTeamName(m, "beta") // a real team, but not this one
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.teamPick.teamClear.kind; got != teamClearName {
		t.Fatalf("a mismatched name must stay on the name stage, kind = %v", got)
	}
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "does not match") {
		t.Fatalf("a mismatched name should be refused with a message, got:\n%s", got)
	}
	for key, paths := range owners {
		if _, err := os.Stat(paths.Dir); err != nil {
			t.Fatalf("a refused name must not clear %s: %v", key, err)
		}
	}
	for key, path := range legacy {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("a refused name must not clear the legacy file %s: %v", key, err)
		}
	}
}

// TestTeamClearRemovesEveryMemberHistoryOfTheTeam pins the confirmed clear:
// every member of the focused team — the leader included — loses both its
// canonical owner directory and its legacy session file, while another team's
// member and the roster itself are untouched.
func TestTeamClearRemovesEveryMemberHistoryOfTheTeam(t *testing.T) {
	m, owners, legacy := clearHistoryFixture(t)
	before := readStoredTeamDoc(t)

	m = walkTeamClear(m, "alpha")
	if got := m.teamPick.teamClear.kind; got != teamClearDone {
		t.Fatalf("the exact name should run the clear, kind = %v", got)
	}
	if m.teamPick.teamClear.errMsg != "" {
		t.Fatalf("a successful clear must not report an error: %s", m.teamPick.teamClear.errMsg)
	}
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Cleared 2 member histories of") || !strings.Contains(got, "alpha") {
		t.Fatalf("the result must name the team and the count, got:\n%s", got)
	}

	for _, key := range []string{"alpha/lead", "alpha/alice"} {
		if _, err := os.Stat(owners[key].Dir); !os.IsNotExist(err) {
			t.Errorf("owner dir of %s survived the clear: %v", key, err)
		}
		if _, err := os.Stat(legacy[key]); !os.IsNotExist(err) {
			t.Errorf("legacy session file of %s survived the clear: %v", key, err)
		}
	}
	if _, err := os.Stat(owners["beta/x1"].Dir); err != nil {
		t.Errorf("another team's owner dir must survive: %v", err)
	}
	// The clear is a history clear, not a roster edit: the slots and the leader
	// marker stay exactly as they were.
	after := readStoredTeamDoc(t)
	if len(after.Teams[0].Template) != 2 || !after.Teams[0].Template[0].Leader {
		t.Fatalf("the roster must survive a history clear, got %+v", after.Teams[0].Template)
	}
	if len(before.Teams[0].Template) != len(after.Teams[0].Template) {
		t.Fatal("the clear changed the roster size")
	}
	// Idempotent: a second confirmed clear over an emptied team is not an error.
	m = walkTeamClear(m, "alpha")
	if m.teamPick.teamClear.errMsg != "" {
		t.Fatalf("a repeated clear must stay clean, got %s", m.teamPick.teamClear.errMsg)
	}
}

// TestTeamClearKeepsStepDownFlow pins the boundary with k: c clears histories
// and leaves the leader in place, so the step-down flow is still reachable and
// still owns its own stages afterwards.
func TestTeamClearKeepsStepDownFlow(t *testing.T) {
	m, _, _ := clearHistoryFixture(t)
	m = walkTeamClear(m, "alpha")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // close the result

	if got := m.teamPick.reset.kind; got != leaderResetNone {
		t.Fatalf("c must not arm the step-down flow, kind = %v", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})
	if got := m.teamPick.reset.kind; got != leaderResetWarn {
		t.Fatalf("k must still arm its own warning stage after a clear, kind = %v", got)
	}
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "step down") {
		t.Fatalf("k's confirmation must still render, got:\n%s", got)
	}
	if got := ansi.Strip(m.renderTeamPicker()); strings.Contains(got, "clear histories") {
		t.Fatalf("k's stage must not render c's confirmation, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeTeamName(m, "lead")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.teamPick.reset.kind; got != leaderResetDone {
		t.Fatalf("the k flow must still complete after a c clear, kind = %v", got)
	}
	if doc := readStoredTeamDoc(t); doc.Teams[0].Template[0].Leader {
		t.Fatal("the leader flag should be off after the step-down")
	}
}

// TestTeamClearOwnerHistoryOutlivesTheRosterSlot pins the owner-store scope: a
// directory whose roster slot is already gone is still cleared, because a
// member's history is enumerated from owner storage rather than from the
// registry that no longer mentions it.
func TestTeamClearOwnerHistoryOutlivesTheRosterSlot(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "alpha", Template: []team.MemberSlot{
		{MemberID: "lead", Leader: true, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	owners := ownerStoreOf(t, m)
	kept := seedOwnerUnit(t, owners, "alpha", "lead")
	orphan := seedOwnerUnit(t, owners, "alpha", "ghost")
	other := seedOwnerUnit(t, owners, "beta", "lead")

	m = walkTeamClear(m, "alpha")
	if m.teamPick.teamClear.errMsg != "" {
		t.Fatalf("clear: %s", m.teamPick.teamClear.errMsg)
	}
	for _, paths := range []team.OwnerPaths{kept, orphan} {
		if _, err := os.Stat(paths.Dir); !os.IsNotExist(err) {
			t.Errorf("owner dir %s survived the clear: %v", paths.Dir, err)
		}
	}
	if _, err := os.Stat(other.Dir); err != nil {
		t.Errorf("another team's owner dir must survive: %v", err)
	}
}

// TestTeamClearPasteLandsInTheNameStage pins the paste route: the name stage is
// the confirmation's only text input, so a pasted team name reaches it instead
// of being swallowed or leaking into the hidden composer.
func TestTeamClearPasteLandsInTheNameStage(t *testing.T) {
	m, _, _ := clearHistoryFixture(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'c'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if target := teamPasteTarget(m.teamPick); target == nil || target != &m.teamPick.teamClear.buf {
		t.Fatal("the name stage must be the overlay's paste target")
	}
	m = walkTeamClear(m, "alpha")
	if m.teamPick.teamClear.errMsg != "" {
		t.Fatalf("a typed name must confirm, got %s", m.teamPick.teamClear.errMsg)
	}
}

// TestTeamClearEnumeratesOwnerDirsDirectly pins the counting half: the count the
// warning and the result show comes from the storage the clear reaches, so an
// owner directory without a roster slot is counted rather than invisible.
func TestTeamClearEnumeratesOwnerDirsDirectly(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "alpha", Template: []team.MemberSlot{
		{MemberID: "lead", Leader: true, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	owners := ownerStoreOf(t, m)
	seedOwnerUnit(t, owners, "alpha", "lead")
	seedOwnerUnit(t, owners, "alpha", "ghost")
	seedOwnerUnit(t, owners, "beta", "lead")

	if got := m.teamPick.clearHistoryTargets("alpha"); got != 2 {
		t.Fatalf("clearHistoryTargets = %d, want the 2 owner dirs of alpha", got)
	}
}

// TestTeamClearRosterOnlyAndAdvertised pins the scope the key is allowed in: c
// is advertised on the roster's help line and armed from the roster alone — a
// bound member session forwards c to the member's own composer, so typing the
// letter can never arm a team-wide delete.
func TestTeamClearRosterOnlyAndAdvertised(t *testing.T) {
	m, _, _ := clearHistoryFixture(t)
	// Rendered wide, so the help block is the single line the key lives on
	// rather than a wrap point that splits the label.
	m.width = 220
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "c clear histories") {
		t.Fatalf("the roster help must advertise c, got:\n%s", got)
	}
	m.width = 80

	m = teamKey(m, tea.KeyPressMsg{Code: 't'}) // open the leader's session window
	if !m.teamPick.session.active {
		t.Fatal("precondition: t must open the session window")
	}
	next, _, consumed := m.handleTeamKey(tea.KeyPressMsg{Code: 'c'})
	m = next.(chatTUI)
	if consumed {
		t.Fatal("a bound session must leave c to the member's composer")
	}
	if got := m.teamPick.teamClear.kind; got != teamClearNone {
		t.Fatalf("c inside a session must not arm the clear, kind = %v", got)
	}
}
