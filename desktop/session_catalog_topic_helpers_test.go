package main

import (
	"testing"
	"time"
)

const sessionCatalogTestDeadline = 30 * time.Second

func waitForInitialCatalogReconcile(t *testing.T, app *App) bool {
	t.Helper()
	app.catalogLifecycleMu.Lock()
	alreadyStarted := app.catalogInitialReconcileDone != nil
	app.catalogLifecycleMu.Unlock()
	app.startSessionCatalog()
	app.catalogLifecycleMu.Lock()
	done := app.catalogInitialReconcileDone
	app.catalogLifecycleMu.Unlock()
	if done == nil {
		t.Fatal("session catalog initial reconcile was not armed")
	}
	select {
	case <-done:
	case <-time.After(sessionCatalogTestDeadline):
		t.Fatal("session catalog initial reconcile did not finish")
	}
	if app.sessionCatalog.Load() == nil {
		t.Fatal("session catalog exited before publication")
	}
	return alreadyStarted
}

func waitForCatalogReconcileJobs(t *testing.T, app *App) {
	t.Helper()
	jobs := make([]<-chan struct{}, 0)
	for _, target := range app.sessionCatalogTargets() {
		if !app.requestSessionCatalogReconcile(target.Path) {
			t.Fatalf("request catalog reconcile for %q", target.Path)
		}
		key := projectRootKey(target.Path)
		app.catalogReconcileMu.Lock()
		if job := app.catalogReconcileJobs[key]; job != nil {
			jobs = append(jobs, job.done)
		}
		app.catalogReconcileMu.Unlock()
	}
	deadline := time.Now().Add(sessionCatalogTestDeadline)
	for _, done := range jobs {
		if !waitChannelBefore(done, deadline) {
			t.Fatal("explicit session catalog reconcile did not finish")
		}
	}
}

func waitForCatalogTopic(t *testing.T, app *App, scope, workspaceRoot, topicID string) []ProjectNode {
	t.Helper()
	alreadyStarted := waitForInitialCatalogReconcile(t, app)
	t.Cleanup(func() { app.stopSessionCatalog(time.Second) })
	if alreadyStarted {
		waitForCatalogReconcileJobs(t, app)
	}
	nodes := app.ListProjectTree()
	for _, folder := range nodes {
		if scope == "project" && (!sameProjectRoot(folder.Root, workspaceRoot) || folder.Kind != "project") {
			continue
		}
		if scope != "project" && folder.Kind != "global_folder" {
			continue
		}
		for _, topic := range folder.Children {
			if topic.TopicID == topicID {
				return nodes
			}
		}
	}
	t.Fatalf("catalog topic %q did not become visible after reconciliation: %#v", topicID, nodes)
	return nil
}

func waitForCatalogTreeCondition(t *testing.T, app *App, description string, matches func([]ProjectNode) bool) []ProjectNode {
	t.Helper()
	alreadyStarted := waitForInitialCatalogReconcile(t, app)
	if alreadyStarted {
		waitForCatalogReconcileJobs(t, app)
	}
	nodes := app.ListProjectTree()
	if matches(nodes) {
		return nodes
	}
	t.Fatalf("catalog did not reach %s: %#v", description, nodes)
	return nil
}
