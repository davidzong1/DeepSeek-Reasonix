package team

// Pure state-machine acceptance for DiscussionDoc: leader-only transitions,
// submit round gate, freeze/clamp, and last-wins idempotency. discussion.go's
// header cites these semantics; the file layer lives in discussion_test.go.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

const semNow = "2026-02-01T00:00:00.000000000Z"

// semDoc builds a started doc; assertions mutate the copy, never a shared one.
func semDoc(t *testing.T, maxRounds int, members ...string) DiscussionDoc {
	t.Helper()
	if len(members) == 0 {
		members = []string{"alice", "bob"}
	}
	d := newDiscussionDoc()
	if err := d.Start("lead", "topic", members, maxRounds, "sem-s1", semNow); err != nil {
		t.Fatalf("start: %v", err)
	}
	return d
}

// TestDiscussionSemanticsStartFreezesAndClamps pins Start's contract: the
// participant slice is snapshotted, the round cap is clamped into [1,3], and a
// running session cannot be started over.
func TestDiscussionSemanticsStartFreezesAndClamps(t *testing.T) {
	members := []string{"alice", "bob", "carol"}
	d := newDiscussionDoc()
	if err := d.Start("lead", "topic", members, 99, "s1", semNow); err != nil {
		t.Fatalf("start: %v", err)
	}
	members[0] = "eve" // mutate the caller's slice after start
	if got := d.Participants; len(got) != 3 || got[0] != "alice" || got[2] != "carol" {
		t.Fatalf("participants must be a frozen snapshot, got %v", got)
	}
	if d.MaxRounds != DiscussionMaxRoundsCap {
		t.Fatalf("maxRounds=99 must clamp to cap %d, got %d", DiscussionMaxRoundsCap, d.MaxRounds)
	}
	d2 := newDiscussionDoc()
	if err := d2.Start("lead", "topic", []string{"a"}, 0, "s2", semNow); err != nil {
		t.Fatalf("start maxRounds=0: %v", err)
	}
	if d2.MaxRounds != 1 {
		t.Fatalf("maxRounds=0 must clamp to floor 1, got %d", d2.MaxRounds)
	}
	if err := d2.Start("lead", "again", []string{"a"}, 1, "s3", semNow); !errors.Is(err, ErrDiscussionActive) {
		t.Fatalf("start while active must refuse with ErrDiscussionActive, got %v", err)
	}
	if err := d2.Start("lead", "", []string{"a"}, 1, "s4", semNow); err == nil {
		t.Fatal("blank topic must be refused")
	}
}

// TestDiscussionSemanticsLeaderOnly pins that Advance and End are leader-only
// transitions; a non-leader member is refused on both regardless of state.
func TestDiscussionSemanticsLeaderOnly(t *testing.T) {
	d := semDoc(t, 3)
	if err := d.Advance("alice", semNow); !errors.Is(err, ErrNotDiscussionLeader) {
		t.Fatalf("non-leader Advance must refuse with ErrNotDiscussionLeader, got %v", err)
	}
	if err := d.End("alice", EndReasonConsensus, "c", "", false, semNow); !errors.Is(err, ErrNotDiscussionLeader) {
		t.Fatalf("non-leader End must refuse with ErrNotDiscussionLeader, got %v", err)
	}
	if d.Status != DiscussionActive || d.Round != 1 {
		t.Fatal("refused transitions must leave the document untouched")
	}
	// A full current round is required before the leader's Advance may move it.
	for _, m := range []string{"alice", "bob"} {
		if err := d.Submit(m, 1, m+" ready", semNow); err != nil {
			t.Fatalf("submit %s: %v", m, err)
		}
	}
	if err := d.Advance("lead", semNow); err != nil {
		t.Fatalf("leader Advance must succeed, got %v", err)
	}
	if d.Round != 2 {
		t.Fatalf("Advance must move to round 2, got %d", d.Round)
	}
}

// TestDiscussionSemanticsSubmitPermissionAndRoundGate pins the member write
// rules: only participants write, only the current round is writable, and an
// advanced round is closed to further writes.
func TestDiscussionSemanticsSubmitPermissionAndRoundGate(t *testing.T) {
	d := semDoc(t, 3)
	if err := d.Submit("mallory", 1, "intruder", semNow); !errors.Is(err, ErrNotParticipant) {
		t.Fatalf("non-participant submit must refuse with ErrNotParticipant, got %v", err)
	}
	if err := d.Submit("alice", 2, "future", semNow); !errors.Is(err, ErrWrongRound) {
		t.Fatalf("submit to a non-current round must refuse with ErrWrongRound, got %v", err)
	}
	if err := d.Submit("alice", 1, "present", semNow); err != nil {
		t.Fatalf("participant submit to current round must succeed, got %v", err)
	}
	if err := d.Submit("bob", 1, "bob present", semNow); err != nil {
		t.Fatalf("second participant must also conclude the round, got %v", err)
	}
	if err := d.Advance("lead", semNow); err != nil {
		t.Fatal(err)
	}
	if err := d.Submit("bob", 1, "late", semNow); !errors.Is(err, ErrWrongRound) {
		t.Fatalf("submit to a closed round must refuse with ErrWrongRound, got %v", err)
	}
	if err := d.Submit("bob", 2, "round two", semNow); err != nil {
		t.Fatalf("participant submit to the new current round must succeed, got %v", err)
	}
}

