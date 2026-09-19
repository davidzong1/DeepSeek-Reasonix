package team

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"reasonix/internal/filelock"
)

func newTestSessionStore(t *testing.T) *TeamSessionStore {
	t.Helper()
	s, err := NewTeamSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSessionStoreRejectsEscapingKeys(t *testing.T) {
	s := newTestSessionStore(t)
	for _, tc := range []struct {
		team, member string
	}{
		{"", "m"},
		{"t", ""},
		{"a/b", "m"},
		{"t", "m/../x"},
		{"t", ".."},
		{"t", "."},
		{"t\x00", "m"},
	} {
		if _, err := s.MemberDir(tc.team, tc.member); !errors.Is(err, ErrInvalidSessionKey) {
			t.Fatalf("MemberDir(%q,%q) err = %v, want ErrInvalidSessionKey", tc.team, tc.member, err)
		}
		if err := s.AppendMessage(tc.team, tc.member, SessionMessage{Kind: "user", Text: "x"}); !errors.Is(err, ErrInvalidSessionKey) {
			t.Fatalf("AppendMessage(%q,%q) err = %v, want ErrInvalidSessionKey", tc.team, tc.member, err)
		}
	}
}

func TestSessionStoreMemberPathStaysUnderContextRoot(t *testing.T) {
	s := newTestSessionStore(t)
	dir, err := s.MemberDir("team-a", "coder-1")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.ToSlash(filepath.Join("context", "team-a", "coder-1"))
	if dir != want {
		t.Fatalf("MemberDir = %q, want %q", dir, want)
	}
}

func TestSessionStoreMessagesLazyCreateAndRoundTrip(t *testing.T) {
	s := newTestSessionStore(t)
	// Absent member directory is empty history, not an error.
	msgs, err := s.Messages("t", "m")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("absent member history = %d messages, want 0", len(msgs))
	}
	if err := s.AppendMessage("t", "m", SessionMessage{Kind: "user", From: "cli", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage("t", "m", SessionMessage{Kind: "agent", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	msgs, err = s.Messages("t", "m")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Text != "hello" || msgs[1].Text != "hi" {
		t.Fatalf("history = %+v, want two appended messages in order", msgs)
	}
	if _, err := os.Stat(filepath.Join(s.store.root, "context", "t", "m", MemberMessagesFile)); err != nil {
		t.Fatalf("history file missing after append: %v", err)
	}
}

func TestSessionStoreMessagesRejectEmptyText(t *testing.T) {
	s := newTestSessionStore(t)
	if err := s.AppendMessage("t", "m", SessionMessage{Kind: "user", Text: "  "}); !errors.Is(err, ErrSessionEmpty) {
		t.Fatalf("err = %v, want ErrSessionEmpty", err)
	}
}

func TestSessionStoreCursorRoundTrip(t *testing.T) {
	s := newTestSessionStore(t)
	c, err := s.ReadCursor("t", "m")
	if err != nil {
		t.Fatal(err)
	}
	if c.Cursor != 0 || c.ResumeCount != 0 {
		t.Fatalf("fresh cursor = %+v, want zeros", c)
	}
	want := SessionCursor{Document: Document{SchemaVersion: SchemaVersion}, Cursor: 7, ResumeCount: 2, ContextRef: "ctx/rev-3", Sequence: 41}
	if err := s.WriteCursor("t", "m", want); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadCursor("t", "m")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cursor round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

// TestSessionStoreCursorLegacyFileReadsSequenceZero pins §7 compatibility: a
// cursor.json written before the Sequence field existed (route §11.3) decodes
// with Sequence zero — a stale event counter must never resurrect.
func TestSessionStoreCursorLegacyFileReadsSequenceZero(t *testing.T) {
	root := t.TempDir()
	s, err := NewTeamSessionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := s.MemberDir("t", "m")
	if err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(s.store.root, dir)
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"schema_version":1,"cursor":3,"resume_count":1,"context_ref":"ctx/rev-1"}`)
	if err := os.WriteFile(filepath.Join(abs, MemberCursorFile), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadCursor("t", "m")
	if err != nil {
		t.Fatal(err)
	}
	if got.Cursor != 3 || got.ResumeCount != 1 || got.Sequence != 0 {
		t.Fatalf("legacy cursor = %+v, want cursor=3 resume=1 sequence=0", got)
	}
}

func TestSessionStoreStateRoundTrip(t *testing.T) {
	s := newTestSessionStore(t)
	st, err := s.ReadState("t", "m")
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "stopped" {
		t.Fatalf("absent member state = %q, want stopped", st.State)
	}
	if err := s.WriteState("t", "m", "running"); err != nil {
		t.Fatal(err)
	}
	st, err = s.ReadState("t", "m")
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "running" {
		t.Fatalf("state = %q, want running", st.State)
	}
}

func TestSessionStoreSelectionRoundTrip(t *testing.T) {
	s := newTestSessionStore(t)
	sel, err := s.ReadSelection("t")
	if err != nil {
		t.Fatal(err)
	}
	if sel.MemberID != "" {
		t.Fatalf("fresh selection member = %q, want empty", sel.MemberID)
	}
	if err := s.WriteSelection("t", SessionSelection{MemberID: "leader-1"}); err != nil {
		t.Fatal(err)
	}
	sel, err = s.ReadSelection("t")
	if err != nil {
		t.Fatal(err)
	}
	if sel.Team != "t" || sel.MemberID != "leader-1" {
		t.Fatalf("selection = %+v, want team t / member leader-1", sel)
	}
}

func TestSessionStoreClearIsTeamScoped(t *testing.T) {
	s := newTestSessionStore(t)
	for _, m := range []string{"a", "b"} {
		if err := s.AppendMessage("t1", m, SessionMessage{Kind: "user", Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AppendMessage("t2", "a", SessionMessage{Kind: "user", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearMember("t1", "a"); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := s.Messages("t1", "a"); len(msgs) != 0 {
		t.Fatalf("cleared member still has %d messages", len(msgs))
	}
	if msgs, _ := s.Messages("t1", "b"); len(msgs) != 1 {
		t.Fatalf("sibling member lost history after ClearMember: %d", len(msgs))
	}
	if err := s.ClearTeam("t1"); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := s.Messages("t1", "b"); len(msgs) != 0 {
		t.Fatalf("team context not cleared: %d messages", len(msgs))
	}
	if msgs, _ := s.Messages("t2", "a"); len(msgs) != 1 {
		t.Fatalf("other team lost history after ClearTeam: %d messages", len(msgs))
	}
}

func TestSessionStoreMemberDirsListsCreatedDirs(t *testing.T) {
	s := newTestSessionStore(t)
	ids, err := s.MemberDirs("t")
	if err != nil {
		t.Fatal(err)
	}
	if ids != nil {
		t.Fatalf("fresh team dirs = %v, want nil", ids)
	}
	if err := s.AppendMessage("t", "b", SessionMessage{Kind: "user", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage("t", "a", SessionMessage{Kind: "user", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	ids, err = s.MemberDirs("t")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("member dirs = %v, want 2", ids)
	}
}

func TestSessionStoreCorruptHistoryFailsLoudly(t *testing.T) {
	s := newTestSessionStore(t)
	dir, err := s.memberDir("t", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.store.root, dir, MemberMessagesFile), []byte("{not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Messages("t", "m"); err == nil {
		t.Fatal("corrupt history read as valid")
	}
}

// TestClearTeamIdempotent pins the A9 idempotency half of the clear contract:
// clearing a team or member that has no context (never entered, or already
// cleared) is a no-op, never an error, so crash-recovery replays are safe.
func TestClearTeamIdempotent(t *testing.T) {
	s := newTestSessionStore(t)
	if err := s.AppendMessage("t1", "a", SessionMessage{Kind: "user", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearTeam("t1"); err != nil {
		t.Fatal(err)
	}
	// Re-runs on a cleared team and clears of absent teams/members no-op.
	if err := s.ClearTeam("t1"); err != nil {
		t.Fatalf("re-clear of cleared team: %v", err)
	}
	if err := s.ClearTeam("ghost"); err != nil {
		t.Fatalf("clear of absent team: %v", err)
	}
	if err := s.ClearMember("ghost", "m"); err != nil {
		t.Fatalf("clear of absent member: %v", err)
	}
	if msgs, _ := s.Messages("t1", "a"); len(msgs) != 0 {
		t.Fatalf("cleared team still holds %d messages", len(msgs))
	}
}

// TestSessionPathsStayUnderTeamRoot pins where the store physically writes.
// The context and session trees belong under .reasonix/team; rooting the file
// store at the project root instead put member histories and the session
// selection in the repository itself, where git then tracked them.
func TestSessionPathsStayUnderTeamRoot(t *testing.T) {
	project := t.TempDir()
	s, err := NewTeamSessionStore(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage("t", "m", SessionMessage{Kind: "user", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSelection("t", SessionSelection{
		Document: Document{SchemaVersion: SchemaVersion}, Team: "t", MemberID: "m",
	}); err != nil {
		t.Fatal(err)
	}

	teamRoot := filepath.Join(project, ".reasonix", "team")
	for _, want := range []string{
		filepath.Join(teamRoot, contextRootDir, "t", "m", MemberMessagesFile),
		filepath.Join(teamRoot, sessionDir, "t.json"),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("expected %s under the team root: %v", want, err)
		}
	}
	for _, leaked := range []string{
		filepath.Join(project, contextRootDir),
		filepath.Join(project, sessionDir),
	} {
		if _, err := os.Stat(leaked); !os.IsNotExist(err) {
			t.Errorf("%s must not exist in the project root (err = %v)", leaked, err)
		}
	}
}

// TestSessionStoreUpdateSelectionIsReadModifyWrite pins the fix for the lost
// update: a caller that only changes the member id must not reset the
// deliberate-exit preference, and a writer holding a stale revision must be
// refused rather than clobber the newer document.
func TestSessionStoreUpdateSelectionIsReadModifyWrite(t *testing.T) {
	s := newTestSessionStore(t)
	// The preference is set first, by the path that owns it.
	if err := s.WriteSelection("t", SessionSelection{Team: "t", Suspended: true}); err != nil {
		t.Fatal(err)
	}
	// A member selection then lands without knowing about it.
	sel, err := s.UpdateSelection("t", 0, func(cur *SessionSelection) error {
		cur.MemberID = "leader-1"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sel.Suspended {
		t.Fatal("a partial update dropped the suspension it never touched")
	}
	if sel.Revision != 2 {
		t.Fatalf("revision = %d, want 2 (one per published write)", sel.Revision)
	}
	// A writer pinned to the revision it read is refused once it moved.
	if _, err := s.UpdateSelection("t", 1, func(cur *SessionSelection) error {
		cur.MemberID = "someone-else"
		return nil
	}); !errors.Is(err, ErrCASConflict) {
		t.Fatalf("stale expected revision err = %v, want ErrCASConflict", err)
	}
	// A mutate error publishes nothing.
	boom := errors.New("refused")
	if _, err := s.UpdateSelection("t", 0, func(*SessionSelection) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("mutate error = %v, want %v", err, boom)
	}
	stored, err := s.ReadSelection("t")
	if err != nil {
		t.Fatal(err)
	}
	if stored.MemberID != "leader-1" || stored.Revision != 2 {
		t.Fatalf("a failed mutate published: %+v", stored)
	}
	// A document written before the revision field existed reads as revision 0,
	// so an unpinned writer still accepts it.
	if _, err := s.UpdateSelection("t", 0, nil); err != nil {
		t.Fatalf("unpinned update err = %v", err)
	}
}

// TestSessionStoreWriteSelectionCASRefusesStaleRevision pins the pinned form:
// WriteSelectionCAS is refused when the document moved since the caller read it,
// which is what makes a whole-document write safe to use from a caller that did
// read first. expected == 0 is the documented "unconditional" case and accepts
// whatever is stored, so the pinned revision here is a real one: a document that
// has been written at least once.
func TestSessionStoreWriteSelectionCASRefusesStaleRevision(t *testing.T) {
	s := newTestSessionStore(t)
	if err := s.WriteSelection("t", SessionSelection{Team: "t", MemberID: "seed"}); err != nil {
		t.Fatal(err)
	}
	sel, err := s.ReadSelection("t")
	if err != nil {
		t.Fatal(err)
	}
	if sel.Revision != 1 {
		t.Fatalf("revision after one write = %d, want 1", sel.Revision)
	}
	if err := s.WriteSelectionCAS("t", SessionSelection{Team: "t", MemberID: "a"}, sel.Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSelectionCAS("t", SessionSelection{Team: "t", MemberID: "b"}, sel.Revision); !errors.Is(err, ErrCASConflict) {
		t.Fatalf("stale CAS write err = %v, want ErrCASConflict", err)
	}
	stored, err := s.ReadSelection("t")
	if err != nil {
		t.Fatal(err)
	}
	if stored.MemberID != "a" {
		t.Fatalf("the refused write changed the selection: %+v", stored)
	}
}

// TestSessionStoreConcurrentSelectionWritersKeepEveryField pins the cross-process
// half of the read-modify-write: two writers that each change a different field
// must both survive. A read-modify-write without the lock loses one of them —
// whichever publishes second overwrites the other's field with the value it read
// before that field was set.
func TestSessionStoreConcurrentSelectionWritersKeepEveryField(t *testing.T) {
	s := newTestSessionStore(t)
	if err := s.WriteSelection("t", SessionSelection{Team: "t", MemberID: "start"}); err != nil {
		t.Fatal(err)
	}
	const writers = 8
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Go(func() {
			// Each writer does the read-modify-write the CAS form is for: read the
			// revision, publish against it, retry while another writer won the race.
			for range 200 {
				sel, err := s.ReadSelection("t")
				if err != nil {
					errs[i] = err
					return
				}
				_, err = s.UpdateSelection("t", sel.Revision, func(cur *SessionSelection) error {
					// Each writer owns one field of its own, so a lost update is
					// visible as a missing marker rather than a last-writer-wins
					// value that happens to look plausible.
					if i%2 == 0 {
						cur.MemberID = "member-" + string(rune('a'+i))
					} else {
						cur.Suspended = i%4 == 1
					}
					return nil
				})
				if err == nil {
					return
				}
				if !errors.Is(err, ErrCASConflict) {
					errs[i] = err
					return
				}
			}
			errs[i] = errors.New("exhausted retries")
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}
	stored, err := s.ReadSelection("t")
	if err != nil {
		t.Fatal(err)
	}
	// Every writer advanced the revision exactly once, so the document carries
	// the sum of all of them rather than one writer's snapshot.
	if stored.Revision != 1+writers {
		t.Fatalf("revision = %d, want %d (a writer's update was lost)", stored.Revision, 1+writers)
	}
}

// TestSessionStoreSelectionCrossProcessLostUpdate pins the cross-process half of
// the read-modify-write, which the in-process test cannot: filelock's in-process
// registry would serialize two goroutines even if the file lock were missing, so
// only a second process proves the flock is what protects the document.
//
// The peer takes the lock and signals before it writes, so an unlocked parent
// would read the pre-peer revision and publish over it — the classic lost
// update. With the lock the parent blocks at Acquire, reads what the peer left,
// and publishes on top of it. The peer's field and the parent's must both
// survive, and the revision must show two publishes after the seed.
func TestSessionStoreSelectionCrossProcessLostUpdate(t *testing.T) {
	if os.Getenv("REASONIX_SELECTION_HELPER") == "1" {
		runSelectionHelper(t)
		return
	}
	root := t.TempDir()
	s, err := NewTeamSessionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSelection("t", SessionSelection{Team: "t", MemberID: "start"}); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(root, "peer-ready")
	release := filepath.Join(root, "peer-release")
	cmd := exec.Command(os.Args[0], "-test.run", "^TestSessionStoreSelectionCrossProcessLostUpdate$")
	cmd.Env = append(os.Environ(),
		"REASONIX_SELECTION_HELPER=1",
		"REASONIX_SELECTION_ROOT="+root,
		"REASONIX_SELECTION_READY="+ready,
		"REASONIX_SELECTION_RELEASE="+release,
	)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	waitForFile(t, ready, "the peer did not take the selection lock")
	// The peer holds the lock and has written nothing yet: an update that took no
	// lock would read the seed here and publish over whatever the peer writes.
	done := make(chan error, 1)
	go func() {
		_, err := s.UpdateSelection("t", 0, func(cur *SessionSelection) error {
			cur.MemberID = "parent"
			return nil
		})
		done <- err
	}()
	// Long enough for an unlocked parent to have read and published by now.
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("the update returned while the peer still held the selection lock: %v", err)
	default:
	}
	if err := os.WriteFile(release, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("parent update: %v", err)
	}
	stored, err := s.ReadSelection("t")
	if err != nil {
		t.Fatal(err)
	}
	if stored.MemberID != "parent" {
		t.Fatalf("member id = %q, want parent (the peer's write clobbered the update)", stored.MemberID)
	}
	if !stored.Suspended {
		t.Fatal("the update dropped the field the peer wrote: lost update across processes")
	}
	if stored.Revision != 3 {
		t.Fatalf("revision = %d, want 3 (seed + peer + parent)", stored.Revision)
	}
}

// runSelectionHelper is the peer process: it takes the selection lock, signals
// readiness, and only then publishes its own field — still under the lock — so
// the parent's concurrent update is forced to queue behind a real second process
// and can only see the peer's document after the lock is released.
func runSelectionHelper(t *testing.T) {
	root := os.Getenv("REASONIX_SELECTION_ROOT")
	s, err := NewTeamSessionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	release, err := filelock.Acquire(context.Background(), s.selectionLockPath("t"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := os.WriteFile(os.Getenv("REASONIX_SELECTION_READY"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, os.Getenv("REASONIX_SELECTION_RELEASE"), "the parent never released the peer")
	// Written while still holding the lock, the way UpdateSelection publishes.
	cur, err := s.readSelection("t")
	if err != nil {
		t.Fatal(err)
	}
	cur.Suspended = true
	cur.Revision++
	if err := s.store.Save(filepath.Join(sessionDir, "t.json"), &cur); err != nil {
		t.Fatal(err)
	}
}

// waitForFile blocks until path exists, failing the test past a deadline rather
// than hanging the suite when a peer dies before signalling.
func waitForFile(t *testing.T, path, msg string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
