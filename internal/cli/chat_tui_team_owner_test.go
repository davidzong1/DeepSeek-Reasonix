package cli

// Owner-scoping regression tests: the pinned To-do panel and the member-property
// draft are state derived from one session and may not be carried into or
// published onto another — the two defects the T0 matrix saw on ed4881e7f.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// todoWriteResult builds the successful todo_write ToolResult a member's agent
// emits; TodoWritten is what makes it canonical for the panel.
func todoWriteResult(contents ...string) event.Event {
	todos := make([]event.Todo, 0, len(contents))
	for _, c := range contents {
		todos = append(todos, event.Todo{Content: c, Status: "in_progress"})
	}
	return event.Event{Kind: event.ToolResult, Tool: event.Tool{
		ID: "todo-1", Name: "todo_write", Output: "Todos updated", TodoWritten: true, Todos: todos,
	}}
}

// boundTeamTUI binds the overlay to a member and returns the sized TUI, the
// shape every To-do scoping test starts from.
func boundTeamTUI(t *testing.T, member string) chatTUI {
	t.Helper()
	m := overlayWithBackends(t, map[string][]provider.Message{
		"lead":  {userMessage("lead-history")},
		"alice": {userMessage("alice-history")},
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	if cmd := m.switchTeamMember(member); cmd == nil {
		t.Fatalf("binding member %q must succeed", member)
	}
	return m
}

// TestTodoPanelScopedToBoundMember pins the core isolation defect: a member's
// committed list must render only while that member is the one bound. Switching
// away drops it, and the member's own buffered turn replays under its own owner
// when the window returns.
func TestTodoPanelScopedToBoundMember(t *testing.T) {
	m := boundTeamTUI(t, "lead")
	m.handleMemberEvent(memberEventMsg{member: "lead", ev: todoWriteResult("LEAD-TODO")})
	if got := ansi.Strip(m.renderTodoPanel()); !strings.Contains(got, "LEAD-TODO") {
		t.Fatalf("the bound member's own todo_write must mount its panel, got:\n%s", got)
	}

	m.switchTeamMember("alice")
	if got := ansi.Strip(m.renderTodoPanel()); strings.Contains(got, "LEAD-TODO") {
		t.Fatalf("switching members must not carry the previous member's panel, got:\n%s", got)
	}

	// A result for the member the window has left is buffered for that member
	// rather than ingested, so it cannot mount on the one now bound.
	m.handleMemberEvent(memberEventMsg{member: "lead", ev: todoWriteResult("LEAD-LATE")})
	if got := ansi.Strip(m.renderTodoPanel()); strings.Contains(got, "LEAD-LATE") {
		t.Fatalf("an event for another member must not mount on the bound one, got:\n%s", got)
	}
	// Switching back replays the buffer under its own owner, so the member's
	// own list is what the window shows on its return.
	m.switchTeamMember("lead")
	if got := ansi.Strip(m.renderTodoPanel()); !strings.Contains(got, "LEAD-LATE") {
		t.Fatalf("switching back must replay the member's own list, got:\n%s", got)
	}
}

// TestTodoPanelRestoresEachBoundSession pins the switch lifecycle: binding
// clears the outgoing panel to prevent leakage, then restores the incoming
// controller's committed projection. Returning to a member (including leader)
// must show that member's existing list without needing a new todo_write event.
func TestTodoPanelRestoresEachBoundSession(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	m.memberEvents = newMemberEventPump()
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		var todos []evidence.TodoItem
		switch b.MemberID {
		case "lead":
			todos = []evidence.TodoItem{{Content: "LEAD-EXISTING", Status: "in_progress"}}
		case "alice":
			todos = []evidence.TodoItem{{Content: "ALICE-EXISTING", Status: "pending"}}
		}
		return stubBackend{label: b.MemberID, todos: todos}, nil
	}, 4)
	m = sized(t, m)

	m.switchTeamMember("lead")
	if got := ansi.Strip(m.renderTodoPanel()); !strings.Contains(got, "LEAD-EXISTING") {
		t.Fatalf("binding leader must restore its committed list, got:\n%s", got)
	}
	m.switchTeamMember("alice")
	if got := ansi.Strip(m.renderTodoPanel()); !strings.Contains(got, "ALICE-EXISTING") || strings.Contains(got, "LEAD-EXISTING") {
		t.Fatalf("binding alice must show only her committed list, got:\n%s", got)
	}
	m.switchTeamMember("lead")
	if got := ansi.Strip(m.renderTodoPanel()); !strings.Contains(got, "LEAD-EXISTING") || strings.Contains(got, "ALICE-EXISTING") {
		t.Fatalf("switching back to leader must restore only the leader list, got:\n%s", got)
	}
}

