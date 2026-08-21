package scheduler

import (
	"fmt"

	"reasonix/internal/team"
)

// PlaceholderScheduler records assignments without executing them (§3.7):
// it picks the target member by the §3.5 strategy order and returns a
// ledger Assignment whose Note opens with ErrRuntimePending. It causes no
// side effects — fleet and task are read-only inputs.
type PlaceholderScheduler struct{}

// NewPlaceholderScheduler returns the placeholder.
func NewPlaceholderScheduler() *PlaceholderScheduler {
	return &PlaceholderScheduler{}
}

// Assign picks a member and records the assignment. Strategy order (§3.5):
// role match (task.RequireRole, empty = any) → prior-task affinity
// (task.AssignedMember, re-dispatch) → load balance (idle before busy) →
// first candidate. Execution is never claimed: the entry is marked pending
// and the note opens with ErrRuntimePending.
func (s *PlaceholderScheduler) Assign(task team.Task, fleet []team.Member) (Assignment, error) {
	m, ok := pick(task, fleet)
	if !ok {
		return Assignment{}, fmt.Errorf("%w: task %s requires role %q", ErrNoSuitableMember, task.ID, task.RequireRole)
	}
	return Assignment{
		TaskID:   task.ID,
		MemberID: m.ID,
		Status:   StatusPending,
		Note: fmt.Sprintf("%s: %s assigned to %s (role %s, state %s)",
			ErrRuntimePending, task.ID, m.ID, m.Role, m.State),
	}, nil
}

// pick implements the strategy order over a read-only fleet.
func pick(task team.Task, fleet []team.Member) (team.Member, bool) {
	roleMatch := make([]team.Member, 0, len(fleet))
	for _, m := range fleet {
		if task.RequireRole == "" || m.Role == task.RequireRole {
			roleMatch = append(roleMatch, m)
		}
	}
	if len(roleMatch) == 0 {
		return team.Member{}, false
	}
	for _, m := range roleMatch { // prior-task affinity
		if m.ID == task.AssignedMember {
			return m, true
		}
	}
	for _, m := range roleMatch { // load balance: idle before busy
		if m.TaskRef == "" {
			return m, true
		}
	}
	return roleMatch[0], true
}
