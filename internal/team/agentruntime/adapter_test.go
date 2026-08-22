package agentruntime

import (
	"errors"
	"reflect"
	"testing"

	"reasonix/internal/team"
)

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	store, err := team.NewTeamSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewRegistry(store)
}

func sharedSpec(key InstanceKey, role string) Spec {
	return Spec{
		Key: key,
		Config: ConfigSnapshot{
			AgentUserRef: "au-1", // the same shared reference in every spec
			Provider:     "deepseek",
			Model:        "deepseek-v4-flash",
		},
		Role:       role,
		BasePrompt: "base",
	}
}

func TestRegistryStartIsIdempotentAndIsolatesSharedConfig(t *testing.T) {
	r := newTestRegistry(t)
	keyA := InstanceKey{Team: "t", MemberID: "coder-1"}
	keyB := InstanceKey{Team: "t", MemberID: "coder-2"}

	// Two members share one AgentUserRef: they get distinct instances.
	instA, err := r.Start(t.Context(), sharedSpec(keyA, "后端"))
	if err != nil {
		t.Fatal(err)
	}
	instB, err := r.Start(t.Context(), sharedSpec(keyB, "前端"))
	if err != nil {
		t.Fatal(err)
	}
	if instA == instB {
		t.Fatal("shared AgentUserRef produced a shared instance")
	}
	if instA.Key() != keyA || instB.Key() != keyB {
		t.Fatalf("instance keys = %v, %v; want %v, %v", instA.Key(), instB.Key(), keyA, keyB)
	}

	// Starting the same key again reuses the instance (idempotent).
	again, err := r.Start(t.Context(), sharedSpec(keyA, "后端"))
	if err != nil {
		t.Fatal(err)
	}
	if again != instA {
		t.Fatal("Start must reuse the existing instance for the same key")
	}

	// The two instances keep independent mutable state: consuming A's
	// history must not move B's cursor.
	if err := r.Send(keyA, "hello A"); err != nil {
		t.Fatal(err)
	}
	if err := r.Send(keyA, "second A"); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkConsumed(keyA); err != nil {
		t.Fatal(err)
	}
	snapA, err := r.Observe(keyA)
	if err != nil {
		t.Fatal(err)
	}
	snapB, err := r.Observe(keyB)
	if err != nil {
		t.Fatal(err)
	}
	if snapA.Cursor.Cursor != 2 || snapA.Cursor.ResumeCount != 1 {
		t.Fatalf("A cursor = %+v, want consumed 2 / resume 1", snapA.Cursor)
	}
	if snapB.Cursor.Cursor != 0 || snapB.Cursor.ResumeCount != 0 {
		t.Fatalf("B cursor moved by A's consumption: %+v", snapB.Cursor)
	}
	if len(snapB.Messages) != 0 {
		t.Fatalf("B saw A's history: %+v", snapB.Messages)
	}
	if snapA.Role != "后端" || snapB.Role != "前端" {
		t.Fatalf("roles = %q, %q; want per-member roles", snapA.Role, snapB.Role)
	}
}

