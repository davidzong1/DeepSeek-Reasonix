package cli

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/team"
)

// seedTeamContext writes one message per member into the fixture project's
// session store, so the team context root exists with real member dirs.
func seedTeamContext(t *testing.T, teamName string, members ...string) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	ss, err := team.NewTeamSessionStore(cwd)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if err := ss.AppendMessage(teamName, m, team.SessionMessage{Kind: "user", Text: "hello"}); err != nil {
			t.Fatal(err)
		}
	}
}

// leaderTeamFixture seeds a team whose first member is the leader and the
// second a regular member, with real member context dirs.
func leaderTeamFixture(t *testing.T) {
	t.Helper()
	writeTeamFixture(t, team.Team{Name: "T", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive},
		{MemberID: "m1", Role: team.RoleTester, Status: team.MemberStatusActive},
	}})
	seedTeamContext(t, "T", "alpha", "m1")
}

// TestLeaderResetFlowClearsLeaderAndContext walks the full k flow: warning →
// exact id → directory list → done, then verifies the leader flag is off,
// the team context root is gone, and nothing else moved.
func TestLeaderResetFlowClearsLeaderAndContext(t *testing.T) {
	leaderTeamFixture(t)
	m := openRoster(t)

	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})
	got := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"step down", "removes the leader marker", "Enter continue"} {
		if !strings.Contains(got, want) {
			t.Fatalf("k should open the warning stage, missing %q in:\n%s", want, got)
		}
	}

	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	got = ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Type the exact leader id") {
		t.Fatalf("enter should advance to the id stage, got:\n%s", got)
	}
	m = typeTeamName(m, "alpha")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	got = ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"member contexts", "directories under", "Enter continue"} {
		if !strings.Contains(got, want) {
			t.Fatalf("matching id should advance to the list stage, missing %q in:\n%s", want, got)
		}
	}

	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	got = ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Leader stepped down") {
		t.Fatalf("enter should run the clear, got:\n%s", got)
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

// TestLeaderResetWrongIdRefused pins the §6.2 exact-match gate: a mismatched
// id keeps the confirmation on the id stage and writes nothing.
func TestLeaderResetWrongIdRefused(t *testing.T) {
	leaderTeamFixture(t)
	before := storedTeamBytes(t)
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	m = typeTeamName(m, "m1") // a member id, but not the leader's
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "does not match") {
		t.Fatalf("a mismatched id should be refused with a message, got:\n%s", got)
	}
	if !strings.Contains(got, "Type the exact leader id") {
		t.Fatalf("the confirmation should stay on the id stage, got:\n%s", got)
	}
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("a refused id must not write team.json")
	}
}

// TestLeaderResetEscCancelsZeroWrite walks every stage's Esc: the overlay
// returns to the roster and neither team.json nor the member contexts move.
func TestLeaderResetEscCancelsZeroWrite(t *testing.T) {
	leaderTeamFixture(t)
	before := storedTeamBytes(t)
	m := openRoster(t)

	// Warning stage.
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := ansi.Strip(m.renderTeamPicker()); strings.Contains(got, "step down") {
		t.Fatalf("esc should cancel the warning stage, got:\n%s", got)
	}
	// Id stage.
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeTeamName(m, "alpha")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := ansi.Strip(m.renderTeamPicker()); strings.Contains(got, "Type the exact leader id") {
		t.Fatalf("esc should cancel the id stage, got:\n%s", got)
	}
	// List stage.
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeTeamName(m, "alpha")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := ansi.Strip(m.renderTeamPicker()); strings.Contains(got, "member contexts") {
		t.Fatalf("esc should cancel the list stage, got:\n%s", got)
	}

	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("esc must not write team.json")
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
		t.Fatalf("member contexts must survive a cancelled reset, got %d dirs", len(dirs))
	}
}

// TestLeaderResetNonLeaderRefused pins the §1.9 gate: k on a non-leader
// member is refused, never a navigation change and never a confirmation.
func TestLeaderResetNonLeaderRefused(t *testing.T) {
	leaderTeamFixture(t)
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // focus m1, a regular member
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})

	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Only the leader can step down") {
		t.Fatalf("k on a non-leader should be refused with a message, got:\n%s", got)
	}
	if strings.Contains(got, "step down ·") && strings.Contains(got, "Enter continue") {
		t.Fatalf("no confirmation stage should open on a non-leader, got:\n%s", got)
	}
}

// TestLeaderResetTimeoutCancelsOnNextKey pins §6.1's timeout half: a stage
// that sits past the deadline cancels on the next keypress, with zero writes.
func TestLeaderResetTimeoutCancelsOnNextKey(t *testing.T) {
	leaderTeamFixture(t)
	before := storedTeamBytes(t)
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})
	m.teamPick.reset.entered = m.teamPick.reset.entered.Add(-(leaderResetTimeout + time.Second))

	next, _ := m.handleTeamPickerKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(chatTUI)
	if m.teamPick.reset.kind != leaderResetNone {
		t.Fatalf("a stale stage should cancel on the next key, kind = %v", m.teamPick.reset.kind)
	}
	if got := ansi.Strip(m.renderTeamPicker()); strings.Contains(got, "step down") {
		t.Fatalf("no confirmation should render after the timeout, got:\n%s", got)
	}
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("a timed-out confirmation must not write team.json")
	}
}

// TestLeaderResetFromMemberEditor pins the §5 wiring: k inside the member
// editor's field list (on the leader) opens the step-down flow too.
func TestLeaderResetFromMemberEditor(t *testing.T) {
	leaderTeamFixture(t)
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // descend into the member editor
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})

	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "step down") {
		t.Fatalf("k on the editor's field list should open the flow, got:\n%s", got)
	}
}

// TestLeaderResetClearsOnlyTargetTeam pins §6.9's scope: a second team's
// context survives the reset.
func TestLeaderResetClearsOnlyTargetTeam(t *testing.T) {
	writeTeamFixture(t,
		team.Team{Name: "T", Template: []team.MemberSlot{
			{MemberID: "alpha", Leader: true, Status: team.MemberStatusActive},
		}},
		team.Team{Name: "U", Template: []team.MemberSlot{
			{MemberID: "x1", Status: team.MemberStatusActive},
		}},
	)
	seedTeamContext(t, "T", "alpha")
	seedTeamContext(t, "U", "x1")

	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeTeamName(m, "alpha")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	ss, err := team.NewTeamSessionStore(cwd)
	if err != nil {
		t.Fatal(err)
	}
	dirs, err := ss.MemberDirs("U")
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 1 {
		t.Fatalf("another team's context must survive the reset, got %d dirs", len(dirs))
	}
}
