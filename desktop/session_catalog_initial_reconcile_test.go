package main

import (
	"testing"
	"time"
)

func TestSessionCatalogInitialReconcileSignalFollowsRestoredTabs(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	app.tabsRestored = make(chan struct{})
	app.startSessionCatalog()
	t.Cleanup(func() { app.stopSessionCatalog(time.Second) })
	_ = waitForSessionCatalogForTest(t, app, nil)

	app.catalogLifecycleMu.Lock()
	done := app.catalogInitialReconcileDone
	app.catalogLifecycleMu.Unlock()
	if done == nil {
		t.Fatal("initial reconcile signal was not armed")
	}
	select {
	case <-done:
		t.Fatal("initial reconcile completed before restored tabs were published")
	default:
	}

	app.markTabsRestored()
	select {
	case <-done:
	case <-time.After(sessionCatalogTestDeadline):
		t.Fatal("initial reconcile did not complete after restored tabs were published")
	}
}