func TestRegistryRestoresCursorOnFreshStart(t *testing.T) {
	store, err := team.NewTeamSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// History and a consumed cursor exist on disk before any instance starts.
	if err := store.AppendMessage("t", "m", team.SessionMessage{Kind: "user", From: "cli", Text: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendMessage("t", "m", team.SessionMessage{Kind: "agent", Text: "reply"}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteCursor("t", "m", team.SessionCursor{Cursor: 2, ResumeCount: 1, ContextRef: "ctx/rev-1"}); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry(store)
	key := InstanceKey{Team: "t", MemberID: "m"}
	if _, err := r.Start(t.Context(), sharedSpec(key, "")); err != nil {
		t.Fatal(err)
	}
	snap, err := r.Observe(key)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Cursor.Cursor != 2 || snap.Cursor.ResumeCount != 1 || snap.Cursor.ContextRef != "ctx/rev-1" {
		t.Fatalf("cursor not restored: %+v", snap.Cursor)
	}
	if len(snap.Messages) != 2 {
		t.Fatalf("history not restored: %d messages", len(snap.Messages))
	}

	// A second registry reading the same store sees the same state — state
	// lives in the member directory, not in any one registry.
	r2 := NewRegistry(store)
	if _, err := r2.Start(t.Context(), sharedSpec(key, "")); err != nil {
		t.Fatal(err)
	}
	snap2, err := r2.Observe(key)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snap2.Cursor, snap.Cursor) {
		t.Fatalf("second registry cursor = %+v, want %+v", snap2.Cursor, snap.Cursor)
	}
}

func TestRegistryStopPersistsStateAndIsNoopWhenStopped(t *testing.T) {
	r := newTestRegistry(t)
	key := InstanceKey{Team: "t", MemberID: "m"}
	if _, err := r.Start(t.Context(), sharedSpec(key, "")); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(key); err != nil {
		t.Fatal(err)
	}
	if st, err := r.Status(key); err != nil || st != RuntimeStateStopped {
		t.Fatalf("status = %q, %v; want stopped", st, err)
	}
	if err := r.Stop(key); err != nil {
		t.Fatalf("stopping a stopped instance must be a no-op: %v", err)
	}
	st, err := r.store.ReadState(key.Team, key.MemberID)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != string(RuntimeStateStopped) {
		t.Fatalf("persisted state = %q, want stopped", st.State)
	}
}

func TestRegistrySwitchMovesWindowAndObserveIsReadOnly(t *testing.T) {
	r := newTestRegistry(t)
	keyA := InstanceKey{Team: "t", MemberID: "a"}
	keyB := InstanceKey{Team: "t", MemberID: "b"}
	if _, err := r.Start(t.Context(), sharedSpec(keyA, "r1")); err != nil {
		t.Fatal(err)
	}
	// The first started instance becomes the window default.
	if cur, ok := r.Current(); !ok || cur != keyA {
		t.Fatalf("current = %v, %v; want keyA as default", cur, ok)
	}
	if _, err := r.Start(t.Context(), sharedSpec(keyB, "r2")); err != nil {
		t.Fatal(err)
	}
	snap, err := r.Switch(keyB)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Key != keyB {
		t.Fatalf("switch snapshot key = %v, want keyB", snap.Key)
	}
	if cur, _ := r.Current(); cur != keyB {
		t.Fatalf("current = %v, want keyB after switch", cur)
	}
	// Observe returns a copy: mutating the snapshot never reaches the
	// instance.
	snap.Key.MemberID = "mutated"
	if cur, _ := r.Current(); cur != keyB {
		t.Fatalf("mutating a snapshot leaked into the registry: %v", cur)
	}
}

func TestRegistrySendWorksOnStoppedInstance(t *testing.T) {
	r := newTestRegistry(t)
	key := InstanceKey{Team: "t", MemberID: "m"}
	if _, err := r.Start(t.Context(), sharedSpec(key, "")); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(key); err != nil {
		t.Fatal(err)
	}
	// A message sent to a stopped instance becomes history for the next
	// assembly — never dropped.
	if err := r.Send(key, "offline note"); err != nil {
		t.Fatal(err)
	}
	snap, err := r.Observe(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Messages) != 1 || snap.Messages[0].Text != "offline note" {
		t.Fatalf("stopped-instance message lost: %+v", snap.Messages)
	}
}

func TestRegistryUnknownKeyRefused(t *testing.T) {
	r := newTestRegistry(t)
	key := InstanceKey{Team: "t", MemberID: "nobody"}
	if _, err := r.Status(key); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("Status err = %v, want ErrInstanceNotFound", err)
	}
	if _, err := r.Switch(key); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("Switch err = %v, want ErrInstanceNotFound", err)
	}
	if err := r.Send(key, "x"); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("Send err = %v, want ErrInstanceNotFound", err)
	}
	if err := r.Stop(key); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("Stop err = %v, want ErrInstanceNotFound", err)
	}
	if err := r.MarkConsumed(key); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("MarkConsumed err = %v, want ErrInstanceNotFound", err)
	}
}

func TestRegistryStartRefusesEmptyKey(t *testing.T) {
	r := newTestRegistry(t)
	if _, err := r.Start(t.Context(), Spec{Key: InstanceKey{Team: "", MemberID: "m"}}); err == nil {
		t.Fatal("empty team accepted")
	}
	if _, err := r.Start(t.Context(), Spec{Key: InstanceKey{Team: "t", MemberID: ""}}); err == nil {
		t.Fatal("empty member accepted")
	}
}

func TestRegistryInstancesSortedAndClosePersistsAll(t *testing.T) {
	r := newTestRegistry(t)
	if got := r.Instances(); len(got) != 0 {
		t.Fatalf("fresh registry lists %v", got)
	}
	keys := []InstanceKey{
		{Team: "t", MemberID: "b"},
		{Team: "a", MemberID: "z"},
		{Team: "t", MemberID: "a"},
	}
	for _, k := range keys {
		if _, err := r.Start(t.Context(), sharedSpec(k, "")); err != nil {
			t.Fatal(err)
		}
	}
	got := r.Instances()
	if !reflect.DeepEqual(got, []InstanceKey{
		{Team: "a", MemberID: "z"},
		{Team: "t", MemberID: "a"},
		{Team: "t", MemberID: "b"},
	}) {
		t.Fatalf("Instances = %v, want team-then-member order", got)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if st, _ := r.Status(k); st != RuntimeStateStopped {
			t.Fatalf("instance %v = %q after Close, want stopped", k, st)
		}
	}
}
