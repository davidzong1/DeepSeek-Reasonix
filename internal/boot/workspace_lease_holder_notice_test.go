package boot

// Acceptance tests for the L1 fix in TEAM_WRITE_LEASE_OPTIMIZATION_ROUTE.md: a
// writer queued behind an identified holder is told who is writing, in what mode
// and for how long, and keeps the generic wording without an identity.

import (
	"context"
	"strings"
	"testing"
	"time"

	"reasonix/internal/workspacelease"
)

// identifiedLeasePair returns two Owners over one workspace, with a labeled
// holder and a peer whose wait notices land on a channel.
func identifiedLeasePair(t *testing.T, label string) (holder, peer *workspacelease.Owner, waited func() bool) {
	t.Helper()
	root, locks := t.TempDir(), t.TempDir()
	var err error
	if holder, err = workspacelease.New(root, locks, nil); err != nil {
		t.Fatal(err)
	}
	holder.SetIdentity(label)
	notices := make(chan struct{}, 1)
	if peer, err = workspacelease.New(root, locks, func() {
		select {
		case notices <- struct{}{}:
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
	return holder, peer, func() bool {
		t.Helper()
		select {
		case <-notices:
			return true
		case <-time.After(2 * time.Second):
			return false
		}
	}
}

// TestWorkspaceLeaseWaitEventNamesTheMemberThatHolds pins the payoff: the queued
// member reads the member that is writing, the mode, and the hold's age, under
// the existing notice code.
func TestWorkspaceLeaseWaitEventNamesTheMemberThatHolds(t *testing.T) {
	holder, peer, waited := identifiedLeasePair(t, "member lead of team alpha")
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

	if !strings.Contains(ev.Detail, "held by member lead of team alpha") {
		t.Fatalf("wait detail = %q, want it to name the holding member", ev.Detail)
	}
	if !strings.Contains(ev.Detail, "(exclusive)") {
		t.Fatalf("wait detail = %q, want the holder's mode", ev.Detail)
	}
	if !strings.Contains(ev.Detail, "whole workspace") {
		t.Fatalf("wait detail = %q, want it to keep the scope classification", ev.Detail)
	}
	if !strings.Contains(ev.Text, "team member") {
		t.Fatalf("notice text = %q, want it to stop calling a teammate another session", ev.Text)
	}
}

// TestWorkspaceLeaseWaitEventStaysGenericWithoutIdentity keeps the fallback: an
// unidentified holder leaves both the text and the detail exactly as published
// before L1, so single-session consumers see no change.
func TestWorkspaceLeaseWaitEventStaysGenericWithoutIdentity(t *testing.T) {
	holder, peer, waited := identifiedLeasePair(t, "")
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

	if strings.Contains(ev.Detail, "held by") {
		t.Fatalf("wait detail = %q, want no holder without an identity", ev.Detail)
	}
	if !strings.Contains(ev.Detail, "whole workspace") {
		t.Fatalf("wait detail = %q, want it to keep the scope classification", ev.Detail)
	}
	if !strings.HasPrefix(ev.Text, "Another session is writing") {
		t.Fatalf("notice text = %q, want the original wording", ev.Text)
	}
}

// TestLeaseHoldAgeReadsAsAges pins the age formatting: a fresh record never
// reads as an instantaneous hold, and a missing start time claims nothing.
func TestLeaseHoldAgeReadsAsAges(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name  string
		since time.Time
		want  string
	}{
		{"fresh", now.Add(-200 * time.Millisecond), "1s"},
		{"seconds", now.Add(-12 * time.Second), "12s"},
		{"minutes", now.Add(-90 * time.Second), "1m30s"},
		{"unknown", time.Time{}, ""},
		{"future", now.Add(time.Second), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := leaseHoldAge(tc.since, now); got != tc.want {
				t.Fatalf("leaseHoldAge = %q, want %q", got, tc.want)
			}
		})
	}
	if got := holderWaitDetail(workspacelease.HolderInfo{Mode: workspacelease.HolderModeShared}, now); got != "" {
		t.Fatalf("holderWaitDetail = %q, want empty for an unlabeled holder", got)
	}
}
