package team

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore is the durable board over SQLite in WAL mode (route §1.3).
// Writes run on a dedicated connection inside an IMMEDIATE transaction so
// concurrent writers queue on busy_timeout instead of failing SQLITE_BUSY
// on deferred-lock upgrade; reads stay concurrent under WAL.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens (creating if absent) the board database at path and
// applies the schema. The schema is idempotent, so reopening is safe.
func NewSQLiteStore(ctx context.Context, path string) (*SQLiteStore, error) {
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("team: open board store: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("team: ping board store: %w", err)
	}
	s := &SQLiteStore{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database. Pending writes fail on the next call.
func (s *SQLiteStore) Close() error { return s.db.Close() }

var boardSchema = []string{
	`CREATE TABLE IF NOT EXISTS board_events (
	  board_id TEXT NOT NULL,
	  seq INTEGER NOT NULL,
	  event_id TEXT NOT NULL,
	  client_msg_id TEXT NOT NULL,
	  kind TEXT NOT NULL,
	  task_id TEXT NOT NULL,
	  member_id TEXT NOT NULL,
	  role TEXT NOT NULL,
	  agent TEXT NOT NULL,
	  generation INTEGER NOT NULL,
	  created_at TEXT NOT NULL,
	  digest TEXT NOT NULL,
	  summary TEXT NOT NULL,
	  payload_ref TEXT,
	  supersedes_json TEXT,
	  archived INTEGER NOT NULL DEFAULT 0,
	  PRIMARY KEY (board_id, seq),
	  UNIQUE (board_id, client_msg_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_board_events_read
	  ON board_events(board_id, seq) WHERE archived = 0`,
	`CREATE TABLE IF NOT EXISTS board_cursors (
	  board_id TEXT NOT NULL,
	  consumer_id TEXT NOT NULL,
	  generation INTEGER NOT NULL,
	  last_seq INTEGER NOT NULL DEFAULT 0,
	  updated_at TEXT NOT NULL,
	  PRIMARY KEY (board_id, consumer_id)
	)`,
	`CREATE TABLE IF NOT EXISTS board_conclusions (
	  board_id TEXT NOT NULL,
	  task_id TEXT NOT NULL,
	  topic TEXT NOT NULL,
	  epoch INTEGER NOT NULL,
	  event_seq INTEGER NOT NULL,
	  digest TEXT NOT NULL,
	  summary TEXT NOT NULL,
	  PRIMARY KEY (board_id, task_id, topic)
	)`,
	`CREATE TABLE IF NOT EXISTS board_bindings (
	  member_id TEXT NOT NULL,
	  leader_id TEXT NOT NULL,
	  generation INTEGER NOT NULL,
	  status TEXT NOT NULL,
	  task_id TEXT NOT NULL,
	  bound_at TEXT NOT NULL,
	  PRIMARY KEY (member_id)
	)`,
}

func (s *SQLiteStore) migrate(ctx context.Context) error {
	for _, ddl := range boardSchema {
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("team: board schema: %w", err)
		}
	}
	return nil
}

// inTx runs fn on a dedicated connection inside an IMMEDIATE transaction
// (route §2.1): the write lock is taken up front so concurrent writers
// queue instead of deadlocking on lock upgrade. fn's error rolls back.
func (s *SQLiteStore) inTx(ctx context.Context, fn func(conn *sql.Conn) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	if err := fn(conn); err != nil {
		conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		return err
	}
	return nil
}

// checkAccess enforces the route §6.2 boundary: a private board accepts
// only its owner's writes and reads; shared boards accept any stamped
// member. The check fails closed without revealing the target's content.
func (s *SQLiteStore) checkAccess(boardID string, id Identity) error {
	if owner, private := PrivateBoard(boardID); private && owner != id.MemberID {
		return ErrForbidden
	}
	if id.MemberID == "" {
		return ErrForbidden
	}
	return nil
}

// CheckGeneration gates a write from an older window (route §4.3): the
// member's current generation is the max of its persisted binding and its
// last stamped event on this board. A claim below it is stale; never-bound
// members with no history pass, preserving the unbound-report path. The
// service layer decides when to call it — Append itself stays a pure
// storage primitive.
func (s *SQLiteStore) CheckGeneration(ctx context.Context, boardID, memberID string, gen uint64) error {
	var boundGen, eventGen uint64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(generation),0) FROM board_bindings WHERE member_id = ?`,
		memberID).Scan(&boundGen); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(generation),0) FROM board_events WHERE board_id = ? AND member_id = ?`,
		boardID, memberID).Scan(&eventGen); err != nil {
		return err
	}
	if gen < max(boundGen, eventGen) {
		return ErrStaleGeneration
	}
	return nil
}

