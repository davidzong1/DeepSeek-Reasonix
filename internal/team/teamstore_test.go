package team

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// newTeamStore returns a TeamStore over a fresh temp project root.
func newTeamStore(t *testing.T) (*TeamStore, string) {
	t.Helper()
	root := t.TempDir()
	ts, err := NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	return ts, root
}

// writeTeamFile seeds a file under the temp project's .reasonix/team dir.
func writeTeamFile(t *testing.T, root, rel, data string) {
	t.Helper()
	full := filepath.Join(root, ".reasonix", "team", rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// validDoc is the canonical v1 registry used across mutation tests.
func validDoc() TeamDoc {
	return TeamDoc{
		Document: Document{SchemaVersion: SchemaVersion},
		Teams: []Team{{
			Name: "alpha",
			Template: []MemberSlot{{
				MemberID: "m1",
				Role:     RoleCoder,
				Status:   MemberStatusActive,
			}},
			DefaultAgentUserRef: "au-1",
		}},
	}
}

func teamFile(t *testing.T, root string) string {
	return filepath.Join(root, ".reasonix", "team", TeamFile)
}

func TestTeamStoreRoundTripPrimary(t *testing.T) {
	ts, root := newTeamStore(t)
	want := validDoc()
	if err := ts.Save(want); err != nil {
		t.Fatal(err)
	}
	got, legacy, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if legacy {
		t.Fatal("primary load reported legacy source")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
	if _, err := os.Stat(teamFile(t, root)); err != nil {
		t.Fatalf("primary file not published: %v", err)
	}
}

func TestTeamStoreLoadFallsBackToLegacy(t *testing.T) {
	ts, root := newTeamStore(t)
	writeTeamFile(t, root, TeamsLegacyFile,
		`{"schema_version":1,"teams":[{"Name":"old","Template":[{"MemberID":"m1","Role":"coder","Status":"active"}],"DefaultAgentUserRef":"au-1"}]}`)
	got, legacy, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !legacy {
		t.Fatal("expected legacy fallback source")
	}
	if len(got.Teams) != 1 || got.Teams[0].Name != "old" {
		t.Fatalf("fallback content wrong: %+v", got)
	}
	if _, err := os.Stat(teamFile(t, root)); !os.IsNotExist(err) {
		t.Fatal("fallback read must not write the primary file")
	}
}

func TestTeamStoreLoadMissingBoth(t *testing.T) {
	ts, _ := newTeamStore(t)
	_, _, err := ts.Load()
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestTeamStoreLoadPrimaryCorruptNotMasked(t *testing.T) {
	ts, root := newTeamStore(t)
	writeTeamFile(t, root, TeamFile, `not json`)
	writeTeamFile(t, root, TeamsLegacyFile, `{"schema_version":1,"teams":[]}`)
	_, _, err := ts.Load()
	var ce *CorruptFileError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *CorruptFileError (corrupt primary must surface)", err)
	}
}

func TestTeamStoreLoadLegacyCorrupt(t *testing.T) {
	ts, root := newTeamStore(t)
	writeTeamFile(t, root, TeamsLegacyFile, `{"schema_version":1,"teams":[`)
	_, _, err := ts.Load()
	var ce *CorruptFileError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *CorruptFileError (corrupt legacy must surface)", err)
	}
}

func TestTeamStoreAddTeam(t *testing.T) {
	ts, _ := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	if err := ts.AddTeam(Team{Name: "beta"}); err != nil {
		t.Fatal(err)
	}
	if err := ts.AddTeam(Team{Name: "alpha"}); !errors.Is(err, ErrTeamExists) {
		t.Fatalf("duplicate name: err = %v, want ErrTeamExists", err)
	}
	if err := ts.AddTeam(Team{Name: "  "}); !errors.Is(err, ErrInvalidTeam) {
		t.Fatalf("blank name: err = %v, want ErrInvalidTeam", err)
	}
	doc, _, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Teams) != 2 || doc.Teams[1].Name != "beta" {
		t.Fatalf("teams after AddTeam: %+v", doc.Teams)
	}
}

func TestTeamStoreDeleteTeam(t *testing.T) {
	ts, _ := newTeamStore(t)
	doc := validDoc()
	doc.Teams = append(doc.Teams, Team{Name: "beta"})
	if err := ts.Save(doc); err != nil {
		t.Fatal(err)
	}
	if err := ts.DeleteTeam("beta"); err != nil {
		t.Fatal(err)
	}
	if err := ts.DeleteTeam("gamma"); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("missing team: err = %v, want ErrTeamNotFound", err)
	}
	if err := ts.DeleteTeam("alpha"); !errors.Is(err, ErrLastTeam) {
		t.Fatalf("last team: err = %v, want ErrLastTeam", err)
	}
	got, _, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Teams) != 1 || got.Teams[0].Name != "alpha" {
		t.Fatalf("registry changed after refused delete: %+v", got.Teams)
	}
}

