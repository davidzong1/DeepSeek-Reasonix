package agentruntime

import (
	"errors"
	"testing"
	"time"

	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// TestRegistrySubscribeRequiresAssembledRuntime pins §11.3: a subscription is
// a runtime-only operation. The pure-state registry mode (no ProviderFactory)
// must refuse with ErrNotAssembled instead of handing out a dead stream.
func TestRegistrySubscribeRequiresAssembledRuntime(t *testing.T) {
	r := newTestRegistry(t) // no factory wired
	key := InstanceKey{Team: "t", MemberID: "m"}
	if _, err := r.Start(t.Context(), sharedSpec(key, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Subscribe(key); !errors.Is(err, ErrNotAssembled) {
		t.Fatalf("Subscribe without a runtime = %v, want ErrNotAssembled", err)
	}
}

// TestRegistrySubscribeStreamsIdentityAndSequence pins the registry-side
// subscription contract (§11.3): a wired factory streams started/delta/
// message/done; every event carries the subscribing instance's (team,
// memberID) and a monotonic per-instance sequence; the final message survives;
// Cancel closes the stream.
func TestRegistrySubscribeStreamsIdentityAndSequence(t *testing.T) {
	store, err := team.NewTeamSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prov := &fakeProvider{chunks: []provider.Chunk{{Type: provider.ChunkText, Text: "hi"}}}
	r := NewRegistry(store, func(Spec) (provider.Provider, error) { return prov, nil })
	// Close awaits the loop's final persistence; EventDone alone does not, so a
	// test that returns on done would race t.TempDir cleanup against the write.
	defer r.Close()
	key := InstanceKey{Team: "t", MemberID: "m"}
	if _, err := r.Start(t.Context(), sharedSpec(key, "")); err != nil {
		t.Fatal(err)
	}
	sub, err := r.Subscribe(key)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()
	if err := r.Send(key, "prompt"); err != nil {
		t.Fatal(err)
	}

	var got []RuntimeEvent
	deadline := time.After(5 * time.Second)
	for len(got) < 4 { // started, delta, message, done
		select {
		case ev, ok := <-sub.C:
			if !ok {
				t.Fatalf("stream closed early, got %d events", len(got))
			}
			got = append(got, ev)
		case <-deadline:
			t.Fatalf("timed out with %d events: %+v", len(got), got)
		}
	}
	for i, ev := range got {
		if ev.Team != key.Team || ev.MemberID != key.MemberID {
			t.Fatalf("event %d identity = (%s,%s), want (%s,%s)", i, ev.Team, ev.MemberID, key.Team, key.MemberID)
		}
		if i > 0 && ev.Sequence <= got[i-1].Sequence {
			t.Fatalf("sequence not monotonic: %+v", got)
		}
	}
	if got[0].Kind != EventStarted || got[len(got)-1].Kind != EventDone {
		t.Fatalf("event kinds = %+v, want started…done", got)
	}
	if got[2].Kind != EventMessage || got[2].Text != "hi" {
		t.Fatalf("message event = %+v, want text %q", got[2], "hi")
	}

	sub.Cancel()
	select {
	case _, ok := <-sub.C:
		if ok {
			t.Fatal("channel still open after Cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel did not close the channel")
	}
}

// TestRegistrySendWritesUserBeforeLoop pins §11.3-3: Send records the user
// message in the member's context before submitting to the agent loop, so a
// hung or failing loop never loses the submitted prompt.
func TestRegistrySendWritesUserBeforeLoop(t *testing.T) {
	store, err := team.NewTeamSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prov := &fakeProvider{hang: true} // loop never completes
	r := NewRegistry(store, func(Spec) (provider.Provider, error) { return prov, nil })
	key := InstanceKey{Team: "t", MemberID: "m"}
	if _, err := r.Start(t.Context(), sharedSpec(key, "")); err != nil {
		t.Fatal(err)
	}
	if err := r.Send(key, "prompt"); err != nil {
		t.Fatal(err)
	}
	msgs, err := store.Messages(key.Team, key.MemberID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Kind != "user" || msgs[0].Text != "prompt" {
		t.Fatalf("user message not persisted before the loop: %+v", msgs)
	}
	if err := r.Stop(key); err != nil {
		t.Fatal(err)
	}
}

// TestRegistrySequenceContinuesAcrossRestart pins the §11.3 sequence
// continuation: the member's cursor persists the event counter, so a fresh
// registry assembled from the same store never renumbers events — the
// restarted member's stream continues from the persisted sequence.
func TestRegistrySequenceContinuesAcrossRestart(t *testing.T) {
	store, err := team.NewTeamSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prov := &fakeProvider{chunks: []provider.Chunk{{Type: provider.ChunkText, Text: "hi"}}}
	factory := func(Spec) (provider.Provider, error) { return prov, nil }
	key := InstanceKey{Team: "t", MemberID: "m"}

	r1 := NewRegistry(store, factory)
	if _, err := r1.Start(t.Context(), sharedSpec(key, "")); err != nil {
		t.Fatal(err)
	}
	sub1, err := r1.Subscribe(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := r1.Send(key, "first"); err != nil {
		t.Fatal(err)
	}
	evs1 := drainEvents(t, sub1, EventStarted, EventDelta, EventMessage, EventDone)
	if evs1[0].Sequence != 1 || evs1[3].Sequence != 4 {
		t.Fatalf("first run sequences = %d..%d, want 1..4", evs1[0].Sequence, evs1[3].Sequence)
	}
	sub1.Cancel()
	if err := r1.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := store.ReadCursor(key.Team, key.MemberID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Sequence != 4 {
		t.Fatalf("persisted sequence = %d, want 4", c.Sequence)
	}

	// A fresh registry over the same store continues the counter.
	r2 := NewRegistry(store, factory)
	defer r2.Close() // as above: await the loop before the temp dir goes away
	if _, err := r2.Start(t.Context(), sharedSpec(key, "")); err != nil {
		t.Fatal(err)
	}
	sub2, err := r2.Subscribe(key)
	if err != nil {
		t.Fatal(err)
	}
	defer sub2.Cancel()
	if err := r2.Send(key, "second"); err != nil {
		t.Fatal(err)
	}
	evs2 := drainEvents(t, sub2, EventStarted, EventDelta, EventMessage, EventDone)
	if evs2[0].Sequence != 5 || evs2[3].Sequence != 8 {
		t.Fatalf("restarted sequences = %d..%d, want 5..8 (no renumbering)", evs2[0].Sequence, evs2[3].Sequence)
	}
}