// Append stamps one event and writes it atomically with the optional
// conclusion revision. A replayed client_msg_id returns the original
// event without a new seq (route §2.2).
func (s *SQLiteStore) Append(ctx context.Context, in AppendInput) (BoardEvent, error) {
	if err := s.checkAccess(in.BoardID, in.Stamped); err != nil {
		return BoardEvent{}, err
	}
	var ev BoardEvent
	err := s.inTx(ctx, func(conn *sql.Conn) error {
		found, err := scanEvent(ctx, conn, in.BoardID, in.ClientMsgID)
		if err != nil {
			return err
		}
		if found != nil {
			ev = *found
			return nil
		}
		var maxSeq int64
		if err := conn.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(seq),0) FROM board_events WHERE board_id = ?`,
			in.BoardID).Scan(&maxSeq); err != nil {
			return err
		}
		ev = stampEvent(in, maxSeq+1)
		if err := insertEvent(ctx, conn, ev); err != nil {
			return err
		}
		if in.Conclusion != nil {
			return upsertConclusion(ctx, conn, ev, in.Conclusion)
		}
		return nil
	})
	return ev, err
}

// AppendBatch writes all inputs in one transaction; a CAS conflict on any
// element rolls the whole batch back (route §2.1).
func (s *SQLiteStore) AppendBatch(ctx context.Context, in []AppendInput) ([]BoardEvent, error) {
	if len(in) == 0 {
		return nil, nil
	}
	for i := range in {
		if err := s.checkAccess(in[i].BoardID, in[i].Stamped); err != nil {
			return nil, err
		}
	}
	var out []BoardEvent
	err := s.inTx(ctx, func(conn *sql.Conn) error {
		for i := range in {
			ev, err := appendOne(ctx, conn, in[i])
			if err != nil {
				return err
			}
			out = append(out, ev)
		}
		return nil
	})
	return out, err
}

func appendOne(ctx context.Context, conn *sql.Conn, in AppendInput) (BoardEvent, error) {
	found, err := scanEvent(ctx, conn, in.BoardID, in.ClientMsgID)
	if err != nil {
		return BoardEvent{}, err
	}
	if found != nil {
		return *found, nil
	}
	var maxSeq int64
	if err := conn.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq),0) FROM board_events WHERE board_id = ?`,
		in.BoardID).Scan(&maxSeq); err != nil {
		return BoardEvent{}, err
	}
	ev := stampEvent(in, maxSeq+1)
	if err := insertEvent(ctx, conn, ev); err != nil {
		return BoardEvent{}, err
	}
	if in.Conclusion != nil {
		if err := upsertConclusion(ctx, conn, ev, in.Conclusion); err != nil {
			return BoardEvent{}, err
		}
	}
	return ev, nil
}

// stampEvent fills the fields only the store may set: seq, identity and
// digest. An empty EventID gets one generated, so every event has a
// globally unique id even when the client did not supply one.
func stampEvent(in AppendInput, seq int64) BoardEvent {
	eventID := in.EventID
	if eventID == "" {
		var b [8]byte
		rand.Read(b[:]) // crypto/rand failure is a machine-level fault
		eventID = hex.EncodeToString(b[:])
	}
	ev := BoardEvent{
		SchemaVersion: SchemaVersion,
		BoardID:       in.BoardID,
		Seq:           seq,
		EventID:       eventID,
		ClientMsgID:   in.ClientMsgID,
		Kind:          in.Kind,
		TaskID:        in.TaskID,
		MemberID:      in.Stamped.MemberID,
		Role:          in.Stamped.Role,
		Agent:         in.Stamped.Agent,
		Generation:    in.Stamped.Generation,
		CreatedAt:     in.CreatedAt,
		Summary:       in.Summary,
		ArtifactRefs:  in.ArtifactRefs,
		Supersedes:    in.Supersedes,
	}
	ev.Digest = digestOf(ev)
	return ev
}

