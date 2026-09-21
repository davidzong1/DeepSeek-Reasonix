package cli

// Focused tests for the read-only team-session follower (F1a). Each drives the
// real bind path rather than the adapter in isolation: the defect being fixed is
// the bind decision, not the adapter's method bodies.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/sandbox"
	"reasonix/internal/session"
	"reasonix/internal/team"

	tea "charm.land/bubbletea/v2"
)

// followerWriter owns one member's session the way a live writer runtime does:
// a v3-exclusive controller that has imported the member's transcript and holds
// the store's writer lease until the test ends.
type followerWriter struct {
	ctrl      *control.Controller
	ref       session.SessionRef
	path      string
	storeRoot string
}

// newFollowerWriter seeds the member's transcript at sourcePath and imports it
// into a fresh final-format store, keeping the importing controller alive: the
// lease is held for as long as the writer does, which is what the follower must
// contend with.
//
// sourcePath must be the path the follower's own probe will resolve — the
// member's canonical owner transcript — because the import records the mapping
// from that source to the published session identity. A second import of the
// same source therefore resolves the SAME identity and contends for it, which is
// exactly the production shape; importing from a different copy would publish a
// fresh identity and test nothing.
func newFollowerWriter(t *testing.T, root, sourcePath string) followerWriter {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o700); err != nil {
		t.Fatal(err)
	}
	seedMemberSession(t, sourcePath)

	storeRoot := filepath.Join(root, "sessions-v4")
	store, err := session.NewService("local", session.NewFilesystemPersistence(storeRoot))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.CloseAll(context.Background()) })
	exec := agent.New(nil, nil, agent.NewSession("member-sys"), agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{
		Executor: exec, SessionDir: filepath.Dir(sourcePath), Label: "writer",
		SystemPrompt: "member-sys", DisableColdResumePrune: true, Sink: event.Discard,
		SessionService: store, ExclusiveSession: true,
	})
	t.Cleanup(ctrl.Close)
	ref, err := ctrl.ContinueLegacySession(context.Background(), sourcePath, "")
	if err != nil {
		t.Fatalf("the writer must own the member session: %v", err)
	}
	if _, err := os.Stat(filepath.Join(storeRoot, ref.SessionID)); err != nil {
		t.Fatalf("the writer's session directory must exist: %v", err)
	}
	return followerWriter{ctrl: ctrl, ref: ref, path: sourcePath, storeRoot: storeRoot}
}

// publishFollowerIdentity records the writer's identity in the member's owner
// metadata, exactly as the production assembly does, so the follower has a stem
// to follow.
func publishFollowerIdentity(t *testing.T, owners *team.OwnerStore, teamName, memberID, stem string) {
	t.Helper()
	key := team.OwnerKey{TeamID: teamName, MemberID: memberID}
	if _, _, err := owners.Init(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if err := owners.BumpHistory(context.Background(), key, stem, false); err != nil {
		t.Fatal(err)
	}
}

// followerBindAttempt drives the member builder's own bind sequence against a
// writer that already owns the member, and reports what the builder returns. It
// mirrors the production order — bindMemberSession, then the write-authority
// lease — because the two axes contend at different steps: a v3-exclusive member
// is refused at import, a legacy one only when it asks for write authority.
//
// The attempt opens its OWN session service over the writer's store root on the
// v3 axis: that is what makes it a second process rather than a second
// controller, and the only shape in which the store refuses with ErrWriterOwned.
func followerBindAttempt(t *testing.T, owners *team.OwnerStore, storeRoot, teamName, memberID, sessionFile, ownerDir string, exclusive bool) (control.SessionAPI, error) {
	t.Helper()
	dir := t.TempDir()
	var store *session.Service
	if storeRoot != "" {
		var err error
		store, err = session.NewService("local", session.NewFilesystemPersistence(storeRoot))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.CloseAll(context.Background()) })
	}
	exec := agent.New(nil, nil, agent.NewSession("member-sys"), agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{
		Executor: exec, SessionDir: dir, Label: memberID,
		SystemPrompt: "member-sys", DisableColdResumePrune: true, Sink: event.Discard,
		SessionService: store, ExclusiveSession: exclusive,
	})
	roots := []string{ownerDir, dir}
	deps := memberBackendDeps{ctx: context.Background(), owners: owners, events: make(chan memberEvent, memberEventBuffer)}
	binding := team.MemberBinding{Team: teamName, MemberID: memberID, SessionFile: sessionFile}

	path, _, bindErr := bindMemberSession(ctrl, sessionFile, roots, ownerDir)
	if bindErr == nil {
		_, bindErr = bindMemberSessionAuthority(ctrl, path, true)
	}
	if bindErr == nil {
		ctrl.Close()
		t.Fatal("the writer owns the member; the plain bind must not succeed")
	}
	if !isMemberWriterContention(bindErr) {
		ctrl.Close()
		t.Fatalf("the second bind must report writer contention, got %v", bindErr)
	}
	return newMemberFollower(deps, ctrl, binding, bindErr)
}

