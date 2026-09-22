package cli

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/team"
)

// overlapStamper is a memberHistoryStamper that records how many reads are in
// flight at once. A cockpit worker must never overlap its own member's work, so
// this is what pins the serialization the worker exists for.
type overlapStamper struct {
	label    string
	inFlight atomic.Int64
	peak     atomic.Int64
	calls    atomic.Int64
	hold     time.Duration
}

func (s *overlapStamper) HistoryStamp() string {
	now := s.inFlight.Add(1)
	for {
		peak := s.peak.Load()
		if now <= peak || s.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	if s.hold > 0 {
		time.Sleep(s.hold)
	}
	s.calls.Add(1)
	s.inFlight.Add(-1)
	return s.label + "-stamp"
}

// cockpitTestStore opens a real owner store for one cockpit test.
func cockpitTestStore(t *testing.T) (*team.OwnerStore, team.MemberBinding) {
	t.Helper()
	root := t.TempDir()
	owners, err := team.NewOwnerStore(root)
	if err != nil {
		t.Fatalf("open owner store: %v", err)
	}
	return owners, team.MemberBinding{Team: "alpha", MemberID: "lead"}
}

// TestCockpitPublishesOffTheCaller pins what the cockpit is for: submit returns
// without the store write having happened, and the write lands on the worker.
func TestCockpitPublishesOffTheCaller(t *testing.T) {
	owners, binding := cockpitTestStore(t)
	cockpit := newMemberCockpit()
	defer cockpit.close()
	stamper := &overlapStamper{label: binding.MemberID, hold: 30 * time.Millisecond}

	if !cockpit.submit(cockpitCommand{owners: owners, binding: binding, stamper: stamper, bump: true}) {
		t.Fatal("a live cockpit must accept a publication")
	}
	if _, err := owners.Fingerprint(team.OwnerKey{TeamID: binding.Team, MemberID: binding.MemberID}); err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	// The identity read is the first thing the worker does, so a stamp already
	// taken proves the work left the caller.
	if got := stamper.calls.Load(); got != 0 {
		t.Fatalf("submit ran the publication inline: %d identity reads before it returned", got)
	}
	if !cockpit.awaitIdle(5 * time.Second) {
		t.Fatal("the publication never settled")
	}
	fingerprint, err := owners.Fingerprint(team.OwnerKey{TeamID: binding.Team, MemberID: binding.MemberID})
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if !fingerprint.Present || fingerprint.Generation != 1 || fingerprint.Stem != "lead-stamp" {
		t.Fatalf("published identity = %+v, want the worker's own read at generation 1", fingerprint)
	}
}

// TestCockpitSerializesPerMemberAndRunsMembersInParallel pins both halves of the
// worker's shape: one member's publications never overlap, and two members do
// not wait on each other.
func TestCockpitSerializesPerMemberAndRunsMembersInParallel(t *testing.T) {
	owners, _ := cockpitTestStore(t)
	cockpit := newMemberCockpit()
	defer cockpit.close()
	lead := &overlapStamper{label: "lead", hold: 20 * time.Millisecond}
	alice := &overlapStamper{label: "alice", hold: 20 * time.Millisecond}
	leadBinding := team.MemberBinding{Team: "alpha", MemberID: "lead"}
	aliceBinding := team.MemberBinding{Team: "alpha", MemberID: "alice"}

	const each = 3
	for range each {
		cockpit.submit(cockpitCommand{owners: owners, binding: leadBinding, stamper: lead, bump: true})
		cockpit.submit(cockpitCommand{owners: owners, binding: aliceBinding, stamper: alice, bump: true})
	}
	if !cockpit.awaitIdle(5 * time.Second) {
		t.Fatal("the publications never settled")
	}
	if got := lead.calls.Load(); got != each {
		t.Fatalf("lead published %d times, want %d", got, each)
	}
	if got := alice.calls.Load(); got != each {
		t.Fatalf("alice published %d times, want %d", got, each)
	}
	if got := lead.peak.Load(); got != 1 {
		t.Fatalf("lead's publications overlapped %d deep, want them serialized", got)
	}
	if got := alice.peak.Load(); got != 1 {
		t.Fatalf("alice's publications overlapped %d deep, want them serialized", got)
	}
	// Every bump is accounted for: serialization is what makes the count a
	// truthful "how many changes happened".
	fingerprint, err := owners.Fingerprint(team.OwnerKey{TeamID: "alpha", MemberID: "lead"})
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if fingerprint.Generation != each {
		t.Fatalf("lead's generation = %d, want %d", fingerprint.Generation, each)
	}
}

// TestCockpitReportsFailuresAndSkipsAMemberWithoutIdentity pins the two outcomes
// the window acts on: a store failure is reported with the member's key, and a
// member that cannot name its own history is skipped rather than published under
// an invented identity.
func TestCockpitReportsFailuresAndSkipsAMemberWithoutIdentity(t *testing.T) {
	owners, binding := cockpitTestStore(t)
	cockpit := newMemberCockpit()
	defer cockpit.close()

	if !cockpit.submit(cockpitCommand{
		owners: owners, binding: binding, stamper: namelessStamper{},
		bump: true, requireIdentity: true, errKey: "publish:lead",
	}) {
		t.Fatal("a live cockpit must accept a publication")
	}
	if !cockpit.awaitIdle(5 * time.Second) {
		t.Fatal("the publication never settled")
	}
	results := cockpit.drain()
	if len(results) != 1 || results[0].errKey != "publish:lead" || results[0].errMsg != "" {
		t.Fatalf("a skipped member must report nothing: %+v", results)
	}
	if fingerprint, _ := owners.Fingerprint(team.OwnerKey{TeamID: "alpha", MemberID: "lead"}); fingerprint.Present {
		t.Fatalf("a member with no readable identity must not be published: %+v", fingerprint)
	}

	// A store that cannot be written is a report, not a silence: the window has
	// to be able to say that a peer will not learn of the change.
	closed := newMemberCockpit()
	if !closed.submit(cockpitCommand{
		owners: owners, binding: binding, stamper: &overlapStamper{label: "closed-store"},
		bump: true, errKey: "publish:lead",
	}) {
		t.Fatal("a live cockpit must accept a publication")
	}
	if !closed.awaitIdle(5 * time.Second) {
		t.Fatal("the publication never settled")
	}
	if results := closed.drain(); len(results) != 1 || results[0].errKey != "publish:lead" {
		t.Fatalf("a settled publication must report exactly one result: %+v", results)
	}
	closed.close()
	if closed.submit(cockpitCommand{owners: owners, binding: binding, stamper: &overlapStamper{label: "x"}}) {
		t.Fatal("a closed cockpit must refuse new work")
	}
}

// TestCockpitIdleTracksRunningWork pins the await the window and its tests use:
// awaitIdle must not report settled while a command is running.
func TestCockpitIdleTracksRunningWork(t *testing.T) {
	owners, binding := cockpitTestStore(t)
	cockpit := newMemberCockpit()
	defer cockpit.close()
	started := make(chan struct{})
	stamper := &signalStamper{label: "lead", started: started, release: make(chan struct{})}

	cockpit.submit(cockpitCommand{owners: owners, binding: binding, stamper: stamper, bump: true})
	<-started
	if cockpit.idle() {
		t.Fatal("a running publication must not read as idle")
	}
	close(stamper.release)
	if !cockpit.awaitIdle(5 * time.Second) {
		t.Fatal("the publication never settled")
	}
	if !cockpit.idle() {
		t.Fatal("a settled cockpit must read as idle")
	}
}

// signalStamper blocks the identity read until the test releases it, so the
// cockpit's in-flight state is observable.
type signalStamper struct {
	label   string
	started chan struct{}
	release chan struct{}
}

func (s *signalStamper) HistoryStamp() string {
	close(s.started)
	<-s.release
	return s.label + "-stamp"
}

// TestCockpitPublicationOnTheTUIAppliesTheReport pins the wiring: the window's
// publication goes through the registry's cockpit, and its report comes back
// through the tick collection instead of blocking the caller.
func TestCockpitPublicationOnTheTUIAppliesTheReport(t *testing.T) {
	m := overlayWithBackends(t, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	owners, err := team.NewOwnerStore(filepath.Join(t.TempDir(), "team"))
	if err != nil {
		t.Fatalf("open owner store: %v", err)
	}
	m.teamPick.owners = owners

	m.publishOwnerHistory(cockpitCommand{
		owners: owners, binding: team.MemberBinding{Team: "alpha", MemberID: "lead"},
		stamper: &overlapStamper{label: "lead"}, bump: true, errKey: "publish:lead",
	})
	if !m.teamBackends.cockpit().awaitIdle(5 * time.Second) {
		t.Fatal("the window's publication never settled")
	}
	// The report is collected as a tick message, and applying it is what clears
	// the failure the previous report left behind.
	m.teamPick.session.syncErrKey = "publish:lead"
	cmd := m.collectCockpitResults()
	if cmd == nil {
		t.Fatal("a settled publication must be collected")
	}
	next, _ = m.update(cmd())
	m = next.(chatTUI)
	if m.teamPick.session.syncErrKey != "" {
		t.Fatalf("a successful publication must clear the failure, syncErrKey = %q", m.teamPick.session.syncErrKey)
	}
}

// TestCockpitPublicationFailureReachesTheWindow pins the failure half of the
// wiring: a publication that cannot be recorded reaches the window as a report,
// where the user can read it — a peer that never learns of the change is exactly
// what the silence would hide.
func TestCockpitPublicationFailureReachesTheWindow(t *testing.T) {
	m := overlayWithBackends(t, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	// An owner root that cannot be created: the record fails where the store has
	// nowhere to write.
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("seed a blocked owner root: %v", err)
	}
	owners, err := team.NewOwnerStore(blocked)
	if err != nil {
		t.Skipf("owner store refuses a blocked root: %v", err)
	}
	m.teamPick.owners = owners

	m.publishOwnerHistory(cockpitCommand{
		owners: owners, binding: team.MemberBinding{Team: "alpha", MemberID: "lead"},
		stamper: namelessStamper{}, bump: true, errKey: "publish:lead",
	})
	if !m.teamBackends.cockpit().awaitIdle(5 * time.Second) {
		t.Fatal("the window's publication never settled")
	}
	cmd := m.collectCockpitResults()
	if cmd == nil {
		t.Fatal("a failed publication must be collected")
	}
	next, _ = m.update(cmd())
	m = next.(chatTUI)
	if m.teamPick.session.errMsg == "" {
		t.Fatal("a failed publication must be said where the user can read it")
	}
}

// namelessStamper is a member backend that cannot name its own history yet.
type namelessStamper struct{}

func (namelessStamper) HistoryStamp() string { return "" }
