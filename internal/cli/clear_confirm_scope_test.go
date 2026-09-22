package cli

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/team"
)

// boundClearTUI binds a two-member team to a real controller per member, so a
// bound /clear rotates a real session path rather than a stub's empty one. It
// returns the TUI, the member's controller, and the ambient chat controller.
func boundClearTUI(t *testing.T, member string) (chatTUI, *control.Controller, *control.Controller) {
	t.Helper()
	writeTeamFixture(t, twoMemberTeam())
	ambient := newOwnedTestController(t, control.Options{SessionDir: t.TempDir(), Label: "chat"})
	m := newChatTUI(ambient, "", make(chan event.Event, 1), 80)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.onTeamButtonClick()
	if !m.teamPick.session.active {
		t.Fatal("the leader-backed fixture must open a team session window")
	}

	built := map[string]*control.Controller{}
	m.memberEvents = newMemberEventPump()
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		dir := t.TempDir()
		exec := agent.New(nil, nil, agent.NewSession("member-sys"), agent.Options{}, event.Discard)
		c := newOwnedTestController(t, control.Options{
			Executor: exec, SessionDir: dir, Label: b.MemberID,
			SystemPrompt: "member-sys", DisableColdResumePrune: true,
		})
		c.SetSessionPath(filepath.Join(dir, "member-"+b.MemberID+".jsonl"))
		if err := c.Snapshot(); err != nil {
			t.Fatal(err)
		}
		built[b.MemberID] = c
		return c, nil
	}, 4)
	if cmd := m.switchTeamMember(member); cmd == nil {
		t.Fatalf("binding member %q must succeed", member)
	}
	return m, built[member], ambient
}

// TestBoundClearScopesPromptAndNoticeToTheMember pins the D5 wording contract: a
// /clear issued while a member is bound destroys only that member's transcript,
// so both the confirmation and the result must name the team and member. A bare
// "current context" reads as the chat's own session — or the whole fleet — and
// the operator has no way to tell which history just went away.
func TestBoundClearScopesPromptAndNoticeToTheMember(t *testing.T) {
	m, memberCtrl, _ := boundClearTUI(t, "lead")
	memberPath := memberCtrl.SessionPath()

	if cmd := m.runSlashCommand("/clear"); cmd != nil {
		t.Fatal("/clear should open a local confirmation without returning a command")
	}
	prompt := ansi.Strip(m.renderClearConfirm())
	for _, want := range []string{"alpha", "lead", "untouched"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the bound confirmation must name the clear scope (%q missing):\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "deletes the current transcript from local history and keeps only the system prompt.") {
		t.Fatalf("the bound confirmation kept the unscoped wording:\n%s", prompt)
	}

	next, _ := m.handleClearConfirmKey(tea.KeyPressMsg{Code: 'y'})
	m = next.(chatTUI)
	notice := ansi.Strip(strings.Join(m.transcript, "\n"))
	for _, want := range []string{"alpha", "lead"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("the bound clear result must name the scope (%q missing):\n%s", want, notice)
		}
	}
	if memberCtrl.SessionPath() == memberPath {
		t.Fatal("a confirmed bound /clear must rotate the member's own session path")
	}
}

