package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/team"
)

// contractRun decodes one request, runs it through Handle against a fresh
// store, and returns the decoded response. The store is per-test so boards
// never bleed between cases.
func contractRun(t *testing.T, db *team.SQLiteStore, in string) (*team.SQLiteStore, response) {
	t.Helper()
	out, err := Handle(context.Background(), db, team.NewBindingRegistry(), []byte(in))
	if err != nil {
		t.Fatalf("Handle(%s) failed: %v", in, err)
	}
	var resp response
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("response not decodable: %v\n%s", err, out)
	}
	return db, resp
}

func newCLIBoard(t *testing.T) *team.SQLiteStore {
	t.Helper()
	s, err := team.NewSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func appendReq(msgID, summary string) string {
	return `{"op":"append","board_id":"shared","client_msg_id":"` + msgID +
		`","kind":"report","task_id":"t1","summary":"` + summary +
		`","identity":{"member_id":"m1","role":"coder","agent":"claude","generation":1}}`
}

// TestCLIAppendStampsAndDigests: one append returns a fully stamped event
// with a 32-char digest and seq 1.
func TestCLIAppendStampsAndDigests(t *testing.T) {
	db := newCLIBoard(t)
	_, resp := contractRun(t, db, appendReq("a1", "hello"))
	if !resp.OK || resp.Event == nil {
		t.Fatalf("append rejected: %+v", resp)
	}
	ev := resp.Event
	if ev.Seq != 1 || ev.MemberID != "m1" || ev.Generation != 1 {
		t.Fatalf("event not stamped: %+v", ev)
	}
	if len(ev.Digest) != 32 {
		t.Fatalf("digest not 32 chars: %q", ev.Digest)
	}
	if ev.SchemaVersion != team.SchemaVersion || ev.Kind != "report" || ev.TaskID != "t1" {
		t.Fatalf("envelope wrong: %+v", ev)
	}
}

// TestCLIAppendIdempotentReplay: the same client_msg_id replays the
// original event without a new seq.
func TestCLIAppendIdempotentReplay(t *testing.T) {
	db := newCLIBoard(t)
	_, first := contractRun(t, db, appendReq("dup", "v1"))
	for i := 0; i < 3; i++ {
		_, again := contractRun(t, db, appendReq("dup", "v1"))
		if !again.OK || again.Event.Seq != first.Event.Seq || again.Event.Digest != first.Event.Digest {
			t.Fatalf("replay diverged: %+v vs %+v", again, first)
		}
	}
}

// TestCLIAppendRequiresIdentity: an append without identity is a request
// error, not a store rejection.
func TestCLIAppendRequiresIdentity(t *testing.T) {
	db := newCLIBoard(t)
	_, resp := contractRun(t, db, `{"op":"append","board_id":"shared","client_msg_id":"x","kind":"report"}`)
	if resp.OK || resp.Error == nil || resp.Error.Kind != "invalid-request" {
		t.Fatalf("want invalid-request, got %+v", resp)
	}
}

// TestCLIAppendPrivateForbidden: cross-member access to a private board is
// rejected with kind forbidden.
func TestCLIAppendPrivateForbidden(t *testing.T) {
	db := newCLIBoard(t)
	_, resp := contractRun(t, db,
		`{"op":"append","board_id":"private/alice","client_msg_id":"sneak","kind":"report",
		  "identity":{"member_id":"bob","generation":1}}`)
	if resp.OK || resp.Error.Kind != "forbidden" {
		t.Fatalf("want forbidden, got %+v", resp)
	}
}

// TestCLIAppendConclusionConflict: a CAS loss surfaces as kind conflict
// with the current epoch in detail.
func TestCLIAppendConclusionConflict(t *testing.T) {
	db := newCLIBoard(t)
	concl := func(msg, base string) string {
		return `{"op":"append","board_id":"shared","client_msg_id":"c` + msg +
			`","kind":"conclusion","task_id":"t1","conclusion":{"topic":"top","base_epoch":` +
			base + `,"summary":"` + msg + `"},"identity":{"member_id":"m1","generation":1}}`
	}
	_, win := contractRun(t, db, concl("v1", "0"))
	if !win.OK {
		t.Fatalf("first CAS lost: %+v", win)
	}
	_, lose := contractRun(t, db, concl("v2", "0"))
	if lose.OK || lose.Error.Kind != "conflict" {
		t.Fatalf("want conflict, got %+v", lose)
	}
	if lose.Error.Detail["current_epoch"] != float64(1) {
		t.Fatalf("conflict detail wrong: %+v", lose.Error.Detail)
	}
}

// TestCLIReadAfterPaging: 3 appends then a page of limit 2 shows has_more
// and a next_seq continuation.
func TestCLIReadAfterPaging(t *testing.T) {
	db := newCLIBoard(t)
	for i := 0; i < 3; i++ {
		contractRun(t, db, appendReq(string(rune('p'+i)), "s"))
	}
	_, resp := contractRun(t, db,
		`{"op":"read-after","board_id":"shared","after_seq":0,"limit":2,"identity":{"member_id":"m1"}}`)
	if !resp.OK || resp.Page == nil {
		t.Fatalf("read-after rejected: %+v", resp)
	}
	if len(resp.Page.Events) != 2 || !resp.Page.HasMore || resp.Page.NextSeq != 2 {
		t.Fatalf("page wrong: %+v", resp.Page)
	}
	_, next := contractRun(t, db,
		`{"op":"read-after","board_id":"shared","after_seq":2,"limit":2,"identity":{"member_id":"m1"}}`)
	if len(next.Page.Events) != 1 || next.Page.HasMore || next.Page.NextSeq != 3 {
		t.Fatalf("continuation wrong: %+v", next.Page)
	}
}

// TestCLIReadAfterNeedResync: a cursor inside an archived hole is flagged
// need_resync.
func TestCLIReadAfterNeedResync(t *testing.T) {
	db := newCLIBoard(t)
	for i := 0; i < 5; i++ {
		contractRun(t, db, appendReq(string(rune('q'+i)), "s"))
	}
	if err := db.ArchiveBefore(context.Background(), "shared", 3, team.Identity{MemberID: "leader", Role: "leader"}); err != nil {
		t.Fatal(err)
	}
	_, resp := contractRun(t, db,
		`{"op":"read-after","board_id":"shared","after_seq":2,"identity":{"member_id":"m1"}}`)
	if !resp.OK || !resp.Page.NeedResync {
		t.Fatalf("want need_resync, got %+v", resp.Page)
	}
}

// TestCLIReadView: a conclusion append is visible through read-view.
func TestCLIReadView(t *testing.T) {
	db := newCLIBoard(t)
	contractRun(t, db,
		`{"op":"append","board_id":"shared","client_msg_id":"cv1","kind":"conclusion","task_id":"t1",
		  "conclusion":{"topic":"top","base_epoch":0,"summary":"final"},
		  "identity":{"member_id":"m1","generation":1}}`)
	_, resp := contractRun(t, db,
		`{"op":"read-view","board_id":"shared","task_id":"t1","identity":{"member_id":"m1"}}`)
	if !resp.OK || resp.View == nil || len(resp.View.Conclusions) != 1 {
		t.Fatalf("view wrong: %+v", resp)
	}
	c := resp.View.Conclusions[0]
	if c.Topic != "top" || c.Epoch != 1 || c.Summary != "final" {
		t.Fatalf("conclusion row wrong: %+v", c)
	}
}

// TestCLIBindLifecycle: bind -> get -> all -> unbind round-trips inside one
// process; unbind requires a valid handoff.
func TestCLIBindLifecycle(t *testing.T) {
	db := newCLIBoard(t)
	reg := team.NewBindingRegistry()
	bindIn := `{"op":"bind","action":"bind","member_id":"m1","task_id":"t1",
	  "identity":{"member_id":"leader","role":"leader","generation":1}}`
	out, err := Handle(context.Background(), db, reg, []byte(bindIn))
	if err != nil {
		t.Fatal(err)
	}
	var resp response
	json.Unmarshal(out, &resp)
	if !resp.OK || resp.Record == nil || resp.Record.Status != "bound" || resp.Record.LeaderID != "leader" {
		t.Fatalf("bind failed: %+v", resp)
	}
	getIn := `{"op":"bind","action":"get","member_id":"m1"}`
	out, _ = Handle(context.Background(), db, reg, []byte(getIn))
	json.Unmarshal(out, &resp)
	if !resp.OK || resp.Record.TaskID != "t1" {
		t.Fatalf("get failed: %+v", resp)
	}
	unbindBad := `{"op":"bind","action":"unbind","member_id":"m1",
	  "identity":{"member_id":"leader","generation":1},
	  "handoff":{"task_id":"t9","digest":"d"}}`
	out, _ = Handle(context.Background(), db, reg, []byte(unbindBad))
	json.Unmarshal(out, &resp)
	if resp.OK || resp.Error.Kind != "invalid-handoff" {
		t.Fatalf("bad handoff want invalid-handoff, got %+v", resp)
	}
	unbindOK := `{"op":"bind","action":"unbind","member_id":"m1",
	  "identity":{"member_id":"leader","generation":1},
	  "handoff":{"task_id":"t1","digest":"d"}}`
	out, _ = Handle(context.Background(), db, reg, []byte(unbindOK))
	json.Unmarshal(out, &resp)
	if !resp.OK || resp.Record.Status != "unbound" {
		t.Fatalf("unbind failed: %+v", resp)
	}
}

// TestCLICursorAdvanceGet: advance persists a position, get reads it back;
// a backwards advance is rejected.
func TestCLICursorAdvanceGet(t *testing.T) {
	db := newCLIBoard(t)
	adv := `{"op":"cursor","action":"advance","board_id":"shared","consumer_id":"c1","generation":1,"last_seq":7}`
	_, resp := contractRun(t, db, adv)
	if !resp.OK {
		t.Fatalf("advance failed: %+v", resp)
	}
	_, resp = contractRun(t, db,
		`{"op":"cursor","action":"get","board_id":"shared","consumer_id":"c1"}`)
	if !resp.OK || resp.Cursor == nil || resp.Cursor.LastSeq != 7 || resp.Cursor.Generation != 1 {
		t.Fatalf("get wrong: %+v", resp)
	}
	_, resp = contractRun(t, db, adv)
	if !resp.OK {
		t.Fatalf("equal advance must be idempotent: %+v", resp)
	}
	_, resp = contractRun(t, db,
		`{"op":"cursor","action":"advance","board_id":"shared","consumer_id":"c1","generation":1,"last_seq":3}`)
	if resp.OK || resp.Error.Kind != "cursor-backwards" {
		t.Fatalf("want cursor-backwards, got %+v", resp)
	}
	_, resp = contractRun(t, db,
		`{"op":"cursor","action":"get","board_id":"shared","consumer_id":"nobody"}`)
	if resp.OK || resp.Error.Kind != "cursor-not-found" {
		t.Fatalf("want cursor-not-found, got %+v", resp)
	}
}

// TestCLIUnknownOpAndBadJSON: these are process-level failures (Handle
// returns an error), not responses.
func TestCLIUnknownOpAndBadJSON(t *testing.T) {
	db := newCLIBoard(t)
	reg := team.NewBindingRegistry()
	if _, err := Handle(context.Background(), db, reg, []byte(`{"op":"nope"}`)); err == nil {
		t.Fatal("unknown op must error")
	}
	if _, err := Handle(context.Background(), db, reg, []byte(`{not json`)); err == nil {
		t.Fatal("bad json must error")
	}
}

// TestCLIRunEndToEnd: run() with a real stdin writes response JSON to
// stdout and exits 0; missing -db exits 2.
func TestCLIRunEndToEnd(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "board.db")
	var out, errBuf bytes.Buffer
	code := run([]string{"-db", dbPath}, strings.NewReader(appendReq("e2e", "s")), &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errBuf.String())
	}
	var resp response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil || !resp.OK || resp.Event == nil {
		t.Fatalf("run output not a valid response: %v\n%s", err, out.String())
	}
	code = run(nil, strings.NewReader(`{}`), &out, &errBuf)
	if code != 2 {
		t.Fatalf("missing -db want exit 2, got %d", code)
	}
	code = run([]string{"-db", filepath.Join(t.TempDir(), "x", "nested", "board.db")}, strings.NewReader(`{}`), &out, &errBuf)
	if code != 1 {
		t.Fatalf("unopenable db want exit 1, got %d", code)
	}
}
