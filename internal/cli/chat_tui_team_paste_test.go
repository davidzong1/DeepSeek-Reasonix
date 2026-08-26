package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/team"
)

// TestTeamPasteIntoAddTeamInput pins the bracketed-paste path in a team
// overlay input state: with the add-team input armed (a), a tea.PasteMsg
// appends to the name buffer — never to the hidden composer — and enter
// confirms the pasted name into the registry.
func TestTeamPasteIntoAddTeamInput(t *testing.T) {
	t.Chdir(t.TempDir())
	m := openTeamOverlay(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})

	next, _ := m.Update(tea.PasteMsg{Content: "acme corp"})
	m = next.(chatTUI)
	if got := m.teamPick.buf; got != "acme corp" {
		t.Fatalf("pasted name = %q, want %q", got, "acme corp")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("paste leaked into the composer: %q", got)
	}

	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	doc := readStoredTeamDoc(t)
	if len(doc.Teams) != 1 || doc.Teams[0].Name != "acme corp" {
		t.Fatalf("pasted name did not publish: %+v", doc.Teams)
	}
}

// TestTeamPasteIntoPoolFieldEditor pins the paste path on the pool field
// editor: opening the id row of a new entry (a, enter) and pasting appends
// to that field's buffer, which enter merges into the draft and s persists.
func TestTeamPasteIntoPoolFieldEditor(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{UserID: "au-1"})
	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the id row

	next, _ := m.Update(tea.PasteMsg{Content: "au-pasted"})
	m = next.(chatTUI)
	if got := m.teamPick.pool.buf; got != "au-pasted" {
		t.Fatalf("pasted id = %q, want %q", got, "au-pasted")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("paste leaked into the composer: %q", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // merge into the draft
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})
	users := readStoredPool(t)
	if len(users) != 2 || users[1].UserID != "au-pasted" {
		t.Fatalf("pasted id did not publish: %+v", users)
	}
}

// TestTeamPasteInertOutsideInputStateKeepsComposerClean pins the modal
// boundary: a paste while the overlay is open but not in a text input state
// is dropped — the hidden composer and its paste-block list stay untouched,
// so the chat input is exactly as the user left it after TEAM closes.
func TestTeamPasteInertOutsideInputStateKeepsComposerClean(t *testing.T) {
	t.Chdir(t.TempDir())
	m := openTeamOverlay(t)
	m.input.SetValue("draft before")

	next, _ := m.Update(tea.PasteMsg{Content: strings.Repeat("x", 200) + "\n" + strings.Repeat("y", 200)})
	m = next.(chatTUI)
	if got := m.input.Value(); got != "draft before" {
		t.Fatalf("composer changed: %q", got)
	}
	if len(m.pastedBlocks) != 0 {
		t.Fatalf("paste created a block outside an input state: %+v", m.pastedBlocks)
	}
	if got := m.teamPick.buf; got != "" {
		t.Fatalf("paste touched the team buffer: %q", got)
	}

	// Esc closes the overlay; the composer still carries exactly the draft.
	next, _ = m.handleTeamPickerKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = next.(chatTUI)
	if m.teamPick != nil {
		t.Fatal("esc should close the overlay")
	}
	if got := m.input.Value(); got != "draft before" {
		t.Fatalf("composer after TEAM close = %q, want the untouched draft", got)
	}
}

// TestTeamPasteInertOnProviderPicker pins that the provider picker — a choice
// control, never a text field — ignores pastes like it ignores letters.
func TestTeamPasteInertOnProviderPicker(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{
		UserID: "au-1", Identity: "alice", Provider: "anthropic",
	})
	m := openPool(t)
	m = poolEditOpenProvider(m, team.AgentUser{UserID: "au-1", Identity: "alice", Provider: "anthropic"})
	before := m.teamPick.pool.buf

	next, _ := m.Update(tea.PasteMsg{Content: "deepseek"})
	m = next.(chatTUI)
	if got := m.teamPick.pool.buf; got != before {
		t.Fatalf("paste moved the picker buffer: %q -> %q", before, got)
	}
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "Claude (Anthropic)") {
		t.Fatalf("picker selection changed, got:\n%s", got)
	}
}

