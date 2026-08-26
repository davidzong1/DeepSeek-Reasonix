package main

// Export-op gateway effect tests (route §1.4/§4.3): the JSONL snapshot
// reaches the caller through the same stdin/stdout seam as every other op,
// checkpoint-consistent and digest-checkable against the SQLite store.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/team"
)

// boardReq builds a request body for any op: the shared fixture seam that
// anchors the task_id/generation/watermark trio — task ids ride on appends
// and read filters, generation gates every write, and the watermark is the
// seq floor on cursor and export.
func boardReq(op string, kv map[string]any) string {
	body := map[string]any{"op": op}
	for k, v := range kv {
		body[k] = v
	}
	b, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// genID is the stamped identity of every fixture append; generation is the
// per-write gate, so raising it simulates a restarted window.
func genID(gen uint64) map[string]any {
	return map[string]any{"member_id": "m1", "role": "coder", "agent": "claude", "generation": gen}
}

// appendBoard appends one report event through the gateway and returns its
// stamped seq — the watermark every later export floors on.
func appendBoard(t *testing.T, db *team.SQLiteStore, msgID, task string, gen uint64) int64 {
	t.Helper()
	_, resp := contractRun(t, db, boardReq("append", map[string]any{
		"board_id": "shared", "client_msg_id": msgID, "kind": "report",
		"task_id": task, "summary": "s-" + msgID, "identity": genID(gen),
	}))
	if resp.Event == nil {
		t.Fatalf("append %s rejected: %+v", msgID, resp.Error)
	}
	return resp.Event.Seq
}

// exportCLI runs the export op through the gateway and returns the snapshot
// report and the JSONL body the response carried.
func exportCLI(t *testing.T, db *team.SQLiteStore, since int64, archived bool) (team.SnapshotReport, string) {
	t.Helper()
	req := map[string]any{"board_id": "shared"}
	if since > 0 {
		req["after_seq"] = since
	}
	if archived {
		req["include_archived"] = true
	}
	_, resp := contractRun(t, db, boardReq("export", req))
	if resp.Snapshot == nil {
		t.Fatalf("export rejected: %+v", resp.Error)
	}
	return team.SnapshotReport{Lines: resp.Snapshot.Lines, Digest: resp.Snapshot.Digest, Archived: resp.Snapshot.Archived}, resp.Jsonl
}

// recomputedDigest re-derives the snapshot digest the way an external
// reconciler must: every line plus a trailing newline (§1.4 step 2).
func recomputedDigest(jsonl string) string {
	h := sha256.New()
	h.Write([]byte(jsonl))
	return hex.EncodeToString(h.Sum(nil))
}

// TestCLIExportEmptyBoardStableDigest pins the empty snapshot: zero lines,
// no JSONL body, and a stable digest — twice in a row — so reconcilers can
// compare empty states without special-casing.
func TestCLIExportEmptyBoardStableDigest(t *testing.T) {
	db := newCLIBoard(t)
	rep, jsonl := exportCLI(t, db, 0, false)
	if rep.Lines != 0 || rep.Archived != 0 || jsonl != "" {
		t.Fatalf("empty board must export zero lines, got %+v jsonl=%q", rep, jsonl)
	}
	rep2, _ := exportCLI(t, db, 0, false)
	if rep.Digest != rep2.Digest {
		t.Fatalf("the empty digest must be stable, got %q then %q", rep.Digest, rep2.Digest)
	}
}

// TestCLIExportMatchesDirectStore pins the gateway against the store seam:
// the same board exported through stdin/stdout and through ExportSnapshot
// directly must agree on line count and digest — the wire adds no row.
func TestCLIExportMatchesDirectStore(t *testing.T) {
	db := newCLIBoard(t)
	appendBoard(t, db, "a1", "t1", 1)
	appendBoard(t, db, "a2", "t2", 1)
	appendBoard(t, db, "a3", "t1", 2)

	rep, jsonl := exportCLI(t, db, 0, false)
	var direct bytes.Buffer
	drep, err := db.ExportSnapshot(context.Background(), &direct, team.ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Lines != drep.Lines || rep.Digest != drep.Digest {
		t.Fatalf("gateway export %+v must equal store export %+v", rep, drep)
	}
	if jsonl != direct.String() {
		t.Fatal("the gateway JSONL body must be byte-identical to the store export")
	}
	if rep.Lines != 3 {
		t.Fatalf("three appends must export three lines, got %d", rep.Lines)
	}
}

// TestCLIExportTwiceIdempotent pins the snapshot as a derived view: two
// exports with no writes between them are byte-identical, digest included.
func TestCLIExportTwiceIdempotent(t *testing.T) {
	db := newCLIBoard(t)
	appendBoard(t, db, "a1", "t1", 1)
	appendBoard(t, db, "a2", "t2", 1)
	rep1, jsonl1 := exportCLI(t, db, 0, false)
	rep2, jsonl2 := exportCLI(t, db, 0, false)
	if rep1.Digest != rep2.Digest || jsonl1 != jsonl2 {
		t.Fatal("a quiet board must export byte-identically twice")
	}
}

// TestCLIExportCheckpointDuringAppend pins the single-transaction snapshot:
// an export taken before a later append keeps its lines and digest — the
// caller holds a consistent checkpoint, never a moving window.
func TestCLIExportCheckpointDuringAppend(t *testing.T) {
	db := newCLIBoard(t)
	appendBoard(t, db, "a1", "t1", 1)
	appendBoard(t, db, "a2", "t2", 1)
	rep, jsonl := exportCLI(t, db, 0, false)
	if rep.Lines != 2 {
		t.Fatalf("snapshot before the append must hold 2 lines, got %d", rep.Lines)
	}
	appendBoard(t, db, "a3", "t3", 1)
	rep2, jsonl2 := exportCLI(t, db, 0, false)
	if rep.Lines != rep2.Lines && rep2.Lines != 3 {
		t.Fatalf("the later export must see 3 lines, got %d", rep2.Lines)
	}
	if rep.Digest == rep2.Digest || jsonl == jsonl2 {
		t.Fatal("a new write must change the next snapshot")
	}
}

// TestCLIExportSinceSeqIncremental pins the watermark path: after_seq is an
// inclusive floor (seq >= floor, matching the cursor), so a consumer that
// persisted its last seq pulls exactly the rows it has not seen.
func TestCLIExportSinceSeqIncremental(t *testing.T) {
	db := newCLIBoard(t)
	appendBoard(t, db, "a1", "t1", 1)
	appendBoard(t, db, "a2", "t2", 1)
	seq := appendBoard(t, db, "a3", "t3", 1)
	if seq != 3 {
		t.Fatalf("third append must stamp seq 3, got %d", seq)
	}
	rep, jsonl := exportCLI(t, db, 3, false)
	if rep.Lines != 1 {
		t.Fatalf("after_seq=3 must export exactly the seq-3 row, got %d", rep.Lines)
	}
	if !strings.Contains(jsonl, `"seq":3`) {
		t.Fatalf("the incremental body must hold the seq-3 row, got:\n%s", jsonl)
	}
	if recomputedDigest(jsonl) != rep.Digest {
		t.Fatal("the incremental digest must be recomputable from its own body")
	}
}

// TestCLIExportDigestRecomputable pins the reconciliation contract: the
// snapshot's digest is exactly sha256 over every line plus a newline, so an
// external process can verify a downloaded snapshot without the store.
func TestCLIExportDigestRecomputable(t *testing.T) {
	db := newCLIBoard(t)
	appendBoard(t, db, "a1", "t1", 1)
	appendBoard(t, db, "a2", "t2", 1)
	appendBoard(t, db, "a3", "t1", 2)
	rep, jsonl := exportCLI(t, db, 0, false)
	if recomputedDigest(jsonl) != rep.Digest {
		t.Fatalf("recomputed digest %s must equal snapshot digest %s", recomputedDigest(jsonl), rep.Digest)
	}
}

// TestCLIExportArchivedExcluded pins the live/archived split over the wire:
// an archived row drops out of the default snapshot (counted in Archived),
// and include_archived=true restores it. The archive is prepared through the
// store — the gateway has no archive op — while the export itself runs the
// real stdin/stdout seam.
func TestCLIExportArchivedExcluded(t *testing.T) {
	db := newCLIBoard(t)
	appendBoard(t, db, "a1", "t1", 1)
	appendBoard(t, db, "a2", "t2", 1)
	appendBoard(t, db, "a3", "t3", 1)
	if err := db.ArchiveBefore(context.Background(), "shared", 2, team.Identity{MemberID: "leader", Role: "leader", Generation: 1}); err != nil {
		t.Fatal(err)
	}
	rep, jsonl := exportCLI(t, db, 0, false)
	if rep.Lines != 1 || rep.Archived != 2 {
		t.Fatalf("default export must be live-only: rows %d archived %d", rep.Lines, rep.Archived)
	}
	if !strings.Contains(jsonl, `"seq":3`) || strings.Contains(jsonl, `"seq":1`) {
		t.Fatalf("the live snapshot must hold seq 3 only, got:\n%s", jsonl)
	}
	all, allJSONL := exportCLI(t, db, 0, true)
	if all.Lines != 3 || !strings.Contains(allJSONL, `"seq":1`) {
		t.Fatalf("include_archived must restore all three rows, got %+v", all)
	}
}
