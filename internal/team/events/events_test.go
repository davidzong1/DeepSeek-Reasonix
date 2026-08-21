package events

import (
	"testing"

	"reasonix/internal/team"
)

// fakeSubscription and fakeBus pin the interface shape: no implementation
// body ships this round (§3.7), so the contract is compile-time only.
type fakeSubscription struct{ ch chan Event }

func (s *fakeSubscription) C() <-chan Event { return s.ch }
func (s *fakeSubscription) Close()          {}

var _ Subscription = (*fakeSubscription)(nil)

type fakeBus struct{}

func (fakeBus) Subscribe(kinds ...EventKind) (Subscription, error) {
	return &fakeSubscription{}, nil
}
func (fakeBus) Publish(e Event) error { return nil }

var _ Bus = fakeBus{}

func TestEventKinds(t *testing.T) {
	got := []EventKind{EventMemberState, EventReport, EventWakeup}
	want := []EventKind{"member-state", "report", "wakeup"}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("kind %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestEventCarriesReferencesNotPayloads(t *testing.T) {
	e := Event{
		Kind:       EventReport,
		TaskID:     team.TaskID("t1"),
		MemberID:   "m1",
		PayloadRef: "blackboard/rev/3",
		TS:         "2026-08-21T00:00:00Z",
	}
	if e.PayloadRef != "blackboard/rev/3" || e.TaskID != "t1" {
		t.Fatalf("event fields not carried: %+v", e)
	}
}
