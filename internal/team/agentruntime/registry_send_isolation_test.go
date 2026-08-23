package agentruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/team"
)

// TestRegistrySendIsolatesMemberContextDirs pins the P4 identity contract
// (§11.2/§11.3): Send lands exactly in the named member's context directory.
// A shared AgentUserRef, a sibling member, or the same member id under a
// different team never see each other's messages — on disk, not just in
// snapshots.
func TestRegistrySendIsolatesMemberContextDirs(t *testing.T) {
	root := t.TempDir()
	store, err := team.NewTeamSessionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(store)
	keyA := InstanceKey{Team: "t", MemberID: "coder-1"}
	keyB := InstanceKey{Team: "t", MemberID: "coder-2"}
	keyC := InstanceKey{Team: "other", MemberID: "coder-1"}
	for _, k := range []InstanceKey{keyA, keyB, keyC} {
		if _, err := r.Start(t.Context(), sharedSpec(k, "")); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Send(keyA, "hello A"); err != nil {
		t.Fatal(err)
	}
	if err := r.Send(keyB, "hello B"); err != nil {
		t.Fatal(err)
	}
	if err := r.Send(keyC, "hello C"); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		key  InstanceKey
		want []string
	}{
		{keyA, []string{"hello A"}},
		{keyB, []string{"hello B"}},
		{keyC, []string{"hello C"}},
	} {
		msgs, err := store.Messages(tc.key.Team, tc.key.MemberID)
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != 1 || msgs[0].Text != tc.want[0] {
			t.Fatalf("(%s,%s) history = %+v; want exactly %v", tc.key.Team, tc.key.MemberID, msgs, tc.want)
		}
	}

	// A second send appends to the target only; nothing leaks.
	if err := r.Send(keyA, "hello A2"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		key  InstanceKey
		want int
	}{
		{keyA, 2},
		{keyB, 1},
		{keyC, 1},
	} {
		msgs, err := store.Messages(tc.key.Team, tc.key.MemberID)
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != tc.want {
			t.Fatalf("(%s,%s) message count = %d, want %d", tc.key.Team, tc.key.MemberID, len(msgs), tc.want)
		}
	}

	// The on-disk layout matches §4.1: one messages.jsonl per member dir,
	// under the project's team root rather than the project root itself.
	teamRoot, err := team.TeamRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []InstanceKey{keyA, keyB, keyC} {
		dir, err := store.MemberDir(k.Team, k.MemberID)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(teamRoot, dir, team.MemberMessagesFile))
		if err != nil {
			t.Fatalf("(%s,%s) messages file unreadable: %v", k.Team, k.MemberID, err)
		}
		if strings.Count(string(data), "\n") < 1 {
			t.Fatalf("(%s,%s) messages file is empty", k.Team, k.MemberID)
		}
	}
}

// TestRegistrySendDoesNotAdvanceCursor pins §11.3: Send records the user
// message but must not move the recovery cursor — only MarkConsumed does, so
// an unconsumed history is never silently re-marked as read.
func TestRegistrySendDoesNotAdvanceCursor(t *testing.T) {
	r := newTestRegistry(t)
	key := InstanceKey{Team: "t", MemberID: "m"}
	if _, err := r.Start(t.Context(), sharedSpec(key, "")); err != nil {
		t.Fatal(err)
	}
	if err := r.Send(key, "one"); err != nil {
		t.Fatal(err)
	}
	snap, err := r.Observe(key)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Cursor.Cursor != 0 || snap.Cursor.ResumeCount != 0 {
		t.Fatalf("Send must not advance the cursor, got %+v", snap.Cursor)
	}
}
