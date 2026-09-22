package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/jobs"
	"reasonix/internal/provider"
	"reasonix/internal/team"
)

// usageOwnerFixture is one member whose writer owns the session, plus the owner
// store its usage channel lives in.
type usageOwnerFixture struct {
	owners      *team.OwnerStore
	writer      followerWriter
	ownerDir    string
	sessionFile string
	teamName    string
	memberID    string
}

func newUsageOwnerFixture(t *testing.T) usageOwnerFixture {
	t.Helper()
	root := t.TempDir()
	owners, err := team.NewOwnerStore(filepath.Join(root, "team"))
	if err != nil {
		t.Fatal(err)
	}
	const teamName, memberID, sessionFile = "alpha", "lead", "team-alpha-lead.json"
	paths, err := owners.Paths(team.OwnerKey{TeamID: teamName, MemberID: memberID})
	if err != nil {
		t.Fatal(err)
	}
	writer := newFollowerWriter(t, root, paths.Transcript)
	publishFollowerIdentity(t, owners, teamName, memberID, writer.ctrl.HistoryStamp())
	return usageOwnerFixture{
		owners: owners, writer: writer, ownerDir: paths.Dir,
		sessionFile: sessionFile, teamName: teamName, memberID: memberID,
	}
}

// publishUsage writes one observation exactly as the writer's publisher does.
func (f usageOwnerFixture) publishUsage(t *testing.T, usage team.OwnerUsage) {
	t.Helper()
	key := team.OwnerKey{TeamID: f.teamName, MemberID: f.memberID}
	if err := f.owners.WriteUsage(context.Background(), key, usage); err != nil {
		t.Fatal(err)
	}
}

// bindFollower attaches the read-only follower for the fixture's member.
func (f usageOwnerFixture) bindFollower(t *testing.T) control.SessionAPI {
	t.Helper()
	backend, err := followerBindAttempt(t, f.owners, f.writer.storeRoot, f.teamName, f.memberID, f.sessionFile, f.ownerDir, true)
	if err != nil {
		t.Fatalf("a contended member must attach a follower, got %v", err)
	}
	t.Cleanup(backend.Close)
	return backend
}

// usageFilePath is the published document's on-disk name, pinned literally so a
// rename has to be a deliberate, visible decision.
func (f usageOwnerFixture) usageFilePath() string { return filepath.Join(f.ownerDir, ".usage.json") }

// sampleWriterUsage is the observation every rendering test publishes.
func sampleWriterUsage(publishedAt time.Time) team.OwnerUsage {
	return team.OwnerUsage{
		PublishedAt: publishedAt.UTC().Format(time.RFC3339Nano),
		ContextUsed: 12000, ContextWindow: 128000, CompactRatio: 0.8,
		LastTurn: &team.OwnerUsageLastTurn{PromptTokens: 1000, CacheHitTokens: 700, CacheMissTokens: 300},
		CacheHit: 700, CacheMiss: 300,
		Jobs: []team.OwnerUsageJob{{ID: "job-1", Kind: "shell", Label: "build", Status: "running", StartedAt: 42}},
	}
}

// TestFollowerRendersWriterUsageInStatusBand is the consumed boundary for this
// work: the window's own frame shows the writer's context, cache and job
// figures. Before the usage channel existed the follower answered zeros, so the
// band rendered none of them — which is what an operator watching a member's
// spend saw.
func TestFollowerRendersWriterUsageInStatusBand(t *testing.T) {
	f := newUsageOwnerFixture(t)
	f.publishUsage(t, sampleWriterUsage(time.Now()))
	backend := f.bindFollower(t)

	m := newChatTUI(newOwnedTestController(t, control.Options{}), "", make(chan event.Event, 1), 140)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	m = next.(chatTUI)
	m.bindBackend(backend, ownerKey{}, replayInline)

	// The context group renders the used tokens and the compaction headroom the
	// writer's CompactRatio implies (80% threshold, 9% used), so asserting the
	// headroom proves the ratio crossed the channel too — not just the tokens.
	band := ansi.Strip(m.View().Content)
	for _, want := range []string{"12.0K", "71%", "70.00%", "⚙ 1"} {
		if !strings.Contains(band, want) {
			t.Fatalf("the bound window must render the writer's usage; %q missing from:\n%s", want, band)
		}
	}
}

