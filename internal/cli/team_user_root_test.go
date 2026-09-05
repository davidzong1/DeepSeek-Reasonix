package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/team"
)

// TestOpenTeamDataRootsUserGlobalAdoptsLegacy proves the Direction B default:
// with REASONIX_STATE_HOME set, the registry/board dir resolves under the user
// state root from any cwd, the project's legacy .reasonix/team is adopted into
// it once, and the source tree is left in place.
func TestOpenTeamDataRootsUserGlobalAdoptsLegacy(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("REASONIX_STATE_HOME", stateHome)
	t.Setenv("REASONIX_HOME", "")

	proj := t.TempDir()
	store, err := team.NewTeamStore(proj)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddTeam(team.Team{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}

	roots, err := openTeamDataRoots(proj)
	if err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.Join(stateHome, "team")
	if roots.dataDir != wantDir {
		t.Fatalf("dataDir = %q, want user-global %q", roots.dataDir, wantDir)
	}
	if roots.note == "" {
		t.Fatal("adoption note must be set when legacy teams are folded in")
	}
	if _, err := os.Stat(filepath.Join(proj, ".reasonix", "team", team.TeamFile)); err != nil {
		t.Fatalf("legacy source must survive adoption: %v", err)
	}
	doc, _, err := roots.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Teams) != 1 || doc.Teams[0].Name != "alpha" {
		t.Fatalf("user store teams = %+v, want adopted alpha", doc.Teams)
	}
	// Reopen: the marker makes the second adoption a no-op.
	again, err := openTeamDataRoots(proj)
	if err != nil {
		t.Fatal(err)
	}
	if again.note != "" {
		t.Fatalf("reopen note = %q, want empty (already adopted)", again.note)
	}
}

// TestOpenTeamDataRootsNoEnvDefaultsToUserSupportDir proves the Direction B
// default: with no REASONIX_HOME / REASONIX_STATE_HOME set, the registry and
// board dir resolve under the home-based user state root (~/.reasonix/team)
// from any cwd, and the project's legacy .reasonix/team is adopted into it
// exactly once, the source tree left in place.
func TestOpenTeamDataRootsNoEnvDefaultsToUserSupportDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("REASONIX_STATE_HOME", "")
	t.Setenv("REASONIX_HOME", "")

	proj := t.TempDir()
	store, err := team.NewTeamStore(proj)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddTeam(team.Team{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}

	roots, err := openTeamDataRoots(proj)
	if err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.Join(home, ".reasonix", "team")
	if roots.dataDir != wantDir {
		t.Fatalf("dataDir = %q, want user-global %q", roots.dataDir, wantDir)
	}
	if roots.note == "" {
		t.Fatal("adoption note must be set when legacy teams are folded in")
	}
	if _, err := os.Stat(filepath.Join(proj, ".reasonix", "team", team.TeamFile)); err != nil {
		t.Fatalf("legacy source must survive adoption: %v", err)
	}
	doc, _, err := roots.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Teams) != 1 || doc.Teams[0].Name != "alpha" {
		t.Fatalf("user store teams = %+v, want adopted alpha", doc.Teams)
	}
	// Reopen: the marker makes the second adoption a no-op.
	again, err := openTeamDataRoots(proj)
	if err != nil {
		t.Fatal(err)
	}
	if again.note != "" {
		t.Fatalf("reopen note = %q, want empty (already adopted)", again.note)
	}
}

// TestOpenTeamDataRootsIsolatedHomeDoesNotAdopt proves an explicit REASONIX_HOME
// routes teams to that home and refuses to import the project's legacy tree.
func TestOpenTeamDataRootsIsolatedHomeDoesNotAdopt(t *testing.T) {
	isoHome := t.TempDir()
	t.Setenv("REASONIX_HOME", isoHome)
	t.Setenv("REASONIX_STATE_HOME", "")

	proj := t.TempDir()
	store, err := team.NewTeamStore(proj)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddTeam(team.Team{Name: "gamma"}); err != nil {
		t.Fatal(err)
	}

	roots, err := openTeamDataRoots(proj)
	if err != nil {
		t.Fatal(err)
	}
	if roots.dataDir != filepath.Join(isoHome, "team") {
		t.Fatalf("dataDir = %q, want isolated home %q", roots.dataDir, filepath.Join(isoHome, "team"))
	}
	doc, _, err := roots.store.Load()
	if err != nil {
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		doc = team.TeamDoc{}
	}
	if len(doc.Teams) != 0 {
		t.Fatalf("isolated home must not adopt project teams, got %+v", doc.Teams)
	}
}

// TestOpenUserTeamDataRootAndKBAnchorCwdIndependent proves the user store is
// not anchored at the launching cwd: opening the default surface from two
// unrelated project cwds reports the same team data dir and Root(), both under
// the user state root, and the knowledge base data root colocates under that
// dir — <user state root>/team/knowledge_base — never under either cwd.
func TestOpenUserTeamDataRootAndKBAnchorCwdIndependent(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("REASONIX_STATE_HOME", stateHome)
	t.Setenv("REASONIX_HOME", "")

	projA := t.TempDir()
	projB := t.TempDir()
	rootsA, err := openTeamDataRoots(projA)
	if err != nil {
		t.Fatal(err)
	}
	rootsB, err := openTeamDataRoots(projB)
	if err != nil {
		t.Fatal(err)
	}

	wantDir := filepath.Join(stateHome, "team")
	wantKB := filepath.Join(wantDir, "knowledge_base")
	for label, roots := range map[string]*teamDataRoots{"A": rootsA, "B": rootsB} {
		if roots.dataDir != wantDir {
			t.Fatalf("%s dataDir = %q, want user-global %q", label, roots.dataDir, wantDir)
		}
		if got := roots.store.Root(); got != wantDir {
			t.Fatalf("%s store.Root() = %q, want the team data dir %q (cwd must not be the anchor)", label, got, wantDir)
		}
		if got := teamKBDataRoot(roots.dataDir); got != wantKB {
			t.Fatalf("%s KB data root = %q, want %q", label, got, wantKB)
		}
	}
	if rootsA.dataDir != rootsB.dataDir || rootsA.store.Root() != rootsB.store.Root() {
		t.Fatal("user surface must resolve identically from every cwd")
	}
	for _, leaked := range []string{projA, projB} {
		if strings.Contains(rootsA.dataDir, leaked) || strings.Contains(rootsA.store.Root(), leaked) ||
			strings.Contains(teamKBDataRoot(rootsA.dataDir), leaked) {
			t.Fatalf("user surface must not reference the launching cwd %q", leaked)
		}
	}
}
