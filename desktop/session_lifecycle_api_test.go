package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func TestPublicDeleteArchivesLegacyWithoutMovingOriginal(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	a.ctx = t.Context()
	pinDesktopSessionRoot(t, a)
	installNoopRuntimeEvents(a)
	t.Cleanup(a.closeSessionServices)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := writeLegacySession(t, dir, "public-archive.jsonl", "preserved original", time.Now())
	before, err := desktopSourceFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteSession(path); err != nil {
		t.Fatal(err)
	}
	after, err := desktopSourceFingerprint(path)
	if err != nil || before != after {
		t.Fatalf("archive modified legacy original: %v", err)
	}
	page, err := a.ListTrashEntries("", "", 50)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("archive not in trash: %+v %v", page, err)
	}
	ref := page.Items[0].Ref
	if result, err := a.ApplySessionLifecycle(lifecycleRequest(t, a, ref, "restore-public-archive", "restore")); err != nil || !result.Committed {
		t.Fatalf("restore: %+v %v", result, err)
	}
	if err := a.ArchiveCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	if err := a.PurgeCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	if err := a.migrateDesktopSessionsV5(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := a.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Deleted {
		t.Fatal("rescan revived purged identity")
	}
	for _, w := range state.Workspaces {
		if len(w.SessionIDs) != 0 {
			t.Fatalf("rescan recreated source under a new ID: %v", w.SessionIDs)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("upgrade original removed by purge")
	}
}

func TestPublicLegacyTrashPurgeRetainsUpgradeOriginal(t *testing.T) {
	isolateDesktopUserDirs(t)
	a := NewApp()
	a.ctx = t.Context()
	pinDesktopSessionRoot(t, a)
	installNoopRuntimeEvents(a)
	t.Cleanup(a.closeSessionServices)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := writeLegacySession(t, dir, "old-trash.jsonl", "retained upgrade evidence", time.Now())
	if err := deleteSessionFile(dir, path); err != nil {
		t.Fatal(err)
	}
	trash := filepath.Join(dir, sessionTrashDir, "old-trash.jsonl", "old-trash.jsonl")
	before, err := desktopSourceFingerprint(trash)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.discoverHistoricalTrash(t.Context()); err != nil {
		t.Fatal(err)
	}
	page, err := a.ListTrashEntries("", "", 50)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("legacy trash missing: %+v %v", page, err)
	}
	if page.Items[0].ArchivedAt != trashedSessionDeletedAt(trash) {
		t.Fatal("legacy archive time changed")
	}
	if err := a.PurgeTrashedSession(trash); err != nil {
		t.Fatal(err)
	}
	after, err := desktopSourceFingerprint(trash)
	if err != nil || before != after {
		t.Fatalf("original removed: %v", err)
	}
	if err := a.discoverHistoricalTrash(t.Context()); err != nil {
		t.Fatal(err)
	}
	page, err = a.ListTrashEntries("", "", 50)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("purged history revived: %+v %v", page, err)
	}
}

