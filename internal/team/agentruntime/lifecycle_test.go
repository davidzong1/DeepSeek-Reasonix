package agentruntime

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// registryWithProvider wires a factory that always returns prov, so registry
// lifecycle tests exercise the assembled-runtime path.
func registryWithProvider(t *testing.T, prov provider.Provider) *Registry {
	t.Helper()
	store, err := team.NewTeamSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewRegistry(store, func(Spec) (provider.Provider, error) { return prov, nil })
}

// waitForEvent drains the subscription until an event of the wanted kind
// arrives or the timeout fires.
func waitForEvent(t *testing.T, sub Subscription, want RuntimeEventKind) RuntimeEvent {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-sub.C:
			if !ok {
				t.Fatalf("subscription closed before %s", want)
			}
			if ev.Kind == want {
				return ev
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s", want)
		}
	}
}

// TestRegistryRestartRecoversRunningState pins the r restart semantic
// (§11.6): Stop persists stopped, and the next Start on the same instance
// returns it to running and persists that — recovery never finds a stopped
// member whose window is live.
func TestRegistryRestartRecoversRunningState(t *testing.T) {
	r := newTestRegistry(t)
	key := testKey()
	if _, err := r.Start(t.Context(), sharedSpec(key, "")); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(key); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.Status(key); st != RuntimeStateStopped {
		t.Fatalf("after Stop status = %s, want stopped", st)
	}
	if _, err := r.Start(t.Context(), sharedSpec(key, "")); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.Status(key); st != RuntimeStateRunning {
		t.Fatalf("after restart status = %s, want running", st)
	}
	if st, err := r.store.ReadState(key.Team, key.MemberID); err != nil || st.State != string(RuntimeStateRunning) {
		t.Fatalf("restart must persist running state, got %+v, %v", st, err)
	}
}

// TestRegistryStopTeamStopsOnlyTargetTeam pins the team-scoped stop gate
// (§11.6): one team's instances stop, sibling teams and unknown teams stay
// untouched.
func TestRegistryStopTeamStopsOnlyTargetTeam(t *testing.T) {
	r := newTestRegistry(t)
	keys := []InstanceKey{
		{Team: "t1", MemberID: "a"},
		{Team: "t1", MemberID: "b"},
		{Team: "t2", MemberID: "c"},
	}
	for _, k := range keys {
		if _, err := r.Start(t.Context(), sharedSpec(k, "")); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.StopTeam("t1"); err != nil {
		t.Fatal(err)
	}
	for _, k := range []InstanceKey{keys[0], keys[1]} {
		if st, _ := r.Status(k); st != RuntimeStateStopped {
			t.Fatalf("(%s,%s) after StopTeam status = %s, want stopped", k.Team, k.MemberID, st)
		}
	}
	if st, _ := r.Status(keys[2]); st != RuntimeStateRunning {
		t.Fatalf("sibling team member must stay running, got %s", st)
	}
	if err := r.StopTeam("nope"); err != nil {
		t.Fatalf("unknown team StopTeam must be a no-op, got %v", err)
	}
}

// TestRegistryStopTeamCancelsRuntimeLoop pins the pre-clear gate with an
// assembled runtime: a hung completion is cancelled, the stopped event lands,
// and the persisted state flips to stopped — nothing keeps writing context
// while the team's contexts are being cleared.
func TestRegistryStopTeamCancelsRuntimeLoop(t *testing.T) {
	r := registryWithProvider(t, &fakeProvider{hang: true})
	key := testKey()
	if _, err := r.Start(t.Context(), sharedSpec(key, "")); err != nil {
		t.Fatal(err)
	}
	sub, err := r.Subscribe(key)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()
	if err := r.Send(key, "hello"); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, sub, EventStarted)
	if err := r.StopTeam("t"); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, sub, EventStopped)
	if st, _ := r.Status(key); st != RuntimeStateStopped {
		t.Fatalf("after StopTeam status = %s, want stopped", st)
	}
	if st, err := r.store.ReadState(key.Team, key.MemberID); err != nil || st.State != string(RuntimeStateStopped) {
		t.Fatalf("StopTeam must persist stopped state, got %+v, %v", st, err)
	}
}