// TestDiscussionSemanticsIdempotentLastWins pins that a retried or revised
// submission overwrites that member's own (round, member) key instead of
// appending, so the entry count stays one per member per round.
func TestDiscussionSemanticsIdempotentLastWins(t *testing.T) {
	d := semDoc(t, 3)
	for _, text := range []string{"draft one", "revised two"} {
		if err := d.Submit("alice", 1, text, semNow); err != nil {
			t.Fatalf("submit %q: %v", text, err)
		}
	}
	if err := d.Submit("bob", 1, "bob's own", semNow); err != nil {
		t.Fatal(err)
	}
	round := d.Conclusions[1]
	if len(round) != 2 {
		t.Fatalf("two members must produce two entries, got %d (%v)", len(round), round)
	}
	if got := round["alice"].Text; got != "revised two" {
		t.Fatalf("revised submission must replace the member's own entry, got %q", got)
	}
	if got := round["bob"].Text; got != "bob's own" {
		t.Fatalf("one member's revision must not touch another's entry, got %q", got)
	}
}

// TestDiscussionSemanticsEndGatesAndDeposit pins End's transition rules and the
// deposit marker: advance past the cap is refused, invalid reasons are refused,
// inactive sessions cannot end, deposit=false leaves no marker, and deposit=true
// with a body creates a deterministic pending marker that flips to delivered
// exactly once.
func TestDiscussionSemanticsEndGatesAndDeposit(t *testing.T) {
	d := semDoc(t, 1)
	if err := d.Advance("lead", semNow); !errors.Is(err, ErrRoundMaxed) {
		t.Fatalf("Advance at the round cap must refuse with ErrRoundMaxed, got %v", err)
	}
	if err := d.End("lead", DiscussionEndReason("bogus"), "c", "", false, semNow); err == nil {
		t.Fatal("invalid end reason must be refused")
	}
	d2 := semDoc(t, 3)
	if err := d2.End("lead", EndReasonConsensus, "decision: files", "route: CAS", false, semNow); err != nil {
		t.Fatalf("end without deposit: %v", err)
	}
	if d2.Status != DiscussionEnded || d2.Deposit != nil {
		t.Fatalf("end deposit=false must leave no marker, got %+v", d2.Deposit)
	}
	if err := d2.Submit("alice", 1, "too late", semNow); !errors.Is(err, ErrDiscussionNotActive) {
		t.Fatalf("submit to an ended session must refuse with ErrDiscussionNotActive, got %v", err)
	}
	if err := d2.End("lead", EndReasonConsensus, "again", "", false, semNow); !errors.Is(err, ErrDiscussionNotActive) {
		t.Fatalf("double End must refuse with ErrDiscussionNotActive, got %v", err)
	}

	d3 := semDoc(t, 3)
	body := "讨论主题: topic\n参与成员: alice, bob\n\n共识: decision: adopt\n技术路线: route: CAS"
	if err := d3.End("lead", EndReasonConsensus, "decision: adopt", "route: CAS", true, semNow); err != nil {
		t.Fatalf("end with deposit: %v", err)
	}
	if !d3.DepositPending() || d3.Deposit == nil || d3.Deposit.Status != DiscussionDepositPending {
		t.Fatalf("ended with deposit=true must leave a pending marker, got %+v", d3.Deposit)
	}
	if got := d3.Deposit.Text; got != body {
		t.Fatalf("deposit text must be deterministic, got %q want %q", got, body)
	}
	sum := sha256.Sum256([]byte(body))
	if d3.Deposit.Hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("deposit hash must be sha256 of the body, got %q", d3.Deposit.Hash)
	}
	if err := d3.MarkDeposited(semNow); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
	if d3.DepositPending() || d3.Deposit.Status != DiscussionDepositDelivered {
		t.Fatalf("mark must flip the marker to delivered, got %+v", d3.Deposit)
	}
	if err := d3.MarkDeposited(semNow); !errors.Is(err, ErrInvalidDeposit) {
		t.Fatalf("double mark must refuse with ErrInvalidDeposit, got %v", err)
	}
	if err := d3.Start("lead", "next", []string{"alice"}, 2, "s2", semNow); err != nil {
		t.Fatalf("start after delivery must succeed, got %v", err)
	}
	if !strings.Contains(d3.Summary(), "1/2") {
		t.Fatalf("summary must reflect the new session round, got:\n%s", d3.Summary())
	}
}

// TestDiscussionSemanticsAdvanceNeedsCollectedRound pins the frozen matrix gate
// (推进轮次需本轮已收齐/达共识/达 cap): a round advances only once every
// participant has concluded it, while the cap and End escape hatches stay open.
func TestDiscussionSemanticsAdvanceNeedsCollectedRound(t *testing.T) {
	d := semDoc(t, 3) // alice, bob
	if err := d.Advance("lead", semNow); !errors.Is(err, ErrRoundIncomplete) {
		t.Fatalf("Advance with no conclusions must refuse with ErrRoundIncomplete, got %v", err)
	}
	if err := d.Submit("alice", 1, "alice ready", semNow); err != nil {
		t.Fatal(err)
	}
	if err := d.Advance("lead", semNow); !errors.Is(err, ErrRoundIncomplete) {
		t.Fatalf("Advance with one member pending must refuse with ErrRoundIncomplete, got %v", err)
	}
	if err := d.Submit("bob", 1, "bob ready", semNow); err != nil {
		t.Fatal(err)
	}
	if err := d.Advance("lead", semNow); err != nil {
		t.Fatalf("Advance with a full round must succeed, got %v", err)
	}
	if d.Round != 2 {
		t.Fatalf("Advance must move to round 2, got %d", d.Round)
	}
	// The cap and End remain reachable even when the round is not full.
	d2 := semDoc(t, 1)
	if err := d2.Advance("lead", semNow); !errors.Is(err, ErrRoundMaxed) {
		t.Fatalf("Advance at the cap must refuse with ErrRoundMaxed, got %v", err)
	}
	if err := d2.End("lead", EndReasonNoIdle, "", "", false, semNow); err != nil {
		t.Fatalf("End must stay open with an incomplete round, got %v", err)
	}
}
