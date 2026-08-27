package cli

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/team"
)

// TestMemberBindFailureReachesTranscript pins the visibility contract behind a
// real diagnosis: a member whose pool entry cannot assemble used to fail
// silently — switchTeamMember wrote session.errMsg and returned, and since R7
// made the detail panel opt-in, the only render site was off screen. The window
// then kept the chat's own backend, so the symptom read as "the team opened but
// shows the wrong model". The reason must reach the transcript, which is always
// on screen, and the window must stay honestly on the ambient backend.
func TestMemberBindFailureReachesTranscript(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openTeamOverlay(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = next.(chatTUI)
	ambient := m.ctrl.Label()
	m.memberEvents = make(chan memberEvent, 4)
	m.teamBackends = newTeamBackends(func(team.MemberBinding) (control.SessionAPI, error) {
		return nil, errors.New(`openai: provider "wanapi": effort "hight" must be low, medium, or high`)
	}, 4)
	m.teamPick.backends = m.teamBackends

	if cmd := m.switchTeamMember("lead"); cmd != nil {
		t.Fatal("a failed assembly must not arm the member event pump")
	}
	joined := strings.Join(m.transcript, "\n")
	for _, want := range []string{"member unavailable", `effort "hight"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("the refusal must be readable in the transcript (%q missing):\n%s", want, joined)
		}
	}
	if got := m.teamPick.session.errMsg; !strings.Contains(got, "member unavailable") {
		t.Errorf("the panel keeps the detail too, got %q", got)
	}
	if got := m.ctrl.Label(); got != ambient {
		t.Errorf("a refused bind must leave the chat's own backend in place, got %q want %q", got, ambient)
	}
	if m.ambient != nil {
		t.Error("nothing was bound, so no ambient backend should have been saved")
	}
}

// TestDryRunPoolEntryUsesTheAdapterAsAuthority pins where the effort vocabulary
// lives: with the adapter, not in a second whitelist here. The store gate accepts
// any well-formed field, so a typo only surfaces at construction — which is
// exactly what the editor now runs before it writes.
func TestDryRunPoolEntryUsesTheAdapterAsAuthority(t *testing.T) {
	base := team.AgentUser{UserID: "wanapi", Provider: "openai",
		BaseURL: "https://api.wanapis.com", Model: "gpt-5.6-sol", APIKey: "sk-x"}
	with := func(f func(*team.AgentUser)) team.AgentUser {
		u := base
		f(&u)
		return u
	}
	for _, tc := range []struct {
		name  string
		user  team.AgentUser
		want  string // substring; "" = must pass
		store bool   // whether the store gate also refuses it
	}{
		{"typo effort", with(func(u *team.AgentUser) { u.Effort = "hight" }), `effort "hight"`, false},
		{"legal effort", with(func(u *team.AgentUser) { u.Effort = "high" }), "", false},
		{"legacy max alias", with(func(u *team.AgentUser) { u.Effort = "max" }), "", false},
		{"empty effort", base, "", false},
		{"no model yet", with(func(u *team.AgentUser) { u.Model = "" }), "", false},
		{"no provider yet", with(func(u *team.AgentUser) { u.Provider = "" }), "", false},
		// An unresolvable provider is the store's call, not this check's: the store
		// preserves a legacy value until the user picks a legal option, so refusing
		// it here would make an existing entry uneditable.
		{"legacy provider", with(func(u *team.AgentUser) { u.Provider = "legacy-x" }), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := dryRunPoolEntry(tc.user)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("must pass, got %v", err)
			case tc.want != "" && err == nil:
				t.Fatalf("must be refused, got nil")
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Fatalf("refusal must name the cause %q, got %v", tc.want, err)
			}
			// The store gate is deliberately laxer: it must keep loading entries
			// written before this check existed, so it is not the typo's catcher.
			if gotStore := team.ValidateAgentUser(tc.user) != nil; gotStore != tc.store {
				t.Errorf("store gate refused = %v, want %v", gotStore, tc.store)
			}
		})
	}
}

// TestSavePoolEditRefusesUnusableEntryZeroWrite pins the editor half: the refusal
// happens before the store write, so a typo never reaches disk to be discovered
// later at bind time, and the editor stays open on the draft.
func TestSavePoolEditRefusesUnusableEntryZeroWrite(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openTeamOverlay(t)
	p := m.teamPick
	p.pool.adding = true
	p.pool.draft = team.AgentUser{UserID: "wanapi", Provider: "openai",
		BaseURL: "https://api.wanapis.com", Model: "gpt-5.6-sol", Effort: "hight", APIKey: "sk-x"}

	p.savePoolEdit()

	if !strings.Contains(p.pool.errMsg, `effort "hight"`) {
		t.Fatalf("the editor must show the adapter's reason, got %q", p.pool.errMsg)
	}
	if !p.pool.adding {
		t.Error("a refused save must stay on the draft, not close the editor")
	}
	users, err := p.store.ListAgentUsers()
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if u.UserID == "wanapi" {
			t.Fatal("a refused entry must not reach disk")
		}
	}

	// Fixing the field is all it takes: the same save now publishes.
	p.pool.draft.Effort = "high"
	p.savePoolEdit()
	if p.pool.errMsg != "" {
		t.Fatalf("a legal entry must save, got %q", p.pool.errMsg)
	}
	users, err = p.store.ListAgentUsers()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range users {
		if u.UserID == "wanapi" {
			found = true
			if u.Effort != "high" {
				t.Errorf("persisted effort = %q, want high", u.Effort)
			}
		}
	}
	if !found {
		t.Error("the corrected entry must be persisted")
	}
}
