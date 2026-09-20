package cli

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/session"
	"reasonix/internal/team"
)

// ownerHistoryService opens a real v3 session service for one member backend,
// closed with the test: a leaked service keeps its writer lock, and the next
// assembly of the same member then fails on a lease nobody holds.
func ownerHistoryService(t *testing.T) *session.Service {
	t.Helper()
	service, err := session.NewService("local", session.NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions-v4")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	return service
}

// ownerHistoryOf reads one member's published history identity back from the
// overlay's own canonical owner store — the exact surface a second window polls.
// A member with no owner directory reads as absent rather than as an error, so
// "nothing was published" is a plain assertion.
func ownerHistoryOf(t *testing.T, m chatTUI, teamName, memberID string) team.OwnerFingerprint {
	t.Helper()
	got, err := ownerStoreOf(t, m).Fingerprint(team.OwnerKey{TeamID: teamName, MemberID: memberID})
	if err != nil {
		t.Fatalf("owner fingerprint for %s/%s: %v", teamName, memberID, err)
	}
	return got
}

// ownerHistoryBoundTUI binds the leader of a two-member team to a real
// controller, so a bound /clear rotates a real session and the publication has
// an identity to advance. It returns the TUI, the member's controller, and the
// member's turn runner, which blocks in Run until the turn is cancelled — how a
// test drives ClearSession into its classified busy refusal.
//
// The stub builder performs the same assembly-time publication the production
// member builder does, at the same point, so the identity this test then watches
// a /clear advance is the one production would have established.
func ownerHistoryBoundTUI(t *testing.T, exclusive bool) (chatTUI, *control.Controller, *blockingTurnRunner) {
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
	owners := ownerStoreOf(t, m)

	runner := &blockingTurnRunner{started: make(chan struct{})}
	var member *control.Controller
	m.memberEvents = make(chan memberEvent, 8)
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		dir := t.TempDir()
		exec := agent.New(nil, nil, agent.NewSession("member-sys"), agent.Options{}, event.Discard)
		opts := control.Options{
			Executor: exec, SessionDir: dir, Label: b.MemberID,
			SystemPrompt: "member-sys", DisableColdResumePrune: true, Sink: event.Discard,
			Runner: runner,
		}
		if exclusive {
			service := ownerHistoryService(t)
			runtime, err := service.Create(context.Background(), session.CreateOptions{SessionID: b.MemberID})
			if err != nil {
				return nil, err
			}
			opts.SessionService, opts.SessionRuntime, opts.ExclusiveSession = service, runtime, true
		}
		c := control.New(opts)
		t.Cleanup(c.Close)
		if !exclusive {
			// The legacy axis executes its path, so a member has a history identity
			// only once a transcript exists there: SetSessionPath alone leaves
			// HistoryStamp() empty. Seed one and take the production probe.
			seedMemberSession(t, filepath.Join(dir, b.SessionFile))
			if _, _, err := bindMemberSession(c, b.SessionFile, []string{dir}, dir); err != nil {
				return nil, err
			}
		}
		if err := recordMemberOwnerHistory(context.Background(), owners, b, c, false); err != nil {
			return nil, err
		}
		member = c
		return c, nil
	}, 4)
	if cmd := m.switchTeamMember("lead"); cmd == nil {
		t.Fatalf("binding member %q must succeed", "lead")
	}
	// The bind establishes the identity without bumping it: assembling a member is
	// not a history change, so a peer window must not reload for it.
	if got := ownerHistoryOf(t, m, "alpha", "lead"); !got.Present || got.Generation != 0 || got.Stem == "" {
		t.Fatalf("the assembly-time publication must establish an unbumped identity, got %+v", got)
	}
	return m, member, runner
}

