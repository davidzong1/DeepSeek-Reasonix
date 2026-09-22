package workspacelease

// Acceptance tests for the L1 fix in TEAM_WRITE_LEASE_OPTIMIZATION_ROUTE.md:
// the lock carries the holder's identity while held, the identity is retired
// with the hold, and a queued writer can name who it queued behind.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// holderLabelsIn lists the distinct labels of the holder records in one lock
// directory, so a test can assert what a queued writer would be able to name.
func holderLabelsIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var labels []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), holderRecordSuffix) {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			t.Fatalf("read holder record %s: %v", entry.Name(), readErr)
		}
		var record holderRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatalf("decode holder record %s: %v", entry.Name(), err)
		}
		if record.Label == "" || seen[record.Label] {
			continue
		}
		seen[record.Label] = true
		labels = append(labels, record.Label)
	}
	sort.Strings(labels)
	return labels
}

// writeLeaseFile writes one file inside the lease root so a path hold has a real
// target to canonicalize.
func writeLeaseFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package component\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// BenchmarkUncontendedPathHoldWithHolderRecord measures what the diagnostic
// records cost against BenchmarkUncontendedPathHold: the same uncontended path
// hold, with an identity set so every acquired lock publishes a record.
func BenchmarkUncontendedPathHoldWithHolderRecord(b *testing.B) {
	root := b.TempDir()
	owner, err := New(root, b.TempDir(), nil)
	if err != nil {
		b.Fatal(err)
	}
	owner.SetIdentity("member lead of team alpha")
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

// TestPathHoldPublishesAndRetiresItsHolderRecord pins the record's lifetime: it
// exists while the lock is held and is gone once the hold ends, so a waiter can
// never read an identity for a lock nobody holds.
func TestPathHoldPublishesAndRetiresItsHolderRecord(t *testing.T) {
	root, locks := t.TempDir(), t.TempDir()
	owner, err := New(root, locks, nil)
	if err != nil {
		t.Fatal(err)
	}
	owner.SetIdentity("member lead of team alpha")
	file := filepath.Join(root, "component.go")
	writeLeaseFile(t, file)

	release, err := owner.HoldWriteForPath(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"member lead of team alpha"}
	if got := holderLabelsIn(t, locks); !slices.Equal(got, want) {
		t.Fatalf("holder records while holding = %v, want %v", got, want)
	}

	release()
	if got := holderLabelsIn(t, locks); len(got) != 0 {
		t.Fatalf("holder records after release = %v, want none", got)
	}
}

// TestUnlabeledHoldPublishesNoHolderRecord keeps the record opt-in: a session
// that never set an identity writes nothing beside the lock.
func TestUnlabeledHoldPublishesNoHolderRecord(t *testing.T) {
	root, locks := t.TempDir(), t.TempDir()
	owner, err := New(root, locks, nil)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "component.go")
	writeLeaseFile(t, file)

	release, err := owner.HoldWriteForPath(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if got := holderLabelsIn(t, locks); len(got) != 0 {
		t.Fatalf("unlabeled holder published records %v, want none", got)
	}
}

// TestWaitingOnNamesTheHolderWhileQueued is the L1 contract: the queued writer
// observes the holder's label, mode, and start time at the moment it reports the
// wait, and stops naming it once the wait is served.
func TestWaitingOnNamesTheHolderWhileQueued(t *testing.T) {
	root, locks := t.TempDir(), t.TempDir()
	holder, err := New(root, locks, nil)
	if err != nil {
		t.Fatal(err)
	}
	holder.SetIdentity("member alice of team alpha")
	holder.BeginRun()
	t.Cleanup(holder.EndRun)

	observed := make(chan HolderInfo, 1)
	var peer *Owner
	peer, err = New(root, locks, func() {
		if info, ok := peer.WaitingOn(); ok {
			select {
			case observed <- info:
			default:
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	peer.BeginRun()
	t.Cleanup(peer.EndRun)

	if err := holder.AcquireWrite(context.Background()); err != nil {
		t.Fatal(err)
	}
	queued := make(chan error, 1)
	go func() { queued <- peer.AcquireWrite(context.Background()) }()

	select {
	case info := <-observed:
		if info.Label != "member alice of team alpha" {
			t.Fatalf("observed holder label = %q, want the holder's identity", info.Label)
		}
		if info.Mode != HolderModeExclusive {
			t.Fatalf("observed holder mode = %q, want %q", info.Mode, HolderModeExclusive)
		}
		if info.Since.IsZero() {
			t.Fatal("observed holder start time is missing")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the queued writer never reported waiting")
	}

	holder.ReleaseWrite()
	if err := <-queued; err != nil {
		t.Fatalf("queued writer after release: %v", err)
	}
	if _, ok := peer.WaitingOn(); ok {
		t.Fatal("a served wait must stop naming a holder")
	}
}

// TestWaitingWholeWorkspaceWriterNamesThePathHolder pins the root domain: a
// workspace-wide writer queued behind a file writer names that file writer, and
// the record it reads is the one the file hold published beside the root lock.
func TestWaitingWholeWorkspaceWriterNamesThePathHolder(t *testing.T) {
	root, locks := t.TempDir(), t.TempDir()
	pathWriter, err := New(root, locks, nil)
	if err != nil {
		t.Fatal(err)
	}
	pathWriter.SetIdentity("member alice of team alpha")
	file := filepath.Join(root, "pkg", "component.go")
	writeLeaseFile(t, file)
	release, err := pathWriter.HoldWriteForPath(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}

	observed := make(chan HolderInfo, 1)
	var builder *Owner
	builder, err = New(root, locks, func() {
		if info, ok := builder.WaitingOn(); ok {
			select {
			case observed <- info:
			default:
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	queued := make(chan error, 1)
	go func() { queued <- builder.AcquireWrite(context.Background()) }()

	select {
	case info := <-observed:
		if info.Label != "member alice of team alpha" {
			t.Fatalf("observed holder label = %q, want the file writer's identity", info.Label)
		}
		if info.Mode != HolderModeShared {
			t.Fatalf("observed holder mode = %q, want %q", info.Mode, HolderModeShared)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the whole-workspace writer never reported waiting")
	}

	release()
	if err := <-queued; err != nil {
		t.Fatalf("whole-workspace writer after release: %v", err)
	}
}

// TestSharedHolderRecordSurvivesUntilTheLastHolderReleases pins the token rule:
// two writers share the ancestor locks, so the record must not be deleted by the
// first releaser while the second still holds.
func TestSharedHolderRecordSurvivesUntilTheLastHolderReleases(t *testing.T) {
	root, locks := t.TempDir(), t.TempDir()
	alice, err := New(root, locks, nil)
	if err != nil {
		t.Fatal(err)
	}
	alice.SetIdentity("alice")
	bob, err := New(root, locks, nil)
	if err != nil {
		t.Fatal(err)
	}
	bob.SetIdentity("bob")
	first, second := filepath.Join(root, "pkg", "a.go"), filepath.Join(root, "pkg", "b.go")
	writeLeaseFile(t, first)
	writeLeaseFile(t, second)

	releaseAlice, err := alice.HoldWriteForPath(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	releaseBob, err := bob.HoldWriteForPath(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if got := holderLabelsIn(t, locks); !slices.Equal(got, []string{"alice", "bob"}) {
		t.Fatalf("holder records while both hold = %v, want both writers", got)
	}

	releaseAlice()
	if got := holderLabelsIn(t, locks); !slices.Equal(got, []string{"bob"}) {
		t.Fatalf("holder records after the first release = %v, want bob still named", got)
	}

	releaseBob()
	if got := holderLabelsIn(t, locks); len(got) != 0 {
		t.Fatalf("holder records after the last release = %v, want none", got)
	}
}
