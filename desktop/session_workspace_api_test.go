package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func TestWorkspaceSessionListSurvivesRuntimePruneAndAppRestart(t *testing.T) {
	root := t.TempDir()
	sessionRoot := filepath.Join(root, "desktop-sessions-v5", "by-id")
	statePath := filepath.Join(root, "desktop", "workspace-state-v1.json")
	projectRoot := filepath.Join(root, "project")

	first := NewApp()
	t.Cleanup(first.closeSessionServices)
	first.desktopSessions.root = sessionRoot
	first.desktopSessions.workspaceState = workspacestate.NewStore(statePath)
	service := first.desktopSessionService(filepath.Join(projectRoot, "sessions"))
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "durable-session", CWD: projectRoot, Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	payload, err := json.Marshal(map[string]any{"message": map[string]any{"id": "user-1", "role": "user", "content": "keep me"}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := runtime.Session().AppendBatch(t.Context(), "user-turn", []session.Event{{Kind: "message/complete", Payload: payload}}); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	workspaceID, err := first.ensureDesktopWorkspace(t.Context(), "project", projectRoot)
	if err != nil {
		t.Fatalf("ensureDesktopWorkspace: %v", err)
	}
	if err := first.desktopSessions.workspaceState.AttachSession(t.Context(), "", workspaceID, runtime.Ref().SessionID, ""); err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	first.closeSessionServices()

	second := NewApp()
	t.Cleanup(second.closeSessionServices)
	second.desktopSessions.root = sessionRoot
	second.desktopSessions.workspaceState = workspacestate.NewStore(statePath)
	page, err := second.ListWorkspaceSessions(workspaceID, "", "", 10, false)
	if err != nil {
		t.Fatalf("ListWorkspaceSessions after restart: %v", err)
	}
	if len(page.Sessions) != 1 || page.Sessions[0].Ref.SessionID != "durable-session" || page.Sessions[0].Preview != "keep me" {
		t.Fatalf("sessions after restart = %#v", page.Sessions)
	}
	if err := second.ArchiveCanonicalSession(page.Sessions[0].Ref); err != nil {
		t.Fatalf("ArchiveCanonicalSession: %v", err)
	}
	visible, err := second.ListWorkspaceSessions(workspaceID, "", "", 10, false)
	if err != nil {
		t.Fatalf("List visible: %v", err)
	}
	if len(visible.Sessions) != 0 {
		t.Fatalf("visible archived sessions = %#v", visible.Sessions)
	}
	archived, err := second.ListWorkspaceSessions(workspaceID, "", "", 10, true)
	if err != nil {
		t.Fatalf("List archived: %v", err)
	}
	if len(archived.Sessions) != 1 || !archived.Sessions[0].Archived {
		t.Fatalf("archived sessions = %#v", archived.Sessions)
	}
	if err := second.RestoreCanonicalSession(archived.Sessions[0].Ref); err != nil {
		t.Fatalf("RestoreCanonicalSession: %v", err)
	}
}

func TestSessionRefHistoryAndRenameDoNotNeedController(t *testing.T) {
	root := t.TempDir()
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.desktopSessions.root = filepath.Join(root, "desktop-sessions-v5", "by-id")
	app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(root, "desktop", "workspace-state-v1.json"))
	service := app.desktopSessionService("")
	runtime, err := service.Create(t.Context(), session.CreateOptions{SessionID: "cold-session", CWD: root, Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"message": map[string]any{"id": "user-1", "role": "user", "content": "cold history"}})
	if _, err := runtime.Session().AppendBatch(t.Context(), "turn", []session.Event{{Kind: "message/complete", Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "global", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.desktopSessions.workspaceState.AttachSession(t.Context(), "", workspaceID, "cold-session", ""); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(t.Context(), runtime.Ref()); err != nil {
		t.Fatal(err)
	}
	page, err := app.ReadSessionHistory(runtime.Ref(), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].Content != "cold history" {
		t.Fatalf("history = %#v", page.Messages)
	}
	if err := app.RenameCanonicalSession(runtime.Ref(), "Cold title"); err != nil {
		t.Fatal(err)
	}
	listed, err := app.ListWorkspaceSessions(workspaceID, "", "", 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].Title != "Cold title" {
		t.Fatalf("renamed list = %#v", listed.Sessions)
	}
	missing := session.SessionRef{HostID: "local", SessionID: "missing"}
	if _, err := app.ReadSessionHistory(missing, "", 10); err == nil {
		t.Fatal("missing SessionID produced readable replacement history")
	}
	if _, err := service.Query().Snapshot(t.Context(), missing); err == nil {
		t.Fatal("missing SessionID was created as an empty replacement")
	}
}

func TestForkSessionPublishesHeaderBackedChildAfterParent(t *testing.T) {
	root := t.TempDir()
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.ctx = t.Context()
	app.desktopSessions.root = filepath.Join(root, "desktop-sessions-v5", "by-id")
	app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(root, "desktop", "workspace-state-v1.json"))
	workspaceID, err := app.ensureDesktopWorkspace(t.Context(), "project", root)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{SessionID: "fork-parent", CWD: root, Origin: session.SessionOriginNew})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"message": provider.Message{ID: "answer", Role: provider.RoleAssistant, Content: "forked history"}})
	if _, err := parent.Session().Append(t.Context(), session.Batch{OperationID: "turn-1", TurnID: "turn-1", Events: []session.Event{
		{Kind: "turn/start"}, {Kind: "message/complete", Payload: payload}, {Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := parent.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := app.desktopSessions.workspaceState.AttachSession(t.Context(), "", workspaceID, parent.Ref().SessionID, ""); err != nil {
		t.Fatal(err)
	}
	child, err := app.ForkSession(parent.Ref(), "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	state, err := app.desktopSessions.workspaceState.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ids := state.Workspaces[workspaceID].SessionIDs
	if len(ids) != 2 || ids[0] != parent.Ref().SessionID || ids[1] != child.SessionID {
		t.Fatalf("fork order = %#v", ids)
	}
	infos, err := listAllCanonicalSessionInfo(t.Context(), app.desktopSessionService("").Query())
	if err != nil {
		t.Fatal(err)
	}
	if infos[child.SessionID].ParentSessionID != parent.Ref().SessionID || infos[child.SessionID].Origin != session.SessionOriginFork {
		t.Fatalf("fork header = %+v", infos[child.SessionID])
	}
}