func lifecycleFixture(t *testing.T) (*App, session.SessionRef) {
	t.Helper()
	isolateDesktopUserDirs(t)
	a := NewApp()
	a.ctx = t.Context()
	pinDesktopSessionRoot(t, a)
	installNoopRuntimeEvents(a)
	t.Cleanup(a.closeSessionServices)
	w, err := a.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	s := a.desktopSessionService("")
	runtime, err := s.Create(t.Context(), session.CreateOptions{SessionID: "lifecycle-fixture", CWD: globalWorkspaceRoot(), Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	appendSessionTestMessage(t, runtime, "user", provider.Message{ID: "user", Role: provider.RoleUser, Content: "retained history"})
	ref := runtime.Ref()
	if err := s.Close(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if err := a.workspaceRegistry().AttachSession(t.Context(), "", w, ref.SessionID, ""); err != nil {
		t.Fatal(err)
	}
	return a, ref
}

func lifecycleRequest(t *testing.T, a *App, ref session.SessionRef, id, action string) SessionLifecycleRequest {
	t.Helper()
	state, err := a.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return SessionLifecycleRequest{OperationID: id, Action: action, Targets: []SessionLifecycleTarget{{Ref: &ref}}, ExpectedGeneration: state.Generation}
}

func TestLifecycleCommandReceiptDoesNotReplayAfterRestore(t *testing.T) {
	a, ref := lifecycleFixture(t)
	req := lifecycleRequest(t, a, ref, "archive", "archive")
	archived, err := a.ApplySessionLifecycle(req)
	if err != nil || !archived.Committed {
		t.Fatalf("archive: %+v %v", archived, err)
	}
	page, err := a.ListTrashEntries("", "", 50)
	if err != nil || len(page.Items) != 1 || page.Items[0].ArchivedAt == 0 {
		t.Fatalf("trash: %+v %v", page, err)
	}
	if result, err := a.ApplySessionLifecycle(lifecycleRequest(t, a, ref, "restore", "restore")); err != nil || !result.Committed {
		t.Fatalf("restore: %+v %v", result, err)
	}
	// New store instance forces a durable receipt read, rather than an RPC cache.
	a.desktopSessions.workspaceState = workspacestate.NewStore(a.workspaceRegistry().Path())
	again, err := a.ApplySessionLifecycle(req)
	if err != nil || again.Generation != archived.Generation {
		t.Fatalf("replay: %+v %v", again, err)
	}
	state, _ := a.workspaceRegistry().Load(t.Context())
	if state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Active {
		t.Fatal("old archive request archived restored session")
	}
	req.Action = "purge"
	if _, err := a.ApplySessionLifecycle(req); err == nil {
		t.Fatal("reused operation accepted different request")
	}
	history, err := a.desktopSessionService("").Query().History(t.Context(), ref)
	if err != nil || len(history) == 0 {
		t.Fatalf("history lost: %v", err)
	}
}

func TestPurgeInterruptedTombstoneRemainsActionableAndResumes(t *testing.T) {
	a, ref := lifecycleFixture(t)
	ctx := t.Context()
	store := a.workspaceRegistry()
	if err := a.ArchiveCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginPurge(ctx, ref.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := store.AdvancePurge(ctx, ref.SessionID, "tombstoned"); err != nil {
		t.Fatal(err)
	}
	page, err := a.ListTrashEntries("", "", 50)
	if err != nil || len(page.Items) != 1 || page.Items[0].CanRestore || page.Items[0].OperationPhase != "tombstoned" {
		t.Fatalf("pending purge disappeared: %+v %v", page, err)
	}
	if _, err := a.OpenSession(ref); err == nil {
		t.Fatal("tombstoned session opened")
	}
	if err := a.recoverDesktopSessionOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.PurgeCanonicalSession(ref); err != nil {
		t.Fatal(err)
	}
	page, err = a.ListTrashEntries("", "", 50)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("purge not completed: %+v %v", page, err)
	}
	state, _ := store.Load(ctx)
	if state.SessionStates[ref.SessionID].Lifecycle != workspacestate.Deleted {
		t.Fatal("tombstone lost")
	}
	if _, err := os.Stat(filepath.Join(a.desktopSessions.root, ref.SessionID)); !os.IsNotExist(err) {
		t.Fatalf("body remains: %v", err)
	}
	if err := store.AttachSession(ctx, "", "global", ref.SessionID, ""); err == nil {
		t.Fatal("deleted session revived")
	}
}

func TestMigrationNestedEvidenceUnknownFields(t *testing.T) {
	var receipt desktopMigrationReceipt
	var conversion desktopMigrationConversion
	if err := json.Unmarshal([]byte(`{"targetSessionId":"s","future":{"x":1}}`), &receipt); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"root":"r","sessionId":"s","codec":"c","future":{"x":1}}`), &conversion); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{receipt, conversion} {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		if string(fields["future"]) != `{"x":1}` {
			t.Fatalf("lost evidence: %s", body)
		}
	}
}