// TestFollowerHidesUsageOnceTheWriterStops pins the TTL rule: the numbers are a
// writer-liveness signal, so a document the writer stopped refreshing must
// disappear rather than pose as current.
func TestFollowerHidesUsageOnceTheWriterStops(t *testing.T) {
	for _, tc := range []struct {
		name    string
		publish bool
		at      time.Time
	}{
		{"stale document", true, time.Now().Add(-2 * followerUsageTTL)},
		{"no document at all", false, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUsageOwnerFixture(t)
			if tc.publish {
				f.publishUsage(t, sampleWriterUsage(tc.at))
			}
			backend := f.bindFollower(t)

			if used, window := backend.ContextSnapshot(); used != 0 || window != 0 {
				t.Fatalf("ContextSnapshot = (%d,%d), want zeros", used, window)
			}
			if u := backend.LastUsage(); u != nil {
				t.Fatalf("LastUsage = %+v, want nil", u)
			}
			if hit, miss := backend.SessionCache(); hit != 0 || miss != 0 {
				t.Fatalf("SessionCache = (%d,%d), want zeros", hit, miss)
			}
			if got := backend.Jobs(); len(got) != 0 {
				t.Fatalf("Jobs = %+v, want none", got)
			}
			if got := backend.CompactRatio(); got != 0 {
				t.Fatalf("CompactRatio = %v, want 0", got)
			}
		})
	}
}

// TestFollowerNeverPublishesUsage pins "only the session's writer publishes" as
// a property of the call graph: the follower has no publisher, so binding it
// neither creates nor rewrites the channel.
func TestFollowerNeverPublishesUsage(t *testing.T) {
	f := newUsageOwnerFixture(t)
	backend := f.bindFollower(t)
	_ = backend

	if _, err := os.Stat(f.usageFilePath()); !os.IsNotExist(err) {
		t.Fatalf("binding a follower created a usage document: %v", err)
	}

	existing := sampleWriterUsage(time.Now())
	f.publishUsage(t, existing)
	before, err := os.ReadFile(f.usageFilePath())
	if err != nil {
		t.Fatal(err)
	}
	_ = f.bindFollower(t)
	after, err := os.ReadFile(f.usageFilePath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("binding a follower rewrote the writer's usage document")
	}
}

// countingUsageReader records how often the follower went to disk.
type countingUsageReader struct {
	mu    sync.Mutex
	reads int
	doc   team.OwnerUsage
}

func (r *countingUsageReader) ReadUsage(context.Context) (team.OwnerUsage, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	return r.doc, true, nil
}

func (r *countingUsageReader) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads
}

// TestFollowerUsageReadIsThrottled pins the frame-rate requirement at the reader
// boundary: the status band calls these methods on every message, so a burst
// must cost one read, and the next interval must read again.
func TestFollowerUsageReadIsThrottled(t *testing.T) {
	reader := &countingUsageReader{doc: sampleWriterUsage(time.Now())}
	backend, err := newMemberFollowerBackend(
		followerLegacySource{path: "unused.json", stamp: "stem:1"}, event.Discard,
		"lead", "", "", "", "", "", reader,
	)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		backend.ContextSnapshot()
		backend.LastUsage()
		backend.SessionCache()
		backend.Jobs()
		backend.CompactRatio()
	}
	if got := reader.count(); got != 1 {
		t.Fatalf("a read burst cost %d reads, want 1", got)
	}
	backend.usageReadAt = time.Now().Add(-2 * followerUsageReadInterval)
	backend.ContextSnapshot()
	if got := reader.count(); got != 2 {
		t.Fatalf("after the interval the follower read %d times, want 2", got)
	}
}

