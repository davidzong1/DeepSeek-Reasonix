package team

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// ExportOptions controls the JSONL snapshot export (route §1.4). The
// snapshot is a derived view of the SQLite board — the only source of
// truth — never a second write path, so it can never fork from it.
type ExportOptions struct {
	SinceSeq        int64 // per-board seq floor; 0 = full history
	IncludeArchived bool  // false = live events only
}

// SnapshotReport summarizes one export run. The digest matches
// PlanMigration's computation (each line plus a newline), so daily
// reconciliation compares equal numbers (route §1.4 step 2).
type SnapshotReport struct {
	Lines    int
	Digest   string
	Archived int // rows excluded by IncludeArchived=false
}

// ExportSnapshot writes the board as checkpoint-consistent JSONL. The
// read runs in one read-only transaction, so every row comes from the
// same WAL snapshot: a concurrent append appears whole or not at all, and
// the export never tears. Rows are ordered by (board_id, seq) — the
// primary key — so repeated exports are byte-identical.
func (s *SQLiteStore) ExportSnapshot(ctx context.Context, w io.Writer, opts ExportOptions) (SnapshotReport, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SnapshotReport{}, fmt.Errorf("team: export snapshot: %w", err)
	}
	defer tx.Rollback()

	var conds []string
	var args []any
	if !opts.IncludeArchived {
		conds = append(conds, "archived = 0")
	}
	if opts.SinceSeq > 0 {
		conds = append(conds, "seq >= ?")
		args = append(args, opts.SinceSeq)
	}
	query := selectEvents
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += " ORDER BY board_id, seq"
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return SnapshotReport{}, fmt.Errorf("team: export snapshot: %w", err)
	}
	defer rows.Close()

	var rep SnapshotReport
	h := sha256.New()
	bw := bufio.NewWriter(w)
	archivedWhere := "archived = 1"
	var archArgs []any
	if opts.SinceSeq > 0 {
		archivedWhere += " AND seq >= ?"
		archArgs = append(archArgs, opts.SinceSeq)
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM board_events WHERE `+archivedWhere, archArgs...).Scan(&rep.Archived); err != nil {
		return rep, fmt.Errorf("team: export snapshot: %w", err)
	}
	for rows.Next() {
		ev, err := rowScan(rows)
		if err != nil {
			return rep, fmt.Errorf("team: export snapshot: %w", err)
		}
		line, err := json.Marshal(rowToExport(ev))
		if err != nil {
			return rep, fmt.Errorf("team: export snapshot: %w", err)
		}
		if _, err := bw.Write(line); err != nil {
			return rep, fmt.Errorf("team: export snapshot: %w", err)
		}
		if err := bw.WriteByte('\n'); err != nil {
			return rep, fmt.Errorf("team: export snapshot: %w", err)
		}
		h.Write(line)
		h.Write([]byte{'\n'})
		rep.Lines++
	}
	if err := rows.Err(); err != nil {
		return rep, fmt.Errorf("team: export snapshot: %w", err)
	}
	if err := bw.Flush(); err != nil {
		return rep, fmt.Errorf("team: export snapshot: %w", err)
	}
	rep.Digest = hex.EncodeToString(h.Sum(nil))
	return rep, nil
}

// exportRow is one JSONL snapshot row (route §1.4): the full event plus
// the legacy results.jsonl aliases (timestamp/member/result/artifact_path
// and report_id), so old parsers keep working unchanged.
type exportRow struct {
	Timestamp    string        `json:"timestamp"`
	Member       string        `json:"member"`
	Result       string        `json:"result"`
	ArtifactPath string        `json:"artifact_path,omitempty"`
	ReportID     string        `json:"report_id,omitempty"`
	BoardID      string        `json:"board_id"`
	Seq          int64         `json:"seq"`
	EventID      string        `json:"event_id"`
	ClientMsgID  string        `json:"client_msg_id,omitempty"`
	Kind         string        `json:"kind"`
	TaskID       string        `json:"task_id"`
	Role         string        `json:"role,omitempty"`
	Agent        string        `json:"agent,omitempty"`
	Generation   uint64        `json:"generation"`
	Digest       string        `json:"digest"`
	Summary      string        `json:"summary,omitempty"`
	ArtifactRefs []ArtifactRef `json:"artifact_refs,omitempty"`
	Supersedes   []int64       `json:"supersedes,omitempty"`
}

// rowToExport maps one event to the snapshot row. The legacy aliases come
// from the stamped event, never from a client-supplied field.
func rowToExport(ev *BoardEvent) exportRow {
	out := exportRow{
		Timestamp:    ev.CreatedAt.Format(time.RFC3339Nano),
		Member:       ev.MemberID,
		Result:       ev.Summary,
		BoardID:      ev.BoardID,
		Seq:          ev.Seq,
		EventID:      ev.EventID,
		ClientMsgID:  ev.ClientMsgID,
		Kind:         string(ev.Kind),
		TaskID:       string(ev.TaskID),
		Role:         ev.Role,
		Agent:        ev.Agent,
		Generation:   ev.Generation,
		Digest:       ev.Digest,
		Summary:      ev.Summary,
		ArtifactRefs: ev.ArtifactRefs,
		Supersedes:   ev.Supersedes,
	}
	if ev.ClientMsgID != "" {
		out.ReportID = ev.ClientMsgID
	}
	if len(ev.ArtifactRefs) > 0 {
		out.ArtifactPath = ev.ArtifactRefs[0].Path
	}
	return out
}
