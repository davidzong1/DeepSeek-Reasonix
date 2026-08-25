package team

import (
	"encoding/json"
	"errors"
	"testing"
)

func makeLegacyLine(t *testing.T, member, ts string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"timestamp": ts,
		"member":    member,
		"result":    "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseLegacyLine(t *testing.T) {
	rec, err := ParseLegacyLine(makeLegacyLine(t, "alice", "2026-08-20T20:50:10.527892Z"))
	if err != nil {
		t.Fatalf("ParseLegacyLine: %v", err)
	}
	if rec.Member != "alice" || rec.Result != "ok" || rec.Verified {
		t.Errorf("record = %+v", rec)
	}

	if _, err := ParseLegacyLine([]byte(`{"timestamp":"2026-08-20T20:50:10Z"}`)); err == nil {
		t.Error("missing member accepted")
	}
	if _, err := ParseLegacyLine([]byte(`{"member":"alice"}`)); err == nil {
		t.Error("missing timestamp accepted")
	}
	if _, err := ParseLegacyLine([]byte(`{"timestamp":"not-a-time","member":"alice"}`)); err == nil {
		t.Error("bad timestamp accepted")
	}
	if _, err := ParseLegacyLine([]byte(`{broken`)); err == nil {
		t.Error("broken json accepted")
	}
}

func TestPlanMigration(t *testing.T) {
	lines := [][]byte{
		makeLegacyLine(t, "alice", "2026-08-20T20:50:10.5Z"),
		makeLegacyLine(t, "bob", "2026-08-20T21:00:00Z"),
		[]byte(`{broken`),
	}
	plan, err := PlanMigration(lines)
	if err != nil {
		t.Fatalf("PlanMigration: %v", err)
	}
	if plan.Lines != 3 || plan.Imported != 2 || plan.Failed != 1 || plan.Digest == "" {
		t.Errorf("plan = %+v", plan)
	}
	plan2, _ := PlanMigration(lines)
	if plan2.Digest != plan.Digest {
		t.Errorf("digest unstable: %q vs %q", plan.Digest, plan2.Digest)
	}
}

func TestVerifyDualWrite(t *testing.T) {
	lines := [][]byte{
		makeLegacyLine(t, "alice", "2026-08-20T20:50:10.5Z"),
		makeLegacyLine(t, "bob", "2026-08-20T21:00:00Z"),
	}
	plan, err := PlanMigration(lines)
	if err != nil {
		t.Fatal(err)
	}
	events := []BoardEvent{
		{Seq: 1, MemberID: "alice"},
		{Seq: 2, MemberID: "bob"},
	}
	if err := VerifyDualWrite(plan, lines, events); err != nil {
		t.Fatalf("consistent dual-write rejected: %v", err)
	}

	if err := VerifyDualWrite(plan, lines, events[:1]); !errors.Is(err, ErrMigrationMismatch) {
		t.Errorf("count mismatch = %v, want ErrMigrationMismatch", err)
	}
	events[1].MemberID = ""
	if err := VerifyDualWrite(plan, lines, events); !errors.Is(err, ErrMigrationMismatch) {
		t.Errorf("unstamped event = %v, want ErrMigrationMismatch", err)
	}
	events[1].MemberID = "mallory"
	if err := VerifyDualWrite(plan, lines, events); !errors.Is(err, ErrMigrationMismatch) {
		t.Errorf("foreign member event = %v, want ErrMigrationMismatch", err)
	}
	events[1].MemberID = "bob"
	plan.Digest = "tampered"
	if err := VerifyDualWrite(plan, lines, events); !errors.Is(err, ErrMigrationMismatch) {
		t.Errorf("digest mismatch = %v, want ErrMigrationMismatch", err)
	}
}