// TestFollowerUsageReadsLeaveTheFramePath covers the tick's half of the throttle:
// once a host has refreshed the snapshot off the Update goroutine, the band serves
// what that read installed, so drawing a frame costs no stat and no parse. What
// the band renders is still the writer's observation, freshness and TTL included.
func TestFollowerUsageReadsLeaveTheFramePath(t *testing.T) {
	reader := &countingUsageReader{doc: sampleWriterUsage(time.Now())}
	backend, err := newMemberFollowerBackend(
		followerLegacySource{path: "unused.json", stamp: "stem:1"}, event.Discard,
		"lead", "", "", "", "", "", reader,
	)
	if err != nil {
		t.Fatal(err)
	}
	m := newChatTUI(newOwnedTestController(t, control.Options{}), "", make(chan event.Event, 1), 140)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	m = next.(chatTUI)
	m.bindBackend(backend, ownerKey{}, replayInline)

	// A frame before any refresh still reads: the fallback is what a host with no
	// tick gets.
	flush := func() {
		m.ctrl.ContextSnapshot()
		m.ctrl.LastUsage()
		m.ctrl.SessionCache()
		m.ctrl.Jobs()
		m.ctrl.CompactRatio()
	}
	flush()
	if got := reader.count(); got != 1 {
		t.Fatalf("a frame before the tick read the document %d times, want 1", got)
	}

	cmd := m.refreshBoundMemberUsage()
	if cmd == nil {
		t.Fatal("a bound follower's usage must be refreshed by the tick")
	}
	if msg := cmd(); msg != nil {
		t.Fatalf("the refresh must answer no message — the tick that armed it is in flight, got %T", msg)
	}
	if got := reader.count(); got != 2 {
		t.Fatalf("the refresh read the document %d times, want 2 in total", got)
	}

	for range 50 {
		flush()
	}
	if got := reader.count(); got != 2 {
		t.Fatalf("frames read the document %d more times after the refresh, want none", got-2)
	}
	if used, window := m.ctrl.ContextSnapshot(); used != 12000 || window != 128000 {
		t.Fatalf("the band must still render the writer's numbers, got (%d,%d)", used, window)
	}
}

// TestFollowerUsageRefreshHidesAStoppedWriter pins the freshness rule at the new
// boundary too: refreshing installs whatever the document says, and the document's
// own stamp — not who read it — decides whether the band shows it.
func TestFollowerUsageRefreshHidesAStoppedWriter(t *testing.T) {
	reader := &countingUsageReader{doc: sampleWriterUsage(time.Now().Add(-2 * followerUsageTTL))}
	backend, err := newMemberFollowerBackend(
		followerLegacySource{path: "unused.json", stamp: "stem:1"}, event.Discard,
		"lead", "", "", "", "", "", reader,
	)
	if err != nil {
		t.Fatal(err)
	}
	backend.refreshUsage(context.Background())
	if used, window := backend.ContextSnapshot(); used != 0 || window != 0 {
		t.Fatalf("a stale document must still be hidden, got (%d,%d)", used, window)
	}
	if u := backend.LastUsage(); u != nil {
		t.Fatalf("LastUsage = %+v, want nil for a stopped writer", u)
	}
}

// sampleUsageBackend answers the five published surfaces with fixed values, so
// the publisher's mapping is asserted without a provider or a real turn.
type sampleUsageBackend struct {
	control.SessionAPI
}

func (sampleUsageBackend) ContextSnapshot() (int, int) { return 12000, 128000 }
func (sampleUsageBackend) CompactRatio() float64       { return 0.8 }
func (sampleUsageBackend) LastUsage() *provider.Usage {
	return &provider.Usage{PromptTokens: 1000, CacheHitTokens: 700, CacheMissTokens: 300}
}
func (sampleUsageBackend) SessionCache() (int, int) { return 700, 300 }
func (sampleUsageBackend) Jobs() []jobs.View {
	return []jobs.View{{ID: "job-1", Kind: "shell", Label: "build", Status: "running", StartedAt: 42}}
}

// TestWriterPublisherPublishesWithoutARosterTick pins the writer's cadence: the
// publisher owns it, so the document stays fresh even with no team overlay open
// — the case where member backends keep running after the window left the team.
// Close must stop the goroutine before the backend releases its session.
func TestWriterPublisherPublishesWithoutARosterTick(t *testing.T) {
	f := newUsageOwnerFixture(t)
	key := team.OwnerKey{TeamID: f.teamName, MemberID: f.memberID}
	publisher := newMemberUsagePublisher(f.owners, key, sampleUsageBackend{SessionAPI: f.writer.ctrl})
	publisher.Start()
	t.Cleanup(publisher.Close)

	waitForCondition(t, func() bool {
		_, ok, _ := f.owners.ReadUsage(context.Background(), key)
		return ok
	})
	doc, ok, err := f.owners.ReadUsage(context.Background(), key)
	if err != nil || !ok {
		t.Fatalf("ReadUsage = (%+v, %v, %v)", doc, ok, err)
	}
	if doc.ContextUsed != 12000 || doc.ContextWindow != 128000 || doc.CompactRatio != 0.8 {
		t.Fatalf("context fields = %+v, want the writer's own reads", doc)
	}
	if doc.LastTurn == nil || doc.LastTurn.CacheHitTokens != 700 || doc.LastTurn.CacheMissTokens != 300 {
		t.Fatalf("last turn = %+v, want the writer's usage", doc.LastTurn)
	}
	if doc.CacheHit != 700 || doc.CacheMiss != 300 {
		t.Fatalf("session cache = (%d,%d), want (700,300)", doc.CacheHit, doc.CacheMiss)
	}
	if len(doc.Jobs) != 1 || doc.Jobs[0].ID != "job-1" || doc.Jobs[0].Status != "running" {
		t.Fatalf("jobs = %+v, want the writer's running job", doc.Jobs)
	}
	if _, ok := doc.Published(); !ok {
		t.Fatalf("published_at = %q, want a parseable stamp", doc.PublishedAt)
	}

	publisher.Close()
	select {
	case <-publisher.done:
	default:
		t.Fatal("Close must stop the publish goroutine before the backend releases its session")
	}
}

