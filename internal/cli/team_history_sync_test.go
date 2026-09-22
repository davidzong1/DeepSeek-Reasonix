package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/team"
)

// syncFixture opens the overlay bound to the leader's stub backend, so a test
// can drive the cross-window poll without a real session store.
func syncFixture(t *testing.T, backend stubBackend) chatTUI {
	t.Helper()
	writeTeamFixture(t, twoMemberTeam())
	m := openTeamOverlay(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.memberEvents = newMemberEventPump()
	m.teamBackends = newTeamBackends(func(b team.MemberBinding) (control.SessionAPI, error) {
		return backend, nil
	}, 4)
	if cmd := m.switchTeamMember("lead"); cmd == nil {
		t.Fatal("binding the leader must succeed")
	}
	return m
}

// renderable hands the window back the overlay's own controller so a frame can
// be rendered: a stub backend cannot answer the status line's sub-ports, and
// the render is incidental to what these tests assert. The session state the
// assertions read — the stamps, the flags, the transcript — is untouched.
func renderable(m chatTUI) chatTUI {
	if m.ambient != nil {
		m.ctrl = m.ambient
	}
	return m
}

// publishOwnerHistory writes one owner generation the way a second window
// would, so the poll under test has a change to notice.
func publishOwnerHistory(t *testing.T, m chatTUI, member string) {
	t.Helper()
	owners := ownerStoreOf(t, m)
	if err := owners.BumpHistory(t.Context(), team.OwnerKey{TeamID: "alpha", MemberID: member}, "peer-stem", true); err != nil {
		t.Fatal(err)
	}
}

// pollHistorySync runs one poll and, when it scheduled a durable read, applies
// the result through the same handler the update loop uses. It returns the TUI
// and whether a read was scheduled: a poll that adopted a change in place, or
// refused one, schedules nothing.
func pollHistorySync(t *testing.T, m chatTUI) (chatTUI, bool) {
	t.Helper()
	cmd := m.syncBoundHistory(m.boundOwnerFingerprint())
	if cmd == nil {
		return m, false
	}
	tick, ok := cmd().(teamRosterRefreshMsg)
	if !ok || tick.sync == nil {
		t.Fatalf("the poll's command must report a history sync result, got %T", cmd())
	}
	next, _ := m.update(tick)
	return next.(chatTUI), true
}

// TestRosterTickPollsBoundHistory pins the wiring the whole feature rests on:
// the existing 1s roster tick is what runs the poll, so no second timer and no
// keypress is needed. The tick schedules the durable read rather than running
// it, which is asserted here without executing the tick's own timer.
func TestRosterTickPollsBoundHistory(t *testing.T) {
	reloads := 0
	m := syncFixture(t, stubBackend{label: "lead", stamp: "s1", reloads: &reloads})
	pollHistorySync(t, m) // first observation adopts the identity
	publishOwnerHistory(t, m, "lead")

	next, cmd := m.update(teamRosterRefreshMsg{})
	m = next.(chatTUI)

	if cmd == nil {
		t.Fatal("the roster tick must schedule the history poll")
	}
	if got := m.teamPick.session.syncInFlight; got == "" {
		t.Fatal("the roster tick must run the poll and schedule a durable read")
	}
	if reloads != 0 {
		t.Fatalf("the poll must not read on the Update goroutine, reloads = %d", reloads)
	}
}

// TestHistorySyncReadRunsOffTheUpdateGoroutine pins the scheduling half of the
// refresh: the durable read reaches the session's history index, which can
// rebuild itself over a whole log, so it must not run on the Update goroutine
// where it would freeze the frame. The poll therefore returns a command without
// having reloaded anything yet, and the reload happens when that command runs.
func TestHistorySyncReadRunsOffTheUpdateGoroutine(t *testing.T) {
	reloads := 0
	m := syncFixture(t, stubBackend{label: "lead", stamp: "s1", reloads: &reloads})
	pollHistorySync(t, m)
	publishOwnerHistory(t, m, "lead")

	cmd := m.syncBoundHistory(m.boundOwnerFingerprint())
	if cmd == nil {
		t.Fatal("a changed owner identity must schedule a durable read")
	}
	if reloads != 0 {
		t.Fatalf("the poll must not reload on the Update goroutine, reloads = %d", reloads)
	}

	tick, ok := cmd().(teamRosterRefreshMsg)
	if !ok || tick.sync == nil {
		t.Fatalf("the scheduled command must report a history sync result, got %T", cmd())
	}
	if !tick.sync.reloaded {
		t.Fatalf("the read must report the adopted change: %+v", tick.sync)
	}
	if reloads != 1 {
		t.Fatalf("running the command must perform exactly one reload, reloads = %d", reloads)
	}
}

// TestUpdateRoutesHistorySyncDone pins the wiring the poll depends on: the
// result of the off-goroutine read has to reach the update loop, or the change
// is adopted into the controller and never rendered. The tick carries it, so a
// routed failure is observable as the pending retry it sets — only an applied
// result ever sets it.
func TestUpdateRoutesHistorySyncDone(t *testing.T) {
	m := syncFixture(t, stubBackend{label: "lead", stamp: "s1"})
	m = renderable(m)
	publishOwnerHistory(t, m, "lead")
	m = pollHistorySyncOnce(t, m)
	stamp := m.teamPick.session.syncStamp
	if stamp == "" || stamp == "absent" {
		t.Fatalf("precondition: the bound member must have an adopted identity, got %q", stamp)
	}
	m.teamPick.session.syncInFlight = stamp

	after, _ := m.Update(teamRosterRefreshMsg{sync: &historySyncDone{
		member: "lead", stamp: stamp, err: errors.New("store unavailable"),
	}})
	got := after.(chatTUI)

	if !got.teamPick.session.syncPending {
		t.Fatalf("the tick must route the carried result: pending = false after a failed read")
	}
	if got.teamPick.session.syncInFlight == "" {
		t.Fatal("the failed result must be superseded by the next tick's own read")
	}
}

// pollHistorySyncOnce adopts the bound member's current identity the way the
// first observation after a bind does, without scheduling a read.
func pollHistorySyncOnce(t *testing.T, m chatTUI) chatTUI {
	t.Helper()
	m.teamPick.session.syncStamp = ""
	next, _ := m.update(teamRosterRefreshMsg{})
	return next.(chatTUI)
}

// TestHistorySyncStaleResultIsDropped pins the ordering guard: a read that
// started before the window moved on must not be applied to whatever the window
// shows now. A result for a stamp no longer in flight — a rebind, a newer
// change, a closed session — is dropped rather than rendered.
func TestHistorySyncStaleResultIsDropped(t *testing.T) {
	reloads := 0
	m := syncFixture(t, stubBackend{label: "lead", stamp: "s1", reloads: &reloads})
	pollHistorySync(t, m)
	publishOwnerHistory(t, m, "lead")

	cmd := m.syncBoundHistory(m.boundOwnerFingerprint())
	if cmd == nil {
		t.Fatal("precondition: a read must be scheduled")
	}
	stale := m.teamPick.session.syncInFlight
	// The read itself already ran in the controller; what must not happen is the
	// window adopting its result. The transcript the window shows is the marker.
	shown := strings.Join(m.transcript, "\n")

	// The window moves on: another change lands and its read supersedes this one.
	m.teamPick.session.syncInFlight = "someone-else"

	m.handleHistorySyncDone(historySyncDone{member: "lead", stamp: stale, reloaded: true})

	if got := m.teamPick.session.syncInFlight; got != "someone-else" {
		t.Fatalf("a stale result must not clear the read actually in flight, got %q", got)
	}
	if m.teamPick.session.syncStamp == stale {
		t.Fatal("a stale result must not be adopted as the rendered identity")
	}
	if got := strings.Join(m.transcript, "\n"); got != shown {
		t.Fatal("a stale result must not rebuild the transcript")
	}
}

// TestHistorySyncAppliesTheResultOnceLanded pins the other half: the result
// comes back through the update loop, is matched against the read in flight,
// and only then rebuilds the transcript.
func TestHistorySyncAppliesTheResultOnceLanded(t *testing.T) {
	reloads := 0
	m := syncFixture(t, stubBackend{label: "lead", stamp: "s1", reloads: &reloads})
	pollHistorySync(t, m)
	publishOwnerHistory(t, m, "lead")
	if got := ownerHistoryOf(t, m, "alpha", "lead").Generation; got != 1 {
		t.Fatalf("precondition: the peer publication must be visible, generation = %d", got)
	}

	m, ran := pollHistorySync(t, m)

	if !ran {
		t.Fatal("the changed identity must schedule a read")
	}
	if reloads != 1 {
		t.Fatalf("the landed result must have reloaded once, reloads = %d", reloads)
	}
	if m.teamPick.session.syncPending {
		t.Fatal("an adopted change must not stay pending")
	}
	if m.teamPick.session.syncInFlight != "" {
		t.Fatalf("the in-flight read must be cleared, got %q", m.teamPick.session.syncInFlight)
	}
	if got, want := m.teamPick.session.syncStamp, ownerHistoryStamp(ownerHistoryOf(t, m, "alpha", "lead")); got != want {
		t.Fatalf("the adopted stamp = %q, want the published one %q", got, want)
	}
}

// TestHistorySyncBusyWindowRetries pins the busy contract: a change observed
// while the window owns an in-flight turn is recorded as pending and retried,
// never adopted underneath the turn — adopting it would drop the local input
// and the running turn it replaced.
func TestHistorySyncBusyWindowRetries(t *testing.T) {
	reloads := 0
	m := syncFixture(t, stubBackend{
		label: "lead", stamp: "s1", reloads: &reloads,
		status: control.RuntimeStatus{Running: true},
	})
	pollHistorySync(t, m)
	publishOwnerHistory(t, m, "lead")

	m, ran := pollHistorySync(t, m)

	if ran {
		t.Fatal("a busy window must not schedule a reload")
	}
	if !m.teamPick.session.syncPending {
		t.Fatal("a change seen while busy must stay pending for a later tick")
	}
	if reloads != 0 {
		t.Fatalf("a busy window must not reload, reloads = %d", reloads)
	}
}

// TestHistorySyncReadFailureIsVisibleAndRetried pins the failure half of the
// change signal: a read that fails is not adopted, is said out loud, and stays
// pending so a later tick retries it. A silently swallowed failure here leaves
// the window rendering a transcript the writer replaced.
func TestHistorySyncReadFailureIsVisibleAndRetried(t *testing.T) {
	m := syncFixture(t, stubBackend{
		label: "lead", stamp: "s1", reloadErr: errors.New("store unavailable"),
	})
	pollHistorySync(t, m)
	publishOwnerHistory(t, m, "lead")

	m, _ = pollHistorySync(t, m)

	if got := strings.Join(m.transcript, "\n"); !strings.Contains(got, "could not be read") {
		t.Fatalf("a refused read must be reported on the transcript, got:\n%s", got)
	}
	if m.teamPick.session.syncErrKey == "" {
		t.Fatal("the reported failure must be recorded so it is not repeated every tick")
	}
	if !m.teamPick.session.syncPending {
		t.Fatal("a failed read must leave the change pending so a later tick retries it")
	}
	// The failure is said once: a second poll for the same stamp adds nothing.
	before := len(m.transcript)
	m, _ = pollHistorySync(t, m)
	if len(m.transcript) != before {
		t.Fatalf("a repeating failure must not be reported again, transcript grew from %d to %d", before, len(m.transcript))
	}
}

// TestHistorySyncCorruptOwnerFailsClosed pins defect (1) on the read side: an
// owner whose metadata cannot be read is not an unnamed owner. The window
// refuses to adopt the identity it cannot trust — it must not render ":0" as if
// the peer had published nothing — and says so instead of quietly polling.
func TestHistorySyncCorruptOwnerFailsClosed(t *testing.T) {
	reloads := 0
	m := syncFixture(t, stubBackend{label: "lead", stamp: "s1", reloads: &reloads})
	pollHistorySync(t, m)
	adopted := m.teamPick.session.syncStamp

	// Corrupt the bound member's owner document exactly as a torn write would.
	dir := filepath.Join(ownerStoreOf(t, m).Root(), "alpha", "lead")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".meta.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	m, ran := pollHistorySync(t, m)

	if ran {
		t.Fatal("an unreadable owner identity must not schedule a reload")
	}
	if reloads != 0 {
		t.Fatalf("an unreadable owner identity must never be adopted, reloads = %d", reloads)
	}
	if got := strings.Join(m.transcript, "\n"); !strings.Contains(got, "unreadable") {
		t.Fatalf("an unreadable owner identity must be reported, got:\n%s", got)
	}
	if m.teamPick.session.errMsg == "" {
		t.Fatal("the refusal must also land on the session panel")
	}
	if m.teamPick.session.syncPending {
		t.Fatal("an unreadable identity is terminal, not a change waiting for a better tick")
	}
	if m.teamPick.session.syncStamp != adopted {
		t.Fatalf("a corrupt owner must not replace the adopted identity %q with %q", adopted, m.teamPick.session.syncStamp)
	}
}

// TestHistorySyncReplaySuppressesLegacyClearScreen pins defect (3): the refresh
// rebuilds the whole transcript, so it arms the same sessionSwitch the other
// rebuilds arm. Without it the legacy scroll-clear workaround answers the
// refresh with a second ClearScreen mid-rebuild — a full redraw on a window
// that merely scrolled.
func TestHistorySyncReplaySuppressesLegacyClearScreen(t *testing.T) {
	m := syncFixture(t, stubBackend{label: "lead", stamp: "s1"})
	m.legacyScrollClear = true

	_ = m.replayBoundHistory()
	if !m.sessionSwitch {
		t.Fatal("a cross-window replay must arm sessionSwitch like every other rebuild")
	}

	// The next rendered frame must not emit the workaround's ClearScreen even
	// though the viewport offset moved.
	m = renderable(m)
	m.forceGotoBottom = true
	m.transcriptDirty = false
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	if cmd != nil {
		t.Fatalf("the refresh frame must not emit a legacy ClearScreen, got a command")
	}
	if m.sessionSwitch {
		t.Fatal("sessionSwitch is consumed by the frame it suppresses")
	}
}
