package team

// The shared-conclusion feed: a member posts one short finding, other members
// read only the delta since their own cursor, and the leader sees nothing it did
// not ask for. Each case pins one of those three contracts at the store level.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func postConclusion(t *testing.T, s *SQLiteStore, member, topic, summary string) Conclusion {
	t.Helper()
	c, err := PostConclusion(context.Background(), s,
		Identity{MemberID: member, Role: "coder", Agent: "claude", Generation: 1},
		"t1", topic, summary)
	if err != nil {
		t.Fatalf("PostConclusion(%s, %s): %v", member, topic, err)
	}
	return c
}

func boardEventCount(t *testing.T, s *SQLiteStore) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM board_events WHERE board_id = ?`, BoardShared).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestConclusionPostRefusesBlankAndOverlongFields pins the write gate: an
// unusable post is refused before anything reaches the board, and an over-cap
// field is never clipped into something that looks stored.
func TestConclusionPostRefusesBlankAndOverlongFields(t *testing.T) {
	s := newTestBoard(t)
	id := Identity{MemberID: "m1", Generation: 1}
	cases := []struct {
		name           string
		topic, summary string
	}{
		{"blank topic", "  ", "a summary"},
		{"blank summary", "a topic", "\t"},
		{"overlong topic", strings.Repeat("t", conclusionTopicMaxRunes+1), "a summary"},
		{"overlong summary", "a topic", strings.Repeat("s", conclusionSummaryMaxRunes+1)},
	}
	for _, tc := range cases {
		if _, err := PostConclusion(context.Background(), s, id, "t1", tc.topic, tc.summary); err == nil {
			t.Errorf("%s: want a refusal", tc.name)
		}
	}
	if n := boardEventCount(t, s); n != 0 {
		t.Fatalf("a refused post wrote %d events", n)
	}
	// At the cap is accepted, so the boundary is inclusive rather than off by one.
	atCap := postConclusion(t, s, "m1", strings.Repeat("t", conclusionTopicMaxRunes), strings.Repeat("s", conclusionSummaryMaxRunes))
	if atCap.Topic != strings.Repeat("t", conclusionTopicMaxRunes) {
		t.Fatalf("at-cap topic was altered: %q", atCap.Topic)
	}
}

// TestConclusionPostRevisesOneTopicPerEpoch pins the CAS revision: a second post
// to the same topic bumps the epoch in place, the board still carries exactly one
// conclusion for it, and the current revision is what readers get.
func TestConclusionPostRevisesOneTopicPerEpoch(t *testing.T) {
	s := newTestBoard(t)
	first := postConclusion(t, s, "m1", "storage", "v1")
	if first.Epoch != 1 || first.Summary != "v1" {
		t.Fatalf("first post = %+v, want epoch 1 summary v1", first)
	}
	second := postConclusion(t, s, "m1", "storage", "v2")
	if second.Epoch != 2 || second.Summary != "v2" {
		t.Fatalf("revision = %+v, want epoch 2 summary v2", second)
	}
	view, err := s.ReadView(context.Background(), BoardShared, ViewSpec{TaskID: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Conclusions) != 1 {
		t.Fatalf("board carries %d conclusions for one topic, want 1", len(view.Conclusions))
	}
	if got := view.Conclusions[0]; got.Epoch != 2 || got.Summary != "v2" || got.MemberID != "m1" {
		t.Fatalf("current revision = %+v, want epoch 2 summary v2 by m1", got)
	}
}

// TestConclusionPostReplayAllocatesNoSeq pins idempotency: an identical
// member+topic+summary replay adds no second seq, while the reported revision is
// the topic's current one rather than the one this call happened to start from.
func TestConclusionPostReplayAllocatesNoSeq(t *testing.T) {
	s := newTestBoard(t)
	postConclusion(t, s, "m1", "storage", "v1")
	postConclusion(t, s, "m1", "storage", "v2")
	after := boardEventCount(t, s)

	replay := postConclusion(t, s, "m1", "storage", "v1")
	if n := boardEventCount(t, s); n != after {
		t.Fatalf("replay wrote %d events, want the %d already there", n, after)
	}
	if replay.Epoch != 2 || replay.Summary != "v2" {
		t.Fatalf("replay reported %+v, want the topic's current revision (epoch 2, v2)", replay)
	}
}

// TestConclusionPostIsNotAWakeup pins the separation the whole feature rests on:
// posting appends a conclusion event and nothing else, so no leader wake path
// can pick a conclusion up.
func TestConclusionPostIsNotAWakeup(t *testing.T) {
	s := newTestBoard(t)
	postConclusion(t, s, "m1", "storage", "v1")
	page, err := s.ReadAfter(context.Background(), BoardShared, 0, Filter{
		Kind: EventWakeup, Stamped: Identity{MemberID: "lead"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 0 {
		t.Fatalf("a conclusion post produced %d wakeup events", len(page.Events))
	}
}

// TestConclusionDeltaFirstReadSkipsHistory pins the join rule: a member with no
// cursor is placed at the board's tail, so it is never handed every conclusion
// posted before it sat down — and that first cursor write is the reader's own,
// which is why it reports nothing to acknowledge.
func TestConclusionDeltaFirstReadSkipsHistory(t *testing.T) {
	s := newTestBoard(t)
	postConclusion(t, s, "m2", "storage", "v1")
	text, advanceTo, err := ReadConclusionDelta(context.Background(), s, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if text != "" || advanceTo != 0 {
		t.Fatalf("first read = (%q, %d), want quiet", text, advanceTo)
	}
	pos, err := s.GetCursor(context.Background(), BoardShared, conclusionConsumer("m1"))
	if err != nil {
		t.Fatal(err)
	}
	if pos.LastSeq != 1 {
		t.Fatalf("first read left the cursor at %d, want the board tail 1", pos.LastSeq)
	}
	// Only what m2 posts after that cursor is delivered.
	postConclusion(t, s, "m2", "storage", "v2")
	text, advanceTo, err = ReadConclusionDelta(context.Background(), s, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if text != "[board delta]\n- m2 storage: v2" || advanceTo != 2 {
		t.Fatalf("second read = (%q, %d), want only the new line at seq 2", text, advanceTo)
	}
}

// TestConclusionDeltaHidesOwnPostsAndStillAdvances pins the self-filter: a
// member's own post never comes back to it, yet the page it was on must still be
// acknowledged or every later step would re-read that same page forever.
func TestConclusionDeltaHidesOwnPostsAndStillAdvances(t *testing.T) {
	s := newTestBoard(t)
	if _, _, err := ReadConclusionDelta(context.Background(), s, "m1"); err != nil {
		t.Fatal(err)
	}
	mine := postConclusion(t, s, "m1", "storage", "v1")
	text, advanceTo, err := ReadConclusionDelta(context.Background(), s, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if text != "" {
		t.Fatalf("own post came back to its author: %q", text)
	}
	if advanceTo != mine.EventSeq {
		t.Fatalf("advanceTo = %d, want the page tail %d so the cursor can pass it", advanceTo, mine.EventSeq)
	}
	if err := AckConclusionDelta(context.Background(), s, "m1", advanceTo); err != nil {
		t.Fatal(err)
	}
	again, againAdvance, err := ReadConclusionDelta(context.Background(), s, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if again != "" || againAdvance != 0 {
		t.Fatalf("after ack = (%q, %d), want quiet", again, againAdvance)
	}
}

// TestConclusionDeltaCapsOnePage pins the page bound: more conclusions than one
// delta may carry arrive in successive reads rather than one oversized block, and
// the cursor moves only as far as each page reached.
func TestConclusionDeltaCapsOnePage(t *testing.T) {
	s := newTestBoard(t)
	if _, _, err := ReadConclusionDelta(context.Background(), s, "m1"); err != nil {
		t.Fatal(err)
	}
	total := conclusionDeltaMaxItems + 3
	for i := range total {
		postConclusion(t, s, "m2", fmt.Sprintf("topic-%02d", i), fmt.Sprintf("summary %d", i))
	}
	first, advanceTo, err := ReadConclusionDelta(context.Background(), s, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(first, "\n- "); n != conclusionDeltaMaxItems {
		t.Fatalf("first page carried %d lines, want %d", n, conclusionDeltaMaxItems)
	}
	if advanceTo != int64(conclusionDeltaMaxItems) {
		t.Fatalf("first page advanceTo = %d, want %d", advanceTo, conclusionDeltaMaxItems)
	}
	if err := AckConclusionDelta(context.Background(), s, "m1", advanceTo); err != nil {
		t.Fatal(err)
	}
	second, secondAdvance, err := ReadConclusionDelta(context.Background(), s, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(second, "\n- "); n != 3 {
		t.Fatalf("second page carried %d lines, want the remaining 3:\n%s", n, second)
	}
	if secondAdvance != int64(total) {
		t.Fatalf("second page advanceTo = %d, want %d", secondAdvance, total)
	}
}

// TestConclusionDeltaAckRefusesBackwards pins the monotonic cursor: an
// acknowledgement may only move forward, and a non-positive target is a no-op
// rather than a reset.
func TestConclusionDeltaAckRefusesBackwards(t *testing.T) {
	s := newTestBoard(t)
	if _, _, err := ReadConclusionDelta(context.Background(), s, "m1"); err != nil {
		t.Fatal(err)
	}
	postConclusion(t, s, "m2", "storage", "v1")
	postConclusion(t, s, "m2", "storage", "v2")
	if err := AckConclusionDelta(context.Background(), s, "m1", 2); err != nil {
		t.Fatal(err)
	}
	if err := AckConclusionDelta(context.Background(), s, "m1", 0); err != nil {
		t.Fatalf("a non-positive ack must be a no-op, got %v", err)
	}
	if err := AckConclusionDelta(context.Background(), s, "m1", 1); !errors.Is(err, ErrCursorBackwards) {
		t.Fatalf("backwards ack = %v, want ErrCursorBackwards", err)
	}
	pos, err := s.GetCursor(context.Background(), BoardShared, conclusionConsumer("m1"))
	if err != nil {
		t.Fatal(err)
	}
	if pos.LastSeq != 2 {
		t.Fatalf("cursor = %d, want it left at 2", pos.LastSeq)
	}
}

// TestConclusionListDoesNotConsumeAMemberDelta pins the isolation between the
// two readers: the leader's read is a snapshot of the current topics, and it
// must leave every member's unread page exactly where it was.
func TestConclusionListDoesNotConsumeAMemberDelta(t *testing.T) {
	s := newTestBoard(t)
	if _, _, err := ReadConclusionDelta(context.Background(), s, "m1"); err != nil {
		t.Fatal(err)
	}
	postConclusion(t, s, "m2", "storage", "v1")
	before, err := s.GetCursor(context.Background(), BoardShared, conclusionConsumer("m1"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ListConclusions(context.Background(), s, Identity{MemberID: "lead"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Topic != "storage" || got[0].MemberID != "m2" {
		t.Fatalf("leader read = %+v, want m2's storage conclusion", got)
	}
	after, err := s.GetCursor(context.Background(), BoardShared, conclusionConsumer("m1"))
	if err != nil {
		t.Fatal(err)
	}
	if after.LastSeq != before.LastSeq {
		t.Fatalf("leader read moved m1's cursor from %d to %d", before.LastSeq, after.LastSeq)
	}
	// The member's own delta still carries the line the leader just read.
	text, _, err := ReadConclusionDelta(context.Background(), s, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if text != "[board delta]\n- m2 storage: v1" {
		t.Fatalf("member delta after a leader read = %q", text)
	}
}

// TestConclusionListBoundsAndRequiresAnIdentity pins the read's own guards: an
// anonymous read is refused, and a limit is bounded so one call cannot pull an
// unbounded board into a leader's context.
func TestConclusionListBoundsAndRequiresAnIdentity(t *testing.T) {
	s := newTestBoard(t)
	if _, err := ListConclusions(context.Background(), s, Identity{}, 0); err == nil {
		t.Fatal("an anonymous read must be refused")
	}
	for i := range conclusionListLimit + 4 {
		postConclusion(t, s, "m2", fmt.Sprintf("topic-%02d", i), "summary")
	}
	got, err := ListConclusions(context.Background(), s, Identity{MemberID: "lead"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != conclusionListLimit {
		t.Fatalf("default limit returned %d conclusions, want the %d cap", len(got), conclusionListLimit)
	}
	if got, err = ListConclusions(context.Background(), s, Identity{MemberID: "lead"}, -1); err != nil || len(got) != conclusionListLimit {
		t.Fatalf("negative limit = (%d conclusions, %v), want the cap", len(got), err)
	}
}

// TestConclusionLineKeepsTopicAndAuthor pins the rendering every reader shares:
// the author and topic are on the line, and an event with no recoverable topic
// still renders as one line rather than a bare colon.
func TestConclusionLineKeepsTopicAndAuthor(t *testing.T) {
	if got := ConclusionLine("m1", "storage", "use sqlite"); got != "- m1 storage: use sqlite" {
		t.Fatalf("line = %q", got)
	}
	if got := ConclusionLine("m1", "", "use sqlite"); got != "- m1: use sqlite" {
		t.Fatalf("topicless line = %q", got)
	}
	// A writer that only had a summary (the blackboard CLI) still round-trips:
	// the whole summary is the body.
	topic, body := conclusionTopicBody("just a summary")
	if topic != "" || body != "just a summary" {
		t.Fatalf("topicless split = (%q, %q)", topic, body)
	}
}