// TestTeamPasteKeyCtrlVArmsClipboardRead pins the key path: while a text input
// is active, ctrl+v reads the native clipboard (the same binding the composer
// has, for terminals that forward the key instead of bracketed-pasting), and
// the resulting text lands in the overlay buffer. Outside an input state the
// key is inert.
func TestTeamPasteKeyCtrlVArmsClipboardRead(t *testing.T) {
	t.Chdir(t.TempDir())
	setLocalClipboardSession(t)
	prev := readNativeClipboardText
	readNativeClipboardText = func() (string, error) { return "clip text", nil }
	t.Cleanup(func() { readNativeClipboardText = prev })

	m := openTeamOverlay(t)
	// Inert outside an input state.
	next, cmd := m.handleTeamPickerKey(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	m = next.(chatTUI)
	if cmd != nil {
		t.Fatal("ctrl+v must be inert outside an input state")
	}

	// Armed in the add-team input; the async result lands in the buffer.
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	_, cmd = m.handleTeamPickerKey(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+v in an input state must arm a clipboard read")
	}
	next, _ = m.Update(cmd())
	m = next.(chatTUI)
	if got := m.teamPick.buf; got != "clip text" {
		t.Fatalf("clipboard text = %q, want %q", got, "clip text")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("clipboard text leaked into the composer: %q", got)
	}
}

// TestTeamOverlayViewReleasesMouse pins the overlay's mouse contract: while
// TEAM is open the fullscreen view requests MouseModeNone (the terminal's
// native click-drag selection), overriding the captured mode, and closing the
// overlay restores MouseModeCellMotion.
func TestTeamOverlayViewReleasesMouse(t *testing.T) {
	t.Chdir(t.TempDir())
	m := openTeamOverlay(t)
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("MouseMode with TEAM open = %v, want MouseModeNone", got)
	}

	next, _ := m.handleTeamPickerKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = next.(chatTUI)
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("MouseMode after TEAM close = %v, want MouseModeCellMotion", got)
	}
}

// TestTeamPasteKeyShiftInsertInPoolField pins shift+insert on the pool field
// editor: the classic paste key reads the clipboard into the open field, the
// same way it does in the composer.
func TestTeamPasteKeyShiftInsertInPoolField(t *testing.T) {
	writeAgentUsersFixture(t, team.AgentUser{UserID: "au-1"})
	setLocalClipboardSession(t)
	prev := readNativeClipboardText
	readNativeClipboardText = func() (string, error) { return "au-shift", nil }
	t.Cleanup(func() { readNativeClipboardText = prev })

	m := openPool(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'a'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the id row
	_, cmd := m.handleTeamPickerKey(tea.KeyPressMsg{Code: tea.KeyInsert, Mod: tea.ModShift})
	if cmd == nil {
		t.Fatal("shift+insert in a field edit must arm a clipboard read")
	}
	next, _ := m.Update(cmd())
	m = next.(chatTUI)
	if got := m.teamPick.pool.buf; got != "au-shift" {
		t.Fatalf("field buffer = %q, want %q", got, "au-shift")
	}
}

// TestTeamPasteInertOnMemberPickerRows pins that the member editor's open
// option list — a choice control like the provider picker — ignores pastes:
// nothing enters the pick, the draft, or the composer.
func TestTeamPasteInertOnMemberPickerRows(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'})          // member editor
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})  // status → proxy
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // open the proxy picker
	before := m.teamPick.memberEdit

	next, _ := m.Update(tea.PasteMsg{Content: "on"})
	m = next.(chatTUI)
	got := m.teamPick.memberEdit
	if got.kind != before.kind || got.edit != before.edit || got.errMsg != before.errMsg ||
		got.draft != before.draft || got.list.kind != before.list.kind ||
		got.list.cursor != before.list.cursor || got.list.offset != before.list.offset ||
		len(got.list.options) != len(before.list.options) ||
		len(got.list.selected) != len(before.list.selected) {
		t.Fatalf("paste moved the member editor: %+v -> %+v", before, got)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("paste leaked into the composer: %q", got)
	}
}

// TestTeamPasteInertInSessionWindow pins the session window: it is a display
// surface with no text input, so a paste while it is open is dropped — the
// window stays up and the composer stays clean.
func TestTeamPasteInertInSessionWindow(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // focus the leader
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})         // open the session window

	next, _ := m.Update(tea.PasteMsg{Content: "session paste"})
	m = next.(chatTUI)
	if !m.teamPick.session.active {
		t.Fatal("paste must not close the session window")
	}
	// The composer is the bound member's input, so a paste lands there — the
	// session no longer owns a draft of its own.
	if got := m.input.Value(); got != "session paste" {
		t.Fatalf("composer = %q, want the pasted text", got)
	}
	if len(m.pastedBlocks) != 0 {
		t.Fatalf("paste created a block in the session window: %+v", m.pastedBlocks)
	}
}

// TestTeamPasteInertInDeleteConfirm pins the delete confirmations: no text is
// being typed while a delete awaits Enter, so a paste is dropped and the
// confirmation stays armed (it must not accelerate or cancel the delete).
func TestTeamPasteInertInDeleteConfirm(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "alpha", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	before := storedTeamBytes(t)
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 'd'}) // arm the member delete

	next, _ := m.Update(tea.PasteMsg{Content: "enter"})
	m = next.(chatTUI)
	if got := m.teamPick.kind; got != teamInputDeleteMember {
		t.Fatalf("paste must leave the delete confirmation armed, got %v", got)
	}
	if got := m.teamPick.buf; got != "" {
		t.Fatalf("paste touched the team buffer: %q", got)
	}
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("paste must not write team.json")
	}
}

