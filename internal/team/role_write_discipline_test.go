package team

// Acceptance tests for the L4 fix in TEAM_WRITE_LEASE_OPTIMIZATION_ROUTE.md:
// members batch several changes to one file into a single edit call because each
// writing tool call takes the write lease; the text also stays byte-stable.

import (
	"strings"
	"testing"
)

func TestCollaborationDisciplineBatchesWritesToOneFile(t *testing.T) {
	for _, tc := range []struct {
		name       string
		discipline string
		wantAbsent string
	}{
		{name: "member", discipline: CollaborationDiscipline(false)},
		{name: "leader", discipline: CollaborationDiscipline(true)},
		{name: "member prompt", discipline: SystemPromptForRole("alpha", "m2", "tester", false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.discipline, "multi_edit") {
				t.Fatalf("discipline must name the batching tool:\n%s", tc.discipline)
			}
			if !strings.Contains(tc.discipline, "write lease") {
				t.Fatalf("discipline must give the lease as the reason:\n%s", tc.discipline)
			}
			if strings.Contains(tc.discipline, "one file at a time") {
				t.Fatalf("discipline must not forbid the batching it asks for:\n%s", tc.discipline)
			}
		})
	}
}

// TestCollaborationDisciplineStaysByteStable pins the cache-first contract the
// discipline's own comment claims: assembling the same prompt twice yields the
// same bytes, so folding it into a stable prefix cannot churn the cache.
func TestCollaborationDisciplineStaysByteStable(t *testing.T) {
	for _, isLeader := range []bool{false, true} {
		if first, second := CollaborationDiscipline(isLeader), CollaborationDiscipline(isLeader); first != second {
			t.Fatalf("discipline for leader=%v is not byte-stable", isLeader)
		}
	}
	first := SystemPromptForRole("alpha", "m1", "tester", false)
	second := SystemPromptForRole("alpha", "m1", "tester", false)
	if first != second {
		t.Fatal("member prompt is not byte-stable across assemblies")
	}
}