// TestBoundClearPublishesOwnerHistoryIdentity pins the /clear half of the
// cross-window contract: a clear replaces the bound member's live session, so
// the identity a second window polls must advance — otherwise the peer keeps
// rendering the transcript this clear destroyed.
//
// The two axes differ in how much of the new identity exists when ClearSession
// returns, and the assertions follow that difference rather than papering over
// it. v3 binds a fresh SessionRef before returning, so the member can already
// name its new session. Legacy rotates to a path whose file is written on the
// next save (probed: the path does not exist yet), so there is no name to
// publish — the generation is the whole signal, and the previous name must be
// retained rather than blanked (see OwnerStore.BumpHistory).
func TestBoundClearPublishesOwnerHistoryIdentity(t *testing.T) {
	for _, exclusive := range []bool{false, true} {
		name := "legacy"
		if exclusive {
			name = "exclusive"
		}
		t.Run(name, func(t *testing.T) {
			m, member, _ := ownerHistoryBoundTUI(t, exclusive)
			before := ownerHistoryOf(t, m, "alpha", "lead")
			m.runSlashCommand("/clear")
			next, _ := m.handleClearConfirmKey(tea.KeyPressMsg{Code: 'y'})
			m = next.(chatTUI)

			got := ownerHistoryOf(t, m, "alpha", "lead")
			if got.Generation != before.Generation+1 {
				t.Fatalf("a successful bound /clear must advance the generation once: %+v -> %+v", before, got)
			}
			// A publication that lost the name entirely would leave the owner
			// unnamed for every peer until the next save.
			if got.Stem == "" {
				t.Fatalf("a bound /clear published an unnamed owner: %+v", got)
			}
			stamp := member.HistoryStamp()
			if exclusive {
				if stamp == "" || got.Stem != stamp {
					t.Fatalf("the published stem = %q, want the member's current stamp %q", got.Stem, stamp)
				}
				// A stem naming the destroyed session would be worse than none: the
				// peer would reload and land on a transcript that no longer exists.
				if got.Stem == before.Stem {
					t.Fatalf("the published stem still names the cleared session %q", got.Stem)
				}
				return
			}
			// Legacy: the fresh transcript is not on disk yet, so the member has
			// no name to offer and the clear is carried by the generation alone.
			if stamp != "" {
				t.Fatalf("precondition: a legacy clear leaves no live transcript, so the stamp must be empty, got %q", stamp)
			}
			if got.Stem != before.Stem {
				t.Fatalf("a legacy clear blanked the owner's name: %q -> %q", before.Stem, got.Stem)
			}
		})
	}
}

// TestBoundClearFailureLeavesOwnerHistoryAlone pins the failure half: a refused
// clear leaves the member's session exactly as it was, so publishing a new
// identity would tell every peer window to reload a change that never happened.
func TestBoundClearFailureLeavesOwnerHistoryAlone(t *testing.T) {
	m, member, runner := ownerHistoryBoundTUI(t, false)
	before := ownerHistoryOf(t, m, "alpha", "lead")
	member.Send("hold the turn open")
	<-runner.started
	t.Cleanup(func() {
		member.Cancel()
		deadline := time.Now().Add(2 * time.Second)
		for member.Running() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	})

	m.runSlashCommand("/clear")
	next, _ := m.handleClearConfirmKey(tea.KeyPressMsg{Code: 'y'})
	m = next.(chatTUI)

	if !member.Running() {
		t.Fatal("precondition: the member's turn must still be running")
	}
	if got := ownerHistoryOf(t, m, "alpha", "lead"); got != before {
		t.Fatalf("a failed bound /clear published %+v, want %+v unchanged", got, before)
	}
}

// TestAmbientClearDoesNotCreateAnOwner pins the scope half: with the window off
// the member session, /clear is the chat's own session, which has no canonical
// owner. A publication here would manufacture an owner directory for a member
// the clear never touched — and name the chat's session as that member's history.
func TestAmbientClearDoesNotCreateAnOwner(t *testing.T) {
	writeTeamFixture(t, twoMemberTeam())
	m := openRoster(t)
	if m.teamPick.session.active {
		t.Fatal("the roster fixture must leave the member session closed")
	}
	m.runSlashCommand("/clear")
	next, _ := m.handleClearConfirmKey(tea.KeyPressMsg{Code: 'y'})
	m = next.(chatTUI)

	for _, memberID := range []string{"lead", "alice"} {
		if got := ownerHistoryOf(t, m, "alpha", memberID); got.Present {
			t.Fatalf("an ambient /clear published member %s's history: %+v", memberID, got)
		}
	}
}
