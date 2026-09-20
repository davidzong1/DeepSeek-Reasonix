package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// legacyHistoryController is a controller executing the legacy path with a
// durable transcript on disk, so HistoryStamp has a real file revision to
// report and ReloadHistoryIfChanged has a real file to load. It returns the
// controller and its transcript path.
func legacyHistoryController(t *testing.T) (*Controller, string) {
	t.Helper()
	return legacyHistoryControllerWithRunner(t, nil)
}

// legacyHistoryControllerWithRunner is legacyHistoryController with an injected
// turn runner, so a test can hold a turn open and drive the busy gate.
func legacyHistoryControllerWithRunner(t *testing.T, runner agent.Runner) (*Controller, string) {
	t.Helper()
	dir := schemaOneTempDir(t)
	exec := agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard)
	exec.Session().Add(provider.Message{Role: provider.RoleUser, Content: "durable-1"})
	c := newOwnedTestController(t, Options{Executor: exec, SessionDir: dir, Label: "test", Runner: runner})
	c.SetSessionPath(agent.NewSessionPath(dir, "test"))
	if err := c.Snapshot(); err != nil {
		t.Fatal(err)
	}
	path := c.SessionPath()
	if path == "" {
		t.Fatal("the fixture must have a transcript path")
	}
	return c, path
}

// writePeerTranscript replaces the transcript on disk with one appended message,
// the way a second runtime writing the same session would leave it.
func writePeerTranscript(t *testing.T, path, body string) {
	t.Helper()
	loaded, err := agent.LoadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Add(provider.Message{Role: provider.RoleUser, Content: body})
	if err := loaded.Save(path); err != nil {
		t.Fatal(err)
	}
}

// TestHistoryStampTracksTheDurableTranscript pins the change signal the whole
// cross-window contract rests on: the stamp moves when the transcript on disk
// moves, and only then. A stamp that did not move would leave a peer rendering
// a transcript its writer replaced; one that moved without a change would make
// every window reload on every tick.
func TestHistoryStampTracksTheDurableTranscript(t *testing.T) {
	c, path := legacyHistoryController(t)
	before := c.HistoryStamp()
	if before == "" {
		t.Fatal("a controller with a durable transcript must have a stamp")
	}
	if again := c.HistoryStamp(); again != before {
		t.Fatalf("an unchanged transcript must stamp the same twice: %q then %q", before, again)
	}

	writePeerTranscript(t, path, "appended by a peer")

	if after := c.HistoryStamp(); after == before {
		t.Fatalf("an appended transcript must change the stamp, still %q", after)
	}
}

// TestHistoryStampIsEmptyWithoutHistory pins the other half of the signal: a
// controller with no readable history has no identity, and an empty stamp is
// how a caller knows not to poll for one.
func TestHistoryStampIsEmptyWithoutHistory(t *testing.T) {
	c := newOwnedTestController(t, Options{})
	if got := c.HistoryStamp(); got != "" {
		t.Fatalf("a controller with no session must stamp empty, got %q", got)
	}
	var nilController *Controller
	if got := nilController.HistoryStamp(); got != "" {
		t.Fatalf("a nil controller must stamp empty, got %q", got)
	}
}

// TestReloadHistoryIfChangedAdoptsTheDurableView pins the reload itself: the
// controller's transcript becomes the durable one, and the stamp it adopted is
// reported so the caller can stop polling for that change.
func TestReloadHistoryIfChangedAdoptsTheDurableView(t *testing.T) {
	c, path := legacyHistoryController(t)
	writePeerTranscript(t, path, "appended by a peer")
	stamp := c.HistoryStamp()

	reloaded, err := c.ReloadHistoryIfChanged(context.Background(), stamp)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !reloaded {
		t.Fatal("a changed stamp must reload")
	}
	joined := messagesText(c.History())
	if !strings.Contains(joined, "appended by a peer") {
		t.Fatalf("the adopted transcript must carry the durable view, got:\n%s", joined)
	}
	// The change is adopted once: the same stamp is a no-op, which is what keeps
	// the 1s poll cheap.
	again, err := c.ReloadHistoryIfChanged(context.Background(), stamp)
	if err != nil {
		t.Fatalf("second reload: %v", err)
	}
	if again {
		t.Fatal("an already-adopted stamp must not reload again")
	}
}