func TestTeamStoreAddMember(t *testing.T) {
	ts, _ := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	if err := ts.AddMember("alpha", MemberSlot{MemberID: "m2", Role: RoleTester, Status: MemberStatusActive}); err != nil {
		t.Fatal(err)
	}
	if err := ts.AddMember("alpha", MemberSlot{MemberID: "m1", Role: RoleCoder, Status: MemberStatusActive}); !errors.Is(err, ErrMemberExists) {
		t.Fatalf("duplicate member: err = %v, want ErrMemberExists", err)
	}
	if err := ts.AddMember("alpha", MemberSlot{MemberID: " ", Role: RoleCoder, Status: MemberStatusActive}); !errors.Is(err, ErrInvalidMember) {
		t.Fatalf("blank member id: err = %v, want ErrInvalidMember", err)
	}
	if err := ts.AddMember("nope", MemberSlot{MemberID: "m9", Role: RoleCoder, Status: MemberStatusActive}); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("missing team: err = %v, want ErrTeamNotFound", err)
	}
	doc, _, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(doc.Teams[0].Template); got != 2 {
		t.Fatalf("template size = %d, want 2", got)
	}
}

func TestTeamStoreDeleteMember(t *testing.T) {
	ts, _ := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	if err := ts.DeleteMember("alpha", "m1"); err != nil {
		t.Fatal(err)
	}
	if err := ts.DeleteMember("alpha", "m1"); !errors.Is(err, ErrMemberNotFound) {
		t.Fatalf("missing member: err = %v, want ErrMemberNotFound", err)
	}
	if err := ts.DeleteMember("nope", "m1"); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("missing team: err = %v, want ErrTeamNotFound", err)
	}
	doc, _, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Teams[0].Template) != 0 {
		t.Fatalf("template not emptied: %+v", doc.Teams[0].Template)
	}
}

func TestTeamStoreSetMemberStatus(t *testing.T) {
	ts, _ := newTeamStore(t)
	if err := ts.Save(validDoc()); err != nil {
		t.Fatal(err)
	}
	if err := ts.SetMemberStatus("alpha", "m1", MemberStatusDisabled); err != nil {
		t.Fatal(err)
	}
	if err := ts.SetMemberStatus("alpha", "m1", MemberStatus("retired")); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("unknown status: err = %v, want ErrInvalidStatus", err)
	}
	if err := ts.SetMemberStatus("alpha", "ghost", MemberStatusArchived); !errors.Is(err, ErrMemberNotFound) {
		t.Fatalf("missing member: err = %v, want ErrMemberNotFound", err)
	}
	doc, _, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Teams[0].Template[0].Status; got != MemberStatusDisabled {
		t.Fatalf("status = %q, want disabled", got)
	}
}

func TestTeamStoreCompareAndSwapConflict(t *testing.T) {
	ts, _ := newTeamStore(t)
	want := validDoc()
	if err := ts.Save(want); err != nil {
		t.Fatal(err)
	}
	stale := validDoc()
	stale.Teams[0].Name = "stale"
	if err := ts.CompareAndSwap(stale, want); !errors.Is(err, ErrCASConflict) {
		t.Fatalf("err = %v, want ErrCASConflict", err)
	}
}

