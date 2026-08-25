package team

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrMigrationMismatch reports a dual-write divergence (route §6.4):
// cutover and archive refuse to start while legacy and board disagree.
var ErrMigrationMismatch = errors.New("team: blackboard migration mismatch: legacy and board disagree")

// LegacyRecord is one parsed results.jsonl line (route §6.4): the member
// field is self-reported, so it stays unverified until a stamped board
// event confirms the identity.
type LegacyRecord struct {
	TS       time.Time
	Member   string
	Result   string
	Artifact string
	Verified bool
}

// legacyLine mirrors the on-disk results.jsonl shape; identity fields stay
// self-reported, matching what the old writer actually emitted.
type legacyLine struct {
	Timestamp string `json:"timestamp"`
	Member    string `json:"member"`
	Result    string `json:"result"`
	Artifact  string `json:"artifact_path"`
}

// ParseLegacyLine parses one results.jsonl line. A line without a member
// or with a broken timestamp is malformed and never silently imported.
func ParseLegacyLine(line []byte) (LegacyRecord, error) {
	var raw legacyLine
	if err := json.Unmarshal(line, &raw); err != nil {
		return LegacyRecord{}, err
	}
	if raw.Member == "" {
		return LegacyRecord{}, errors.New("team: legacy line missing member")
	}
	ts, err := time.Parse(time.RFC3339Nano, raw.Timestamp)
	if err != nil {
		return LegacyRecord{}, fmt.Errorf("team: legacy line bad timestamp: %w", err)
	}
	return LegacyRecord{TS: ts, Member: raw.Member, Result: raw.Result, Artifact: raw.Artifact}, nil
}

// MigrationPhase names the four-step cutover (route §6.4): import, dual
// write, cutover and archive. Each step is one named state; cutover and
// archive refuse to start while the dual-write verification fails.
type MigrationPhase string

const (
	PhaseImport  MigrationPhase = "import"
	PhaseDual    MigrationPhase = "dual"
	PhaseCutover MigrationPhase = "cutover"
	PhaseArchive MigrationPhase = "archive"
)

// MigrationReport is the dry-run result (route §6.4): counts, the batch
// digest of the raw lines and the consistency flag gating cutover and
// archive. Task linkage is verified from stamped events, never legacy.
type MigrationReport struct {
	Lines      int
	Imported   int
	Failed     int
	Digest     string
	Consistent bool
}

// PlanMigration dry-runs the import (route §6.4): every line parses and
// the batch digest is computed, with nothing written. The same lines must
// reproduce the digest later, or the file changed under the migration.
func PlanMigration(lines [][]byte) (MigrationReport, error) {
	r := MigrationReport{Lines: len(lines)}
	h := sha256.New()
	for _, line := range lines {
		h.Write(line)
		h.Write([]byte{'\n'})
		if _, err := ParseLegacyLine(line); err != nil {
			r.Failed++
			continue
		}
		r.Imported++
	}
	r.Digest = hex.EncodeToString(h.Sum(nil))
	return r, nil
}

// VerifyDualWrite gates cutover and archive (route §6.4): the imported
// count must equal the event count, the batch digest must still match the
// raw lines, every event must carry a stamped member, and each legacy
// member must appear among the events. Any mismatch returns
// ErrMigrationMismatch, so a divergent dual-write window can never be cut
// over; archive re-runs the same gate.
func VerifyDualWrite(plan MigrationReport, lines [][]byte, events []BoardEvent) error {
	if plan.Imported != len(events) {
		return fmt.Errorf("%w: imported %d, board events %d", ErrMigrationMismatch, plan.Imported, len(events))
	}
	h := sha256.New()
	for _, line := range lines {
		h.Write(line)
		h.Write([]byte{'\n'})
	}
	if hex.EncodeToString(h.Sum(nil)) != plan.Digest {
		return fmt.Errorf("%w: legacy file changed since the plan", ErrMigrationMismatch)
	}
	legacy := make(map[string]int)
	for _, line := range lines {
		rec, err := ParseLegacyLine(line)
		if err != nil {
			continue
		}
		legacy[rec.Member]++
	}
	for _, ev := range events {
		if ev.MemberID == "" {
			return fmt.Errorf("%w: unstamped event seq %d", ErrMigrationMismatch, ev.Seq)
		}
		n, ok := legacy[ev.MemberID]
		if !ok {
			return fmt.Errorf("%w: event member %q absent from legacy", ErrMigrationMismatch, ev.MemberID)
		}
		if n == 0 {
			return fmt.Errorf("%w: member %q has more events than legacy lines", ErrMigrationMismatch, ev.MemberID)
		}
		legacy[ev.MemberID] = n - 1
	}
	for member, n := range legacy {
		if n != 0 {
			return fmt.Errorf("%w: member %q has %d unmatched legacy lines", ErrMigrationMismatch, member, n)
		}
	}
	return nil
}
