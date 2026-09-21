package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/team"
)

// TestMemberBackendPublishesOwnerHistoryIdentity pins the assembly-time half of
// the cross-window contract: the one point a member backend is built must
// establish the owner's history identity, so a peer window has something to
// compare against. It must NOT advance the generation — assembling a member is
// not a history change, and a bump here would make every peer window reload a
// transcript that did not change.
func TestMemberBackendPublishesOwnerHistoryIdentity(t *testing.T) {
	writeTeamFixture(t, leaderTeam())
	m := openRoster(t)
	owners := ownerStoreOf(t, m)
	name, err := team.MemberSessionFile("alpha", "lead")
	if err != nil {
		t.Fatal(err)
	}
	// A member with a transcript on disk, so assembly resumes a real history and
	// the published identity has a name to carry. The empty-stem case (a brand-new
	// legacy member, whose stamp is "" until the first save) is a store unit test.
	workspace := t.TempDir()
	seedMemberSession(t, filepath.Join(config.ProjectSessionDir(workspace), name))
	deps := memberBackendDeps{
		ctx: t.Context(), owners: owners,
		users: fakePool{users: map[string]team.AgentUser{
			"u": {UserID: "u", Provider: "openai", Model: "gpt-5.6",
				BaseURL: "https://example.invalid/v1", APIKey: "k"},
		}},
		events:        make(chan memberEvent, memberEventBuffer),
		workspaceRoot: workspace,
		base: func() boot.Options {
			return boot.Options{SessionDir: t.TempDir(), Stderr: io.Discard}
		},
	}
	backend, err := newMemberBackendBuilder(deps)(team.MemberBinding{
		Team: "alpha", MemberID: "lead", Leader: true, AgentUserRef: "u", SessionFile: name,
	})
	if err != nil {
		t.Fatalf("assembling the member: %v", err)
	}
	t.Cleanup(backend.Close)

	meta, err := owners.Meta(team.OwnerKey{TeamID: "alpha", MemberID: "lead"})
	if err != nil {
		t.Fatalf("owner meta after assembly: %v", err)
	}
	if meta.History.Generation != 0 {
		t.Fatalf("assembly must not bump the generation, got %d", meta.History.Generation)
	}
	// The builder hands back the driving port; the identity is the narrower
	// observation slice, which is what the publication path asserts for.
	stamper, ok := backend.(memberHistoryStamper)
	if !ok {
		t.Fatalf("an assembled member backend must expose its history identity, got %T", backend)
	}
	if meta.History.Stem == "" || meta.History.Stem != stamper.HistoryStamp() {
		t.Fatalf("the published stem = %q, want the backend's own stamp %q", meta.History.Stem, stamper.HistoryStamp())
	}
	// The identity a peer polls must be readable without the owner's metadata
	// lock and must agree with the document it was published in.
	fingerprint, err := owners.Fingerprint(team.OwnerKey{TeamID: "alpha", MemberID: "lead"})
	if err != nil {
		t.Fatalf("owner fingerprint: %v", err)
	}
	if !fingerprint.Present || fingerprint.Stem != meta.History.Stem || fingerprint.Generation != 0 {
		t.Fatalf("fingerprint = %+v, want the published identity %+v", fingerprint, meta.History)
	}
}

