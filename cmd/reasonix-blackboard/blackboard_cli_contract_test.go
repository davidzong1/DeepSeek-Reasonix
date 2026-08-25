package main

// P8 contract tests for the P6.1 JSON wire protocol (route §9): the
// error-kind mapping, field round-trips, report_id idempotency and per-op
// response shapes are the parts the Python gateway depends on.

import (
	"testing"
	"time"
)

// TestCLIErrorKindMatrix: every business rejection is a response with the
// documented error.kind, never a process failure.
func TestCLIErrorKindMatrix(t *testing.T) {
	cases := []struct {
		name string
		req  string
		kind string
	}{
		{"private non-owner append", `{"op":"append","board_id":"private/alice","client_msg_id":"x","kind":"report","summary":"s","identity":{"member_id":"bob","generation":1}}`, "forbidden"},
		{"missing identity", `{"op":"append","board_id":"shared","client_msg_id":"x","kind":"report","summary":"s"}`, "invalid-request"},
		{"unknown bind action", `{"op":"bind","action":"frobnicate"}`, "invalid-request"},
		{"cursor get without row", `{"op":"cursor","action":"get","board_id":"shared","consumer_id":"nobody"}`, "cursor-not-found"},
		{"bind get without record", `{"op":"bind","action":"get","member_id":"nobody"}`, "not-found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newCLIBoard(t)
			_, resp := contractRun(t, db, tc.req)
			if resp.OK || resp.Error == nil || resp.Error.Kind != tc.kind {
				t.Fatalf("got ok=%v error=%+v, want kind %q", resp.OK, resp.Error, tc.kind)
			}
		})
	}
}

// TestCLIConclusionConflictDetail: a CAS miss surfaces the current epoch
// and seq in error.detail so the caller can re-read before superseding.
func TestCLIConclusionConflictDetail(t *testing.T) {
	db := newCLIBoard(t)
	_, resp := contractRun(t, db, `{"op":"append","board_id":"shared","client_msg_id":"c1","kind":"conclusion","task_id":"t","summary":"v1","conclusion":{"topic":"plan","base_epoch":0,"summary":"v1"},"identity":{"member_id":"m","generation":1}}`)
	if !resp.OK || resp.Event == nil {
		t.Fatalf("first conclusion rejected: %+v", resp)
	}
	_, resp = contractRun(t, db, `{"op":"append","board_id":"shared","client_msg_id":"c2","kind":"conclusion","task_id":"t","summary":"v2","conclusion":{"topic":"plan","base_epoch":0,"summary":"v2"},"identity":{"member_id":"m","generation":1}}`)
	if resp.OK || resp.Error == nil || resp.Error.Kind != "conflict" {
		t.Fatalf("stale conclusion: got %+v, want kind conflict", resp)
	}
	if resp.Error.Detail["current_epoch"] == nil || resp.Error.Detail["current_seq"] == nil {
		t.Fatalf("conflict detail missing epoch/seq: %+v", resp.Error.Detail)
	}
}

// TestCLICursorBackwardsKind: advancing below the persisted position is a
// cursor-backwards response, not a crash.
func TestCLICursorBackwardsKind(t *testing.T) {
	db := newCLIBoard(t)
	_, resp := contractRun(t, db, `{"op":"cursor","action":"advance","board_id":"shared","consumer_id":"m","generation":1,"last_seq":10}`)
	if !resp.OK {
		t.Fatalf("first advance rejected: %+v", resp)
	}
	_, resp = contractRun(t, db, `{"op":"cursor","action":"advance","board_id":"shared","consumer_id":"m","generation":1,"last_seq":5}`)
	if resp.OK || resp.Error == nil || resp.Error.Kind != "cursor-backwards" {
		t.Fatalf("backwards advance: got %+v, want kind cursor-backwards", resp)
	}
}

// TestCLIAppendWireRoundTrip: artifact refs, supersedes, conclusion and
// identity survive the JSON trip and read back identical (route P6.1).
func TestCLIAppendWireRoundTrip(t *testing.T) {
	db := newCLIBoard(t)
	_, resp := contractRun(t, db, `{"op":"append","board_id":"shared","client_msg_id":"r1","kind":"conclusion","task_id":"t9","event_id":"ev-1","created_at":"2026-08-24T12:00:00Z","summary":"s","artifact_refs":[{"name":"a","path":"/tmp/a","size":7,"digest":"abc"}],"supersedes":[3,4],"conclusion":{"topic":"t","base_epoch":0,"summary":"s"},"identity":{"member_id":"m1","role":"tester","agent":"claude","generation":2}}`)
	if !resp.OK || resp.Event == nil {
		t.Fatalf("append rejected: %+v", resp)
	}
	ev := resp.Event
	if ev.BoardID != "shared" || ev.ClientMsgID != "r1" || ev.EventID != "ev-1" || ev.Kind != "conclusion" || ev.TaskID != "t9" {
		t.Fatalf("event fields lost: %+v", ev)
	}
	if ev.Role != "tester" || ev.Agent != "claude" || ev.Generation != 2 {
		t.Fatalf("identity fields lost: %+v", ev)
	}
	if len(ev.ArtifactRefs) != 1 || ev.ArtifactRefs[0].Path != "/tmp/a" || ev.ArtifactRefs[0].Digest != "abc" {
		t.Fatalf("artifact refs lost: %+v", ev.ArtifactRefs)
	}
	if len(ev.Supersedes) != 2 || ev.Supersedes[0] != 3 || ev.Supersedes[1] != 4 {
		t.Fatalf("supersedes lost: %+v", ev.Supersedes)
	}
	_, resp = contractRun(t, db, `{"op":"read-after","board_id":"shared","after_seq":0,"identity":{"member_id":"m1","generation":2}}`)
	if !resp.OK || resp.Page == nil || len(resp.Page.Events) != 1 {
		t.Fatalf("read-back failed: %+v", resp)
	}
	back := resp.Page.Events[0]
	if back.Digest != ev.Digest || back.CreatedAt.Unix() != ev.CreatedAt.Unix() || len(back.ArtifactRefs) != 1 || len(back.Supersedes) != 2 {
		t.Fatalf("read-back diverged: %+v vs %+v", back, ev)
	}
}

