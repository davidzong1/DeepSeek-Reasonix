package team

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// modeFixtureStore roots a fresh two-member store (a leader and one member) at
// its own temp dir and returns the store plus the project root for reopening.
func modeFixtureStore(t *testing.T) (*TeamStore, string) {
	t.Helper()
	root := t.TempDir()
	store, err := NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(TeamDoc{Document: Document{SchemaVersion: SchemaVersion}, Teams: []Team{{
		Name: "alpha",
		Template: []MemberSlot{
			{MemberID: "lead", Leader: true, Status: MemberStatusActive},
			{MemberID: "alice", Role: RoleCoder, Status: MemberStatusActive},
		},
	}}}); err != nil {
		t.Fatal(err)
	}
	return store, root
}

// slotOfDoc returns the named slot from the stored document.
func slotOfDoc(t *testing.T, store *TeamStore, memberID string) MemberSlot {
	t.Helper()
	doc, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, slot := range doc.Teams[0].Template {
		if slot.MemberID == memberID {
			return slot
		}
	}
	t.Fatalf("member %q missing from stored doc", memberID)
	return MemberSlot{}
}

// TestMemberApprovalModeDefaultsAndLeaderPinned pins the read semantics: every
// slot reads auto by default (the empty field a pre-mode document carries), the
// leader reads auto even when stored bytes claim manual — a hand-edited
// document cannot unlock the leader — and the write path refuses the leader.
func TestMemberApprovalModeDefaultsAndLeaderPinned(t *testing.T) {
	store, _ := modeFixtureStore(t)
	doc, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"alice", "lead"} {
		if got := slotOfDoc(t, store, id).EffectiveApprovalMode(); got != ApprovalModeAuto {
			t.Fatalf("%s default mode = %q, want auto", id, got)
		}
	}
	if err := store.SetMemberApprovalMode("alpha", "lead", ApprovalModeManual); !errors.Is(err, ErrLeaderApprovalModeFixed) {
		t.Fatalf("leader mode change = %v, want ErrLeaderApprovalModeFixed", err)
	}
	if got := slotOfDoc(t, store, "lead").ApprovalMode; got != "" {
		t.Fatalf("a refused leader write must not persist, stored = %q", got)
	}
	// A hand-edited document that stores manual on the leader still reads auto.
	doc.Teams[0].Template[0].ApprovalMode = ApprovalModeManual
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	if got := slotOfDoc(t, store, "lead").EffectiveApprovalMode(); got != ApprovalModeAuto {
		t.Fatalf("leader effective mode = %q, want auto", got)
	}
}

// TestSetMemberApprovalModePersistsAcrossStores pins the self-service write
// path: manual persists to a fresh store on the same root (a restart keeps the
// mode), auto writes the empty field back so a default member's slot returns to
// its pre-mode bytes, and unknown modes, members, and teams are refused.
func TestSetMemberApprovalModePersistsAcrossStores(t *testing.T) {
	store, root := modeFixtureStore(t)
	if err := store.SetMemberApprovalMode("alpha", "alice", ApprovalModeManual); err != nil {
		t.Fatal(err)
	}
	if got := slotOfDoc(t, store, "alice").ApprovalMode; got != ApprovalModeManual {
		t.Fatalf("stored mode = %q, want manual", got)
	}
	reopened, err := NewTeamStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := slotOfDoc(t, reopened, "alice").EffectiveApprovalMode(); got != ApprovalModeManual {
		t.Fatalf("mode after restart = %q, want manual", got)
	}
	if err := reopened.SetMemberApprovalMode("alpha", "alice", ApprovalModeAuto); err != nil {
		t.Fatal(err)
	}
	if got := slotOfDoc(t, reopened, "alice").ApprovalMode; got != "" {
		t.Fatalf("auto must clear the field, stored = %q", got)
	}
	if err := reopened.SetMemberApprovalMode("alpha", "alice", "yolo"); !errors.Is(err, ErrInvalidApprovalMode) {
		t.Fatalf("unknown mode = %v, want ErrInvalidApprovalMode", err)
	}
	if err := reopened.SetMemberApprovalMode("alpha", "nobody", ApprovalModeManual); err == nil {
		t.Fatal("unknown member must be refused")
	}
	if err := reopened.SetMemberApprovalMode("missing", "alice", ApprovalModeManual); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("unknown team = %v, want ErrTeamNotFound", err)
	}
}

