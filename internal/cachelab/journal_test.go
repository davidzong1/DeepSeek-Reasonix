package cachelab

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sample builds a valid sample for journal tests.
func sample(t *testing.T, arm string, seq, turn, hit, miss int) Sample {
	t.Helper()
	s := Sample{
		Arm: arm, Seq: seq, At: "2026-09-24T08:00:00Z", MemberID: "member",
		RequestHash: "hash", RequestBytes: 1024, TurnSeq: turn,
		Status: 200, UsageReported: true, UsageSplit: true,
		CacheHitTokens: hit, CacheMissTokens: miss, PromptTokens: hit + miss,
		QualityCheck: QualityPass,
	}
	if s.PromptTokens == 0 {
		s.PromptTokens = 1
	}
	return s
}

func TestJournalRoundTripsSamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []Sample{
		sample(t, "B1-baseline-repeat", 1, 1, 0, 100),
		sample(t, "B1-baseline-repeat", 2, 2, 90, 10),
	}
	for _, s := range want {
		if err := j.Append(s); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if j.Lines() != len(want) {
		t.Fatalf("lines = %d, want %d", j.Lines(), len(want))
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	got, skipped, err := ReadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 || len(got) != len(want) {
		t.Fatalf("read %d samples with %d skipped, want %d and 0", len(got), skipped, len(want))
	}
	for i := range want {
		if got[i].RequestHash != want[i].RequestHash || got[i].CacheHitTokens != want[i].CacheHitTokens {
			t.Fatalf("sample %d round-tripped as %+v", i, got[i])
		}
	}
}

func TestJournalRefusesGuardedContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	j.Guard("a stable prefix clause that never changes")
	leak := sample(t, "B1-baseline-repeat", 1, 1, 1, 1)
	leak.Error = "upstream refused: a stable prefix clause that never changes"
	if err := j.Append(leak); err == nil {
		t.Fatal("a line carrying a guarded literal must be refused, not stored")
	}
	if j.Lines() != 0 {
		t.Fatalf("lines = %d, want 0 after a refusal", j.Lines())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 0 {
		t.Fatalf("journal file holds %q after a refusal", raw)
	}
}

func TestJournalSkipsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	body := `{"arm":"a","seq":1,"at":"2026-09-24T08:00:00Z","status":200,"quality_check":"quality_pass","usage_reported":true}
{"arm":"a","seq":2,`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, skipped, err := ReadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || skipped != 1 {
		t.Fatalf("read %d samples with %d skipped, want 1 and 1", len(got), skipped)
	}
}

func TestSampleValidateRefusesIncompleteRecords(t *testing.T) {
	cases := map[string]Sample{
		"no arm":       {Seq: 1, At: "2026-09-24T08:00:00Z", Status: 200, QualityCheck: QualityPass},
		"no sequence":  {Arm: "a", At: "2026-09-24T08:00:00Z", Status: 200, QualityCheck: QualityPass},
		"no timestamp": {Arm: "a", Seq: 1, Status: 200, QualityCheck: QualityPass},
		"status zero":  {Arm: "a", Seq: 1, At: "2026-09-24T08:00:00Z", QualityCheck: QualityPass},
		"no digest":    {Arm: "a", Seq: 1, At: "2026-09-24T08:00:00Z", Status: 200, QualityCheck: QualityPass},
		"no quality":   {Arm: "a", Seq: 1, At: "2026-09-24T08:00:00Z", Status: 200, RequestHash: "h"},
	}
	for name, s := range cases {
		if err := s.Validate(); err == nil {
			t.Fatalf("%s: Validate accepted an incomplete sample", name)
		}
	}
	tolerated := Sample{Arm: "a", Seq: 1, At: "2026-09-24T08:00:00Z", Status: 502, Error: "dial refused", QualityCheck: QualityFail}
	if err := tolerated.Validate(); err != nil {
		t.Fatalf("a transport failure without a digest must validate: %v", err)
	}
}

func TestJournalRejectsShortGuardLiteral(t *testing.T) {
	j, err := OpenJournal(filepath.Join(t.TempDir(), "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	j.Guard("short")
	if len(j.guard) != 0 {
		t.Fatalf("guard = %v, want a short literal ignored", j.guard)
	}
	if !strings.Contains(j.Path(), "journal.jsonl") {
		t.Fatalf("path = %q", j.Path())
	}
}
