package team

import (
	"os"
	"path/filepath"
	"testing"
)

func writeDocFile(t *testing.T, dir string, doc any) {
	t.Helper()
	fs, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Save(TeamFile, doc); err != nil {
		t.Fatal(err)
	}
}

func seedProjectTeamDir(t *testing.T, dir string, teamNames []string) {
	t.Helper()
	doc := TeamDoc{Document: Document{SchemaVersion: SchemaVersion}}
	for _, n := range teamNames {
		doc.Teams = append(doc.Teams, Team{Name: n})
	}
	writeDocFile(t, dir, &doc)
}

func seedProjectPool(t *testing.T, dir string, ids []string) {
	t.Helper()
	doc := AgentUsersDoc{Document: Document{SchemaVersion: SchemaVersion}}
	for _, id := range ids {
		doc.AgentUsers = append(doc.AgentUsers, AgentUser{UserID: id})
	}
	if err := NewFileStoreOrDie(t, dir).Save(AgentUsersFile, &doc); err != nil {
		t.Fatal(err)
	}
}

func NewFileStoreOrDie(t *testing.T, dir string) *FileStore {
	t.Helper()
	fs, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func seedHistory(t *testing.T, dir, teamName, memberID string) {
	t.Helper()
	ss, err := NewTeamSessionStoreDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ss.AppendMessage(teamName, memberID, SessionMessage{Kind: "user", From: memberID, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
}

func teamNames(t *testing.T, dir string) []string {
	t.Helper()
	s, err := NewTeamStoreAt("", dir)
	if err != nil {
		t.Fatal(err)
	}
	doc, _, err := s.Load()
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	out := make([]string, 0, len(doc.Teams))
	for _, t := range doc.Teams {
		out = append(out, t.Name)
	}
	return out
}

func hasStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// TestAdoptProjectIntoCarriesRegistryPoolAndHistoryOnce proves the legacy
// project .reasonix/team is folded into a user data dir exactly once: teams,
// pool, and one member history land in the target, the source tree is left
// untouched, and a second run no-ops via the adoption marker.
func TestAdoptProjectIntoCarriesRegistryPoolAndHistoryOnce(t *testing.T) {
	projTeam := filepath.Join(t.TempDir(), ".reasonix", "team")
	userTeam := filepath.Join(t.TempDir(), "team")
	if err := os.MkdirAll(projTeam, 0o755); err != nil {
		t.Fatal(err)
	}
	seedProjectTeamDir(t, projTeam, []string{"alpha", "beta"})
	seedProjectPool(t, projTeam, []string{"u1"})
	seedHistory(t, projTeam, "alpha", "m1")

	rep, err := AdoptProjectInto(userTeam, projTeam, AdoptOptions{AllowLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.TeamsCreated != 2 || rep.AgentUsersCreated != 1 || rep.HistoriesAdopted != 1 {
		t.Fatalf("adopt report = %+v, want 2 teams / 1 pool / 1 history", rep)
	}
	// Source is never deleted or modified.
	if _, err := os.Stat(filepath.Join(projTeam, TeamFile)); err != nil {
		t.Fatalf("legacy source was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projTeam, "context", "alpha", "m1", MemberMessagesFile)); err != nil {
		t.Fatalf("legacy history was removed: %v", err)
	}
	for _, n := range []string{"alpha", "beta"} {
		if !hasStr(teamNames(t, userTeam), n) {
			t.Fatalf("team %q not adopted", n)
		}
	}
	ss, err := NewTeamSessionStoreDir(userTeam)
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := ss.Messages("alpha", "m1")
	if err != nil || len(msgs) != 1 || msgs[0].Text != "hello" {
		t.Fatalf("member history not adopted: msgs=%v err=%v", msgs, err)
	}

	again, err := AdoptProjectInto(userTeam, projTeam, AdoptOptions{AllowLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	if again.Skipped != "already adopted" {
		t.Fatalf("second adopt skipped=%q, want already adopted", again.Skipped)
	}
	if len(teamNames(t, userTeam)) != 2 {
		t.Fatalf("re-adopt resurrected or duplicated teams")
	}
}

// TestAdoptProjectIntoIsolatedHomeRefusesLegacy proves an explicit REASONIX_HOME
// (AllowLegacy=false) never reads the project's legacy tree, so an isolated
// instance cannot leak another install's team data.
func TestAdoptProjectIntoIsolatedHomeRefusesLegacy(t *testing.T) {
	projTeam := filepath.Join(t.TempDir(), ".reasonix", "team")
	userTeam := filepath.Join(t.TempDir(), "team")
	if err := os.MkdirAll(projTeam, 0o755); err != nil {
		t.Fatal(err)
	}
	seedProjectTeamDir(t, projTeam, []string{"alpha"})

	rep, err := AdoptProjectInto(userTeam, projTeam, AdoptOptions{AllowLegacy: false})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Skipped == "" {
		t.Fatalf("isolated adopt must skip legacy, got %+v", rep)
	}
	if len(teamNames(t, userTeam)) != 0 {
		t.Fatalf("isolated adopt leaked legacy teams into the user store")
	}
}

// TestAdoptProjectIntoSameDirAndNoLegacySkip covers the two no-op guards: an
// empty legacy source and a user store that already is the source dir.
func TestAdoptProjectIntoSameDirAndNoLegacySkip(t *testing.T) {
	dir := t.TempDir()
	rep, err := AdoptProjectInto(dir, dir, AdoptOptions{AllowLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Skipped != "legacy and user store share one dir" {
		t.Fatalf("same-dir adopt skipped=%q", rep.Skipped)
	}

	projTeam := filepath.Join(t.TempDir(), ".reasonix", "team")
	userTeam := filepath.Join(t.TempDir(), "team")
	if err := os.MkdirAll(projTeam, 0o755); err != nil {
		t.Fatal(err)
	}
	rep, err = AdoptProjectInto(userTeam, projTeam, AdoptOptions{AllowLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Skipped != "no legacy teams" {
		t.Fatalf("empty-source adopt skipped=%q", rep.Skipped)
	}
}

// TestAdoptProjectIntoLeavesExistingGlobalTeamUntouched proves a team already
// in the user store — with its history — is never overwritten by a legacy
// project's same-named team: the registry entry and history both survive.
func TestAdoptProjectIntoLeavesExistingGlobalTeamUntouched(t *testing.T) {
	projTeam := filepath.Join(t.TempDir(), ".reasonix", "team")
	userTeam := filepath.Join(t.TempDir(), "team")
	for _, d := range []string{projTeam, userTeam} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	seedProjectTeamDir(t, projTeam, []string{"alpha", "gamma"})
	seedHistory(t, projTeam, "alpha", "legacy-m")
	seedHistory(t, projTeam, "gamma", "m1")
	// The user store already owns "alpha" with its own history.
	seedProjectTeamDir(t, userTeam, []string{"alpha"})
	seedHistory(t, userTeam, "alpha", "global-m")

	rep, err := AdoptProjectInto(userTeam, projTeam, AdoptOptions{AllowLegacy: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.TeamsCreated != 1 || rep.TeamsSkipped != 1 {
		t.Fatalf("adopt report = %+v, want gamma created and alpha skipped", rep)
	}
	names := teamNames(t, userTeam)
	if len(names) != 2 || !hasStr(names, "alpha") || !hasStr(names, "gamma") {
		t.Fatalf("user teams = %v, want alpha+gamma", names)
	}
	ss, err := NewTeamSessionStoreDir(userTeam)
	if err != nil {
		t.Fatal(err)
	}
	globalMsgs, err := ss.Messages("alpha", "global-m")
	if err != nil || len(globalMsgs) != 1 || globalMsgs[0].Text != "hello" {
		t.Fatalf("global alpha history was clobbered: msgs=%v err=%v", globalMsgs, err)
	}
	if _, err := ss.Messages("alpha", "legacy-m"); err == nil {
		if got, _ := ss.Messages("alpha", "legacy-m"); len(got) != 0 {
			t.Fatalf("legacy alpha history leaked under the existing global team")
		}
	}
}

// TestNewTeamStoreAtRootsAtExplicitDir proves a store rooted at an explicit
// team data dir reads and writes there, and Root() reports the anchor.
func TestNewTeamStoreAtRootsAtExplicitDir(t *testing.T) {
	dir := t.TempDir()
	anchor := filepath.Join(t.TempDir(), "workspace")
	s, err := NewTeamStoreAt(anchor, dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Root() != anchor {
		t.Fatalf("Root() = %q, want anchor %q", s.Root(), anchor)
	}
	if err := s.AddTeam(Team{Name: "zeta"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, TeamFile)); err != nil {
		t.Fatalf("team.json not written at data dir %s: %v", dir, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".reasonix", "team", TeamFile)); err == nil {
		t.Fatalf("team.json must not nest a second .reasonix/team under an explicit data dir")
	}
	bare, err := NewTeamStoreAt("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if bare.Root() != dir {
		t.Fatalf("empty-anchor Root() = %q, want data dir %q", bare.Root(), dir)
	}
}

// TestNewTeamSessionStoreDirRootsContextUnderDataDir proves member context
// files land under the explicit data dir, not a nested .reasonix/team.
func TestNewTeamSessionStoreDirRootsContextUnderDataDir(t *testing.T) {
	dir := t.TempDir()
	ss, err := NewTeamSessionStoreDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ss.AppendMessage("omega", "m1", SessionMessage{Kind: "user", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(dir, contextRootDir, "omega", "m1", MemberMessagesFile)
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("history not under data dir: %v", err)
	}
}