// TestAuthzLedgerAppendReadPins the ledger contract: entries read back newest
// first with every field intact, a team with no ledger is empty not an error,
// and a team key that could escape the data dir is refused before any write.
func TestAuthzLedgerAppendRead(t *testing.T) {
	store, _ := modeFixtureStore(t)
	if err := store.AppendAuthz("alpha", AuthzEntry{TS: "t1", Member: "alice", Source: "auto", Allow: true, ID: "a1", Tool: "bash"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendAuthz("alpha", AuthzEntry{TS: "t2", Member: "alice", Source: "leader", Allow: false, ID: "a2", Tool: "read_file", Subject: "notes.md"}); err != nil {
		t.Fatal(err)
	}
	entries, err := store.AuthzEntries("alpha", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("read back %d entries, want 2", len(entries))
	}
	if got := entries[0]; got.TS != "t2" || got.Member != "alice" || got.Source != "leader" || got.Allow || got.ID != "a2" || got.Tool != "read_file" || got.Subject != "notes.md" {
		t.Fatalf("newest entry = %+v, want the leader deny of a2", got)
	}
	if got := entries[1]; got.ID != "a1" || got.Source != "auto" || !got.Allow {
		t.Fatalf("oldest entry = %+v, want the auto grant of a1", got)
	}
	if got, err := store.AuthzEntries("beta", 0); err != nil || got != nil {
		t.Fatalf("unseeded team read = %v, %v; want nil entries", got, err)
	}
	if err := store.AppendAuthz("a/b", AuthzEntry{}); !errors.Is(err, ErrInvalidSessionKey) {
		t.Fatalf("path-escaping team name = %v, want ErrInvalidSessionKey", err)
	}
}

// TestAuthzLedgerBoundedAndTornTailSafe pins the read bounds and crash
// tolerance: a query is clamped to the page ceiling with the newest entries
// kept, and a record torn by a crash mid-append is skipped, never misread.
func TestAuthzLedgerBoundedAndTornTailSafe(t *testing.T) {
	store, _ := modeFixtureStore(t)
	for i := range authzReadPage + 6 {
		if err := store.AppendAuthz("alpha", AuthzEntry{TS: fmt.Sprintf("t%02d", i), Member: "alice", Source: "auto", Allow: true, ID: fmt.Sprintf("a%02d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := store.AuthzEntries("alpha", authzReadPage*10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != authzReadPage {
		t.Fatalf("bounded read returned %d entries, want the %d ceiling", len(entries), authzReadPage)
	}
	if got := entries[0].ID; got != fmt.Sprintf("a%02d", authzReadPage+5) {
		t.Fatalf("newest after clamp = %q, want the last appended id", got)
	}
	// A crash mid-append leaves a final line that is not valid JSON: it must be
	// skipped, and the ledger must still read clean.
	ledger := filepath.Join(storeDataDir(t, store), "authz", "alpha.jsonl")
	f, err := os.OpenFile(ledger, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{broken"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err = store.AuthzEntries("alpha", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != authzReadPage {
		t.Fatalf("after a torn tail read %d entries, want %d", len(entries), authzReadPage)
	}
}

// storeDataDir returns the team data dir a NewTeamStore(root) store writes to.
func storeDataDir(t *testing.T, store *TeamStore) string {
	t.Helper()
	return filepath.Join(store.Root(), ".reasonix", "team")
}

// TestAuthzLedgerConcurrentAppendsLoseNothing pins the concurrency contract:
// each decision is one O_APPEND write, so parallel appends never drop a record.
func TestAuthzLedgerConcurrentAppendsLoseNothing(t *testing.T) {
	store, _ := modeFixtureStore(t)
	const writers, each = 4, 10
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range each {
				if err := store.AppendAuthz("alpha", AuthzEntry{TS: "t", Member: "alice", Source: "auto", Allow: true, ID: fmt.Sprintf("w%d-%d", w, i)}); err != nil {
					t.Error(err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	entries, err := store.AuthzEntries("alpha", writers*each)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != writers*each {
		t.Fatalf("read %d entries after %d concurrent appends, want all of them", len(entries), writers*each)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.ID] {
			t.Fatalf("duplicate entry %q", e.ID)
		}
		seen[e.ID] = true
	}
}