func TestTeamStoreMutateMigratesLegacyOnFirstWrite(t *testing.T) {
	ts, root := newTeamStore(t)
	writeTeamFile(t, root, TeamsLegacyFile,
		`{"schema_version":1,"teams":[{"Name":"old","Template":[{"MemberID":"m1","Role":"coder","Status":"active"}],"DefaultAgentUserRef":"au-1"}]}`)
	if err := ts.AddTeam(Team{Name: "new"}); err != nil {
		t.Fatal(err)
	}
	doc, legacy, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if legacy {
		t.Fatal("post-write load still reports legacy source")
	}
	if len(doc.Teams) != 2 || doc.Teams[1].Name != "new" {
		t.Fatalf("teams after migrate-on-write: %+v", doc.Teams)
	}
	if _, err := os.Stat(teamFile(t, root)); err != nil {
		t.Fatalf("primary not created by migrate-on-write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".reasonix", "team", TeamsLegacyFile)); err != nil {
		t.Fatal("legacy file must be left in place")
	}
}

func TestMigrateLegacyOnce(t *testing.T) {
	ts, root := newTeamStore(t)
	writeTeamFile(t, root, TeamsLegacyFile,
		`{"schema_version":1,"teams":[{"Name":"old","Template":[{"MemberID":"m1","Role":"coder","Status":"active"}],"DefaultAgentUserRef":"au-1"}]}`)
	if err := ts.MigrateLegacy(); err != nil {
		t.Fatal(err)
	}
	doc, legacy, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if legacy || len(doc.Teams) != 1 || doc.Teams[0].Name != "old" {
		t.Fatalf("post-migration load wrong: legacy=%v doc=%+v", legacy, doc)
	}
	if _, err := os.Stat(filepath.Join(root, ".reasonix", "team", TeamsLegacyFile)); err != nil {
		t.Fatal("legacy file must survive migration")
	}
}

func TestMigrateLegacyRefusesExistingPrimary(t *testing.T) {
	ts, root := newTeamStore(t)
	want := validDoc()
	if err := ts.Save(want); err != nil {
		t.Fatal(err)
	}
	writeTeamFile(t, root, TeamsLegacyFile,
		`{"schema_version":1,"teams":[{"Name":"old","Template":[],"DefaultAgentUserRef":""}]}`)
	err := ts.MigrateLegacy()
	if !errors.Is(err, ErrMigrateRefused) {
		t.Fatalf("err = %v, want ErrMigrateRefused", err)
	}
	if !errors.Is(err, ErrCASConflict) {
		t.Fatalf("err = %v, want ErrCASConflict too", err)
	}
	got, _, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("primary overwritten by refused migration")
	}
}

func TestMigrateLegacyNoSources(t *testing.T) {
	ts, _ := newTeamStore(t)
	if err := ts.MigrateLegacy(); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

// TestTeamStoreAddTeamBootstrapsMissingRegistry pins the fresh-project path: a
// project with no registry at all can create its first team, so the [ TEAM ]
// overlay never needs a hand-written team.json to get started.
func TestTeamStoreAddTeamBootstrapsMissingRegistry(t *testing.T) {
	ts, root := newTeamStore(t)
	if err := ts.AddTeam(Team{Name: "first"}); err != nil {
		t.Fatalf("AddTeam on a fresh project: %v", err)
	}
	doc, legacy, err := ts.Load()
	if err != nil {
		t.Fatal(err)
	}
	if legacy {
		t.Fatal("bootstrap must publish the primary file, not the legacy one")
	}
	if len(doc.Teams) != 1 || doc.Teams[0].Name != "first" {
		t.Fatalf("registry after bootstrap: %+v", doc.Teams)
	}
	info, err := os.Stat(teamFile(t, root))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("bootstrapped team.json perm = %o, want 600", perm)
	}
}

// TestTeamStoreBootstrapOnlyCreatesOnAdd pins that an absent registry is an
// empty one, not a blank slate that invents targets: every mutation naming a
// team that does not exist still fails with ErrTeamNotFound.
func TestTeamStoreBootstrapOnlyCreatesOnAdd(t *testing.T) {
	ts, root := newTeamStore(t)
	if err := ts.DeleteTeam("ghost"); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("DeleteTeam on a fresh project: err = %v, want ErrTeamNotFound", err)
	}
	if err := ts.AddMember("ghost", MemberSlot{MemberID: "m1"}); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("AddMember on a fresh project: err = %v, want ErrTeamNotFound", err)
	}
	if err := ts.SetMemberStatus("ghost", "m1", MemberStatusActive); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("SetMemberStatus on a fresh project: err = %v, want ErrTeamNotFound", err)
	}
	if _, err := os.Stat(teamFile(t, root)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a refused mutation must not create team.json")
	}
}

