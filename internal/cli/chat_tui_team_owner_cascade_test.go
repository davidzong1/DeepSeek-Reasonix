package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/team"
)

// ownerStoreOf returns the overlay's canonical owner store, failing the test
// when the wiring did not install one: every cascade and adoption assertion
// below is meaningless without it.
func ownerStoreOf(t *testing.T, m chatTUI) *team.OwnerStore {
	t.Helper()
	if m.teamPick == nil || m.teamPick.owners == nil {
		t.Fatal("the overlay must open with a canonical owner store wired")
	}
	return m.teamPick.owners
}

// seedOwnerUnit gives one member a canonical owner directory holding a
// transcript, the state a member accumulates after a first turn.
func seedOwnerUnit(t *testing.T, owners *team.OwnerStore, teamName, memberID string) team.OwnerPaths {
	t.Helper()
	key := team.OwnerKey{TeamID: teamName, MemberID: memberID}
	paths, _, err := owners.Init(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Transcript, []byte(`{"schema_version":1,"messages":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return paths
}

// TestTeamPickerDeleteMemberCascadesToOwnerStore pins the cascade: deleting a
// member removes its roster slot AND its canonical owner directory, which holds
// the transcript and every derived sidecar. Leaving it behind would leak a
// removed member's history and let a later member of the same id resume a
// conversation the operator deleted.
func TestTeamPickerDeleteMemberCascadesToOwnerStore(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	owners := ownerStoreOf(t, m)
	victim := seedOwnerUnit(t, owners, "alpha", "lead")
	sibling := seedOwnerUnit(t, owners, "alpha", "alice")

	m = teamKey(m, tea.KeyPressMsg{Code: 'd'})
	teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if _, err := os.Stat(victim.Dir); !os.IsNotExist(err) {
		t.Fatalf("the deleted member's owner dir survived the cascade: %v", err)
	}
	if _, err := os.Stat(sibling.Dir); err != nil {
		t.Fatalf("a sibling member's owner dir must not be touched: %v", err)
	}
	// The staged copy is swept: this is a delete, not a recoverable trash.
	bucket := filepath.Join(owners.Root(), ".trash", "owners")
	if entries, err := os.ReadDir(bucket); err == nil && len(entries) != 0 {
		t.Fatalf("the staged owner trash was left behind: %v", entries)
	}
}

// TestTeamPickerDeleteMemberRefusedKeepsOwnerDir pins the ordering half: the
// roster slot is removed first, so a refused roster delete must leave the
// member's history exactly where it was. A cascade that ran first would take a
// member's history away from a member that still exists.
func TestTeamPickerDeleteMemberRefusedKeepsOwnerDir(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	owners := ownerStoreOf(t, m)
	kept := seedOwnerUnit(t, owners, "alpha", "lead")
	if err := m.teamPick.store.SetMemberWritePolicy(team.MemberWriteLeaderOnly); err != nil {
		t.Fatal(err)
	}
	if err := m.teamPick.deleteMember(); err == nil {
		t.Fatal("a leader-only store must refuse the delete")
	}
	if _, err := os.Stat(kept.Transcript); err != nil {
		t.Fatalf("a refused delete must not touch the member's history: %v", err)
	}
}

// TestTeamPickerSelectionWriteFailureIsVisible pins the error propagation the
// selection write used to swallow: a store whose selection path cannot be
// written must report the failure to the caller, and the callers that own a
// banner must put it on the page. The member a relaunch resumes is whatever the
// write managed to store, so a dropped error is a silently wrong restart.
func TestTeamPickerSelectionWriteFailureIsVisible(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	// A regular file where the selection directory belongs: every write to the
	// selection path now fails with ENOTDIR, deterministically.
	blocked := t.TempDir()
	if err := os.WriteFile(filepath.Join(blocked, "session"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions, err := team.NewTeamSessionStoreDir(blocked)
	if err != nil {
		t.Fatal(err)
	}
	m.teamPick.sessions = sessions
	m.teamPick.session = newSessionState("alpha", "lead")

	if err := m.teamPick.persistSessionSelection(); err == nil {
		t.Fatal("an unwritable selection store must report the failure, not swallow it")
	}
	if err := m.teamPick.clearSelectedMember("alpha"); err == nil {
		t.Fatal("clearSelectedMember must report the failure, not swallow it")
	}
	if err := m.teamPick.suspendAutoSession(); err == nil {
		t.Fatal("suspendAutoSession must report the failure, not swallow it")
	}
	// The auto-session toggle exists for the persisted preference, so a failed
	// suspend must be reported on the page instead of announced as "auto-session
	// off". It is pressed from the management page, where the banner renders.
	m.teamPick.session = sessionState{}
	m.exitAllTeamSessions()
	if m.teamPick.session.active {
		t.Fatal("a failed suspend must not close the session it could not record")
	}
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "Auto-session not turned off") {
		t.Fatalf("a failed suspend must be visible on the page, got:\n%s", got)
	}
}

// TestMemberSessionRootsPutOwnerDirFirst pins the probe order the wiring
// installs: the canonical owner directory is the highest-priority candidate and
// the create target, so an adopted history is what every later launch reads and
// a first entry is written where the member's state belongs.
func TestMemberSessionRootsPutOwnerDirFirst(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	owners := ownerStoreOf(t, m)
	paths := seedOwnerUnit(t, owners, "alpha", "lead")

	ctrl := control.New(control.Options{SessionDir: t.TempDir()})
	t.Cleanup(ctrl.Close)
	roots, create := memberSessionRootsWithOwner(ctrl, t.TempDir(), paths.Dir)
	if len(roots) == 0 || roots[0] != paths.Dir {
		t.Fatalf("roots = %v, want the owner dir %q first", roots, paths.Dir)
	}
	if create != paths.Dir {
		t.Fatalf("create target = %q, want the owner dir %q", create, paths.Dir)
	}
	// An empty owner dir leaves the historical order untouched.
	plain, plainCreate := memberSessionRootsWithOwner(ctrl, "", "")
	legacy, legacyCreate := memberSessionRoots(ctrl, "")
	if len(plain) != len(legacy) || plainCreate != legacyCreate {
		t.Fatalf("an empty owner dir changed the probe order: %v/%q vs %v/%q", plain, plainCreate, legacy, legacyCreate)
	}
}

// TestLeaderStepDownClearsOwnerHistories pins the step-down promise against the
// new storage location: since a member's history now lives in its canonical
// owner directory, a step-down that only deleted the session-directory copies
// would leave the real transcripts on disk while telling the user they were
// removed. The member list comes from the owner store, so a slot already gone
// from the registry is cleared too.
func TestLeaderStepDownClearsOwnerHistories(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	owners := ownerStoreOf(t, m)
	lead := seedOwnerUnit(t, owners, "alpha", "lead")
	alice := seedOwnerUnit(t, owners, "alpha", "alice")
	// A member whose roster slot is already gone: the directory outlives the
	// slot, and a step-down promised to remove the team's histories.
	orphan := seedOwnerUnit(t, owners, "alpha", "ghost")
	// Another team's member must survive the step-down.
	other := seedOwnerUnit(t, owners, "beta", "lead")

	if err := m.teamPick.clearTeamHistories("alpha"); err != nil {
		t.Fatalf("clearTeamHistories: %v", err)
	}
	for _, paths := range []team.OwnerPaths{lead, alice, orphan} {
		if _, err := os.Stat(paths.Dir); !os.IsNotExist(err) {
			t.Errorf("owner dir %s survived the step-down: %v", paths.Dir, err)
		}
	}
	if _, err := os.Stat(other.Transcript); err != nil {
		t.Errorf("another team's history must survive the step-down: %v", err)
	}
	// Idempotent: a second clear over an already-empty team is not an error.
	if err := m.teamPick.clearTeamHistories("alpha"); err != nil {
		t.Errorf("a repeated clear must be idempotent, got %v", err)
	}
}

// TestMemberBackendAdoptsLegacyHistoryIntoOwnerStore pins the migration the
// wiring performs at the one point a member backend is assembled: a history that
// still lives in a session-directory candidate is adopted into the member's
// canonical owner directory, and the backend then binds the canonical copy. The
// source is never moved, so the member's history survives in both places until
// the operator removes the old one.
func TestMemberBackendAdoptsLegacyHistoryIntoOwnerStore(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	owners := ownerStoreOf(t, m)
	workspace := t.TempDir()
	legacyDir := config.ProjectSessionDir(workspace)
	if legacyDir == "" {
		t.Fatal("the fixture workspace must resolve a stable session root")
	}
	name, err := team.MemberSessionFile("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacyDir, name)
	seedMemberSession(t, legacyPath)
	before, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}

	ctrl := memberTurnController(t, "member-sys", newRecorderTurnRunner())
	deps := memberBackendDeps{
		ctx: t.Context(), owners: owners,
		users: fakePool{users: map[string]team.AgentUser{
			"u": {UserID: "u", Provider: "openai", Model: "gpt-5.6",
				BaseURL: "https://example.invalid/v1", APIKey: "k"},
		}},
		events:        make(chan memberEvent, memberEventBuffer),
		workspaceRoot: workspace,
		base: func() boot.Options {
			return boot.Options{SessionDir: t.TempDir(), Stderr: io.Discard}
		},
	}
	legacyRoots, _ := memberSessionRoots(ctrl, workspace)
	adopt := func() (string, error) {
		return adoptMemberOwnerHistory(deps.ctx, ctrl, deps.owners, team.MemberBinding{
			Team: "alpha", MemberID: "lead", AgentUserRef: "u", SessionFile: name,
		}, legacyRoots)
	}
	ownerDir, err := adopt()
	if err != nil {
		t.Fatalf("adoptMemberOwnerHistory: %v", err)
	}
	canonical := filepath.Join(ownerDir, name)
	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("the legacy history was not adopted into the owner dir: %v", err)
	}
	if string(got) != string(before) {
		t.Fatal("the adopted history does not match the source bytes")
	}
	after, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("adoption must not move or delete the source: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("adoption rewrote the source transcript")
	}
	// Idempotent: a second assembly adopts nothing and leaves the canonical copy
	// as the authority.
	if _, err := adopt(); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(before) {
		t.Fatal("a second adoption rewrote the canonical history")
	}
}
