// Package events is the team event bus contract (TASK.md §3.7): typed
// events for member-state changes, reports, and wakeups, plus the
// subscribe/publish interfaces. The bus replaces terminal injection as the
// member interaction channel; payloads travel as references into the
// blackboard/content space, never inline material (K1).
//
// This round ships types and interfaces only — no implementation body
// (NG1, TASK.md §3.7). The runtime stage implements Bus against the
// blackboard version stream. Empty kinds in Subscribe means all kinds.
package events
