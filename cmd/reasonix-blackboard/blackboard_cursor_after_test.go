package main

// After-seq and cursor contract tests at the P6.1 wire boundary (route
// §2.3, P8): incremental reads never replay, cursor positions survive a
// reopen, and the edge values the Python gateway may pass behave sanely.

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"reasonix/internal/team"
)

func openStore(t *testing.T, path string) *team.SQLiteStore {
	t.Helper()
	s, err := team.NewSQLiteStore(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// cliStoreAppend appends through the in-process contractRun path (the
// cross-process suite already defines cliBoardAppend for the real binary).
func cliStoreAppend(t *testing.T, db *team.SQLiteStore, msgID string) int64 {
	t.Helper()
	_, resp := contractRun(t, db, `{"op":"append","board_id":"shared","client_msg_id":"`+msgID+`","kind":"report","task_id":"t","summary":"s","identity":{"member_id":"m","generation":1}}`)
	if !resp.OK || resp.Event == nil {
		t.Fatalf("append %s rejected: %+v", msgID, resp)
	}
	return resp.Event.Seq
}

func cliStoreReadAfter(t *testing.T, db *team.SQLiteStore, after int64, limit int) *wirePage {
	t.Helper()
	req := fmt.Sprintf(`{"op":"read-after","board_id":"shared","after_seq":%d,"limit":%d,"identity":{"member_id":"m","generation":1}}`, after, limit)
	_, resp := contractRun(t, db, req)
	if !resp.OK || resp.Page == nil {
		t.Fatalf("read-after(%d) rejected: %+v", after, resp)
	}
	return resp.Page
}

// TestCLIAfterSeqIncrementalNoReplay: paging covers every event exactly
// once; after the cursor advances to the end, the next read returns only
// events appended afterwards (route §3.2 no-reinjection).
func TestCLIAfterSeqIncrementalNoReplay(t *testing.T) {
	db := newCLIBoard(t)
	for i := 0; i < 50; i++ {
		cliStoreAppend(t, db, fmt.Sprintf("seed-%d", i))
	}
	after, seen := int64(0), 0
	for {
		page := cliStoreReadAfter(t, db, after, 10)
		seen += len(page.Events)
		if len(page.Events) > 10 {
			t.Fatalf("page of %d exceeds limit 10", len(page.Events))
		}
		if !page.HasMore {
			break
		}
		after = page.NextSeq
	}
	if seen != 50 {
		t.Fatalf("paged %d events, want 50", seen)
	}
	_, resp := contractRun(t, db, `{"op":"cursor","action":"advance","board_id":"shared","consumer_id":"m","generation":1,"last_seq":50}`)
	if !resp.OK {
		t.Fatalf("advance rejected: %+v", resp)
	}
	if page := cliStoreReadAfter(t, db, 50, 100); len(page.Events) != 0 {
		t.Fatalf("replayed %d events after cursor", len(page.Events))
	}
	cliStoreAppend(t, db, "new-1")
	cliStoreAppend(t, db, "new-2")
	page := cliStoreReadAfter(t, db, 50, 100)
	if len(page.Events) != 2 || page.Events[0].Seq != 51 || page.Events[1].Seq != 52 {
		t.Fatalf("after cursor got %d events, want the 2 new ones", len(page.Events))
	}
}

// TestCLIAfterSeqEdgeValues: a negative after_seq reads from the start
// (seq > after_seq admits everything) and an after_seq beyond the newest
// event is an empty, finished page — both must not break the gateway.
func TestCLIAfterSeqEdgeValues(t *testing.T) {
	db := newCLIBoard(t)
	for i := 0; i < 5; i++ {
		cliStoreAppend(t, db, fmt.Sprintf("e-%d", i))
	}
	if page := cliStoreReadAfter(t, db, -1, 100); len(page.Events) != 5 || page.HasMore {
		t.Fatalf("negative after_seq: %d events, has_more=%v; want all, done", len(page.Events), page.HasMore)
	}
	page := cliStoreReadAfter(t, db, 1_000_000, 100)
	if len(page.Events) != 0 || page.HasMore {
		t.Fatalf("past-end after_seq: %d events, has_more=%v; want empty, done", len(page.Events), page.HasMore)
	}
}

// TestCLICursorPersistsAcrossReopen: the cursor row and the no-replay
// guarantee survive a store reopen on the same db file (route P6.2).
func TestCLICursorPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.db")
	db := openStore(t, path)
	for i := 0; i < 5; i++ {
		cliStoreAppend(t, db, fmt.Sprintf("e-%d", i))
	}
	_, resp := contractRun(t, db, `{"op":"cursor","action":"advance","board_id":"shared","consumer_id":"m","generation":1,"last_seq":5}`)
	if !resp.OK {
		t.Fatalf("advance rejected: %+v", resp)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db = openStore(t, path)
	_, resp = contractRun(t, db, `{"op":"cursor","action":"get","board_id":"shared","consumer_id":"m"}`)
	if !resp.OK || resp.Cursor == nil || resp.Cursor.LastSeq != 5 {
		t.Fatalf("cursor after reopen: %+v", resp)
	}
	if page := cliStoreReadAfter(t, db, 5, 100); len(page.Events) != 0 {
		t.Fatalf("replayed %d events after reopen", len(page.Events))
	}
}