// TestCLIReportIDIdempotentReplay: the Python gateway's report_id is the
// wire client_msg_id — replaying the same report_id returns the original
// event (same seq, digest, event_id) and distinct report_ids are distinct
// events (route §2.2).
func TestCLIReportIDIdempotentReplay(t *testing.T) {
	db := newCLIBoard(t)
	req := `{"op":"append","board_id":"shared","client_msg_id":"report:42","kind":"report","task_id":"t","summary":"s","identity":{"member_id":"m","generation":1}}`
	_, resp := contractRun(t, db, req)
	if !resp.OK || resp.Event == nil {
		t.Fatalf("first report rejected: %+v", resp)
	}
	want := resp.Event
	_, resp = contractRun(t, db, req)
	if !resp.OK || resp.Event == nil {
		t.Fatalf("replay rejected: %+v", resp)
	}
	if resp.Event.Seq != want.Seq || resp.Event.Digest != want.Digest || resp.Event.EventID != want.EventID {
		t.Fatalf("replay not original: %+v vs %+v", resp.Event, want)
	}
	_, resp = contractRun(t, db, `{"op":"append","board_id":"shared","client_msg_id":"report:43","kind":"report","task_id":"t","summary":"s2","identity":{"member_id":"m","generation":1}}`)
	if !resp.OK || resp.Event.Seq != want.Seq+1 {
		t.Fatalf("distinct report_id: got seq %d, want %d", resp.Event.Seq, want.Seq+1)
	}
}

// TestCLICreatedAtEmptyDefaultsNow: an absent created_at stamps the
// current UTC time, RFC3339Nano-parseable (route P6.1).
func TestCLICreatedAtEmptyDefaultsNow(t *testing.T) {
	db := newCLIBoard(t)
	before := time.Now().UTC().Add(-time.Minute)
	_, resp := contractRun(t, db, `{"op":"append","board_id":"shared","client_msg_id":"t1","kind":"report","summary":"s","identity":{"member_id":"m","generation":1}}`)
	if !resp.OK || resp.Event == nil {
		t.Fatalf("append rejected: %+v", resp)
	}
	ts := resp.Event.CreatedAt
	if ts.Before(before) || ts.After(time.Now().UTC().Add(time.Minute)) {
		t.Fatalf("created_at not ~now: %v", ts)
	}
}

// TestCLIResponseShapePerOp: ok responses carry exactly one payload field
// per op, so the gateway can dispatch without ambiguity.
func TestCLIResponseShapePerOp(t *testing.T) {
	cases := []struct {
		name string
		req  string
		has  func(*response) bool
	}{
		{"append", appendReq("s1", "hi"), func(r *response) bool {
			return r.Event != nil && r.Page == nil && r.View == nil && r.Record == nil && r.Cursor == nil && len(r.Records) == 0
		}},
		{"read-after", `{"op":"read-after","board_id":"shared","after_seq":0,"identity":{"member_id":"m","generation":1}}`, func(r *response) bool { return r.Page != nil && r.Event == nil && r.View == nil }},
		{"read-view", `{"op":"read-view","board_id":"shared","identity":{"member_id":"m","generation":1}}`, func(r *response) bool { return r.View != nil && r.Page == nil && r.Event == nil }},
		{"bind all", `{"op":"bind","action":"all"}`, func(r *response) bool { return len(r.Records) == 0 && r.Record == nil }},
		{"cursor advance", `{"op":"cursor","action":"advance","board_id":"shared","consumer_id":"m","generation":1,"last_seq":1}`, func(r *response) bool {
			return r.OK && r.Cursor == nil && r.Event == nil && r.Page == nil && r.View == nil && r.Record == nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newCLIBoard(t)
			_, resp := contractRun(t, db, tc.req)
			if !resp.OK {
				t.Fatalf("rejected: %+v", resp)
			}
			if !tc.has(&resp) {
				t.Fatalf("unexpected response shape: %+v", resp)
			}
		})
	}
	// cursor get needs a persisted row, so it runs on a db the advance
	// populated instead of a fresh one.
	t.Run("cursor get", func(t *testing.T) {
		db := newCLIBoard(t)
		_, resp := contractRun(t, db, `{"op":"cursor","action":"advance","board_id":"shared","consumer_id":"m","generation":1,"last_seq":1}`)
		if !resp.OK {
			t.Fatalf("advance rejected: %+v", resp)
		}
		_, resp = contractRun(t, db, `{"op":"cursor","action":"get","board_id":"shared","consumer_id":"m"}`)
		if !resp.OK || resp.Cursor == nil {
			t.Fatalf("cursor get: %+v", resp)
		}
		if resp.Cursor.LastSeq != 1 || resp.Event != nil || resp.Page != nil {
			t.Fatalf("unexpected response shape: %+v", resp)
		}
	})
}
