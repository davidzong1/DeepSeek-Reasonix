package cachelab

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestReplayJournalsReclassifyTheSameRecords re-reads recorded journals and
// reports, per arm, what the current classifier makes of them. It is the
// re-analysis path for a run whose journal was kept: the records hold the
// provider's own numbers as the recorder read them, so re-running this is not a
// re-run of the provider and costs nothing.
//
// It skips when no journal directory is supplied, so it is inert in CI:
//
//	PART_C_JOURNAL_DIR=/tmp/cachelab-runs go test ./internal/cachelab/ -run TestReplayJournals -v
func TestReplayJournalsReclassifyTheSameRecords(t *testing.T) {
	dir := os.Getenv("PART_C_JOURNAL_DIR")
	if dir == "" {
		t.Skip("set PART_C_JOURNAL_DIR to a directory of recorded journals to re-analyse them")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("no journals at %s: %v", dir, err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".jsonl" {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Skipf("no .jsonl files in %s", dir)
	}
	for _, name := range names {
		samples, skipped, err := ReadJournal(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		byArm := ByArm(samples)
		arms := make([]string, 0, len(byArm))
		for arm := range byArm {
			arms = append(arms, arm)
		}
		sort.Strings(arms)
		for _, armID := range arms {
			group := byArm[armID]
			arm, err := ArmByID(armID)
			if err != nil {
				t.Logf("%s arm=%s: not a registered arm, skipped", name, armID)
				continue
			}
			stats := Summarize(arm, group, Prices{})
			oracle, agrees := 0, 0
			for _, s := range group {
				if s.UsageOraclePresent {
					oracle++
					if s.UsageOracleAgrees {
						agrees++
					}
				}
			}
			t.Logf("%s arm=%s samples=%d skipped=%d eligible=%d rate=%.4f hit=%d miss=%d prompt=%d oracle=%d/%d agree=%d",
				name, armID, stats.Samples, skipped, stats.Eligible, stats.Rate,
				stats.HitTokens, stats.MissTokens, stats.PromptTokens, oracle, stats.Samples, agrees)
			for _, s := range group {
				if s.Eligible() {
					continue
				}
				t.Logf("    excluded: seq=%d class=%s status=%d problem=%s err=%q keys=%v",
					s.Seq, s.Classify(), s.Status, s.UsageProblem, s.Error, s.UsageKeys)
			}
		}
	}
}