// TestBoundClearLeavesAmbientSessionAndLeaseAlone pins the isolation half of D5:
// the window's backend is the member's, so the chat's own controller and the
// ambient single-writer lease must come out of a bound /clear byte-for-byte
// unchanged. Repointing the ambient keeper at the member's fresh path would hand
// the chat's lease to a member's file and leave the chat session unguarded.
func TestBoundClearLeavesAmbientSessionAndLeaseAlone(t *testing.T) {
	m, memberCtrl, ambient := boundClearTUI(t, "lead")
	ambientPath := filepath.Join(t.TempDir(), "ambient-session.jsonl")
	ambient.SetSessionPath(ambientPath)
	if err := ambient.Snapshot(); err != nil {
		t.Fatal(err)
	}
	leases := control.NewSessionLeaseKeeper()
	defer leases.Release()
	if err := leases.Rebind(ambientPath); err != nil {
		t.Fatal(err)
	}
	m.leases = leases
	held := leases.HeldPath()

	m.runSlashCommand("/clear")
	next, _ := m.handleClearConfirmKey(tea.KeyPressMsg{Code: 'y'})
	m = next.(chatTUI)

	if got := ambient.SessionPath(); got != agent.CanonicalSessionPath(ambientPath) {
		t.Fatalf("a bound /clear rotated the chat's own session to %q", got)
	}
	if got := leases.HeldPath(); got != held {
		t.Fatalf("ambient lease after a bound /clear = %q, want %q unchanged", got, held)
	}
	if memberCtrl.SessionPath() == "" {
		t.Fatal("the member's own session must be the one that rotated")
	}
}

// TestUnboundClearKeepsTheUnscopedWording pins the other side: with no member
// bound the clear really is the chat's own session, so the original wording —
// and only it — is what the operator sees. A scope suffix leaking into the plain
// path would name a team that has nothing to do with the clear.
func TestUnboundClearKeepsTheUnscopedWording(t *testing.T) {
	m := boundClearTUIAmbient(t)
	if scope := m.clearScope(); scope != "" {
		t.Fatalf("an unbound window must not name a clear scope, got %q", scope)
	}
	m.runSlashCommand("/clear")
	prompt := ansi.Strip(m.renderClearConfirm())
	if !strings.Contains(prompt, "deletes the current transcript from local history") {
		t.Fatalf("the unbound confirmation must keep its original wording:\n%s", prompt)
	}
	if strings.Contains(prompt, "member") {
		t.Fatalf("the unbound confirmation must not mention a member:\n%s", prompt)
	}
}

// boundClearTUIAmbient is boundClearTUI's overlay without a member bind: the
// chat's own session is the clear target.
func boundClearTUIAmbient(t *testing.T) chatTUI {
	t.Helper()
	writeTeamFixture(t, twoMemberTeam())
	ambient := newOwnedTestController(t, control.Options{SessionDir: t.TempDir(), Label: "chat"})
	m := newChatTUI(ambient, "", make(chan event.Event, 1), 80)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return next.(chatTUI)
}

// TestBoundClearOnlyTouchesTheBoundMembersOwner pins the owner isolation the
// panel promises: a bound /clear resets the view for the bound member's owner
// and leaves every other member's mounted state — and the ambient session's —
// exactly as it was.
func TestBoundClearOnlyTouchesTheBoundMembersOwner(t *testing.T) {
	m, _, _ := boundClearTUI(t, "lead")
	m.handleMemberEvent(memberEventMsg{member: "lead", ev: todoWriteResult("LEAD-TODO")})
	if got := ansi.Strip(m.renderTodoPanel()); !strings.Contains(got, "LEAD-TODO") {
		t.Fatalf("precondition: the bound member's list must be mounted, got:\n%s", got)
	}

	m.runSlashCommand("/clear")
	next, _ := m.handleClearConfirmKey(tea.KeyPressMsg{Code: 'y'})
	m = next.(chatTUI)

	if got := ansi.Strip(m.renderTodoPanel()); strings.Contains(got, "LEAD-TODO") {
		t.Fatalf("a bound /clear must clear the bound member's own panel, got:\n%s", got)
	}
	// The owner key survives the reset, so the member's next list still mounts.
	if m.todo.owner != memberOwner("alpha", "lead") {
		t.Fatalf("the panel owner after a bound /clear = %+v, want the bound member", m.todo.owner)
	}
	m.handleMemberEvent(memberEventMsg{member: "lead", ev: todoWriteResult("LEAD-NEXT")})
	if got := ansi.Strip(m.renderTodoPanel()); !strings.Contains(got, "LEAD-NEXT") {
		t.Fatalf("the member's next list must mount on its own panel, got:\n%s", got)
	}
}
