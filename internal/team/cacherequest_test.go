package team

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// newCacheRequestStore returns an owner store plus an initialised owner, which
// is what a member's request log is written into.
func newCacheRequestStore(t *testing.T) (*OwnerStore, OwnerKey) {
	t.Helper()
	store, err := NewOwnerStore(filepath.Join(t.TempDir(), "team"))
	if err != nil {
		t.Fatal(err)
	}
	key := OwnerKey{TeamID: "alpha", MemberID: "ipc-protocol"}
	if _, _, err := store.Init(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	return store, key
}

// sampleCacheRequest is one complete record, so a round trip has every field to
// compare.
func sampleCacheRequest(observedAt time.Time) MemberCacheRequest {
	return MemberCacheRequest{
		RequestID: "turn:t1:7", RequestIDSource: "turn_event",
		ObservedAt: observedAt.UTC().Format(time.RFC3339Nano),
		TeamID:     "alpha", MemberID: "ipc-protocol",
		Provider: "deepseek", ModelRef: "deepseek/deepseek-v4-flash",
		TurnID: "t1", SessionSequence: 7, SessionRequestSeq: 3,
		SecondsSincePrevRequest: 12.5, HasPrevRequest: true,
		PromptTokens: 1000, ContextPromptTokens: 900,
		CacheHitTokens: 700, CacheMissTokens: 300, CacheWriteTokens: 120,
		CompletionTokens: 40, RequestCount: 1,
		UsageSource: "executor", FinishReason: "stop",
		ContextUsed: 900, ContextWindow: 1000000,
		DiagnosticsAvailable: true, PrefixHash: "aabbccdd", StablePrefixHash: "11223344",
		PrefixChanged: true, StablePrefixChanged: true,
		PrefixChangeReasons: []string{"tools"}, ToolSchemaTokensEstimate: 4200,
		SessionContextDigest: "deadbeef", SessionContextReasons: []string{"workspace"},
	}
}

// TestCacheRequestAppendAndReadRoundTrip is the storage contract: one appended
// record comes back with every field intact, stamped with the schema version and
// with the accounting verdict already computed.
func TestCacheRequestAppendAndReadRoundTrip(t *testing.T) {
	store, key := newCacheRequestStore(t)
	want := sampleCacheRequest(time.Now())
	if err := store.AppendCacheRequests(context.Background(), key, want); err != nil {
		t.Fatal(err)
	}
	got, err := store.ReadCacheRequests(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("read %d records, want 1", len(got))
	}
	want.Document = Document{SchemaVersion: SchemaVersion}
	want.AccountingValid = true
	if !reflect.DeepEqual(got[0], want) {
		t.Fatalf("round trip = %+v, want %+v", got[0], want)
	}
	if got[0].SchemaVersion != SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", got[0].SchemaVersion, SchemaVersion)
	}
}

// TestCacheRequestReadIsEmptyNotAnError pins the telemetry contract: every
// "cannot tell" answer is an empty result, so a reader that finds no log never
// fails closed on a member.
func TestCacheRequestReadIsEmptyNotAnError(t *testing.T) {
	store, key := newCacheRequestStore(t)
	got, err := store.ReadCacheRequests(context.Background(), key)
	if err != nil || len(got) != 0 {
		t.Fatalf("read of an unwritten log = (%v, %v), want (empty, nil)", got, err)
	}
	missing := OwnerKey{TeamID: "alpha", MemberID: "never-seen"}
	if got, err := store.ReadCacheRequests(context.Background(), missing); err != nil || len(got) != 0 {
		t.Fatalf("read of an absent owner = (%v, %v), want (empty, nil)", got, err)
	}
}

// TestCacheRequestAccountingIsReportedNeverCorrected covers the data-quality
// rule: a negative counter and a split that does not close against the prompt
// are both recorded as anomalies, and the values themselves are stored exactly
// as they arrived.
func TestCacheRequestAccountingIsReportedNeverCorrected(t *testing.T) {
	cases := []struct {
		name   string
		rec    MemberCacheRequest
		issues []string
	}{
		{
			name:   "closed",
			rec:    MemberCacheRequest{PromptTokens: 1000, CacheHitTokens: 700, CacheMissTokens: 300, RequestCount: 1},
			issues: nil,
		},
		{
			name:   "split exceeds prompt",
			rec:    MemberCacheRequest{PromptTokens: 100, CacheHitTokens: 80, CacheMissTokens: 50, RequestCount: 1},
			issues: []string{"split_exceeds_prompt"},
		},
		{
			name:   "negative counters",
			rec:    MemberCacheRequest{PromptTokens: -1, CacheMissTokens: -2, RequestCount: 1},
			issues: []string{"negative:prompt_tokens", "negative:cache_miss_tokens"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			valid, issues := tc.rec.Accounting()
			if valid != (len(tc.issues) == 0) {
				t.Fatalf("Accounting() valid = %v, want %v", valid, len(tc.issues) == 0)
			}
			if strings.Join(issues, ",") != strings.Join(tc.issues, ",") {
				t.Fatalf("Accounting() issues = %v, want %v", issues, tc.issues)
			}
		})
	}

	store, key := newCacheRequestStore(t)
	broken := MemberCacheRequest{
		ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), TeamID: "alpha", MemberID: "ipc-protocol",
		PromptTokens: 100, CacheHitTokens: 80, CacheMissTokens: 50, RequestCount: 1,
	}
	if err := store.AppendCacheRequests(context.Background(), key, broken); err != nil {
		t.Fatal(err)
	}
	got, err := store.ReadCacheRequests(context.Background(), key)
	if err != nil || len(got) != 1 {
		t.Fatalf("read = (%v, %v)", got, err)
	}
	if got[0].AccountingValid {
		t.Fatal("an unclosed split must be stored as accounting-invalid")
	}
	if got[0].PromptTokens != 100 || got[0].CacheMissTokens != 50 {
		t.Fatalf("a recorded anomaly must keep the reported values, got %+v", got[0])
	}
}

