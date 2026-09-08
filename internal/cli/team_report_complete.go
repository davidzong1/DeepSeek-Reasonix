package cli

import (
	"context"
	"errors"
	"fmt"

	"reasonix/internal/team"
	"reasonix/internal/team/agentruntime"
)

// translateCompleteError turns a refused runtime.Complete into a member
// answer. Complete answers ErrTaskUnknown when its registry entry vanished
// between pickReportTarget's driving check and claimTerminal — the TOCTOU
// window a concurrent report or cancel wins. The durable row then decides the
// message: terminal or gone means the task was already closed elsewhere; a
// live row nothing drives now is the undriven shape whose fix is the leader's.
// Everything else passes through — store and transition failures are
// host-side, not member-fixable.
func (s *teamTaskService) translateCompleteError(target *team.Task, err error) error {
	if !errors.Is(err, agentruntime.ErrTaskUnknown) {
		return err
	}
	if s.board == nil {
		return fmt.Errorf("task %s changed state while this report was in flight: report again or ask the leader", target.ID)
	}
	row, loadErr := s.board.LoadTask(context.Background(), target.ID)
	if loadErr != nil && !errors.Is(loadErr, team.ErrTaskNotFound) {
		return fmt.Errorf("task %s changed state while this report was in flight and its record could not be re-read: ask the leader for its status", target.ID)
	}
	if errors.Is(loadErr, team.ErrTaskNotFound) {
		return fmt.Errorf("task %s was already closed (its record is gone) before this report landed: no change was made", target.ID)
	}
	switch row.Status {
	case team.TaskStatusReported, team.TaskStatusCanceled, team.TaskStatusArchived:
		return fmt.Errorf("task %s was already closed (recorded %s) before this report landed: a concurrent report or cancel won — no change was made", row.ID, row.Status)
	case team.TaskStatusFailed:
		return undrivenTaskError(&row)
	}
	// A retry re-drove the row between the drop and this read — report again
	// now that it is executing.
	if s.driving(row.ID) {
		return fmt.Errorf("task %s changed state while this report was in flight: report again", row.ID)
	}
	return undrivenTaskError(&row)
}
