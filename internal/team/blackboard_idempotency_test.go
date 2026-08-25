package team

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestBlackboardBatchReplayIdempotent matches route §2.2: replaying the
// same batch returns the same seqs and stores nothing new.
func TestBlackboardBatchReplayIdempotent(t *testing.T) {
	s := newTestBoard(t)
	mk := func(i int) AppendInput {
		return AppendInput{
			BoardID: BoardShared, ClientMsgID: fmt.Sprintf("b-%d", i), Kind: EventReport,
			TaskID: "t", CreatedAt: time.Now().UTC(), Summary: fmt.Sprintf("s%d", i),
			Stamped: Identity{MemberID: "m", Generation: 1},
		}
	}
	var batch []AppendInput
	for i := 0; i < 5; i++ {
		batch = append(batch, mk(i))
	}
	first, err := s.AppendBatch(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AppendBatch(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	for i := range first {
		if first[i].Seq != second[i].Seq {
			t.Fatalf("replay changed seq of %d: %d -> %d", i, first[i].Seq, second[i].Seq)
		}
	}
	page, err := s.ReadAfter(context.Background(), BoardShared, 0,
		Filter{Limit: 100, Stamped: Identity{MemberID: "m", Generation: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 5 {
		t.Fatalf("stored %d events after replay, want 5", len(page.Events))
	}
}

// TestBlackboardReplayKeepsOriginalConclusion: a replayed msg_id carrying
// different content returns the original event and leaves the conclusion
// revision untouched (route §2.2).
func TestBlackboardReplayKeepsOriginalConclusion(t *testing.T) {
	s := newTestBoard(t)
	in := AppendInput{
		BoardID: BoardShared, ClientMsgID: "c1", Kind: EventConclusion, TaskID: "t",
		CreatedAt: time.Now().UTC(), Summary: "original",
		Stamped:    Identity{MemberID: "m", Generation: 1},
		Conclusion: &ConclusionUpdate{Topic: "topic1", BaseEpoch: 0, Summary: "v1"},
	}
	first, err := s.Append(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	replay := in
	replay.Summary = "mutated"
	replay.Conclusion = &ConclusionUpdate{Topic: "topic1", BaseEpoch: 0, Summary: "v2-mutated"}
	second, err := s.Append(context.Background(), replay)
	if err != nil {
		t.Fatal(err)
	}
	if second.Seq != first.Seq {
		t.Fatalf("replay allocated new seq %d, want %d", second.Seq, first.Seq)
	}
	if second.Summary != "original" {
		t.Fatalf("replay rewrote event summary: %q", second.Summary)
	}
	view, err := s.ReadView(context.Background(), BoardShared, ViewSpec{TaskID: "t"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range view.Conclusions {
		if c.Topic == "topic1" {
			found = true
			if c.Summary != "v1" {
				t.Fatalf("replay rewrote conclusion: %q", c.Summary)
			}
		}
	}
	if !found {
		t.Fatal("conclusion missing from view")
	}
}

// TestBlackboardSupersedeReplayNoDoubleChain: replaying a supersede with
// the same client_msg_id publishes no second revision — the chain length
// stays one per unique msg_id (route §1.1 audit chain).
func TestBlackboardSupersedeReplayNoDoubleChain(t *testing.T) {
	s := newTestBoard(t)
	ev, err := boardAppend(s, "orig-1", "m", 1)
	if err != nil {
		t.Fatal(err)
	}
	replacement := AppendInput{
		ClientMsgID: "sup-1", Kind: EventSupersede, TaskID: "t",
		CreatedAt: time.Now().UTC(), Summary: "rev",
		Stamped: Identity{MemberID: "m", Generation: 1},
	}
	first, err := s.Supersede(context.Background(), BoardShared, []int64{ev.Seq}, replacement)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Supersede(context.Background(), BoardShared, []int64{ev.Seq}, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if second.Seq != first.Seq {
		t.Fatalf("replayed supersede got new seq %d, want %d", second.Seq, first.Seq)
	}
	page, err := s.ReadAfter(context.Background(), BoardShared, 0,
		Filter{Limit: 100, Stamped: Identity{MemberID: "m", Generation: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("stored %d events, want 2 (original + one supersede)", len(page.Events))
	}
}
