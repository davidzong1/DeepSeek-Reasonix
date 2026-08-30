package cli

// P0: a member backend's persisted session must carry its own write authority
// (bindMemberSessionAuthority); without it a session refuses the first task
// with admission 6.

import (
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
)

// TestMemberAuthorityAdmitsFirstTurn pins the fix at the primitive the builder
// calls: after bindMemberSessionAuthority, a persisted member session admits
// the first submitted task instead of refusing it as an admission-6.
func TestMemberAuthorityAdmitsFirstTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coder-session.jsonl")
	rec := newRecorderTurnRunner()
	ctrl := memberAuthorityController(t, path, rec)
	wl, err := bindMemberSessionAuthority(ctrl, path, true)
	if err != nil {
		t.Fatalf("bindMemberSessionAuthority: %v", err)
	}
	t.Cleanup(wl.Close)

	if err := ctrl.SubmitUserTurnOrError("build the widget", "build the widget"); err != nil {
		t.Fatalf("first submit after authority bind = %v, want admission admitted", err)
	}
	waitForTurns(t, rec, 1)
	waitMemberTurnDone(t, ctrl) // the post-commit save completes with the turn
	if ctrl.WriteAuthorityGeneration() == 0 {
		t.Error("the member controller must hold a bound write-authority generation")
	}
	if !agent.SessionLeaseHeldByCurrentRuntime(path) {
		t.Error("the member session lease must be held while the backend is live")
	}
}

// TestMemberAuthorityWithoutBindingRefusesFirstTurn pins the pre-fix failure
// that bindMemberSessionAuthority fixes, at the same primitive level: a
// persisted member session that requires write authority but has none refuses
// the first submitted task with the authority refusal.
func TestMemberAuthorityWithoutBindingRefusesFirstTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coder-session.jsonl")
	sess := agent.NewSession("sys")
	sess.RequireWriteAuthority()
	exec := agent.New(nil, nil, sess, agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{
		Executor: exec, SystemPrompt: "sys", SessionPath: path,
		SessionDir: filepath.Dir(path), Sink: event.Discard,
		Runner: newRecorderTurnRunner(),
	})
	defer ctrl.Close()
	if err := ctrl.SubmitUserTurnOrError("build it", "build it"); err == nil {
		t.Fatal("a persisted member session without a bound authority must refuse the first submit")
	}
}

// TestMemberAuthorityReleaseReleasesLease pins the wrapper teardown: retiring a
// member (backend Close) makes its session lease available again, so the next
// bind re-acquires it — the lease lives exactly as long as the backend.
func TestMemberAuthorityReleaseReleasesLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coder-session.jsonl")
	rec := newRecorderTurnRunner()
	ctrl := memberAuthorityController(t, path, rec)
	wl, err := bindMemberSessionAuthority(ctrl, path, true)
	if err != nil {
		t.Fatalf("bindMemberSessionAuthority: %v", err)
	}
	if err := ctrl.SubmitUserTurnOrError("go", "go"); err != nil {
		t.Fatalf("submit = %v", err)
	}
	waitForTurns(t, rec, 1)
	waitMemberTurnDone(t, ctrl)
	if !agent.SessionLeaseHeldByCurrentRuntime(path) {
		t.Fatal("the member session lease must be held after binding")
	}
	ctrl.Close()
	wl.Close()
	if agent.SessionLeaseHeldByCurrentRuntime(path) {
		t.Error("closing the member authority must release the session lease")
	}
}

// memberAuthorityController is a member-shaped controller that owns no lease,
// so tests bind one through the production helper.
func memberAuthorityController(t *testing.T, path string, rec *recorderTurnRunner) *control.Controller {
	t.Helper()
	sess := agent.NewSession("sys")
	exec := agent.New(nil, nil, sess, agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{
		Executor: exec, SystemPrompt: "sys", SessionPath: path,
		SessionDir: filepath.Dir(path), Sink: event.Discard, Runner: rec,
	})
	t.Cleanup(func() { ctrl.Close() }) // idempotent; tests may close earlier
	return ctrl
}

// waitMemberTurnDone blocks until the controller settles one turn (admission
// opens again), so the post-commit save has completed. A turn is settled when
// SubmitUserTurn admits again — finishGuardedTurn has run and the write
// completed with the turn.
func waitMemberTurnDone(t *testing.T, c *control.Controller) {
	t.Helper()
	if c == nil {
		return
	}
	deadline := time.Now().Add(3 * time.Second)
	for c.RuntimeStatus().Running {
		if time.Now().After(deadline) {
			t.Fatal("member turn never settled")
		}
		time.Sleep(time.Millisecond)
	}
}