// TestOwnerUsageMappingRoundTrip keeps the two shapes that meet at the wire — the
// published document and provider/jobs display data — from drifting apart.
func TestOwnerUsageMappingRoundTrip(t *testing.T) {
	usage := &provider.Usage{PromptTokens: 11, CompletionTokens: 22, TotalTokens: 33, CacheHitTokens: 44, CacheMissTokens: 55, ReasoningTokens: 66, Unknown: true}
	if got := providerUsageFromLastTurn(ownerUsageLastTurn(usage)); got == nil || *got != *usage {
		t.Fatalf("provider usage round trip = %+v, want %+v", got, usage)
	}
	views := []jobs.View{{ID: "j1", Kind: "shell", Label: "build", Status: "running", StartedAt: 7}}
	if got := jobViewsFromOwnerUsage(ownerUsageJobs(views)); len(got) != 1 || got[0] != views[0] {
		t.Fatalf("job view round trip = %+v, want %+v", got, views)
	}
}

// TestAmbientWriterPublishesForItsOwnMember covers the second writer shape: the
// leader's window shows its own member as a read-only follower, because that
// member's canonical session IS this process's own chat. The owner document and
// the controller count generations independently — the document's is bumped by
// publishes, the controller's by its own history mutations — so the match must be
// on the identity in front of the colon, which this test pins by publishing a
// deliberately different generation.
func TestAmbientWriterPublishesForItsOwnMember(t *testing.T) {
	f := newUsageOwnerFixture(t)
	m, _, _ := clearHistoryFixture(t)
	owners := ownerStoreOf(t, m)
	ambient := f.writer.ctrl
	identity, ok := followerStemIdentity(ambient.HistoryStamp())
	if !ok || identity == "" {
		t.Fatalf("the fixture writer must hold a real history identity, got %q", ambient.HistoryStamp())
	}
	m.ambient = ambient
	m.teamPick.session = sessionState{active: true, teamName: "alpha", current: "lead"}
	publishFollowerIdentity(t, owners, "alpha", "lead", identity+":97")

	m.syncAmbientOwnerUsage(m.boundOwnerFingerprint())
	if m.teamPick.ambientUsage == nil {
		t.Fatal("the window's own chat writes this member; its usage channel must be published")
	}
	key := team.OwnerKey{TeamID: "alpha", MemberID: "lead"}
	waitForCondition(t, func() bool {
		_, ok, _ := owners.ReadUsage(context.Background(), key)
		return ok
	})

	// Unbinding the member stops the ambient publisher: this window is no longer
	// that member's writer.
	m.unbindTeamMember()
	if m.teamPick.ambientUsage != nil {
		t.Fatal("unbinding the member must stop the ambient usage publisher")
	}
}

// TestAmbientWriterStaysQuietForAMemberItDoesNotWrite is the negative half: a
// member whose published identity is another session's keeps an empty channel
// rather than advertising this window's numbers under that member's name.
func TestAmbientWriterStaysQuietForAMemberItDoesNotWrite(t *testing.T) {
	f := newUsageOwnerFixture(t)
	m, _, _ := clearHistoryFixture(t)
	owners := ownerStoreOf(t, m)
	m.ambient = f.writer.ctrl
	m.teamPick.session = sessionState{active: true, teamName: "alpha", current: "lead"}
	publishFollowerIdentity(t, owners, "alpha", "lead", "9e0c63269f7ffcaa57b1603811ecddc4:2")

	m.syncAmbientOwnerUsage(m.boundOwnerFingerprint())
	if m.teamPick.ambientUsage != nil {
		t.Fatal("this window does not write that member; nothing may be published under its name")
	}
	if _, ok, _ := owners.ReadUsage(context.Background(), team.OwnerKey{TeamID: "alpha", MemberID: "lead"}); ok {
		t.Fatal("a foreign member's usage channel must stay empty")
	}
}