// digestOf hashes the full event, so any client-visible change breaks the
// digest and views can compare revisions cheaply (route §1.1).
func digestOf(ev BoardEvent) string {
	h := sha256.New()
	json.NewEncoder(h).Encode(ev)
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func insertEvent(ctx context.Context, conn *sql.Conn, ev BoardEvent) error {
	supersedes, err := json.Marshal(ev.Supersedes)
	if err != nil {
		return err
	}
	payload := ""
	if len(ev.ArtifactRefs) > 0 {
		refs, err := json.Marshal(ev.ArtifactRefs)
		if err != nil {
			return err
		}
		payload = string(refs)
	}
	_, err = conn.ExecContext(ctx,
		`INSERT INTO board_events
		  (board_id, seq, event_id, client_msg_id, kind, task_id, member_id,
		   role, agent, generation, created_at, digest, summary, payload_ref,
		   supersedes_json)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		ev.BoardID, ev.Seq, ev.EventID, ev.ClientMsgID, string(ev.Kind),
		string(ev.TaskID), ev.MemberID, ev.Role, ev.Agent, ev.Generation,
		ev.CreatedAt.Format(time.RFC3339Nano), ev.Digest, ev.Summary, payload,
		string(supersedes))
	return err
}

// scanEvent returns the stored event for a client_msg_id, or nil when the
// key is new (route §2.2 idempotent replay).
func scanEvent(ctx context.Context, conn *sql.Conn, boardID, clientMsgID string) (*BoardEvent, error) {
	rows, err := conn.QueryContext(ctx, selectEvents+`
		  WHERE board_id = ? AND client_msg_id = ?`, boardID, clientMsgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	ev, err := rowScan(rows)
	if err != nil {
		return nil, err
	}
	return ev, rows.Err()
}

// upsertConclusion applies a conclusion revision with base_epoch CAS
// (route §2.2): insert at epoch 1 when absent, otherwise update where the
// epoch still matches and bump it. A mismatch returns ErrConflict with the
// current revision so the caller can merge.
func upsertConclusion(ctx context.Context, conn *sql.Conn, ev BoardEvent, cu *ConclusionUpdate) error {
	if cu.BaseEpoch == 0 {
		var exists int
		err := conn.QueryRowContext(ctx,
			`SELECT 1 FROM board_conclusions
			  WHERE board_id = ? AND task_id = ? AND topic = ?`,
			ev.BoardID, string(ev.TaskID), cu.Topic).Scan(&exists)
		if err == nil {
			return currentConflict(ctx, conn, ev, cu.Topic)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = conn.ExecContext(ctx,
			`INSERT INTO board_conclusions
			  (board_id, task_id, topic, epoch, event_seq, digest, summary)
			 VALUES (?,?,?,1,?,?,?)`,
			ev.BoardID, string(ev.TaskID), cu.Topic, ev.Seq, ev.Digest, cu.Summary)
		return err
	}
	res, err := conn.ExecContext(ctx,
		`UPDATE board_conclusions
		    SET epoch = epoch + 1, event_seq = ?, digest = ?, summary = ?
		  WHERE board_id = ? AND task_id = ? AND topic = ? AND epoch = ?`,
		ev.Seq, ev.Digest, cu.Summary, ev.BoardID, string(ev.TaskID), cu.Topic, cu.BaseEpoch)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return currentConflict(ctx, conn, ev, cu.Topic)
	}
	return nil
}

func currentConflict(ctx context.Context, conn *sql.Conn, ev BoardEvent, topic string) error {
	var epoch uint64
	var seq int64
	err := conn.QueryRowContext(ctx,
		`SELECT epoch, event_seq FROM board_conclusions
		  WHERE board_id = ? AND task_id = ? AND topic = ?`,
		ev.BoardID, string(ev.TaskID), topic).Scan(&epoch, &seq)
	if errors.Is(err, sql.ErrNoRows) {
		epoch, seq = 0, 0
	} else if err != nil {
		return err
	}
	return &ErrConflict{BoardID: ev.BoardID, TaskID: ev.TaskID, Topic: topic,
		CurrentEpoch: epoch, CurrentSeq: seq}
}

// ReadAfter returns events strictly after afterSeq, newest last (route
// §2.3). A cursor that has fallen into an archived hole is flagged
// NeedResync: the caller drops its local cache and reloads.
func (s *SQLiteStore) ReadAfter(ctx context.Context, boardID string, afterSeq int64, f Filter) (Page, error) {
	if err := s.checkAccess(boardID, f.Stamped); err != nil {
		return Page{}, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	var page Page
	var marker int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM board_events
		  WHERE board_id = ? AND archived = 1 AND seq <= ? LIMIT 1`,
		boardID, afterSeq).Scan(&marker)
	if err == nil {
		page.NeedResync = true
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Page{}, err
	}

	where := `board_id = ? AND seq > ? AND archived = 0`
	args := []any{boardID, afterSeq}
	if f.TaskID != "" {
		where += ` AND task_id = ?`
		args = append(args, string(f.TaskID))
	}
	if f.Kind != "" {
		where += ` AND kind = ?`
		args = append(args, string(f.Kind))
	}
	if f.MemberID != "" {
		where += ` AND member_id = ?`
		args = append(args, f.MemberID)
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, selectEvents+
		` WHERE `+where+` ORDER BY seq LIMIT ?`, args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	for rows.Next() {
		ev, err := rowScan(rows)
		if err != nil {
			return Page{}, err
		}
		if len(page.Events) == limit {
			page.HasMore = true
			break
		}
		page.Events = append(page.Events, *ev)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if n := len(page.Events); n > 0 {
		page.NextSeq = page.Events[n-1].Seq
	}
	return page, nil
}

// selectEvents is the shared event column set for scanEvent and ReadAfter.
const selectEvents = `SELECT board_id, seq, event_id, client_msg_id, kind,
        task_id, member_id, role, agent, generation, created_at, digest,
        summary, payload_ref, supersedes_json FROM board_events`

func rowScan(rows *sql.Rows) (*BoardEvent, error) {
	var ev BoardEvent
	var kind, taskID, createdAt, payloadRef, supersedesJSON string
	err := rows.Scan(&ev.BoardID, &ev.Seq, &ev.EventID, &ev.ClientMsgID,
		&kind, &taskID, &ev.MemberID, &ev.Role, &ev.Agent, &ev.Generation,
		&createdAt, &ev.Digest, &ev.Summary, &payloadRef, &supersedesJSON)
	if err != nil {
		return nil, err
	}
	ev.SchemaVersion = SchemaVersion
	ev.Kind = EventKind(kind)
	ev.TaskID = TaskID(taskID)
	ev.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, err
	}
	if payloadRef != "" {
		if err := json.Unmarshal([]byte(payloadRef), &ev.ArtifactRefs); err != nil {
			return nil, err
		}
	}
	if supersedesJSON != "" && supersedesJSON != "null" {
		if err := json.Unmarshal([]byte(supersedesJSON), &ev.Supersedes); err != nil {
			return nil, err
		}
	}
	return &ev, nil
}

// ReadView returns the materialized conclusion snapshot for a task (route
// §1.2). L0-L3 token budgets and caching are P3; this is the durable
// projection they are built on.
func (s *SQLiteStore) ReadView(ctx context.Context, boardID string, view ViewSpec) (MaterializedView, error) {
	if err := s.checkAccess(boardID, Identity{MemberID: "*"}); err != nil {
		return MaterializedView{}, err
	}
	limit := view.Limit
	if limit <= 0 {
		limit = 32
	}
	var out MaterializedView
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq),0), COALESCE(MAX(epoch),0) FROM board_events e
		   JOIN board_conclusions c ON c.event_seq = e.seq
		  WHERE e.board_id = ?`,
		boardID).Scan(&out.SourceSeq, &out.Epoch); err != nil {
		return MaterializedView{}, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.task_id, c.topic, c.epoch, c.event_seq, c.digest, c.summary
		   FROM board_conclusions c JOIN board_events e ON e.seq = c.event_seq
		  WHERE c.board_id = ? AND (? = '' OR c.task_id = ?)
		  ORDER BY c.topic LIMIT ?`,
		boardID, string(view.TaskID), string(view.TaskID), limit)
	if err != nil {
		return MaterializedView{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Conclusion
		var taskID string
		if err := rows.Scan(&taskID, &c.Topic, &c.Epoch, &c.EventSeq, &c.Digest, &c.Summary); err != nil {
			return MaterializedView{}, err
		}
		c.BoardID = boardID
		c.TaskID = TaskID(taskID)
		out.Conclusions = append(out.Conclusions, c)
	}
	if err := rows.Err(); err != nil {
		return MaterializedView{}, err
	}
	return out, nil
}

