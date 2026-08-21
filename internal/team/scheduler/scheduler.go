package scheduler

import (
	"errors"

	"reasonix/internal/team"
)

var (
	// ErrNoSuitableMember reports an assignment with no fleet member whose
	// role matches the task (or any member at all).
	ErrNoSuitableMember = errors.New("scheduler: no suitable member in fleet")
	// ErrRuntimePending is the uniform account for placeholder execution
	// (§3.7): the assignment is recorded, the execution itself is deferred
	// to the runtime stage and must never be claimed.
	ErrRuntimePending = errors.New("scheduler: execution [runtime-pending]")
)

// Scheduler assigns one task to one fleet member (§3.5).
type Scheduler interface {
	Assign(task team.Task, fleet []team.Member) (Assignment, error)
}

// Status is an assignment's recorded state. Only pending exists this round;
// assigned/reported states extend with the runtime stage.
type Status string

const StatusPending Status = "pending"

// Assignment is the scheduling ledger entry: who gets the task, in what
// state, and a human-readable note.
type Assignment struct {
	TaskID   team.TaskID
	MemberID string // fleet member id
	Status   Status
	Note     string
}
