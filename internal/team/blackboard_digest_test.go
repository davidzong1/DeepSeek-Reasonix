package team

// Cross-implementation digest contract (route §1.1, P8): the digest is
// sha256 over the Go json encoding of the event; any side that recomputes
// it must serialize byte-identically, locked against a Python twin.

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// pythonDigestScript mirrors Go's json encoding of a BoardEvent, field by
// field, then hashes it. BoardEvent carries no json tags, so the digest
// input is the Go field names (PascalCase) — different from the wire's
// snake_case — every field is present (nil slices as null, digest empty),
// with no spaces, < > & escaped, and a trailing newline.
const pythonDigestScript = `
import hashlib, json, sys
schema = ["SchemaVersion","BoardID","Seq","EventID","ClientMsgID","Kind","TaskID",
          "MemberID","Role","Agent","Generation","CreatedAt","Digest","Summary",
          "ArtifactRefs","Supersedes"]
vals = json.load(sys.stdin)
ev = {k: v for k, v in zip(schema, vals)}
s = json.dumps(ev, ensure_ascii=False, separators=(",", ":"))
s = s.replace("<", "\\u003c").replace(">", "\\u003e").replace("&", "\\u0026")
print(hashlib.sha256((s + "\n").encode("utf-8")).hexdigest()[:32])
`

// digestFields lays the event out in Go struct order. Every field is
// present; empty refs/supersedes come as nil so they encode as null, as
// the digest input does. Refs pass through as the ArtifactRef slice — a
// hand-built map would reorder keys alphabetically and diverge from the
// store's struct-order encoding.
func digestFields(ev BoardEvent) []any {
	var refs any
	if len(ev.ArtifactRefs) > 0 {
		refs = ev.ArtifactRefs
	}
	var supersedes any
	if len(ev.Supersedes) > 0 {
		supersedes = ev.Supersedes
	}
	return []any{
		ev.SchemaVersion, ev.BoardID, ev.Seq, ev.EventID, ev.ClientMsgID,
		string(ev.Kind), string(ev.TaskID), ev.MemberID, ev.Role, ev.Agent,
		ev.Generation, ev.CreatedAt.Format(time.RFC3339Nano), ev.Digest, ev.Summary,
		refs, supersedes,
	}
}

// TestDigestCrossImplementationPython: a Python reimplementation of the
// encoding produces the identical digest for varied events, so any gateway
// side recomputing digests agrees with the store. Skipped where python3 is
// absent (CI without the server-side toolchain).
func TestDigestCrossImplementationPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	events := []BoardEvent{
		{SchemaVersion: SchemaVersion, BoardID: BoardShared, Seq: 7, EventID: "ev-1", ClientMsgID: "report:42",
			Kind: EventReport, TaskID: "t1", MemberID: "m1", Role: "tester", Agent: "claude",
			Generation: 2, CreatedAt: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC), Digest: "",
			Summary:      "hello 世界 <tag> & co",
			ArtifactRefs: []ArtifactRef{{Name: "a", Path: "/tmp/a", Size: 7, Digest: "abc"}},
			Supersedes:   []int64{3, 4}},
		{SchemaVersion: SchemaVersion, BoardID: BoardShared, Seq: 1, EventID: "ev-2", ClientMsgID: "plain",
			Kind: EventConclusion, TaskID: "t2", MemberID: "m2", Generation: 1,
			CreatedAt: time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC), Digest: "", Summary: "plain"},
		{SchemaVersion: SchemaVersion, BoardID: BoardPrivatePrefix + "alice", Seq: 3, EventID: "",
			ClientMsgID: "refs-only", Kind: EventEvidence, TaskID: "t3", MemberID: "alice",
			Generation: 5, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC), Digest: "",
			Summary: "", ArtifactRefs: []ArtifactRef{{Name: "x", Path: "/x"}}},
	}
	for i, ev := range events {
		fields, err := json.Marshal(digestFields(ev))
		if err != nil {
			t.Fatalf("case %d: marshal fields: %v", i, err)
		}
		cmd := exec.Command("python3", "-c", pythonDigestScript)
		cmd.Stdin = bytes.NewReader(fields)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("case %d: python digest failed: %v", i, err)
		}
		got := strings.TrimSpace(string(out))
		if want := digestOf(ev); got != want {
			t.Fatalf("case %d: python digest %q != store digest %q", i, got, want)
		}
	}
}

// TestDigestChangesOnAnyClientField: every client-visible field is inside
// the hash, so a revision is always detectable without comparing payloads
// (route §1.1). The digest field itself is excluded — it is empty at
// stamp time by construction.
func TestDigestChangesOnAnyClientField(t *testing.T) {
	base := BoardEvent{SchemaVersion: SchemaVersion, BoardID: BoardShared, Seq: 1,
		EventID: "e", ClientMsgID: "c", Kind: EventReport, TaskID: "t", MemberID: "m",
		Generation: 1, CreatedAt: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC), Summary: "s"}
	want := digestOf(base)
	mutate := func(f func(*BoardEvent)) string {
		ev := base
		f(&ev)
		return digestOf(ev)
	}
	for _, tc := range []struct {
		name string
		f    func(*BoardEvent)
	}{
		{"board", func(e *BoardEvent) { e.BoardID = "shared2" }},
		{"seq", func(e *BoardEvent) { e.Seq = 2 }},
		{"event id", func(e *BoardEvent) { e.EventID = "other" }},
		{"client msg id", func(e *BoardEvent) { e.ClientMsgID = "other" }},
		{"kind", func(e *BoardEvent) { e.Kind = EventCheckpoint }},
		{"task", func(e *BoardEvent) { e.TaskID = "t2" }},
		{"member", func(e *BoardEvent) { e.MemberID = "other" }},
		{"role", func(e *BoardEvent) { e.Role = "other" }},
		{"agent", func(e *BoardEvent) { e.Agent = "other" }},
		{"generation", func(e *BoardEvent) { e.Generation = 9 }},
		{"created at", func(e *BoardEvent) { e.CreatedAt = e.CreatedAt.Add(time.Second) }},
		{"summary", func(e *BoardEvent) { e.Summary = "other" }},
		{"artifact refs", func(e *BoardEvent) { e.ArtifactRefs = []ArtifactRef{{Name: "n", Path: "p"}} }},
		{"supersedes", func(e *BoardEvent) { e.Supersedes = []int64{1} }},
	} {
		if got := mutate(tc.f); got == want {
			t.Errorf("%s change did not alter digest", tc.name)
		}
	}
}
