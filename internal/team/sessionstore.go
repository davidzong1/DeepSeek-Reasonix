package team

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// Team context/session paths (route §4): each member owns one directory under
// the team context root, and the team's session selection lives in one file.
// Every path is project-relative and safePath-verified, so no key can escape
// the .reasonix/team tree.
const (
	contextRootDir = "context"
	sessionDir     = "session"
	trashRootDir   = ".trash" // staged team context roots awaiting deletion (route §6)
)

// Session files inside one member directory. The member runtime is the only
// logical writer; writes go through the atomic chokepoint (§3.4).
const (
	MemberMessagesFile = "messages.jsonl" // message/thinking history, one JSON object per line
	MemberStateFile    = "state.json"     // runtime state and version
	MemberCursorFile   = "cursor.json"    // recovery cursor, resume count, context ref
)

// Session key errors. ErrInvalidSessionKey refuses a team or member id that
// could escape the context root or alias another member's directory.
var (
	ErrInvalidSessionKey = errors.New("team: session key must be non-empty and contain no path separators")
	ErrSessionEmpty      = errors.New("team: session message text must not be empty")
)

// SessionMessage is one line of a member's message history. Seq ordering is
// the line index; the recovery cursor counts consumed lines.
type SessionMessage struct {
	Kind string // "user", "agent", "system"
	From string // sender id; empty for the system channel
	Text string
	TS   string // RFC3339 timestamp
}

// SessionCursor is a member's recovery position (§4.1): how many history
// lines the runtime has consumed, the resume counter, and the context-view
// pointer. Every member owns one; sharing an AgentUserRef never shares it.
type SessionCursor struct {
	Document
	Cursor      int    `json:"cursor"` // consumed message lines; 0 = nothing consumed
	ResumeCount int    `json:"resume_count"`
	ContextRef  string `json:"context_ref,omitempty"`
}

// SessionState is the persisted runtime state of one member (§4.1).
type SessionState struct {
	Document
	State string `json:"state"` // runtime lifecycle state; see agentruntime
}

// SessionSelection is the team's persisted session-window selection (§4.2):
// the currently selected member id, empty when no member is usable.
type SessionSelection struct {
	Document
	Team     string `json:"team"`
	MemberID string `json:"member_id,omitempty"` // empty = no selection (e.g. no leader)
}

// TeamSessionStore is the member-context and session-selection store (route
// §4): .reasonix/team/context/<team>/<member-id>/{messages.jsonl,state.json,
// cursor.json} plus .reasonix/team/session/<team>.json. Member directories
// exist only after first entry (lazy creation); a missing directory is empty
// history, never corruption. All writes go through the atomic chokepoint, so
// the store stays safe under a single logical writer per member.
type TeamSessionStore struct {
	store *FileStore
}

// NewTeamSessionStore returns a session store rooted at projectRoot.
func NewTeamSessionStore(projectRoot string) (*TeamSessionStore, error) {
	if _, err := TeamRoot(projectRoot); err != nil {
		return nil, err
	}
	store, err := NewFileStore(projectRoot)
	if err != nil {
		return nil, err
	}
	return &TeamSessionStore{store: store}, nil
}

// MemberDir returns the member's context directory path, refusing keys that
// escape the context root.
func (s *TeamSessionStore) MemberDir(teamName, memberID string) (string, error) {
	dir, err := s.memberDir(teamName, memberID)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(dir), nil
}

// TeamDir returns the team's context root directory path.
func (s *TeamSessionStore) TeamDir(teamName string) (string, error) {
	if err := validateSessionKey(teamName); err != nil {
		return "", err
	}
	return filepath.Join(contextRootDir, teamName), nil
}

// memberDir resolves the member context directory and ensures it exists
// (lazy creation on first entry).
func (s *TeamSessionStore) memberDir(teamName, memberID string) (string, error) {
	if err := validateSessionKey(teamName); err != nil {
		return "", err
	}
	if err := validateSessionKey(memberID); err != nil {
		return "", err
	}
	rel := filepath.Join(contextRootDir, teamName, memberID)
	if err := os.MkdirAll(filepath.Join(s.store.root, rel), 0o700); err != nil {
		return "", err
	}
	return rel, nil
}

