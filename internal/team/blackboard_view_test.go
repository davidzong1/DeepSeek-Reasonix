package team

import (
	"fmt"
	"strings"
	"testing"
)

func vEvent(seq int64, kind EventKind, task TaskID, member, summary string) BoardEvent {
	return BoardEvent{Seq: seq, Kind: kind, TaskID: task, MemberID: member, Summary: summary}
}

// TestL0BoundedAndTruncated matches route §3.1: the L0 index stays inside
// its 2 KB budget, truncation is flagged, and identical input renders
// identical bytes and digest.
func TestL0BoundedAndTruncated(t *testing.T) {
	b := NewViewBuilder("b1", 1)
	var events []BoardEvent
	for i := int64(1); i <= 200; i++ {
		events = append(events, vEvent(i, EventAssignment, "t1", "m1", strings.Repeat("a", 150)))
	}
	v1 := b.L0(map[string]int{"t1": 200}, events)
	if len(v1.Content) > viewBudgetL0 {
		t.Fatalf("L0 content %d bytes exceeds budget %d", len(v1.Content), viewBudgetL0)
	}
	if !v1.Truncated {
		t.Fatal("L0 with 200 action lines must be truncated")
	}
	if !strings.Contains(v1.Content, "# L0 b1") {
		t.Fatalf("L0 missing header: %q", v1.Content)
	}
	v2 := b.L0(map[string]int{"t1": 200}, events)
	if v2.Content != v1.Content || v2.Digest != v1.Digest {
		t.Fatal("L0 must render byte-identical content and digest for the same input")
	}
}

// TestL0PriorityOrder matches route §3.2's priority: 指令 > 最新结论 > 验收证据
// > 历史报告 — truncation never reorders the surviving lines.
func TestL0PriorityOrder(t *testing.T) {
	b := NewViewBuilder("b1", 1)
	events := []BoardEvent{
		vEvent(1, EventReport, "t1", "m1", "r1"),
		vEvent(2, EventReport, "t2", "m1", "r2"),
		vEvent(3, EventEvidence, "t3", "m1", "e1"),
		vEvent(4, EventConclusion, "t4", "m1", "c1"),
		vEvent(5, EventAssignment, "t5", "m1", "a1"),
	}
	v := b.L0(map[string]int{"t1": 1, "t2": 1, "t3": 1, "t4": 1, "t5": 1}, events)
	idx := func(s string) int {
		i := strings.Index(v.Content, s)
		if i < 0 {
			t.Fatalf("L0 missing %q in %q", s, v.Content)
		}
		return i
	}
	if !(idx("- assignment t5") < idx("- conclusion t4") &&
		idx("- conclusion t4") < idx("- evidence t3") &&
		idx("- evidence t3") < idx("- report t1")) {
		t.Fatalf("L0 action lines not in priority order:\n%s", v.Content)
	}
}

// TestL1DeltaProportional matches route §3.4: L1 cost scales with the delta
// size, never with the board history.
func TestL1DeltaProportional(t *testing.T) {
	b := NewViewBuilder("b1", 1)
	var events []BoardEvent
	for i := int64(1); i <= 100; i++ {
		events = append(events, vEvent(i, EventReport, "t1", "m1", "summary"))
	}
	items := func(v BoardView) int {
		return strings.Count(v.Content, "\n- ")
	}
	full := b.L1(0, events)
	if n := items(full); n != 100 {
		t.Fatalf("full delta must render 100 item lines, got %d", n)
	}
	tail := b.L1(90, events[90:])
	if n := items(tail); n != 10 {
		t.Fatalf("delta of 10 must render 10 item lines, got %d", n)
	}
	if !strings.Contains(tail.Content, "items 10") || strings.Contains(tail.Content, "seq=1 ") {
		t.Fatalf("delta must only cover events after the cursor:\n%s", tail.Content)
	}
}

// TestL1Bounded matches route §3.1: the L1 delta stays inside its 4 KB
// budget and flags truncation.
func TestL1Bounded(t *testing.T) {
	b := NewViewBuilder("b1", 1)
	var events []BoardEvent
	for i := int64(1); i <= 100; i++ {
		events = append(events, vEvent(i, EventReport, "t1", "m1", strings.Repeat("s", 300)))
	}
	v := b.L1(0, events)
	if len(v.Content) > viewBudgetL1 {
		t.Fatalf("L1 content %d bytes exceeds budget %d", len(v.Content), viewBudgetL1)
	}
	if !v.Truncated {
		t.Fatal("L1 with 100 long summaries must be truncated")
	}
}

