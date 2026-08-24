package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestTeamButtonOpensLeaderSession pins the [TEAM] click target state
// (§11.4): the overlay opens straight on the focused team's leader — session
// active, leader current, the chat composer live as that member's input — and no
// panel at all, so the leader's own history owns the frame. The panel is one
// [ TEAM ] click away.
func TestTeamButtonOpensLeaderSession(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openTeamOverlay(t)

	if !m.teamPick.session.active {
		t.Fatal("the click must open the session window")
	}
	if got := m.teamPick.session.current; got != "lead" {
		t.Fatalf("the session must open on the leader, got %q", got)
	}
	if m.hideComposer() {
		t.Fatal("the composer must be visible while a member session is bound — it is the member's input")
	}
	if got := m.renderTeamPicker(); got != "" {
		t.Fatalf("entering a session must render no panel, got:\n%s", got)
	}
	// Revealed, the panel is a member selector: the bound member's history lives
	// in the main transcript, not in a panel of its own.
	m.setSessionPanel(true)
	got := ansi.Strip(m.renderTeamPicker())
	for _, want := range []string{"lead", "alice"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the session window should render the roster entry %q, got:\n%s", want, got)
		}
	}
}

// TestTeamButtonTogglesSessionPanel pins the panel's visibility contract: a
// freshly entered session renders no panel, the [ TEAM ] button reveals it in
// place and hides it again without re-entering the session, and esc dismisses the
// panel before it closes the session — leaving the team is one esc further out.
func TestTeamButtonTogglesSessionPanel(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openTeamOverlay(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = next.(chatTUI)
	if got := m.renderTeamPicker(); got != "" {
		t.Fatalf("a freshly entered session must render no panel, got:\n%s", got)
	}
	rowsHidden := m.bottomRows()

	x, y := statusButtonXY(t, m, teamButtonText)
	cmd, hit := m.handleTeamStatusClick(x, y)
	if !hit {
		t.Fatal("[ TEAM ] must stay clickable while a member is bound")
	}
	if cmd != nil {
		t.Fatal("[ TEAM ] while bound toggles the panel in place — it must not re-enter the team")
	}
	if !m.teamPick.session.active || m.teamPick.session.current != "lead" {
		t.Fatal("revealing the panel must not rebind the session")
	}
	if m.renderTeamPicker() == "" {
		t.Fatal("[ TEAM ] must reveal the session panel")
	}
	if got := m.bottomRows(); got <= rowsHidden {
		t.Errorf("the panel costs transcript rows: %d hidden vs %d shown", rowsHidden, got)
	}

	// The panel's rows push the status line down, so the button is re-located: it
	// must stay clickable at wherever it now renders.
	x, y = statusButtonXY(t, m, teamButtonText)
	if _, hit := m.handleTeamStatusClick(x, y); !hit {
		t.Fatal("the same button must hide the panel again")
	}
	if got := m.renderTeamPicker(); got != "" {
		t.Fatalf("a second [ TEAM ] click must hide the panel, got:\n%s", got)
	}

	m.setSessionPanel(true)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if !m.teamPick.session.active {
		t.Fatal("esc must dismiss the panel before it closes the session")
	}
	if got := m.renderTeamPicker(); got != "" {
		t.Fatalf("esc must hide the panel, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.teamPick.session.active {
		t.Fatal("a second esc must close the session")
	}
}

// statusButtonXY locates a status-line button in the rendered frame, measured in
// terminal cells, so a click lands exactly where the user sees the button.
func statusButtonXY(t *testing.T, m chatTUI, label string) (int, int) {
	t.Helper()
	for y, styled := range strings.Split(m.View().Content, "\n") {
		if before, _, found := strings.Cut(ansi.Strip(styled), label); found {
			return visibleWidth(before), y
		}
	}
	t.Fatalf("button %q is missing from the rendered frame", label)
	return -1, -1
}

// TestTeamButtonSessionRoutesTypingToTheComposer pins §11.4 key ownership after
// the R2.3 inversion: while a member is bound the main composer IS that member's
// input, so a printable key reaches it through the whole Update chain — not
// merely past handleTeamKey — while a reserved switch key stays with the session.
func TestTeamButtonSessionRoutesTypingToTheComposer(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openTeamOverlay(t)
	m.input.SetValue("draft")

	m = teamKey(m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if !m.teamPick.session.active {
		t.Fatal("typing must not close the session window")
	}
	typed := m.input.Value()
	if !strings.Contains(typed, "x") || !strings.Contains(typed, "draft") {
		t.Fatalf("a printable key must reach the member's composer, got %q", typed)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl})
	if got := m.input.Value(); got != typed {
		t.Fatalf("a reserved switch key must not type into the composer, got %q", got)
	}
}

// TestTeamButtonEscapeReturnsToTeamList pins the session exit state: Esc
// closes the session window back to the team list, where the roster
// management page is one Enter away — the overlay stays up, the chat
// composer stays hidden.
func TestTeamButtonEscapeReturnsToTeamList(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openTeamOverlay(t)

	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.teamPick.session.active {
		t.Fatal("esc must close the session window")
	}
	if m.teamPick == nil {
		t.Fatal("the overlay must stay up after the session closes")
	}
	if !m.hideComposer() {
		t.Fatal("the chat composer stays hidden while the overlay is up")
	}
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "Teams") {
		t.Fatalf("esc should land on the team list, got:\n%s", got)
	}
}
