package team

// Real-file tests for the discussion store: reopen/replay, corruption, 0600,
// and the deposit gate that makes an ended document a durable outbox.
// Semantics live in discussion_semantics_acceptance_test.go; these pin persistence.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const discNow = "2026-01-01T00:00:00.000000000Z"

func newTestDiscussionStore(t *testing.T) (*DiscussionStore, string) {
	t.Helper()
	dir := t.TempDir()
	fs, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return NewDiscussionStore(fs), dir
}

func discussionPath(t *testing.T, dir, team string) string {
	t.Helper()
	rel, err := DiscussionFile(team)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, rel)
}

// TestDiscussionStoreLifecycleReopenAnd0600 drives a full session through the
// real store, then reopens it from disk: the ended deposit survives a restart
// as pending, flips to delivered, and a later start is allowed. The document
// is written 0600 like every team registry document.
func TestDiscussionStoreLifecycleReopenAnd0600(t *testing.T) {
	ds, dir := newTestDiscussionStore(t)

	participants := []string{"alice", "bob"}
	err := ds.Update("alpha", func(d *DiscussionDoc) error {
		return d.Start("lead", "pick a durable discussion store", participants, 9, "s1", discNow)
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	participants[0] = "eve" // mutate after start: the doc must hold the snapshot
	doc, err := ds.Load("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Status != DiscussionActive || doc.Round != 1 || doc.MaxRounds != DiscussionMaxRoundsCap {
		t.Fatalf("start must be active/round1/max3, got status=%s round=%d max=%d", doc.Status, doc.Round, doc.MaxRounds)
	}
	if doc.Participants[0] != "alice" {
		t.Fatalf("participants must be a frozen snapshot, got %v", doc.Participants)
	}
	if err := ds.Update("alpha", func(d *DiscussionDoc) error { return d.Submit("alice", 1, "round one: file store", discNow) }); err != nil {
		t.Fatal(err)
	}
	if err := ds.Update("alpha", func(d *DiscussionDoc) error { return d.Submit("alice", 1, "round one revision", discNow) }); err != nil {
		t.Fatal(err)
	}
	// bob must conclude round one before the leader may advance it (matrix gate).
	if err := ds.Update("alpha", func(d *DiscussionDoc) error { return d.Submit("bob", 1, "round one: bob", discNow) }); err != nil {
		t.Fatal(err)
	}
	if err := ds.Update("alpha", func(d *DiscussionDoc) error { return d.Advance("lead", discNow) }); err != nil {
		t.Fatal(err)
	}
	if err := ds.Update("alpha", func(d *DiscussionDoc) error { return d.Submit("bob", 2, "round two: files win", discNow) }); err != nil {
		t.Fatal(err)
	}

	// End with a leader consensus and deposit enabled (the host only enables it
	// when a KB is configured).
	err = ds.Update("alpha", func(d *DiscussionDoc) error {
		return d.End("lead", EndReasonConsensus, "decision: adopt the file store", "", true, discNow)
	})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	doc, _ = ds.Load("alpha")
	if !doc.DepositPending() {
		t.Fatalf("ended consensus must leave a pending deposit, got %+v", doc.Deposit)
	}

	// 0600 on the discussion document, same as every .reasonix/team write.
	if info, err := os.Stat(discussionPath(t, dir, "alpha")); err != nil {
		t.Fatal(err)
	} else if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("discussion file must be 0600, got %o", perm)
	}

	// Reopen from disk (restart) — the pending deposit and full state survive.
	fs2, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ds2 := NewDiscussionStore(fs2)
	doc, err = ds2.Load("alpha")
	if err != nil {
		t.Fatalf("reopen load: %v", err)
	}
	if doc.Status != DiscussionEnded || !doc.DepositPending() {
		t.Fatalf("reopen must see the ended pending deposit, got %+v", doc)
	}
	if err := ds2.Update("alpha", func(d *DiscussionDoc) error { return d.MarkDeposited(discNow) }); err != nil {
		t.Fatalf("mark deposited: %v", err)
	}

	// Delivered: a fresh start for the next session is allowed.
	if err := ds2.Update("alpha", func(d *DiscussionDoc) error {
		return d.Start("lead", "next topic", []string{"alice"}, 2, "s2", discNow)
	}); err != nil {
		t.Fatalf("start after delivered deposit must succeed, got %v", err)
	}
}

// TestDiscussionDepositPendingBlocksNextStart pins the durable-outbox gate: a
// crash between end and delivery must not be resolved by overwriting the
// consensus with a new session.
func TestDiscussionDepositPendingBlocksNextStart(t *testing.T) {
	ds, _ := newTestDiscussionStore(t)
	if err := ds.Update("alpha", func(d *DiscussionDoc) error {
		return d.Start("lead", "topic", []string{"alice"}, 1, "s1", discNow)
	}); err != nil {
		t.Fatal(err)
	}
	if err := ds.Update("alpha", func(d *DiscussionDoc) error {
		return d.End("lead", EndReasonConsensus, "decision: use files", "", true, discNow)
	}); err != nil {
		t.Fatal(err)
	}
	err := ds.Update("alpha", func(d *DiscussionDoc) error {
		return d.Start("lead", "overwrite?", []string{"alice"}, 1, "s2", discNow)
	})
	if err == nil || !strings.Contains(err.Error(), ErrDepositPending.Error()) {
		t.Fatalf("start while a deposit is pending must be refused with ErrDepositPending, got %v", err)
	}
	if err := ds.Update("alpha", func(d *DiscussionDoc) error { return d.MarkDeposited(discNow) }); err != nil {
		t.Fatal(err)
	}
	if err := ds.Update("alpha", func(d *DiscussionDoc) error {
		return d.Start("lead", "next", []string{"alice"}, 1, "s2", discNow)
	}); err != nil {
		t.Fatalf("start after delivered must succeed, got %v", err)
	}
}

// TestDiscussionStoreCorruptFailsClosed pins fail-closed recovery: a corrupt
// or schema-mismatched discussion file is an error, never a silent reset that
// would let a new session overwrite the lost one.
func TestDiscussionStoreCorruptFailsClosed(t *testing.T) {
	ds, dir := newTestDiscussionStore(t)
	path := discussionPath(t, dir, "alpha")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.Load("alpha"); err == nil {
		t.Fatal("corrupt file must fail Load, not read as zero")
	}
	err := ds.Update("alpha", func(d *DiscussionDoc) error {
		return d.Start("lead", "must not clobber", []string{"alice"}, 1, "s1", discNow)
	})
	if err == nil {
		t.Fatal("corrupt file must fail Update, not silently overwrite")
	}
	// Schema mismatch is equally refused.
	if err := os.WriteFile(path, []byte(`{"schema_version": 999, "status": "idle"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.Load("alpha"); err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Fatalf("schema mismatch must fail closed, got %v", err)
	}
}

// TestDiscussionStoreConcurrentSubmitsDistinctMembers is a -race probe: N
// participants submit to the same round concurrently through the store and each
// member's entry lands exactly once.
func TestDiscussionStoreConcurrentSubmitsDistinctMembers(t *testing.T) {
	ds, _ := newTestDiscussionStore(t)
	parts := []string{"m0", "m1", "m2", "m3", "m4", "m5", "m6", "m7"}
	if err := ds.Update("alpha", func(d *DiscussionDoc) error {
		return d.Start("lead", "parallel", parts, 1, "s1", discNow)
	}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, m := range parts {
		wg.Add(1)
		go func(m string) {
			defer wg.Done()
			_ = ds.Update("alpha", func(d *DiscussionDoc) error { return d.Submit(m, 1, m+"-conclusion", discNow) })
		}(m)
	}
	wg.Wait()
	doc, err := ds.Load("alpha")
	if err != nil {
		t.Fatal(err)
	}
	round := doc.Conclusions[1]
	if len(round) != len(parts) {
		t.Fatalf("every participant must land once, got %d/%d entries %v", len(round), len(parts), round)
	}
	for _, m := range parts {
		if _, ok := round[m]; !ok {
			t.Errorf("missing conclusion from %q", m)
		}
	}
}
