package boot

// Acceptance tests for the B3 fix in TEAM_MEMBER_PARALLELISM_ROUTE.md: a member
// waiting for the workspace write lease must produce a distinguishable signal,
// and a whole-workspace wait must be tellable from a file wait.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/workspacelease"
)

// leasePair returns the shared workspace root and two Owners over it, with the
// peer's wait notices funneled into a channel and a wait helper that reports a
// missing notice instead of hanging the test.
func leasePair(t *testing.T) (root string, holder, peer *workspacelease.Owner, waited func() bool) {
	t.Helper()
	root, locks := t.TempDir(), t.TempDir()
	var err error
	if holder, err = workspacelease.New(root, locks, nil); err != nil {
		t.Fatal(err)
	}
	waiting := make(chan struct{}, 1)
	if peer, err = workspacelease.New(root, locks, func() {
		select {
		case waiting <- struct{}{}:
		default:
		}
	}); err != nil {
		t.Fatal(err)
	}
	holder.BeginRun()
	peer.BeginRun()
	t.Cleanup(func() {
		peer.EndRun()
		holder.EndRun()
	})
	return root, holder, peer, func() bool {
		t.Helper()
		select {
		case <-waiting:
			return true
		case <-time.After(2 * time.Second):
			return false
		}
	}
}

// TestWorkspaceLeaseWaitEventNamesTheWorkspaceWait pins the whole-workspace
// case: the member queued behind another writer's workspace-wide hold reports a
// wait that names the whole workspace, under the existing notice code so every
// current consumer still sees a lease notice.
func TestWorkspaceLeaseWaitEventNamesTheWorkspaceWait(t *testing.T) {
	_, holder, peer, waited := leasePair(t)
	if err := holder.AcquireWrite(context.Background()); err != nil {
		t.Fatal(err)
	}

	queued := make(chan error, 1)
	go func() { queued <- peer.AcquireWrite(context.Background()) }()
	if !waited() {
		t.Fatal("the queued writer never reported waiting")
	}
	ev := workspaceLeaseWaitEvent(peer)

	holder.ReleaseWrite()
	if err := <-queued; err != nil {
		t.Fatalf("queued writer after release: %v", err)
	}

	if ev.Code != event.NoticeCodeWorkspaceLease {
		t.Fatalf("notice code = %q, want %q", ev.Code, event.NoticeCodeWorkspaceLease)
	}
	if !strings.Contains(ev.Detail, "whole workspace") {
		t.Fatalf("wait detail = %q, want it to name the whole workspace", ev.Detail)
	}
}

// TestWorkspaceLeaseWaitEventNamesTheFileWait pins the other half of the
// distinction: a queued file-scoped write names the file, so a member blocked on
// one path is tellable from a member blocked on the whole workspace.
func TestWorkspaceLeaseWaitEventNamesTheFileWait(t *testing.T) {
	root, holder, peer, waited := leasePair(t)
	file := filepath.Join(root, "component.go")
	if err := os.WriteFile(file, []byte("package component\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := holder.HoldWriteForPath(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}

	queued := make(chan error, 1)
	go func() {
		held, err := peer.HoldWriteForPath(context.Background(), file)
		if err == nil {
			held()
		}
		queued <- err
	}()
	if !waited() {
		t.Fatal("the queued file writer never reported waiting")
	}
	ev := workspaceLeaseWaitEvent(peer)

	release()
	if err := <-queued; err != nil {
		t.Fatalf("queued file writer after release: %v", err)
	}

	if ev.Code != event.NoticeCodeWorkspaceLease {
		t.Fatalf("notice code = %q, want %q", ev.Code, event.NoticeCodeWorkspaceLease)
	}
	if !strings.Contains(ev.Detail, "component.go") {
		t.Fatalf("wait detail = %q, want it to name the file", ev.Detail)
	}
	if strings.Contains(ev.Detail, "whole workspace") {
		t.Fatalf("a file wait must not be reported as a whole-workspace wait: %q", ev.Detail)
	}
}