// TestTodoPanelIgnoresAmbientEventsWhileBound pins the owner check on the
// ambient route: the chat's own session can still be running a turn while the
// window is on a member, and its events must neither mount on nor clear the
// member's panel.
func TestTodoPanelIgnoresAmbientEventsWhileBound(t *testing.T) {
	m := boundTeamTUI(t, "lead")
	m.handleMemberEvent(memberEventMsg{member: "lead", ev: todoWriteResult("LEAD-TODO")})

	var drained agentEventDrain
	m.consumeAgentEvent(todoWriteResult("AMBIENT-TODO"), &drained)
	if got := ansi.Strip(m.renderTodoPanel()); strings.Contains(got, "AMBIENT-TODO") {
		t.Fatalf("the ambient session's result must not take over the member's panel, got:\n%s", got)
	}
	m.consumeAgentEvent(event.Event{Kind: event.TurnStarted}, &drained)
	if got := ansi.Strip(m.renderTodoPanel()); !strings.Contains(got, "LEAD-TODO") {
		t.Fatalf("the ambient session's turn boundary must not clear the member's panel, got:\n%s", got)
	}
}

// TestTodoPanelResetByMemberTurnStarted pins the missing turn boundary: the
// panel resets at the host's TurnStarted, and a member's turn reaches that
// boundary only through the event router — before this, member events had no
// TurnStarted case at all and the list survived its own turn.
func TestTodoPanelResetByMemberTurnStarted(t *testing.T) {
	m := boundTeamTUI(t, "lead")
	m.handleMemberEvent(memberEventMsg{member: "lead", ev: todoWriteResult("LEAD-TODO")})
	if got := ansi.Strip(m.renderTodoPanel()); !strings.Contains(got, "LEAD-TODO") {
		t.Fatalf("panel should hold the member's list before its next turn, got:\n%s", got)
	}
	m.handleMemberEvent(memberEventMsg{member: "lead", ev: event.Event{Kind: event.TurnStarted}})
	if got := ansi.Strip(m.renderTodoPanel()); strings.Contains(got, "LEAD-TODO") {
		t.Fatalf("a member's own TurnStarted must reset its panel, got:\n%s", got)
	}
}

// TestTodoPanelUntouchedByAnotherMembersTurn pins the other half of the owner
// check: a turn boundary belonging to a member the window is not showing must
// not wipe the list it is showing.
func TestTodoPanelUntouchedByAnotherMembersTurn(t *testing.T) {
	m := boundTeamTUI(t, "lead")
	m.handleMemberEvent(memberEventMsg{member: "lead", ev: todoWriteResult("LEAD-TODO")})

	// alice's turn starts in the background: her event buffers (she is not
	// bound) and must leave the lead's mounted panel alone.
	m.handleMemberEvent(memberEventMsg{member: "alice", ev: event.Event{Kind: event.TurnStarted}})
	if got := ansi.Strip(m.renderTodoPanel()); !strings.Contains(got, "LEAD-TODO") {
		t.Fatalf("another member's turn must not clear the mounted panel, got:\n%s", got)
	}
}