// AppendMessage appends one message to the member's history, creating the
// member directory on first write. The history is rewritten atomically, so a
// failed write never truncates the member's context.
func (s *TeamSessionStore) AppendMessage(teamName, memberID string, msg SessionMessage) error {
	if err := validateSessionKey(teamName); err != nil {
		return err
	}
	if err := validateSessionKey(memberID); err != nil {
		return err
	}
	if strings.TrimSpace(msg.Text) == "" {
		return ErrSessionEmpty
	}
	if msg.Kind == "" {
		msg.Kind = "user"
	}
	dir, err := s.memberDir(teamName, memberID)
	if err != nil {
		return err
	}
	msgs, err := s.readMessages(teamName, memberID)
	if err != nil {
		return err
	}
	line, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	msgs = append(msgs, line)
	// Join with the newline separator the lines lost on read, so each
	// message stays its own JSONL line.
	data := bytes.Join(msgs, []byte{'\n'})
	data = append(data, '\n')
	return s.atomicWrite(s.store.root, filepath.Join(dir, MemberMessagesFile), data)
}

// Messages returns the member's history in append order; a member without a
// directory is empty history, not an error.
func (s *TeamSessionStore) Messages(teamName, memberID string) ([]SessionMessage, error) {
	if err := validateSessionKey(teamName); err != nil {
		return nil, err
	}
	if err := validateSessionKey(memberID); err != nil {
		return nil, err
	}
	msgs, err := s.readMessages(teamName, memberID)
	if err != nil {
		return nil, err
	}
	out := make([]SessionMessage, 0, len(msgs))
	for _, line := range msgs {
		var m SessionMessage
		if err := json.Unmarshal(line, &m); err != nil {
			return nil, &CorruptFileError{Path: filepath.Join(contextRootDir, teamName, memberID, MemberMessagesFile), Err: err}
		}
		out = append(out, m)
	}
	return out, nil
}

// readMessages returns the raw history lines; an absent file is no lines.
func (s *TeamSessionStore) readMessages(teamName, memberID string) ([][]byte, error) {
	full, err := safePath(s.store.root, filepath.Join(contextRootDir, teamName, memberID, MemberMessagesFile))
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		lines = append(lines, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

// ReadCursor returns the member's recovery cursor; absent cursor or member
// directory is a zero cursor (fresh member), never an error.
func (s *TeamSessionStore) ReadCursor(teamName, memberID string) (SessionCursor, error) {
	if err := validateSessionKey(teamName); err != nil {
		return SessionCursor{}, err
	}
	if err := validateSessionKey(memberID); err != nil {
		return SessionCursor{}, err
	}
	var c SessionCursor
	err := s.store.Load(filepath.Join(contextRootDir, teamName, memberID, MemberCursorFile), &c)
	if errors.Is(err, os.ErrNotExist) {
		return SessionCursor{Document: Document{SchemaVersion: SchemaVersion}}, nil
	}
	if err != nil {
		return SessionCursor{}, err
	}
	return c, nil
}

// WriteCursor persists the member's recovery cursor atomically.
func (s *TeamSessionStore) WriteCursor(teamName, memberID string, c SessionCursor) error {
	if err := validateSessionKey(teamName); err != nil {
		return err
	}
	if err := validateSessionKey(memberID); err != nil {
		return err
	}
	dir, err := s.memberDir(teamName, memberID)
	if err != nil {
		return err
	}
	c.Document = Document{SchemaVersion: SchemaVersion}
	return s.store.Save(filepath.Join(dir, MemberCursorFile), &c)
}

// ReadState returns the member's persisted runtime state; an absent member
// is the stopped state, never an error.
func (s *TeamSessionStore) ReadState(teamName, memberID string) (SessionState, error) {
	if err := validateSessionKey(teamName); err != nil {
		return SessionState{}, err
	}
	if err := validateSessionKey(memberID); err != nil {
		return SessionState{}, err
	}
	var st SessionState
	err := s.store.Load(filepath.Join(contextRootDir, teamName, memberID, MemberStateFile), &st)
	if errors.Is(err, os.ErrNotExist) {
		return SessionState{Document: Document{SchemaVersion: SchemaVersion}, State: "stopped"}, nil
	}
	if err != nil {
		return SessionState{}, err
	}
	return st, nil
}

// WriteState persists the member's runtime state atomically.
func (s *TeamSessionStore) WriteState(teamName, memberID string, state string) error {
	if err := validateSessionKey(teamName); err != nil {
		return err
	}
	if err := validateSessionKey(memberID); err != nil {
		return err
	}
	dir, err := s.memberDir(teamName, memberID)
	if err != nil {
		return err
	}
	return s.store.Save(filepath.Join(dir, MemberStateFile), &SessionState{Document: Document{SchemaVersion: SchemaVersion}, State: state})
}

// ReadSelection returns the team's persisted session selection; an absent
// file is an empty selection (no member selected).
func (s *TeamSessionStore) ReadSelection(teamName string) (SessionSelection, error) {
	if err := validateSessionKey(teamName); err != nil {
		return SessionSelection{}, err
	}
	var sel SessionSelection
	err := s.store.Load(filepath.Join(sessionDir, teamName+".json"), &sel)
	if errors.Is(err, os.ErrNotExist) {
		return SessionSelection{Document: Document{SchemaVersion: SchemaVersion}, Team: teamName}, nil
	}
	if err != nil {
		return SessionSelection{}, err
	}
	return sel, nil
}

// WriteSelection persists the team's session selection atomically.
func (s *TeamSessionStore) WriteSelection(teamName string, sel SessionSelection) error {
	if err := validateSessionKey(teamName); err != nil {
		return err
	}
	sel.Document = Document{SchemaVersion: SchemaVersion}
	sel.Team = teamName
	return s.store.Save(filepath.Join(sessionDir, teamName+".json"), &sel)
}

// MemberDirs lists the team's member context directories, or nil when the
// team context root does not exist yet.
func (s *TeamSessionStore) MemberDirs(teamName string) ([]string, error) {
	teamDir, err := s.TeamDir(teamName)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(s.store.root, teamDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}

// ClearMember removes one member's context directory. The destructive
// three-stage confirmation lives in the control layer (route §6); the store
// provides the primitive only.
func (s *TeamSessionStore) ClearMember(teamName, memberID string) error {
	dir, err := s.memberDir(teamName, memberID)
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.store.root, dir))
}

// ClearTeam removes the whole team context root. Like ClearMember, this is
// the store primitive; staging through .trash is the control layer's job
// (route §6).
func (s *TeamSessionStore) ClearTeam(teamName string) error {
	teamDir, err := s.TeamDir(teamName)
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.store.root, teamDir))
}

