package main

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/serve"
	"reasonix/internal/servecontract"
)

func TestRemoteInboxQueueCapabilitiesAndIdentity(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: path, Sink: event.Discard})
	defer ctrl.Close()
	if err := ctrl.SetInboxPaused(true); err != nil {
		t.Fatal(err)
	}
	receipt, err := ctrl.EnqueueInbox(control.InboxRequest{Submit: "remote full text", Idempotency: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(serve.New(ctrl, nil, config.ServeConfig{}).Handler())
	defer server.Close()
	a, tab := remoteRuntimeTestApp(server.Client())
	tab.base, tab.routing.currentPath, tab.session.path = server.URL, path, path
	target, err := a.CaptureInboxTarget(tab.id, path)
	if err != nil {
		t.Fatal(err)
	}
	request := control.InboxQueueRequest{Kind: "read", ItemID: receipt.ItemID}
	unsupported, err := a.InboxQueueForTarget(target, request)
	if err != nil || unsupported.Reason != "unsupported" {
		t.Fatalf("old remote: %+v %v", unsupported, err)
	}
	tab.capabilities[servecontract.InboxMutationsV1] = true
	read, err := a.InboxQueueForTarget(target, request)
	if err != nil || read.Edit == nil || read.Edit.Text != "remote full text" {
		t.Fatalf("read: %+v %v", read, err)
	}
	request = control.InboxQueueRequest{Kind: "edit", ItemID: receipt.ItemID, ContentVersion: read.Edit.ContentVersion, Text: "edited remote"}
	saved, err := a.InboxQueueForTarget(target, request)
	if err != nil || saved.Outcome != "applied" || !saved.Snapshot.Paused {
		t.Fatalf("save: %+v %v", saved, err)
	}
	tab.selectionRevision++
	stale, err := a.InboxQueueForTarget(target, request)
	if err != nil || stale.Reason != "session_changed" {
		t.Fatalf("stale target: %+v %v", stale, err)
	}
	_, env, _ := ctrl.ReadInboxItem(receipt.ItemID)
	if env.SubmitText != "edited remote" {
		t.Fatal("remote queue changed unexpectedly")
	}
	tab.selectionRevision--
	ctrl.SetSessionPath(filepath.Join(dir, "other.jsonl"))
	stale, err = a.InboxQueueForTarget(target, control.InboxQueueRequest{Kind: "pause", Paused: true})
	if err != nil || stale.Reason != "session_changed" {
		t.Fatalf("remote foreground switch: %+v %v", stale, err)
	}
}
