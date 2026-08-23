package team

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
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
