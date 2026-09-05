package cli

import (
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/team"
)

// pinUserHomeDefault is the Direction B "no env" default: REASONIX_HOME and
// REASONIX_STATE_HOME both unset, so the user state root is the home-based
// ~/.reasonix and legacy project .reasonix trees are adopted (no isolation).
func pinUserHomeDefault(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("REASONIX_HOME", "")
	t.Setenv("REASONIX_STATE_HOME", "")
	return home
}

// seedLegacyProjectTeam writes a .reasonix/team registry under projectDir, the
// shape a pre-Direction-B install leaves behind.
func seedLegacyProjectTeam(t *testing.T, projectDir string, names ...string) {
	t.Helper()
	store, err := team.NewTeamStore(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := store.AddTeam(team.Team{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestOpenTeamDataRootsAnyCwdReadsOneUserGlobalStore is the cross-directory
// guarantee Direction B exists for: a registry and member history written from
// one working directory are the very files read from any other — an unrelated
// empty dir and a deep nested cwd included — never a fresh per-cwd store.
func TestOpenTeamDataRootsAnyCwdReadsOneUserGlobalStore(t *testing.T) {
	home := pinUserHomeDefault(t)
	creator := t.TempDir()
	other := t.TempDir()
	nested := filepath.Join(other, "x", "y")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.Join(home, ".reasonix", "team")

	roots, err := openTeamDataRoots(creator)
	if err != nil {
		t.Fatal(err)
	}
	if roots.dataDir != wantDir {
		t.Fatalf("creator dataDir = %q, want %q", roots.dataDir, wantDir)
	}
	if err := roots.store.AddTeam(team.Team{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := roots.sessions.AppendMessage("alpha", "m1", team.SessionMessage{Kind: "user", From: "m1", Text: "hi"}); err != nil {
		t.Fatal(err)
	}

	for _, cwd := range []string{other, nested} {
		got, err := openTeamDataRoots(cwd)
		if err != nil {
			t.Fatalf("open from %s: %v", cwd, err)
		}
		if got.dataDir != wantDir {
			t.Fatalf("dataDir from %s = %q, want %q", cwd, got.dataDir, wantDir)
		}
		if got.note != "" {
			t.Fatalf("arbitrary cwd %s must not adopt anything, note = %q", cwd, got.note)
		}
		doc, _, err := got.store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Teams) != 1 || doc.Teams[0].Name != "alpha" {
			t.Fatalf("teams from %s = %+v, want the user-global alpha", cwd, doc.Teams)
		}
		msgs, err := got.sessions.Messages("alpha", "m1")
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != 1 || msgs[0].Text != "hi" {
			t.Fatalf("history from %s = %v, want the message written from the creator cwd", cwd, msgs)
		}
	}
}

// TestOpenTeamDataRootsDeletedTeamNotResurrectedFromLegacy proves the adoption
// marker is what stops a project .reasonix tree from re-importing a team the
// user has since deleted from the user-global store — and that adoption never
// rewrites the legacy source (byte-identical before and after).
func TestOpenTeamDataRootsDeletedTeamNotResurrectedFromLegacy(t *testing.T) {
	pinUserHomeDefault(t)
	proj := t.TempDir()
	seedLegacyProjectTeam(t, proj, "alpha", "beta")
	srcFile := filepath.Join(proj, ".reasonix", "team", team.TeamFile)
	before, err := os.ReadFile(srcFile)
	if err != nil {
		t.Fatal(err)
	}

	roots, err := openTeamDataRoots(proj)
	if err != nil {
		t.Fatal(err)
	}
	if roots.note == "" {
		t.Fatal("adoption note must be set on first open of a legacy project")
	}
	doc, _, err := roots.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Teams) != 2 {
		t.Fatalf("adopted teams = %+v, want alpha+beta", doc.Teams)
	}
	if err := roots.store.DeleteTeam("alpha"); err != nil {
		t.Fatal(err)
	}

	again, err := openTeamDataRoots(proj)
	if err != nil {
		t.Fatal(err)
	}
	if again.note != "" {
		t.Fatalf("reopen note = %q, want empty (already adopted)", again.note)
	}
	doc, _, err = again.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Teams) != 1 || doc.Teams[0].Name != "beta" {
		t.Fatalf("user teams after delete = %+v, want only beta (alpha must not resurrect)", doc.Teams)
	}
	after, err := os.ReadFile(srcFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("legacy source was modified by adoption/reopen")
	}
	// The legacy tree still holds both teams for a rollback.
	legacy, err := team.NewTeamStore(proj)
	if err != nil {
		t.Fatal(err)
	}
	ldoc, _, err := legacy.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(ldoc.Teams) != 2 {
		t.Fatalf("legacy source teams = %+v, want both alpha and beta intact", ldoc.Teams)
	}
}

// TestTeamDataDisjointAcrossReasonixHomes proves two explicit REASONIX_HOME
// roots own two disjoint team stores: a team added under one home is invisible
// from the other, and each open resolves back to its own home.
func TestTeamDataDisjointAcrossReasonixHomes(t *testing.T) {
	homeA, homeB := t.TempDir(), t.TempDir()
	setHome := func(home string) {
		t.Helper()
		t.Setenv("REASONIX_HOME", home)
		t.Setenv("REASONIX_STATE_HOME", "")
	}

	setHome(homeA)
	a, err := openTeamDataRoots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if a.dataDir != filepath.Join(homeA, "team") {
		t.Fatalf("home A dataDir = %q, want %q", a.dataDir, filepath.Join(homeA, "team"))
	}
	if err := a.store.AddTeam(team.Team{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}

	setHome(homeB)
	b, err := openTeamDataRoots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if b.dataDir != filepath.Join(homeB, "team") {
		t.Fatalf("home B dataDir = %q, want %q", b.dataDir, filepath.Join(homeB, "team"))
	}
	if err := b.store.AddTeam(team.Team{Name: "beta"}); err != nil {
		t.Fatal(err)
	}

	setHome(homeA)
	a2, err := openTeamDataRoots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	doc, _, err := a2.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Teams) != 1 || doc.Teams[0].Name != "alpha" {
		t.Fatalf("home A sees %+v after home B wrote, want only alpha", doc.Teams)
	}
	if _, err := os.Stat(filepath.Join(homeB, "team", team.TeamFile)); err != nil {
		t.Fatalf("home B store vanished: %v", err)
	}
}

// TestOpenTeamDataRootsStateHomeWinsOverHomeAndStillIsolated pins the root
// precedence REASONIX_STATE_HOME > REASONIX_HOME > ~/.reasonix for the team
// data dir, while an explicit REASONIX_HOME still marks the runtime isolated —
// a project's legacy .reasonix tree is never adopted even though the team data
// root itself follows STATE_HOME.
func TestOpenTeamDataRootsStateHomeWinsOverHomeAndStillIsolated(t *testing.T) {
	stateHome, isoHome, plainHome := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", plainHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(plainHome, ".config"))
	t.Setenv("REASONIX_HOME", isoHome)
	t.Setenv("REASONIX_STATE_HOME", stateHome)

	proj := t.TempDir()
	seedLegacyProjectTeam(t, proj, "legacy")

	roots, err := openTeamDataRoots(proj)
	if err != nil {
		t.Fatal(err)
	}
	if roots.dataDir != filepath.Join(stateHome, "team") {
		t.Fatalf("dataDir = %q, want STATE_HOME %q", roots.dataDir, filepath.Join(stateHome, "team"))
	}
	doc, _, err := roots.store.Load()
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(doc.Teams) != 0 {
		t.Fatalf("isolated runtime must not adopt the project legacy tree, got %+v", doc.Teams)
	}
	for _, stray := range []string{
		filepath.Join(isoHome, "team"),
		filepath.Join(plainHome, ".reasonix", "team"),
	} {
		if _, err := os.Stat(stray); !os.IsNotExist(err) {
			t.Fatalf("team store leaked to non-state root %q: %v", stray, err)
		}
	}
}