// TrashTeam stages the team's context root into a timestamped trash directory
// under <root>/.trash/, returning the staged path (project-relative). An
// absent context root is nothing to trash — ("", nil) — so a repeated clear
// after a crash is not an error. The caller deletes the staged dir with
// RemoveTrash; a failed remove keeps the trash in place for audit (§6.6).
func (s *TeamSessionStore) TrashTeam(teamName string) (string, error) {
	teamDir, err := s.TeamDir(teamName)
	if err != nil {
		return "", err
	}
	src := filepath.Join(s.store.root, teamDir)
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	ts := time.Now().UTC().Format("20060102T150405.000000000Z")
	trash := filepath.Join(trashRootDir, teamName+"-"+ts)
	if err := os.MkdirAll(filepath.Join(s.store.root, trashRootDir), 0o700); err != nil {
		return "", err
	}
	if err := os.Rename(src, filepath.Join(s.store.root, trash)); err != nil {
		return "", err
	}
	return filepath.ToSlash(trash), nil
}

// RemoveTrash deletes a staged trash directory. An absent trash dir is
// success, so a repeated clear completes idempotently.
func (s *TeamSessionStore) RemoveTrash(trashPath string) error {
	full, err := safePath(s.store.root, trashPath)
	if err != nil {
		return err
	}
	return os.RemoveAll(full)
}

// ClearTeamTrash removes the team's context root with crash recovery (§6):
// leftover trash from an interrupted run is deleted first, then the root is
// staged into a timestamped trash dir and deleted. A failure keeps the staged
// trash in place and reports the error; repeating the call completes the
// clear. The scope is exactly the team's context root — other teams and
// ordinary session history are untouched.
func (s *TeamSessionStore) ClearTeamTrash(teamName string) error {
	if err := s.sweepStaleTrash(teamName); err != nil {
		return err
	}
	trash, err := s.TrashTeam(teamName)
	if err != nil {
		return err
	}
	if trash == "" {
		return nil // nothing to stage; leftover trash already swept
	}
	return s.RemoveTrash(trash)
}

// sweepStaleTrash deletes trash dirs a previous interrupted clear left behind,
// so a crash between staging and deletion never leaks history.
func (s *TeamSessionStore) sweepStaleTrash(teamName string) error {
	entries, err := os.ReadDir(filepath.Join(s.store.root, trashRootDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), teamName+"-") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.store.root, trashRootDir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// validateSessionKey refuses empty ids and ids containing path separators or
// dot segments, so a key can never escape the context root or alias another
// member's directory.
func validateSessionKey(id string) error {
	if id == "" || strings.ContainsAny(id, "/\\") || id == "." || id == ".." {
		return ErrInvalidSessionKey
	}
	for _, r := range id {
		if r < utf8.RuneSelf && r < 0x20 {
			return ErrInvalidSessionKey
		}
	}
	return nil
}

// atomicWrite exposes the atomic chokepoint for the JSONL history (which has
// no schema_version header and therefore cannot go through FileStore.Save).
func (s *TeamSessionStore) atomicWrite(root, path string, data []byte) error {
	full, err := safePath(root, path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return err
	}
	return AtomicWrite(root, path, data)
}