// TestReloadHistoryIfChangedRefusesABusyRuntime pins the busy gate: a running
// turn owns the in-memory transcript, and adopting the durable view underneath
// it would drop the local input and the turn in flight. The refusal reports
// false without recording the stamp, so the next idle poll still sees it.
func TestReloadHistoryIfChangedRefusesABusyRuntime(t *testing.T) {
	sess := agent.NewSession("sys")
	release := make(chan struct{})
	c, path := legacyHistoryControllerWithRunner(t, blockingRunner{session: sess, release: release})
	writePeerTranscript(t, path, "appended by a peer")
	stamp := c.HistoryStamp()

	done := make(chan error, 1)
	go func() { done <- c.RunTurn(context.Background(), "hold the turn open") }()
	waitForRunning(t, c)
	t.Cleanup(func() {
		close(release)
		<-done
	})

	reloaded, err := c.ReloadHistoryIfChanged(context.Background(), stamp)
	if err != nil {
		t.Fatalf("a busy refusal must not be an error: %v", err)
	}
	if reloaded {
		t.Fatal("a busy runtime must not adopt the durable view")
	}
	if got := messagesText(c.History()); strings.Contains(got, "appended by a peer") {
		t.Fatalf("a busy runtime must keep its own transcript, got:\n%s", got)
	}
}

// TestReloadHistoryIfChangedIsContextBounded pins defect (5)'s bound: the read
// pages the session's history index, which can rebuild itself over a whole log,
// so a caller must be able to give up on it. A cancelled context must surface as
// an error and must not adopt anything.
func TestReloadHistoryIfChangedIsContextBounded(t *testing.T) {
	c, path := legacyHistoryController(t)
	writePeerTranscript(t, path, "appended by a peer")
	stamp := c.HistoryStamp()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	reloaded, err := c.ReloadHistoryIfChanged(ctx, stamp)
	if err == nil {
		t.Fatal("a cancelled read must report the failure, not look like a no-op")
	}
	if reloaded {
		t.Fatal("a cancelled read must not adopt anything")
	}
	// The stamp is not recorded, so the change is still visible to a later call.
	if got := messagesText(c.History()); strings.Contains(got, "appended by a peer") {
		t.Fatalf("a cancelled read must leave the transcript alone, got:\n%s", got)
	}
	reloaded, err = c.ReloadHistoryIfChanged(context.Background(), stamp)
	if err != nil || !reloaded {
		t.Fatalf("the change must still be adoptable after a cancelled read: reloaded=%v err=%v", reloaded, err)
	}
}

// TestReloadHistoryIfChangedIgnoresAnEmptyStamp pins the guard: an empty stamp
// means "no readable identity", not "reload now". Adopting on it would replace
// the transcript of every controller that has no history yet.
func TestReloadHistoryIfChangedIgnoresAnEmptyStamp(t *testing.T) {
	c, _ := legacyHistoryController(t)
	before := messagesText(c.History())

	reloaded, err := c.ReloadHistoryIfChanged(context.Background(), "  ")
	if err != nil || reloaded {
		t.Fatalf("an empty stamp must be a no-op, reloaded=%v err=%v", reloaded, err)
	}
	if got := messagesText(c.History()); got != before {
		t.Fatalf("an empty stamp must not touch the transcript:\n%s\nwant:\n%s", got, before)
	}
}

// TestReloadHistoryIfChangedReportsAReadFailure pins the propagation: a durable
// read that fails must surface as an error rather than a silent no-op, because
// the caller has to be able to say why the window is not refreshing.
func TestReloadHistoryIfChangedReportsAReadFailure(t *testing.T) {
	c, path := legacyHistoryController(t)
	writePeerTranscript(t, path, "appended by a peer")
	stamp := c.HistoryStamp()

	// A directory where the transcript belongs: loading it fails
	// deterministically, and it is not a missing file (which is the empty state).
	replaced := filepath.Join(t.TempDir(), "as-dir.jsonl")
	if err := os.MkdirAll(replaced, 0o700); err != nil {
		t.Fatal(err)
	}
	c.SetSessionPath(replaced)

	reloaded, err := c.ReloadHistoryIfChanged(context.Background(), stamp)
	if err == nil {
		t.Fatal("an unreadable transcript must report the failure")
	}
	if reloaded {
		t.Fatal("a failed read must not adopt anything")
	}
}

func messagesText(msgs []provider.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(string(m.Content))
		b.WriteString("\n")
	}
	return b.String()
}
