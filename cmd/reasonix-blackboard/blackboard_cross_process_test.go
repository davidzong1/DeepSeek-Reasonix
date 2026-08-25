package main

// P8 cross-process acceptance (route §9): durability, dual-write and
// cursor claims are exercised across real process boundaries — a freshly
// compiled CLI binary and a SIGKILLed helper (route P6.1 subprocess contract).

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/team"
)

// cliPath is the reasonix-blackboard binary built once for all tests.
var cliPath string

// TestMain builds the CLI binary once. The kill test re-executes this same
// test binary as its subprocess, so building a separate executable keeps
// every other test on the production subprocess contract.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "blackboard-cli-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	cliPath = filepath.Join(dir, "reasonix-blackboard")
	cmd := exec.Command("go", "build", "-o", cliPath, ".")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build reasonix-blackboard: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// cliRun executes one JSON request against a fresh CLI process. Each call
// is a separate process; the durable state is the database file, which is
// exactly what the assertions below depend on.
func cliRun(t *testing.T, dbPath, in string) response {
	t.Helper()
	cmd := exec.Command(cliPath, "-db", dbPath)
	cmd.Stdin = strings.NewReader(in)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("cli(%s): %v\n%s", in, err, out)
	}
	var resp response
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode %s: %v\n%s", in, err, out)
	}
	return resp
}

// cliAppend runs one append through a fresh CLI process.
func cliAppend(t *testing.T, db, msgID, member string, gen uint64, summary string) response {
	t.Helper()
	in := fmt.Sprintf(`{"op":"append","board_id":"shared","client_msg_id":%q,`+
		`"kind":"report","task_id":"t1","summary":%q,`+
		`"identity":{"member_id":%q,"role":"coder","agent":"claude","generation":%d}}`,
		msgID, summary, member, gen)
	return cliRun(t, db, in)
}

// cliReadAfter runs one incremental read through a fresh CLI process.
func cliReadAfter(t *testing.T, db string, after int64) *wirePage {
	t.Helper()
	resp := cliRun(t, db, fmt.Sprintf(`{"op":"read-after","board_id":"shared",`+
		`"after_seq":%d,"identity":{"member_id":"m1","generation":1}}`, after))
	if !resp.OK || resp.Page == nil {
		t.Fatalf("read-after: %+v", resp)
	}
	return resp.Page
}

// wireToEvent converts a wire event back to the store type for the
// dual-write gate, which inspects seq and stamped identity.
func wireToEvent(w wireEvent) team.BoardEvent {
	return team.BoardEvent{
		SchemaVersion: w.SchemaVersion, BoardID: w.BoardID, Seq: w.Seq,
		EventID: w.EventID, ClientMsgID: w.ClientMsgID, Kind: team.EventKind(w.Kind),
		TaskID: team.TaskID(w.TaskID), MemberID: w.MemberID, Role: w.Role,
		Agent: w.Agent, Generation: w.Generation, CreatedAt: w.CreatedAt,
		Digest: w.Digest, Summary: w.Summary,
	}
}

// cliBoardEvents reads the full board through the CLI as store events.
func cliBoardEvents(t *testing.T, db string) []team.BoardEvent {
	t.Helper()
	page := cliReadAfter(t, db, 0)
	out := make([]team.BoardEvent, 0, len(page.Events))
	for i := range page.Events {
		out = append(out, wireToEvent(page.Events[i]))
	}
	return out
}