// AdvanceCursor moves one consumer's cursor forward (route §2.2): the
// advance must be monotonic and come from the current generation. The
// update is a single row write, so concurrent consumers never cover each
// other.
func (s *SQLiteStore) AdvanceCursor(ctx context.Context, c CursorUpdate) error {
	if err := s.checkAccess(c.BoardID, Identity{MemberID: c.ConsumerID}); err != nil {
		return err
	}
	err := s.inTx(ctx, func(conn *sql.Conn) error {
		var gen uint64
		var last int64
		err := conn.QueryRowContext(ctx,
			`SELECT generation, last_seq FROM board_cursors
			  WHERE board_id = ? AND consumer_id = ?`,
			c.BoardID, c.ConsumerID).Scan(&gen, &last)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = conn.ExecContext(ctx,
				`INSERT INTO board_cursors (board_id, consumer_id, generation, last_seq, updated_at)
				 VALUES (?,?,?,?,?)`,
				c.BoardID, c.ConsumerID, c.Generation, c.LastSeq,
				time.Now().UTC().Format(time.RFC3339Nano))
			return err
		}
		if err != nil {
			return err
		}
		if c.Generation < gen {
			return ErrStaleGeneration
		}
		if c.LastSeq < last {
			return ErrCursorBackwards
		}
		_, err = conn.ExecContext(ctx,
			`UPDATE board_cursors
			    SET generation = ?, last_seq = ?, updated_at = ?
			  WHERE board_id = ? AND consumer_id = ?`,
			c.Generation, c.LastSeq, time.Now().UTC().Format(time.RFC3339Nano),
			c.BoardID, c.ConsumerID)
		return err
	})
	return err
}

