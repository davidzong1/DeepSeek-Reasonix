package team

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedTeamContext writes one message for each member so the team context root
// exists with real history, then returns the store.
func seedTeamContext(t *testing.T, s *TeamSessionStore, teamName string, members ...string) {
	t.Helper()
	for _, m := range members {
		if err := s.AppendMessage(teamName, m, SessionMessage{Kind: "user", Text: "hello"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSessionStoreTrashTeamStagesAndClears(t *testing.T) {
	s := newTestSessionStore(t)
	seedTeamContext(t, s, "t", "m1", "m2")

	trash, err := s.TrashTeam("t")
	if err != nil {
		t.Fatal(err)
	}
	if trash == "" {
		t.Fatal("TrashTeam should return a staged trash path")
	}
	if !strings.HasPrefix(filepath.ToSlash(trash), trashRootDir+"/") {
		t.Fatalf("staged trash %q should live under %s/", trash, trashRootDir)
	}
	if _, err := os.Stat(filepath.Join(s.store.root, filepath.Join("context", "t"))); !os.IsNotExist(err) {
		t.Fatalf("context root should be gone after staging, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.store.root, filepath.FromSlash(trash))); err != nil {
		t.Fatalf("staged trash should exist: %v", err)
	}
	// Other teams stay untouched.
	if _, err := os.Stat(filepath.Join(s.store.root, filepath.Join("context", "other"))); !os.IsNotExist(err) {
		t.Fatal("an unrelated team context should not exist")
	}

	if err := s.RemoveTrash(trash); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.store.root, filepath.FromSlash(trash))); !os.IsNotExist(err) {
		t.Fatalf("trash should be gone after remove, stat err = %v", err)
	}
}

func TestSessionStoreTrashTeamAbsentRoot(t *testing.T) {
	s := newTestSessionStore(t)
	trash, err := s.TrashTeam("ghost")
	if err != nil {
		t.Fatal(err)
	}
	if trash != "" {
		t.Fatalf("absent context root should stage nothing, got %q", trash)
	}
}

func TestSessionStoreRemoveTrashIdempotent(t *testing.T) {
	s := newTestSessionStore(t)
	seedTeamContext(t, s, "t", "m1")
	trash, err := s.TrashTeam("t")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveTrash(trash); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveTrash(trash); err != nil {
		t.Fatalf("second remove must be idempotent: %v", err)
	}
	// Escaping trash paths are refused, never deleted.
	if err := s.RemoveTrash("../outside"); err == nil {
		t.Fatal("escaping trash path must be refused")
	}
}

func TestSessionStoreClearTeamTrashIdempotent(t *testing.T) {
	s := newTestSessionStore(t)
	seedTeamContext(t, s, "t", "m1", "m2")

	// A simulated crash between staging and deletion leaves trash behind; the
	// next clear sweeps it and completes.
	if _, err := s.TrashTeam("t"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearTeamTrash("t"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.store.root, filepath.Join("context", "t"))); !os.IsNotExist(err) {
		t.Fatal("context root should be gone after ClearTeamTrash")
	}
	entries, err := os.ReadDir(filepath.Join(s.store.root, trashRootDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("no trash dirs should remain after a completed clear, got %d", len(entries))
	}
	// Repeating the clear is a no-op, not an error.
	if err := s.ClearTeamTrash("t"); err != nil {
		t.Fatalf("repeated clear must be idempotent: %v", err)
	}
}

func TestSessionStoreClearTeamTrashScopedToTeam(t *testing.T) {
	s := newTestSessionStore(t)
	seedTeamContext(t, s, "t", "m1")
	seedTeamContext(t, s, "other", "x1")

	if err := s.ClearTeamTrash("t"); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.Messages("other", "x1")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("another team's history must survive the clear, got %d messages", len(msgs))
	}
}

// TestSessionStoreClearTeamTrashFailureKeepsTrash pins the §6.6 guarantee: a
// failed deletion leaves the staged trash in place and reports the error.
// The failure needs a read-only trash dir, which the root user can bypass.
func TestSessionStoreClearTeamTrashFailureKeepsTrash(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based failure injection is bypassed as root")
	}
	s := newTestSessionStore(t)
	seedTeamContext(t, s, "t", "m1")

	trash, err := s.TrashTeam("t")
	if err != nil {
		t.Fatal(err)
	}
	trashFull := filepath.Join(s.store.root, filepath.FromSlash(trash))
	if err := os.Chmod(trashFull, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(trashFull, 0o700) })

	if err := s.RemoveTrash(trash); err == nil {
		t.Fatal("RemoveTrash on a read-only trash dir should fail")
	}
	if _, err := os.Stat(trashFull); err != nil {
		t.Fatalf("the staged trash must be preserved on failure, stat err = %v", err)
	}
	// A fresh clear after the failure is refused for the same reason; restoring
	// write access lets the retry complete.
	if err := os.Chmod(trashFull, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveTrash(trash); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(trashFull); !os.IsNotExist(err) {
		t.Fatal("the retry should complete the deletion")
	}
}
