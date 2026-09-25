// Part B (§B.2 item 4 of the one-shot plan): the `messages` reason must be
// reachable from a shape the producer really emits, not only from a hand-built
// one. A consumer class keyed on a shape the producer never emits still passes
// its unit test while being unreachable on real data — a mistake made once here.
//
// The chain below is production end to end: a real member agent's request → the
// real observation sink → memberUsagePublisher → the owner store's request log →
// the real consumer, team.CacheMissCauseOf.
package cli

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/agent/testutil"
	"reasonix/internal/cachereason"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/team"
	"reasonix/internal/tool"
)

// TestUnclaimedArrayRewriteReachesItsOwnCauseClass drives one member session
// through the producer, the publisher and the consumer, with nothing in the
// middle replaced by a fixture except the scripted provider.
//
// The rewrite goes through Session.Replace, the API every unclaimed production
// mutate site uses: a history sync, an interrupt recovery, a projection restore,
// a guardian merge, a fork continuation and a planner rollback all replace the
// array without queueing a reason. The Part B result doc enumerates those call
// sites; what this test fixes in place is that such a rewrite, produced for real,
// arrives at the consumer carrying exactly the vocabulary value the consumer
// splits on.
func TestUnclaimedArrayRewriteReachesItsOwnCauseClass(t *testing.T) {
	const system = "You are member lead of the team.\nWork in the shared workspace."
	ctx := t.Context()

	owners, err := team.NewOwnerStore(filepath.Join(t.TempDir(), "team"))
	if err != nil {
		t.Fatalf("owner store: %v", err)
	}
	key := team.OwnerKey{TeamID: "alpha", MemberID: "lead"}
	publisher := newMemberUsagePublisher(owners, key, "openai/deepseek-v4.1-flash")
	if publisher == nil {
		t.Fatal("a publisher with an owner store must exist")
	}
	// Sink allocates the request queue and Start drains it, which is the order
	// the member builder installs them in.
	sink := publisher.Sink(event.Discard)
	publisher.Start()
	defer publisher.Close()

	usage := func(prompt int) *provider.Usage {
		return &provider.Usage{
			PromptTokens: prompt, ContextPromptTokens: prompt, CacheMissTokens: prompt,
			CompletionTokens: 4, TotalTokens: prompt + 4,
			RequestCount: 1, RequestCountObserved: true,
		}
	}
	prov := testutil.NewMock("member",
		testutil.Turn{Text: "first answer", Usage: usage(400)},
		testutil.Turn{Text: "second answer", Usage: usage(420)},
		testutil.Turn{Text: "spare answer", Usage: usage(440)},
	)
	sess := agent.NewSession(system)
	ag := agent.New(prov, tool.NewRegistry(), sess, agent.Options{}, sink)

	if err := ag.Run(ctx, "first task"); err != nil {
		t.Fatalf("first turn: %v", err)
	}

	// The unclaimed rewrite restores the conversation to the session's opening
	// shape, dropping a message the provider already read: a rollback of a failed
	// tail, as an interrupt recovery or a history sync does it.
	msgs := sess.Snapshot()
	if len(msgs) < 2 {
		t.Fatalf("the first turn left %d messages, want at least system + user: %+v", len(msgs), msgs)
	}
	sess.Replace(append([]provider.Message(nil), msgs[:1]...))

	if err := ag.Run(ctx, "second task"); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if got := prov.CallCount(); got != 2 {
		t.Fatalf("the provider saw %d requests, want one per turn: the second record is only the post-rewrite one if the turns are 1:1", got)
	}

	records := waitForPublishedRequests(t, owners, key, 2)
	first, last := records[0], records[len(records)-1]

	// The control: the classifier is not blanket-returning the new class. The
	// writer's first request is a cold prefix, and it must still read as one.
	if got := team.CacheMissCauseOf(first); got != team.CacheCauseColdPrefix {
		t.Errorf("the writer's first request classified as %q, want %q", got, team.CacheCauseColdPrefix)
	}

	if !last.DiagnosticsAvailable {
		t.Fatalf("the post-rewrite request carried no diagnosis: %+v", last)
	}
	if !slices.Equal(last.PrefixChangeReasons, []string{cachereason.Messages}) {
		t.Fatalf("reasons = %v, want exactly [%s]: the producer must name an unclaimed array rewrite, "+
			"which is the shape this class is defined on", last.PrefixChangeReasons, cachereason.Messages)
	}
	if !last.MessagesComparable {
		t.Error("the request rewrote the array but reports no comparable predecessor")
	}
	if last.MessagesRewritten != 1 {
		t.Errorf("MessagesRewritten = %d, want 1 (the sent message the restored shape did not reuse)", last.MessagesRewritten)
	}
	if last.FirstDivergenceOffset != 0 {
		t.Errorf("FirstDivergenceOffset = %d, want 0 (the first conversation message, system excluded)", last.FirstDivergenceOffset)
	}
	// The point of the whole Part B change: system and tools never moved, so the
	// pre-existing diagnosis had nothing to report even though the provider had
	// to re-read a message it had already seen.
	if last.StablePrefixChanged {
		t.Error("the stable prefix was expected to stay put; this test is about the array, not the surface")
	}
	if got := team.CacheMissCauseOf(last); got != team.CacheCauseMessagesRewritten {
		t.Errorf("the unclaimed rewrite classified as %q, want %q: the consumer must split %s away from the "+
			"rewrites an operation claimed", got, team.CacheCauseMessagesRewritten, cachereason.Messages)
	}
}

// waitForPublishedRequests waits for the writer's queue to drain into the log,
// the way a reader of a live member observes it. The publisher records on its own
// goroutine, so the log trails the turn by design rather than by accident.
func waitForPublishedRequests(t *testing.T, owners *team.OwnerStore, key team.OwnerKey, want int) []team.MemberCacheRequest {
	t.Helper()
	var last []team.MemberCacheRequest
	for range 100 {
		recs, err := owners.ReadCacheRequests(context.Background(), key)
		if err != nil {
			t.Fatalf("ReadCacheRequests: %v", err)
		}
		last = recs
		if len(recs) >= want {
			time.Sleep(10 * time.Millisecond)
			if relaxed, err := owners.ReadCacheRequests(context.Background(), key); err == nil && len(relaxed) > len(recs) {
				last = relaxed
			}
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the writer published %d requests, want at least %d: %+v", len(last), want, last)
	return last
}