// Supersede publishes a replacement event that chains the given event
// seqs, atomically in one transaction (route §1.1): the superseded events
// stay on the board as audit history.
func (s *SQLiteStore) Supersede(ctx context.Context, boardID string, ids []int64, replacement AppendInput) (BoardEvent, error) {
	if err := s.checkAccess(boardID, replacement.Stamped); err != nil {
		return BoardEvent{}, err
	}
	if len(ids) == 0 {
		return BoardEvent{}, fmt.Errorf("team: supersede requires at least one target seq")
	}
	replacement.BoardID = boardID
	replacement.Supersedes = ids
	var ev BoardEvent
	err := s.inTx(ctx, func(conn *sql.Conn) error {
		for _, id := range ids {
			var exists int
			err := conn.QueryRowContext(ctx,
				`SELECT 1 FROM board_events WHERE board_id = ? AND seq = ?`,
				boardID, id).Scan(&exists)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("team: supersede target seq %d not found on %s", id, boardID)
			}
			if err != nil {
				return err
			}
		}
		var err error
		ev, err = appendOne(ctx, conn, replacement)
		return err
	})
	return ev, err
}

// ArchiveBefore marks events up to seq as archived (route §2.3). Logical
// seqs are never renumbered; readers whose cursor lands in the hole get
// NeedResync. Archived events remain queryable only by explicit seq.
// Destructive, so the management channel gates it (route §6.2).
func (s *SQLiteStore) ArchiveBefore(ctx context.Context, boardID string, seq int64, id Identity) error {
	if err := RequireManagement(id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE board_events SET archived = 1 WHERE board_id = ? AND seq <= ? AND archived = 0`,
		boardID, seq)
	return err
}
