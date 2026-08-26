package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// TestBoundSessionGivesKeyboardToComposer pins R2.3's inversion: while a member
// is bound the main composer owns typing, so a letter reaches the composer
// instead of the roster. Only the reserved keys stay with the session.
func TestBoundSessionGivesKeyboardToComposer(t *testing.T) {
	m := overlayWithBackends(t, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.switchTeamMember("lead")

	if m.hideComposer() {
		t.Fatal("the composer must be visible while a member is bound")
	}
	if m.teamOverlayModal() {
		t.Fatal("a bound session is not modal")
	}
	for _, r := range "hi" {
		if _, _, consumed := m.handleTeamKey(tea.KeyPressMsg{Code: r}); consumed {
			t.Fatalf("%q must fall through to the composer", r)
		}
	}
	// The reserved keys are still the session's.
	for _, key := range []tea.KeyPressMsg{
		{Code: tea.KeyDown, Mod: tea.ModCtrl},
		{Code: tea.KeyUp, Mod: tea.ModCtrl},
		{Code: tea.KeyEsc},
	} {
		if _, _, consumed := m.handleTeamKey(key); !consumed {
			t.Errorf("%v must stay reserved for the session", key.String())
		}
	}
}

// TestRosterKeepsFullKeyboard pins the other half: the management page and its
// transient states stay modal, so a letter there is a roster command and never
// reaches the hidden composer.
func TestRosterKeepsFullKeyboard(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openRoster(t)
	if !m.hideComposer() {
		t.Fatal("the roster must hide the composer")
	}
	if !m.teamOverlayModal() {
		t.Fatal("the roster must be modal")
	}
	if _, _, consumed := m.handleTeamKey(tea.KeyPressMsg{Code: 'a'}); !consumed {
		t.Error("the roster must consume every key")
	}
}

// TestBoundSessionSubmitReachesTheMemberBackend pins the architectural payoff:
// the composer submits through m.ctrl, which IS the bound member's backend, so
// no separate team send path exists to drift from the chat's own.
func TestBoundSessionSubmitReachesTheMemberBackend(t *testing.T) {
	m := overlayWithBackends(t, map[string][]provider.Message{
		"lead": {userMessage("LEAD-HISTORY")},
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.switchTeamMember("lead")

	if got := m.ctrl.Label(); got != "lead" {
		t.Fatalf("the submit target must be the bound member, got %q", got)
	}
	// A draft belongs to the backend it was typed for: switching clears it rather
	// than aiming it at another member.
	m.input.SetValue("for the leader")
	m.switchTeamMember("alice")
	if got := m.input.Value(); got != "" {
		t.Errorf("switching member must clear the draft, got %q", got)
	}
	if joined := strings.Join(m.transcript, "\n"); strings.Contains(joined, "LEAD-HISTORY") {
		t.Error("the outgoing member's transcript must not linger")
	}
}

// TestMemberModelRebindsAgentUser pins R2.4 (§8-5 拍板): /model inside a bound
// member session changes THAT MEMBER's agent user — a member's model is whatever
// its pool entry configures — and retires the stale backend, binding a freshly
// assembled one immediately so the window never serves the old provider. The
// chat's own model is untouched.
func TestMemberModelRebindsAgentUser(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	if err := m.teamPick.store.AddAgentUser(team.AgentUser{
		UserID: "pool-b", Provider: "openai", Model: "gpt", BaseURL: "https://x/v1",
	}); err != nil {
		t.Fatal(err)
	}
	closed := 0
	m.memberEvents = make(chan memberEvent, 4)
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return stubBackend{label: b.AgentUserRef, closed: &closed}, nil
	}, 4)
	m.teamBackends.setFingerprint(func(b team.MemberBinding) (string, error) {
		return b.AgentUserRef, nil
	})
	m.teamPick.backends = m.teamBackends
	m.switchTeamMember("lead")
	chatModel := m.ambient.ModelRef()

	m.runModelSubcommand("/model pool-b")

	slot, ok := m.teamPick.slotOf("lead")
	if !ok {
		t.Fatal("the leader slot must still exist")
	}
	if slot.AgentUserRef != "pool-b" {
		t.Errorf("member agent user = %q, want pool-b", slot.AgentUserRef)
	}
	if closed != 1 {
		t.Errorf("the stale backend must be retired, closed = %d", closed)
	}
	if _, still := m.teamBackends.bound("alpha", "lead"); !still {
		t.Error("a freshly assembled backend must be bound immediately after a model change")
	}
	if m.modelRef != "pool-b/model" {
		t.Errorf("the window must show the rebound backend, got %q", m.modelRef)
	}
	if m.ambient.ModelRef() != chatModel {
		t.Errorf("the chat's own backend must not move, got %q want %q", m.ambient.ModelRef(), chatModel)
	}
	// The change is persisted, not just in memory.
	doc := readStoredTeamDoc(t)
	if got := doc.Teams[0].Template[0].AgentUserRef; got != "pool-b" {
		t.Errorf("persisted agent user = %q, want pool-b", got)
	}
}

// TestMemberModelPickerListsThePool pins the no-argument form: it offers the pool
// as the member's model catalog and marks the current entry.
func TestMemberModelPickerListsThePool(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	for _, id := range []string{"pool-a", "pool-b"} {
		if err := m.teamPick.store.AddAgentUser(team.AgentUser{
			UserID: id, Provider: "openai", Model: "gpt", BaseURL: "https://x/v1",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.teamPick.store.BindAgentUser("alpha", "lead", "pool-b"); err != nil {
		t.Fatal(err)
	}
	if err := m.teamPick.reload("lead"); err != nil {
		t.Fatal(err)
	}

	m.runModelSubcommand("/model")
	if m.quickPick == nil {
		t.Fatal("/model in a bound session must open the member's agent-user picker")
	}
	if m.quickPick.kind != quickPickerMemberAgentUser {
		t.Errorf("picker kind = %q", m.quickPick.kind)
	}
	if len(m.quickPick.items) != 2 {
		t.Fatalf("picker should list both pool entries, got %d", len(m.quickPick.items))
	}
	if got := m.quickPick.items[m.quickPick.selected].ID; got != "pool-b" {
		t.Errorf("the member's current entry must be preselected, got %q", got)
	}
}

// TestStatusMemberButtonsRenderAndBind pins R3, the last piece of the original
// ask: the status line lists the open team's members beside [ TEAM ], a click on
// a member button binds that member's Agent, and the bound one is highlighted.
//
// The frame is rendered while the ambient controller is still bound: the status
// line reads sub-ports a partial stand-in does not implement (§8-12), so the
// click is located first and the stub is only bound by the click itself.
func TestStatusMemberButtonsRenderAndBind(t *testing.T) {
	m := overlayWithBackends(t, map[string][]provider.Message{
		"alice": {userMessage("ALICE-HISTORY")},
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = next.(chatTUI)

	status := ansi.Strip(m.appendTeamButton(""))
	for _, want := range []string{teamButtonText, memberButtonText("lead"), memberButtonText("alice")} {
		if !strings.Contains(status, want) {
			t.Errorf("status row missing %q, got %q", want, status)
		}
	}

	frame := strings.Split(m.View().Content, "\n")
	x, y := -1, -1
	for row, line := range frame {
		if before, _, found := strings.Cut(ansi.Strip(line), memberButtonText("alice")); found {
			x, y = visibleWidth(before), row
			break
		}
	}
	if x < 0 {
		t.Fatal("alice's button must appear in the rendered frame")
	}
	if got, teamEntry := m.teamStatusButtonHit(x, y); got != "alice" || teamEntry {
		t.Fatalf("hit at alice's button = (%q, %v)", got, teamEntry)
	}

	cmd, hit := m.handleTeamStatusClick(x, y)
	if !hit || cmd == nil {
		t.Fatal("clicking a member button must bind that member")
	}
	if got := m.ctrl.Label(); got != "alice" {
		t.Errorf("bound backend = %q, want alice", got)
	}
	if joined := strings.Join(m.transcript, "\n"); !strings.Contains(joined, "ALICE-HISTORY") {
		t.Errorf("the transcript must follow the clicked member:\n%s", joined)
	}
	// The bound member's button is styled differently from the rest.
	styled := m.appendTeamButton("")
	if !strings.Contains(styled, accent(memberButtonText("alice"))) {
		t.Error("the bound member's button must be highlighted")
	}
	if !strings.Contains(styled, dim(memberButtonText("lead"))) {
		t.Error("unbound members' buttons must stay dim")
	}
}

// TestStatusMemberButtonsAbsentWithoutTeam pins the scope: the plain chat shows
// only [ TEAM ], the management page shows no member row either — it is modal, so
// the mouse belongs to the terminal there and the buttons could not respond — and
// a bound session's row is bounded so a large team cannot crowd out the rest of
// the status line.
func TestStatusMemberButtonsAbsentWithoutTeam(t *testing.T) {
	ctrl := control.New(control.Options{})
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)
	if got := m.statusMemberIDs(); got != nil {
		t.Errorf("no team overlay means no member buttons, got %v", got)
	}
	if status := ansi.Strip(m.appendTeamButton("x")); !strings.HasSuffix(status, teamButtonText) {
		t.Errorf("plain chat status row = %q", status)
	}

	big := make([]team.MemberSlot, 0, statusMemberButtonLimit+3)
	for i, id := range []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8", "m9"} {
		big = append(big, team.MemberSlot{MemberID: id, Leader: i == 0, Status: team.MemberStatusActive})
	}
	writeTeamFixture(t, team.Team{Name: "big", Template: big})
	withTeam := openTeamOverlay(t) // the leader's session opens, so the row renders
	if got := len(withTeam.statusMemberIDs()); got != statusMemberButtonLimit {
		t.Errorf("member row = %d buttons, want the %d cap", got, statusMemberButtonLimit)
	}
	// Back on the management page the row goes away with the session.
	parked := teamKey(withTeam, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := parked.statusMemberIDs(); got != nil {
		t.Errorf("the modal management page must offer no member buttons, got %v", got)
	}
}

// TestLeaderResetClearsMemberSessionFiles pins the promise the three-stage
// step-down makes: it clears every member's history. After D5 a member's history
// IS its Reasonix session file, so that is what must be deleted — clearing only
// the pre-D5 context tree would leave the real transcripts on disk while telling
// the user they were removed.
func TestLeaderResetClearsMemberSessionFiles(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	dir := t.TempDir()
	m.teamPick.sessionDir = dir

	var paths []string
	for _, id := range []string{"lead", "alice"} {
		name, err := team.MemberSessionFile("alpha", id)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(`{"messages":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	// A file belonging to another team must survive.
	other := filepath.Join(dir, "team-other-lead.json")
	if err := os.WriteFile(other, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := m.teamPick.clearTeamHistories("alpha"); err != nil {
		t.Fatalf("clearTeamHistories: %v", err)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s must be deleted, stat err = %v", filepath.Base(path), err)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another team's session file must survive: %v", err)
	}
	// Idempotent: a second clear over already-missing files is not an error.
	if err := m.teamPick.clearTeamHistories("alpha"); err != nil {
		t.Errorf("a repeated clear must be idempotent, got %v", err)
	}
}