// TestFoldCheckpoints matches route §3.2: the newest event per task
// survives, all older seqs are listed as superseded, and the fold is sorted
// by task id.
func TestFoldCheckpoints(t *testing.T) {
	events := []BoardEvent{
		vEvent(1, EventConclusion, "t1", "m1", "old"),
		vEvent(2, EventConclusion, "t1", "m1", "new"),
		vEvent(3, EventEvidence, "t2", "m1", "evidence"),
		vEvent(4, EventReport, "t3", "m1", "history"),
	}
	cps := foldCheckpoints(events, 7)
	if len(cps) != 3 || cps[0].TaskID != "t1" || cps[1].TaskID != "t2" || cps[2].TaskID != "t3" {
		t.Fatalf("fold must yield one sorted summary per task: %+v", cps)
	}
	if cps[0].SourceSeq != 2 || cps[0].Kind != EventConclusion || cps[0].Summary != "new" {
		t.Fatalf("t1 fold must keep the newest event: %+v", cps[0])
	}
	if len(cps[0].Supersedes) != 1 || cps[0].Supersedes[0] != 1 {
		t.Fatalf("t1 fold must supersede seq 1: %+v", cps[0].Supersedes)
	}
	if len(cps[1].Supersedes) != 0 || cps[1].Epoch != 7 {
		t.Fatalf("t2 fold must carry its epoch and no supersedes: %+v", cps[1])
	}
}

// TestL2CheckpointEquivalence matches route §3.4: the L2 detail built from
// an earlier fold plus the delta after it is semantically identical to the
// fold of the full event tail.
func TestL2CheckpointEquivalence(t *testing.T) {
	b := NewViewBuilder("b1", 1)
	var events []BoardEvent
	for i := int64(1); i <= 20; i++ {
		if i%2 == 1 {
			events = append(events, vEvent(i, EventConclusion, "t1", "m1", "conclusion"))
		} else {
			events = append(events, vEvent(i, EventEvidence, "t2", "m1", "evidence"))
		}
	}
	vFull := b.L2(foldCheckpoints(events, 1), nil)
	vIncremental := b.L2(foldCheckpoints(events[:15], 1), events[15:])
	if vIncremental.Content != vFull.Content || vIncremental.Digest != vFull.Digest {
		t.Fatalf("checkpoint + delta must equal full fold:\nfull: %q\nincr: %q", vFull.Content, vIncremental.Content)
	}
}

// TestL2BoundedAndSections matches route §3.1/§3.2: the L2 detail stays
// inside its 8 KB budget, flags truncation, and renders the assignments
// section before conclusions.
func TestL2BoundedAndSections(t *testing.T) {
	b := NewViewBuilder("b1", 1)
	var events []BoardEvent
	for i := int64(1); i <= 60; i++ {
		events = append(events, vEvent(i, EventConclusion, TaskID(fmt.Sprintf("t%02d", i)), "m1", strings.Repeat("c", 200)))
	}
	events = append(events, vEvent(61, EventAssignment, "ta", "m1", "assign"))
	v := b.L2(foldCheckpoints(events, 1), nil)
	if len(v.Content) > viewBudgetL2 {
		t.Fatalf("L2 content %d bytes exceeds budget %d", len(v.Content), viewBudgetL2)
	}
	if !v.Truncated {
		t.Fatal("L2 with 61 per-task summaries must be truncated")
	}
	smallEvents := []BoardEvent{
		vEvent(1, EventConclusion, "t1", "m1", "c1"),
		vEvent(2, EventConclusion, "t2", "m1", "c2"),
		vEvent(3, EventAssignment, "ta", "m1", "assign"),
	}
	small := b.L2(foldCheckpoints(smallEvents, 1), nil)
	if i, j := strings.Index(small.Content, "assignments:"), strings.Index(small.Content, "conclusions:"); i < 0 || j < 0 || i > j {
		t.Fatalf("L2 sections must order assignments before conclusions:\n%s", small.Content)
	}
}