// TestTeamStoreCorruptPrimaryBlocksBootstrap keeps an unreadable registry from
// being mistaken for an absent one and silently replaced.
func TestTeamStoreCorruptPrimaryBlocksBootstrap(t *testing.T) {
	ts, root := newTeamStore(t)
	writeTeamFile(t, root, TeamFile, `{"schema_version":1,"teams":[`)
	err := ts.AddTeam(Team{Name: "first"})
	var ce *CorruptFileError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *CorruptFileError (corrupt must not bootstrap)", err)
	}
	data, readErr := os.ReadFile(teamFile(t, root))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != `{"schema_version":1,"teams":[` {
		t.Fatalf("corrupt primary was overwritten: %s", data)
	}
}

// TestTeamStoreDeleteAgentUserRefusedWhileReferenced pins the binding
// protection (§2.1): an entry any team references — as a member override or
// the team default — cannot be deleted until every reference is gone, so a
// removal can never orphan a binding.
func TestTeamStoreDeleteAgentUserRefusedWhileReferenced(t *testing.T) {
	ts, _ := newTeamStore(t)
	doc := TeamDoc{
		Document: Document{SchemaVersion: SchemaVersion},
		Teams: []Team{{
			Name: "alpha",
			Template: []MemberSlot{
				{MemberID: "m1", Role: RoleCoder, Status: MemberStatusActive, AgentUserRef: "au-1"},
				{MemberID: "m2", Role: RoleTester, Status: MemberStatusActive},
			},
			DefaultAgentUserRef: "au-2",
		}},
	}
	if err := ts.Save(doc); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"au-1", "au-2", "au-3"} {
		if err := ts.AddAgentUser(AgentUser{UserID: id}); err != nil {
			t.Fatal(err)
		}
	}
	// au-1 is a member override, au-2 the team default; au-3 is unreferenced.
	for _, id := range []string{"au-1", "au-2"} {
		if err := ts.DeleteAgentUser(id); !errors.Is(err, ErrAgentUserInUse) {
			t.Fatalf("delete %s: err = %v, want ErrAgentUserInUse", id, err)
		}
	}
	if err := ts.DeleteAgentUser("au-3"); err != nil {
		t.Fatalf("unreferenced delete: %v", err)
	}
	// Unbinding the override frees au-1; the team default still blocks au-2.
	if err := ts.UnbindAgentUser("alpha", "m1"); err != nil {
		t.Fatal(err)
	}
	if err := ts.DeleteAgentUser("au-1"); err != nil {
		t.Fatalf("delete after unbind: %v", err)
	}
	if err := ts.DeleteAgentUser("au-2"); !errors.Is(err, ErrAgentUserInUse) {
		t.Fatalf("team-default reference must still block: err = %v", err)
	}
	pool, err := ts.ListAgentUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(pool) != 1 || pool[0].UserID != "au-2" {
		t.Fatalf("pool after protected deletes: %+v", pool)
	}
}

// TestTeamStoreDeleteAgentUserAbsentRegistry pins the fresh-project path: with
// no team registry at all, a pool-only store delete is never refused by a
// phantom reference (the standalone AgentUsersStore has no check at all).
func TestTeamStoreDeleteAgentUserAbsentRegistry(t *testing.T) {
	ts, _ := newTeamStore(t)
	if err := ts.AddAgentUser(AgentUser{UserID: "solo"}); err != nil {
		t.Fatal(err)
	}
	if err := ts.DeleteAgentUser("solo"); !errors.Is(err, ErrLastAgentUser) {
		t.Fatalf("last-entry delete: err = %v, want ErrLastAgentUser", err)
	}
	if err := ts.AddAgentUser(AgentUser{UserID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := ts.AddAgentUser(AgentUser{UserID: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := ts.DeleteAgentUser("a"); err != nil {
		t.Fatalf("delete without a team registry must pass: %v", err)
	}
}