// TestBlackboardKillHelper is the subprocess half of the kill test: it
// commits three events, signals READY on stdout, then parks without Close
// until the parent SIGKILLs it.
func TestBlackboardKillHelper(t *testing.T) {
	if os.Getenv("BLACKBOARD_KILL_HELPER") != "1" {
		t.Skip("helper process only")
	}
	db := os.Getenv("BLACKBOARD_KILL_DB")
	s, err := team.NewSQLiteStore(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := s.Append(context.Background(), team.AppendInput{
			BoardID: team.BoardShared, ClientMsgID: fmt.Sprintf("k%d", i),
			Kind: team.EventReport, TaskID: "t1", Summary: "s",
			Stamped: team.Identity{MemberID: "m1", Generation: 1},
		}); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println("READY")
	select {}
}

// TestBlackboardKillReopen matches route §2.4: SIGKILL (no graceful Close,
// no deferred sync) must not lose committed events — WAL recovery hands
// them back on reopen.
func TestBlackboardKillReopen(t *testing.T) {
	db := filepath.Join(t.TempDir(), "board.db")
	cmd := exec.Command(os.Args[0], "-test.run=TestBlackboardKillHelper")
	cmd.Env = append(os.Environ(),
		"BLACKBOARD_KILL_HELPER=1", "BLACKBOARD_KILL_DB="+db)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(stdout)
	if !sc.Scan() || sc.Text() != "READY" {
		t.Fatalf("helper did not reach ready: %v", sc.Err())
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()

	s, err := team.NewSQLiteStore(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	page, err := s.ReadAfter(context.Background(), team.BoardShared, 0,
		team.Filter{Stamped: team.Identity{MemberID: "m1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 3 {
		t.Fatalf("kill lost committed events: want 3, got %d", len(page.Events))
	}
}

// TestBlackboardDualWriteDigestSeq matches route §1.4: the Python side
// dual-writes every CLI response as a legacy results.jsonl line; the
// VerifyDualWrite gate (count + digest + stamped coverage) must pass and
// seqs must be contiguous 1..3.
func TestBlackboardDualWriteDigestSeq(t *testing.T) {
	db := filepath.Join(t.TempDir(), "board.db")
	var lines [][]byte
	for i := 1; i <= 3; i++ {
		msgID := fmt.Sprintf("rep-%d", i)
		resp := cliAppend(t, db, msgID, "m1", 1, fmt.Sprintf("result %d", i))
		if !resp.OK || resp.Event == nil {
			t.Fatalf("append %d rejected: %+v", i, resp)
		}
		if resp.Event.Seq != int64(i) {
			t.Fatalf("seq gap at %d: got %d", i, resp.Event.Seq)
		}
		line, err := json.Marshal(map[string]string{
			"timestamp": time.Now().UTC().Format(time.RFC3339),
			"member":    "m1",
			"result":    fmt.Sprintf("result %d", i),
			"report_id": msgID,
		})
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	plan, err := team.PlanMigration(lines)
	if err != nil {
		t.Fatal(err)
	}
	if err := team.VerifyDualWrite(plan, lines, cliBoardEvents(t, db)); err != nil {
		t.Fatalf("dual-write gate: %v", err)
	}
}

// TestBlackboardStaleGenerationAppendDenied matches route §4.3: a window
// superseded by a newer generation cannot report — handleAppend's
// CheckGeneration gate (persisted binding, route §9.1 item 3) rejects it.
func TestBlackboardStaleGenerationAppendDenied(t *testing.T) {
	db := filepath.Join(t.TempDir(), "board.db")
	if resp := cliAppend(t, db, "g2", "m1", 2, "new window"); !resp.OK {
		t.Fatalf("current generation append rejected: %+v", resp)
	}
	resp := cliAppend(t, db, "g1", "m1", 1, "stale window")
	if resp.OK {
		t.Fatal("stale-generation append must be rejected, got accepted")
	}
	if resp.Error == nil || resp.Error.Kind != "stale-generation" {
		t.Fatalf("stale-generation append: got %+v, want stale-generation error", resp.Error)
	}
}

// TestBlackboardCursorZeroReplay matches route §2.3/§3.4: process A
// appends five events and persists cursor m1=5; process B (fresh CLI, same
// db) appends two more and reads only the delta — no replay — and the
// cursor row survives the reopen.
func TestBlackboardCursorZeroReplay(t *testing.T) {
	db := filepath.Join(t.TempDir(), "board.db")
	for i := 1; i <= 5; i++ {
		cliAppend(t, db, fmt.Sprintf("c%d", i), "m1", 1, "s")
	}
	resp := cliRun(t, db, fmt.Sprintf(`{"op":"cursor","action":"advance",`+
		`"board_id":"shared","consumer_id":"m1","generation":1,"last_seq":5}`))
	if !resp.OK {
		t.Fatalf("cursor advance: %+v", resp)
	}
	cliAppend(t, db, "c6", "m1", 1, "s")
	cliAppend(t, db, "c7", "m1", 1, "s")
	page := cliReadAfter(t, db, 5)
	if page.NeedResync {
		t.Fatal("no archive, must not resync")
	}
	if len(page.Events) != 2 || page.Events[0].Seq != 6 || page.Events[1].Seq != 7 {
		t.Fatalf("delta replay: want exactly events 6,7, got %+v", page.Events)
	}
	if page.NextSeq != 7 {
		t.Fatalf("next_seq: got %d, want 7", page.NextSeq)
	}
	cur := cliRun(t, db, `{"op":"cursor","action":"get",`+
		`"board_id":"shared","consumer_id":"m1"}`)
	if cur.Cursor == nil || cur.Cursor.LastSeq != 5 {
		t.Fatalf("cursor not persisted across processes: %+v", cur.Cursor)
	}
}

// TestBlackboardBindPersistsAcrossProcesses matches route §4.3: each cliRun
// is a fresh process, so a bind seen by a later process proves the record
// survives in board_bindings — restore, generation take-over, and unbind
// replay all cross process boundaries.
func TestBlackboardBindPersistsAcrossProcesses(t *testing.T) {
	db := filepath.Join(t.TempDir(), "board.db")
	bind := func(member, leader string, gen uint64, task string) response {
		t.Helper()
		return cliRun(t, db, fmt.Sprintf(`{"op":"bind","action":"bind","member_id":%q,`+
			`"task_id":%q,"identity":{"member_id":%q,"role":"leader","generation":%d}}`,
			member, task, leader, gen))
	}

	// Process A: first bind lands durably.
	if resp := bind("m1", "leader", 1, "t1"); !resp.OK || resp.Record == nil ||
		resp.Record.Generation != 1 || resp.Record.Status != "bound" {
		t.Fatalf("first bind: %+v", resp)
	}
	// Process B: a fresh registry restores the record.
	if resp := cliRun(t, db, `{"op":"bind","action":"get","member_id":"m1"}`); !resp.OK ||
		resp.Record == nil || resp.Record.LeaderID != "leader" || resp.Record.TaskID != "t1" {
		t.Fatalf("restored bind: %+v", resp)
	}
	if resp := cliRun(t, db, `{"op":"bind","action":"all"}`); !resp.OK ||
		len(resp.Records) != 1 || resp.Records[0].MemberID != "m1" {
		t.Fatalf("restored roster: %+v", resp)
	}
	// Process C: a higher generation rebind replaces the record durably.
	if resp := bind("m1", "leader2", 2, "t2"); !resp.OK ||
		resp.Record.Generation != 2 || resp.Record.LeaderID != "leader2" {
		t.Fatalf("take-over bind: %+v", resp)
	}
	// Process D: the take-over survived.
	if resp := cliRun(t, db, `{"op":"bind","action":"get","member_id":"m1"}`); !resp.OK ||
		resp.Record.Generation != 2 || resp.Record.TaskID != "t2" {
		t.Fatalf("take-over persisted: %+v", resp)
	}
	// Process E: unbind with a matching handoff persists as unbound.
	un := cliRun(t, db, `{"op":"bind","action":"unbind","member_id":"m1",`+
		`"identity":{"member_id":"leader2","generation":2},`+
		`"handoff":{"task_id":"t2","digest":"abc"}}`)
	if !un.OK || un.Record == nil || un.Record.Status != "unbound" {
		t.Fatalf("unbind: %+v", un)
	}
	// Process F: the unbind replays — the record reads back unbound, and a
	// same-task unbind is an idempotent replay, not ErrNotBound.
	if resp := cliRun(t, db, `{"op":"bind","action":"get","member_id":"m1"}`); !resp.OK ||
		resp.Record == nil || resp.Record.Status != "unbound" {
		t.Fatalf("unbind persisted: %+v", resp)
	}
	if resp := cliRun(t, db, `{"op":"bind","action":"unbind","member_id":"m1",`+
		`"identity":{"member_id":"leader2","generation":2},`+
		`"handoff":{"task_id":"t2","digest":"abc"}}`); !resp.OK {
		t.Fatalf("idempotent unbind replay: %+v", resp)
	}
}

// TestBlackboardLegacyCompat matches route §1.4/§6.4: real results.jsonl
// lines (artifact_path/compressed_context_path/report_id included) parse,
// import into the CLI board, and satisfy the dual-write gate.
func TestBlackboardLegacyCompat(t *testing.T) {
	raw := `{"timestamp":"2026-08-24T10:00:00Z","member":"alice",` +
		`"result":"done","artifact_path":"/tmp/a.md",` +
		`"compressed_context_path":"/tmp/c.md","report_id":"rep-1"}`
	rec, err := team.ParseLegacyLine([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Member != "alice" || rec.Result != "done" || rec.Artifact != "/tmp/a.md" {
		t.Fatalf("legacy line misparsed: %+v", rec)
	}
	db := filepath.Join(t.TempDir(), "board.db")
	if resp := cliAppend(t, db, "rep-1", "alice", 1, "done"); !resp.OK {
		t.Fatalf("append: %+v", resp)
	}
	lines := [][]byte{[]byte(raw)}
	plan, err := team.PlanMigration(lines)
	if err != nil {
		t.Fatal(err)
	}
	if err := team.VerifyDualWrite(plan, lines, cliBoardEvents(t, db)); err != nil {
		t.Fatalf("dual-write gate: %v", err)
	}
}