// TestRecordMemberOwnerHistoryReportsFailure pins the propagation: a member
// whose identity cannot be published must surface a named error rather than
// binding anyway, because a member that runs with no observable identity is
// exactly the case a peer window cannot sync with. The assertion is at the
// record call rather than through the builder: adoption writes to the same
// owner store first, so a broken store fails there and would mask this half.
func TestRecordMemberOwnerHistoryReportsFailure(t *testing.T) {
	root := t.TempDir()
	owners, err := team.NewOwnerStore(root)
	if err != nil {
		t.Fatal(err)
	}
	// A regular file where the team's owner directory belongs: every owner write
	// now fails with ENOTDIR, deterministically.
	if err := os.WriteFile(filepath.Join(root, "alpha"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = recordMemberOwnerHistory(t.Context(), owners,
		team.MemberBinding{Team: "alpha", MemberID: "lead"}, staticHistoryStamp("stem"), true)
	if err == nil {
		t.Fatal("an unpublished identity must report the failure, not bind anyway")
	}
	if !strings.Contains(err.Error(), "lead") {
		t.Fatalf("the failure must name the member it belongs to, got %v", err)
	}
}

// staticHistoryStamp is a member backend's identity slice with no session
// behind it, so a publication failure can be driven without a controller.
type staticHistoryStamp string

func (s staticHistoryStamp) HistoryStamp() string { return string(s) }

// TestAmbientSeedRepublishesOwnerIdentity pins the identity a fresh member bind
// must leave behind. A leader's first entry is seeded from the chat's history,
// which REPLACES the transcript the bind just resolved and advances the session
// it runs, so the publication made before the seed describes a state the member
// no longer runs. The owner document is what a peer window compares and what a
// later bind reads, so it must name the post-seed state.
//
// The session id survives the seed (the projection is replaced in place); the
// generation does not. Publishing before the seed therefore leaves every peer
// believing the member's history changed the moment it was bound.
func TestAmbientSeedRepublishesOwnerIdentity(t *testing.T) {
	root := t.TempDir()
	owners, err := team.NewOwnerStore(filepath.Join(root, "team"))
	if err != nil {
		t.Fatal(err)
	}
	const teamName, memberID, sessionFile = "alpha", "leader-agent", "team-alpha-leader-agent.json"
	key := team.OwnerKey{TeamID: teamName, MemberID: memberID}
	paths, _, err := owners.Init(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	store, err := session.NewService("local", session.NewFilesystemPersistence(filepath.Join(root, "sessions-v4")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.CloseAll(context.Background()) })
	dir := t.TempDir()
	ctrl := control.New(control.Options{
		Executor:   agent.New(nil, nil, agent.NewSession("member-sys"), agent.Options{}, event.Discard),
		SessionDir: dir, Label: memberID, SystemPrompt: "member-sys",
		DisableColdResumePrune: true, Sink: event.Discard,
		SessionService: store, ExclusiveSession: true,
	})
	t.Cleanup(ctrl.Close)
	if _, err := ctrl.BindFreshSession(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	binding := team.MemberBinding{Team: teamName, MemberID: memberID, SessionFile: sessionFile}
	// The builder's pre-seed publication.
	if err := recordMemberOwnerHistory(context.Background(), owners, binding, ctrl, false); err != nil {
		t.Fatal(err)
	}
	// The seed: the chat's conversation replaces this member's transcript.
	carry := leaderAmbientCarry([]provider.Message{
		{Role: provider.RoleSystem, Content: "You are Reasonix, a coding agent."},
		{Role: provider.RoleUser, Content: "do the thing"},
		{Role: provider.RoleAssistant, Content: "done"},
	}, ctrl.History())
	if len(carry) == 0 {
		t.Fatal("the fixture must produce a carry, or the seed is a no-op")
	}
	ctrl.AdoptHistory(carry, paths.Transcript)
	if err := ctrl.Snapshot(); err != nil {
		t.Fatal(err)
	}
	// The builder's post-seed publication.
	if err := recordMemberOwnerHistory(context.Background(), owners, binding, ctrl, false); err != nil {
		t.Fatal(err)
	}

	published, err := owners.Fingerprint(key)
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := followerStemIdentity(published.Stem)
	ref, hasRef := ctrl.SessionRef()
	if !ok || !hasRef {
		t.Fatalf("the fixture must publish a real identity (stem=%q ref=%v)", published.Stem, hasRef)
	}
	if identity != ref.SessionID {
		t.Fatalf("owner publishes session %q but the member runs %q", identity, ref.SessionID)
	}
	// The generation is the half the pre-seed publication gets wrong: the seed
	// advanced the session, and the document must say so.
	if want := ctrl.HistoryStamp(); published.Stem != want {
		t.Fatalf("owner publishes %q but the member's history identity is %q", published.Stem, want)
	}
}

// TestMemberBindTakesOverThePublishedOwnerSession pins plan A at the boundary
// the host consumes: a member whose owner document names a readable session but
// whose directory holds no transcript must come back from the bind WRITABLE, on
// that published identity. Without the takeover the bind takes the first-entry
// branch, seeds a new session and leaves the owner stale forever — the member is
// then read-only with no writer anywhere, which is the state this repairs.
func TestMemberBindTakesOverThePublishedOwnerSession(t *testing.T) {
	root := t.TempDir()
	owners, err := team.NewOwnerStore(filepath.Join(root, "team"))
	if err != nil {
		t.Fatal(err)
	}
	const teamName, memberID, sessionFile = "alpha", "leader-agent", "team-alpha-leader-agent.json"
	key := team.OwnerKey{TeamID: teamName, MemberID: memberID}
	paths, _, err := owners.Init(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	store, err := session.NewService("local", session.NewFilesystemPersistence(filepath.Join(root, "sessions-v4")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.CloseAll(context.Background()) })
	newCtrl := func() *control.Controller {
		ctrl := control.New(control.Options{
			Executor:   agent.New(nil, nil, agent.NewSession("member-sys"), agent.Options{}, event.Discard),
			SessionDir: t.TempDir(), Label: memberID, SystemPrompt: "member-sys",
			DisableColdResumePrune: true, Sink: event.Discard,
			SessionService: store, ExclusiveSession: true,
		})
		t.Cleanup(ctrl.Close)
		return ctrl
	}
	// A published session holding history, exactly as a previous leader seed left it.
	seeder := newCtrl()
	if _, err := seeder.BindFreshSession(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	seeder.AdoptHistory([]provider.Message{
		{Role: provider.RoleSystem, Content: "You are Reasonix, a coding agent."},
		{Role: provider.RoleUser, Content: "the task"},
		{Role: provider.RoleAssistant, Content: "done"},
	}, paths.Transcript)
	if err := seeder.Snapshot(); err != nil {
		t.Fatal(err)
	}
	seeded, ok := seeder.SessionRef()
	if !ok {
		t.Fatal("the seeder must publish an identity")
	}
	binding := team.MemberBinding{Team: teamName, MemberID: memberID, SessionFile: sessionFile}
	if err := recordMemberOwnerHistory(context.Background(), owners, binding, seeder, false); err != nil {
		t.Fatal(err)
	}
	// The state under test: the member's own directory holds no transcript.
	if _, err := os.Stat(paths.Transcript); !os.IsNotExist(err) {
		t.Fatalf("the fixture must leave the owner directory without a transcript, got %v", err)
	}

	next := newCtrl()
	deps := memberBackendDeps{ctx: context.Background(), owners: owners, events: make(chan memberEvent, 4)}
	_, fresh, follower, err := bindMemberOwnerSession(deps, next, binding)
	if err != nil || follower != nil {
		t.Fatalf("bind returned a read-only follower (err=%v), want the member bound writable", err)
	}
	if fresh {
		t.Fatal("taking over the published session is not a first entry; the owner already had history")
	}
	ref, ok := next.SessionRef()
	if !ok || ref.SessionID != seeded.SessionID {
		t.Fatalf("bound session = %v, want the published %v", ref, seeded.SessionID)
	}
	if got := next.History(); len(got) == 0 {
		t.Fatal("the takeover must bring the member's history with it, not an empty session")
	}
}

// TestOwnerSessionTakeoverRefusesWithoutAReadableOwner is the refusal half: an
// owner with no identity published, or naming a session that is gone, is a
// member whose history is missing. Binding a fresh session under that identity
// would fabricate a member the operator never had, so the historical first-entry
// path must stay in charge.
func TestOwnerSessionTakeoverRefusesWithoutAReadableOwner(t *testing.T) {
	root := t.TempDir()
	owners, err := team.NewOwnerStore(filepath.Join(root, "team"))
	if err != nil {
		t.Fatal(err)
	}
	const teamName, memberID, sessionFile = "alpha", "leader-agent", "team-alpha-leader-agent.json"
	key := team.OwnerKey{TeamID: teamName, MemberID: memberID}
	paths, _, err := owners.Init(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	store, err := session.NewService("local", session.NewFilesystemPersistence(filepath.Join(root, "sessions-v4")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.CloseAll(context.Background()) })
	ctrl := control.New(control.Options{
		Executor:   agent.New(nil, nil, agent.NewSession("member-sys"), agent.Options{}, event.Discard),
		SessionDir: t.TempDir(), Label: memberID, SystemPrompt: "member-sys",
		DisableColdResumePrune: true, Sink: event.Discard,
		SessionService: store, ExclusiveSession: true,
	})
	t.Cleanup(ctrl.Close)
	binding := team.MemberBinding{Team: teamName, MemberID: memberID, SessionFile: sessionFile}

	for _, tc := range []struct {
		name string
		stem string
	}{
		{"no owner identity published", ""},
		{"owner names a session that does not exist", "0123456789abcdef01234567:3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.stem != "" {
				if err := owners.BumpHistory(context.Background(), key, tc.stem, false); err != nil {
					t.Fatal(err)
				}
			}
			took, err := ownerSessionTakeover(ctrl, owners, binding, paths.Dir)
			if err != nil {
				t.Fatalf("refusal must be quiet, got %v", err)
			}
			if took {
				t.Fatal("an owner with no readable history must not be taken over")
			}
		})
	}
}