// TestTodoPanelClearedByTeamSessionExit pins the bind path out of the team: the
// panel is re-owned by the ambient session when the window leaves the member.
func TestTodoPanelClearedByTeamSessionExit(t *testing.T) {
	m := boundTeamTUI(t, "lead")
	m.handleMemberEvent(memberEventMsg{member: "lead", ev: todoWriteResult("LEAD-TODO")})
	m.closeSession()
	if got := ansi.Strip(m.renderTodoPanel()); strings.Contains(got, "LEAD-TODO") {
		t.Fatalf("leaving the member session must drop its panel, got:\n%s", got)
	}
	if owner := m.todo.owner; owner != (ownerKey{}) {
		t.Fatalf("the panel must be re-owned by the ambient session, got %+v", owner)
	}
}

// TestTodoCommandDismissesOnlyItsOwnOwner pins /todo: the dismissal is the
// mounted view's own preference, so it may only apply to the list the command
// was issued against.
func TestTodoCommandDismissesOnlyItsOwnOwner(t *testing.T) {
	m := boundTeamTUI(t, "lead")
	m.handleMemberEvent(memberEventMsg{member: "lead", ev: todoWriteResult("LEAD-TODO")})

	// A dismissal aimed at another owner leaves the mounted panel alone.
	m.todo.dismiss(ownerKey{Team: "alpha", Member: "alice"})
	if got := ansi.Strip(m.renderTodoPanel()); !strings.Contains(got, "LEAD-TODO") {
		t.Fatalf("a dismissal for another owner must not hide the mounted panel, got:\n%s", got)
	}
	m.todo.dismiss(m.todo.owner)
	if got := ansi.Strip(m.renderTodoPanel()); strings.Contains(got, "LEAD-TODO") {
		t.Fatalf("the mounted owner's dismissal must hide the panel, got:\n%s", got)
	}
}

// memberOwnerFixture is a two-member team with a pool, so the editor's fields
// have real references to validate against.
func memberOwnerFixture() team.Team {
	return team.Team{Name: "alpha", AgentUserPool: []string{"au-1", "au-2", "au-3"}, Template: []team.MemberSlot{
		{MemberID: "lead", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive, AgentUserRef: "au-1"},
		{MemberID: "bob", Role: team.RoleTester, Status: team.MemberStatusActive, AgentUserRef: "au-2"},
	}}
}

// openBobEditor opens the member-property editor on the non-leader member, the
// draft whose owner every mismatch test then moves out from under it.
func openBobEditor(t *testing.T) chatTUI {
	t.Helper()
	writeTeamPoolFixture(t, []team.Team{memberOwnerFixture()}, poolRosterUsers())
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // focus bob, the non-leader
	m = teamKey(m, tea.KeyPressMsg{Code: 'e'})         // descend into the editor
	if got := m.teamPick.memberEdit; got.kind != memberEditFieldList || got.owner.Member != "bob" {
		t.Fatalf("editor should be armed on bob, got kind=%v owner=%+v", got.kind, got.owner)
	}
	return m
}

// storedMember returns one member's persisted slot, read back from the document
// the overlay writes.
func storedMember(t *testing.T, id string) team.MemberSlot {
	t.Helper()
	for _, slot := range readStoredTeamDoc(t).Teams[0].Template {
		if slot.MemberID == id {
			return slot
		}
	}
	t.Fatalf("member %q is not in the stored document", id)
	return team.MemberSlot{}
}