// TestTeamPasteIntoLeaderResetID pins the bracketed-paste path on the step-down
// confirmation's exact-id stage (§6): the leader id is typed into a text
// buffer, so a paste lands there — and a pasted id still has to match exactly
// before the list stage arms.
func TestTeamPasteIntoLeaderResetID(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)                                  // leaders first: the roster opens on the pinned leader
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})          // warn stage
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // exact-id stage

	next, _ := m.Update(tea.PasteMsg{Content: "lead"})
	m = next.(chatTUI)
	if got := m.teamPick.reset.buf; got != "lead" {
		t.Fatalf("pasted id = %q, want %q", got, "lead")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("paste leaked into the composer: %q", got)
	}

	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // the pasted id confirms
	if got := m.teamPick.reset.kind; got != leaderResetList {
		t.Fatalf("the pasted id should pass the exact match, got stage %v", got)
	}
}

// TestTeamPasteRestoresComposerAfterClose pins the exit boundary: once TEAM
// closes, the modal intercept is gone and a paste lands in the composer again
// — the hidden-composer guard never lingers past the overlay.
func TestTeamPasteRestoresComposerAfterClose(t *testing.T) {
	t.Chdir(t.TempDir())
	m := openTeamOverlay(t)
	next, _ := m.handleTeamPickerKey(tea.KeyPressMsg{Code: tea.KeyEsc}) // close TEAM
	m = next.(chatTUI)
	if m.teamPick != nil {
		t.Fatal("esc should close the overlay")
	}

	next, _ = m.Update(tea.PasteMsg{Content: "back to chat"})
	m = next.(chatTUI)
	if got := m.input.Value(); got != "back to chat" {
		t.Fatalf("composer after TEAM close = %q, want the pasted text", got)
	}
}

// TestTeamClipboardImageResultWhileOverlayOpen pins the one async clipboard
// path that bypasses applyComposerPasteCount: an image paste result (armed in
// the composer before TEAM opened) must not attach to the hidden composer —
// the modal intercept covers image results too, not just text.
func TestTeamClipboardImageResultWhileOverlayOpen(t *testing.T) {
	t.Chdir(t.TempDir())
	m := openTeamOverlay(t)
	m.input.SetValue("draft before")

	next, _ := m.Update(clipboardImageMsg{path: "shots/one.png"})
	m = next.(chatTUI)
	if got := m.input.Value(); got != "draft before" {
		t.Fatalf("image paste leaked into the hidden composer: %q", got)
	}
	if len(m.pastedBlocks) != 0 {
		t.Fatalf("image paste created a block: %+v", m.pastedBlocks)
	}
	if m.clipboardImagePending {
		t.Fatal("image paste state must reset even when the result is dropped")
	}
}

// TestTeamMouseRightPasteIntoSessionInput pins the §11.7 mouse seam on the
// right-click path: while the session composer is focused, a right-click arms
// the text-clipboard read and the result lands in the session draft — never
// the hidden composer. The overlay's browsing state has no text target, so the
// same click stays inert there. (The overlay requests MouseModeNone, so this
// is the belt-and-braces seam for terminals that deliver clicks anyway.)
func TestTeamMouseRightPasteIntoSessionInput(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	setLocalClipboardSession(t)
	prev := readNativeClipboardText
	readNativeClipboardText = func() (string, error) { return "clip session", nil }
	t.Cleanup(func() { readNativeClipboardText = prev })

	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // focus the leader
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})         // open the session window

	next, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseRight})
	m = next.(chatTUI)
	if cmd == nil {
		t.Fatal("right-click in a bound member session must arm a clipboard read")
	}
	next, _ = m.Update(cmd())
	m = next.(chatTUI)
	if got := m.input.Value(); got != "clip session" {
		t.Fatalf("composer = %q, want %q — the composer is the bound member's input", got, "clip session")
	}

	// Browsing state: the window is open but no text target — the click is inert.
	// Esc leaves the session for the management page, where the composer is
	// hidden again: a right-click there has no text target and stays inert.
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	next, cmd = m.Update(tea.MouseClickMsg{Button: tea.MouseRight})
	m = next.(chatTUI)
	if cmd != nil {
		t.Fatal("right-click without an active text target must stay inert")
	}
}

// TestTeamMouseMiddlePasteIntoSessionInput pins the middle-click half of the
// same seam: with the session composer focused, a middle click reads the
// selection owner and the resulting paste lands in the session draft.
func TestTeamMouseMiddlePasteIntoSessionInput(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	t.Setenv("TMUX", "") // force the primary-selection path, tmux or not
	prev := readPrimaryPasteSelection
	readPrimaryPasteSelection = func() (string, error) { return "primary session", nil }
	t.Cleanup(func() { readPrimaryPasteSelection = prev })

	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // focus the leader
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})         // open the session window

	next, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseMiddle})
	m = next.(chatTUI)
	if cmd == nil {
		t.Fatal("middle-click in a bound member session must arm a selection read")
	}
	next, _ = m.Update(cmd())
	m = next.(chatTUI)
	if got := m.input.Value(); got != "primary session" {
		t.Fatalf("composer = %q, want %q — the composer is the bound member's input", got, "primary session")
	}
}