// TestCacheRequestCacheWriteIsNotAddedToMiss pins the accounting convention the
// whole report rests on: a cache write is already inside the miss count, so
// adding it again would inflate the denominator and depress every rate.
func TestCacheRequestCacheWriteIsNotAddedToMiss(t *testing.T) {
	rec := sampleCacheRequest(time.Now())
	rec.CacheWriteTokens = rec.CacheMissTokens
	valid, issues := rec.Accounting()
	if !valid {
		t.Fatalf("a cache write equal to the miss count is the normal case, got issues %v", issues)
	}
	totals := CacheTokenTotals{HitTokens: rec.CacheHitTokens, MissTokens: rec.CacheMissTokens}
	if rate, ok := totals.Rate(); !ok || rate != 0.7 {
		t.Fatalf("rate = (%v, %v), want (0.7, true): a write must not change the denominator", rate, ok)
	}
}

// TestCacheRequestReadSkipsCorruptLines pins the bounded-damage rule: a torn
// trailing line left by a crash mid-append is skipped, and the intact records
// around it are still read.
func TestCacheRequestReadSkipsCorruptLines(t *testing.T) {
	store, key := newCacheRequestStore(t)
	rec := sampleCacheRequest(time.Now())
	line, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Join([]string{string(line), `{"request_id": "torn`, string(line)}, "\n")
	path, err := store.cacheRequestRel(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Root(), path), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := store.ReadCacheRequests(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d records, want the 2 intact ones", len(got))
	}
}

// TestCacheRequestLogCompactsPastItsCeiling pins the bound: once an append
// pushes the log past its ceiling, the oldest records are dropped and the newest
// survive. The log must not grow with member lifetime.
func TestCacheRequestLogCompactsPastItsCeiling(t *testing.T) {
	store, key := newCacheRequestStore(t)
	path, err := store.cacheRequestRel(key)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// Fill the log directly past the ceiling with records that are all inside
	// the retention window, so only the size bound can shrink it.
	var buf strings.Builder
	for buf.Len() < cacheRequestMaxBytes+64*1024 {
		rec := sampleCacheRequest(now)
		rec.RequestID = rec.RequestID + strings.Repeat("x", 512)
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(store.Root(), path), []byte(buf.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	newest := sampleCacheRequest(now)
	newest.RequestID = "newest-survivor"
	if err := store.AppendCacheRequests(context.Background(), key, newest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(store.Root(), path))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > cacheRequestMaxBytes {
		t.Fatalf("log is %d bytes, want it compacted under %d", info.Size(), cacheRequestMaxBytes)
	}
	got, err := store.ReadCacheRequests(context.Background(), key)
	if err != nil || len(got) == 0 {
		t.Fatalf("read = (%d records, %v)", len(got), err)
	}
	if got[len(got)-1].RequestID != "newest-survivor" {
		t.Fatalf("compaction dropped the newest record: last = %q", got[len(got)-1].RequestID)
	}
}

// TestCacheRequestLogDropsExpiredAndUnstampedRecords pins retention: a record
// past the window is dropped at the next compaction, and one whose stamp cannot
// be parsed is dropped too, because it could never be aged.
func TestCacheRequestLogDropsExpiredAndUnstampedRecords(t *testing.T) {
	now := time.Now()
	expired := sampleCacheRequest(now.Add(-2 * cacheRequestRetention))
	unstamped := sampleCacheRequest(now)
	unstamped.ObservedAt = "not-a-timestamp"
	fresh := sampleCacheRequest(now)
	kept := retainedCacheRequests([]MemberCacheRequest{expired, unstamped, fresh}, now)
	if len(kept) != 1 || kept[0].ObservedAt != fresh.ObservedAt {
		t.Fatalf("retained %d records (%+v), want only the fresh one", len(kept), kept)
	}
}

// TestCacheRequestLogStaysInsideItsOwnerDirectory pins confinement: the log is
// a sibling of .usage.json inside the member's own directory, so clearing a
// member takes its observations with it.
func TestCacheRequestLogStaysInsideItsOwnerDirectory(t *testing.T) {
	store, key := newCacheRequestStore(t)
	paths, err := store.Paths(key)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := store.cacheRequestRel(key)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(filepath.Join(store.Root(), rel)) != paths.Dir {
		t.Fatalf("log dir = %q, want the owner dir %q", filepath.Dir(rel), paths.Dir)
	}
	if !strings.HasPrefix(cacheRequestFile, ".") {
		t.Fatalf("log file %q must stay a dot-prefixed control file like .usage.json", cacheRequestFile)
	}
}

// TestCacheRequestRejectsAnUnsafeKey pins path safety: an owner key that could
// escape the store root is refused rather than written through.
func TestCacheRequestRejectsAnUnsafeKey(t *testing.T) {
	store, _ := newCacheRequestStore(t)
	bad := OwnerKey{TeamID: "..", MemberID: "escape"}
	err := store.AppendCacheRequests(context.Background(), bad, sampleCacheRequest(time.Now()))
	if err == nil {
		t.Fatal("an escaping owner key must be refused")
	}
	if got, err := store.ReadCacheRequests(context.Background(), bad); err != nil || len(got) != 0 {
		t.Fatalf("read of an unsafe key = (%v, %v), want (empty, nil)", got, err)
	}
}