// TestFollowerAttachesWhenWriterOwnsSession is the F1a acceptance test on the
// v3 axis: the second runtime cannot import the member's session (the writer
// holds it), so it attaches a read-only follower that renders the SAME
// committed transcript — the member is visible and readable, not unavailable.
func TestFollowerAttachesWhenWriterOwnsSession(t *testing.T) {
	root := t.TempDir()
	owners, err := team.NewOwnerStore(filepath.Join(root, "team"))
	if err != nil {
		t.Fatal(err)
	}
	const sessionFile = "team-alpha-lead.json"
	paths, err := owners.Paths(team.OwnerKey{TeamID: "alpha", MemberID: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	writer := newFollowerWriter(t, root, paths.Transcript)
	publishFollowerIdentity(t, owners, "alpha", "lead", writer.ctrl.HistoryStamp())
	backend, err := followerBindAttempt(t, owners, writer.storeRoot, "alpha", "lead", sessionFile, paths.Dir, true)
	if err != nil {
		t.Fatalf("a contended member must attach a follower, got %v", err)
	}
	defer backend.Close()

	// The follower reads the writer's own committed history: the same messages
	// the writer imported, verbatim.
	got := backend.History()
	var sawUser, sawReply bool
	for _, msg := range got {
		switch msg.Content {
		case "remembered user":
			sawUser = true
		case "remembered reply":
			sawReply = true
		}
	}
	if !sawUser || !sawReply {
		t.Fatalf("the follower must read the writer's history, got %+v", got)
	}
	// The identity it reports is the writer's, so a peer window comparing stamps
	// sees one history rather than two.
	if stamp := backend.HistoryStamp(); stamp == "" || stamp != writer.ctrl.HistoryStamp() {
		t.Fatalf("follower stamp = %q, want the writer's %q", stamp, writer.ctrl.HistoryStamp())
	}
	if status := backend.RuntimeStatus(); status.Running {
		t.Fatal("a follower drives no turn of its own; its status must read idle")
	}
}

// TestFollowerRefusesEveryMutationPath pins the read-only half: submit, clear,
// approve and branch all refuse with the follower's sentinel, and none of them
// reaches the writer's session.
func TestFollowerRefusesEveryMutationPath(t *testing.T) {
	root := t.TempDir()
	owners, err := team.NewOwnerStore(filepath.Join(root, "team"))
	if err != nil {
		t.Fatal(err)
	}
	const sessionFile = "team-alpha-lead.json"
	paths, err := owners.Paths(team.OwnerKey{TeamID: "alpha", MemberID: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	writer := newFollowerWriter(t, root, paths.Transcript)
	publishFollowerIdentity(t, owners, "alpha", "lead", writer.ctrl.HistoryStamp())
	backend, err := followerBindAttempt(t, owners, writer.storeRoot, "alpha", "lead", sessionFile, paths.Dir, true)
	if err != nil {
		t.Fatalf("a contended member must attach a follower, got %v", err)
	}
	defer backend.Close()

	// submit
	if err := backend.SubmitUserTurnOrError("hello", "hello"); !errors.Is(err, errFollowerReadOnly) {
		t.Fatalf("SubmitUserTurnOrError = %v, want the read-only refusal", err)
	}
	if err := backend.Run(context.Background(), "hello"); !errors.Is(err, errFollowerReadOnly) {
		t.Fatalf("Run = %v, want the read-only refusal", err)
	}
	// clear
	if err := backend.ClearSession(); !errors.Is(err, errFollowerReadOnly) {
		t.Fatalf("ClearSession = %v, want the read-only refusal", err)
	}
	if err := backend.NewSession(); !errors.Is(err, errFollowerReadOnly) {
		t.Fatalf("NewSession = %v, want the read-only refusal", err)
	}
	// approve
	if err := backend.ResolveApproval("id", true, sandbox.ApprovalScopeOnce); !errors.Is(err, errFollowerReadOnly) {
		t.Fatalf("ResolveApproval = %v, want the read-only refusal", err)
	}
	if _, err := backend.Ask(context.Background(), nil); !errors.Is(err, errFollowerReadOnly) {
		t.Fatalf("Ask = %v, want the read-only refusal", err)
	}
	// branch
	if _, err := backend.Branch("named"); !errors.Is(err, errFollowerReadOnly) {
		t.Fatalf("Branch = %v, want the read-only refusal", err)
	}
	if _, err := backend.Fork(0); !errors.Is(err, errFollowerReadOnly) {
		t.Fatalf("Fork = %v, want the read-only refusal", err)
	}
	if _, err := backend.SwitchBranch("main"); !errors.Is(err, errFollowerReadOnly) {
		t.Fatalf("SwitchBranch = %v, want the read-only refusal", err)
	}
}

// memberBindAttempt drives the production member-assembly bind sequence against
// a writer that already owns the member, and reports what the builder returns —
// including the stale-import case, where the bind SUCCEEDS on an identity the
// owner has already rotated away from and must still not become a writable
// backend.
//
// It mirrors newMemberBackendBuilder's order exactly (bindMemberSession, then
// the stale-import guard, then the write-authority lease) rather than
// followerBindAttempt's, which stops at the contention assertion: after a clear
// there is no contention to assert, and the whole point of the case is that the
// plain bind returns nil.
func memberBindAttempt(t *testing.T, owners *team.OwnerStore, storeRoot, teamName, memberID, sessionFile, ownerDir string) (control.SessionAPI, error) {
	t.Helper()
	dir := t.TempDir()
	store, err := session.NewService("local", session.NewFilesystemPersistence(storeRoot))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.CloseAll(context.Background()) })
	exec := agent.New(nil, nil, agent.NewSession("member-sys"), agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{
		Executor: exec, SessionDir: dir, Label: memberID,
		SystemPrompt: "member-sys", DisableColdResumePrune: true, Sink: event.Discard,
		SessionService: store, ExclusiveSession: true,
	})
	deps := memberBackendDeps{ctx: context.Background(), owners: owners, events: make(chan memberEvent, memberEventBuffer)}
	binding := team.MemberBinding{Team: teamName, MemberID: memberID, SessionFile: sessionFile}

	path, _, bindErr := bindMemberSession(ctrl, sessionFile, []string{ownerDir, dir}, ownerDir)
	if bindErr != nil {
		if !isMemberWriterContention(bindErr) {
			ctrl.Close()
			return nil, bindErr
		}
		return newMemberFollower(deps, ctrl, binding, bindErr)
	}
	if staleMemberImport(deps.owners, binding, ctrl, path) {
		return newMemberFollower(deps, ctrl, binding, errMemberStaleImport)
	}
	if _, err := bindMemberSessionAuthority(ctrl, path, true); err != nil {
		if isMemberWriterContention(err) {
			return newMemberFollower(deps, ctrl, binding, err)
		}
		ctrl.Close()
		return nil, err
	}
	return memberLeasedBackend{SessionAPI: ctrl}, nil
}

// TestFollowerReplacesStaleImportAfterWriterClear is the clear-rotation
// regression: a writer clears its session, which rotates the live identity from
// sidA to sidB and republishes the owner stem — but the legacy transcript the
// original import was frozen from is left in place and unchanged. Re-importing
// it is deterministic (the migration target is derived from the source path and
// digest), so a second process reproduces sidA and its import SUCCEEDS.
//
// The published stem is the authority, so that second process must get a
// read-only follower on sidB — never a writable backend executing a session the
// owner has already left.
func TestFollowerReplacesStaleImportAfterWriterClear(t *testing.T) {
	root := t.TempDir()
	owners, err := team.NewOwnerStore(filepath.Join(root, "team"))
	if err != nil {
		t.Fatal(err)
	}
	const sessionFile = "team-alpha-lead.json"
	paths, err := owners.Paths(team.OwnerKey{TeamID: "alpha", MemberID: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	writer := newFollowerWriter(t, root, paths.Transcript)
	staleID := writer.ref.SessionID
	publishFollowerIdentity(t, owners, "alpha", "lead", writer.ctrl.HistoryStamp())

	// The rotation: the writer's live session becomes a fresh one, the owner
	// identity is republished, and the pre-rotation session is deleted from the
	// store — while the legacy transcript the import was frozen from stays put.
	if err := writer.ctrl.ClearSession(); err != nil {
		t.Fatalf("the writer must be able to clear its own session: %v", err)
	}
	current := writer.ctrl.HistoryStamp()
	if current == "" {
		t.Fatal("the writer must have a live identity after its clear")
	}
	publishFollowerIdentity(t, owners, "alpha", "lead", current)
	if got, ok := writer.ctrl.SessionRef(); !ok || got.SessionID == staleID {
		t.Fatalf("the clear must rotate the writer's identity, got %+v (was %q)", got, staleID)
	}
	if _, err := os.Stat(paths.Transcript); err != nil {
		t.Fatalf("precondition: the frozen import source must still be on disk: %v", err)
	}

	backend, err := memberBindAttempt(t, owners, writer.storeRoot, "alpha", "lead", sessionFile, paths.Dir)
	if err != nil {
		t.Fatalf("a stale-import member must attach a follower, got %v", err)
	}
	defer backend.Close()

	if _, ok := backend.(*memberFollowerBackend); !ok {
		t.Fatalf("a stale import must never be handed back writable, got %T", backend)
	}
	if stamp := backend.HistoryStamp(); stamp != current {
		t.Fatalf("follower stamp = %q, want the owner's current stem %q", stamp, current)
	}
	// The identity it reports names the post-rotation session, not the one the
	// re-import reproduced.
	if strings.HasPrefix(backend.HistoryStamp(), staleID+":") {
		t.Fatalf("the follower reports the pre-rotation identity %q", backend.HistoryStamp())
	}
	if err := backend.ClearSession(); !errors.Is(err, errFollowerReadOnly) {
		t.Fatalf("ClearSession = %v, want the read-only refusal", err)
	}
	if err := backend.SubmitUserTurnOrError("hello", "hello"); !errors.Is(err, errFollowerReadOnly) {
		t.Fatalf("SubmitUserTurnOrError = %v, want the read-only refusal", err)
	}
	// The writer keeps the rotated session's lease: the follower attach and its
	// close must not move ownership of the identity the writer now runs.
	assertWriterStillOwned(t, writer.storeRoot, sessionRefOf(t, writer.ctrl))
}

// sessionRefOf is the writer's current immutable identity, which the lease
// assertions must name rather than the pre-rotation one.
func sessionRefOf(t *testing.T, ctrl *control.Controller) session.SessionRef {
	t.Helper()
	ref, ok := ctrl.SessionRef()
	if !ok {
		t.Fatal("the writer must expose its current session identity")
	}
	return ref
}

// TestFollowerLeavesWriterLeaseIntact pins the other half of the contract: the
// writer keeps its lease and keeps appending while the follower reads, and the
// follower never becomes a second writer.
func TestFollowerLeavesWriterLeaseIntact(t *testing.T) {
	root := t.TempDir()
	owners, err := team.NewOwnerStore(filepath.Join(root, "team"))
	if err != nil {
		t.Fatal(err)
	}
	const sessionFile = "team-alpha-lead.json"
	paths, err := owners.Paths(team.OwnerKey{TeamID: "alpha", MemberID: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	writer := newFollowerWriter(t, root, paths.Transcript)
	publishFollowerIdentity(t, owners, "alpha", "lead", writer.ctrl.HistoryStamp())
	backend, err := followerBindAttempt(t, owners, writer.storeRoot, "alpha", "lead", sessionFile, paths.Dir, true)
	if err != nil {
		t.Fatalf("a contended member must attach a follower, got %v", err)
	}
	defer backend.Close()

	// A third, independent runtime must still be refused the writer lease: the
	// probe opens its own service over the same store root, the only shape that
	// exercises the cross-process ownership lock.
	assertWriterStillOwned(t, writer.storeRoot, writer.ref)
	// The writer is still the writer: its own read surface is unchanged.
	if got := writer.ctrl.History(); len(got) == 0 {
		t.Fatal("the writer must keep its own transcript after a follower attached")
	}
	// Closing the follower must not release anything the writer holds, so the
	// lease is still refused afterwards.
	backend.Close()
	assertWriterStillOwned(t, writer.storeRoot, writer.ref)
}

// assertWriterStillOwned opens a fresh service over the writer's store root and
// requires the session's write handle to be refused: a second runtime, a follower
// attach and a follower close must none of them move the writer's ownership.
func assertWriterStillOwned(t *testing.T, storeRoot string, ref session.SessionRef) {
	t.Helper()
	probe, err := session.NewService("local", session.NewFilesystemPersistence(storeRoot))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.CloseAll(context.Background()) }()
	if _, err := probe.Open(context.Background(), ref); !errors.Is(err, session.ErrWriterOwned) {
		t.Fatalf("the writer's lease must be intact, Open = %v", err)
	}
	// The read half stays available to that same third runtime: read-only
	// followers are the whole point, so contention must not block reading.
	if _, err := probe.Query().History(context.Background(), ref); err != nil {
		t.Fatalf("a cold read must stay available while the writer owns the session: %v", err)
	}
}

// TestFollowerRefusedWithoutReadableIdentity pins fail-closed: a member whose
// owner metadata publishes no readable stem must stay visibly unavailable. A
// follower that invented an empty transcript would look like a member whose
// history was cleared.
func TestFollowerRefusedWithoutReadableIdentity(t *testing.T) {
	root := t.TempDir()
	owners, err := team.NewOwnerStore(filepath.Join(root, "team"))
	if err != nil {
		t.Fatal(err)
	}
	const sessionFile = "team-alpha-lead.json"
	key := team.OwnerKey{TeamID: "alpha", MemberID: "lead"}
	// Init creates the owner directory but no identity: BumpHistory never runs,
	// so the stem stays empty and there is nothing to follow.
	if _, _, err := owners.Init(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	paths, err := owners.Paths(key)
	if err != nil {
		t.Fatal(err)
	}
	writer := newFollowerWriter(t, root, paths.Transcript)
	if _, err := followerBindAttempt(t, owners, writer.storeRoot, "alpha", "lead", sessionFile, paths.Dir, true); err == nil {
		t.Fatal("a member with no readable identity must stay unavailable")
	}
	// A nil owner store is a host without canonical storage: no follower either.
	if _, err := followerBindAttempt(t, nil, writer.storeRoot, "alpha", "lead", sessionFile, paths.Dir, true); err == nil {
		t.Fatal("a host without owner storage must not attach a follower")
	}
}

// TestFollowerBindsIntoWindowAndRefusesSubmit is the window-level half: the
// follower is a real backend the TUI binds, so the member renders its
// transcript instead of "member unavailable" — and a submission through the
// bound window refuses visibly rather than looking like a turn that did
// nothing.
func TestFollowerBindsIntoWindowAndRefusesSubmit(t *testing.T) {
	root := t.TempDir()
	owners, err := team.NewOwnerStore(filepath.Join(root, "team"))
	if err != nil {
		t.Fatal(err)
	}
	const sessionFile = "team-alpha-lead.json"
	paths, err := owners.Paths(team.OwnerKey{TeamID: "alpha", MemberID: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	writer := newFollowerWriter(t, root, paths.Transcript)
	publishFollowerIdentity(t, owners, "alpha", "lead", writer.ctrl.HistoryStamp())

	backend, err := followerBindAttempt(t, owners, writer.storeRoot, "alpha", "lead", sessionFile, paths.Dir, true)
	if err != nil {
		t.Fatalf("a contended member must attach a follower, got %v", err)
	}
	defer backend.Close()

	ctrl := control.New(control.Options{})
	t.Cleanup(ctrl.Close)
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(chatTUI)
	m.bindBackend(backend, ownerKey{})

	if got := m.ctrl.History(); len(got) == 0 {
		t.Fatal("binding the follower must render the member's transcript, not an empty window")
	}
	// The window must not treat the follower as a live member backend it can
	// drive: a submission through it reports the read-only refusal.
	if err := m.ctrl.SubmitUserTurnOrError("hello", "hello"); !errors.Is(err, errFollowerReadOnly) {
		t.Fatalf("a submit through the bound follower = %v, want the read-only refusal", err)
	}
}

// TestFollowerAttachesOnLegacyAxis pins the other axis: a legacy member session
// has no store to import through, so contention surfaces when this runtime asks
// for write authority on the transcript the writer already holds. The follower
// must attach there too, reading the same file rather than reporting the member
// unavailable.
func TestFollowerAttachesOnLegacyAxis(t *testing.T) {
	root := t.TempDir()
	owners, err := team.NewOwnerStore(filepath.Join(root, "team"))
	if err != nil {
		t.Fatal(err)
	}
	const sessionFile = "team-alpha-lead.json"
	paths, err := owners.Paths(team.OwnerKey{TeamID: "alpha", MemberID: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	seedMemberSession(t, paths.Transcript)

	// The writer is a legacy controller holding the transcript's session lease.
	dir := filepath.Dir(paths.Transcript)
	writer := control.New(control.Options{
		Executor:   agent.New(nil, nil, agent.NewSession("member-sys"), agent.Options{}, event.Discard),
		SessionDir: dir, Label: "writer", SystemPrompt: "member-sys",
		DisableColdResumePrune: true, Sink: event.Discard,
	})
	t.Cleanup(writer.Close)
	loaded, err := loadResumableSession(paths.Transcript)
	if err != nil {
		t.Fatal(err)
	}
	writer.Resume(loaded, paths.Transcript)
	wl, err := bindMemberSessionAuthority(writer, paths.Transcript, true)
	if err != nil {
		t.Fatalf("the writer must hold the transcript lease: %v", err)
	}
	t.Cleanup(wl.Close)

	// The stem is the writer's legacy stamp; publish it as the member identity.
	publishFollowerIdentity(t, owners, "alpha", "lead", writer.HistoryStamp())

	backend, err := followerBindAttempt(t, owners, "", "alpha", "lead", sessionFile, paths.Dir, false)
	if err != nil {
		t.Fatalf("a legacy-contended member must attach a follower, got %v", err)
	}
	defer backend.Close()

	var sawUser bool
	for _, msg := range backend.History() {
		if msg.Content == "remembered user" {
			sawUser = true
		}
	}
	if !sawUser {
		t.Fatal("the legacy follower must read the writer's transcript")
	}
	if err := backend.ClearSession(); !errors.Is(err, errFollowerReadOnly) {
		t.Fatalf("ClearSession = %v, want the read-only refusal", err)
	}
	// The writer's lease survived: a second authority request is still refused.
	if _, err := bindMemberSessionAuthority(control.New(control.Options{
		Executor:   agent.New(nil, nil, agent.NewSession("member-sys"), agent.Options{}, event.Discard),
		SessionDir: dir, Label: "probe", SystemPrompt: "member-sys", Sink: event.Discard,
	}), paths.Transcript, true); err == nil {
		t.Fatal("the writer's lease must survive the legacy follower attach")
	}
}
