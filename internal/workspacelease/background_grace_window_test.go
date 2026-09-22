package workspacelease

// L3 (TEAM_WRITE_LEASE_OPTIMIZATION_ROUTE.md): the background-job retention
// window is the dominant blocking cost a teammate pays, while the lease it holds
// open is re-acquirable in well under a millisecond.

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestRetainedWindowMatchesTheConfiguredGrace measures the window itself: after
// a session goes idle under a resident job, the lease stays held for the
// configured grace and no longer.
func TestRetainedWindowMatchesTheConfiguredGrace(t *testing.T) {
	const grace = 120 * time.Millisecond
	owner := newOwnerWithGrace(t, t.TempDir(), t.TempDir(), grace)
	owner.BeginRun()
	if err := owner.AcquireWrite(context.Background()); err != nil {
		t.Fatal(err)
	}
	resident := make(chan struct{})
	defer close(resident)
	owner.RetainUntil(resident)
	idleAt := time.Now()
	owner.EndRun()

	deadline := idleAt.Add(2 * time.Second)
	for owner.State().Acquired && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	held := time.Since(idleAt)
	if owner.State().Acquired {
		t.Fatalf("the retained hold never released (held %s)", held)
	}
	if held < grace/2 {
		t.Fatalf("retained window %s, want at least the configured %s grace", held, grace)
	}
	if held > 5*grace {
		t.Fatalf("retained window %s, want about the configured %s grace", held, grace)
	}
}

// TestBackgroundGraceStaysInTheDecidedBand records the L3 decision: the window
// must stay small, because a released lease costs one re-acquisition
// (BenchmarkUncontendedPathHold) while every idle second costs a teammate that
// second. Below the band the retention no longer absorbs a write burst.
func TestBackgroundGraceStaysInTheDecidedBand(t *testing.T) {
	if backgroundGrace > 5*time.Second {
		t.Fatalf("backgroundGrace = %s, over the decided 2–5s band: an idle teammate waits that long per released write call", backgroundGrace)
	}
	if backgroundGrace < 2*time.Second {
		t.Fatalf("backgroundGrace = %s, under the decided 2–5s band: the retention would stop absorbing write bursts", backgroundGrace)
	}
}

// BenchmarkResidentJobHoldReuse measures the other side of the trade: with the
// grace armed, re-writing the same path reuses the retained hold instead of
// acquiring it again, so keeping the window open costs no lock churn.
func BenchmarkResidentJobHoldReuse(b *testing.B) {
	root := b.TempDir()
	owner, err := New(root, b.TempDir(), nil)
	if err != nil {
		b.Fatal(err)
	}
	owner.graceAfter = backgroundGrace
	resident := make(chan struct{})
	defer close(resident)
	owner.RetainUntil(resident)
	path := filepath.Join(root, "src", "internal", "package", "file.go")
	b.ResetTimer()
	for b.Loop() {
		release, err := owner.HoldWriteForPath(context.Background(), path)
		if err != nil {
			b.Fatal(err)
		}
		release()
	}
}
