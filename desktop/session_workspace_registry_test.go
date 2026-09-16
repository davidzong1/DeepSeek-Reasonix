package main

import (
	"context"
	"path/filepath"
	"testing"

	"reasonix/desktop/internal/workspacestate"
	"reasonix/internal/session"
)

type registrySessionCreator struct{ service *session.Service }

func (c registrySessionCreator) BindFreshSession(ctx context.Context, sessionID string) (session.SessionRef, error) {
	runtime, err := c.service.Create(ctx, session.CreateOptions{SessionID: sessionID})
	if err != nil {
		return session.SessionRef{}, err
	}
	return runtime.Ref(), nil
}

func (c registrySessionCreator) BindFreshSessionWithOptions(ctx context.Context, options session.CreateOptions) (session.SessionRef, error) {
	runtime, err := c.service.Create(ctx, options)
	if err != nil {
		return session.SessionRef{}, err
	}
	return runtime.Ref(), nil
}

func TestFreshDesktopSessionIsDurableRegistryMemberBeforeReturn(t *testing.T) {
	root := t.TempDir()
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.desktopSessions.root = filepath.Join(root, "desktop-sessions-v5", "by-id")
	app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(root, "desktop", "workspace-state-v1.json"))
	service := app.desktopSessionService(filepath.Join(root, "old-project-sessions"))
	project := filepath.Join(root, "project")

	ref, workspaceID, err := app.bindFreshDesktopSession(t.Context(), "project", project, registrySessionCreator{service: service})
	if err != nil {
		t.Fatalf("bindFreshDesktopSession: %v", err)
	}
	state, err := app.desktopSessions.workspaceState.Load(t.Context())
	if err != nil {
		t.Fatalf("Load workspace state: %v", err)
	}
	workspace := state.Workspaces[workspaceID]
	if len(workspace.SessionIDs) != 1 || workspace.SessionIDs[0] != ref.SessionID {
		t.Fatalf("workspace sessions = %#v, ref = %#v", workspace.SessionIDs, ref)
	}
	if len(state.PendingCreates) != 0 {
		t.Fatalf("pending creates = %#v", state.PendingCreates)
	}
	page, err := service.Query().List(t.Context(), "", 10)
	if err != nil {
		t.Fatalf("List sessions: %v", err)
	}
	if len(page.Sessions) != 1 || page.Sessions[0].SessionID != ref.SessionID || page.Sessions[0].CWD != project {
		t.Fatalf("sessions = %#v", page.Sessions)
	}
}

func TestWorkspaceRegistryRejectsSessionHeaderFromDifferentWorkspace(t *testing.T) {
	root := t.TempDir()
	app := NewApp()
	t.Cleanup(app.closeSessionServices)
	app.desktopSessions.root = filepath.Join(root, "desktop-sessions-v5", "by-id")
	app.desktopSessions.workspaceState = workspacestate.NewStore(filepath.Join(root, "desktop", "workspace-state-v1.json"))
	service := app.desktopSessionService("")
	projectA := filepath.Join(root, "project-a")
	projectB := filepath.Join(root, "project-b")
	runtime, err := service.Create(t.Context(), session.CreateOptions{
		SessionID: "belongs-to-a", CWD: projectA, Origin: session.SessionOriginNew,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := app.attachDesktopSession(t.Context(), "project", projectB, runtime.Ref()); err == nil {
		t.Fatal("attaching a session to a workspace that disagrees with its immutable header succeeded")
	}
	state, err := app.workspaceRegistry().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	workspace := state.Workspaces[desktopWorkspaceID("project", projectB)]
	if len(workspace.SessionIDs) != 0 {
		t.Fatalf("mismatched workspace members = %#v", workspace.SessionIDs)
	}
}
