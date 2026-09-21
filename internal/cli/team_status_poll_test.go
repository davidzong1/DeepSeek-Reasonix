package cli

// Leader status-poll throttle (R1): a status answer is ~48 tokens but every
// read re-sends the whole provider prefix. These tests pin the floor, the
// bypass on real change, and the per-member keying.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// pollTeam builds a service with a pinned clock so no test sleeps for the real
// interval.
func pollTeam(t *testing.T) (*teamTaskService, *time.Time) {
	t.Helper()
	svc, _, _ := newConcurrencyTeam(t)
	clock := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return clock }
	return svc, &clock
}

// TestStatusPollFirstReadAlwaysAnswers pins the floor's edge: the very first
// read of a key is never throttled — there is nothing to compare against, and
// suppressing it would leave the leader with no status at all.
func TestStatusPollFirstReadAlwaysAnswers(t *testing.T) {
	svc, _ := pollTeam(t)
	got, err := svc.checkStatus("coder")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "unchanged") {
		t.Fatalf("the first read must answer, got:\n%s", got)
	}
	if !strings.Contains(got, "coder") {
		t.Fatalf("the first read must carry the roster, got:\n%s", got)
	}
}

// TestStatusPollThrottlesIdenticalRepeat is the token contract: a second read
// inside the interval with an unchanged answer returns the throttled reply, and
// that reply repeats the roster verbatim so the provider prefix stays
// byte-stable and only the tail differs.
func TestStatusPollThrottlesIdenticalRepeat(t *testing.T) {
	svc, _ := pollTeam(t)
	first, err := svc.checkStatus("coder")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.checkStatus("coder")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(second, "unchanged (next read in ") {
		t.Fatalf("a repeat inside the interval must be throttled, got:\n%s", second)
	}
	if !strings.HasSuffix(second, first) {
		t.Fatalf("the throttled reply must repeat the roster verbatim:\nfirst=%q\nsecond=%q", first, second)
	}
	if !strings.Contains(second, "60s") {
		t.Fatalf("a read at the same instant must report the full interval, got:\n%s", second)
	}
}

// TestStatusPollReopensAfterInterval pins the floor's other edge: once the
// interval elapsed the read answers again, and the remaining-seconds count
// shrinks as the clock advances so the leader can time its retry.
func TestStatusPollReopensAfterInterval(t *testing.T) {
	svc, clock := pollTeam(t)
	if _, err := svc.checkStatus("coder"); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(30 * time.Second)
	mid, err := svc.checkStatus("coder")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mid, "next read in 30s") {
		t.Fatalf("halfway through the interval must report 30s, got:\n%s", mid)
	}
	*clock = clock.Add(30 * time.Second)
	after, err := svc.checkStatus("coder")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(after, "unchanged") {
		t.Fatalf("a read at the interval boundary must answer, got:\n%s", after)
	}
}

// TestStatusPollBypassesFloorOnRealChange is the latency half of the contract:
// a status that actually moved must reach the leader immediately. Throttling a
// finished member would trade tokens for a leader that waits out the interval.
func TestStatusPollBypassesFloorOnRealChange(t *testing.T) {
	svc, _, backends := newConcurrencyTeam(t)
	clock := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return clock }

	if _, err := svc.checkStatus("coder"); err != nil {
		t.Fatal(err)
	}
	assign := leaderTool(t, newLeaderTaskTools(svc, "alpha", "lead"), "leader_assign_subtask")
	if _, err := assign.Execute(t.Context(), json.RawMessage(`{"member_name":"coder","subtask":"build it"}`)); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if !backends["coder"].Running() {
		t.Fatal("coder must actually be running")
	}
	// Same instant: inside the interval, but the answer moved.
	changed, err := svc.checkStatus("coder")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(changed, "unchanged") {
		t.Fatalf("a changed status must bypass the floor, got:\n%s", changed)
	}
	if !strings.Contains(changed, "working task=") {
		t.Fatalf("the changed read must show the driven task, got:\n%s", changed)
	}
}

// TestStatusPollKeysPerMember pins the keying: one member's poll must not
// throttle another's, or a leader watching two members would only ever see the
// first one move.
func TestStatusPollKeysPerMember(t *testing.T) {
	svc, _ := pollTeam(t)
	if _, err := svc.checkStatus("coder"); err != nil {
		t.Fatal(err)
	}
	other, err := svc.checkStatus("tester")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(other, "unchanged") {
		t.Fatalf("a different member's first read must not be throttled, got:\n%s", other)
	}
	if !strings.Contains(other, "tester") {
		t.Fatalf("the other member's read must carry its row, got:\n%s", other)
	}
}
