package team

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fixedTime is the deterministic CreatedAt for golden exports; a golden
// line is only reproducible when every stamped field is fixed.
var fixedTime = time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

// goldenAppend writes one deterministic event: fixed id, time and identity,
// so the export line is byte-stable.
func goldenAppend(t *testing.T, s *SQLiteStore, msgID, eventID, summary string, kind EventKind, task TaskID, refs []ArtifactRef, supersedes []int64) {
	t.Helper()
	_, err := s.Append(context.Background(), AppendInput{
		BoardID: BoardShared, EventID: eventID, ClientMsgID: msgID,
		Kind: kind, TaskID: task, CreatedAt: fixedTime, Summary: summary,
		ArtifactRefs: refs, Supersedes: supersedes,
		Stamped: Identity{MemberID: "m1", Role: "coder", Agent: "claude", Generation: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// exportedRows runs one export and parses every line, failing on any line
// that is not a whole JSON object.
func exportedRows(t *testing.T, s *SQLiteStore, opts ExportOptions) ([]exportRow, SnapshotReport) {
	t.Helper()
	var buf bytes.Buffer
	rep, err := s.ExportSnapshot(context.Background(), &buf, opts)
	if err != nil {
		t.Fatal(err)
	}
	var rows []exportRow
	for _, ln := range bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n")) {
		if len(ln) == 0 {
			continue
		}
		var r exportRow
		if err := json.Unmarshal(ln, &r); err != nil {
			t.Fatalf("torn line %q: %v", ln, err)
		}
		rows = append(rows, r)
	}
	return rows, rep
}

// TestExportGoldenFile pins the exact snapshot shape (route §1.4): the
// legacy results.jsonl aliases ride alongside the full event fields.
func TestExportGoldenFile(t *testing.T) {
	s := newTestBoard(t)
	goldenAppend(t, s, "r1", "ev-1", "done", EventReport, "t1", nil, nil)
	goldenAppend(t, s, "r2", "ev-2", "concluded", EventConclusion, "t1",
		[]ArtifactRef{{Name: "plan", Path: "artifacts/plan.md", Size: 12, Digest: "abc"}},
		[]int64{1})

	var buf bytes.Buffer
	rep, err := s.ExportSnapshot(context.Background(), &buf, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Lines != 2 {
		t.Fatalf("want 2 lines, got %d", rep.Lines)
	}
	if len(rep.Digest) != 64 {
		t.Fatalf("digest must be 64 hex chars, got %q", rep.Digest)
	}
	// The whole snapshot is pinned byte-for-byte: any shape change to the
	// exported rows (field order, aliases, formatting) fails here.
	const golden = `{"timestamp":"2026-08-25T12:00:00Z","member":"m1","result":"done","report_id":"r1","board_id":"shared","seq":1,"event_id":"ev-1","client_msg_id":"r1","kind":"report","task_id":"t1","role":"coder","agent":"claude","generation":1,"digest":"39e5101edf3f567b4a5238b589c82735","summary":"done"}
{"timestamp":"2026-08-25T12:00:00Z","member":"m1","result":"concluded","artifact_path":"artifacts/plan.md","report_id":"r2","board_id":"shared","seq":2,"event_id":"ev-2","client_msg_id":"r2","kind":"conclusion","task_id":"t1","role":"coder","agent":"claude","generation":1,"digest":"085dd5b7327226888bb46ef5dbb38520","summary":"concluded","artifact_refs":[{"name":"plan","path":"artifacts/plan.md","size":12,"digest":"abc"}],"supersedes":[1]}
`
	if buf.String() != golden {
		t.Fatalf("golden mismatch:\n--- got ---\n%s--- want ---\n%s", buf.String(), golden)
	}

	var row exportRow
	first := bytes.SplitN(buf.Bytes(), []byte("\n"), 2)[0]
	if err := json.Unmarshal(first, &row); err != nil {
		t.Fatal(err)
	}
	// legacy aliases
	if row.Timestamp != fixedTime.Format(time.RFC3339Nano) || row.Member != "m1" || row.Result != "done" {
		t.Fatalf("legacy aliases wrong: %+v", row)
	}
	if row.ReportID != "r1" || row.ArtifactPath != "" {
		t.Fatalf("legacy aliases wrong: %+v", row)
	}
	// full event fields
	if row.BoardID != BoardShared || row.Seq != 1 || row.EventID != "ev-1" || row.ClientMsgID != "r1" {
		t.Fatalf("event fields wrong: %+v", row)
	}
	if row.Kind != "report" || row.TaskID != "t1" || row.Role != "coder" || row.Agent != "claude" || row.Generation != 1 {
		t.Fatalf("stamped fields wrong: %+v", row)
	}
}

// TestExportIdempotent: two exports of the same store are byte-identical.
func TestExportIdempotent(t *testing.T) {
	s := newTestBoard(t)
	for i := 0; i < 10; i++ {
		boardAppendKind(t, s, fmt.Sprintf("e%d", i), "m1", 1, EventReport, "t1", "s")
	}
	var a, b bytes.Buffer
	ra, err := s.ExportSnapshot(context.Background(), &a, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rb, err := s.ExportSnapshot(context.Background(), &b, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("repeat exports must be byte-identical")
	}
	if ra.Digest != rb.Digest || ra.Lines != rb.Lines {
		t.Fatalf("reports must match: %+v vs %+v", ra, rb)
	}
}

// TestExportConcurrentAppendNoTear matches route §1.4: a snapshot exported
// while writers append reads one WAL snapshot — every line is whole, seqs
// never repeat, and the row count is some intermediate total, never a
// torn one.
func TestExportConcurrentAppendNoTear(t *testing.T) {
	s := newTestBoard(t)
	for i := 0; i < 5; i++ {
		boardAppendKind(t, s, fmt.Sprintf("pre%d", i), "m1", 1, EventReport, "t1", "s")
	}
	// Writers run concurrently on the same store: exports take a
	// read-only transaction while appends queue on their own writes.
	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if _, err := s.Append(context.Background(), AppendInput{
					BoardID: BoardShared, ClientMsgID: fmt.Sprintf("w%d-%d", w, i),
					Kind: EventReport, TaskID: "t1", Summary: "s",
					Stamped: Identity{MemberID: fmt.Sprintf("m%d", w), Generation: 1},
				}); err != nil {
					return
				}
			}
		}(w)
	}
	rows, rep := exportedRows(t, s, ExportOptions{})
	wg.Wait()
	if rep.Lines != len(rows) {
		t.Fatalf("report lines %d != parsed rows %d", rep.Lines, len(rows))
	}
	if rep.Lines < 5 || rep.Lines > 205 {
		t.Fatalf("snapshot must be a whole intermediate state, got %d rows", rep.Lines)
	}
	seen := map[int64]bool{}
	for _, row := range rows {
		if row.BoardID != BoardShared || seen[row.Seq] {
			t.Fatalf("duplicate or foreign row: %+v", row)
		}
		seen[row.Seq] = true
	}
}

// errWriter fails after max successful writes, standing in for a killed
// process mid-export.
type errWriter struct {
	max int
	n   int
}

func (w *errWriter) Write(p []byte) (int, error) {
	if w.n >= w.max {
		return 0, errors.New("write interrupted")
	}
	w.n++
	return len(p), nil
}

// TestExportKillReopen matches route §9 recovery: an interrupted export
// leaves nothing half-written behind, and a reopened store exports the
// full history identically.
func TestExportKillReopen(t *testing.T) {
	dir := t.TempDir()
	s := newTestBoardAt(t, dir)
	// Enough events to overflow the internal buffer, so the interrupted
	// writer is actually reached mid-export.
	for i := 0; i < 100; i++ {
		boardAppendKind(t, s, fmt.Sprintf("e%d", i), "m1", 1, EventReport, "t1", "s")
	}
	if _, err := s.ExportSnapshot(context.Background(), &errWriter{max: 2}, ExportOptions{}); err == nil {
		t.Fatal("export must surface the write failure")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2 := newTestBoardAt(t, dir)
	defer s2.Close()
	boardAppendKind(t, s2, "e100", "m1", 1, EventReport, "t1", "s")
	rows, rep := exportedRows(t, s2, ExportOptions{})
	if rep.Lines != 101 || len(rows) != 101 {
		t.Fatalf("reopened store must export all 101 events, got %d", rep.Lines)
	}
	var a, b bytes.Buffer
	ra, _ := s2.ExportSnapshot(context.Background(), &a, ExportOptions{})
	rb, _ := s2.ExportSnapshot(context.Background(), &b, ExportOptions{})
	if !bytes.Equal(a.Bytes(), b.Bytes()) || ra.Digest != rb.Digest {
		t.Fatal("repeat exports after reopen must be identical")
	}
}

// TestExportSinceSeqAndArchived: SinceSeq filters per-board seq, archived
// rows are excluded by default and reported in the count.
func TestExportSinceSeqAndArchived(t *testing.T) {
	s := newTestBoard(t)
	for i := 0; i < 6; i++ {
		boardAppendKind(t, s, fmt.Sprintf("e%d", i), "m1", 1, EventReport, "t1", "s")
	}
	if err := s.ArchiveBefore(context.Background(), BoardShared, 2, Identity{MemberID: "leader", Role: "leader", Generation: 1}); err != nil {
		t.Fatal(err)
	}
	rows, rep := exportedRows(t, s, ExportOptions{})
	if len(rows) != 4 || rep.Archived != 2 {
		t.Fatalf("default export must be live-only: rows %d archived %d", len(rows), rep.Archived)
	}
	for i, row := range rows {
		if row.Seq != int64(i+3) {
			t.Fatalf("live rows must be seq 3..6, got %+v", row)
		}
	}
	all, _ := exportedRows(t, s, ExportOptions{IncludeArchived: true})
	if len(all) != 6 {
		t.Fatalf("IncludeArchived must include all 6, got %d", len(all))
	}
	since, _ := exportedRows(t, s, ExportOptions{SinceSeq: 4})
	if len(since) != 3 || since[0].Seq != 4 {
		t.Fatalf("SinceSeq must floor at 4, got %+v", since)
	}
}
