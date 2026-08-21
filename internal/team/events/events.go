package events

import "reasonix/internal/team"

// EventKind classifies team events (§3.7).
type EventKind string

const (
	EventMemberState EventKind = "member-state"
	EventReport      EventKind = "report"
	EventWakeup      EventKind = "wakeup"
)

// Event is one bus event. PayloadRef points into the blackboard/content
// space; references travel, never key material (K1).
type Event struct {
	Kind       EventKind
	TaskID     team.TaskID
	MemberID   string // team member id; empty when not member-scoped
	PayloadRef string // content-space reference; empty when none
	TS         string // RFC3339 timestamp
}

// Subscription drains the events delivered to one subscriber.
type Subscription interface {
	C() <-chan Event
	Close()
}

// Bus is the team event bus (§3.7): subscribe by kind, publish typed
// events. Empty kinds subscribe to all. No implementation body this round.
type Bus interface {
	Subscribe(kinds ...EventKind) (Subscription, error)
	Publish(e Event) error
}