// TestMemberEditRefusesDraftOwnedByAnotherMember pins the draft's ownership: a
// draft armed on bob, with the roster's focus moved to lead, must not publish
// bob's values onto lead — it is discarded, nothing is written, and the refusal
// is visible.
func TestMemberEditRefusesDraftOwnedByAnotherMember(t *testing.T) {
	m := openBobEditor(t)
	m.teamPick.memberEdit.draft.AgentUserRef = "au-3" // bob's draft, a real change
	m.teamPick.model.FocusMember("lead")              // the focus moved out from under it

	m = teamKey(m, tea.KeyPressMsg{Code: 's'})

	if got := storedMember(t, "lead"); got.AgentUserRef != "au-1" {
		t.Fatalf("bob's draft must not be published onto lead, lead = %q", got.AgentUserRef)
	}
	if got := storedMember(t, "bob"); got.AgentUserRef != "au-2" {
		t.Fatalf("the refused save must write nothing at all, bob = %q", got.AgentUserRef)
	}
	if kind := m.teamPick.memberEdit.kind; kind != memberEditNone {
		t.Fatalf("the stale draft must be discarded, kind = %v", kind)
	}
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "bob") {
		t.Fatalf("the refusal must be visible on the page, got:\n%s", got)
	}
}

// TestMemberEditDroppedWhenItsMemberDisappears pins the delete path: a draft
// armed on a member that is no longer in the roster is discarded by the reload
// that discovered it, before any save can publish it to the neighbour the
// cursor falls back to.
func TestMemberEditDroppedWhenItsMemberDisappears(t *testing.T) {
	m := openBobEditor(t)
	m.teamPick.memberEdit.draft.AgentUserRef = "au-3"

	// A removal that did not go through this overlay's own delete path — the
	// remote edit the 1s roster poll exists to notice.
	if err := m.teamPick.store.DeleteMember("alpha", "bob"); err != nil {
		t.Fatal(err)
	}
	if err := m.teamPick.reload(""); err != nil {
		t.Fatal(err)
	}

	if kind := m.teamPick.memberEdit.kind; kind != memberEditNone {
		t.Fatalf("a draft whose member is gone must be discarded, kind = %v", kind)
	}
	if got := storedMember(t, "lead"); got.AgentUserRef != "au-1" {
		t.Fatalf("the vanished member's draft must not reach the focused one, lead = %q", got.AgentUserRef)
	}
}

// TestMemberEditSaveIsAllOrNothing pins the publication boundary: every changed
// field is validated before the first one is written, so a draft that fails on
// its last row cannot leave the earlier rows published. The order matters —
// "agent" is published before "role", which is the field that refuses here.
func TestMemberEditSaveIsAllOrNothing(t *testing.T) {
	m := openBobEditor(t)
	me := &m.teamPick.memberEdit
	me.draft.AgentUserRef = "au-3"                // valid, and written first
	me.draft.Role = team.RoleID("bad\x01control") // refused: a control character

	m = teamKey(m, tea.KeyPressMsg{Code: 's'})

	if got := storedMember(t, "bob"); got.AgentUserRef != "au-2" {
		t.Fatalf("a refused save must publish none of its fields, agent = %q", got.AgentUserRef)
	}
	if got := storedMember(t, "bob"); got.Role != team.RoleTester {
		t.Fatalf("a refused save must not write the role either, role = %q", got.Role)
	}
	if kind := me.kind; kind != memberEditFieldList {
		t.Fatalf("a refused save keeps the editor open on the field, kind = %v", kind)
	}
	if me.errMsg == "" {
		t.Fatal("the refusal must be visible on the field that caused it")
	}
}

// TestMemberEditSavesItsOwnDraft pins that the guards did not break the happy
// path: an untouched-owner draft still publishes.
func TestMemberEditSavesItsOwnDraft(t *testing.T) {
	m := openBobEditor(t)
	m.teamPick.memberEdit.draft.AgentUserRef = "au-3"
	m = teamKey(m, tea.KeyPressMsg{Code: 's'})

	if got := storedMember(t, "bob"); got.AgentUserRef != "au-3" {
		t.Fatalf("the draft's own save must publish, bob = %q", got.AgentUserRef)
	}
	if got := storedMember(t, "lead"); got.AgentUserRef != "au-1" {
		t.Fatalf("the save must not touch another member, lead = %q", got.AgentUserRef)
	}
}