// TestRegistryStopThenSendReusesRuntime pins the restart seam: after Stop the
// same MemberRuntime is reused on the next Send (plugin-engineer contract —
// Stop never destroys the assembled runtime), and the event sequence keeps
// climbing across the restart instead of renumbering.
func TestRegistryStopThenSendReusesRuntime(t *testing.T) {
	prov := &fakeProvider{chunks: []provider.Chunk{{Type: provider.ChunkText, Text: "hi"}}}
	r := registryWithProvider(t, prov)
	key := testKey()
	if _, err := r.Start(t.Context(), sharedSpec(key, "")); err != nil {
		t.Fatal(err)
	}
	sub, err := r.Subscribe(key)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()
	if err := r.Send(key, "one"); err != nil {
		t.Fatal(err)
	}
	first := waitForEvent(t, sub, EventDone)
	if err := r.Stop(key); err != nil {
		t.Fatal(err)
	}
	if err := r.Send(key, "two"); err != nil {
		t.Fatalf("Send after Stop must reuse the runtime, got %v", err)
	}
	second := waitForEvent(t, sub, EventDone)
	if second.Sequence <= first.Sequence {
		t.Fatalf("event sequence must keep climbing across restart: first=%d second=%d", first.Sequence, second.Sequence)
	}
	msgs, err := r.store.Messages(key.Team, key.MemberID)
	if err != nil || len(msgs) != 4 {
		t.Fatalf("history = %d messages, %v; want 4 (2 user + 2 assistant)", len(msgs), err)
	}
}

// TestRegistryConcurrentSendSerialPerMember pins same-member serial (§11.3):
// a Send while the member's completion is in flight is refused with ErrBusy,
// the persisted user message stays for the next run, and after Stop the
// member accepts sends again.
func TestRegistryConcurrentSendSerialPerMember(t *testing.T) {
	r := registryWithProvider(t, &fakeProvider{hang: true})
	key := testKey()
	if _, err := r.Start(t.Context(), sharedSpec(key, "")); err != nil {
		t.Fatal(err)
	}
	sub, err := r.Subscribe(key)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()
	if err := r.Send(key, "one"); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, sub, EventStarted)

	var wg sync.WaitGroup
	wg.Add(1)
	var busyErr error
	go func() {
		defer wg.Done()
		busyErr = r.Send(key, "two")
	}()
	wg.Wait()
	if !errors.Is(busyErr, ErrBusy) {
		t.Fatalf("concurrent Send = %v, want ErrBusy", busyErr)
	}
	// The refused Send still persisted its message (route §11.3: a failed
	// submit keeps the user message as retryable history) but the loop never
	// consumed it — the cursor stays at zero.
	if msgs, err := r.store.Messages(key.Team, key.MemberID); err != nil || len(msgs) != 2 {
		t.Fatalf("refused Send must persist its message, got %d msgs, %v", len(msgs), err)
	}
	if c, err := r.store.ReadCursor(key.Team, key.MemberID); err != nil || c.Cursor != 0 {
		t.Fatalf("in-flight loop must not consume the refused message, cursor=%d, %v", c.Cursor, err)
	}
	if err := r.Stop(key); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, sub, EventStopped)
	if err := r.Send(key, "three"); err != nil {
		t.Fatalf("Send after Stop must be accepted again, got %v", err)
	}
}

// TestRegistryStopTeamThenRestartRecovers pins the combined lifecycle: the
// k-clear path stops the team, and a later session entry on the same process
// starts the members again through the plain Start path.
func TestRegistryStopTeamThenRestartRecovers(t *testing.T) {
	r := newTestRegistry(t)
	key := testKey()
	if _, err := r.Start(t.Context(), sharedSpec(key, "")); err != nil {
		t.Fatal(err)
	}
	if err := r.StopTeam("t"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(t.Context(), sharedSpec(key, "")); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.Status(key); st != RuntimeStateRunning {
		t.Fatalf("restart after StopTeam status = %s, want running", st)
	}
}

// TestRegistryStopIdempotentAcrossTeamAndSingle pins no-op semantics: Stop
// after StopTeam and StopTeam after Stop are both no-ops, so the k-clear path
// cannot double-cancel.
func TestRegistryStopIdempotentAcrossTeamAndSingle(t *testing.T) {
	r := newTestRegistry(t)
	key := testKey()
	if _, err := r.Start(t.Context(), sharedSpec(key, "")); err != nil {
		t.Fatal(err)
	}
	if err := r.StopTeam("t"); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(key); err != nil {
		t.Fatalf("Stop after StopTeam must be a no-op, got %v", err)
	}
	if err := r.StopTeam("t"); err != nil {
		t.Fatalf("StopTeam after Stop must be a no-op, got %v", err)
	}
}

// TestRegistryEventTextNeverCarriesKeyMaterial pins K3 at the registry
// boundary: error events surface provider-facing text only, never raw auth
// bodies (the loop's userFacingError seam).
func TestRegistryEventTextNeverCarriesKeyMaterial(t *testing.T) {
	r := registryWithProvider(t, &fakeProvider{err: &provider.AuthError{Provider: "deepseek"}})
	key := testKey()
	if _, err := r.Start(t.Context(), sharedSpec(key, "")); err != nil {
		t.Fatal(err)
	}
	sub, err := r.Subscribe(key)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()
	if err := r.Send(key, "hello"); err != nil {
		t.Fatal(err)
	}
	ev := waitForEvent(t, sub, EventError)
	if strings.Contains(ev.Text, "sk-") || strings.Contains(ev.Text, "api_key") {
		t.Fatalf("error event must not carry key material, got %q", ev.Text)
	}
}
