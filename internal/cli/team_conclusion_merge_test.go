package cli

// The merge gate for the shared-conclusion feature: Part A's tools write and
// read the board, and Part B's member reader is now the durable feed over that
// same board. The leader's exclusion is asserted end to end, not by halves.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/team"
)

// TestConclusionMergeMemberDeltaCarriesOnlyTheOtherMember is the join: after m2
// posts through its own tool, m1's durable delta — the reader a member backend is
// assembled with — returns exactly that one line, and the next read is quiet.
func TestConclusionMergeMemberDeltaCarriesOnlyTheOtherMember(t *testing.T) {
	svc, board := conclusionFixture(t)
	post := conclusionToolByName(t, newMemberTaskTools(svc, "alpha", "m2"), postConclusionName)
	if _, err := post.Execute(context.Background(), json.RawMessage(`{"topic":"storage","summary":"use sqlite"}`)); err != nil {
		t.Fatal(err)
	}
	reader := conclusionDeltaReaderFor(board)
	if reader == nil {
		t.Fatal("a live board must yield a reader")
	}
	// m1 joins after the post: its first read only establishes a cursor.
	if text, _, err := reader.Read(context.Background(), "m1"); err != nil || text != "" {
		t.Fatalf("first read = (%q, %v), want a quiet cursor build", text, err)
	}
	if _, err := post.Execute(context.Background(), json.RawMessage(`{"topic":"storage","summary":"files lose transactions"}`)); err != nil {
		t.Fatal(err)
	}
	text, advanceTo, err := reader.Read(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	const want = "[board delta]\n- m2 storage: files lose transactions"
	if text != want {
		t.Fatalf("m1's delta = %q, want exactly one line:\n%s", text, want)
	}
	if err := reader.Ack(context.Background(), "m1", advanceTo); err != nil {
		t.Fatal(err)
	}
	if again, _, err := reader.Read(context.Background(), "m1"); err != nil || again != "" {
		t.Fatalf("the delivered line must not come back: (%q, %v)", again, err)
	}
}

// TestConclusionMergeLeaderStaysUnwiredAndUninformed pins the exclusion that
// makes the feature worth having: a leader's executor carries no board hook at
// all, and nothing the member posted reaches a leader's wait result.
func TestConclusionMergeLeaderStaysUnwiredAndUninformed(t *testing.T) {
	svc, board := conclusionFixture(t)
	post := conclusionToolByName(t, newMemberTaskTools(svc, "alpha", "m2"), postConclusionName)
	const summary = "files lose transactions"
	if _, err := post.Execute(context.Background(), json.RawMessage(`{"topic":"storage","summary":"`+summary+`"}`)); err != nil {
		t.Fatal(err)
	}
	// A leader backend is assembled through the same builder as a member's; the
	// difference the exclusion rests on is the binding's Leader flag.
	leader := memberBuildDeps(t, t.TempDir())
	leader.conclusions = conclusionDeltaReaderFor(board)
	backend, err := newMemberBackendBuilder(leader)(team.MemberBinding{
		Team: "alpha", MemberID: "lead", Leader: true, AgentUserRef: "member-user",
		SessionFile: "alpha/lead.jsonl",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Close)
	if memberExecutorOf(t, backend).BoardDeltaInstalled() {
		t.Fatal("a leader's executor must carry no board delta")
	}

	// The leader's wait is the path a conclusion could leak through, so the
	// posted summary must not appear in its result.
	waitSvc, bus := leaderWaitFixture(t)
	waitSvc.board = board
	wait := leaderWaitToolFrom(t, waitSvc)
	done := make(chan string, 1)
	go func() {
		out, err := wait.Execute(context.Background(), json.RawMessage(`{"timeout_seconds":1200}`))
		if err != nil {
			done <- "error: " + err.Error()
			return
		}
		done <- out
	}()
	waitForWaitSubscriber(t, bus)
	bus.publish(leaderReportEvent("t1"))
	out := <-done
	if strings.Contains(out, summary) {
		t.Fatalf("a member's conclusion leaked into leader_wait:\n%s", out)
	}
	if !strings.Contains(out, "t1") {
		t.Fatalf("the wait must still report the member result it woke for:\n%s", out)
	}
}

// TestConclusionMergeLeaderReadsOnRequestOnly is the other half of the same
// boundary: the leader does get the conclusions, but only from its own tool, and
// that read leaves every member's unread delta intact.
func TestConclusionMergeLeaderReadsOnRequestOnly(t *testing.T) {
	svc, board := conclusionFixture(t)
	post := conclusionToolByName(t, newMemberTaskTools(svc, "alpha", "m2"), postConclusionName)
	if _, err := post.Execute(context.Background(), json.RawMessage(`{"topic":"storage","summary":"use sqlite"}`)); err != nil {
		t.Fatal(err)
	}
	reader := conclusionDeltaReaderFor(board)
	if _, _, err := reader.Read(context.Background(), "m1"); err != nil { // m1 joins
		t.Fatal(err)
	}
	read := conclusionToolByName(t, newLeaderTaskTools(svc, "alpha", "lead"), readConclusionsName)
	out, err := read.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "- m2 storage: use sqlite") {
		t.Fatalf("the leader's read must show the member's conclusion:\n%s", out)
	}
	// m1 has still read nothing: the leader's read is not a delivery.
	post2 := conclusionToolByName(t, newMemberTaskTools(svc, "alpha", "m2"), postConclusionName)
	if _, err := post2.Execute(context.Background(), json.RawMessage(`{"topic":"storage","summary":"revised"}`)); err != nil {
		t.Fatal(err)
	}
	text, _, err := reader.Read(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "- m2 storage: revised") {
		t.Fatalf("m1's delta after a leader read = %q, want the new revision", text)
	}
}
